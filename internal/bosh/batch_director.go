package bosh

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// Error for batch pool timeout.
var (
	ErrBatchPoolTimeout = errors.New("timeout waiting for batch connection slot")
)

// BatchDirector wraps a Director with a separate connection pool for batch operations.
// This allows batch upgrade jobs to use their own pool without competing with
// normal API operations for connection slots.
type BatchDirector struct {
	director  Director          // Base director (not pooled)
	semaphore chan struct{}     // Batch operation semaphore
	timeout   time.Duration     // Timeout for acquiring slot
	logger    Logger

	// Metrics
	activeConnections int32
	queuedRequests    int32
}

// NewBatchDirector creates a new batch director wrapper.
func NewBatchDirector(director Director, maxBatchJobs int, timeout time.Duration, logger Logger) *BatchDirector {
	if maxBatchJobs <= 0 {
		maxBatchJobs = 4 // Default
	}

	return &BatchDirector{
		director:  director,
		semaphore: make(chan struct{}, maxBatchJobs),
		timeout:   timeout,
		logger:    logger,
	}
}

// acquireSlot waits for an available batch slot.
func (b *BatchDirector) acquireSlot(ctx context.Context) error {
	atomic.AddInt32(&b.queuedRequests, 1)
	defer atomic.AddInt32(&b.queuedRequests, -1)

	select {
	case b.semaphore <- struct{}{}:
		atomic.AddInt32(&b.activeConnections, 1)
		if b.logger != nil {
			b.logger.Debugf("Acquired batch slot (active: %d, queued: %d)",
				atomic.LoadInt32(&b.activeConnections),
				atomic.LoadInt32(&b.queuedRequests))
		}
		return nil

	case <-time.After(b.timeout):
		return fmt.Errorf("%w after %v", ErrBatchPoolTimeout, b.timeout)

	case <-ctx.Done():
		return fmt.Errorf("context cancelled while waiting for batch slot: %w", ctx.Err())
	}
}

// releaseSlot releases a batch slot back to the pool.
func (b *BatchDirector) releaseSlot() {
	<-b.semaphore
	atomic.AddInt32(&b.activeConnections, -1)
	if b.logger != nil {
		b.logger.Debugf("Released batch slot (active: %d, queued: %d)",
			atomic.LoadInt32(&b.activeConnections),
			atomic.LoadInt32(&b.queuedRequests))
	}
}

// GetBatchPoolStats returns current batch pool statistics.
func (b *BatchDirector) GetBatchPoolStats() (active, queued, max int) {
	return int(atomic.LoadInt32(&b.activeConnections)),
		int(atomic.LoadInt32(&b.queuedRequests)),
		cap(b.semaphore)
}

// UpdateMaxBatchJobs updates the maximum number of concurrent batch jobs.
// Note: This creates a new semaphore. Existing in-flight operations continue
// with the old semaphore until they complete.
func (b *BatchDirector) UpdateMaxBatchJobs(maxBatchJobs int) {
	if maxBatchJobs <= 0 {
		maxBatchJobs = 4
	}
	b.semaphore = make(chan struct{}, maxBatchJobs)
	if b.logger != nil {
		b.logger.Infof("Updated batch pool size to %d", maxBatchJobs)
	}
}

// =====================================================
// Director interface implementation
// =====================================================

// GetDeployment passes through to the base director (read operation, no pooling).
func (b *BatchDirector) GetDeployment(name string) (*DeploymentDetail, error) {
	return b.director.GetDeployment(name)
}

// UpdateDeployment uses the batch pool for rate limiting.
// This is the main blocking operation during batch upgrades.
func (b *BatchDirector) UpdateDeployment(name, manifest string) (*Task, error) {
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout)
	defer cancel()

	if err := b.acquireSlot(ctx); err != nil {
		return nil, fmt.Errorf("failed to acquire batch slot: %w", err)
	}
	defer b.releaseSlot()

	return b.director.UpdateDeployment(name, manifest)
}

// FindRunningTaskForDeployment passes through (read operation).
func (b *BatchDirector) FindRunningTaskForDeployment(deploymentName string) (*Task, error) {
	return b.director.FindRunningTaskForDeployment(deploymentName)
}

// CancelTask passes through (quick write operation, uses general pool).
func (b *BatchDirector) CancelTask(taskID int) error {
	return b.director.CancelTask(taskID)
}

// =====================================================
// Pass-through methods for full Director interface
// These are needed if upgrade manager uses the full interface
// =====================================================

func (b *BatchDirector) GetInfo() (*Info, error) {
	return b.director.GetInfo()
}

func (b *BatchDirector) GetDeployments() ([]Deployment, error) {
	return b.director.GetDeployments()
}

func (b *BatchDirector) CreateDeployment(manifest string) (*Task, error) {
	return b.director.CreateDeployment(manifest)
}

func (b *BatchDirector) DeleteDeployment(name string) (*Task, error) {
	return b.director.DeleteDeployment(name)
}

