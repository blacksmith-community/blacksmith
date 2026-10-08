package bosh

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Error for batch pool timeout.
var (
	ErrBatchPoolTimeout = errors.New("timeout waiting for batch connection slot")
)

// defaultMaxBatchJobs is the limit a max_batch_jobs of zero or less means.
const defaultMaxBatchJobs = 4

// BatchDirector wraps a Director with a separate connection pool for batch operations.
// This allows batch upgrade jobs to use their own pool without competing with
// normal API operations for connection slots.
//
// The slots are counted under one mutex. inFlight is how many deploys hold a
// slot, limit is how many may, and changed is closed and replaced whenever a
// slot is released or the limit changes, so a deploy waiting for a slot
// looks again.
type BatchDirector struct {
	director Director      // Base director (not pooled)
	timeout  time.Duration // Timeout for acquiring slot
	logger   Logger

	mu       sync.Mutex
	inFlight int
	limit    int
	queued   int
	changed  chan struct{}
}

// NewBatchDirector creates a new batch director wrapper.
func NewBatchDirector(director Director, maxBatchJobs int, timeout time.Duration, logger Logger) *BatchDirector {
	if maxBatchJobs <= 0 {
		maxBatchJobs = defaultMaxBatchJobs
	}

	if logger == nil {
		logger = &noOpLogger{}
	}

	return &BatchDirector{
		director: director,
		timeout:  timeout,
		logger:   logger,
		limit:    maxBatchJobs,
		changed:  make(chan struct{}),
	}
}

// GetBatchPoolStats returns how many deploys hold a slot, how many wait
// for one, and the limit.
func (b *BatchDirector) GetBatchPoolStats() (int, int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.inFlight, b.queued, b.limit
}

// UpdateMaxBatchJobs changes the maximum number of concurrent batch jobs.
// Deploys that already hold a slot stay counted against the new limit, so
// a smaller limit admits no new deploy until enough of them finish.
func (b *BatchDirector) UpdateMaxBatchJobs(maxBatchJobs int) {
	if maxBatchJobs <= 0 {
		maxBatchJobs = defaultMaxBatchJobs
	}

	b.mu.Lock()
	b.limit = maxBatchJobs
	b.signalLocked()
	b.mu.Unlock()

	b.logger.Infof("Updated batch pool size to %d", maxBatchJobs)
}

// =====================================================
// Director interface implementation
// =====================================================

// GetDeployment passes through to the base director (read operation, no pooling).
func (b *BatchDirector) GetDeployment(ctx context.Context, name string) (*DeploymentDetail, error) {
	return b.director.GetDeployment(ctx, name)
}

// UpdateDeployment uses the batch pool for rate limiting.
// This is the main blocking operation during batch upgrades.
func (b *BatchDirector) UpdateDeployment(ctx context.Context, name, manifest string) (*Task, error) {
	if err := b.acquireSlot(ctx); err != nil {
		return nil, fmt.Errorf("failed to acquire batch slot: %w", err)
	}
	defer b.releaseSlot()

	return b.director.UpdateDeployment(ctx, name, manifest)
}

// FindRunningTaskForDeployment passes through (read operation).
func (b *BatchDirector) FindRunningTaskForDeployment(ctx context.Context, deploymentName string) (*Task, error) {
	return b.director.FindRunningTaskForDeployment(ctx, deploymentName)
}

// FindTaskByContextID passes through (read operation).
func (b *BatchDirector) FindTaskByContextID(ctx context.Context, contextID string) (*Task, error) {
	task, err := b.director.FindTaskByContextID(ctx, contextID)
	if err != nil {
		return nil, fmt.Errorf("find the task with context ID %s: %w", contextID, err)
	}

	return task, nil
}

// CancelTask passes through (quick write operation, uses general pool).
func (b *BatchDirector) CancelTask(ctx context.Context, taskID int) error {
	return b.director.CancelTask(ctx, taskID)
}

// =====================================================
// Pass-through methods for full Director interface
// These are needed if upgrade manager uses the full interface
// =====================================================

func (b *BatchDirector) GetInfo(ctx context.Context) (*Info, error) {
	return b.director.GetInfo(ctx)
}

func (b *BatchDirector) GetDeployments(ctx context.Context) ([]Deployment, error) {
	return b.director.GetDeployments(ctx)
}

