package upgrade

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/interfaces"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/google/uuid"
)

// Vault paths for storing upgrade data.
const (
	upgradeTasksVaultPath    = "upgrade-tasks"
	upgradeSettingsVaultPath = "upgrade-settings"
)

// cancelTaskTimeout bounds the director call that cancels a running upgrade
// task, so a hung director cannot hold up a cancel.
const cancelTaskTimeout = 30 * time.Second

// cancelWaitTimeout bounds how long an upgrade instance keeps waiting for
// its deploy task once the upgrade is cancelled. A test shortens it.
//
//nolint:gochecknoglobals // a test seam
var cancelWaitTimeout = 5 * time.Minute

// awaitTaskByContextID looks for the task of a deploy whose answer was
// lost when no batch watcher looks for it. A test replaces it.
//
//nolint:gochecknoglobals // a test seam
var awaitTaskByContextID = bosh.AwaitTaskByContextID

// contextIDWaiter is a director that can wait for the task of a deploy
// whose answer was lost, by the deploy's context ID, and report the task's
// ID to found as soon as it finds the task. The batch director is one, and
// its watcher serves the wait.
type contextIDWaiter interface {
	WaitForContextIDReporting(ctx context.Context, contextID string, found func(taskID int)) (*bosh.Task, error)
}

// Final states of a director task.
const (
	boshTaskDone      = "done"
	boshTaskCancelled = "cancelled"
)

// loadRetryBackoff is how long the loader waits before it reads Vault again
// after a failed read. The wait doubles after each failure, up to
// loadRetryBackoffMax. A test shortens it.
//
//nolint:gochecknoglobals // a test seam
var loadRetryBackoff = 5 * time.Second

// loadRetryBackoffMax is the longest the loader waits between reads.
const loadRetryBackoffMax = time.Minute

// taskQueueCapacity is how many upgrades the queue holds before a send
// waits for the dispatcher. A test lowers it.
//
//nolint:gochecknoglobals // a test seam
var taskQueueCapacity = 100

// noDirectorMessage is what an instance whose deploy was in flight at a
// restart says when the broker has no BOSH director to settle it with.
const noDirectorMessage = "Blacksmith has no BOSH director configured, so it cannot settle this deploy until it restarts with one"

// adopter is a director that can count a deploy task started before a
// restart against its limit and follow it. The batch director is one.
type adopter interface {
	Adopt(taskID int)
}

// persistTaskTimeout bounds the Vault writes that save an upgrade task.
const persistTaskTimeout = 10 * time.Second

// Error variables for err113 compliance.
var (
	ErrTaskNotFound    = errors.New("upgrade task not found")
	ErrNoInstances     = errors.New("no instances specified for upgrade")
	ErrInvalidStemcell = errors.New("invalid stemcell target")
	ErrTaskNotRunning  = errors.New("task is not running")
	ErrTaskNotPaused   = errors.New("task is not paused")
)

// Manager manages upgrade tasks.
type Manager struct {
	mu       sync.RWMutex
	tasks    map[string]*UpgradeTask
	logger   interfaces.Logger
	director interfaces.Director
	vault    interfaces.Vault

	// Task execution control
	taskQueue chan *UpgradeTask
	stopCh    chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once
	wg        sync.WaitGroup

	// ctx bounds every director call an upgrade makes, and Stop cancels it.
	ctx    context.Context
	cancel context.CancelFunc

	// Cancellation tracking - maps taskID to cancel channel
	taskCancelCh   map[string]chan struct{}
	taskCancelLock sync.RWMutex

	// Settings
	settings     Settings
	settingsLock sync.RWMutex

	// Parallel job control - nil means no limit
	jobSemaphore chan struct{}

	// Callback when max_batch_jobs changes (used to update BatchDirector pool)
	onMaxBatchJobsChanged func(maxJobs int)

	// snapshotSeq numbers the copies of tasks that are saved, in the order
	// they were taken under mu, so a save never overwrites a newer one.
	snapshotSeq atomic.Uint64

	// persistMu guards persisted, which holds the save state of each task.
	persistMu sync.Mutex
	persisted map[string]*taskPersistence

	// indexMu is held across each read-modify-write of the tasks index, so
	// two tasks saved at once never lose each other's entries. A task's own
	// save lock is taken before it, and mu is never taken while either is
	// held.
	indexMu sync.Mutex

	// settling holds, under mu, how many instances of each loaded upgrade
	// still wait for the deploy that was in flight when the broker stopped.
	settling map[string]int

	// loaded is closed once the loader has read the stored upgrades and
	// started settling their instances, or Stop ended it first.
	loaded chan struct{}

	// owned holds, under mu, each upgrade that is queued or being
	// processed, so only one processor ever runs an upgrade.
	owned map[string]bool

	// lookups holds, under mu, how many loaded deploys saved without a
	// task ID are still being looked up, by their context ID or by their
	// deployment. No upgrade resumes while it is above zero, so a task a
	// lookup finds is adopted into the batch limit before any resumed
	// deploy can take a slot.
	lookups int

	// heldResumes holds, under mu, the loaded upgrades that resume once
	// the last lookup ends.
	heldResumes []*UpgradeTask
}

// taskSnapshot is a copy of an upgrade task taken under the manager's lock,
// numbered in the order the copies were taken.
type taskSnapshot struct {
	task *UpgradeTask
	seq  uint64
}

// taskPersistence is the save state of one upgrade task. mu is held across
// each save of the task, and written is the number of the newest snapshot
// saved.
type taskPersistence struct {
	mu      sync.Mutex
	written uint64
}

// NewManager creates a new upgrade manager and loads its settings. It
// starts nothing, so the caller can apply the persisted batch limit before
// Start resumes any upgrade.
func NewManager(logger interfaces.Logger, director interfaces.Director, vault interfaces.Vault) *Manager {
	m := &Manager{
		tasks:        make(map[string]*UpgradeTask),
		logger:       logger.Named("upgrade-manager"),
		director:     director,
		vault:        vault,
		taskQueue:    make(chan *UpgradeTask, taskQueueCapacity),
		stopCh:       make(chan struct{}),
		taskCancelCh: make(map[string]chan struct{}),
		settling:     make(map[string]int),
		loaded:       make(chan struct{}),
		owned:        make(map[string]bool),
	}

	// The manager's context is a root. The manager lives as long as the
	// broker, NewManager receives no context, and Stop is what ends it, so
	// every director call an upgrade makes descends from this context.
	m.ctx, m.cancel = context.WithCancel(context.Background())

	// Load persisted settings from vault
	ctx := context.Background()
	if err := m.loadSettings(ctx); err != nil {
		m.logger.Error("Failed to load settings: %v", err)
	}

	// Initialize job semaphore based on settings
	m.updateJobSemaphore()

	return m
}

// Start starts the task dispatcher and the loader, which loads the
// persisted tasks, settles the deploys they left in flight, and resumes
// them. A second call, or a call after Stop, does nothing.
func (m *Manager) Start() {
	m.startOnce.Do(func() {
		if m.ctx.Err() != nil {
			close(m.loaded)

			return
		}

		m.wg.Add(1)

		go m.loadAndSettle()

		m.wg.Add(1)

		go m.dispatchTasks()
	})
}

// Stop stops the manager and waits for pending operations to complete. A
// second call does nothing.
func (m *Manager) Stop() {
	m.stopOnce.Do(func() {
		close(m.stopCh)
		m.cancel()
		m.wg.Wait()
	})
}

// CreateTask creates a new upgrade task.
func (m *Manager) CreateTask(ctx context.Context, req CreateTaskRequest) (*UpgradeTask, error) {
	if len(req.InstanceIDs) == 0 {
		return nil, ErrNoInstances
	}

	if req.TargetStemcell.OS == "" || req.TargetStemcell.Version == "" {
		return nil, ErrInvalidStemcell
	}

	m.logger.Info("Creating upgrade task for %d instances to stemcell %s/%s",
		len(req.InstanceIDs), req.TargetStemcell.OS, req.TargetStemcell.Version)

	// Build instance list with deployment names
	instances, err := m.buildInstanceList(ctx, req.InstanceIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to build instance list: %w", err)
	}

	task := &UpgradeTask{
		ID:             uuid.New().String(),
		Name:           req.Name,
		Status:         TaskStatusPending,
		TargetStemcell: req.TargetStemcell,
		Instances:      instances,
		TotalCount:     len(instances),
		CompletedCount: 0,
		FailedCount:    0,
		CreatedAt:      time.Now(),
	}

	m.mu.Lock()
	m.tasks[task.ID] = task
	m.owned[task.ID] = true
	created := copyTask(task)
	m.mu.Unlock()

	// Persist task to vault
	if err := m.persistTask(ctx, task); err != nil {
		m.logger.Error("Failed to persist task %s: %v", task.ID, err)
		// Continue anyway - task is in memory
	}

	m.enqueue(task)

	return created, nil
}

