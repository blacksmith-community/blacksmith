package bosh

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

// Errors of the batch director.
var (
	ErrBatchPoolTimeout = errors.New("timeout waiting for batch connection slot")
	// ErrBatchDirectorStopped is returned to a deploy that asks for a slot
	// after Stop.
	ErrBatchDirectorStopped = errors.New("the batch director is stopped")
	// ErrNoWatcherForContextID is returned by WaitForContextID when no
	// watcher looks for the context ID and a lookup on the director found
	// no task with it. It says nothing about whether a deploy ran, because
	// the director may not have recorded the task yet, or may have pruned
	// it.
	ErrNoWatcherForContextID = errors.New("no batch watcher looks for the context ID, and the BOSH director found no task with it")
)

// defaultMaxBatchJobs is the limit a batch director built with a
// max_batch_jobs of zero or less has.
const defaultMaxBatchJobs = 4

// BatchDirector wraps a Director with a separate connection pool for batch operations.
// This allows batch upgrade jobs to use their own pool without competing with
// normal API operations for connection slots.
//
// The slots are counted under one mutex. inFlight is how many deploys hold a
// slot, limit is how many may, and waiters holds the deploys waiting for a
// slot in the order they asked. Only the first waiter may take a free slot,
// so a deploy that asks later never takes a slot ahead of one that waits.
// changed is closed and replaced whenever a slot is released, the limit
// changes, or the first waiter changes, so the waiting deploys look again.
//
// A deploy holds its slot until its director task ends. When the director
// accepts a deploy, the batch director's task follower runs a watcher that
// follows the task and releases the slot when the task ends, so the limit
// bounds the deploys running on the director and not only the calls that
// start them. The follower shares the batch director's mutex. Stop ends the
// watchers.
type BatchDirector struct {
	director Director      // Base director (not pooled)
	timeout  time.Duration // Timeout for acquiring slot
	logger   Logger
	follow   *taskFollower

	// configured is the limit the batch director was built with, which a
	// max_batch_jobs of zero or less restores.
	configured int

	mu       sync.Mutex
	inFlight int
	limit    int
	// waiters holds a ticket for each deploy waiting for a slot, in the
	// order the deploys asked, and nextTicket is the ticket of the next.
	waiters    []uint64
	nextTicket uint64
	changed    chan struct{}
	stopped    bool
}

// NewBatchDirector creates a new batch director wrapper.
func NewBatchDirector(director Director, maxBatchJobs int, timeout time.Duration, logger Logger) *BatchDirector {
	if maxBatchJobs <= 0 {
		maxBatchJobs = defaultMaxBatchJobs
	}

	if logger == nil {
		logger = &noOpLogger{}
	}

	batch := &BatchDirector{
		director:   director,
		timeout:    timeout,
		logger:     logger,
		configured: maxBatchJobs,
		limit:      maxBatchJobs,
		changed:    make(chan struct{}),
	}

	batch.follow = newTaskFollower(followerConfig{
		director:    director,
		logger:      logger,
		mu:          &batch.mu,
		releaseSlot: batch.releaseSlot,
		owner:       "batch director",
		slot:        "batch slot",
	})

	return batch
}

// deployFollow is the follow of a deploy of the deployment. A later
// deploy of the deployment may take over the slot of a deploy whose answer
// was lost.
func deployFollow(name string) follow {
	return follow{kind: kindDeploy, deployment: name, supersedable: true, lookup: contextIDLookup{match: deployMatch(name)}}
}

// GetBatchPoolStats returns how many deploys hold a slot, how many wait
// for one, and the limit.
func (b *BatchDirector) GetBatchPoolStats() (int, int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.inFlight, len(b.waiters), b.limit
}

