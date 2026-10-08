package upgrade

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/interfaces"

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

// Final states of a director task.
const (
	boshTaskDone      = "done"
	boshTaskCancelled = "cancelled"
)

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
}

// NewManager creates a new upgrade manager.
func NewManager(logger interfaces.Logger, director interfaces.Director, vault interfaces.Vault) *Manager {
	m := &Manager{
		tasks:        make(map[string]*UpgradeTask),
		logger:       logger.Named("upgrade-manager"),
		director:     director,
		vault:        vault,
		taskQueue:    make(chan *UpgradeTask, 100),
		stopCh:       make(chan struct{}),
		taskCancelCh: make(map[string]chan struct{}),
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

	// Load persisted tasks from vault
	if err := m.loadPersistedTasks(ctx); err != nil {
		m.logger.Error("Failed to load persisted tasks: %v", err)
	}

	// Start the task dispatcher
	m.wg.Add(1)

	go m.dispatchTasks()

	return m
}

// Stop stops the manager and waits for pending operations to complete.
func (m *Manager) Stop() {
	close(m.stopCh)
	m.cancel()
	m.wg.Wait()
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
	m.mu.Unlock()

	// Persist task to vault
	if err := m.persistTask(ctx, task); err != nil {
		m.logger.Error("Failed to persist task %s: %v", task.ID, err)
		// Continue anyway - task is in memory
	}

	// Queue the task for processing
	select {
	case m.taskQueue <- task:
		m.logger.Debug("Task %s queued for processing", task.ID)
	default:
		m.logger.Error("Task queue is full, task %s not queued", task.ID)
	}

	return task, nil
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

// GetTask retrieves a task by ID.
func (m *Manager) GetTask(taskID string) (*UpgradeTask, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	task, exists := m.tasks[taskID]
	if !exists {
		return nil, ErrTaskNotFound
	}

	return task, nil
}

// ListTasks returns all tasks.
func (m *Manager) ListTasks() []*UpgradeTask {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tasks := make([]*UpgradeTask, 0, len(m.tasks))
	for _, task := range m.tasks {
		tasks = append(tasks, task)
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

			go func(t *UpgradeTask) {
				defer m.wg.Done()

				// Acquire semaphore slot if max batch jobs is set
				m.settingsLock.RLock()
				semaphore := m.jobSemaphore
				m.settingsLock.RUnlock()

				if semaphore != nil {
					select {
					case semaphore <- struct{}{}:
						// Got a slot
						defer func() { <-semaphore }()
					case <-m.stopCh:
						m.logger.Info("Manager stopping, task %s not started", t.ID)

						return
					}
				}

				m.processTask(t)
			}(task)
		}
	}
}

// processTask processes a single upgrade task.
func (m *Manager) processTask(task *UpgradeTask) {
	ctx := context.Background()

	// Create cancel channel for this task
	cancelCh := make(chan struct{})
	m.taskCancelLock.Lock()
	m.taskCancelCh[task.ID] = cancelCh
	m.taskCancelLock.Unlock()

	// Cleanup cancel channel when done
	defer func() {
		m.taskCancelLock.Lock()
		delete(m.taskCancelCh, task.ID)
		m.taskCancelLock.Unlock()
	}()

	// Recover from any panics to prevent crashing the entire blacksmith process
	defer func() {
		if r := recover(); r != nil {
			m.logger.Error("PANIC in processTask for task %s: %v", task.ID, r)
			m.mu.Lock()
			task.Status = TaskStatusFailed
			completedAt := time.Now()
			task.CompletedAt = &completedAt
			m.mu.Unlock()

			// Persist failed state
			if err := m.persistTask(ctx, task); err != nil {
				m.logger.Error("Failed to persist task %s after panic: %v", task.ID, err)
			}
		}
	}()

	m.logger.Info("Processing upgrade task %s", task.ID)

	now := time.Now()

	m.mu.Lock()
	task.StartedAt = &now
	task.Status = TaskStatusRunning
	m.mu.Unlock()

	// Persist running state
	if err := m.persistTask(ctx, task); err != nil {
		m.logger.Error("Failed to persist task %s running state: %v", task.ID, err)
	}

	// Process each instance sequentially
	for i := range task.Instances {
		instance := &task.Instances[i]

		// Check if task was cancelled
		m.mu.RLock()
		if task.Status == TaskStatusCancelled {
			m.mu.RUnlock()
			m.logger.Info("Task %s was cancelled, stopping processing", task.ID)
			m.skipPendingInstances(ctx, task)
			return
		}
		m.mu.RUnlock()

		// Check if task is paused - wait for resume or cancel
		for {
			m.mu.RLock()
			isPaused := task.Paused
			isCancelled := task.Status == TaskStatusCancelled
			m.mu.RUnlock()

			if isCancelled {
				m.logger.Info("Task %s was cancelled while paused, stopping processing", task.ID)
				m.skipPendingInstances(ctx, task)
				return
			}

			if !isPaused {
				break
			}

			m.logger.Debug("Task %s is paused, waiting for resume...", task.ID)
			select {
			case <-cancelCh:
				// Check status again after signal
				continue
			case <-m.stopCh:
				m.logger.Info("Manager stopping while task %s is paused", task.ID)
				return
			case <-time.After(1 * time.Second):
				// Periodic check
				continue
			}
		}

		// Skip already processed instances
		if instance.Status == InstanceStatusFailed {
			m.mu.Lock()
			task.FailedCount++
			m.mu.Unlock()
			continue
		}
		if instance.Status == InstanceStatusSuccess ||
			instance.Status == InstanceStatusCancelled ||
			instance.Status == InstanceStatusSkipped {
			continue
		}

		m.upgradeInstance(m.ctx, task, instance, cancelCh)
	}

	// Update task status (only if not already cancelled)
	m.mu.Lock()
	if task.Status != TaskStatusCancelled {
		completedAt := time.Now()
		task.CompletedAt = &completedAt

		if task.FailedCount > 0 && task.CompletedCount == 0 {
			task.Status = TaskStatusFailed
		} else if task.FailedCount > 0 || task.CancelledCount > 0 {
			task.Status = TaskStatusCompleted // Partial success
		} else {
			task.Status = TaskStatusCompleted
		}
	}
	m.mu.Unlock()

	// Persist completed state
	if err := m.persistTask(ctx, task); err != nil {
		m.logger.Error("Failed to persist task %s completed state: %v", task.ID, err)
	}

	m.logger.Info("Upgrade task %s completed: %d/%d succeeded, %d failed, %d cancelled",
		task.ID, task.CompletedCount, task.TotalCount, task.FailedCount, task.CancelledCount)
}

// upgradeInstance upgrades a single instance. It sends the deploy, saves
// the deploy's task ID at once, and then waits for that task to end, so
// the outcome comes from the task's own final state.
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

	deployTask, err := m.director.UpdateDeployment(ctx, instance.DeploymentName, newManifest)
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

	// The task ID is saved before the wait, so the UI shows it while the
	// deploy runs, and a broker that stops during the wait leaves it on
	// record.
	m.mu.Lock()
	instance.BOSHTaskID = deployTask.ID
	m.mu.Unlock()
	m.logger.Info("BOSH task %d deploys %s for instance %s", deployTask.ID, instance.DeploymentName, instance.InstanceID)
	m.persistTask(persistCtx, task) //nolint:errcheck

	finished, cancelRequested, err := m.awaitDeployTask(ctx, task, instance, deployTask.ID, cancelCh)
	if err != nil {
		if cancelRequested {
			// The task outlived the wait after the cancel, or could not be
			// read, so its outcome is not known yet.
			message := fmt.Sprintf("cancel requested for BOSH task %d", deployTask.ID)

			m.mu.Lock()
			instance.Error = message
			m.mu.Unlock()
			m.logger.Error("Instance %s: %s, and the task had not ended when Blacksmith stopped waiting: %v", instance.InstanceID, message, err)
			m.persistTask(persistCtx, task) //nolint:errcheck
			return
		}

		if ctx.Err() != nil {
			// Stop ended the wait. The task keeps running on the director,
			// and its ID stays on record.
			message := fmt.Sprintf("Blacksmith stopped waiting at shutdown; BOSH task %d is still running", deployTask.ID)

			m.mu.Lock()
			instance.Error = message
			m.mu.Unlock()
			m.logger.Info("Instance %s: %s", instance.InstanceID, message)
			m.persistTask(persistCtx, task) //nolint:errcheck
			return
		}

		m.mu.Lock()
		instance.Status = InstanceStatusFailed
		instance.Error = fmt.Sprintf("stopped waiting for BOSH task %d, which may still be running: %v", deployTask.ID, err)
		task.FailedCount++
		m.logger.Error("Stopped waiting for BOSH task %d of %s: %v", deployTask.ID, instance.DeploymentName, err)
		m.mu.Unlock()
		m.persistTask(persistCtx, task) //nolint:errcheck
		return
	}

	m.settleInstance(task, instance, finished)
	m.persistTask(persistCtx, task) //nolint:errcheck
}

