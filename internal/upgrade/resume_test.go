//nolint:testpackage // the tests resume stored upgrades and read the manager's unexported state
package upgrade

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
)

const (
	// heldTaskID names the upgrade whose deploy holds the only slot.
	heldTaskID = "held-upgrade"
	// heldDeployment is the deployment whose deploys the test director
	// holds until the test releases them.
	heldDeployment = "redis-held"
	// quietPeriod is how long a test watches for something that must not
	// happen.
	quietPeriod = 200 * time.Millisecond
	// firstDeployment and nextDeployment are the deployments of a stored
	// upgrade's first and second instances.
	firstDeployment = "redis-instance-1"
	nextDeployment  = "redis-instance-2"
)

// resumeDirector is a director for resumed upgrades. Each deploy gets a
// task of its own, and the wait on a task returns it done at once, unless
// the test holds the task or its deployment, when the wait lasts until the
// test releases it or cancels it. A call it does not script panics through
// the nil embedded Director.
type resumeDirector struct {
	bosh.Director

	mu          sync.Mutex
	nextTaskID  int
	deploys     []string
	deployments map[int]string
	held        map[string]bool
	releases    map[int]chan struct{}
	states      map[int]string
	waiting     map[int]bool
	cancels     []int
}

func newResumeDirector() *resumeDirector {
	return &resumeDirector{
		nextTaskID:  100,
		deployments: make(map[int]string),
		held:        make(map[string]bool),
		releases:    make(map[int]chan struct{}),
		states:      make(map[int]string),
		waiting:     make(map[int]bool),
	}
}

func (d *resumeDirector) GetDeployment(_ context.Context, name string) (*bosh.DeploymentDetail, error) {
	manifest := fmt.Sprintf("name: %s\nstemcells:\n- alias: default\n  os: ubuntu-noble\n  version: \"1.1\"\n", name)

	return &bosh.DeploymentDetail{Name: name, Manifest: manifest}, nil
}

func (d *resumeDirector) UpdateDeployment(_ context.Context, name, _ string) (*bosh.Task, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.nextTaskID++
	taskID := d.nextTaskID

	d.deploys = append(d.deploys, name)
	d.deployments[taskID] = name

	if d.held[name] {
		d.releases[taskID] = make(chan struct{})
	}

	return &bosh.Task{ID: taskID, State: waitStateRunning, Deployment: name}, nil
}

func (d *resumeDirector) WaitForTask(ctx context.Context, taskID int) (*bosh.Task, error) {
	d.mu.Lock()
	release, held := d.releases[taskID]
	d.waiting[taskID] = true
	deployment := d.deployments[taskID]
	d.mu.Unlock()

	if held {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, &bosh.DirectorCallError{
				Operation: "WaitForTask",
				Director:  "https://director.example.test:25555",
				Cause:     ctx.Err(),
				Err:       ctx.Err(),
				TaskID:    taskID,
				Task:      bosh.TaskLeftRunning,
			}
		}
	}

	d.mu.Lock()
	state, ok := d.states[taskID]
	d.mu.Unlock()

	if !ok {
		state = boshTaskDone
	}

	return &bosh.Task{ID: taskID, State: state, Deployment: deployment}, nil
}

func (d *resumeDirector) CancelTask(_ context.Context, taskID int) error {
	d.mu.Lock()
	d.cancels = append(d.cancels, taskID)
	d.states[taskID] = boshTaskCancelled
	d.mu.Unlock()

	d.release(taskID)

	return nil
}

// holdAdopted makes the wait on the adopted task last until the test
// releases it.
func (d *resumeDirector) holdAdopted() {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.releases[adoptedTaskID] = make(chan struct{})
}

// holdDeployment makes the wait on each deploy of the deployment last
// until the test releases the deployment.
func (d *resumeDirector) holdDeployment(deployment string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.held[deployment] = true
}

// release ends the wait on the task, which ends in state done unless a
// cancel set another state.
func (d *resumeDirector) release(taskID int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if release, ok := d.releases[taskID]; ok {
		delete(d.releases, taskID)
		close(release)
	}
}

