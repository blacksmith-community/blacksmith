package bosh

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// Errors of the batch director.
var (
	ErrBatchPoolTimeout = errors.New("timeout waiting for batch connection slot")
	// ErrBatchDirectorStopped is returned to a deploy that asks for a slot
	// after Stop.
	ErrBatchDirectorStopped = errors.New("the batch director is stopped")
	// ErrWatchAbandoned is reported to the waiters of a watcher that gave
	// up on its task and released its slot, because the director no longer
	// knew the task, could not be read for batchWatchMax, or refused reads
	// for UnknownOutcomeGrace, or because no deploy ran on the deployment
	// of a lost answer that carried no context ID.
	ErrWatchAbandoned = errors.New("the batch director stopped following the BOSH task and released its slot")
	// ErrNoTaskWithContextID is reported to the waiters of a watcher whose
	// deploy lost its answer, when the director showed that no task has its
	// context ID.
	ErrNoTaskWithContextID = errors.New("the BOSH director lists no task with the context ID")
	// ErrNoWatcherForContextID is returned by WaitForContextID when no
	// watcher looks for the context ID and a lookup on the director found
	// no task with it. It says nothing about whether a deploy ran, because
	// the director may not have recorded the task yet, or may have pruned
	// it.
	ErrNoWatcherForContextID = errors.New("no batch watcher looks for the context ID, and the BOSH director found no task with it")
)

const (
	// defaultMaxBatchJobs is the limit a max_batch_jobs of zero or less
	// means.
	defaultMaxBatchJobs = 4
	// batchWatchMax is how long a watcher keeps reading a task, or looking
	// for the task of a lost answer, while every read fails. It counts from
	// the first failed read of a run of failures, and a read that succeeds
	// starts the count again, so a task that runs longer is still followed.
	batchWatchMax = 6 * time.Hour
	// batchLostAnswerInterval is how often a watcher looks for the task of
	// a deploy whose answer was lost.
	batchLostAnswerInterval = 30 * time.Second
	// batchWatchBackoffMin and batchWatchBackoffMax bound the wait before
	// a watcher reads its task again after a read failed.
	batchWatchBackoffMin = 5 * time.Second
	batchWatchBackoffMax = 2 * time.Minute
	// batchWatchBackoffFactor is how much longer each wait is than the
	// one before.
	batchWatchBackoffFactor = 2
	// batchDonePrune is how long a watcher keeps the final task of its
	// deploy for waiters after the task ends.
	batchDonePrune = 10 * time.Minute
	// batchFollowReportInterval is how often a watcher logs that it still
	// follows a task that holds a batch slot.
	batchFollowReportInterval = time.Hour
	// batchFinishedReadTimeout bounds the read of a task that had already
	// ended when UpdateDeployment returned it.
	batchFinishedReadTimeout = 30 * time.Second
)

// batchClock is the time source of the batch director's watchers.
type batchClock interface {
	Now() time.Time
	// Sleep waits for the duration given, and returns the context's error
	// when the context ends first.
	Sleep(ctx context.Context, duration time.Duration) error
}

// systemBatchClock reads the system clock.
type systemBatchClock struct{}

func (systemBatchClock) Now() time.Time {
	return time.Now()
}

func (systemBatchClock) Sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("sleep ended early: %w", ctx.Err())
	}
}

// batchWatch is the watcher of one deploy that holds a batch slot.
type batchWatch struct {
	// deployment is the deployment of the deploy, or empty for a task
	// adopted after a restart.
	deployment string
	// contextID is the context ID of a deploy whose answer was lost.
	contextID string
	// taskID is the task the watcher follows, or 0 while it looks for the
	// task of a lost answer. It is guarded by the BatchDirector's mu.
	taskID int
	// holdsSlot is false for the watcher of a task that had already ended
	// when UpdateDeployment returned it, whose slot was released at once.
	holdsSlot bool
	// done is closed once task and err hold the watcher's result.
	done chan struct{}
	task *Task
	err  error
}

