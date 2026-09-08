package upgrade

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"blacksmith/internal/interfaces"

	"github.com/google/uuid"
)

// Vault paths for storing upgrade data.
const (
	upgradeTasksVaultPath    = "upgrade-tasks"
	upgradeSettingsVaultPath = "upgrade-settings"
)

// Error variables for err113 compliance.
var (
	ErrTaskNotFound    = errors.New("upgrade task not found")
	ErrNoInstances     = errors.New("no instances specified for upgrade")
	ErrInvalidStemcell = errors.New("invalid stemcell target")
	ErrTaskNotRunning  = errors.New("task is not running")
	ErrTaskNotPaused   = errors.New("task is not paused")
)

// Watch tuning (vars, not consts, so tests can shrink them).
var (
	// instanceDeadline bounds how long we watch a single instance's BOSH task before giving
	// up, marking it timed-out, and moving on to the next instance. The deployment is left
	// running. No service upgrade should take longer than this.
	instanceDeadline = 60 * time.Minute
	// pollInterval is how often we poll the BOSH task state.
	pollInterval = 5 * time.Second
	// perCallTimeout bounds a single GetTask poll so one stalled connection cannot hang the
	// watch; the next tick retries on a fresh connection.
	perCallTimeout = 30 * time.Second
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
			instance.Status == InstanceStatusSkipped ||
			instance.Status == InstanceStatusTimedOut {
			continue
		}

		m.upgradeInstance(task, instance, cancelCh)
	}

	// Update task status (only if not already cancelled)
	m.mu.Lock()
	if task.Status != TaskStatusCancelled {
		completedAt := time.Now()
		task.CompletedAt = &completedAt

		// Only a job with NO successes and NO timeouts (i.e. everything hard-failed) is
		// "failed". Timeouts count as partial completion (the deploy may still finish).
		if task.CompletedCount == 0 && task.TimedOutCount == 0 && task.FailedCount > 0 {
			task.Status = TaskStatusFailed
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

// upgradeInstance upgrades a single instance using 2 goroutines:
// 1. Goroutine 1: Runs UpdateDeployment (blocking until BOSH task completes)
// 2. Goroutine 2: Polls to find and store the BOSH task ID for UI display
// Main thread waits for completion or task-level cancel signal.
func (m *Manager) upgradeInstance(task *UpgradeTask, instance *InstanceUpgrade, cancelCh <-chan struct{}) {
	ctx := context.Background()
	m.logger.Info("Upgrading instance %s (deployment: %s)", instance.InstanceID, instance.DeploymentName)

	now := time.Now()

	m.mu.Lock()
	instance.StartedAt = &now
	instance.Status = InstanceStatusRunning
	m.mu.Unlock()

	// Persist running state immediately so the UI shows the instance as running
	m.persistTask(ctx, task) //nolint:errcheck

	// Validate deployment name
	if instance.DeploymentName == "" {
		m.mu.Lock()
		instance.Status = InstanceStatusFailed
		instance.Error = "deployment name is missing"
		task.FailedCount++
		m.mu.Unlock()
		m.logger.Error("Instance %s has no deployment name", instance.InstanceID)
		m.persistTask(ctx, task) //nolint:errcheck
		return
	}

	// Get current deployment manifest
	m.logger.Debug("Getting deployment %s for instance %s", instance.DeploymentName, instance.InstanceID)
	deployment, err := m.director.GetDeployment(instance.DeploymentName)
	if err != nil {
		m.mu.Lock()
		instance.Status = InstanceStatusFailed
		instance.Error = fmt.Sprintf("failed to get deployment: %v", err)
		task.FailedCount++
		m.mu.Unlock()
		m.logger.Error("Failed to get deployment %s: %v", instance.DeploymentName, err)
		m.persistTask(ctx, task) //nolint:errcheck
		return
	}

	if deployment == nil {
		m.mu.Lock()
		instance.Status = InstanceStatusFailed
		instance.Error = "deployment returned nil without error"
		task.FailedCount++
		m.mu.Unlock()
		m.logger.Error("Deployment %s returned nil without error", instance.DeploymentName)
		m.persistTask(ctx, task) //nolint:errcheck
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
		m.persistTask(ctx, task) //nolint:errcheck
		return
	}

	m.logger.Debug("Stemcell overlay merged successfully for %s, new manifest size: %d bytes",
		instance.DeploymentName, len(newManifest))

	// === FIRE, THEN WATCH THE TASK OURSELVES ===
	// We deliberately do NOT use the blocking dep.Update() (UpdateDeployment): it has no
	// request timeout and hangs forever on a stalled connection, wedging the whole job and
	// leaking its batch slot. Instead we fire the update (returns immediately with a task id)
	// and poll GetTask with our own per-call timeout and an overall per-instance deadline.
	m.logger.Debug("Firing deployment update for %s", instance.DeploymentName)

	fired, err := m.director.UpdateDeploymentAsync(instance.DeploymentName, newManifest)
	if err != nil {
		m.mu.Lock()
		instance.Status = InstanceStatusFailed
		instance.Error = fmt.Sprintf("failed to start deployment update: %v", err)
		task.FailedCount++
		m.mu.Unlock()
		m.logger.Error("Failed to fire update for %s: %v", instance.DeploymentName, err)
		m.persistTask(ctx, task) //nolint:errcheck

		return
	}

	m.mu.Lock()
	instance.BOSHTaskID = fired.ID
	m.mu.Unlock()
	m.logger.Info("Deployment update for %s running as BOSH task %d", instance.DeploymentName, fired.ID)
	m.persistTask(ctx, task) //nolint:errcheck

	outcome, msg := m.watchTask(instance, fired.ID, cancelCh)

	m.mu.Lock()
	switch outcome {
	case InstanceStatusSuccess:
		completedAt := time.Now()
		instance.CompletedAt = &completedAt
		instance.Status = InstanceStatusSuccess
		task.CompletedCount++
		m.logger.Info("Successfully upgraded instance %s", instance.InstanceID)
	case InstanceStatusTimedOut:
		instance.Status = InstanceStatusTimedOut
		instance.Error = msg
		task.TimedOutCount++
		m.logger.Error("Instance %s timed out: %s", instance.InstanceID, msg)
	case InstanceStatusCancelled:
		instance.Status = InstanceStatusCancelled
		task.CancelledCount++
		m.logger.Info("Instance %s cancelled", instance.InstanceID)
	default: // InstanceStatusFailed
		instance.Status = InstanceStatusFailed
		instance.Error = msg
		task.FailedCount++
		m.logger.Error("Instance %s failed: %s", instance.InstanceID, msg)
	}
	m.mu.Unlock()

	m.persistTask(ctx, task) //nolint:errcheck
}

// watchTask polls the BOSH task until it reaches a terminal state, the per-instance deadline
// elapses, or a cancel/stop signal arrives. Each GetTask call is bounded by perCallTimeout so
// a single stalled poll cannot hang the watch — the next tick retries on a fresh connection.
// On deadline the BOSH deployment is LEFT RUNNING (not cancelled) and the instance is marked
// timed-out for manual verification. Returns the resolved status and a message.
func (m *Manager) watchTask(instance *InstanceUpgrade, taskID int, cancelCh <-chan struct{}) (InstanceStatus, string) {
	deadline := time.After(instanceDeadline)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	// pollState fetches the task state with a per-call timeout. A stalled call is abandoned
	// (its goroutine leaks until the underlying HTTP call returns) and reported as not-ready,
	// so the next tick retries on a new connection.
	pollState := func() (state string, ok bool) {
		type res struct {
			state string
			ok    bool
		}

		ch := make(chan res, 1)

		go func() {
			t, err := m.director.GetTask(taskID)
			if err != nil || t == nil {
				ch <- res{"", false}

				return
			}

			ch <- res{t.State, true}
		}()

		select {
		case r := <-ch:
			return r.state, r.ok
		case <-time.After(perCallTimeout):
			m.logger.Debug("GetTask(%d) for %s stalled past %s; retrying next tick",
				taskID, instance.DeploymentName, perCallTimeout)

			return "", false
		}
	}

	for {
		if state, ok := pollState(); ok {
			switch state {
			case "done":
				return InstanceStatusSuccess, ""
			case "error", "errored", "cancelled", "cancelling":
				return InstanceStatusFailed, fmt.Sprintf("BOSH task %d finished in state %q", taskID, state)
			}
			// queued/processing → keep watching
		}

		select {
		case <-ticker.C:
			// poll again
		case <-deadline:
			return InstanceStatusTimedOut, fmt.Sprintf(
				"exceeded %s deadline; BOSH task %d may still be running — verify manually",
				instanceDeadline, taskID)
		case <-cancelCh:
			m.cancelRunningTaskForDeployment(instance.DeploymentName)

			return InstanceStatusCancelled, ""
		case <-m.stopCh:
			return InstanceStatusCancelled, "manager stopped"
		}
	}
}

// cancelRunningTaskForDeployment finds and cancels any running BOSH task for the deployment.
func (m *Manager) cancelRunningTaskForDeployment(deploymentName string) {
	runningTask, err := m.director.FindRunningTaskForDeployment(deploymentName)
	if err != nil {
		m.logger.Error("Failed to find running task for %s: %v", deploymentName, err)
		return
	}

	if runningTask != nil {
		m.logger.Info("Cancelling BOSH task %d for deployment %s", runningTask.ID, deploymentName)
		if err := m.director.CancelTask(runningTask.ID); err != nil {
			m.logger.Error("Failed to cancel BOSH task %d: %v", runningTask.ID, err)
		}
	} else {
		m.logger.Debug("No running task found to cancel for %s", deploymentName)
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
	m.logger.Info("Paused task %s", taskID)
	// Release before persisting: persistTask snapshots under its own RLock, so we must not
	// hold the write lock across it (would deadlock and would hold the lock during I/O).
	m.mu.Unlock()

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

// CancelTask cancels a running or paused task.
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

	// Find and cancel any running instance's BOSH task
	for i := range task.Instances {
		instance := &task.Instances[i]
		if instance.Status == InstanceStatusRunning && instance.BOSHTaskID > 0 {
			m.logger.Info("Cancelling BOSH task %d for instance %s", instance.BOSHTaskID, instance.InstanceID)
			if err := m.director.CancelTask(instance.BOSHTaskID); err != nil {
				m.logger.Error("Failed to cancel BOSH task %d: %v", instance.BOSHTaskID, err)
			}
			instance.Status = InstanceStatusCancelled
			instance.Error = "cancelled by user"
			task.CancelledCount++
		} else if instance.Status == InstanceStatusPending {
			instance.Status = InstanceStatusSkipped
			instance.Error = "skipped due to task cancellation"
			task.SkippedCount++
		}
	}

	task.Status = TaskStatusCancelled
	completedAt := time.Now()
	task.CompletedAt = &completedAt
	m.mu.Unlock()

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

// persistTask saves an upgrade task to vault. It marshals a consistent SNAPSHOT taken under
// the read lock, so it never races the goroutines that mutate the task under the write lock.
// Callers must NOT hold m.mu when calling this (it takes m.mu.RLock and does I/O).
func (m *Manager) persistTask(ctx context.Context, task *UpgradeTask) error {
	m.logger.Debug("Persisting task %s to vault", task.ID)

	// Snapshot under RLock: copy the struct + deep-copy the Instances slice so marshaling
	// reads stable data. (Instance time pointers are set once and never mutated afterward.)
	m.mu.RLock()
	snapshot := *task
	snapshot.Instances = make([]InstanceUpgrade, len(task.Instances))
	copy(snapshot.Instances, task.Instances)
	m.mu.RUnlock()

	// Save the full task data
	taskPath := upgradeTasksVaultPath + "/" + snapshot.ID

	err := m.vault.Put(ctx, taskPath, &snapshot)
	if err != nil {
		return fmt.Errorf("failed to save task to vault: %w", err)
	}

	// Update the tasks index
	var tasksIndex map[string]interface{}

	exists, err := m.vault.Get(ctx, upgradeTasksVaultPath, &tasksIndex)
	if err != nil {
		m.logger.Error("Failed to get tasks index: %v", err)
		tasksIndex = make(map[string]interface{})
	}

	if !exists {
		tasksIndex = make(map[string]interface{})
	}

	// Add task reference to index (from the snapshot, not the live task)
	tasksIndex[snapshot.ID] = map[string]interface{}{
		"status":     snapshot.Status,
		"created_at": snapshot.CreatedAt.Unix(),
	}

	err = m.vault.Put(ctx, upgradeTasksVaultPath, tasksIndex)
	if err != nil {
		return fmt.Errorf("failed to update tasks index: %w", err)
	}

	m.logger.Debug("Task %s persisted successfully", snapshot.ID)

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
