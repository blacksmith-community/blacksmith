//nolint:testpackage // the tests resume stored release upgrades and read the manager's unexported state
package upgrade

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/pkg/logger"
)

// releaseListingWait is how the waiting message on an instance starts.
const releaseListingWait = "waiting for the director to list its releases"

// listingDirector is a releaseDirector whose release listing fails while
// the test has it failing, and waits while the test holds it.
type listingDirector struct {
	*releaseDirector

	listMu   sync.Mutex
	failing  bool
	held     chan struct{}
	listings int
}

func newListingDirector() *listingDirector {
	return &listingDirector{releaseDirector: newReleaseDirector()}
}

func (d *listingDirector) GetReleases(ctx context.Context) ([]bosh.Release, error) {
	d.listMu.Lock()
	d.listings++
	failing := d.failing
	held := d.held
	d.listMu.Unlock()

	if held != nil {
		select {
		case <-held:
		case <-ctx.Done():
			return nil, fmt.Errorf("listing the releases: %w", ctx.Err())
		}
	}

	if failing {
		return nil, errReleaseListInTest
	}

	return d.releaseDirector.GetReleases(ctx)
}

// setFailing makes each release listing from now on fail, or pass.
func (d *listingDirector) setFailing(failing bool) {
	d.listMu.Lock()
	defer d.listMu.Unlock()

	d.failing = failing
}

// hold makes each release listing from now on wait until the function it
// returns is called.
func (d *listingDirector) hold() func() {
	held := make(chan struct{})

	d.listMu.Lock()
	d.held = held
	d.listMu.Unlock()

	var once sync.Once

	return func() { once.Do(func() { close(held) }) }
}

// listingCount returns how many release listings the director has served
// or is serving.
func (d *listingDirector) listingCount() int {
	d.listMu.Lock()
	defer d.listMu.Unlock()

	return d.listings
}

// shortenListingBackoff makes the release check retry a failed listing
// at once, until the test ends. A test calls it before it starts the
// manager, so the manager stops before the backoff is restored.
func shortenListingBackoff(t *testing.T) {
	t.Helper()

	previous := loadRetryBackoff
	loadRetryBackoff = 10 * time.Millisecond

	t.Cleanup(func() { loadRetryBackoff = previous })
}

// twoInstanceReleaseUpgrade stores a release upgrade of two pending
// instances in a new vault and returns it.
func twoInstanceReleaseUpgrade(t *testing.T) *waitVault {
	t.Helper()

	vault := newWaitVault()
	storeUpgrade(t, vault, releaseUpgrade(
		pendingInstance("instance-1", firstDeployment),
		pendingInstance("instance-2", nextDeployment)))

	return vault
}

// expectWaitingForTheListing waits until the upgrade's first instance
// shows that the upgrade waits for the release listing, and checks that
// the upgrade is still running and nothing was deployed.
func expectWaitingForTheListing(t *testing.T, manager *Manager, director *listingDirector) {
	t.Helper()

	waiting := eventuallyWithin(t, manager, loadedTaskID, waitTimeout, "the waiting message", func(current UpgradeTask) bool {
		return strings.HasPrefix(current.Instances[0].Error, releaseListingWait)
	})

	if waiting.Status != TaskStatusRunning || !strings.Contains(waiting.Instances[0].Error, errReleaseListInTest.Error()) {
		t.Fatalf("expected a running upgrade that names the listing error, got %+v", waiting)
	}

	for _, instance := range waiting.Instances {
		if instance.Status != InstanceStatusPending {
			t.Fatalf("expected every instance to stay pending, got %+v", waiting.Instances)
		}
	}

	if deploys := director.allDeploys(); len(deploys) != 0 {
		t.Fatalf("expected no deploy while the listing fails, got %v", deploys)
	}
}