// UpdateMaxBatchJobs changes the maximum number of concurrent batch jobs,
// and a value of zero or less restores the limit the batch director was
// built with. Deploys that already hold a slot stay counted against the
// new limit, so a smaller limit admits no new deploy until enough of them
// finish.
func (b *BatchDirector) UpdateMaxBatchJobs(maxBatchJobs int) {
	if maxBatchJobs <= 0 {
		maxBatchJobs = b.configured
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

// Adopt counts, in the batch, a deploy task that was started before a
// restart, and follows it like a deploy the batch director started. It
// takes a slot without waiting, even past the limit, so new deploys wait
// until enough adopted ones finish. A task a running watcher follows is not
// counted twice, a task whose watcher has finished is adopted again, and
// nothing is adopted after Stop.
func (b *BatchDirector) Adopt(taskID int) {
	if taskID <= 0 {
		b.logger.Errorf("BOSH task ID %d names no task, so it is not adopted into the batch", taskID)

		return
	}

	b.mu.Lock()
	followed := b.follow.followsLocked(taskID)
	stopped := b.stopped

	if !followed && !stopped {
		b.inFlight++
		b.follow.startTaskLocked(follow{kind: kindDeploy}, taskID)
	}

	active, limit := b.inFlight, b.limit
	b.mu.Unlock()

	switch {
	case followed:
		b.logger.Infof("The batch director already follows BOSH task %d, so it is not adopted again", taskID)
	case stopped:
		b.logger.Infof("The batch director is stopped, so BOSH task %d is not adopted", taskID)
	default:
		b.logger.Infof("Adopted BOSH task %d into the batch (active: %d, limit: %d)", taskID, active, limit)
	}
}

// Stop ends every watcher and waits for them to return. A watcher ended
// by Stop releases its slot and reports context.Canceled, with no final
// task, to its waiters. Deploys that wait for a slot, and any later ones,
// get ErrBatchDirectorStopped.
func (b *BatchDirector) Stop() {
	b.mu.Lock()
	b.stopped = true
	b.follow.markStoppedLocked()
	b.signalLocked()
	b.mu.Unlock()

	b.follow.Stop()
}

// UpdateDeployment uses the batch pool for rate limiting. The deploy holds
// its slot until its director task ends, which a watcher follows after the
// call returns. A deploy whose error shows that no task of it runs, because
// the director answered it with an error or never received it, releases its
// slot at once. Any other error keeps the slot while a watcher looks for
// the task, because the director may have started one.
//
// A deploy of a deployment whose earlier deploy lost its answer ends that
// deploy's watcher first, and takes over the slot the watcher held, with no
// change to the count of slots held. The director takes a lock for each
// deployment, so once it accepts this deploy the lost one is not running.
// The deploy does not wait in the queue for the slot, because it is the
// slot of its own deployment, and no deploy queued for another deployment
// can take it in between.
func (b *BatchDirector) UpdateDeployment(ctx context.Context, name, manifest string) (*Task, error) {
	if !b.follow.supersedeLostAnswer(name) {
		err := b.acquireSlot(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to acquire batch slot: %w", err)
		}
	}

	// handedOff is set, under mu, in the same step that starts the watcher
	// which owns the slot, before anything else can panic. Until then the
	// deferred release returns the slot however the call ends, a panic in
	// the base director included.
	handedOff := false

	defer func() {
		if !handedOff {
			b.releaseSlot()
		}
	}()

	task, err := b.director.UpdateDeployment(ctx, name, manifest)

	task, err = b.follow.settleCall(deployFollow(name), task, err, &handedOff)
	if err != nil {
		return task, fmt.Errorf("update deployment %s: %w", name, err)
	}

	return task, nil
}

// WaitForContextID waits for the task of a deploy whose answer was lost,
// once the batch director's watcher has found and followed it, or for the
// caller's context. It returns ErrNoTaskWithContextID when that watcher
// showed that no task has the context ID.
//
// With no watcher for the context ID, it looks the task up once on the
// base director. A task it finds is waited for through WaitForTask, so a
// watcher that follows the task serves the wait. When the lookup finds no
// task, it returns ErrNoWatcherForContextID, never ErrNoTaskWithContextID,
// because a single lookup does not show that no deploy ran.
func (b *BatchDirector) WaitForContextID(ctx context.Context, contextID string) (*Task, error) {
	return b.WaitForContextIDReporting(ctx, contextID, nil)
}

// WaitForContextIDReporting is WaitForContextID that also reports the
// task's ID to found as soon as the task is known, before the task ends:
// once the watcher has found the task, or once the lookup with no watcher
// has found it. found runs on the caller's goroutine, at most once, and is
// not called for a task already known to have ended. A nil found is never
// called.
func (b *BatchDirector) WaitForContextIDReporting(ctx context.Context, contextID string, found func(taskID int)) (*Task, error) {
	followed, watched, err := b.follow.awaitContextIDReporting(ctx, contextID, found)
	if watched {
		return followed, err
	}

	task, err := b.director.FindTaskByContextID(ctx, contextID)
	if err != nil {
		return nil, fmt.Errorf("find the task with context ID %s: %w", contextID, err)
	}

	if task == nil || task.ID <= 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoWatcherForContextID, contextID)
	}

	if found != nil && !isTerminalTaskState(task.State) {
		found(task.ID)
	}

	waited, err := b.WaitForTask(ctx, task.ID)
	if err != nil {
		return nil, fmt.Errorf("context ID %s: %w", contextID, err)
	}

	return waited, nil
}