// buildInstanceList builds the instance upgrade list with deployment information from vault.
func (m *Manager) buildInstanceList(ctx context.Context, instanceIDs []string) ([]InstanceUpgrade, error) {
	instances := make([]InstanceUpgrade, 0, len(instanceIDs))

	for _, instanceID := range instanceIDs {
		instance := InstanceUpgrade{
			InstanceID: instanceID,
			Status:     InstanceStatusPending,
		}

		// Get instance info from vault
		var instanceData map[string]interface{}

		exists, err := m.vault.Get(ctx, instanceID, &instanceData)
		if err != nil {
			m.logger.Error("Failed to get instance %s from vault: %v", instanceID, err)
			instance.Error = fmt.Sprintf("failed to get instance data: %v", err)
			instance.Status = InstanceStatusFailed
		} else if !exists {
			m.logger.Error("Instance %s not found in vault", instanceID)
			instance.Error = "instance not found in vault"
			instance.Status = InstanceStatusFailed
		} else {
			// Extract deployment name and other info
			if deploymentName, ok := instanceData["deployment_name"].(string); ok && deploymentName != "" {
				instance.DeploymentName = deploymentName
			}

			if instanceName, ok := instanceData["instance_name"].(string); ok {
				instance.InstanceName = instanceName
			}

			if serviceID, ok := instanceData["service_id"].(string); ok {
				instance.ServiceID = serviceID
			}

			if planID, ok := instanceData["plan_id"].(string); ok {
				instance.PlanID = planID
			}

			// If deployment_name is not set, try to construct it from plan_id + instance_id
			if instance.DeploymentName == "" && instance.PlanID != "" {
				instance.DeploymentName = instance.PlanID + "-" + instanceID
			}
		}

		instances = append(instances, instance)
	}

	return instances, nil
}

// GetTask retrieves a copy of a task by ID. The copy is taken under the
// manager's lock, so a caller reads it while the upgrade goes on.
func (m *Manager) GetTask(taskID string) (*UpgradeTask, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	task, exists := m.tasks[taskID]
	if !exists {
		return nil, ErrTaskNotFound
	}

	return copyTask(task), nil
}

// ListTasks returns a copy of every task.
func (m *Manager) ListTasks() []*UpgradeTask {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tasks := make([]*UpgradeTask, 0, len(m.tasks))
	for _, task := range m.tasks {
		tasks = append(tasks, copyTask(task))
	}

	return tasks
}

// dispatchTasks dispatches tasks from the queue to be processed in parallel.
func (m *Manager) dispatchTasks() {
	defer m.wg.Done()

	for {
		select {
		case <-m.stopCh:
			m.logger.Info("Upgrade manager stopping")

			return
		case task := <-m.taskQueue:
			// Spawn a goroutine for each task to allow parallel processing
			m.wg.Add(1)

			go m.runTask(task)
		}
	}
}

// upgradeInstance upgrades a single instance. It saves a fresh context ID
// for the deploy, sends the deploy with that ID, saves the deploy's task
// ID at once, and then waits for that task to end, so the outcome comes
// from the task's own final state. A deploy whose answer was lost is never
// marked failed for that, and the instance waits for its task by the
// context ID instead.
func (m *Manager) upgradeInstance(ctx context.Context, task *UpgradeTask, instance *InstanceUpgrade, cancelCh <-chan struct{}) {
	// Progress is saved even after Stop ends ctx, so the stored task shows
	// how far the upgrade got.
	persistCtx := context.WithoutCancel(ctx)
	m.logger.Info("Upgrading instance %s (deployment: %s)", instance.InstanceID, instance.DeploymentName)

	now := time.Now()

	// A cancel collects the task IDs of running instances, so an instance
	// starts only while the upgrade is not cancelled, checked in the same
	// step that marks it running.
	m.mu.Lock()
	if task.Status == TaskStatusCancelled {
		m.mu.Unlock()
		m.logger.Info("Task %s was cancelled, so instance %s is not started", task.ID, instance.InstanceID)

		// This instance may be the last one, which no later pass of the
		// upgrade's loop would skip.
		m.skipPendingInstances(persistCtx, task)

		return
	}

	instance.StartedAt = &now
	instance.Status = InstanceStatusRunning
	m.mu.Unlock()

	// Persist running state immediately so the UI shows the instance as running
	m.persistTask(persistCtx, task) //nolint:errcheck

	// Validate deployment name
	if instance.DeploymentName == "" {
		m.mu.Lock()
		instance.Status = InstanceStatusFailed
		instance.Error = "deployment name is missing"
		task.FailedCount++
		m.mu.Unlock()
		m.logger.Error("Instance %s has no deployment name", instance.InstanceID)
		m.persistTask(persistCtx, task) //nolint:errcheck
		return
	}

	// Get current deployment manifest
	m.logger.Debug("Getting deployment %s for instance %s", instance.DeploymentName, instance.InstanceID)
	deployment, err := m.director.GetDeployment(ctx, instance.DeploymentName)
	if err != nil && stoppedBeforeTheDeploy(ctx, err) {
		m.returnToPending(persistCtx, task, instance, err)

		return
	}

	if err != nil {
		m.mu.Lock()
		instance.Status = InstanceStatusFailed
		instance.Error = fmt.Sprintf("failed to get deployment: %v", err)
		task.FailedCount++
		m.mu.Unlock()
		m.logger.Error("Failed to get deployment %s: %v", instance.DeploymentName, err)
		m.persistTask(persistCtx, task) //nolint:errcheck
		return
	}

	if deployment == nil {
		m.mu.Lock()
		instance.Status = InstanceStatusFailed
		instance.Error = "deployment returned nil without error"
		task.FailedCount++
		m.mu.Unlock()
		m.logger.Error("Deployment %s returned nil without error", instance.DeploymentName)
		m.persistTask(persistCtx, task) //nolint:errcheck
		return
	}

	m.logger.Debug("Got deployment %s, manifest size: %d bytes", instance.DeploymentName, len(deployment.Manifest))

	// Merge stemcell overlay
	m.logger.Debug("Merging stemcell overlay for %s: %s/%s",
		instance.DeploymentName, task.TargetStemcell.OS, task.TargetStemcell.Version)

	newManifest, err := MergeStemcellOverlay(deployment.Manifest,
		task.TargetStemcell.OS, task.TargetStemcell.Version)
	if err != nil && stoppedBeforeTheDeploy(ctx, err) {
		m.returnToPending(persistCtx, task, instance, err)

		return
	}

	if err != nil {
		m.mu.Lock()
		instance.Status = InstanceStatusFailed
		instance.Error = fmt.Sprintf("failed to merge stemcell overlay: %v", err)
		task.FailedCount++
		m.mu.Unlock()
		m.logger.Error("Failed to merge stemcell overlay for %s: %v", instance.DeploymentName, err)
		m.persistTask(persistCtx, task) //nolint:errcheck
		return
	}

	m.logger.Debug("Stemcell overlay merged successfully for %s, new manifest size: %d bytes",
		instance.DeploymentName, len(newManifest))

	// The context ID is saved before the deploy is sent, so a broker that
	// stops while the deploy is in flight leaves it on record, and the
	// deploy's task can still be found by it.
	contextID := uuid.NewString()

	m.mu.Lock()
	instance.ContextID = contextID
	m.mu.Unlock()

	err = m.persistTask(persistCtx, task)
	if err != nil {
		m.logger.Error("Failed to save context ID %s of the deploy of %s before sending it: %v", contextID, instance.DeploymentName, err)
	}

	deployTask, err := m.director.UpdateDeployment(boshapi.WithContextID(ctx, contextID), instance.DeploymentName, newManifest)
	if errors.Is(err, bosh.ErrOutcomeUnknown) {
		m.awaitLostAnswer(ctx, task, instance, contextID, err, cancelCh)

		return
	}

	// Any error but a lost answer shows the director never received the
	// deploy, including a wait for a batch slot that Stop ended.
	if err != nil && stoppedBeforeTheDeploy(ctx, err) {
		m.returnToPending(persistCtx, task, instance, err)

		return
	}

	if err != nil {
		m.mu.Lock()
		instance.Status = InstanceStatusFailed
		instance.Error = fmt.Sprintf("BOSH task failed: %v", err)
		task.FailedCount++
		m.logger.Error("Update failed for %s: %v", instance.DeploymentName, err)
		m.mu.Unlock()
		m.persistTask(persistCtx, task) //nolint:errcheck
		return
	}

	if deployTask == nil || deployTask.ID <= 0 {
		m.mu.Lock()
		instance.Status = InstanceStatusFailed
		instance.Error = "the director named no task for the deploy"
		task.FailedCount++
		m.logger.Error("The director named no task for the deploy of %s", instance.DeploymentName)
		m.mu.Unlock()
		m.persistTask(persistCtx, task) //nolint:errcheck
		return
	}

	m.followDeployTask(ctx, task, instance, deployTask.ID, cancelCh)
}