// releaseDeployment ends the wait on each held deploy of the deployment.
func (d *resumeDirector) releaseDeployment(deployment string) {
	d.mu.Lock()
	delete(d.held, deployment)

	var taskIDs []int

	for taskID, name := range d.deployments {
		if name == deployment {
			taskIDs = append(taskIDs, taskID)
		}
	}
	d.mu.Unlock()

	for _, taskID := range taskIDs {
		d.release(taskID)
	}
}

// deployCount returns how many deploys of the deployment were sent.
func (d *resumeDirector) deployCount(deployment string) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	count := 0

	for _, name := range d.deploys {
		if name == deployment {
			count++
		}
	}

	return count
}

// allDeploys returns the deployment of every deploy sent, in order.
func (d *resumeDirector) allDeploys() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return slices.Clone(d.deploys)
}

// cancelled returns the tasks a cancel was sent for.
func (d *resumeDirector) cancelled() []int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return slices.Clone(d.cancels)
}

// waitForWaitOn waits until the manager waits on the task.
func (d *resumeDirector) waitForWaitOn(t *testing.T, taskID int) {
	t.Helper()

	waitUntil(t, fmt.Sprintf("a wait on BOSH task %d", taskID), func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()

		return d.waiting[taskID]
	})
}

// waitForDeployOf waits until a deploy of the deployment was sent.
func (d *resumeDirector) waitForDeployOf(t *testing.T, deployment string) {
	t.Helper()

	waitUntil(t, "a deploy of "+deployment, func() bool { return d.deployCount(deployment) > 0 })
}

// processLogger counts, for each upgrade, how many times its processing
// started.
type processLogger struct {
	*logger.NoOpLogger

	mu        sync.Mutex
	processed map[string]int
}

func newProcessLogger() *processLogger {
	return &processLogger{NoOpLogger: &logger.NoOpLogger{}, processed: make(map[string]int)}
}

func (l *processLogger) Named(string) logger.Logger { return l }

func (l *processLogger) Info(msg string, args ...interface{}) {
	if msg != "Processing upgrade task %s" || len(args) == 0 {
		return
	}

	taskID, _ := args[0].(string)

	l.mu.Lock()
	l.processed[taskID]++
	l.mu.Unlock()
}

// processedCount returns how many times the upgrade's processing started.
func (l *processLogger) processedCount(taskID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.processed[taskID]
}

