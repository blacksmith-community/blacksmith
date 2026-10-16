//nolint:testpackage // the tests resume stored release upgrades and read the manager's unexported state
package upgrade

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/pkg/logger"
)

const (
	// redisForge is the forge release of the test deployments, and
	// redisForgeTarget the version the release upgrades move it to.
	redisForge       = "redis-forge"
	redisForgeTarget = "9.9.9"
	// rabbitmqForge is a release the director holds that no test
	// deployment runs.
	rabbitmqForge = "rabbitmq-forge"
	// deployedBPMVersion is the bpm version every test deployment runs.
	deployedBPMVersion = "1.4.20"
	// deployedStemcellVersion is the stemcell version every test
	// deployment runs.
	deployedStemcellVersion = "1.1"
)

// releaseDirector is a resumeDirector whose deployments run the redis
// forge and bpm releases, and which keeps every manifest it deploys. It
// holds redis-forge 9.9.9 and 9.9.8, bpm 1.4.20, and rabbitmq-forge
// 2.0.0, less any version the test withdraws.
type releaseDirector struct {
	*resumeDirector

	mu        sync.Mutex
	manifests map[string][]string
	// abandoned names the deployments whose deploy task Blacksmith stops
	// following, so the instance's outcome is never known.
	abandoned map[string]bool
	// withdrawn names the "name/version" releases the director no longer
	// holds.
	withdrawn map[string]bool
}

func newReleaseDirector() *releaseDirector {
	return &releaseDirector{
		resumeDirector: newResumeDirector(),
		manifests:      make(map[string][]string),
		abandoned:      make(map[string]bool),
		withdrawn:      make(map[string]bool),
	}
}

func (d *releaseDirector) GetReleases(_ context.Context) ([]bosh.Release, error) {
	held := map[string][]string{
		redisForge:    {redisForgeTarget, "9.9.8"},
		bpmRelease:    {deployedBPMVersion},
		rabbitmqForge: {"2.0.0"},
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	releases := make([]bosh.Release, 0, len(held))

	for _, name := range []string{redisForge, bpmRelease, rabbitmqForge} {
		release := bosh.Release{Name: name}

		for _, version := range held[name] {
			if !d.withdrawn[name+"/"+version] {
				release.ReleaseVersions = append(release.ReleaseVersions, bosh.ReleaseVersion{Version: version})
			}
		}

		releases = append(releases, release)
	}

	return releases, nil
}

func (d *releaseDirector) GetDeployment(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
	manifest := fmt.Sprintf(`name: %s
releases:
- name: %s
  version: latest
- name: bpm
  version: %q
  url: https://example.test/bpm
  sha1: sha256:abc123
stemcells:
- alias: default
  os: ubuntu-noble
  version: %q
`, name, redisForge, deployedBPMVersion, deployedStemcellVersion)

	return &bosh.DeploymentDetail{Name: name, Manifest: manifest}, nil
}

func (d *releaseDirector) UpdateDeployment(ctx context.Context, name, manifest string) (*bosh.Task, error) {
	d.mu.Lock()
	d.manifests[name] = append(d.manifests[name], manifest)
	d.mu.Unlock()

	return d.resumeDirector.UpdateDeployment(ctx, name, manifest)
}

func (d *releaseDirector) WaitForTask(ctx context.Context, taskID int) (*bosh.Task, error) {
	d.resumeDirector.mu.Lock()
	deployment := d.deployments[taskID]
	d.resumeDirector.mu.Unlock()

	d.mu.Lock()
	abandoned := d.abandoned[deployment]
	d.mu.Unlock()

	if abandoned {
		return nil, fmt.Errorf("%w: the deploy of %s", bosh.ErrWatchAbandoned, deployment)
	}

	return d.resumeDirector.WaitForTask(ctx, taskID)
}

// abandon makes the watch of each deploy of the deployment end before
// the deploy's outcome is known.
func (d *releaseDirector) abandon(deployment string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.abandoned[deployment] = true
}

// withdrawTarget removes the redis forge target from the director's
// releases.
func (d *releaseDirector) withdrawTarget() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.withdrawn[redisForge+"/"+redisForgeTarget] = true
}