func (b *BatchDirector) CreateDeployment(ctx context.Context, manifest string) (*Task, error) {
	return b.director.CreateDeployment(ctx, manifest)
}

func (b *BatchDirector) DeleteDeployment(ctx context.Context, name string) (*Task, error) {
	return b.director.DeleteDeployment(ctx, name)
}

func (b *BatchDirector) GetDeploymentVMs(ctx context.Context, deployment string) ([]VM, error) {
	return b.director.GetDeploymentVMs(ctx, deployment)
}

func (b *BatchDirector) GetReleases(ctx context.Context) ([]Release, error) {
	return b.director.GetReleases(ctx)
}

func (b *BatchDirector) UploadRelease(ctx context.Context, url, sha1 string) (*Task, error) {
	return b.director.UploadRelease(ctx, url, sha1)
}

func (b *BatchDirector) GetStemcells(ctx context.Context) ([]Stemcell, error) {
	return b.director.GetStemcells(ctx)
}

func (b *BatchDirector) UploadStemcell(ctx context.Context, url, sha1 string) (*Task, error) {
	return b.director.UploadStemcell(ctx, url, sha1)
}

func (b *BatchDirector) GetTask(ctx context.Context, id int) (*Task, error) {
	return b.director.GetTask(ctx, id)
}

func (b *BatchDirector) GetTasks(ctx context.Context, taskType string, limit int, states []string, team string) ([]Task, error) {
	return b.director.GetTasks(ctx, taskType, limit, states, team)
}

func (b *BatchDirector) GetAllTasks(ctx context.Context, limit int) ([]Task, error) {
	return b.director.GetAllTasks(ctx, limit)
}

// WaitForTask passes through (a series of task reads, holding no slot).
func (b *BatchDirector) WaitForTask(ctx context.Context, id int) (*Task, error) {
	return b.director.WaitForTask(ctx, id)
}

func (b *BatchDirector) GetTaskOutput(ctx context.Context, id int, outputType string) (string, error) {
	return b.director.GetTaskOutput(ctx, id, outputType)
}

func (b *BatchDirector) GetTaskEvents(ctx context.Context, id int) ([]TaskEvent, error) {
	return b.director.GetTaskEvents(ctx, id)
}

func (b *BatchDirector) GetEvents(ctx context.Context, deployment string) ([]Event, error) {
	return b.director.GetEvents(ctx, deployment)
}

func (b *BatchDirector) UpdateCloudConfig(ctx context.Context, config string) error {
	return b.director.UpdateCloudConfig(ctx, config)
}

func (b *BatchDirector) GetCloudConfig(ctx context.Context) (string, error) {
	return b.director.GetCloudConfig(ctx)
}

func (b *BatchDirector) ListCloudConfigs(ctx context.Context) ([]CloudConfigEntry, error) {
	entries, err := b.director.ListCloudConfigs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list cloud configs: %w", err)
	}

	return entries, nil
}

func (b *BatchDirector) GetConfigs(ctx context.Context, limit int, configTypes []string) ([]BoshConfig, error) {
	return b.director.GetConfigs(ctx, limit, configTypes)
}

func (b *BatchDirector) GetConfigVersions(ctx context.Context, configType, name string, limit int) ([]BoshConfig, error) {
	return b.director.GetConfigVersions(ctx, configType, name, limit)
}

func (b *BatchDirector) GetConfigByID(ctx context.Context, configID string) (*BoshConfigDetail, error) {
	return b.director.GetConfigByID(ctx, configID)
}

func (b *BatchDirector) GetConfigContent(ctx context.Context, configID string) (string, error) {
	return b.director.GetConfigContent(ctx, configID)
}

func (b *BatchDirector) GetConfig(ctx context.Context, configType, configName string) (interface{}, error) {
	return b.director.GetConfig(ctx, configType, configName)
}

func (b *BatchDirector) ComputeConfigDiff(ctx context.Context, fromID, toID string) (*ConfigDiff, error) {
	return b.director.ComputeConfigDiff(ctx, fromID, toID)
}

func (b *BatchDirector) Cleanup(ctx context.Context, removeAll bool) (*Task, error) {
	return b.director.Cleanup(ctx, removeAll)
}