// waitUntil waits until done holds, and fails the test when it never does.
func waitUntil(t *testing.T, what string, done func() bool) {
	t.Helper()

	deadline := time.Now().Add(waitTimeout)

	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("%s never happened", what)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

// pendingInstance returns an instance an upgrade has not started.
func pendingInstance(instanceID, deployment string) InstanceUpgrade {
	return InstanceUpgrade{InstanceID: instanceID, DeploymentName: deployment, Status: InstanceStatusPending}
}

// pausedUpgrade returns the stored upgrade, paused by the operator.
func pausedUpgrade(instances ...InstanceUpgrade) UpgradeTask {
	task := storedUpgrade(loadedTaskID, instances...)
	task.Status = TaskStatusPaused
	task.Paused = true

	return task
}

// storeBatchLimit saves the upgrade settings with the limit given.
func storeBatchLimit(t *testing.T, vault interfaces.Vault, limit int) {
	t.Helper()

	err := vault.Put(context.Background(), upgradeSettingsVaultPath, Settings{MaxBatchJobs: limit})
	if err != nil {
		t.Fatalf("storing the upgrade settings: %v", err)
	}
}

// loadManagerLogging is loadManager with the logger given.
func loadManagerLogging(t *testing.T, log logger.Logger, director bosh.Director, vault interfaces.Vault) *Manager {
	t.Helper()

	manager := NewManager(log, director, vault)
	t.Cleanup(manager.Stop)

	waitForLoad(t, manager)

	return manager
}

// expectCompleted waits until the upgrade completes and returns it.
func expectCompleted(t *testing.T, manager *Manager, taskID string) UpgradeTask {
	t.Helper()

	return eventuallyWithin(t, manager, taskID, waitTimeout, "the completion of upgrade "+taskID, func(current UpgradeTask) bool {
		return current.Status == TaskStatusCompleted
	})
}

// expectQueued waits until the upgrade waits for a processing slot, which
// is when its cancel channel is registered.
func expectQueued(t *testing.T, manager *Manager, taskID string) {
	t.Helper()

	waitUntil(t, "the queueing of upgrade "+taskID, func() bool {
		manager.taskCancelLock.RLock()
		defer manager.taskCancelLock.RUnlock()

		_, queued := manager.taskCancelCh[taskID]

		return queued
	})
}

// holdTheOnlySlot stores a batch limit of 1 and a running upgrade whose
// deploy the director holds, so the resumed upgrade holds the only slot.
func holdTheOnlySlot(t *testing.T, director *resumeDirector, vault interfaces.Vault) {
	t.Helper()

	storeBatchLimit(t, vault, 1)
	director.holdDeployment(heldDeployment)
	storeUpgrade(t, vault, storedUpgrade(heldTaskID, pendingInstance("instance-held", heldDeployment)))
}

// expectQuiet watches the upgrade for quietPeriod and fails the test when
// it leaves the status given or a deploy of the deployment is sent.
func expectQuiet(t *testing.T, manager *Manager, director *resumeDirector, taskID string, status TaskStatus, deployment string) {
	t.Helper()

	time.Sleep(quietPeriod)

	current := eventuallyWithin(t, manager, taskID, 0, "a read of upgrade "+taskID, func(UpgradeTask) bool { return true })
	if current.Status != status {
		t.Fatalf("expected upgrade %s to stay %s, got %+v", taskID, status, current)
	}

	if count := director.deployCount(deployment); count != 0 {
		t.Fatalf("expected no deploy of %s, got %d", deployment, count)
	}
}

//nolint:paralleltest // the resumed upgrade waits on a shared test director
func TestLoadResumesTheUpgradeAutomatically(t *testing.T) {
	director := newResumeDirector()
	vault := newWaitVault()

	storeUpgrade(t, vault, storedUpgrade(loadedTaskID,
		inFlight(adoptedDeployment, InstanceStatusInterrupted, adoptedTaskID, loadedContextID),
		pendingInstance("instance-2", nextDeployment)))

	manager := loadManager(t, director, vault)

	done := expectCompleted(t, manager, loadedTaskID)

	if done.Instances[0].Status != InstanceStatusSuccess || done.Instances[1].Status != InstanceStatusSuccess {
		t.Fatalf("expected the settled instance and the next pending one to succeed, got %+v", done.Instances)
	}

	if deploys := director.allDeploys(); !slices.Equal(deploys, []string{nextDeployment}) {
		t.Fatalf("expected one deploy, of the pending instance, got %v", deploys)
	}
}

//nolint:paralleltest // the resumed upgrade waits on a shared test director
func TestLoadResumesARunningUpgradeWithNoInstanceInFlight(t *testing.T) {
	director := newResumeDirector()
	vault := newWaitVault()

	storeUpgrade(t, vault, storedUpgrade(loadedTaskID,
		pendingInstance("instance-1", firstDeployment),
		pendingInstance("instance-2", nextDeployment)))

	manager := loadManager(t, director, vault)

	done := expectCompleted(t, manager, loadedTaskID)

	if done.CompletedCount != 2 {
		t.Fatalf("expected both instances to succeed, got %+v", done)
	}

	if deploys := director.allDeploys(); !slices.Equal(deploys, []string{firstDeployment, nextDeployment}) {
		t.Fatalf("expected one deploy of each instance in order, got %v", deploys)
	}
}

//nolint:paralleltest // the resumed upgrade waits on a shared test director
func TestLoadLeavesAPausedUpgradePaused(t *testing.T) {
	director := newResumeDirector()
	vault := newWaitVault()

	storeUpgrade(t, vault, pausedUpgrade(pendingInstance("instance-1", firstDeployment)))

	manager := loadManager(t, director, vault)

	expectQuiet(t, manager, director, loadedTaskID, TaskStatusPaused, firstDeployment)

	err := manager.ResumeTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("resuming the paused upgrade: %v", err)
	}

	done := expectCompleted(t, manager, loadedTaskID)

	if done.Instances[0].Status != InstanceStatusSuccess || director.deployCount(firstDeployment) != 1 {
		t.Fatalf("expected the resume to deploy the pending instance once, got %+v and deploys %v", done.Instances, director.allDeploys())
	}
}

