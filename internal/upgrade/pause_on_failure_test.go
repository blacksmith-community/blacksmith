//nolint:testpackage // the tests read the manager's unexported state
package upgrade

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/certinventory"
)

// failDeploy makes the BOSH task the director answers for its next
// deploy end in state error. The director numbers its deploys from 101.
func (d *resumeDirector) failDeploy(taskID int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.states[taskID] = "error"
}

// holdTask makes the wait on the task last until the test releases it.
func (d *resumeDirector) holdTask(taskID int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.releases[taskID] = make(chan struct{})
}

// createUpgrade starts a manager over the director and asks it for an
// upgrade of the instances given, which pauses on a failure as pause
// says, or is left out when pause is nil.
func createUpgrade(t *testing.T, director *resumeDirector, pause *bool, instanceIDs ...string) (*Manager, *UpgradeTask) {
	t.Helper()

	manager := loadManager(t, director, newWaitVault())

	task, err := manager.CreateTask(context.Background(), CreateTaskRequest{
		InstanceIDs:    instanceIDs,
		TargetStemcell: StemcellTarget{OS: targetStemcellOS, Version: targetStemcellVersion},
		PauseOnFailure: pause,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	return manager, task
}

// expectFailurePause waits until the upgrade pauses after a failure and
// fails the test unless its reason names the failed instance.
func expectFailurePause(t *testing.T, manager *Manager, taskID, failedID string) UpgradeTask {
	t.Helper()

	paused := eventuallyWithin(t, manager, taskID, waitTimeout, "the pause of upgrade "+taskID, func(current UpgradeTask) bool {
		return current.Status == TaskStatusPaused
	})

	if !paused.Paused || !strings.Contains(paused.PauseReason, failedID) {
		t.Fatalf("expected a pause whose reason names %s, got paused %v with reason %q", failedID, paused.Paused, paused.PauseReason)
	}

	return paused
}

// An upgrade whose request leaves pause_on_failure out records that it
// doesn't pause, and goes on past a failed instance.
//
//nolint:paralleltest // the upgrade waits on a shared test director
func TestUpgradeLeftOutPauseOnFailureGoesOn(t *testing.T) {
	director := newResumeDirector()
	director.failDeploy(101)

	manager, task := createUpgrade(t, director, nil, "instance-1", "instance-2")

	if task.PauseOnFailure == nil || *task.PauseOnFailure {
		t.Fatalf("expected the upgrade to record that it doesn't pause, got %v", task.PauseOnFailure)
	}

	done := expectCompleted(t, manager, task.ID)

	if done.Instances[0].Status != InstanceStatusFailed || done.Instances[1].Status != InstanceStatusSuccess || done.PauseReason != "" {
		t.Fatalf("expected the first instance failed and the second deployed with no pause, got %+v", done)
	}
}

// An upgrade that asks to pause on a failure pauses once an instance
// fails, starts no other instance, and names the failed one. Resume goes
// on with the remaining instances and never retries the failed one.
//
//nolint:paralleltest // the upgrade waits on a shared test director
func TestUpgradePausesOnFailureWhenAsked(t *testing.T) {
	director := newResumeDirector()
	director.failDeploy(101)

	manager, task := createUpgrade(t, director, new(true), "instance-1", "instance-2", "instance-3")

	if task.PauseOnFailure == nil || !*task.PauseOnFailure {
		t.Fatalf("expected the upgrade to record that it pauses, got %v", task.PauseOnFailure)
	}

	paused := expectFailurePause(t, manager, task.ID, "instance-1")

	expectQuiet(t, manager, director, task.ID, TaskStatusPaused, "redis-instance-2")

	if paused.Instances[0].Status != InstanceStatusFailed || paused.Instances[1].Status != InstanceStatusPending || paused.Instances[2].Status != InstanceStatusPending {
		t.Fatalf("expected the failed instance and two pending ones, got %+v", paused.Instances)
	}

	if saved := savedTask(t, manager, task.ID); saved.Status != TaskStatusPaused || saved.PauseReason != paused.PauseReason || saved.PauseOnFailure == nil || !*saved.PauseOnFailure {
		t.Fatalf("expected the pause and its reason saved, got %+v", saved)
	}

	err := manager.ResumeTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}

	done := expectCompleted(t, manager, task.ID)

	if done.PauseReason != "" || done.Instances[1].Status != InstanceStatusSuccess || done.Instances[2].Status != InstanceStatusSuccess {
		t.Fatalf("expected the remaining instances deployed and the reason cleared, got %+v", done)
	}

	if deploys := director.allDeploys(); !slices.Equal(deploys, []string{"redis-instance-1", "redis-instance-2", "redis-instance-3"}) {
		t.Fatalf("expected each instance deployed once, got %v", deploys)
	}
}

// A cancel of an upgrade paused after a failure clears the reason.
//
//nolint:paralleltest // the upgrade waits on a shared test director
func TestCancelClearsTheFailurePauseReason(t *testing.T) {
	director := newResumeDirector()
	director.failDeploy(101)

	manager, task := createUpgrade(t, director, new(true), "instance-1", "instance-2")

	expectFailurePause(t, manager, task.ID, "instance-1")

	err := manager.CancelTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	cancelled := eventuallyWithin(t, manager, task.ID, waitTimeout, "the cancel of upgrade "+task.ID, func(current UpgradeTask) bool {
		return current.Status == TaskStatusCancelled
	})

	if cancelled.PauseReason != "" || director.deployCount("redis-instance-2") != 0 {
		t.Fatalf("expected the reason cleared and nothing more deployed, got %+v and deploys %v", cancelled, director.allDeploys())
	}
}

// A deploy that fails after its upgrade was cancelled leaves the task
// cancelled, rather than pausing it with a reason a resume could act on.
//
//nolint:paralleltest // the upgrade waits on a shared test director
func TestCancelledUpgradeStaysCancelledAfterALateFailure(t *testing.T) {
	director := newResumeDirector()
	director.holdThroughCancel()
	director.holdTask(101)

	manager, task := createUpgrade(t, director, new(true), "instance-1", "instance-2")
	director.waitForWaitOn(t, 101)

	err := manager.CancelTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	director.failDeploy(101)
	director.release(101)

	settled := eventuallyWithin(t, manager, task.ID, waitTimeout, "the late failure of instance-1", func(current UpgradeTask) bool {
		return current.Instances[0].Status == InstanceStatusFailed
	})

	if settled.Status != TaskStatusCancelled || settled.Paused || settled.PauseReason != "" {
		t.Fatalf("expected the task to stay cancelled with no pause, got %s paused %v with reason %q", settled.Status, settled.Paused, settled.PauseReason)
	}

	if director.deployCount("redis-instance-2") != 0 {
		t.Fatalf("expected nothing more deployed, got %v", director.allDeploys())
	}
}

// An upgrade saved before tasks carried pause_on_failure goes on past a
// failed instance, as it always did.
//
//nolint:paralleltest // the upgrade waits on a shared test director
func TestStoredUpgradeWithoutPauseOnFailureGoesOn(t *testing.T) {
	director := newResumeDirector()
	director.failDeploy(101)

	vault := newWaitVault()
	storeUpgrade(t, vault, storedUpgrade(loadedTaskID,
		pendingInstance("instance-1", firstDeployment),
		pendingInstance("instance-2", nextDeployment)))

	manager := loadManager(t, director, vault)

	done := expectCompleted(t, manager, loadedTaskID)

	if done.PauseOnFailure != nil || done.Instances[0].Status != InstanceStatusFailed || done.Instances[1].Status != InstanceStatusSuccess {
		t.Fatalf("expected the old upgrade to go on past its failure, got %+v", done)
	}
}

// A failure pauses an upgrade whose other deploy is in flight. That deploy
// finishes, no pending instance starts, and Resume carries on with it.
//
//nolint:paralleltest // the settle waits on a shared test director
func TestUpgradeFailureLetsInFlightDeploysFinish(t *testing.T) {
	const heldTaskID = 8

	director := newResumeDirector()
	director.failDeploy(adoptedTaskID)
	director.holdTask(heldTaskID)

	started := time.Now().Add(-time.Minute)
	failing := InstanceUpgrade{InstanceID: "instance-a", DeploymentName: "redis-instance-a", Status: InstanceStatusInterrupted, BOSHTaskID: adoptedTaskID, ContextID: loadedContextID, StartedAt: &started}
	held := InstanceUpgrade{InstanceID: "instance-b", DeploymentName: "redis-instance-b", Status: InstanceStatusInterrupted, BOSHTaskID: heldTaskID, ContextID: "7c2a4d5f-1b8e-4d66-8b2f-4a3c9e0d1f22", StartedAt: &started}

	stored := storedUpgrade(loadedTaskID, failing, held, pendingInstance("instance-c", "redis-instance-c"))
	stored.PauseOnFailure = new(true)

	vault := newWaitVault()
	storeUpgrade(t, vault, stored)

	manager := loadManager(t, director, vault)

	expectFailurePause(t, manager, loadedTaskID, "instance-a")

	director.release(heldTaskID)

	settled := eventuallyWithin(t, manager, loadedTaskID, waitTimeout, "the in-flight deploy to finish", func(current UpgradeTask) bool {
		return current.Instances[1].Status == InstanceStatusSuccess
	})

	if settled.Status != TaskStatusPaused || settled.Instances[2].Status != InstanceStatusPending {
		t.Fatalf("expected the upgrade to stay paused with its last instance pending, got %+v", settled)
	}

	expectQuiet(t, manager, director, loadedTaskID, TaskStatusPaused, "redis-instance-c")

	err := manager.ResumeTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}

	done := expectCompleted(t, manager, loadedTaskID)

	if done.Instances[0].Status != InstanceStatusFailed || done.Instances[2].Status != InstanceStatusSuccess {
		t.Fatalf("expected the failed instance left failed and the pending one deployed, got %+v", done.Instances)
	}

	if deploys := director.allDeploys(); !slices.Equal(deploys, []string{"redis-instance-c"}) {
		t.Fatalf("expected one deploy, of the pending instance, got %v", deploys)
	}
}