// followDeployTask saves the deploy's task ID and waits for the task to
// end, then records the instance's outcome from the task's final state.
func (m *Manager) followDeployTask(ctx context.Context, task *UpgradeTask, instance *InstanceUpgrade, taskID int, cancelCh <-chan struct{}) {
	persistCtx := context.WithoutCancel(ctx)

	// The task ID is saved before the wait, so the UI shows it while the
	// deploy runs, and a broker that stops during the wait leaves it on
	// record. A cancel that came after the instance started but before
	// this point found no task ID to cancel, so the upgrade cancels the
	// task itself. The ID and the upgrade's status are read and written
	// under the same lock as CancelTask's, so exactly one side sends it.
	m.mu.Lock()
	instance.BOSHTaskID = taskID
	cancelledBeforeTheTask := task.Status == TaskStatusCancelled
	m.mu.Unlock()
	m.logger.Info("BOSH task %d deploys %s for instance %s", taskID, instance.DeploymentName, instance.InstanceID)

	if cancelledBeforeTheTask {
		m.cancelDeployTask(ctx, instance.InstanceID, taskID)
	}

	m.persistTask(persistCtx, task) //nolint:errcheck

	finished, cancelRequested, err := m.awaitDeployTask(ctx, task, instance, taskID, cancelCh)
	if err != nil {
		if errors.Is(err, bosh.ErrWatchAbandoned) || bosh.IsTaskNotFound(err) {
			// Blacksmith no longer follows the task, or the director no
			// longer has it, so its outcome will not be known here.
			m.settleOutcomeUnknown(task, instance, taskID, err.Error())
			m.persistTask(persistCtx, task) //nolint:errcheck

			return
		}

		if cancelRequested && waitStopped(ctx, err) {
			// Stop ended the wait after the cancel, so the task may still
			// run on the director, and its ID stays on record.
			m.interruptAtShutdown(persistCtx, task, instance,
				fmt.Sprintf("Blacksmith stopped waiting at shutdown after a cancel was requested for BOSH task %d", taskID))

			return
		}

		if cancelRequested {
			// The task outlived the wait after the cancel, or could not be
			// read, so its outcome is not known yet.
			message := fmt.Sprintf("cancel requested for BOSH task %d", taskID)

			m.mu.Lock()
			instance.Error = message
			m.mu.Unlock()
			m.logger.Error("Instance %s: %s, and the task had not ended when Blacksmith stopped waiting: %v", instance.InstanceID, message, err)
			m.persistTask(persistCtx, task) //nolint:errcheck
			return
		}

		if waitStopped(ctx, err) {
			// Stop ended the wait. The task keeps running on the director,
			// and its ID stays on record.
			m.interruptAtShutdown(persistCtx, task, instance,
				fmt.Sprintf("Blacksmith stopped waiting at shutdown; BOSH task %d is still running", taskID))

			return
		}

		m.mu.Lock()
		instance.Status = InstanceStatusFailed
		instance.Error = fmt.Sprintf("stopped waiting for BOSH task %d, which may still be running: %v", taskID, err)
		task.FailedCount++
		m.logger.Error("Stopped waiting for BOSH task %d of %s: %v", taskID, instance.DeploymentName, err)
		m.mu.Unlock()
		m.persistTask(persistCtx, task) //nolint:errcheck
		return
	}

	m.settleInstance(task, instance, taskID, finished)
	m.persistTask(persistCtx, task) //nolint:errcheck
}

// awaitLostAnswer waits for the task of a deploy whose answer was lost,
// and records the instance's outcome once the wait settles it. It saves
// when the answer was lost, and never marks the instance failed before
// the wait settles it. A batch director's watcher serves the wait when one
// looks for the context ID, and the ID of the task it finds is stored as
// soon as the task is found, so a cancel reaches the task. Otherwise
// AwaitTaskByContextID looks for the task, from the time the answer was
// lost, and a task it finds is followed like any accepted deploy. When
// Stop ends the wait, the instance is marked interrupted.
func (m *Manager) awaitLostAnswer(ctx context.Context, task *UpgradeTask, instance *InstanceUpgrade, contextID string, lost error, cancelCh <-chan struct{}) {
	persistCtx := context.WithoutCancel(ctx)
	unknownSince := time.Now()

	m.mu.Lock()
	instance.UnknownSince = &unknownSince
	instance.Error = "answer lost; looking for the BOSH task by context ID " + contextID
	m.mu.Unlock()

	m.logger.Error("The answer to the deploy of %s for instance %s was lost, so Blacksmith looks for its BOSH task by context ID %s: %v",
		instance.DeploymentName, instance.InstanceID, contextID, lost)

	err := m.persistTask(persistCtx, task)
	if err != nil {
		m.logger.Error("Failed to save when the answer to the deploy of %s was lost: %v", instance.DeploymentName, err)
	}

	if waiter, ok := m.director.(contextIDWaiter); ok {
		found := func(taskID int) { m.storeFoundTask(ctx, task, instance, contextID, taskID) }
		finished, err := waiter.WaitForContextIDReporting(ctx, contextID, found)

		switch {
		case err == nil:
			taskID := 0
			if finished != nil {
				taskID = finished.ID
			}

			m.mu.Lock()
			instance.BOSHTaskID = taskID
			m.mu.Unlock()

			m.settleInstance(task, instance, taskID, finished)
			m.persistTask(persistCtx, task) //nolint:errcheck

			return
		case waitStopped(ctx, err):
			m.logger.Info("Blacksmith stopped waiting for the deploy of %s with context ID %s, whose answer was lost: %v", instance.DeploymentName, contextID, err)
			m.interruptLostAnswer(persistCtx, task, instance, contextID)

			return
		case !errors.Is(err, bosh.ErrNoWatcherForContextID) && m.settleLostAnswer(task, instance, contextID, err):
			m.persistTask(persistCtx, task) //nolint:errcheck

			return
		}

		m.logger.Info("No batch watcher settled the deploy of %s with context ID %s, so Blacksmith looks for its task on the director: %v", instance.DeploymentName, contextID, err)
	}

	found, err := awaitTaskByContextID(ctx, m.director, contextID, unknownSince)
	if err != nil {
		if waitStopped(ctx, err) {
			m.logger.Info("Blacksmith stopped looking for the deploy of %s with context ID %s, whose answer was lost: %v", instance.DeploymentName, contextID, err)
			m.interruptLostAnswer(persistCtx, task, instance, contextID)

			return
		}

		if !m.settleLostAnswer(task, instance, contextID, err) {
			m.settleOutcomeUnknown(task, instance, 0, err.Error())
		}

		m.persistTask(persistCtx, task) //nolint:errcheck

		return
	}

	if found == nil || found.ID <= 0 {
		m.settleOutcomeUnknown(task, instance, 0, "the lookup by context ID named no task")
		m.persistTask(persistCtx, task) //nolint:errcheck

		return
	}

	m.logger.Info("Found BOSH task %d for the deploy of %s with context ID %s, whose answer was lost", found.ID, instance.DeploymentName, contextID)
	m.followDeployTask(ctx, task, instance, found.ID, cancelCh)
}

// stoppedBeforeTheDeploy reports whether a step before the director
// received the deploy failed because Blacksmith is stopping, either because
// Stop ended ctx or because the batch director stopped.
func stoppedBeforeTheDeploy(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, bosh.ErrBatchDirectorStopped)
}

// waitStopped reports whether a wait ended because Stop ended it, either
// the manager's own Stop, which ends ctx, or the batch director's, which
// ends its watchers with context.Canceled.
func waitStopped(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled)
}

// settleLostAnswer records the outcome of an instance whose deploy lost
// its answer, when the wait for its task ended with an error that settles
// it, and reports whether it did. A later deploy of the deployment skips
// the instance, a watch that gave up leaves its outcome unknown, and no
// task with the context ID fails it.
func (m *Manager) settleLostAnswer(task *UpgradeTask, instance *InstanceUpgrade, contextID string, err error) bool {
	switch {
	case errors.Is(err, bosh.ErrLostAnswerSuperseded):
		m.mu.Lock()
		defer m.mu.Unlock()

		replaced := "a later deploy of " + instance.DeploymentName + " replaced the deploy whose answer was lost"

		var superseded *bosh.LostAnswerSupersededError
		if errors.As(err, &superseded) {
			replaced = superseded.Error()
		}

		completedAt := time.Now()
		instance.CompletedAt = &completedAt
		instance.Status = InstanceStatusSkipped
		instance.Error = fmt.Sprintf("skipped, because %s (context ID %s)", replaced, contextID)
		task.SkippedCount++

		m.logger.Info("Instance %s: %s", instance.InstanceID, instance.Error)

		return true
	case errors.Is(err, bosh.ErrWatchAbandoned):
		m.settleOutcomeUnknown(task, instance, 0, err.Error())

		return true
	case errors.Is(err, bosh.ErrNoTaskWithContextID):
		m.mu.Lock()
		defer m.mu.Unlock()

		completedAt := time.Now()
		instance.CompletedAt = &completedAt
		instance.Status = InstanceStatusFailed
		instance.Error = fmt.Sprintf("no BOSH task with context ID %s turned up within %v after the deploy's answer was lost", contextID, bosh.UnknownOutcomeGrace)
		task.FailedCount++

		m.logger.Error("Instance %s: %s: %v", instance.InstanceID, instance.Error, err)

		return true
	default:
		return false
	}
}

// settleOutcomeUnknown records that Blacksmith stopped following the
// instance's deploy before its outcome was known. The instance counts as
// finished, so the upgrade moves on, and it neither fails the upgrade nor
// counts as a success. taskID is the BOSH task that was followed, or 0
// when none was, and reason says why the following stopped, for the log.
func (m *Manager) settleOutcomeUnknown(task *UpgradeTask, instance *InstanceUpgrade, taskID int, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	followed := ""
	if taskID > 0 {
		followed = fmt.Sprintf(" (BOSH task %d)", taskID)
	}

	completedAt := time.Now()
	instance.CompletedAt = &completedAt
	instance.Status = InstanceStatusOutcomeUnknown
	instance.Error = fmt.Sprintf("Blacksmith stopped following the deploy with context ID %s%s before its outcome was known. "+
		"Check the director's task list for that context ID.", instance.ContextID, followed)
	task.UnknownCount++

	m.logger.Error("Instance %s of %s: %s: %s", instance.InstanceID, instance.DeploymentName, instance.Error, reason)
}