//nolint:paralleltest // the load runs on a shared test director
func TestCancelBeforeRestartIsNotResumed(t *testing.T) {
	director := newResumeDirector()
	vault := newWaitVault()

	settled := pendingInstance("instance-1", firstDeployment)
	settled.Status = InstanceStatusSuccess

	stored := storedUpgrade(loadedTaskID, settled,
		pendingInstance("instance-2", nextDeployment),
		pendingInstance("instance-3", "redis-instance-3"))
	stored.Status = TaskStatusCancelled
	stored.CompletedCount = 1

	storeUpgrade(t, vault, stored)

	manager := loadManager(t, director, vault)

	done := eventuallyWithin(t, manager, loadedTaskID, waitTimeout, "the skipping of the pending instances", func(current UpgradeTask) bool {
		return current.SkippedCount == 2
	})

	if done.Status != TaskStatusCancelled || done.Instances[1].Status != InstanceStatusSkipped || done.Instances[2].Status != InstanceStatusSkipped {
		t.Fatalf("expected the upgrade to stay cancelled with its pending instances skipped, got %+v", done)
	}

	eventuallySaved(t, vault, loadedTaskID, func(saved UpgradeTask) bool { return saved.SkippedCount == 2 })

	time.Sleep(quietPeriod)

	if deploys := director.allDeploys(); len(deploys) != 0 {
		t.Fatalf("expected nothing deployed, got %v", deploys)
	}
}

//nolint:paralleltest // the settle waits on a shared test director
func TestCancelDuringLoadSettlingStaysCancelled(t *testing.T) {
	director := newResumeDirector()
	director.holdAdopted()

	vault := newWaitVault()

	storeUpgrade(t, vault, storedUpgrade(loadedTaskID,
		inFlight(adoptedDeployment, InstanceStatusInterrupted, adoptedTaskID, loadedContextID),
		pendingInstance("instance-2", nextDeployment)))

	manager := loadManager(t, director, vault)

	director.waitForWaitOn(t, adoptedTaskID)

	err := manager.CancelTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("cancelling the settling upgrade: %v", err)
	}

	done := eventuallyWithin(t, manager, loadedTaskID, waitTimeout, "the skipping of the pending instance", func(current UpgradeTask) bool {
		return current.Instances[1].Status == InstanceStatusSkipped
	})

	if done.Status != TaskStatusCancelled || done.Instances[0].Status != InstanceStatusCancelled {
		t.Fatalf("expected the upgrade to stay cancelled with its settled instance cancelled, got %+v", done)
	}

	time.Sleep(quietPeriod)

	if deploys := director.allDeploys(); len(deploys) != 0 {
		t.Fatalf("expected nothing deployed, got %v", deploys)
	}

	if cancels := director.cancelled(); !slices.Equal(cancels, []int{adoptedTaskID}) {
		t.Fatalf("expected one cancel of the settling task, got %v", cancels)
	}
}

//nolint:paralleltest // the queued upgrade waits on a shared test director
func TestCancelWhileQueuedIsNotOverwritten(t *testing.T) {
	director := newResumeDirector()
	vault := newWaitVault()

	holdTheOnlySlot(t, director, vault)
	storeUpgrade(t, vault, pausedUpgrade(pendingInstance("instance-1", firstDeployment)))

	manager := loadManager(t, director, vault)

	director.waitForDeployOf(t, heldDeployment)

	err := manager.ResumeTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("resuming the paused upgrade: %v", err)
	}

	expectQueued(t, manager, loadedTaskID)

	err = manager.CancelTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("cancelling the queued upgrade: %v", err)
	}

	director.releaseDeployment(heldDeployment)
	expectCompleted(t, manager, heldTaskID)

	done := eventuallyWithin(t, manager, loadedTaskID, waitTimeout, "the skipping of the pending instance", func(current UpgradeTask) bool {
		return current.Instances[0].Status == InstanceStatusSkipped
	})

	expectQuiet(t, manager, director, loadedTaskID, TaskStatusCancelled, firstDeployment)

	if done.SkippedCount != 1 {
		t.Fatalf("expected the pending instance to be skipped once, got %+v", done)
	}
}