// A certificate task that doesn't pause on a failure goes on past one
// while the certificate settings pause.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificateTaskPauseOffOverridesTheSettings(t *testing.T) {
	env := newRedeployEnv(t)

	task := env.failingFirstTask()
	task.PauseOnFailure = new(false)

	manager := env.startLoaded(task)

	done := waitForInstance(t, manager, 1, InstanceStatusSuccess)
	if done.Instances[0].Status != InstanceStatusFailed {
		t.Fatalf("expected the first instance to fail, got %s", done.Instances[0].Status)
	}

	waitUntil(t, "the task to complete", func() bool { return current(t, manager).Status == TaskStatusCompleted })
}

// A certificate task that pauses on a failure pauses after one while the
// certificate settings don't, with a reason that names the instance.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCertificateTaskPauseOnOverridesTheSettings(t *testing.T) {
	env := newRedeployEnv(t)
	env.settings = func() certinventory.Settings {
		settings := certinventory.DefaultSettings()
		settings.PauseOnFailure = false

		return settings
	}

	task := env.failingFirstTask()
	task.PauseOnFailure = new(true)

	manager := env.startLoaded(task)

	waitForInstance(t, manager, 0, InstanceStatusFailed)
	waitUntil(t, "the task to pause", func() bool { return current(t, manager).Status == TaskStatusPaused })

	paused := current(t, manager)
	if paused.Instances[1].Status != InstanceStatusPending || !strings.Contains(paused.PauseReason, "instance-0") {
		t.Fatalf("expected the next instance pending and a reason naming instance-0, got %s and %q", paused.Instances[1].Status, paused.PauseReason)
	}
}

