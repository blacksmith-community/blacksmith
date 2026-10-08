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
	// knew the task or could not be read for batchWatchMax.
	ErrWatchAbandoned = errors.New("the batch director stopped following the BOSH task and released its slot")
	// ErrNoTaskWithContextID is reported to the waiters of a watcher whose
	// deploy lost its answer, when the director showed that no task has its
	// context ID.
	ErrNoTaskWithContextID = errors.New("the BOSH director lists no task with the context ID")
)

const (
	// defaultMaxBatchJobs is the limit a max_batch_jobs of zero or less
	// means.
	defaultMaxBatchJobs = 4
	// batchWatchMax is how long a watcher keeps reading a task the director
	// does not answer for. It is longer than any deploy has run.
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
	// batchCreateDeploymentDescription is the director's description of a
	// deploy, which a watcher of a lost answer follows when it runs on the
	// deployment.
	batchCreateDeploymentDescription = "create deployment"
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
// until enough adopted ones finish. A task already followed is not counted
// twice, and nothing is adopted after Stop.
func (b *BatchDirector) Adopt(taskID int) {
	if taskID <= 0 {
		b.logger.Errorf("BOSH task ID %d names no task, so it is not adopted into the batch", taskID)

		return
	}

	watch := newBatchWatch("", "")
	deadline := b.clock.Now().Add(b.watchMax)

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.byTask[taskID] != nil {
		b.logger.Infof("BOSH task %d already holds a batch slot, so it is not adopted again", taskID)

		return
	}

	if b.stopped {
		b.logger.Infof("The batch director is stopped, so BOSH task %d is not adopted", taskID)

		return
	}

	b.inFlight++
	b.startWatchLocked(watch, taskID, func(ctx context.Context) (*Task, error) {
		return b.followTask(ctx, watch, taskID, deadline)
	})

	b.logger.Infof("Adopted BOSH task %d into the batch (active: %d, limit: %d)", taskID, b.inFlight, b.limit)
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
// call returns. A deploy the director rejected, or never received, releases
// its slot at once, and a deploy whose answer was lost keeps it while a
// watcher looks for its task.
func (b *BatchDirector) UpdateDeployment(ctx context.Context, name, manifest string) (*Task, error) {
	err := b.acquireSlot(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire batch slot: %w", err)
	}

	// handedOff is set once a watcher owns the slot. Until then the
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
			handedOff = b.watchLostAnswer(name, unknown.ContextID)
		} else {
			b.logger.Infof("Released the batch slot of deployment %s at once, because the deploy failed without a task to follow: %v", name, err)
		}

		return task, fmt.Errorf("update deployment %s: %w", name, err)
	}

	if task == nil || task.ID <= 0 {
		b.logger.Errorf("Released the batch slot of deployment %s at once, because the director named no task for the deploy", name)

		return task, nil
	}

	handedOff = b.watchTask(name, task.ID)

	return task, nil
}

// WaitForContextID waits for the task of a deploy whose answer was lost,
// once the batch director's watcher has found and followed it, or for the
// caller's context. It returns ErrNoTaskWithContextID when the director
// showed that no task has the context ID. With no watcher for the context
// ID, it looks the task up once on the base director and waits for it
// there.
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

	if task == nil {
		return nil, fmt.Errorf("%w %s", ErrNoTaskWithContextID, contextID)
	}

	waited, err := b.director.WaitForTask(ctx, task.ID)
	if err != nil {
		return nil, fmt.Errorf("wait for BOSH task %d with context ID %s: %w", task.ID, contextID, err)
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

	b.waiters = b.waiters[1:]
	b.inFlight++

	// The next waiter is first in the queue now, and may take a slot the
	// limit still allows.
	if len(b.waiters) > 0 {
		b.signalLocked()
	}

	active, queued := b.inFlight, len(b.waiters)
	b.mu.Unlock()

	b.logger.Debugf("Acquired batch slot (active: %d, queued: %d)", active, queued)

	return nil
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
	return &batchWatch{deployment: deployment, contextID: contextID, done: make(chan struct{})}
}