// awaitDeployTask waits for the deploy task of an upgrade instance to end.
// The cancel itself is sent by CancelTask. When the upgrade is cancelled
// during the wait, it keeps waiting for the task's own end under a
// deadline of cancelWaitTimeout that does not end with ctx, and reports
// that a cancel was requested. A signal on cancelCh that is not a cancel,
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

			afterCancel, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelWaitTimeout)
			finished, err := m.director.WaitForTask(afterCancel, taskID)

			cancel()

			return finished, true, err //nolint:wrapcheck // the caller adds the context
		}
	}
}

// settleInstance records the outcome of an upgrade instance from the final
// state of its deploy task. It alone settles an instance that started.
func (m *Manager) settleInstance(task *UpgradeTask, instance *InstanceUpgrade, finished *bosh.Task) {
	m.mu.Lock()
	defer m.mu.Unlock()

	completedAt := time.Now()
	instance.CompletedAt = &completedAt

	switch finished.State {
	case boshTaskDone:
		instance.Status = InstanceStatusSuccess
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
	skipped := 0

	m.mu.Lock()
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

// GetInstancesForTask retrieves instances for a specific task.
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
	defer m.mu.Unlock()

	task, exists := m.tasks[taskID]
	if !exists {
		return ErrTaskNotFound
	}

	if task.Status != TaskStatusRunning {
		return ErrTaskNotRunning
	}

	task.Paused = true
	task.Status = TaskStatusPaused
	m.logger.Info("Paused task %s", taskID)

	// Persist the paused state
	if err := m.persistTask(ctx, task); err != nil {
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

	return nil
}

// CancelTask cancels a running or paused task. It sends one cancel for the
// deploy task of each running instance and leaves every instance's status
// and the counts to the upgrade, which records each instance's outcome
// from its task's final state. The manager's lock is never held across a
// director call.
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
		if instance.Status == InstanceStatusRunning && instance.BOSHTaskID > 0 {
			running = append(running, runningDeploy{instanceID: instance.InstanceID, boshTaskID: instance.BOSHTaskID})
		}
	}

	task.Status = TaskStatusCancelled
	completedAt := time.Now()
	task.CompletedAt = &completedAt
	m.mu.Unlock()

	// The cancel is sent even when the request that asked for it ends.
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelTaskTimeout)
	defer cancel()

	for _, deploy := range running {
		m.logger.Info("Cancelling BOSH task %d for instance %s", deploy.boshTaskID, deploy.instanceID)

		err := m.director.CancelTask(cancelCtx, deploy.boshTaskID)

		switch {
		case err == nil:
		case errors.Is(err, bosh.ErrTaskCannotBeCancelled):
			m.logger.Info("BOSH task %d for instance %s had already ended or was being cancelled: %v", deploy.boshTaskID, deploy.instanceID, err)
		default:
			m.logger.Error("Failed to cancel BOSH task %d: %v", deploy.boshTaskID, err)
		}
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

// loadPersistedTasks loads all upgrade tasks from vault on startup.
func (m *Manager) loadPersistedTasks(ctx context.Context) error {
	m.logger.Debug("Loading persisted upgrade tasks from vault")

	var tasksIndex map[string]interface{}

	exists, err := m.vault.Get(ctx, upgradeTasksVaultPath, &tasksIndex)
	if err != nil {
		return fmt.Errorf("failed to get tasks index from vault: %w", err)
	}

	if !exists {
		m.logger.Debug("No persisted upgrade tasks found")

		return nil
	}

	loadedCount := 0

	for taskID := range tasksIndex {
		var task UpgradeTask

		taskPath := upgradeTasksVaultPath + "/" + taskID

		taskExists, err := m.vault.Get(ctx, taskPath, &task)
		if err != nil {
			m.logger.Error("Failed to load task %s: %v", taskID, err)

			continue
		}

		if !taskExists {
			m.logger.Debug("Task %s not found at path %s", taskID, taskPath)

			continue
		}

		m.tasks[taskID] = &task
		loadedCount++
	}

	m.logger.Info("Loaded %d persisted upgrade tasks", loadedCount)

	return nil
}

// persistTask saves an upgrade task to vault.
func (m *Manager) persistTask(ctx context.Context, task *UpgradeTask) error {
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

	// Update the tasks index
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

	m.logger.Debug("Task %s persisted successfully", task.ID)

	return nil
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