// describe names the task of a watch for logs and errors.
func (w *batchWatch) describe(taskID int) string {
	if w.deployment == "" {
		return fmt.Sprintf("BOSH task %d", taskID)
	}

	return fmt.Sprintf("BOSH task %d of deployment %s", taskID, w.deployment)
}

// finished reports whether the watcher's follow has returned.
func (w *batchWatch) finished() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

// lostDeploy names the deploy of a watch whose answer was lost, for logs
// and errors.
func (w *batchWatch) lostDeploy() string {
	if w.contextID == "" {
		return fmt.Sprintf("the deploy of %s, which carries no context ID,", w.deployment)
	}

	return fmt.Sprintf("the deploy of %s with context ID %s", w.deployment, w.contextID)
}

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
// accepts a deploy, a watcher goroutine follows the task and releases the
// slot when the task ends, so the limit bounds the deploys running on the
// director and not only the calls that start them. Stop ends the watchers.
type BatchDirector struct {
	director Director      // Base director (not pooled)
	timeout  time.Duration // Timeout for acquiring slot
	logger   Logger
	clock    batchClock
	watchMax time.Duration

	// lifetime is the context the watchers run under, and stop cancels it.
	lifetime context.Context //nolint:containedctx // watchers outlive the calls that start them, and Stop ends them through it
	stop     context.CancelFunc
	watchers sync.WaitGroup

	mu       sync.Mutex
	inFlight int
	limit    int
	// waiters holds a ticket for each deploy waiting for a slot, in the
	// order the deploys asked, and nextTicket is the ticket of the next.
	waiters    []uint64
	nextTicket uint64
	changed    chan struct{}
	stopped    bool
	// running counts the watcher goroutines that have not returned.
	running int
	// byTask and byContextID hold each watcher until batchDonePrune after
	// its task ends, keyed by its task ID and by the context ID of a lost
	// answer.
	byTask      map[int]*batchWatch
	byContextID map[string]*batchWatch
}

// NewBatchDirector creates a new batch director wrapper.
func NewBatchDirector(director Director, maxBatchJobs int, timeout time.Duration, logger Logger) *BatchDirector {
	if maxBatchJobs <= 0 {
		maxBatchJobs = defaultMaxBatchJobs
	}

	if logger == nil {
		logger = &noOpLogger{}
	}

	lifetime, stop := context.WithCancel(context.Background())

	return &BatchDirector{
		director:    director,
		timeout:     timeout,
		logger:      logger,
		clock:       systemBatchClock{},
		watchMax:    batchWatchMax,
		lifetime:    lifetime,
		stop:        stop,
		limit:       maxBatchJobs,
		changed:     make(chan struct{}),
		byTask:      map[int]*batchWatch{},
		byContextID: map[string]*batchWatch{},
	}
}