// watchTask follows the task of a deploy the director accepted, and
// releases the deploy's slot when the task ends. It reports whether a
// watcher took the slot, which it does not once the batch director is
// stopped.
func (b *BatchDirector) watchTask(deployment string, taskID int) bool {
	watch := newBatchWatch(deployment, "")
	deadline := b.clock.Now().Add(b.watchMax)

	b.mu.Lock()
	started := b.startWatchLocked(watch, taskID, func(ctx context.Context) (*Task, error) {
		return b.followTask(ctx, watch, taskID, deadline)
	})
	b.mu.Unlock()

	if !started {
		b.logger.Infof("The batch director is stopped, so it released the slot of %s without following it", watch.describe(taskID))
	}

	return started
}

// watchLostAnswer keeps the slot of a deploy whose answer was lost, and
// looks for its task until it finds it, or until the director shows that
// the deploy never became a task. It reports whether a watcher took the
// slot, which it does not once the batch director is stopped.
func (b *BatchDirector) watchLostAnswer(deployment, contextID string) bool {
	watch := newBatchWatch(deployment, contextID)
	unknownSince := b.clock.Now()
	deadline := unknownSince.Add(b.watchMax)

	b.mu.Lock()
	started := b.startWatchLocked(watch, 0, func(ctx context.Context) (*Task, error) {
		return b.resolveLostAnswer(ctx, watch, unknownSince, deadline)
	})
	b.mu.Unlock()

	if !started {
		b.logger.Infof("The batch director is stopped, so it released the slot of deployment %s, whose answer with context ID %s was lost, without looking for its task",
			deployment, contextID)

		return false
	}

	b.logger.Infof("The answer to the deploy of %s with context ID %s was lost, so its batch slot stays held while the batch director looks for its task",
		deployment, contextID)

	return true
}

// startWatchLocked registers a watch and runs it on its own goroutine,
// which releases the watch's slot when the watch ends. It reports false,
// and starts nothing, once the batch director is stopped. The caller holds
// mu, and has counted the watch's slot in inFlight.
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