// pauseCreateEnv is createEnv with certificate settings whose
// pause_on_failure the test sets.
func pauseCreateEnv(t *testing.T, pause *atomic.Bool) *previewEnv {
	t.Helper()

	env := newPreviewEnv(t)
	env.configure(t, func(deps *Deps) {
		deps.Settings = func() certinventory.Settings {
			settings := certinventory.DefaultSettings()
			settings.PauseOnFailure = pause.Load()

			return settings
		}
	})

	env.manager.vault = &createVault{waitVault: newWaitVault()}
	env.manager.createRetry = 5 * time.Millisecond
	env.manager.Start()
	waitForLoad(t, env.manager)

	return env
}

// A certificate task takes the settings' pause_on_failure as it stands at
// its creation when the request leaves it out, and the request's value
// otherwise. A later change to the settings leaves the task's value as it
// was.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCreateCertificateTaskPauseOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings bool
		request  *bool
		want     bool
	}{
		{name: "left out with the settings on", settings: true, want: true},
		{name: "left out with the settings off", settings: false, want: false},
		{name: "off with the settings on", settings: true, request: new(false), want: false},
		{name: "on with the settings off", settings: false, request: new(true), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var pause atomic.Bool
			pause.Store(tc.settings)

			env := pauseCreateEnv(t, &pause)
			preview := previewOf(t, env, redeploy(waitInstanceID))

			req := createRequest(preview.ID)
			req.PauseOnFailure = tc.request

			task, _, err := env.manager.CreateCertificateTask(context.Background(), req)
			if err != nil {
				t.Fatalf("CreateCertificateTask: %v", err)
			}

			if task.PauseOnFailure == nil || *task.PauseOnFailure != tc.want {
				t.Fatalf("expected pause_on_failure %v, got %v", tc.want, task.PauseOnFailure)
			}

			if env.manager.pausesOnFailure() != tc.settings {
				t.Fatal("the request changed the certificate settings")
			}

			pause.Store(!tc.settings)

			kept := taskCopy(t, env.manager, task.ID)
			if *kept.PauseOnFailure != tc.want || env.manager.taskPausesOnFailure(&kept) != tc.want {
				t.Fatalf("expected the task to keep pause_on_failure %v after the settings changed, got %v", tc.want, *kept.PauseOnFailure)
			}
		})
	}
}