func (b *BatchDirector) GetDeploymentVMs(deployment string) ([]VM, error) {
	return b.director.GetDeploymentVMs(deployment)
}

func (b *BatchDirector) GetReleases() ([]Release, error) {
	return b.director.GetReleases()
}

func (b *BatchDirector) UploadRelease(url, sha1 string) (*Task, error) {
	return b.director.UploadRelease(url, sha1)
}

func (b *BatchDirector) GetStemcells() ([]Stemcell, error) {
	return b.director.GetStemcells()
}

func (b *BatchDirector) UploadStemcell(url, sha1 string) (*Task, error) {
	return b.director.UploadStemcell(url, sha1)
}

func (b *BatchDirector) GetTask(id int) (*Task, error) {
	return b.director.GetTask(id)
}

func (b *BatchDirector) GetTasks(taskType string, limit int, states []string, team string) ([]Task, error) {
	return b.director.GetTasks(taskType, limit, states, team)
}

func (b *BatchDirector) GetAllTasks(limit int) ([]Task, error) {
	return b.director.GetAllTasks(limit)
}

func (b *BatchDirector) GetTaskOutput(id int, outputType string) (string, error) {
	return b.director.GetTaskOutput(id, outputType)
}

func (b *BatchDirector) GetTaskEvents(id int) ([]TaskEvent, error) {
	return b.director.GetTaskEvents(id)
}

func (b *BatchDirector) GetEvents(deployment string) ([]Event, error) {
	return b.director.GetEvents(deployment)
}

func (b *BatchDirector) UpdateCloudConfig(config string) error {
	return b.director.UpdateCloudConfig(config)
}

func (b *BatchDirector) GetCloudConfig() (string, error) {
	return b.director.GetCloudConfig()
}

func (b *BatchDirector) GetConfigs(limit int, configTypes []string) ([]BoshConfig, error) {
	return b.director.GetConfigs(limit, configTypes)
}

func (b *BatchDirector) GetConfigVersions(configType, name string, limit int) ([]BoshConfig, error) {
	return b.director.GetConfigVersions(configType, name, limit)
}

func (b *BatchDirector) GetConfigByID(configID string) (*BoshConfigDetail, error) {
	return b.director.GetConfigByID(configID)
}

func (b *BatchDirector) GetConfigContent(configID string) (string, error) {
	return b.director.GetConfigContent(configID)
}

func (b *BatchDirector) GetConfig(configType, configName string) (interface{}, error) {
	return b.director.GetConfig(configType, configName)
}

func (b *BatchDirector) ComputeConfigDiff(fromID, toID string) (*ConfigDiff, error) {
	return b.director.ComputeConfigDiff(fromID, toID)
}

func (b *BatchDirector) Cleanup(removeAll bool) (*Task, error) {
	return b.director.Cleanup(removeAll)
}

func (b *BatchDirector) FetchLogs(deployment, jobName, jobIndex string) (string, error) {
	return b.director.FetchLogs(deployment, jobName, jobIndex)
}

func (b *BatchDirector) SSHCommand(deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) {
	return b.director.SSHCommand(deployment, instance, index, command, args, options)
}

func (b *BatchDirector) SSHSession(deployment, instance string, index int, options map[string]interface{}) (interface{}, error) {
	return b.director.SSHSession(deployment, instance, index, options)
}

func (b *BatchDirector) EnableResurrection(deployment string, enabled bool) error {
	return b.director.EnableResurrection(deployment, enabled)
}

func (b *BatchDirector) DeleteResurrectionConfig(deploymentName string) error {
	return b.director.DeleteResurrectionConfig(deploymentName)
}

func (b *BatchDirector) RestartDeployment(name string, opts RestartOpts) (*Task, error) {
	return b.director.RestartDeployment(name, opts)
}

func (b *BatchDirector) StopDeployment(name string, opts StopOpts) (*Task, error) {
	return b.director.StopDeployment(name, opts)
}

func (b *BatchDirector) StartDeployment(name string, opts StartOpts) (*Task, error) {
	return b.director.StartDeployment(name, opts)
}

func (b *BatchDirector) RecreateDeployment(name string, opts RecreateOpts) (*Task, error) {
	return b.director.RecreateDeployment(name, opts)
}

func (b *BatchDirector) ListErrands(deployment string) ([]Errand, error) {
	return b.director.ListErrands(deployment)
}

func (b *BatchDirector) RunErrand(deployment, errand string, opts ErrandOpts) (*ErrandResult, error) {
	return b.director.RunErrand(deployment, errand, opts)
}

func (b *BatchDirector) GetInstances(deployment string) ([]Instance, error) {
	return b.director.GetInstances(deployment)
}

func (b *BatchDirector) GetPoolStats() (*PoolStats, error) {
	return b.director.GetPoolStats()
}

// Ensure BatchDirector implements Director interface.
var _ Director = (*BatchDirector)(nil)