// deployed returns the manifest of the only deploy of the deployment, and
// fails the test unless there was exactly one.
func (d *releaseDirector) deployed(t *testing.T, deployment string) string {
	t.Helper()

	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.manifests[deployment]) != 1 {
		t.Fatalf("expected one deploy of %s, got %d", deployment, len(d.manifests[deployment]))
	}

	return d.manifests[deployment][0]
}

// releaseUpgrade returns a stored running upgrade of the instances to the
// redis forge target, with no stemcell target.
func releaseUpgrade(instances ...InstanceUpgrade) UpgradeTask {
	task := storedUpgrade(loadedTaskID, instances...)
	task.TargetStemcell = StemcellTarget{}
	task.TargetReleases = []ReleaseTarget{{Name: redisForge, Version: redisForgeTarget}}

	return task
}

// expectReleaseBumped fails the test unless the manifest runs the redis
// forge target, keeps bpm as it was, and runs the stemcell version given.
func expectReleaseBumped(t *testing.T, manifest, stemcellVersion string) {
	t.Helper()

	if version := releaseVersion(t, manifest, redisForge); version != redisForgeTarget {
		t.Fatalf("expected %s at %s, got %q in:\n%s", redisForge, redisForgeTarget, version, manifest)
	}

	if version := releaseVersion(t, manifest, bpmRelease); version != deployedBPMVersion {
		t.Fatalf("expected bpm to stay at %s, got %q in:\n%s", deployedBPMVersion, version, manifest)
	}

	os, version, err := GetCurrentStemcell(manifest)
	if err != nil || os != targetStemcellOS || version != stemcellVersion {
		t.Fatalf("expected stemcell %s/%s, got %s/%s (%v) in:\n%s", targetStemcellOS, stemcellVersion, os, version, err, manifest)
	}
}

//nolint:paralleltest // the resumed upgrade waits on a shared test director
func TestReleaseUpgradeDeploysTheBumpedReleaseAndKeepsTheStemcell(t *testing.T) {
	director := newReleaseDirector()
	vault := newWaitVault()

	storeUpgrade(t, vault, releaseUpgrade(pendingInstance("instance-1", firstDeployment)))

	manager := loadManager(t, director, vault)

	done := expectCompleted(t, manager, loadedTaskID)
	if done.CompletedCount != 1 {
		t.Fatalf("expected the instance to succeed, got %+v", done.Instances)
	}

	if applied := done.Instances[0].AppliedReleases; !slices.Equal(applied, releaseUpgrade().TargetReleases) {
		t.Fatalf("expected the instance to record the forge release it applied, got %+v", applied)
	}

	expectReleaseBumped(t, director.deployed(t, firstDeployment), deployedStemcellVersion)
}

//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestStemcellAndReleaseUpgradeAppliesBothTargets(t *testing.T) {
	director := newReleaseDirector()
	vault := newWaitVault()

	stored := releaseUpgrade(pendingInstance("instance-1", firstDeployment))
	stored.TargetStemcell = StemcellTarget{OS: targetStemcellOS, Version: targetStemcellVersion}
	storeUpgrade(t, vault, stored)

	manager := loadManager(t, director, vault)

	done := expectCompleted(t, manager, loadedTaskID)
	if done.CompletedCount != 1 {
		t.Fatalf("expected the instance to succeed, got %+v", done.Instances)
	}

	expectReleaseBumped(t, director.deployed(t, firstDeployment), targetStemcellVersion)
}

//nolint:paralleltest // the resumed upgrade waits on a shared test director
func TestReleaseUpgradeSkipsAnInstanceWithoutTheRelease(t *testing.T) {
	director := newReleaseDirector()
	vault := newWaitVault()

	stored := releaseUpgrade(pendingInstance("instance-1", firstDeployment))
	stored.TargetReleases = []ReleaseTarget{{Name: rabbitmqForge, Version: "2.0.0"}}
	storeUpgrade(t, vault, stored)

	manager := loadManager(t, director, vault)

	done := expectCompleted(t, manager, loadedTaskID)

	skipped := done.Instances[0]
	if skipped.Status != InstanceStatusSkipped || done.SkippedCount != 1 || skipped.CompletedAt == nil {
		t.Fatalf("expected the instance to be skipped, got %+v", done)
	}

	if len(skipped.AppliedReleases) != 0 {
		t.Fatalf("expected the skipped instance to record no applied release, got %+v", skipped.AppliedReleases)
	}

	for _, part := range []string{"rabbitmq-forge/2.0.0", firstDeployment} {
		if !strings.Contains(skipped.Error, part) {
			t.Fatalf("expected the skip to name %q, got %q", part, skipped.Error)
		}
	}

	if deploys := director.allDeploys(); len(deploys) != 0 {
		t.Fatalf("expected no deploy, got %v", deploys)
	}
}