// A retry of a certificate create that asks for another pause_on_failure
// is a different request.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestCreateCertificateTaskPauseOnFailureConflict(t *testing.T) {
	env, _ := createEnv(t)
	preview := previewOf(t, env, redeploy(waitInstanceID))

	_, _, err := env.manager.CreateCertificateTask(context.Background(), createRequest(preview.ID))
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	changed := createRequest(preview.ID)
	changed.PauseOnFailure = new(false)

	_, _, err = env.manager.CreateCertificateTask(context.Background(), changed)
	expectAPIError(t, err, http.StatusConflict, apierr.CodeRequestIDConflict)
}

// savedTask reads the upgrade the manager saved in its vault.
func savedTask(t *testing.T, manager *Manager, taskID string) UpgradeTask {
	t.Helper()

	var saved UpgradeTask

	_, err := manager.vault.Get(context.Background(), upgradeTasksVaultPath+"/"+taskID, &saved)
	if err != nil {
		t.Fatalf("reading the saved upgrade: %v", err)
	}

	return saved
}

// The save that records an upgrade's failed instance records the pause
// with it, so a restart from that save, as after a stop or a crash before
// any later save, starts no other instance.
//
//nolint:paralleltest // the upgrade waits on a shared test director
func TestUpgradeSavesTheFailureAndThePauseTogether(t *testing.T) {
	director := newResumeDirector()
	director.failDeploy(101)

	var (
		once  sync.Once
		first []byte
	)

	// The pause shows in memory before the save that carries it runs, so
	// the test waits for the save itself rather than for the pause.
	firstSaved := make(chan struct{})

	vault := newWaitVault()
	vault.onPut = func(path string, raw []byte) {
		if !strings.HasPrefix(path, upgradeTasksVaultPath+"/") {
			return
		}

		var saved UpgradeTask
		if json.Unmarshal(raw, &saved) != nil || len(saved.Instances) == 0 || saved.Instances[0].Status != InstanceStatusFailed {
			return
		}

		once.Do(func() {
			first = raw
			close(firstSaved)
		})
	}

	manager := loadManager(t, director, vault)

	task, err := manager.CreateTask(context.Background(), CreateTaskRequest{
		InstanceIDs:    []string{"instance-1", "instance-2"},
		TargetStemcell: StemcellTarget{OS: targetStemcellOS, Version: targetStemcellVersion},
		PauseOnFailure: new(true),
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	expectFailurePause(t, manager, task.ID, "instance-1")

	select {
	case <-firstSaved:
	case <-time.After(waitTimeout):
		t.Fatal("the failed instance was never saved")
	}

	var stored UpgradeTask

	err = json.Unmarshal(first, &stored)
	if err != nil {
		t.Fatalf("expected a save of the failed instance, got %v", err)
	}

	if stored.Status != TaskStatusPaused || !stored.Paused || !strings.Contains(stored.PauseReason, "instance-1") {
		t.Fatalf("expected the save of the failure to carry the pause, got %s paused %v with reason %q", stored.Status, stored.Paused, stored.PauseReason)
	}

	restarted := newResumeDirector()
	restartVault := newWaitVault()
	storeUpgrade(t, restartVault, stored)

	again := loadManager(t, restarted, restartVault)

	expectQuiet(t, again, restarted, task.ID, TaskStatusPaused, "redis-instance-2")
}

// An upgrade that pauses on a failure pauses when an instance's deploy
// outcome is unknown too, with a reason that says so, and Resume goes on
// with the rest.
//
//nolint:paralleltest // MergeStemcellOverlay runs spruce, whose global state races across tests
func TestUpgradeOutcomeUnknownPausesWhenAsked(t *testing.T) {
	director := newLostDirector()
	director.abandonWaits = 1

	manager := loadManager(t, director, newWaitVault())

	task, err := manager.CreateTask(context.Background(), CreateTaskRequest{
		InstanceIDs:    []string{waitInstanceID, secondInstanceID},
		TargetStemcell: StemcellTarget{OS: "ubuntu-noble", Version: "1.2"},
		PauseOnFailure: new(true),
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	paused := expectFailurePause(t, manager, task.ID, waitInstanceID)

	if !strings.Contains(paused.PauseReason, "unknown") {
		t.Fatalf("expected the reason to say the outcome is unknown, got %q", paused.PauseReason)
	}

	if paused.Instances[0].Status != InstanceStatusOutcomeUnknown || paused.Instances[1].Status != InstanceStatusPending {
		t.Fatalf("expected an unknown outcome and a pending instance, got %+v", paused.Instances)
	}

	if saved := savedTask(t, manager, task.ID); saved.Status != TaskStatusPaused || saved.PauseReason != paused.PauseReason {
		t.Fatalf("expected the pause and its reason saved, got %+v", saved)
	}

	time.Sleep(quietPeriod)

	if sent := director.contextIDs(); len(sent) != 1 {
		t.Fatalf("expected one deploy before the pause, got %q", sent)
	}

	err = manager.ResumeTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}

	waitForTheDeployWait(t, director.waitDirector)

	director.finish <- &bosh.Task{ID: waitDeployTaskID, State: boshTaskDone}

	done := expectCompleted(t, manager, task.ID)

	if done.PauseReason != "" || done.Instances[1].Status != InstanceStatusSuccess {
		t.Fatalf("expected the second instance deployed and the reason cleared, got %+v", done)
	}
}

// An upgrade an operator paused while a deploy ran takes the failure's
// reason when that deploy fails, so the detail says why it waits.
//
//nolint:paralleltest // the upgrade waits on a shared test director
func TestUpgradePausedByHandTakesTheFailureReason(t *testing.T) {
	director := newResumeDirector()
	director.failDeploy(101)
	director.holdTask(101)

	manager, task := createUpgrade(t, director, new(true), "instance-1", "instance-2")

	waitUntil(t, "the first deploy", func() bool { return director.deployCount("redis-instance-1") == 1 })

	err := manager.PauseTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("PauseTask: %v", err)
	}

	director.release(101)

	held := eventuallyWithin(t, manager, task.ID, waitTimeout, "the failure's reason", func(current UpgradeTask) bool {
		return current.Instances[0].Status == InstanceStatusFailed && current.PauseReason != ""
	})

	if held.Status != TaskStatusPaused || !strings.Contains(held.PauseReason, "instance-1") {
		t.Fatalf("expected the paused upgrade to name instance-1, got %s with %q", held.Status, held.PauseReason)
	}

	expectQuiet(t, manager, director, task.ID, TaskStatusPaused, "redis-instance-2")
}

// withPending appends a pending instance on its own deployment to the
// certificate task.
func (e *redeployEnv) withPending(task UpgradeTask, instanceID string) UpgradeTask {
	next := e.task().Instances[0]
	next.InstanceID = instanceID
	next.DeploymentName = "redis-" + instanceID

	task.Instances = append(task.Instances, next)
	task.TotalCount = len(task.Instances)

	return task
}

// withFailedFirst puts a failed instance ahead of the certificate task's
// others, and the reason it paused the task with.
func (e *redeployEnv) withFailedFirst(task UpgradeTask) UpgradeTask {
	completed := validateNow
	failed := e.task().Instances[0]
	failed.InstanceID = "instance-0"
	failed.DeploymentName = "redis-instance-0"
	failed.Status = InstanceStatusFailed
	failed.Error = "The deploy failed."
	failed.CompletedAt = &completed
	failed.Certificates.Phase = RotationPhaseDone

	task.Instances = append([]InstanceUpgrade{failed}, task.Instances...)
	task.TotalCount = len(task.Instances)
	task.FailedCount++
	task.PauseReason = failurePauseReason(&failed)

	return task
}

// A decision that fails an instance pauses the task with a reason that
// names it, in the same save as the decision.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestFailedDecisionNamesTheFailedInstance(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored func(*redeployEnv) UpgradeTask
		decide func(*Manager) error
	}{
		{
			name:   "resolve failed",
			stored: (*redeployEnv).awaitingOutcome,
			decide: func(manager *Manager) error {
				return manager.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: "failed"})
			},
		},
		{
			name: "abandon the regeneration",
			stored: func(env *redeployEnv) UpgradeTask {
				env.madeNewVersion()

				return env.awaitingReconcile(nil)
			},
			decide: func(manager *Manager) error {
				return manager.ReconcileRegeneration(context.Background(), certTaskID, waitInstanceID, ReconcileRequest{Variable: regenLeaf, Action: "abandon"})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newRedeployEnv(t)
			manager := env.load(env.withPending(tc.stored(env), "instance-2"))

			err := tc.decide(manager)
			if err != nil {
				t.Fatalf("the decision: %v", err)
			}

			task := current(t, manager)
			if task.Status != TaskStatusPaused || task.Instances[0].Status != InstanceStatusFailed || task.Instances[1].Status != InstanceStatusPending {
				t.Fatalf("expected a paused task with a failed and a pending instance, got %s with %+v", task.Status, task.Instances)
			}

			if !strings.Contains(task.PauseReason, waitInstanceID) {
				t.Fatalf("expected a reason naming %s, got %q", waitInstanceID, task.PauseReason)
			}

			if saved := env.stored(); saved.PauseReason != task.PauseReason || saved.Instances[0].Status != InstanceStatusFailed {
				t.Fatalf("expected the reason saved with the decision, got %q with %s", saved.PauseReason, saved.Instances[0].Status)
			}
		})
	}
}