// GetBatchPoolStats returns how many deploys hold a slot, how many wait
// for one, and the limit.
func (b *BatchDirector) GetBatchPoolStats() (int, int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.inFlight, len(b.waiters), b.limit
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

	watch := newBatchWatch("", "")

	b.mu.Lock()
	current := b.byTask[taskID]
	followed := current != nil && !current.finished()
	stopped := b.stopped

	if !followed && !stopped {
		b.inFlight++
		b.startWatchLocked(watch, taskID, func(ctx context.Context) (*Task, error) {
			return b.followTask(ctx, watch, taskID)
		})
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
	b.signalLocked()
	b.mu.Unlock()

	b.stop()
	b.watchers.Wait()
}

// UpdateDeployment uses the batch pool for rate limiting. The deploy holds
// its slot until its director task ends, which a watcher follows after the
// call returns. A deploy whose error shows that no task of it runs, because
// the director answered it with an error or never received it, releases its
// slot at once. Any other error keeps the slot while a watcher looks for
// the task, because the director may have started one.
func (b *BatchDirector) UpdateDeployment(ctx context.Context, name, manifest string) (*Task, error) {
	err := b.acquireSlot(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire batch slot: %w", err)
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
	if err != nil {
		var unknown *OutcomeUnknownError

		if errors.As(err, &unknown) {
			b.watchLostAnswer(name, unknown.ContextID, &handedOff)
		} else if reason, ok := noDeployTaskRuns(err); ok {
			b.logger.Infof("Released the batch slot of deployment %s at once, because %s: %v", name, reason, err)
		} else {
			b.logger.Infof("Kept the batch slot of deployment %s, because the deploy's error does not show that no deploy task runs: %v", name, err)

			b.watchLostAnswer(name, contextIDOf(err), &handedOff)
		}

		return task, fmt.Errorf("update deployment %s: %w", name, err)
	}

	if task == nil || task.ID <= 0 {
		b.logger.Errorf("Released the batch slot of deployment %s at once, because the director named no task for the deploy", name)

		return task, nil
	}

	if isTerminalTaskState(task.State) {
		handedOff = true

		b.releaseSlot()
		b.logger.Infof("BOSH task %d of deployment %s had ended in state %s when the deploy call returned, so its batch slot is released at once",
			task.ID, name, task.State)
		b.keepFinishedTask(name, *copyTask(task))

		return task, nil
	}

	b.watchTask(name, task.ID, &handedOff)

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
	b.mu.Lock()
	watch := b.byContextID[contextID]
	b.mu.Unlock()

	if watch != nil {
		return awaitWatch(ctx, watch, "the BOSH task with context ID "+contextID)
	}

	task, err := b.director.FindTaskByContextID(ctx, contextID)
	if err != nil {
		return nil, fmt.Errorf("find the task with context ID %s: %w", contextID, err)
	}

	if task == nil || task.ID <= 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoWatcherForContextID, contextID)
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
	b.mu.Lock()
	watch := b.byTask[id]
	b.mu.Unlock()

	if watch != nil {
		return awaitWatch(ctx, watch, fmt.Sprintf("BOSH task %d", id))
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

// =====================================================
// Watchers
// =====================================================

func newBatchWatch(deployment, contextID string) *batchWatch {
	return &batchWatch{deployment: deployment, contextID: contextID, holdsSlot: true, done: make(chan struct{})}
}

// keepFinishedTask keeps a task that had already ended when the deploy
// call returned it, so a wait for it gets the final task. It holds no
// slot. When the task lacks the deployment or the end time, it reads the
// task once, bounded by batchFinishedReadTimeout, and when that read
// fails, waiters get the task the deploy call returned.
func (b *BatchDirector) keepFinishedTask(deployment string, returned Task) {
	watch := newBatchWatch(deployment, "")
	watch.holdsSlot = false

	b.mu.Lock()
	b.startWatchLocked(watch, returned.ID, func(ctx context.Context) (*Task, error) {
		return b.readFinishedTask(ctx, watch, returned), nil
	})
	b.mu.Unlock()
}

// readFinishedTask returns the director's record of a finished task, or
// the task the deploy call returned when that record cannot be read.
func (b *BatchDirector) readFinishedTask(ctx context.Context, watch *batchWatch, returned Task) *Task {
	if returned.EndedAt != nil && returned.Deployment != "" {
		return &returned
	}

	readCtx, cancel := context.WithTimeout(ctx, batchFinishedReadTimeout)
	defer cancel()

	task, err := b.director.GetTask(readCtx, returned.ID)
	if err != nil || task == nil || task.ID != returned.ID || !isTerminalTaskState(task.State) {
		b.logger.Infof("The read of finished %s did not give its final record, so its waiters get the task the deploy call returned: %v",
			watch.describe(returned.ID), err)

		return &returned
	}

	return task
}

// watchTask follows the task of a deploy the director accepted, and
// releases the deploy's slot when the task ends. It sets handedOff, in the
// step that starts the watcher, when a watcher took the slot, which it does
// not once the batch director is stopped.
func (b *BatchDirector) watchTask(deployment string, taskID int, handedOff *bool) {
	watch := newBatchWatch(deployment, "")

	b.mu.Lock()
	started := b.startWatchLocked(watch, taskID, func(ctx context.Context) (*Task, error) {
		return b.followTask(ctx, watch, taskID)
	})
	*handedOff = started
	b.mu.Unlock()

	if !started {
		b.logger.Infof("The batch director is stopped, so it released the slot of %s without following it", watch.describe(taskID))
	}
}

// watchLostAnswer keeps the slot of a deploy whose answer was lost, and
// looks for its task until it finds it, or until the director shows that
// the deploy never became a task. It sets handedOff, in the step that
// starts the watcher, when a watcher took the slot, which it does not once
// the batch director is stopped.
func (b *BatchDirector) watchLostAnswer(deployment, contextID string, handedOff *bool) {
	watch := newBatchWatch(deployment, contextID)
	unknownSince := b.clock.Now()

	b.mu.Lock()
	started := b.startWatchLocked(watch, 0, func(ctx context.Context) (*Task, error) {
		return b.resolveLostAnswer(ctx, watch, unknownSince)
	})
	*handedOff = started
	b.mu.Unlock()

	if !started {
		b.logger.Infof("The batch director is stopped, so it released the slot of %s without looking for its task", watch.lostDeploy())

		return
	}

	b.logger.Infof("The batch slot of %s stays held while the batch director looks for its task", watch.lostDeploy())
}

// startWatchLocked registers a watch and runs it on its own goroutine,
// which releases the watch's slot, if it holds one, when the watch ends.
// It reports false, and starts nothing, once the batch director is
// stopped. The caller holds mu, and has counted a watch that holds a slot
// in inFlight.
func (b *BatchDirector) startWatchLocked(watch *batchWatch, taskID int, follow func(ctx context.Context) (*Task, error)) bool {
	if b.stopped {
		return false
	}

	if taskID > 0 {
		watch.taskID = taskID
		b.byTask[taskID] = watch
	}

	if watch.contextID != "" {
		b.byContextID[watch.contextID] = watch
	}

	b.running++
	b.watchers.Add(1)

	go b.runWatch(watch, follow)

	return true
}

// runWatch runs one watcher. A watcher that holds a slot releases it
// before it reports the result, so a waiter that starts the next deploy
// finds the slot free, and every watcher keeps its result for waiters
// until batchDonePrune has passed.
func (b *BatchDirector) runWatch(watch *batchWatch, follow func(ctx context.Context) (*Task, error)) {
	defer b.watchers.Done()
	defer b.watcherReturned()

	if watch.holdsSlot {
		watch.task, watch.err = b.followHoldingSlot(follow)
	} else {
		watch.task, watch.err = follow(b.lifetime)
	}

	close(watch.done)

	err := b.clock.Sleep(b.lifetime, batchDonePrune)
	if err != nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if watch.taskID > 0 && b.byTask[watch.taskID] == watch {
		delete(b.byTask, watch.taskID)
	}

	if watch.contextID != "" && b.byContextID[watch.contextID] == watch {
		delete(b.byContextID, watch.contextID)
	}
}

// followHoldingSlot runs a watcher's follow, and releases its slot however
// the follow ends.
func (b *BatchDirector) followHoldingSlot(follow func(ctx context.Context) (*Task, error)) (*Task, error) {
	defer b.releaseSlot()

	return follow(b.lifetime)
}

func (b *BatchDirector) watcherReturned() {
	b.mu.Lock()
	b.running--
	b.mu.Unlock()
}

// readFailures tracks a run of failed reads. since is when the first
// failed read of the run happened, and refusedSince is when the first of
// the latest run of reads the director refused happened. Each is zero when
// no such run is going on.
type readFailures struct {
	since        time.Time
	refusedSince time.Time
}

// succeeded ends the run of failed reads.
func (r *readFailures) succeeded() {
	r.since, r.refusedSince = time.Time{}, time.Time{}
}

// failed records a read that failed at now with err. It returns how long
// the run of failures has gone on and how long it may go on, and whether
// the director refused the read. A run of refused reads, which a retry
// cannot fix on its own, may go on for UnknownOutcomeGrace, and any other
// run for watchMax.
func (r *readFailures) failed(now time.Time, err error, watchMax time.Duration) (time.Duration, time.Duration, bool) {
	if r.since.IsZero() {
		r.since = now
	}

	if !boshapi.IsUnauthorized(err) && !boshapi.IsForbidden(err) {
		r.refusedSince = time.Time{}

		return now.Sub(r.since), watchMax, false
	}

	if r.refusedSince.IsZero() {
		r.refusedSince = now
	}

	return now.Sub(r.refusedSince), min(UnknownOutcomeGrace, watchMax), true
}

// followTask waits for the task to end and returns it. After a wait that
// fails, it reads the task with backoff until a read succeeds, and then
// waits again. It gives up once reads have failed for watchMax with no
// read that succeeded in between, or once the director has refused them
// for UnknownOutcomeGrace, and a task the director answers it has no
// record of ends the watch at once. It logs every hour that it still
// follows the task.
func (b *BatchDirector) followTask(ctx context.Context, watch *batchWatch, taskID int) (*Task, error) {
	stopReports := b.reportFollow(ctx, watch, taskID)
	defer stopReports()

	var failures readFailures

	for {
		task, err := b.director.WaitForTask(ctx, taskID)
		if err == nil && task != nil {
			b.logger.Infof("%s ended in state %s, so its batch slot is released", watch.describe(taskID), task.State)

			return task, nil
		}

		task, err = b.readUntilAnswered(ctx, watch, taskID, &failures, err)
		if err != nil || task != nil {
			return task, err
		}
	}
}

// reportFollow logs every batchFollowReportInterval that the watcher still
// follows the task, until the function it returns is called.
func (b *BatchDirector) reportFollow(ctx context.Context, watch *batchWatch, taskID int) func() {
	reportCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	started := b.clock.Now()

	go func() {
		defer close(done)

		for b.clock.Sleep(reportCtx, batchFollowReportInterval) == nil {
			b.logger.Infof("The batch director has followed %s for %v, and the task still holds a batch slot",
				watch.describe(taskID), b.clock.Now().Sub(started))
		}
	}()

	return func() {
		cancel()
		<-done
	}
}

// readUntilAnswered handles a wait for a task that failed with waitErr. It
// reads the task with backoff until a read succeeds. It returns the task
// when the read shows that it ended, nil with no error when it still runs,
// and an error when the watch ends.
func (b *BatchDirector) readUntilAnswered(ctx context.Context, watch *batchWatch, taskID int, failures *readFailures, waitErr error) (*Task, error) {
	backoff := batchWatchBackoffMin
	err := waitErr

	for {
		if err == nil {
			err = ErrEmptyDirectorAnswer
		}

		if ctx.Err() != nil {
			return nil, stoppedFollowing(watch.describe(taskID))
		}

		if isTaskNotFound(err) {
			b.logger.Errorf("The director has no %s, so the batch director released its slot: %v", watch.describe(taskID), err)

			return nil, fmt.Errorf("%w: the director has no %s: %w", ErrWatchAbandoned, watch.describe(taskID), err)
		}

		failing, limit, refused := failures.failed(b.clock.Now(), err, b.watchMax)
		if refused {
			b.logger.Errorf("The director refused the batch director's read of %s, which it retries for at most %v: %v", watch.describe(taskID), limit, err)
		}

		if failing >= limit {
			b.logger.Errorf("The batch director could not read %s for %v, so it released the slot of the deploy: %v", watch.describe(taskID), failing, err)

			return nil, fmt.Errorf("%w: %s could not be read for %v: %w", ErrWatchAbandoned, watch.describe(taskID), failing, err)
		}

		b.logger.Infof("Reading %s failed, so the batch director reads it again in %v: %v", watch.describe(taskID), backoff, err)

		if b.clock.Sleep(ctx, backoff) != nil {
			return nil, stoppedFollowing(watch.describe(taskID))
		}

		backoff = min(backoff*batchWatchBackoffFactor, batchWatchBackoffMax)

		var task *Task

		task, err = b.director.GetTask(ctx, taskID)
		if err == nil && task != nil {
			failures.succeeded()

			if isTerminalTaskState(task.State) {
				b.logger.Infof("%s ended in state %s, so its batch slot is released", watch.describe(taskID), task.State)

				return task, nil
			}

			return nil, nil //nolint:nilnil // a task that still runs is waited for again
		}
	}
}

// resolveLostAnswer looks for the task of a deploy whose answer was lost
// every batchLostAnswerInterval, and follows it once found. It gives up
// only after a lookup by context ID that succeeded, made at or after
// unknownSince plus UnknownOutcomeGrace, found no task, while no deploy
// task runs on the deployment. For a deploy that carries no context ID, a
// check made at or after that time that found no deploy running on the
// deployment is enough. It also gives up once lookups have failed
// for watchMax with no lookup that succeeded in between, or once the
// director has refused them for UnknownOutcomeGrace.
func (b *BatchDirector) resolveLostAnswer(ctx context.Context, watch *batchWatch, unknownSince time.Time) (*Task, error) {
	what := watch.lostDeploy()

	var failures readFailures

	for {
		now := b.clock.Now()
		taskID, err := b.findLostTask(ctx, watch)

		if ctx.Err() != nil {
			return nil, stoppedFollowing(what)
		}

		if taskID > 0 {
			b.followFoundTask(watch, taskID)

			return b.followTask(ctx, watch, taskID)
		}

		if err == nil && !now.Before(unknownSince.Add(UnknownOutcomeGrace)) && watch.contextID == "" {
			b.logger.Errorf("No deploy runs on deployment %s %v after the answer to %s was lost, so the batch director released its slot",
				watch.deployment, UnknownOutcomeGrace, what)

			return nil, fmt.Errorf("%w: no deploy runs on deployment %s, and its lost deploy carries no context ID", ErrWatchAbandoned, watch.deployment)
		}

		if err == nil && !now.Before(unknownSince.Add(UnknownOutcomeGrace)) {
			b.logger.Errorf("No task has the context ID of %s, and no deploy runs on the deployment, %v after its answer was lost, so the batch director released its slot",
				what, UnknownOutcomeGrace)

			return nil, fmt.Errorf("%w %s, and no deploy runs on deployment %s", ErrNoTaskWithContextID, watch.contextID, watch.deployment)
		}

		if err == nil {
			failures.succeeded()
		} else {
			failing, limit, refused := failures.failed(now, err, b.watchMax)
			if refused {
				b.logger.Errorf("The director refused the batch director's lookup of the task of %s, which it retries for at most %v: %v", what, limit, err)
			}

			if failing >= limit {
				b.logger.Errorf("The batch director could not look for the task of %s for %v, so it released the slot: %v", what, failing, err)

				return nil, fmt.Errorf("%w: the task of %s could not be looked up for %v: %w", ErrWatchAbandoned, what, failing, err)
			}

			b.logger.Infof("Looking for the task of %s failed, so the batch director looks again in %v: %v", what, batchLostAnswerInterval, err)
		}

		if b.clock.Sleep(ctx, batchLostAnswerInterval) != nil {
			return nil, stoppedFollowing(what)
		}
	}
}

// followFoundTask records the task a watcher of a lost answer found, so a
// wait for that task is served by the watcher. A task another watcher
// already follows keeps that watcher.
func (b *BatchDirector) followFoundTask(watch *batchWatch, taskID int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	watch.taskID = taskID

	if b.byTask[taskID] == nil {
		b.byTask[taskID] = watch
	}
}

// findLostTask looks for the task of a deploy whose answer was lost, by
// its context ID, when it carries one, and then as a deploy running on its
// deployment, behind any other task that runs there. It returns the task
// ID when either finds the task, and an error when either lookup failed.
func (b *BatchDirector) findLostTask(ctx context.Context, watch *batchWatch) (int, error) {
	var lookupErr error

	if watch.contextID != "" {
		task, err := b.director.FindTaskByContextID(ctx, watch.contextID)
		if err != nil {
			lookupErr = fmt.Errorf("the lookup of the task with context ID %s failed: %w", watch.contextID, err)
		} else if task != nil {
			b.logger.Infof("Found BOSH task %d by the context ID %s of the deploy of %s", task.ID, watch.contextID, watch.deployment)

			return task.ID, nil
		}
	}

	running, err := b.director.FindRunningTasksForDeployment(ctx, watch.deployment)
	if err != nil {
		return 0, errors.Join(lookupErr, fmt.Errorf("the check for a task running on deployment %s failed: %w", watch.deployment, err))
	}

	for _, task := range running {
		if task.Deployment == watch.deployment && task.Description == DeployTaskDescription {
			b.logger.Infof("BOSH task %d is a deploy running on deployment %s, so the batch director follows it for %s",
				task.ID, watch.deployment, watch.lostDeploy())

			return task.ID, nil
		}
	}

	for _, task := range running {
		b.logger.Infof("BOSH task %d (%q on deployment %q) runs on deployment %s, and it is not a %q task, so it is not followed",
			task.ID, task.Description, task.Deployment, watch.deployment, DeployTaskDescription)
	}

	return 0, lookupErr
}

// isTaskNotFound reports whether the director answered that it has no
// such task, with its task-not-found code. Any other not-found answer,
// such as a route the director does not serve or a director-not-found
// code from a proxy, is a failed read.
func isTaskNotFound(err error) bool {
	var apiErr *boshapi.APIError

	return errors.As(err, &apiErr) && apiErr.Code == boshapi.ErrorCodeTaskNotFound
}

// noDeployTaskRuns reports whether a deploy's error shows that no task of
// the deploy runs on the director, and says why. Only the director's own
// error answer, a request that never reached the director, and a failed
// read of the task after the deploy call returned show that. A gateway's
// error answer to the deploy does not, because the director may have
// accepted the request behind it.
func noDeployTaskRuns(err error) (string, bool) {
	if boshapi.IsOutcomeUnknown(err) {
		return "", false
	}

	var apiErr *boshapi.APIError
	if errors.As(err, &apiErr) {
		return "the director answered the deploy with an error", true
	}

	var transportErr *boshapi.TransportError
	if errors.As(err, &transportErr) && !transportErr.Sent {
		return "the deploy request never reached the director", true
	}

	// The base director returns ErrFailedGetTaskUpdate only after its
	// deploy call returned without an error, which the bosh-cli deploy call
	// does once its task has ended.
	if errors.Is(err, ErrFailedGetTaskUpdate) {
		return "the deploy call returned and only the read of its task failed", true
	}

	return "", false
}

// contextIDOf returns the context ID a deploy's error carries, or an empty
// string when it carries none.
func contextIDOf(err error) string {
	var transportErr *boshapi.TransportError
	if errors.As(err, &transportErr) && transportErr.ContextID != "" {
		return transportErr.ContextID
	}

	var answerErr *boshapi.TaskAnswerError
	if errors.As(err, &answerErr) && answerErr.ContextID != "" {
		return answerErr.ContextID
	}

	var redirectErr *boshapi.RedirectError
	if errors.As(err, &redirectErr) {
		return redirectErr.ContextID
	}

	return ""
}

// awaitWatch waits for a watcher's result or for the caller's context.
// Each waiter gets its own copy of the watcher's final task.
func awaitWatch(ctx context.Context, watch *batchWatch, what string) (*Task, error) {
	select {
	case <-watch.done:
		return copyTask(watch.task), watch.err
	case <-ctx.Done():
		return nil, fmt.Errorf("stopped waiting for %s, which the batch director still follows: %w", what, ctx.Err())
	}
}

// copyTask returns a copy of a task, its end time included, so a waiter
// that changes the task it got does not change what other waiters get.
func copyTask(task *Task) *Task {
	if task == nil {
		return nil
	}

	copied := *task

	if task.EndedAt != nil {
		ended := *task.EndedAt
		copied.EndedAt = &ended
	}

	return &copied
}

// stoppedFollowing is the result of a watcher that Stop ended.
func stoppedFollowing(what string) error {
	return fmt.Errorf("the batch director stopped and no longer follows %s: %w", what, context.Canceled)
}