// awaitDeployTask waits for the deploy task of an upgrade instance to end.
// The cancel itself is sent by CancelTask, or by upgradeInstance when the
// cancel came before the deploy named its task. When the upgrade is cancelled
// during the wait, it keeps waiting for the task's own end under a
// deadline of cancelWaitTimeout, which Stop also ends, and reports that a
// cancel was requested. A signal on cancelCh that is not a cancel,
// such as a resume, changes nothing.
func (m *Manager) awaitDeployTask(ctx context.Context, task *UpgradeTask, instance *InstanceUpgrade, taskID int, cancelCh <-chan struct{}) (*bosh.Task, bool, error) {
	type waitResult struct {
		task *bosh.Task
		err  error
	}

	waitCtx, stopWaiting := context.WithCancel(ctx)
	defer stopWaiting()

	waited := make(chan waitResult, 1)

	go func() {
		finished, err := m.director.WaitForTask(waitCtx, taskID)
		waited <- waitResult{task: finished, err: err}
	}()

	for {
		select {
		case result := <-waited:
			return result.task, false, result.err
		case <-cancelCh:
			m.mu.RLock()
			cancelled := task.Status == TaskStatusCancelled
			m.mu.RUnlock()

			if !cancelled {
				continue
			}

			stopWaiting()

			// The wait may have ended with the task just before it was
			// stopped.
			if result := <-waited; result.err == nil {
				return result.task, true, nil
			}

			m.logger.Info("Task-level cancel for instance %s; waiting up to %s for BOSH task %d to end", instance.InstanceID, cancelWaitTimeout, taskID)

			afterCancel, cancel := context.WithTimeout(ctx, cancelWaitTimeout)
			finished, err := m.director.WaitForTask(afterCancel, taskID)

			cancel()

			return finished, true, err //nolint:wrapcheck // the caller adds the context
		}
	}
}

// settleInstance records the outcome of an upgrade instance from the final
// state of its deploy task. It alone settles an instance that started. A
// wait that ended with no task and no error is a failed wait.
func (m *Manager) settleInstance(task *UpgradeTask, instance *InstanceUpgrade, taskID int, finished *bosh.Task) {
	m.mu.Lock()
	defer m.mu.Unlock()

	completedAt := time.Now()
	instance.CompletedAt = &completedAt

	if finished == nil {
		instance.Status = InstanceStatusFailed
		instance.Error = fmt.Sprintf("the director returned no task when Blacksmith waited on BOSH task %d, which may still be running", taskID)
		task.FailedCount++

		m.logger.Error("The director returned no task for BOSH task %d of %s", taskID, instance.DeploymentName)

		return
	}

	switch finished.State {
	case boshTaskDone:
		instance.Status = InstanceStatusSuccess
		instance.Error = ""
		task.CompletedCount++
		m.logger.Info("Successfully upgraded instance %s", instance.InstanceID)
	case boshTaskCancelled:
		instance.Status = InstanceStatusCancelled
		instance.Error = fmt.Sprintf("BOSH task %d was cancelled", finished.ID)

		if task.Status == TaskStatusCancelled {
			instance.Error = "cancelled by user"
		}

		task.CancelledCount++
		m.logger.Info("BOSH task %d of %s was cancelled", finished.ID, instance.DeploymentName)
	default:
		instance.Status = InstanceStatusFailed
		instance.Error = fmt.Sprintf("BOSH task %d ended in state %s: %s", finished.ID, finished.State, finished.Result)
		task.FailedCount++
		m.logger.Error("Update failed for %s: BOSH task %d ended in state %s: %s", instance.DeploymentName, finished.ID, finished.State, finished.Result)
	}
}

// skipPendingInstances marks every instance of a cancelled upgrade that
// never started as skipped.
func (m *Manager) skipPendingInstances(ctx context.Context, task *UpgradeTask) {
	m.mu.Lock()
	skipped := m.skipPendingLocked(task)
	m.mu.Unlock()

	if skipped == 0 {
		return
	}

	m.logger.Info("Skipped %d pending instances of cancelled task %s", skipped, task.ID)

	err := m.persistTask(ctx, task)
	if err != nil {
		m.logger.Error("Failed to persist the skipped instances of task %s: %v", task.ID, err)
	}
}

// GetInstancesForTask retrieves a copy of the instances of a specific task.
func (m *Manager) GetInstancesForTask(taskID string) ([]InstanceUpgrade, error) {
	task, err := m.GetTask(taskID)
	if err != nil {
		return nil, err
	}

	return task.Instances, nil
}

// GetTaskWithRunningInstance returns the task that has a specific instance currently running.
func (m *Manager) GetTaskWithRunningInstance(instanceID string) (*UpgradeTask, *InstanceUpgrade) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, task := range m.tasks {
		if task.Status != TaskStatusRunning {
			continue
		}

		for i := range task.Instances {
			if task.Instances[i].InstanceID == instanceID && task.Instances[i].Status == InstanceStatusRunning {
				return task, &task.Instances[i]
			}
		}
	}

	return nil, nil
}

// PauseTask pauses a running task. Current instance continues, but next instances won't start.
func (m *Manager) PauseTask(ctx context.Context, taskID string) error {
	m.mu.Lock()

	task, exists := m.tasks[taskID]
	if !exists {
		m.mu.Unlock()

		return ErrTaskNotFound
	}

	if task.Status != TaskStatusRunning {
		m.mu.Unlock()

		return ErrTaskNotRunning
	}

	task.Paused = true
	task.Status = TaskStatusPaused

	// The paused state is copied under the lock that set it, and saved
	// after the lock is released, so no reader waits on Vault.
	paused := m.snapshotLocked(task)
	m.mu.Unlock()

	m.logger.Info("Paused task %s", taskID)

	// Persist the paused state
	err := m.writeSnapshot(ctx, paused)
	if err != nil {
		m.logger.Error("Failed to persist paused state for task %s: %v", taskID, err)
	}

	return nil
}

// ResumeTask resumes a paused task.
func (m *Manager) ResumeTask(ctx context.Context, taskID string) error {
	m.mu.Lock()
	task, exists := m.tasks[taskID]
	if !exists {
		m.mu.Unlock()
		return ErrTaskNotFound
	}

	if task.Status != TaskStatusPaused {
		m.mu.Unlock()
		return ErrTaskNotPaused
	}

	task.Paused = false
	task.Status = TaskStatusRunning
	m.logger.Info("Resumed task %s", taskID)
	m.mu.Unlock()

	// Persist the resumed state
	if err := m.persistTask(ctx, task); err != nil {
		m.logger.Error("Failed to persist resumed state for task %s: %v", taskID, err)
	}

	// Signal the task to continue processing
	m.taskCancelLock.RLock()
	if cancelCh, exists := m.taskCancelCh[taskID]; exists {
		select {
		case cancelCh <- struct{}{}:
		default:
		}
	}
	m.taskCancelLock.RUnlock()

	// An upgrade with no processor, as a loaded one has, is queued once
	// nothing of it is settling.
	m.resumeIfReady(task) //nolint:contextcheck // the resume outlives the request, so it runs under the manager's context

	return nil
}

// CancelTask cancels a running or paused task. It sends one cancel for the
// deploy task of each running or interrupted instance that has recorded
// one, since an instance whose deploy has not named its task yet cancels
// it itself. An interrupted instance is one whose deploy was in flight at a
// restart, and which a loaded upgrade is still settling. It
// leaves every instance's status and the counts to the upgrade, which
// records each instance's outcome from its task's final state. The
// manager's lock is never held across a director call.
func (m *Manager) CancelTask(ctx context.Context, taskID string) error {
	m.mu.Lock()
	task, exists := m.tasks[taskID]
	if !exists {
		m.mu.Unlock()
		return ErrTaskNotFound
	}

	if task.Status != TaskStatusRunning && task.Status != TaskStatusPaused {
		m.mu.Unlock()
		return ErrTaskNotRunning
	}

	m.logger.Info("Cancelling task %s", taskID)

	type runningDeploy struct {
		instanceID string
		boshTaskID int
	}

	var running []runningDeploy

	for i := range task.Instances {
		instance := &task.Instances[i]

		inFlight := instance.Status == InstanceStatusRunning || instance.Status == InstanceStatusInterrupted
		if inFlight && instance.BOSHTaskID > 0 {
			running = append(running, runningDeploy{instanceID: instance.InstanceID, boshTaskID: instance.BOSHTaskID})
		}
	}

	task.Status = TaskStatusCancelled
	completedAt := time.Now()
	task.CompletedAt = &completedAt
	m.mu.Unlock()

	for _, deploy := range running {
		m.cancelDeployTask(ctx, deploy.instanceID, deploy.boshTaskID)
	}

	// Signal cancellation
	m.taskCancelLock.Lock()
	if cancelCh, exists := m.taskCancelCh[taskID]; exists {
		close(cancelCh)
		delete(m.taskCancelCh, taskID)
	}
	m.taskCancelLock.Unlock()

	// Persist the cancelled state
	if err := m.persistTask(ctx, task); err != nil {
		m.logger.Error("Failed to persist cancelled state for task %s: %v", taskID, err)
	}

	return nil
}