func TestCopyTaskCopiesTheReleaseTargets(t *testing.T) {
	t.Parallel()

	task := releaseUpgrade(pendingInstance("instance-1", firstDeployment))
	task.Instances[0].AppliedReleases = slices.Clone(task.TargetReleases)

	copied := copyTask(&task)
	copied.TargetReleases[0].Version = "0.0.1"
	copied.Instances[0].AppliedReleases[0].Version = "0.0.1"

	if task.TargetReleases[0].Version != redisForgeTarget {
		t.Fatalf("expected the copy to share no release targets with the task, got %+v", task.TargetReleases)
	}

	if task.Instances[0].AppliedReleases[0].Version != redisForgeTarget {
		t.Fatalf("expected the copy to share no applied releases with the task, got %+v", task.Instances[0].AppliedReleases)
	}
}

// A broker that stops while a release upgrade deploys its first instance
// saves the upgrade with its targets, and the next broker settles that
// deploy and deploys the second instance with the same targets.
//
//nolint:paralleltest // the resumed upgrades wait on shared test directors
func TestReleaseUpgradeSurvivesARestartMidTask(t *testing.T) {
	vault := newWaitVault()

	storeUpgrade(t, vault, releaseUpgrade(
		pendingInstance("instance-1", firstDeployment),
		pendingInstance("instance-2", nextDeployment)))

	before := newReleaseDirector()
	before.holdDeployment(firstDeployment)

	first := NewManager(&logger.NoOpLogger{}, before, vault)
	first.Start()
	waitForLoad(t, first)
	before.waitForDeployOf(t, firstDeployment)
	before.waitForWaitOn(t, 101)
	first.Stop()

	saved := vault.savedTask(t, loadedTaskID)
	if saved.Instances[0].Status != InstanceStatusInterrupted || !slices.Equal(saved.TargetReleases, releaseUpgrade().TargetReleases) {
		t.Fatalf("expected the stopped broker to save the interrupted instance and the release targets, got %+v", saved)
	}

	after := newReleaseDirector()
	manager := loadManager(t, after, vault)

	done := expectCompleted(t, manager, loadedTaskID)
	if done.CompletedCount != 2 {
		t.Fatalf("expected both instances to succeed, got %+v", done.Instances)
	}

	if deploys := after.allDeploys(); !slices.Equal(deploys, []string{nextDeployment}) {
		t.Fatalf("expected the next broker to deploy only the second instance, got %v", deploys)
	}

	expectReleaseBumped(t, after.deployed(t, nextDeployment), deployedStemcellVersion)
}

// A release upgrade cancelled while its first instance deploys cancels
// that deploy, skips the second instance, and keeps its targets.
//
//nolint:paralleltest // the resumed upgrade waits on a shared test director
func TestCancelledReleaseUpgradeKeepsItsTargets(t *testing.T) {
	director := newReleaseDirector()
	director.holdDeployment(firstDeployment)

	vault := newWaitVault()

	storeUpgrade(t, vault, releaseUpgrade(
		pendingInstance("instance-1", firstDeployment),
		pendingInstance("instance-2", nextDeployment)))

	manager := loadManager(t, director, vault)

	director.waitForWaitOn(t, 101)

	err := manager.CancelTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("cancelling the upgrade: %v", err)
	}

	done := eventuallyWithin(t, manager, loadedTaskID, waitTimeout, "the cancel to settle", func(current UpgradeTask) bool {
		return current.SkippedCount == 1 && current.Instances[0].Status.settled()
	})

	if done.Status != TaskStatusCancelled || done.Instances[1].Status != InstanceStatusSkipped {
		t.Fatalf("expected the upgrade cancelled with its second instance skipped, got %+v", done)
	}

	if !slices.Equal(done.TargetReleases, releaseUpgrade().TargetReleases) {
		t.Fatalf("expected the cancelled upgrade to keep its release targets, got %+v", done.TargetReleases)
	}

	eventuallySaved(t, vault, loadedTaskID, func(saved UpgradeTask) bool {
		return saved.Status == TaskStatusCancelled && slices.Equal(saved.TargetReleases, releaseUpgrade().TargetReleases)
	})

	if deploys := director.allDeploys(); !slices.Equal(deploys, []string{firstDeployment}) {
		t.Fatalf("expected only the first instance deployed, got %v", deploys)
	}

	expectReleaseBumped(t, director.deployed(t, firstDeployment), deployedStemcellVersion)
}

