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

// Error variables for err113 compliance.
var (
	ErrTaskNotFound      = errors.New("upgrade task not found")
	ErrNoInstances       = errors.New("no instances specified for upgrade")
	ErrInvalidStemcell   = errors.New("invalid stemcell target")
	ErrInstanceNotFound  = errors.New("instance not found")
	ErrDeploymentMissing = errors.New("deployment name missing for instance")
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
}

// NewManager creates a new upgrade manager.
func NewManager(logger interfaces.Logger, director interfaces.Director, vault interfaces.Vault) *Manager {
	m := &Manager{
		tasks:     make(map[string]*UpgradeTask),
		logger:    logger.Named("upgrade-manager"),
		director:  director,
		vault:     vault,
		taskQueue: make(chan *UpgradeTask, 100),
		stopCh:    make(chan struct{}),
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
	// Recover from any panics to prevent crashing the entire blacksmith process
	defer func() {
		if r := recover(); r != nil {
			m.logger.Error("PANIC in processTask for task %s: %v", task.ID, r)
			m.mu.Lock()
			task.Status = TaskStatusFailed
			completedAt := time.Now()
			task.CompletedAt = &completedAt
			m.mu.Unlock()
		}
	}()

	m.logger.Info("Processing upgrade task %s", task.ID)

	now := time.Now()

	m.mu.Lock()
	task.StartedAt = &now
	task.Status = TaskStatusRunning
	m.mu.Unlock()

	// Process each instance sequentially
	for i := range task.Instances {
		instance := &task.Instances[i]

		// Skip already failed instances
		if instance.Status == InstanceStatusFailed {
			m.mu.Lock()
			task.FailedCount++
			m.mu.Unlock()

			continue
		}

		m.upgradeInstance(task, instance)
	}

	// Update task status
	completedAt := time.Now()

	m.mu.Lock()
	task.CompletedAt = &completedAt

	if task.FailedCount > 0 && task.CompletedCount == 0 {
		task.Status = TaskStatusFailed
	} else if task.FailedCount > 0 {
		task.Status = TaskStatusCompleted // Partial success
	} else {
		task.Status = TaskStatusCompleted
	}
	m.mu.Unlock()

	m.logger.Info("Upgrade task %s completed: %d/%d succeeded, %d failed",
		task.ID, task.CompletedCount, task.TotalCount, task.FailedCount)
}

// upgradeInstance upgrades a single instance.
func (m *Manager) upgradeInstance(task *UpgradeTask, instance *InstanceUpgrade) {
	m.logger.Info("Upgrading instance %s (deployment: %s)", instance.InstanceID, instance.DeploymentName)

	now := time.Now()

	m.mu.Lock()
	instance.StartedAt = &now
	instance.Status = InstanceStatusRunning
	m.mu.Unlock()

	// Validate deployment name
	if instance.DeploymentName == "" {
		m.mu.Lock()
		instance.Status = InstanceStatusFailed
		instance.Error = "deployment name is missing"
		task.FailedCount++
		m.mu.Unlock()
		m.logger.Error("Instance %s has no deployment name", instance.InstanceID)

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

		return
	}

	if deployment == nil {
		m.mu.Lock()
		instance.Status = InstanceStatusFailed
		instance.Error = "deployment returned nil without error"
		task.FailedCount++
		m.mu.Unlock()
		m.logger.Error("Deployment %s returned nil without error", instance.DeploymentName)

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

		return
	}

	m.mu.Lock()
	instance.BOSHTaskID = boshTask.ID
	m.mu.Unlock()
	m.logger.Info("Started BOSH task %d for deployment %s", boshTask.ID, instance.DeploymentName)

	// Wait for BOSH task to complete
	err = m.waitForBOSHTask(boshTask.ID, instance)
	if err != nil {
		m.mu.Lock()
		instance.Status = InstanceStatusFailed
		instance.Error = fmt.Sprintf("BOSH task failed: %v", err)
		task.FailedCount++
		m.mu.Unlock()
		m.logger.Error("BOSH task %d failed for %s: %v", boshTask.ID, instance.DeploymentName, err)

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
}

// waitForBOSHTask waits for a BOSH task to complete.
func (m *Manager) waitForBOSHTask(taskID int, instance *InstanceUpgrade) error {
	ticker := time.NewTicker(10 * time.Second)
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

// ManagerInterface defines the interface for the upgrade manager.
type ManagerInterface interface {
	CreateTask(ctx context.Context, req CreateTaskRequest) (*UpgradeTask, error)
	GetTask(taskID string) (*UpgradeTask, error)
	ListTasks() []*UpgradeTask
	GetInstancesForTask(taskID string) ([]InstanceUpgrade, error)
	Stop()
}

// Ensure Manager implements ManagerInterface.
var _ ManagerInterface = (*Manager)(nil)
