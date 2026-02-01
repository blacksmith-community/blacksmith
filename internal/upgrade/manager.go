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

// Vault path for storing upgrade tasks.
const upgradeTasksVaultPath = "upgrade-tasks"

// Error variables for err113 compliance.
var (
	ErrTaskNotFound       = errors.New("upgrade task not found")
	ErrNoInstances        = errors.New("no instances specified for upgrade")
	ErrInvalidStemcell    = errors.New("invalid stemcell target")
	ErrInstanceNotFound   = errors.New("instance not found")
	ErrDeploymentMissing  = errors.New("deployment name missing for instance")
	ErrTaskNotRunning     = errors.New("task is not running")
	ErrTaskNotPaused      = errors.New("task is not paused")
	ErrInstanceNotRunning = errors.New("instance is not running")
	ErrNoBOSHTask         = errors.New("instance has no BOSH task")
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

	// Load persisted tasks from vault
	ctx := context.Background()
	if err := m.loadPersistedTasks(ctx); err != nil {
		m.logger.Error("Failed to load persisted tasks: %v", err)
	}

	// Start the task processor
	m.wg.Add(1)

	go m.processTaskQueue()

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

// processTaskQueue processes tasks from the queue.
func (m *Manager) processTaskQueue() {
	defer m.wg.Done()

	for {
		select {
		case <-m.stopCh:
			m.logger.Info("Upgrade manager stopping")

			return
		case task := <-m.taskQueue:
			m.processTask(task)
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
			instance.Status == InstanceStatusSkipped {
			continue
		}

		m.upgradeInstance(task, instance, cancelCh)
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

// upgradeInstance upgrades a single instance.
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

	// Update deployment (redeploy)
	m.logger.Debug("Updating deployment %s with new stemcell", instance.DeploymentName)

	boshTask, err := m.director.UpdateDeployment(instance.DeploymentName, newManifest)
	if err != nil {
		m.mu.Lock()
		instance.Status = InstanceStatusFailed
		instance.Error = fmt.Sprintf("failed to update deployment: %v", err)
		task.FailedCount++
		m.mu.Unlock()
		m.logger.Error("Failed to update deployment %s: %v", instance.DeploymentName, err)
		m.persistTask(ctx, task) //nolint:errcheck

		return
	}

	m.mu.Lock()
	instance.BOSHTaskID = boshTask.ID
	m.mu.Unlock()
	m.logger.Info("Started BOSH task %d for deployment %s", boshTask.ID, instance.DeploymentName)

	// Persist immediately so the BOSH task ID is visible in the UI
	m.persistTask(ctx, task) //nolint:errcheck

	// Wait for BOSH task to complete
	err = m.waitForBOSHTask(boshTask.ID, instance, cancelCh)
	if err != nil {
		m.mu.Lock()
		// Don't overwrite status if instance was already cancelled by CancelTask
		if instance.Status != InstanceStatusCancelled {
			instance.Status = InstanceStatusFailed
			instance.Error = fmt.Sprintf("BOSH task failed: %v", err)
			task.FailedCount++
			m.logger.Error("BOSH task %d failed for %s: %v", boshTask.ID, instance.DeploymentName, err)
		} else {
			m.logger.Info("Instance %s was cancelled, BOSH task error: %v", instance.InstanceID, err)
		}
		m.mu.Unlock()
		m.persistTask(ctx, task) //nolint:errcheck

		return
	}

	// Success
	completedAt := time.Now()

	m.mu.Lock()
	instance.CompletedAt = &completedAt
	instance.Status = InstanceStatusSuccess
	task.CompletedCount++
	m.mu.Unlock()
	m.logger.Info("Successfully upgraded instance %s", instance.InstanceID)

	// Persist progress
	m.persistTask(ctx, task) //nolint:errcheck
}

// waitForBOSHTask waits for a BOSH task to complete.
func (m *Manager) waitForBOSHTask(taskID int, instance *InstanceUpgrade, cancelCh <-chan struct{}) error {
	ticker := time.NewTicker(5 * time.Second) // Poll every 5 seconds for faster response
	defer ticker.Stop()

	// Timeout after 30 minutes
	timeout := time.After(30 * time.Minute)

	for {
		select {
		case <-timeout:
			return fmt.Errorf("timeout waiting for BOSH task %d", taskID)
		case <-ticker.C:
			task, err := m.director.GetTask(taskID)
			if err != nil {
				m.logger.Error("Failed to get BOSH task %d status: %v", taskID, err)

				continue
			}

			switch task.State {
			case "done":
				return nil
			case "error", "cancelled", "timeout":
				return fmt.Errorf("BOSH task %d ended with state: %s, result: %s", taskID, task.State, task.Result)
			default:
				m.logger.Debug("BOSH task %d still %s", taskID, task.State)
			}
		case <-cancelCh:
			m.logger.Info("Received cancel signal for BOSH task %d", taskID)
			return fmt.Errorf("BOSH task %d cancelled by user", taskID)
		case <-m.stopCh:
			return fmt.Errorf("manager stopped while waiting for BOSH task %d", taskID)
		}
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

// CancelInstance cancels a specific running instance within a task.
func (m *Manager) CancelInstance(ctx context.Context, taskID, instanceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, exists := m.tasks[taskID]
	if !exists {
		return ErrTaskNotFound
	}

	// Find the instance
	var instance *InstanceUpgrade
	for i := range task.Instances {
		if task.Instances[i].InstanceID == instanceID {
			instance = &task.Instances[i]
			break
		}
	}

	if instance == nil {
		return ErrInstanceNotFound
	}

	if instance.Status != InstanceStatusRunning {
		return ErrInstanceNotRunning
	}

	if instance.BOSHTaskID == 0 {
		return ErrNoBOSHTask
	}

	m.logger.Info("Cancelling BOSH task %d for instance %s in task %s", instance.BOSHTaskID, instanceID, taskID)

	// Cancel the BOSH task
	if err := m.director.CancelTask(instance.BOSHTaskID); err != nil {
		m.logger.Error("Failed to cancel BOSH task %d: %v", instance.BOSHTaskID, err)
		return fmt.Errorf("failed to cancel BOSH task: %w", err)
	}

	instance.Status = InstanceStatusCancelled
	instance.Error = "cancelled by user"
	task.CancelledCount++

	// Persist the updated state
	if err := m.persistTask(ctx, task); err != nil {
		m.logger.Error("Failed to persist instance cancellation for task %s: %v", taskID, err)
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
	CancelInstance(ctx context.Context, taskID, instanceID string) error
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
		m.logger.Error("Failed to get tasks index: %v", err)
		tasksIndex = make(map[string]interface{})
	}

	if !exists {
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