//nolint:paralleltest // the queued upgrade waits on a shared test director
func TestPauseWhileQueuedCanBeResumed(t *testing.T) {
	director := newResumeDirector()
	vault := newWaitVault()

	holdTheOnlySlot(t, director, vault)
	storeUpgrade(t, vault, pausedUpgrade(pendingInstance("instance-1", firstDeployment)))

	manager := loadManager(t, director, vault)

	director.waitForDeployOf(t, heldDeployment)

	err := manager.ResumeTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("resuming the paused upgrade: %v", err)
	}

	expectQueued(t, manager, loadedTaskID)

	err = manager.PauseTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("pausing the queued upgrade: %v", err)
	}

	director.releaseDeployment(heldDeployment)
	expectCompleted(t, manager, heldTaskID)

	expectQuiet(t, manager, director, loadedTaskID, TaskStatusPaused, firstDeployment)

	err = manager.ResumeTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("resuming the upgrade paused in the queue: %v", err)
	}

	expectCompleted(t, manager, loadedTaskID)

	if count := director.deployCount(firstDeployment); count != 1 {
		t.Fatalf("expected one deploy of the pending instance, got %d", count)
	}
}

//nolint:paralleltest // the settle waits on a shared test director
func TestPauseAndResumeDuringSettlingStartsOneProcessor(t *testing.T) {
	director := newResumeDirector()
	director.holdAdopted()

	vault := newWaitVault()
	log := newProcessLogger()

	storeUpgrade(t, vault, storedUpgrade(loadedTaskID,
		inFlight(adoptedDeployment, InstanceStatusInterrupted, adoptedTaskID, loadedContextID),
		pendingInstance("instance-2", nextDeployment)))

	manager := loadManagerLogging(t, log, director, vault)

	director.waitForWaitOn(t, adoptedTaskID)

	err := manager.PauseTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("pausing the settling upgrade: %v", err)
	}

	err = manager.ResumeTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("resuming the settling upgrade: %v", err)
	}

	director.release(adoptedTaskID)

	expectCompleted(t, manager, loadedTaskID)

	if processed := log.processedCount(loadedTaskID); processed != 1 {
		t.Fatalf("expected one processor of the upgrade, got %d", processed)
	}

	if deploys := director.allDeploys(); !slices.Equal(deploys, []string{nextDeployment}) {
		t.Fatalf("expected one deploy, of the pending instance, got %v", deploys)
	}
}

//nolint:paralleltest // the queued upgrade waits on a shared test director
func TestResumeWhileQueuedDoesNotEnqueueTwice(t *testing.T) {
	director := newResumeDirector()
	vault := newWaitVault()
	log := newProcessLogger()

	holdTheOnlySlot(t, director, vault)
	storeUpgrade(t, vault, pausedUpgrade(
		pendingInstance("instance-1", firstDeployment),
		pendingInstance("instance-2", nextDeployment)))

	manager := loadManagerLogging(t, log, director, vault)

	director.waitForDeployOf(t, heldDeployment)

	err := manager.ResumeTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("resuming the paused upgrade: %v", err)
	}

	expectQueued(t, manager, loadedTaskID)

	err = manager.PauseTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("pausing the queued upgrade: %v", err)
	}

	err = manager.ResumeTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("resuming the queued upgrade again: %v", err)
	}

	director.releaseDeployment(heldDeployment)

	done := expectCompleted(t, manager, loadedTaskID)

	time.Sleep(quietPeriod)

	if done.CompletedCount != 2 || director.deployCount(firstDeployment) != 1 || director.deployCount(nextDeployment) != 1 {
		t.Fatalf("expected each pending instance deployed once, got %+v and deploys %v", done, director.allDeploys())
	}

	if processed := log.processedCount(loadedTaskID); processed != 1 {
		t.Fatalf("expected one processor of the upgrade, got %d", processed)
	}
}