// A release upgrade whose first deploy settles with an unknown outcome
// moves on and deploys the next instance with the same targets.
//
//nolint:paralleltest // the resumed upgrade waits on a shared test director
func TestReleaseUpgradeMovesOnWithItsTargetsAfterAnUnknownOutcome(t *testing.T) {
	director := newReleaseDirector()
	director.abandon(firstDeployment)

	vault := newWaitVault()

	storeUpgrade(t, vault, releaseUpgrade(
		pendingInstance("instance-1", firstDeployment),
		pendingInstance("instance-2", nextDeployment)))

	manager := loadManager(t, director, vault)

	done := expectCompleted(t, manager, loadedTaskID)
	if done.Instances[0].Status != InstanceStatusOutcomeUnknown || done.UnknownCount != 1 || done.CompletedCount != 1 {
		t.Fatalf("expected an unknown outcome and then a success, got %+v", done)
	}

	expectReleaseBumped(t, director.deployed(t, firstDeployment), deployedStemcellVersion)
	expectReleaseBumped(t, director.deployed(t, nextDeployment), deployedStemcellVersion)
}

//nolint:paralleltest // the resumed upgrade waits on a shared test director
func TestLoadLeavesAPausedReleaseUpgradePaused(t *testing.T) {
	director := newReleaseDirector()
	vault := newWaitVault()

	stored := releaseUpgrade(pendingInstance("instance-1", firstDeployment))
	stored.Status = TaskStatusPaused
	stored.Paused = true
	storeUpgrade(t, vault, stored)

	manager := loadManager(t, director, vault)

	expectQuiet(t, manager, director.resumeDirector, loadedTaskID, TaskStatusPaused, firstDeployment)

	err := manager.ResumeTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("resuming the paused upgrade: %v", err)
	}

	expectCompleted(t, manager, loadedTaskID)
	expectReleaseBumped(t, director.deployed(t, firstDeployment), deployedStemcellVersion)
}

// A release upgrade paused while its first instance deploys starts no
// other instance until it is resumed, and the resumed instance gets the
// same targets.
//
//nolint:paralleltest // the resumed upgrade waits on a shared test director
func TestPausedReleaseUpgradeResumesWithItsTargets(t *testing.T) {
	director := newReleaseDirector()
	director.holdDeployment(firstDeployment)

	vault := newWaitVault()

	storeUpgrade(t, vault, releaseUpgrade(
		pendingInstance("instance-1", firstDeployment),
		pendingInstance("instance-2", nextDeployment)))

	manager := loadManager(t, director, vault)

	director.waitForWaitOn(t, 101)

	err := manager.PauseTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("pausing the upgrade: %v", err)
	}

	director.releaseDeployment(firstDeployment)

	eventuallyWithin(t, manager, loadedTaskID, waitTimeout, "the first instance's success", func(current UpgradeTask) bool {
		return current.Instances[0].Status == InstanceStatusSuccess
	})
	expectQuiet(t, manager, director.resumeDirector, loadedTaskID, TaskStatusPaused, nextDeployment)

	err = manager.ResumeTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("resuming the upgrade: %v", err)
	}

	expectCompleted(t, manager, loadedTaskID)
	expectReleaseBumped(t, director.deployed(t, firstDeployment), deployedStemcellVersion)
	expectReleaseBumped(t, director.deployed(t, nextDeployment), deployedStemcellVersion)
}