// A release check that can't list the director's releases waits and
// checks again, and the upgrade deploys every instance once a listing
// passes.
//
//nolint:paralleltest // it shortens the package's retry backoff
func TestReleaseCheckWaitsOutAFailedListing(t *testing.T) {
	shortenListingBackoff(t)

	director := newListingDirector()
	director.setFailing(true)

	log := newProcessLogger()
	manager := loadManagerLogging(t, log, director, twoInstanceReleaseUpgrade(t))

	expectWaitingForTheListing(t, manager, director)
	waitUntil(t, "a second listing", func() bool { return director.listingCount() > 1 })

	if !log.loggedError(loadedTaskID, "again in", errReleaseListInTest.Error()) {
		t.Fatalf("expected an error naming task %s and the next check, got %v", loadedTaskID, log.errors)
	}

	director.setFailing(false)

	done := expectCompleted(t, manager, loadedTaskID)

	for _, instance := range done.Instances {
		if instance.Status != InstanceStatusSuccess || instance.Error != "" {
			t.Fatalf("expected every instance upgraded with no message left, got %+v", done.Instances)
		}
	}

	expectReleaseBumped(t, director.deployed(t, firstDeployment), deployedStemcellVersion)
	expectReleaseBumped(t, director.deployed(t, nextDeployment), deployedStemcellVersion)
}

// A cancel while the release check waits for a listing skips the pending
// instances.
//
//nolint:paralleltest // it shortens the package's retry backoff
func TestReleaseCheckSkipsThePendingInstancesOnACancelDuringTheWait(t *testing.T) {
	shortenListingBackoff(t)

	director := newListingDirector()
	director.setFailing(true)

	vault := twoInstanceReleaseUpgrade(t)
	manager := loadManager(t, director, vault)

	expectWaitingForTheListing(t, manager, director)

	err := manager.CancelTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("cancelling the upgrade: %v", err)
	}

	done := eventuallyWithin(t, manager, loadedTaskID, waitTimeout, "the skip of the pending instances", func(current UpgradeTask) bool {
		return current.SkippedCount == len(current.Instances)
	})

	for _, instance := range done.Instances {
		if instance.Status != InstanceStatusSkipped {
			t.Fatalf("expected every instance skipped, got %+v", done.Instances)
		}
	}

	if done.Status != TaskStatusCancelled || len(director.allDeploys()) != 0 {
		t.Fatalf("expected a cancelled upgrade and no deploy, got %+v and %v", done, director.allDeploys())
	}

	eventuallySaved(t, vault, loadedTaskID, func(saved UpgradeTask) bool { return saved.SkippedCount == len(saved.Instances) })
}

// Stop while the release check waits for a listing leaves the upgrade
// running with its instances pending, so the next start resumes it.
//
//nolint:paralleltest // it shortens the package's retry backoff
func TestReleaseCheckLeavesTheUpgradeRunningOnAStopDuringTheWait(t *testing.T) {
	shortenListingBackoff(t)

	director := newListingDirector()
	director.setFailing(true)

	vault := twoInstanceReleaseUpgrade(t)
	manager := loadManager(t, director, vault)

	expectWaitingForTheListing(t, manager, director)

	manager.Stop()

	saved := vault.savedTask(t, loadedTaskID)
	if saved.Status != TaskStatusRunning || saved.CompletedAt != nil {
		t.Fatalf("expected the saved upgrade to stay running, got %+v", saved)
	}

	for _, instance := range saved.Instances {
		if instance.Status != InstanceStatusPending {
			t.Fatalf("expected every saved instance to stay pending, got %+v", saved.Instances)
		}
	}

	if deploys := director.allDeploys(); len(deploys) != 0 {
		t.Fatalf("expected no deploy, got %v", deploys)
	}
}

// A release version that left the director is final, and the upgrade
// completes when an instance was already upgraded.
//
//nolint:paralleltest // the resumed upgrade waits on a shared test director
func TestReleaseCheckCompletesAPartlyUpgradedUpgradeWhenATargetIsGone(t *testing.T) {
	director := newReleaseDirector()
	director.withdrawTarget()

	upgraded := pendingInstance("instance-1", firstDeployment)
	upgraded.Status = InstanceStatusSuccess

	task := releaseUpgrade(upgraded, pendingInstance("instance-2", nextDeployment))
	task.CompletedCount = 1

	vault := newWaitVault()
	storeUpgrade(t, vault, task)

	manager := loadManager(t, director, vault)

	expectStoppedForMissingReleases(t, manager, director, TaskStatusCompleted, nil)
	eventuallySaved(t, vault, loadedTaskID, func(saved UpgradeTask) bool { return saved.Status == TaskStatusCompleted })
}