// ManagerInterface defines the interface for the upgrade manager.
type ManagerInterface interface {
	CreateTask(ctx context.Context, req CreateTaskRequest) (*UpgradeTask, error)
	GetTask(taskID string) (*UpgradeTask, error)
	ListTasks() []*UpgradeTask
	GetInstancesForTask(taskID string) ([]InstanceUpgrade, error)
	PauseTask(ctx context.Context, taskID string) error
	ResumeTask(ctx context.Context, taskID string) error
	CancelTask(ctx context.Context, taskID string) error
	GetSettings() Settings
	UpdateSettings(ctx context.Context, settings Settings) error
	Stop()
}

// Ensure Manager implements ManagerInterface.
var _ ManagerInterface = (*Manager)(nil)

// persistTask saves an upgrade task to vault. It copies the task under the
// manager's read lock and saves the copy, so the caller must not hold mu.
func (m *Manager) persistTask(ctx context.Context, task *UpgradeTask) error {
	m.mu.RLock()
	snapshot := m.snapshotLocked(task)
	m.mu.RUnlock()

	return m.writeSnapshot(ctx, snapshot)
}

// loadSettings loads settings from vault.
func (m *Manager) loadSettings(ctx context.Context) error {
	m.logger.Debug("Loading upgrade settings from vault")

	var settings Settings

	exists, err := m.vault.Get(ctx, upgradeSettingsVaultPath, &settings)
	if err != nil {
		return fmt.Errorf("failed to get settings from vault: %w", err)
	}

	if exists {
		m.settingsLock.Lock()
		m.settings = settings
		m.settingsLock.Unlock()
		m.logger.Info("Loaded upgrade settings: max_batch_jobs=%d", settings.MaxBatchJobs)
	} else {
		m.logger.Debug("No persisted upgrade settings found, using defaults")
	}

	return nil
}

// GetSettings returns the current settings.
func (m *Manager) GetSettings() Settings {
	m.settingsLock.RLock()
	defer m.settingsLock.RUnlock()

	return m.settings
}

// UpdateSettings updates the settings and persists them.
func (m *Manager) UpdateSettings(ctx context.Context, settings Settings) error {
	m.settingsLock.Lock()
	m.settings = settings
	m.settingsLock.Unlock()

	// Update the job semaphore
	m.updateJobSemaphore()

	// Notify callback (e.g., to update BatchDirector pool size)
	if m.onMaxBatchJobsChanged != nil {
		m.onMaxBatchJobsChanged(settings.MaxBatchJobs)
	}

	// Persist settings
	err := m.vault.Put(ctx, upgradeSettingsVaultPath, settings)
	if err != nil {
		return fmt.Errorf("failed to save settings to vault: %w", err)
	}

	m.logger.Info("Updated upgrade settings: max_batch_jobs=%d", settings.MaxBatchJobs)

	return nil
}

// SetOnMaxBatchJobsChanged sets a callback that is called when max_batch_jobs changes.
// This is used to synchronize the BatchDirector pool size with max_batch_jobs.
func (m *Manager) SetOnMaxBatchJobsChanged(callback func(maxJobs int)) {
	m.onMaxBatchJobsChanged = callback
}

// runTask runs one queued upgrade. Its cancel channel is registered before
// it waits for a slot, so a cancel or a resume during that wait reaches it,
// and the upgrade stops being owned once its processing returns.
func (m *Manager) runTask(task *UpgradeTask) {
	defer m.wg.Done()

	cancelCh := make(chan struct{})

	m.taskCancelLock.Lock()
	m.taskCancelCh[task.ID] = cancelCh
	m.taskCancelLock.Unlock()

	defer func() {
		m.taskCancelLock.Lock()
		if m.taskCancelCh[task.ID] == cancelCh {
			delete(m.taskCancelCh, task.ID)
		}
		m.taskCancelLock.Unlock()

		m.mu.Lock()
		delete(m.owned, task.ID)
		m.mu.Unlock()
	}()

	release, ok := m.acquireSlot(task, cancelCh)
	if !ok {
		return
	}

	defer release()

	m.processTask(task, cancelCh)
}

// acquireSlot waits for a slot when a batch limit is set, and returns the
// function that frees it. An upgrade cancelled during the wait goes on at
// once without a slot, since it deploys nothing more. It returns false when
// Stop ends the wait.
func (m *Manager) acquireSlot(task *UpgradeTask, cancelCh <-chan struct{}) (func(), bool) {
	m.settingsLock.RLock()
	semaphore := m.jobSemaphore
	m.settingsLock.RUnlock()

	if semaphore == nil {
		return func() {}, true
	}

	for {
		select {
		case semaphore <- struct{}{}:
			return func() { <-semaphore }, true
		case <-cancelCh:
			m.mu.RLock()

			cancelled := task.Status == TaskStatusCancelled

			m.mu.RUnlock()

			if cancelled {
				return func() {}, true
			}
		case <-m.stopCh:
			m.logger.Info("Manager stopping, task %s not started", task.ID)

			return nil, false
		}
	}
}

// processTask processes a single upgrade task. A signal on cancelCh wakes
// it from a pause, and a close of it is a cancel.
func (m *Manager) processTask(task *UpgradeTask, cancelCh <-chan struct{}) {
	ctx := context.Background()

	// Recover from any panics to prevent crashing the entire blacksmith process
	defer m.recoverTask(ctx, task)

	m.logger.Info("Processing upgrade task %s", task.ID)

	if m.startProcessing(task) {
		m.logger.Info("Task %s was cancelled before it started, stopping processing", task.ID)
		m.skipPendingInstances(ctx, task)

		return
	}

	// Persist running state
	err := m.persistTask(ctx, task)
	if err != nil {
		m.logger.Error("Failed to persist task %s running state: %v", task.ID, err)
	}

	// Process each instance sequentially
	for i := range task.Instances {
		if !m.processInstance(task, &task.Instances[i], cancelCh) {
			return
		}
	}

	// A stop during the last instance leaves the upgrade running rather
	// than marking it completed.
	if m.ctx.Err() != nil {
		m.logger.Info("Manager stopping at the end of task %s, so it stays running", task.ID)

		return
	}

	m.finishTask(ctx, task)
}

// recoverTask fails an upgrade whose processing panicked, so the panic
// never crashes the broker. processTask defers it.
func (m *Manager) recoverTask(ctx context.Context, task *UpgradeTask) {
	r := recover()
	if r == nil {
		return
	}

	m.logger.Error("PANIC in processTask for task %s: %v", task.ID, r)

	m.mu.Lock()

	task.Status = TaskStatusFailed
	completedAt := time.Now()
	task.CompletedAt = &completedAt

	m.mu.Unlock()

	// Persist failed state
	err := m.persistTask(ctx, task)
	if err != nil {
		m.logger.Error("Failed to persist task %s after panic: %v", task.ID, err)
	}
}

// processInstance takes one instance of an upgrade through the cancel
// check, the pause wait, and its deploy, and reports whether the upgrade
// goes on to its next instance. Its saves go on after Stop.
func (m *Manager) processInstance(task *UpgradeTask, instance *InstanceUpgrade, cancelCh <-chan struct{}) bool {
	ctx := context.WithoutCancel(m.ctx)

	// Check if task was cancelled
	m.mu.RLock()

	cancelled := task.Status == TaskStatusCancelled

	m.mu.RUnlock()

	if cancelled {
		m.logger.Info("Task %s was cancelled, stopping processing", task.ID)
		m.skipPendingInstances(ctx, task)

		return false
	}

	// Check if task is paused - wait for resume or cancel
	if !m.waitWhilePaused(ctx, task, cancelCh) {
		return false
	}

	m.mu.RLock()

	status := instance.Status

	m.mu.RUnlock()

	// An instance that has settled was counted when it settled, so a
	// resumed upgrade skips it without counting it again.
	if status.settled() {
		return true
	}

	// An instance whose deploy is still in flight has not settled, so
	// the upgrade stops here rather than deploy it again or finish
	// while that deploy runs.
	if status == InstanceStatusRunning || status == InstanceStatusInterrupted {
		m.logger.Info("Instance %s of task %s is still %s, so the upgrade stops until it settles", instance.InstanceID, task.ID, status)

		return false
	}

	m.upgradeInstance(m.ctx, task, instance, cancelCh)

	// A manager that is stopping leaves the upgrade as it is, and the
	// instances not yet started stay pending.
	if m.ctx.Err() != nil {
		m.logger.Info("Manager stopping during task %s, so its remaining instances stay pending", task.ID)

		return false
	}

	return true
}

// startProcessing marks the upgrade running as its processing starts, and
// reports whether it was cancelled. A resumed upgrade keeps its first start
// time, and a cancel or a pause that landed while it waited in the queue or
// for a slot stands.
func (m *Manager) startProcessing(task *UpgradeTask) bool {
	now := time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	if task.StartedAt == nil {
		task.StartedAt = &now
	}

	if task.Status != TaskStatusCancelled && task.Status != TaskStatusPaused {
		task.Status = TaskStatusRunning
	}

	return task.Status == TaskStatusCancelled
}

// waitWhilePaused waits while the upgrade is paused, and reports whether
// its processing goes on. A cancel ends the processing, and so does Stop.
func (m *Manager) waitWhilePaused(ctx context.Context, task *UpgradeTask, cancelCh <-chan struct{}) bool {
	for {
		m.mu.RLock()

		isPaused := task.Paused
		isCancelled := task.Status == TaskStatusCancelled

		m.mu.RUnlock()

		if isCancelled {
			m.logger.Info("Task %s was cancelled while paused, stopping processing", task.ID)
			m.skipPendingInstances(ctx, task)

			return false
		}

		if !isPaused {
			return true
		}

		m.logger.Debug("Task %s is paused, waiting for resume...", task.ID)

		select {
		case <-cancelCh:
			// Check status again after signal
		case <-m.stopCh:
			m.logger.Info("Manager stopping while task %s is paused", task.ID)

			return false
		case <-time.After(1 * time.Second):
			// Periodic check
		}
	}
}