// FindRunningTaskForDeployment passes through (read operation).
func (b *BatchDirector) FindRunningTaskForDeployment(ctx context.Context, deploymentName string) (*Task, error) {
	return b.director.FindRunningTaskForDeployment(ctx, deploymentName)
}

// FindRunningTasksForDeployment passes through (read operation).
func (b *BatchDirector) FindRunningTasksForDeployment(ctx context.Context, deploymentName string) ([]Task, error) {
	tasks, err := b.director.FindRunningTasksForDeployment(ctx, deploymentName)
	if err != nil {
		return nil, fmt.Errorf("list the running tasks of deployment %s: %w", deploymentName, err)
	}

	return tasks, nil
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

// WaitForTask waits for the watcher that follows the task to end, or for
// the caller's context, and returns the watcher's final task. A task no
// watcher follows is waited for on the base director, as a series of task
// reads that holds no slot.
func (b *BatchDirector) WaitForTask(ctx context.Context, id int) (*Task, error) {
	followed, watched, err := b.follow.awaitTask(ctx, id)
	if watched {
		return followed, err
	}

	task, err := b.director.WaitForTask(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("wait for BOSH task %d: %w", id, err)
	}

	return task, nil
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

// acquireSlot joins the end of the queue of deploys waiting for a batch
// slot, and takes a slot once it is first in the queue and fewer deploys
// hold one than the limit allows. Until then it waits for the slots, the
// limit, or the queue to change, for its context to end, or for its
// timeout.
func (b *BatchDirector) acquireSlot(ctx context.Context) error {
	timer := time.NewTimer(b.timeout)
	defer timer.Stop()

	b.mu.Lock()
	ticket := b.nextTicket
	b.nextTicket++
	b.waiters = append(b.waiters, ticket)

	for b.stopped || b.inFlight >= b.limit || b.waiters[0] != ticket {
		if b.stopped {
			b.leaveQueueLocked(ticket)
			b.mu.Unlock()

			return ErrBatchDirectorStopped
		}

		changed := b.changed
		b.mu.Unlock()

		select {
		case <-changed:
		case <-timer.C:
			b.leaveQueue(ticket)

			return fmt.Errorf("%w after %v", ErrBatchPoolTimeout, b.timeout)
		case <-ctx.Done():
			b.leaveQueue(ticket)

			return fmt.Errorf("context cancelled while waiting for batch slot: %w", ctx.Err())
		}

		b.mu.Lock()
	}

	b.takeHeadLocked()
	active, queued := b.inFlight, len(b.waiters)
	b.mu.Unlock()

	b.logger.Debugf("Acquired batch slot (active: %d, queued: %d)", active, queued)

	return nil
}

// takeHeadLocked gives a slot to the waiter at the head of the queue. The
// next waiter is first in the queue then, and is woken, because it may
// take a slot the limit still allows. The caller holds mu.
func (b *BatchDirector) takeHeadLocked() {
	b.waiters = b.waiters[1:]
	b.inFlight++

	if len(b.waiters) > 0 {
		b.signalLocked()
	}
}

// leaveQueue takes a deploy that stopped waiting for a slot out of the
// queue.
func (b *BatchDirector) leaveQueue(ticket uint64) {
	b.mu.Lock()
	b.leaveQueueLocked(ticket)
	b.mu.Unlock()
}

// leaveQueueLocked takes a waiter out of the queue, and wakes the others,
// because the waiter after it may now be first. The caller holds mu.
func (b *BatchDirector) leaveQueueLocked(ticket uint64) {
	b.waiters = slices.DeleteFunc(b.waiters, func(waiting uint64) bool { return waiting == ticket })
	b.signalLocked()
}

// releaseSlot releases a batch slot back to the pool.
func (b *BatchDirector) releaseSlot() {
	b.mu.Lock()
	b.inFlight--
	b.signalLocked()
	active, queued := b.inFlight, len(b.waiters)
	b.mu.Unlock()

	b.logger.Debugf("Released batch slot (active: %d, queued: %d)", active, queued)
}

// signalLocked wakes every deploy waiting for a slot. The caller holds mu.
func (b *BatchDirector) signalLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
}