// A pause that arrives while the release check lists the director's
// releases stops the next deploy until a resume.
//
//nolint:paralleltest // the resumed upgrade waits on a shared test director
func TestReleaseCheckHonoursAPauseDuringTheListing(t *testing.T) {
	director := newListingDirector()
	letGo := director.hold()
	t.Cleanup(letGo)

	manager := loadManager(t, director, twoInstanceReleaseUpgrade(t))

	waitUntil(t, "a release listing", func() bool { return director.listingCount() > 0 })

	err := manager.PauseTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("pausing the upgrade: %v", err)
	}

	letGo()

	expectQuiet(t, manager, director.resumeDirector, loadedTaskID, TaskStatusPaused, firstDeployment)

	err = manager.ResumeTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("resuming the upgrade: %v", err)
	}

	expectCompleted(t, manager, loadedTaskID)
	expectReleaseBumped(t, director.deployed(t, firstDeployment), deployedStemcellVersion)
	expectReleaseBumped(t, director.deployed(t, nextDeployment), deployedStemcellVersion)
}

// A target found gone after a pause arrived during the listing ends the
// upgrade, which is no longer paused.
//
//nolint:paralleltest // the resumed upgrade waits on a shared test director
func TestReleaseCheckClearsThePauseWhenATargetIsGone(t *testing.T) {
	director := newListingDirector()
	director.withdrawTarget()
	letGo := director.hold()
	t.Cleanup(letGo)

	manager := loadManager(t, director, twoInstanceReleaseUpgrade(t))

	waitUntil(t, "a release listing", func() bool { return director.listingCount() > 0 })

	err := manager.PauseTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("pausing the upgrade: %v", err)
	}

	letGo()

	done := expectStoppedForMissingReleases(t, manager, director.releaseDirector, TaskStatusFailed, nil)
	if done.Paused {
		t.Fatalf("expected the failed upgrade to be no longer paused, got %+v", done)
	}
}

// pauseWatchLogger is a processLogger that holds the upgrade at the line
// that logs its wait for the director's releases, which comes just before
// the wait begins, until proceed closes. It closes paused once the upgrade
// waits for a resume.
type pauseWatchLogger struct {
	*processLogger

	reached chan struct{}
	proceed chan struct{}
	paused  chan struct{}

	heldOnce   sync.Once
	pausedOnce sync.Once
	letGoOnce  sync.Once
}

func newPauseWatchLogger() *pauseWatchLogger {
	return &pauseWatchLogger{
		processLogger: newProcessLogger(),
		reached:       make(chan struct{}),
		proceed:       make(chan struct{}),
		paused:        make(chan struct{}),
	}
}

func (l *pauseWatchLogger) Named(string) logger.Logger { return l }

func (l *pauseWatchLogger) Error(msg string, args ...interface{}) {
	l.processLogger.Error(msg, args...)

	if strings.HasPrefix(msg, "Task %s can't check its release targets") {
		l.heldOnce.Do(func() {
			close(l.reached)
			<-l.proceed
		})
	}
}

func (l *pauseWatchLogger) Debug(msg string, _ ...interface{}) {
	if strings.HasPrefix(msg, "Task %s is paused") {
		l.pausedOnce.Do(func() { close(l.paused) })
	}
}

// letGo lets the held upgrade go on.
func (l *pauseWatchLogger) letGo() { l.letGoOnce.Do(func() { close(l.proceed) }) }

// A pause sent just before the release check starts to wait out a failed
// listing ends the wait at once, rather than when the backoff ends, and
// sends no deploy.
//
//nolint:paralleltest // it sets the package's retry backoff
func TestReleaseCheckEndsItsWaitAtOnceOnAPause(t *testing.T) {
	previous := loadRetryBackoff
	loadRetryBackoff = time.Minute

	t.Cleanup(func() { loadRetryBackoff = previous })

	director := newListingDirector()
	director.setFailing(true)

	log := newPauseWatchLogger()
	manager := loadManagerLogging(t, log, director, twoInstanceReleaseUpgrade(t))
	t.Cleanup(log.letGo)

	select {
	case <-log.reached:
	case <-time.After(waitTimeout):
		t.Fatal("the release check never logged its wait")
	}

	expectWaitingForTheListing(t, manager, director)

	// The pause, and its signal, come before the wait begins.
	err := manager.PauseTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("pausing the upgrade: %v", err)
	}

	log.letGo()

	select {
	case <-log.paused:
	case <-time.After(waitTimeout):
		t.Fatal("the wait for the director's releases never ended on the pause")
	}

	if deploys := director.allDeploys(); len(deploys) != 0 || director.listingCount() != 1 {
		t.Fatalf("expected one listing and no deploy, got %d listings and %v", director.listingCount(), deploys)
	}
}