// finishTask records the final status of an upgrade whose instances have
// all been processed, unless it was cancelled, and saves it.
func (m *Manager) finishTask(ctx context.Context, task *UpgradeTask) {
	m.mu.Lock()

	if task.Status != TaskStatusCancelled {
		completedAt := time.Now()
		task.CompletedAt = &completedAt

		if task.FailedCount > 0 && task.CompletedCount == 0 {
			task.Status = TaskStatusFailed
		} else {
			// A partial success completes too.
			task.Status = TaskStatusCompleted
		}
	}

	m.mu.Unlock()

	// Persist completed state
	err := m.persistTask(ctx, task)
	if err != nil {
		m.logger.Error("Failed to persist task %s completed state: %v", task.ID, err)
	}

	m.logger.Info("Upgrade task %s completed: %d/%d succeeded, %d failed, %d cancelled, %d with an unknown outcome",
		task.ID, task.CompletedCount, task.TotalCount, task.FailedCount, task.CancelledCount, task.UnknownCount)
}

// skipPendingLocked marks every pending instance of a cancelled upgrade
// skipped and returns how many it marked. The caller holds mu.
func (m *Manager) skipPendingLocked(task *UpgradeTask) int {
	skipped := 0

	for i := range task.Instances {
		instance := &task.Instances[i]
		if instance.Status != InstanceStatusPending {
			continue
		}

		instance.Status = InstanceStatusSkipped
		instance.Error = "skipped due to task cancellation"
		task.SkippedCount++
		skipped++
	}

	return skipped
}

// resumeIfReady moves on an upgrade that has no processor, deciding under
// mu. A running upgrade, or a pending one that was queued but never
// processed before a restart, that is not paused, has no instance
// settling, and is not owned becomes owned and is queued, once no lookup
// of a loaded deploy is still running. A cancelled upgrade with no
// instance settling skips its pending instances. Anything else waits for
// its settles, the lookups, its resume, or its processor, and a manager
// that is stopping queues nothing. With no BOSH director, an upgrade that
// would be queued stays as it is, because it has nothing to deploy with,
// and the log says why.
func (m *Manager) resumeIfReady(task *UpgradeTask) {
	m.mu.Lock()
	settled := m.settling[task.ID] == 0
	resumable := task.Status == TaskStatusRunning || task.Status == TaskStatusPending
	status := task.Status

	ready := settled && m.lookups == 0 && m.ctx.Err() == nil && resumable && !task.Paused && !m.owned[task.ID]
	noDirector := ready && m.director == nil

	queue := ready && !noDirector
	if queue {
		m.owned[task.ID] = true
	}

	skipped := 0
	if settled && task.Status == TaskStatusCancelled {
		skipped = m.skipPendingLocked(task)
	}
	m.mu.Unlock()

	if skipped > 0 {
		m.logger.Info("Skipped %d pending instances of cancelled task %s", skipped, task.ID)

		err := m.persistTask(context.WithoutCancel(m.ctx), task)
		if err != nil {
			m.logger.Error("Failed to persist the skipped instances of task %s: %v", task.ID, err)
		}
	}

	if noDirector {
		m.logger.Error("Upgrade task %s cannot resume, because Blacksmith has no BOSH director configured, so it stays %s until Blacksmith restarts with one", task.ID, status)
	}

	if queue {
		m.logger.Info("Upgrade task %s is resuming after a restart", task.ID)
		m.enqueue(task)
	}
}

// enqueue puts an upgrade the caller made owned on the queue. The send
// waits for room in the queue, and when Stop ends the manager first, the
// upgrade is no longer owned.
func (m *Manager) enqueue(task *UpgradeTask) {
	select {
	case m.taskQueue <- task:
		m.logger.Debug("Task %s queued for processing", task.ID)
	case <-m.ctx.Done():
		m.mu.Lock()
		delete(m.owned, task.ID)
		m.mu.Unlock()

		m.logger.Info("Upgrade manager stopping, so task %s was not queued", task.ID)
	}
}

// loadAndSettle loads the upgrade tasks saved in Vault, settles every
// instance whose deploy was in flight when the broker stopped, and resumes
// each upgrade that has nothing settling once every lookup of a deploy
// saved without a task ID has ended. Stop ends it, and it closes loaded
// once the settles have started.
func (m *Manager) loadAndSettle() {
	defer m.wg.Done()
	defer close(m.loaded)

	tasks, ok := m.loadPersistedTasks()
	if !ok {
		m.logger.Info("Upgrade manager stopping before the persisted upgrade tasks were loaded")

		return
	}

	m.settleLoadedTasks(tasks)

	// Each upgrade resumes on its own, so one upgrade's slow settle never
	// holds back another.
	for _, task := range tasks {
		m.resumeIfReady(task)
	}
}

// loadPersistedTasks loads every upgrade task saved in Vault. A failed read
// of the index or of a task is retried until it succeeds or Stop ends the
// load, so a Vault that starts after the broker never loses an upgrade. It
// returns the tasks it loaded, and false when Stop ended the load. A task
// already in memory, created after the broker started, is kept.
func (m *Manager) loadPersistedTasks() ([]*UpgradeTask, bool) {
	m.logger.Debug("Loading persisted upgrade tasks from vault")

	var tasksIndex map[string]interface{}

	exists, ok := m.readWithRetry("the upgrade tasks index from Vault", func(ctx context.Context) (bool, error) {
		tasksIndex = nil

		return m.vault.Get(ctx, upgradeTasksVaultPath, &tasksIndex)
	})
	if !ok {
		return nil, false
	}

	if !exists {
		m.logger.Debug("No persisted upgrade tasks found")

		return nil, true
	}

	loaded := make([]*UpgradeTask, 0, len(tasksIndex))

	for taskID := range tasksIndex {
		var task *UpgradeTask

		taskPath := upgradeTasksVaultPath + "/" + taskID

		taskExists, ok := m.readWithRetry("upgrade task "+taskID+" from Vault", func(ctx context.Context) (bool, error) {
			task = &UpgradeTask{}

			return m.vault.Get(ctx, taskPath, task)
		})
		if !ok {
			return nil, false
		}

		if !taskExists {
			m.logger.Debug("Task %s not found at path %s", taskID, taskPath)

			continue
		}

		m.mu.Lock()
		_, inMemory := m.tasks[taskID]

		if !inMemory {
			m.tasks[taskID] = task
		}
		m.mu.Unlock()

		if inMemory {
			continue
		}

		loaded = append(loaded, task)
	}

	m.logger.Info("Loaded %d persisted upgrade tasks", len(loaded))

	return loaded, true
}

// readWithRetry runs read under the manager's context until it succeeds,
// waiting loadRetryBackoff after the first failure and twice as long after
// each later one, up to loadRetryBackoffMax. It logs each failure as an
// Error that names what it reads. It returns what read found, and false
// when Stop ended the retries.
func (m *Manager) readWithRetry(what string, read func(ctx context.Context) (bool, error)) (bool, bool) {
	backoff := loadRetryBackoff

	for {
		exists, err := read(m.ctx)
		if err == nil {
			return exists, true
		}

		if m.ctx.Err() != nil {
			return false, false
		}

		m.logger.Error("Failed to read %s, retrying in %v: %v", what, backoff, err)

		select {
		case <-m.ctx.Done():
			return false, false
		case <-time.After(backoff):
		}

		backoff = min(backoff+backoff, loadRetryBackoffMax)
	}
}

// loadedDeploy is an instance of a loaded upgrade whose deploy was in
// flight when the broker stopped, with the IDs it was saved with.
type loadedDeploy struct {
	task         *UpgradeTask
	instance     *InstanceUpgrade
	taskID       int
	contextID    string
	unknownSince time.Time
	cancelCh     chan struct{}
}

// settleLoadedTasks settles each instance of the loaded upgrades that is
// running or interrupted, whatever the upgrade's own status, because an
// upgrade paused or cancelled before the restart can still have a deploy in
// flight. Each upgrade's settling count holds how many of its instances
// are settling, and each upgrade gets a cancel channel its settles share,
// so a cancel reaches them. Every deploy with a task ID is adopted into the
// director's limit before any settle starts. Every deploy without one is
// counted as a lookup, and no upgrade resumes until each lookup has
// adopted the task it found or found that none exists.
func (m *Manager) settleLoadedTasks(tasks []*UpgradeTask) {
	loadedAt := time.Now()
	deploys := make([]loadedDeploy, 0, len(tasks))

	m.mu.Lock()
	for _, task := range tasks {
		deploys = append(deploys, m.prepareSettlingLocked(task, loadedAt)...)
	}
	m.mu.Unlock()

	if len(deploys) == 0 {
		return
	}

	if m.director == nil {
		m.interruptWithoutDirector(deploys)

		return
	}

	lookups := 0

	for _, deploy := range deploys {
		if deploy.taskID > 0 {
			m.adoptTask(deploy.taskID)

			continue
		}

		lookups++
	}

	if lookups > 0 {
		m.mu.Lock()
		m.lookups = lookups
		m.heldResumes = tasks
		m.mu.Unlock()

		m.logger.Info("No upgrade resumes until the tasks of %d deploys that were in flight without a saved task ID are looked up", lookups)
	}

	for _, deploy := range deploys {
		m.wg.Add(1)

		go m.settleLoadedDeploy(deploy)
	}
}