//nolint:paralleltest // the settle waits on a shared test director
func TestResumeDuringSettlingDoesNotRedeployTheSettlingInstance(t *testing.T) {
	director := newResumeDirector()
	director.holdAdopted()

	vault := newWaitVault()

	storeUpgrade(t, vault, pausedUpgrade(
		inFlight(adoptedDeployment, InstanceStatusInterrupted, adoptedTaskID, loadedContextID),
		pendingInstance("instance-2", nextDeployment)))

	manager := loadManager(t, director, vault)

	director.waitForWaitOn(t, adoptedTaskID)

	err := manager.ResumeTask(context.Background(), loadedTaskID)
	if err != nil {
		t.Fatalf("resuming the settling upgrade: %v", err)
	}

	time.Sleep(quietPeriod)

	if deploys := director.allDeploys(); len(deploys) != 0 {
		t.Fatalf("expected nothing deployed while the instance settles, got %v", deploys)
	}

	director.release(adoptedTaskID)

	done := expectCompleted(t, manager, loadedTaskID)

	if settled := done.Instances[0]; settled.Status != InstanceStatusSuccess || settled.BOSHTaskID != adoptedTaskID {
		t.Fatalf("expected the settling instance to settle as a success from BOSH task %d, got %+v", adoptedTaskID, settled)
	}

	if deploys := director.allDeploys(); !slices.Equal(deploys, []string{nextDeployment}) {
		t.Fatalf("expected one deploy, of the pending instance, got %v", deploys)
	}
}

//nolint:paralleltest // the settle waits on a shared test director
func TestEachUpgradeResumesWithoutWaitingForAnother(t *testing.T) {
	const otherTaskID = "other-upgrade"

	director := newResumeDirector()
	director.holdAdopted()

	vault := newWaitVault()

	storeUpgrade(t, vault, storedUpgrade(loadedTaskID,
		inFlight(adoptedDeployment, InstanceStatusInterrupted, adoptedTaskID, loadedContextID)))
	storeUpgrade(t, vault, storedUpgrade(otherTaskID, pendingInstance("instance-2", nextDeployment)))

	manager := loadManager(t, director, vault)

	expectCompleted(t, manager, otherTaskID)

	settling := eventuallyWithin(t, manager, loadedTaskID, 0, "a read of the settling upgrade", func(UpgradeTask) bool { return true })
	if settling.Instances[0].Status != InstanceStatusInterrupted {
		t.Fatalf("expected the other upgrade's instance to be still settling, got %+v", settling.Instances[0])
	}

	director.release(adoptedTaskID)

	expectCompleted(t, manager, loadedTaskID)
}

//nolint:paralleltest // the resumed upgrade waits on a shared test director
func TestResumeKeepsTheFirstStartTime(t *testing.T) {
	director := newResumeDirector()
	vault := newWaitVault()

	stored := storedUpgrade(loadedTaskID, pendingInstance("instance-1", firstDeployment))
	storeUpgrade(t, vault, stored)

	manager := loadManager(t, director, vault)

	done := expectCompleted(t, manager, loadedTaskID)

	if done.StartedAt == nil || !done.StartedAt.Equal(*stored.StartedAt) {
		t.Fatalf("expected the upgrade to keep its first start time %v, got %v", stored.StartedAt, done.StartedAt)
	}
}

//nolint:paralleltest // it lowers the package's queue capacity
func TestResumeQueuesMoreUpgradesThanTheQueueHolds(t *testing.T) {
	saved := taskQueueCapacity
	taskQueueCapacity = 2

	t.Cleanup(func() { taskQueueCapacity = saved })

	director := newResumeDirector()
	vault := newWaitVault()

	taskIDs := []string{"upgrade-1", "upgrade-2", "upgrade-3"}
	for i, taskID := range taskIDs {
		instanceID := fmt.Sprintf("instance-%d", i+1)
		storeUpgrade(t, vault, storedUpgrade(taskID, pendingInstance(instanceID, "redis-"+instanceID)))
	}

	manager := loadManager(t, director, vault)

	for _, taskID := range taskIDs {
		expectCompleted(t, manager, taskID)
	}
}