// A decision that fails an instance follows the task's own choice over
// the certificate settings, whichever way the two disagree.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestFailedDecisionFollowsTheTaskChoice(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings bool
		task     bool
	}{
		{name: "the task pauses where the settings don't", settings: false, task: true},
		{name: "the task goes on where the settings pause", settings: true, task: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newRedeployEnv(t)
			env.settings = func() certinventory.Settings {
				settings := certinventory.DefaultSettings()
				settings.PauseOnFailure = tc.settings

				return settings
			}

			stored := env.withPending(env.awaitingOutcome(), "instance-2")
			stored.PauseOnFailure = new(tc.task)
			manager := env.load(stored)

			err := manager.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: "failed"})
			if err != nil {
				t.Fatalf("ResolveDeployOutcome: %v", err)
			}

			saved := env.stored()
			if saved.Instances[0].Status != InstanceStatusFailed {
				t.Fatalf("expected the instance saved as failed, got %s", saved.Instances[0].Status)
			}

			if named := strings.Contains(saved.PauseReason, waitInstanceID); named != tc.task {
				t.Fatalf("expected a reason naming %s to be %v with the task's choice %v, got %q", waitInstanceID, tc.task, tc.task, saved.PauseReason)
			}
		})
	}
}

// A task paused after a failure stays paused when a later decision settles
// another instance well, so its pending instance waits for Continue.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestFailurePauseOutlastsALaterDecision(t *testing.T) {
	env := newRedeployEnv(t)
	stored := env.withPending(env.withFailedFirst(env.awaitingOutcome()), "instance-2")
	manager := env.load(stored)

	err := manager.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: "verify"})
	if err != nil {
		t.Fatalf("ResolveDeployOutcome: %v", err)
	}

	time.Sleep(quietPeriod)

	task := current(t, manager)
	if task.Instances[1].Status != InstanceStatusSuccess || task.Instances[2].Status != InstanceStatusPending {
		t.Fatalf("expected the verified instance and a pending one, got %+v", task.Instances)
	}

	if task.Status != TaskStatusPaused || task.PauseReason != stored.PauseReason {
		t.Fatalf("expected the task to stay paused for instance-0, got %s with %q", task.Status, task.PauseReason)
	}

	if deploys := env.director.deploys(); len(deploys) != 0 {
		t.Fatalf("expected no deploy, got %d", len(deploys))
	}
}

// A decision that lets a task go on clears a failure's reason once no
// instance is left for it to hold. The engine never stores a reason with
// no pending instance, since the hold needs one, so withFailedFirst builds
// that state by hand to guard resumeAfterDecision's defensive clear.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestDecisionResumeClearsAnIdleFailureReason(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.load(env.withFailedFirst(env.awaitingOutcome()))

	err := manager.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: "verify"})
	if err != nil {
		t.Fatalf("ResolveDeployOutcome: %v", err)
	}

	waitUntil(t, "the task to finish", func() bool { return current(t, manager).Status != TaskStatusPaused })

	if task := current(t, manager); task.PauseReason != "" {
		t.Fatalf("expected the reason cleared, got %s with %q", task.Status, task.PauseReason)
	}

	if saved := env.stored(); saved.PauseReason != "" {
		t.Fatalf("expected the cleared reason saved, got %q", saved.PauseReason)
	}
}