// endLookup counts the lookup of one loaded deploy as ended. Its caller
// has already adopted the task the lookup found, if any. Once the last
// lookup ends, each loaded upgrade that is ready resumes.
func (m *Manager) endLookup() {
	m.mu.Lock()

	m.lookups--
	if m.lookups > 0 {
		m.mu.Unlock()

		return
	}

	held := m.heldResumes
	m.heldResumes = nil
	m.mu.Unlock()

	for _, task := range held {
		m.resumeIfReady(task)
	}
}

// prepareSettlingLocked finds the instances of a loaded upgrade whose
// deploy was in flight, sets the upgrade's settling count, and registers
// the cancel channel their settles share. The caller holds mu.
func (m *Manager) prepareSettlingLocked(task *UpgradeTask, loadedAt time.Time) []loadedDeploy {
	var deploys []loadedDeploy

	for i := range task.Instances {
		instance := &task.Instances[i]
		if instance.Status != InstanceStatusRunning && instance.Status != InstanceStatusInterrupted {
			continue
		}

		// A deploy whose answer was lost before UnknownSince was saved has
		// its grace start at the load.
		unknownSince := loadedAt
		if instance.UnknownSince != nil {
			unknownSince = *instance.UnknownSince
		}

		deploys = append(deploys, loadedDeploy{
			task:         task,
			instance:     instance,
			taskID:       instance.BOSHTaskID,
			contextID:    instance.ContextID,
			unknownSince: unknownSince,
		})
	}

	if len(deploys) == 0 {
		return nil
	}

	m.settling[task.ID] = len(deploys)

	cancelCh := make(chan struct{})

	m.taskCancelLock.Lock()
	m.taskCancelCh[task.ID] = cancelCh
	m.taskCancelLock.Unlock()

	for i := range deploys {
		deploys[i].cancelCh = cancelCh
	}

	m.logger.Info("Upgrade task %s has %d instances whose deploys were in flight when Blacksmith stopped, so it settles them", task.ID, len(deploys))

	return deploys
}

// interruptWithoutDirector marks each loaded instance whose deploy was in
// flight interrupted when the broker has no BOSH director. Their settling
// counts stay, so the upgrade waits for a restart that has a director.
func (m *Manager) interruptWithoutDirector(deploys []loadedDeploy) {
	saved := make(map[string]*UpgradeTask)

	m.mu.Lock()
	for _, deploy := range deploys {
		deploy.instance.Status = InstanceStatusInterrupted
		deploy.instance.Error = noDirectorMessage
		saved[deploy.task.ID] = deploy.task
	}
	m.mu.Unlock()

	m.logger.Error("Blacksmith has no BOSH director configured, so the %d upgrade deploys that were in flight when it stopped stay unsettled until it restarts with one", len(deploys))

	for _, task := range saved {
		err := m.persistTask(m.ctx, task)
		if err != nil {
			m.logger.Error("Failed to save the interrupted instances of task %s: %v", task.ID, err)
		}
	}
}

// adoptTask counts a deploy task started before a restart against the
// director's limit and has the director follow it, when the director can
// adopt one. A wait on the task is then served by that follow, so the task
// is followed once.
func (m *Manager) adoptTask(taskID int) {
	if batch, ok := m.director.(adopter); ok {
		batch.Adopt(taskID)
	}
}

// settleLoadedDeploy settles one loaded instance whose deploy was in
// flight, by the task ID it was saved with, by its context ID, or, with
// neither, by whatever runs on its deployment. A wait that Stop ends leaves
// the instance unsettled for the next restart.
func (m *Manager) settleLoadedDeploy(deploy loadedDeploy) {
	defer m.wg.Done()
	defer m.endSettling(deploy)

	switch {
	case deploy.taskID > 0:
		m.followDeployTask(m.ctx, deploy.task, deploy.instance, deploy.taskID, deploy.cancelCh)
	case deploy.contextID != "":
		m.settleByContextID(deploy)
	default:
		m.settleWithoutIDs(deploy)
	}
}

// endSettling counts a loaded instance's settle as ended. Once the last of
// the upgrade's settles ends, its shared cancel channel is removed, unless
// a cancel already removed it, and the upgrade moves on.
func (m *Manager) endSettling(deploy loadedDeploy) {
	m.mu.Lock()

	m.settling[deploy.task.ID]--
	if m.settling[deploy.task.ID] > 0 {
		m.mu.Unlock()

		return
	}

	delete(m.settling, deploy.task.ID)

	m.taskCancelLock.Lock()
	if m.taskCancelCh[deploy.task.ID] == deploy.cancelCh {
		delete(m.taskCancelCh, deploy.task.ID)
	}
	m.taskCancelLock.Unlock()
	m.mu.Unlock()

	m.resumeIfReady(deploy.task)
}

// settleByContextID settles a loaded instance that has a context ID and no
// task ID, by looking for its deploy's task by the context ID. A task that
// turns up is adopted into the director's limit, as a task the instance
// was saved with is, and followed like any deploy. When none turns up, an
// instance whose answer was lost settles as a lost answer does, and an
// instance whose deploy may never have been sent goes back to pending, so
// the upgrade deploys it again.
func (m *Manager) settleByContextID(deploy loadedDeploy) {
	task, instance := deploy.task, deploy.instance
	persistCtx := context.WithoutCancel(m.ctx)

	m.mu.RLock()

	answerLost := instance.UnknownSince != nil

	m.mu.RUnlock()

	m.logger.Info("Looking for the BOSH task of the deploy of %s with context ID %s, which was in flight when Blacksmith stopped", instance.DeploymentName, deploy.contextID)

	found, err := awaitTaskByContextID(m.ctx, m.director, deploy.contextID, deploy.unknownSince)

	taskFound := err == nil && found != nil && found.ID > 0
	if taskFound {
		m.logger.Info("Found BOSH task %d for the deploy of %s with context ID %s", found.ID, instance.DeploymentName, deploy.contextID)
		m.adoptTask(found.ID)
	}

	m.endLookup()

	switch {
	case taskFound:
		m.followDeployTask(m.ctx, task, instance, found.ID, deploy.cancelCh)
	case err == nil:
		m.settleOutcomeUnknown(task, instance, 0, "the lookup by context ID named no task")
		m.persistSettled(persistCtx, task)
	case waitStopped(m.ctx, err):
		m.logger.Info("Blacksmith stopped looking for the deploy of %s with context ID %s at shutdown, so instance %s stays unsettled", instance.DeploymentName, deploy.contextID, instance.InstanceID)
	case errors.Is(err, bosh.ErrNoTaskWithContextID) && !answerLost:
		m.resetToPending(persistCtx, task, instance,
			fmt.Sprintf("no BOSH task with context ID %s turned up, so the deploy was never sent", deploy.contextID))
	default:
		if !m.settleLostAnswer(task, instance, deploy.contextID, err) {
			m.settleOutcomeUnknown(task, instance, 0, err.Error())
		}

		m.persistSettled(persistCtx, task)
	}
}

// persistSettled saves a loaded upgrade once one of its instances has
// settled, and logs a save that fails.
func (m *Manager) persistSettled(ctx context.Context, task *UpgradeTask) {
	err := m.persistTask(ctx, task)
	if err != nil {
		m.logger.Error("Failed to persist task %s after a loaded instance settled: %v", task.ID, err)
	}
}

// settleWithoutIDs settles a loaded instance that has neither a task ID
// nor a context ID. Its deploy may have been sent before either was saved,
// so when a task runs on its deployment, the instance waits for that task
// to end. Nothing ties the task to the instance, so the instance takes
// nothing from it and goes back to pending, and the upgrade deploys it
// again.
func (m *Manager) settleWithoutIDs(deploy loadedDeploy) {
	task, instance := deploy.task, deploy.instance
	persistCtx := context.WithoutCancel(m.ctx)

	var running *bosh.Task

	_, answered := m.readWithRetry("the running task of deployment "+instance.DeploymentName+" from the director", func(ctx context.Context) (bool, error) {
		var err error

		running, err = m.director.FindRunningTaskForDeployment(ctx, instance.DeploymentName)

		return running != nil, err //nolint:wrapcheck // readWithRetry logs it with what it read
	})

	taskRunning := answered && running != nil && running.ID > 0
	if taskRunning {
		m.adoptTask(running.ID)
	}

	m.endLookup()

	if !answered {
		return
	}

	if taskRunning {
		m.logger.Info("BOSH task %d runs on deployment %s, so instance %s waits for it to end before it is deployed again", running.ID, instance.DeploymentName, instance.InstanceID)

		_, err := m.director.WaitForTask(m.ctx, running.ID)
		if err != nil && waitStopped(m.ctx, err) {
			m.logger.Info("Blacksmith stopped waiting for BOSH task %d on deployment %s at shutdown, so instance %s stays unsettled", running.ID, instance.DeploymentName, instance.InstanceID)

			return
		}

		if err != nil {
			m.logger.Error("Stopped waiting for BOSH task %d on deployment %s: %v", running.ID, instance.DeploymentName, err)
		}
	}

	m.resetToPending(persistCtx, task, instance, "no ID of its deploy was saved")
}