func (b *BatchDirector) FetchLogs(ctx context.Context, deployment, jobName, jobIndex string) (string, error) {
	return b.director.FetchLogs(ctx, deployment, jobName, jobIndex)
}

func (b *BatchDirector) SSHCommand(ctx context.Context, deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) {
	return b.director.SSHCommand(ctx, deployment, instance, index, command, args, options)
}

func (b *BatchDirector) SSHSession(ctx context.Context, deployment, instance string, index int, options map[string]interface{}) (interface{}, error) {
	return b.director.SSHSession(ctx, deployment, instance, index, options)
}

func (b *BatchDirector) EnableResurrection(ctx context.Context, deployment string, enabled bool) error {
	return b.director.EnableResurrection(ctx, deployment, enabled)
}

func (b *BatchDirector) DeleteResurrectionConfig(ctx context.Context, deploymentName string) error {
	return b.director.DeleteResurrectionConfig(ctx, deploymentName)
}

func (b *BatchDirector) RestartDeployment(ctx context.Context, name string, opts RestartOpts) (*Task, error) {
	return b.director.RestartDeployment(ctx, name, opts)
}

func (b *BatchDirector) StopDeployment(ctx context.Context, name string, opts StopOpts) (*Task, error) {
	return b.director.StopDeployment(ctx, name, opts)
}

func (b *BatchDirector) StartDeployment(ctx context.Context, name string, opts StartOpts) (*Task, error) {
	return b.director.StartDeployment(ctx, name, opts)
}

func (b *BatchDirector) RecreateDeployment(ctx context.Context, name string, opts RecreateOpts) (*Task, error) {
	return b.director.RecreateDeployment(ctx, name, opts)
}

func (b *BatchDirector) ListErrands(ctx context.Context, deployment string) ([]Errand, error) {
	return b.director.ListErrands(ctx, deployment)
}

func (b *BatchDirector) RunErrand(ctx context.Context, deployment, errand string, opts ErrandOpts) (*ErrandResult, error) {
	return b.director.RunErrand(ctx, deployment, errand, opts)
}

func (b *BatchDirector) GetInstances(ctx context.Context, deployment string) ([]Instance, error) {
	return b.director.GetInstances(ctx, deployment)
}

func (b *BatchDirector) GetPoolStats() (*PoolStats, error) {
	return b.director.GetPoolStats()
}

// Ensure BatchDirector implements Director interface.
var _ Director = (*BatchDirector)(nil)

// =====================================================
// Batch slot accounting
// =====================================================

// acquireSlot takes a batch slot when fewer deploys hold one than the
// limit allows, and otherwise waits for a slot to be released or the limit
// to change, for its context to end, or for its timeout.
func (b *BatchDirector) acquireSlot(ctx context.Context) error {
	timer := time.NewTimer(b.timeout)
	defer timer.Stop()

	b.mu.Lock()
	b.queued++

	for b.inFlight >= b.limit {
		changed := b.changed
		b.mu.Unlock()

		select {
		case <-changed:
		case <-timer.C:
			b.leaveQueue()

			return fmt.Errorf("%w after %v", ErrBatchPoolTimeout, b.timeout)
		case <-ctx.Done():
			b.leaveQueue()

			return fmt.Errorf("context cancelled while waiting for batch slot: %w", ctx.Err())
		}

		b.mu.Lock()
	}

	b.queued--
	b.inFlight++
	active, queued := b.inFlight, b.queued
	b.mu.Unlock()

	b.logger.Debugf("Acquired batch slot (active: %d, queued: %d)", active, queued)

	return nil
}

// leaveQueue counts out a deploy that stopped waiting for a slot.
func (b *BatchDirector) leaveQueue() {
	b.mu.Lock()
	b.queued--
	b.mu.Unlock()
}

// releaseSlot releases a batch slot back to the pool.
func (b *BatchDirector) releaseSlot() {
	b.mu.Lock()
	b.inFlight--
	b.signalLocked()
	active, queued := b.inFlight, b.queued
	b.mu.Unlock()

	b.logger.Debugf("Released batch slot (active: %d, queued: %d)", active, queued)
}

// signalLocked wakes every deploy waiting for a slot. The caller holds mu.
func (b *BatchDirector) signalLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
}