// runWatch runs one watcher. It releases the slot before it reports the
// result, so a waiter that starts the next deploy finds the slot free, and
// it keeps the result for waiters until batchDonePrune has passed.
func (b *BatchDirector) runWatch(watch *batchWatch, follow func(ctx context.Context) (*Task, error)) {
	defer b.watchers.Done()
	defer b.watcherReturned()

	watch.task, watch.err = b.followHoldingSlot(follow)
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

// followTask waits for the task to end and returns it. A failed read is
// retried with backoff until the deadline has passed, and a task the
// director does not know ends the watch at once.
func (b *BatchDirector) followTask(ctx context.Context, watch *batchWatch, taskID int, deadline time.Time) (*Task, error) {
	backoff := batchWatchBackoffMin

	for {
		task, err := b.director.WaitForTask(ctx, taskID)
		if err == nil && task != nil {
			b.logger.Infof("%s ended in state %s, so its batch slot is released", watch.describe(taskID), task.State)

			return task, nil
		}

		if err == nil {
			err = ErrEmptyDirectorAnswer
		}

		if ctx.Err() != nil {
			return nil, stoppedFollowing(watch.describe(taskID))
		}

		if boshapi.IsNotFound(err) {
			b.logger.Errorf("The director has no %s, so the batch director released its slot: %v", watch.describe(taskID), err)

			return nil, fmt.Errorf("%w: the director has no %s: %w", ErrWatchAbandoned, watch.describe(taskID), err)
		}

		if !b.clock.Now().Before(deadline) {
			b.logger.Errorf("The batch director could not read %s for %v, so it released the slot of the deploy: %v", watch.describe(taskID), b.watchMax, err)

			return nil, fmt.Errorf("%w: %s could not be read for %v: %w", ErrWatchAbandoned, watch.describe(taskID), b.watchMax, err)
		}

		b.logger.Infof("Reading %s failed, so the batch director reads it again in %v: %v", watch.describe(taskID), backoff, err)

		if b.clock.Sleep(ctx, backoff) != nil {
			return nil, stoppedFollowing(watch.describe(taskID))
		}

		backoff = min(backoff*batchWatchBackoffFactor, batchWatchBackoffMax)
	}
}

// resolveLostAnswer looks for the task of a deploy whose answer was lost
// every batchLostAnswerInterval, and follows it once found. It gives up
// only after a lookup by context ID that succeeded, made at or after
// unknownSince plus UnknownOutcomeGrace, found no task, while no deploy
// task runs on the deployment.
func (b *BatchDirector) resolveLostAnswer(ctx context.Context, watch *batchWatch, unknownSince, deadline time.Time) (*Task, error) {
	what := fmt.Sprintf("the deploy of %s with context ID %s", watch.deployment, watch.contextID)

	for {
		now := b.clock.Now()
		taskID, err := b.findLostTask(ctx, watch)

		if ctx.Err() != nil {
			return nil, stoppedFollowing(what)
		}

		if taskID > 0 {
			b.followFoundTask(watch, taskID)

			return b.followTask(ctx, watch, taskID, deadline)
		}

		if err == nil && !now.Before(unknownSince.Add(UnknownOutcomeGrace)) {
			b.logger.Errorf("No task has the context ID of %s, and no deploy runs on the deployment, %v after its answer was lost, so the batch director released its slot",
				what, UnknownOutcomeGrace)

			return nil, fmt.Errorf("%w %s, and no deploy runs on deployment %s", ErrNoTaskWithContextID, watch.contextID, watch.deployment)
		}

		if err != nil {
			if !now.Before(deadline) {
				b.logger.Errorf("The batch director could not look for the task of %s for %v, so it released the slot: %v", what, b.watchMax, err)

				return nil, fmt.Errorf("%w: the task of %s could not be looked up for %v: %w", ErrWatchAbandoned, what, b.watchMax, err)
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
// its context ID and then as a deploy running on its deployment. It
// returns the task ID when either finds the task, and an error when either
// lookup failed.
func (b *BatchDirector) findLostTask(ctx context.Context, watch *batchWatch) (int, error) {
	var lookupErr error

	if watch.contextID == "" {
		lookupErr = ErrEmptyContextID
	} else {
		task, err := b.director.FindTaskByContextID(ctx, watch.contextID)
		if err != nil {
			lookupErr = fmt.Errorf("the lookup of the task with context ID %s failed: %w", watch.contextID, err)
		} else if task != nil {
			b.logger.Infof("Found BOSH task %d by the context ID %s of the deploy of %s", task.ID, watch.contextID, watch.deployment)

			return task.ID, nil
		}
	}

	running, err := b.director.FindRunningTaskForDeployment(ctx, watch.deployment)
	if err != nil {
		return 0, errors.Join(lookupErr, fmt.Errorf("the check for a task running on deployment %s failed: %w", watch.deployment, err))
	}

	if running != nil {
		if running.Deployment == watch.deployment && running.Description == batchCreateDeploymentDescription {
			b.logger.Infof("BOSH task %d is a deploy running on deployment %s, so the batch director follows it for the deploy with context ID %s",
				running.ID, watch.deployment, watch.contextID)

			return running.ID, nil
		}

		b.logger.Infof("BOSH task %d (%q on deployment %q) runs on deployment %s, and it is not a %q task, so it is not followed",
			running.ID, running.Description, running.Deployment, watch.deployment, batchCreateDeploymentDescription)
	}

	return 0, lookupErr
}

// awaitWatch waits for a watcher's result or for the caller's context.
func awaitWatch(ctx context.Context, watch *batchWatch, what string) (*Task, error) {
	select {
	case <-watch.done:
		return watch.task, watch.err
	case <-ctx.Done():
		return nil, fmt.Errorf("stopped waiting for %s, which the batch director still follows: %w", what, ctx.Err())
	}
}

// stoppedFollowing is the result of a watcher that Stop ended.
func stoppedFollowing(what string) error {
	return fmt.Errorf("the batch director stopped and no longer follows %s: %w", what, context.Canceled)
}