// updateJobSemaphore updates the job semaphore based on current settings.
func (m *Manager) updateJobSemaphore() {
	m.settingsLock.Lock()
	defer m.settingsLock.Unlock()

	if m.settings.MaxBatchJobs > 0 {
		m.jobSemaphore = make(chan struct{}, m.settings.MaxBatchJobs)
		m.logger.Info("Set max parallel batch jobs to %d", m.settings.MaxBatchJobs)
	} else {
		m.jobSemaphore = nil
		m.logger.Info("No limit on parallel batch jobs")
	}
}

// cancelDeployTask sends one cancel for the deploy task of an upgrade
// instance. The cancel is sent even when ctx has ended, under its own
// deadline, and the caller must not hold the manager's lock.
func (m *Manager) cancelDeployTask(ctx context.Context, instanceID string, boshTaskID int) {
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelTaskTimeout)
	defer cancel()

	m.logger.Info("Cancelling BOSH task %d for instance %s", boshTaskID, instanceID)

	err := m.director.CancelTask(cancelCtx, boshTaskID)

	switch {
	case err == nil:
	case errors.Is(err, bosh.ErrTaskCannotBeCancelled):
		m.logger.Info("BOSH task %d for instance %s had already ended or was being cancelled: %v", boshTaskID, instanceID, err)
	default:
		m.logger.Error("Failed to cancel BOSH task %d: %v", boshTaskID, err)
	}
}

// storeFoundTask stores the ID of a deploy task found by its context ID
// while the wait for it goes on. A cancel that came before this point
// found no task ID to cancel, so the upgrade cancels the task itself. The
// ID and the upgrade's status are read and written under the same lock as
// CancelTask's, so exactly one side sends the cancel.
func (m *Manager) storeFoundTask(ctx context.Context, task *UpgradeTask, instance *InstanceUpgrade, contextID string, taskID int) {
	if taskID <= 0 {
		return
	}

	m.mu.Lock()
	instance.BOSHTaskID = taskID
	cancelledBeforeTheTask := task.Status == TaskStatusCancelled
	m.mu.Unlock()

	m.logger.Info("Found BOSH task %d for the deploy of %s with context ID %s, whose answer was lost", taskID, instance.DeploymentName, contextID)

	if cancelledBeforeTheTask {
		m.cancelDeployTask(ctx, instance.InstanceID, taskID)
	}
}

// snapshotLocked deep-copies the task, its instances, and every time they
// point at, and numbers the copy. The caller holds mu, for reading or for
// writing, so no copy is taken while the task changes, and a copy with a
// higher number holds a state at least as new.
func (m *Manager) snapshotLocked(task *UpgradeTask) taskSnapshot {
	return taskSnapshot{task: copyTask(task), seq: m.snapshotSeq.Add(1)}
}

// copyTask returns a deep copy of the task, its instances, and every time
// they point at.
func copyTask(task *UpgradeTask) *UpgradeTask {
	copied := *task
	copied.StartedAt = copyTime(task.StartedAt)
	copied.CompletedAt = copyTime(task.CompletedAt)

	if task.Instances != nil {
		copied.Instances = make([]InstanceUpgrade, len(task.Instances))

		for i, instance := range task.Instances {
			instance.UnknownSince = copyTime(instance.UnknownSince)
			instance.StartedAt = copyTime(instance.StartedAt)
			instance.CompletedAt = copyTime(instance.CompletedAt)
			copied.Instances[i] = instance
		}
	}

	return &copied
}

// copyTime returns a copy of the time t points at, or nil.
func copyTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	copied := *t

	return &copied
}

// persistenceFor returns the save state of the task with the ID.
func (m *Manager) persistenceFor(taskID string) *taskPersistence {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()

	if m.persisted == nil {
		m.persisted = make(map[string]*taskPersistence)
	}

	persistence, exists := m.persisted[taskID]
	if !exists {
		persistence = &taskPersistence{}
		m.persisted[taskID] = persistence
	}

	return persistence
}

// writeSnapshot saves a copy of an upgrade task to vault, without mu. The
// task's saves run one at a time, and a copy no newer than the last one
// saved is dropped, because a newer state has already landed. The copy's
// number is recorded only once the task's record is written, so a failed
// save never keeps a later one from landing.
func (m *Manager) writeSnapshot(ctx context.Context, snapshot taskSnapshot) error {
	task := snapshot.task

	persistence := m.persistenceFor(task.ID)

	persistence.mu.Lock()
	defer persistence.mu.Unlock()

	if snapshot.seq <= persistence.written {
		m.logger.Debug("A newer state of task %s is already saved, so this save is dropped", task.ID)

		return nil
	}

	m.logger.Debug("Persisting task %s to vault", task.ID)

	// The task's in-memory state and the director's task have already
	// changed by the time this runs, so the save must land even when the
	// request or manager context has ended. It keeps ctx's values and
	// ignores its cancellation, under its own deadline.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTaskTimeout)
	defer cancel()

	// Save the full task data
	taskPath := upgradeTasksVaultPath + "/" + task.ID

	err := m.vault.Put(ctx, taskPath, task)
	if err != nil {
		return fmt.Errorf("failed to save task to vault: %w", err)
	}

	persistence.written = snapshot.seq

	err = m.addToTasksIndex(ctx, task)
	if err != nil {
		return err
	}

	m.logger.Debug("Task %s persisted successfully", task.ID)

	return nil
}

// addToTasksIndex records the task in the tasks index. The read, the
// change, and the write run under indexMu, so two tasks saved at once never
// lose each other's entries.
func (m *Manager) addToTasksIndex(ctx context.Context, task *UpgradeTask) error {
	m.indexMu.Lock()
	defer m.indexMu.Unlock()

	var tasksIndex map[string]interface{}

	exists, err := m.vault.Get(ctx, upgradeTasksVaultPath, &tasksIndex)
	if err != nil {
		// A fresh map written over an index that could not be read would
		// drop every other task from it, and loadPersistedTasks would forget
		// them at the next restart. The task's own record is already saved.
		m.logger.Error("Failed to get tasks index: %v", err)

		return fmt.Errorf("failed to read the tasks index before adding task %s: %w", task.ID, err)
	}

	if !exists || tasksIndex == nil {
		tasksIndex = make(map[string]interface{})
	}

	// Add task reference to index
	tasksIndex[task.ID] = map[string]interface{}{
		"status":     task.Status,
		"created_at": task.CreatedAt.Unix(),
	}

	err = m.vault.Put(ctx, upgradeTasksVaultPath, tasksIndex)
	if err != nil {
		return fmt.Errorf("failed to update tasks index: %w", err)
	}

	return nil
}

// returnToPending puts an instance back to pending when a step before the
// director received its deploy failed because Blacksmith is stopping. The
// director never received the deploy, so the counts stay as they are, and
// the instance is deployed when the upgrade runs again.
func (m *Manager) returnToPending(ctx context.Context, task *UpgradeTask, instance *InstanceUpgrade, err error) {
	m.resetToPending(ctx, task, instance, fmt.Sprintf("Blacksmith is stopping before its deploy was sent: %v", err))
}

// resetToPending puts an instance back to pending, with its start time,
// IDs, and error cleared and the counts untouched, so the instance is
// deployed when the upgrade runs again. reason says why, for the log.
func (m *Manager) resetToPending(ctx context.Context, task *UpgradeTask, instance *InstanceUpgrade, reason string) {
	m.mu.Lock()
	instance.Status = InstanceStatusPending
	instance.StartedAt = nil
	instance.BOSHTaskID = 0
	instance.ContextID = ""
	instance.UnknownSince = nil
	instance.Error = ""
	m.mu.Unlock()

	m.logger.Info("Instance %s goes back to pending, because %s", instance.InstanceID, reason)

	err := m.persistTask(ctx, task)
	if err != nil {
		m.logger.Error("Failed to save instance %s back to pending: %v", instance.InstanceID, err)
	}
}

// interruptAtShutdown marks an instance interrupted when Stop ended the
// wait for its deploy. The deploy may still run on the director, and the
// instance keeps its task ID and context ID, so it can be found after a
// restart. The counts stay as they are.
func (m *Manager) interruptAtShutdown(ctx context.Context, task *UpgradeTask, instance *InstanceUpgrade, message string) {
	m.mu.Lock()
	instance.Status = InstanceStatusInterrupted
	instance.Error = message
	m.mu.Unlock()

	m.logger.Info("Instance %s: %s", instance.InstanceID, message)

	err := m.persistTask(ctx, task)
	if err != nil {
		m.logger.Error("Failed to save instance %s as interrupted at shutdown: %v", instance.InstanceID, err)
	}
}

// interruptLostAnswer marks an instance interrupted when Stop ended the
// wait for the task of a deploy whose answer was lost. The message names
// the task when a batch watcher found and stored it, and the context ID
// otherwise.
func (m *Manager) interruptLostAnswer(ctx context.Context, task *UpgradeTask, instance *InstanceUpgrade, contextID string) {
	m.mu.RLock()

	taskID := instance.BOSHTaskID

	m.mu.RUnlock()

	message := fmt.Sprintf("Blacksmith stopped looking for the deploy with context ID %s at shutdown", contextID)
	if taskID > 0 {
		message = fmt.Sprintf("Blacksmith stopped waiting at shutdown; BOSH task %d is still running", taskID)
	}

	m.interruptAtShutdown(ctx, task, instance, message)
}