// expectStoppedForMissingReleases fails the test unless the upgrade
// ended in the status given, with each instance that had not started
// failed for the missing release, and nothing deployed after the
// instances already settled. It returns the upgrade as it ended.
func expectStoppedForMissingReleases(t *testing.T, manager *Manager, director *releaseDirector, status TaskStatus, settledDeploys []string) UpgradeTask {
	t.Helper()

	done := eventuallyWithin(t, manager, loadedTaskID, waitTimeout, "the upgrade's end", func(current UpgradeTask) bool {
		return current.Status == status
	})

	failed := done.Instances[len(done.Instances)-1]
	if failed.Status != InstanceStatusFailed || !strings.Contains(failed.Error, redisForge+"/"+redisForgeTarget) || done.CompletedAt == nil {
		t.Fatalf("expected the instance not started to fail naming %s/%s, got %+v", redisForge, redisForgeTarget, done)
	}

	if deploys := director.allDeploys(); !slices.Equal(deploys, settledDeploys) {
		t.Fatalf("expected the deploys %v and no more, got %v", settledDeploys, deploys)
	}

	return done
}

// A release upgrade loaded after a restart checks its targets on the
// director again before it deploys, and fails when a version is gone.
//
//nolint:paralleltest // the resumed upgrade waits on a shared test director
func TestLoadedReleaseUpgradeFailsWhenTheDirectorLacksATarget(t *testing.T) {
	director := newReleaseDirector()
	director.withdrawTarget()

	vault := newWaitVault()

	storeUpgrade(t, vault, releaseUpgrade(pendingInstance("instance-1", firstDeployment)))

	manager := loadManager(t, director, vault)

	expectStoppedForMissingReleases(t, manager, director, TaskStatusFailed, nil)
	eventuallySaved(t, vault, loadedTaskID, func(saved UpgradeTask) bool { return saved.Status == TaskStatusFailed })
}

// A paused release upgrade checks its targets on the director again when
// it is resumed, and stops before its next deploy when a version is gone.
// It completes, since an instance was already upgraded.
//
//nolint:paralleltest // the resumed upgrade waits on a shared test director
func TestResumedReleaseUpgradeChecksItsTargetsAgain(t *testing.T) {
	director := newReleaseDirector()
	director.holdDeployment(firstDeployment)

	vault := newWaitVault()

	storeUpgrade(t, vault, releaseUpgrade(
		pendingInstance("instance-1", firstDeployment),
		pendingInstance("instance-2", nextDeployment)))

	manager := loadManager(t, director, vault)

	director.waitForWaitOn(t, 101)

	err := manager.PauseTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("pausing the upgrade: %v", err)
	}

	director.releaseDeployment(firstDeployment)

	eventuallyWithin(t, manager, loadedTaskID, waitTimeout, "the first instance's success", func(current UpgradeTask) bool {
		return current.Instances[0].Status == InstanceStatusSuccess
	})

	director.withdrawTarget()

	err = manager.ResumeTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("resuming the upgrade: %v", err)
	}

	expectStoppedForMissingReleases(t, manager, director, TaskStatusCompleted, []string{firstDeployment})
}

func TestTaskSummaryNamesTheTargets(t *testing.T) {
	t.Parallel()

	releasesOnly := releaseUpgrade(pendingInstance("instance-1", firstDeployment))

	summary := releasesOnly.ToSummary()
	if summary.TargetStemcell != "" || !slices.Equal(summary.TargetReleases, []string{redisForge + "/" + redisForgeTarget}) {
		t.Fatalf("expected no stemcell and the forge release in the summary, got %+v", summary)
	}

	stemcellOnly := storedUpgrade(loadedTaskID, pendingInstance("instance-1", firstDeployment))

	summary = stemcellOnly.ToSummary()
	if summary.TargetStemcell != targetStemcellOS+"/"+targetStemcellVersion || len(summary.TargetReleases) != 0 {
		t.Fatalf("expected the stemcell and no releases in the summary, got %+v", summary)
	}
}

func TestTaskSummaryCountsSkippedAndCancelledInstances(t *testing.T) {
	t.Parallel()

	task := releaseUpgrade(pendingInstance("instance-1", firstDeployment))
	task.SkippedCount = 2
	task.CancelledCount = 1

	summary := task.ToSummary()
	if summary.SkippedCount != 2 || summary.CancelledCount != 1 {
		t.Fatalf("expected the summary to carry 2 skipped and 1 cancelled instances, got %+v", summary)
	}
}
