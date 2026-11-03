package parallel

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Common errors returned by the pool.
var (
	ErrPoolShutdown   = errors.New("worker pool is shut down")
	ErrPoolFull       = errors.New("task queue is full")
	ErrTaskCanceled   = errors.New("task was canceled")
	ErrTaskTimeout    = errors.New("task timed out")
	ErrInvalidWorkers = errors.New("invalid worker count configuration")
)

// TaskFunc represents a task to be executed.
type TaskFunc func(ctx context.Context) error

// PoolConfig configures the worker pool.
type PoolConfig struct {
	// MinWorkers is the minimum number of workers to maintain
	MinWorkers int

	// MaxWorkers is the maximum number of workers allowed
	MaxWorkers int

	// QueueSize is the capacity of the task queue
	QueueSize int

	// IdleTimeout is how long a worker waits before exiting if idle
	IdleTimeout time.Duration

	// ScaleInterval is how often to check for scaling
	ScaleInterval time.Duration

	// EnableAutoScale enables automatic worker scaling
	EnableAutoScale bool

	// RateLimiter optionally limits task execution rate
	RateLimiter RateLimiter
}

// DefaultPoolConfig returns a default configuration.
func DefaultPoolConfig() *PoolConfig {
	return &PoolConfig{
		MinWorkers:      2,
		MaxWorkers:      runtime.NumCPU() * 2,
		QueueSize:       1000,
		IdleTimeout:     30 * time.Second,
		ScaleInterval:   100 * time.Millisecond,
		EnableAutoScale: true,
	}
}

// WorkerPool is an adaptive worker pool that scales based on load.
type WorkerPool struct {
	config *PoolConfig

	// Task queue
	taskQueue chan *poolTask

	// Worker management
	activeWorkers atomic.Int32
	workerMu      sync.Mutex
	workerWg      sync.WaitGroup

	// Lifecycle
	ctx       context.Context
	cancel    context.CancelFunc
	shutdown  atomic.Bool
	closeOnce sync.Once
	doneCh    chan struct{}

	// Statistics
	stats poolStats

	// Monitoring
	monitor *Monitor
}

// poolTask wraps a task with result channel.
type poolTask struct {
	fn     TaskFunc
	ctx    context.Context
	result chan error
}

// poolStats tracks pool statistics.
type poolStats struct {
	submitted atomic.Uint64
	completed atomic.Uint64
	failed    atomic.Uint64
	rejected  atomic.Uint64
	totalWait atomic.Int64 // nanoseconds
	totalExec atomic.Int64 // nanoseconds
}

// NewPool creates a new worker pool with the given configuration.
func NewPool(minWorkers, maxWorkers int) (*WorkerPool, error) {
	config := DefaultPoolConfig()
	config.MinWorkers = minWorkers
	config.MaxWorkers = maxWorkers
	return NewPoolWithConfig(config)
}

// NewPoolWithConfig creates a new worker pool with full configuration.
func NewPoolWithConfig(config *PoolConfig) (*WorkerPool, error) {
	if config == nil {
		config = DefaultPoolConfig()
	}

	if config.MinWorkers < 0 {
		config.MinWorkers = 0
	}
	if config.MaxWorkers <= 0 {
		config.MaxWorkers = 1
	}
	if config.MinWorkers > config.MaxWorkers {
		return nil, ErrInvalidWorkers
	}
	if config.QueueSize <= 0 {
		config.QueueSize = 100
	}

	ctx, cancel := context.WithCancel(context.Background())

	wp := &WorkerPool{
		config:    config,
		taskQueue: make(chan *poolTask, config.QueueSize),
		ctx:       ctx,
		cancel:    cancel,
		doneCh:    make(chan struct{}),
	}

	// Start minimum workers
	for i := 0; i < config.MinWorkers; i++ {
		wp.startWorker()
	}

	// Start auto-scaler if enabled
	if config.EnableAutoScale {
		go wp.autoScaler()
	}

	return wp, nil
}

// Submit submits a task to the pool without waiting for completion.
func (wp *WorkerPool) Submit(task TaskFunc) error {
	return wp.SubmitContext(wp.ctx, task)
}

// SubmitContext submits a task with a specific context.
func (wp *WorkerPool) SubmitContext(ctx context.Context, task TaskFunc) error {
	if wp.shutdown.Load() {
		return ErrPoolShutdown
	}

	if task == nil {
		return nil
	}

	pt := &poolTask{
		fn:     task,
		ctx:    ctx,
		result: nil, // No result channel needed for Submit
	}

	select {
	case wp.taskQueue <- pt:
		wp.stats.submitted.Add(1)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-wp.ctx.Done():
		return ErrPoolShutdown
	default:
		wp.stats.rejected.Add(1)
		return ErrPoolFull
	}
}

// SubmitBlocking submits a task, blocking until a queue slot is available
// (or ctx/the pool is done), but returns as soon as the task is enqueued -
// it does not wait for the task to execute. Use this instead of
// SubmitContext when many tasks are being fanned out concurrently and the
// caller wants backpressure on submission (bounded queue growth) without
// serializing on each task's completion the way SubmitWaitContext would:
// SubmitContext's non-blocking select turns a full queue into an
// immediate ErrPoolFull, which is wrong for a producer that is only
// trying to enqueue faster than the workers can drain, not asking whether
// the pool has capacity right now.
func (wp *WorkerPool) SubmitBlocking(ctx context.Context, task TaskFunc) error {
	if wp.shutdown.Load() {
		return ErrPoolShutdown
	}

	if task == nil {
		return nil
	}

	pt := &poolTask{
		fn:     task,
		ctx:    ctx,
		result: nil, // fire-and-forget once enqueued, like SubmitContext
	}

	select {
	case wp.taskQueue <- pt:
		wp.stats.submitted.Add(1)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-wp.ctx.Done():
		return ErrPoolShutdown
	}
}

// SubmitWait submits a task and waits for its completion.
func (wp *WorkerPool) SubmitWait(task TaskFunc) error {
	return wp.SubmitWaitContext(wp.ctx, task)
}

// SubmitWaitContext submits a task with context and waits for completion.
func (wp *WorkerPool) SubmitWaitContext(ctx context.Context, task TaskFunc) error {
	if wp.shutdown.Load() {
		return ErrPoolShutdown
	}

	if task == nil {
		return nil
	}

	pt := &poolTask{
		fn:     task,
		ctx:    ctx,
		result: make(chan error, 1),
	}

	submitTime := time.Now()

	select {
	case wp.taskQueue <- pt:
		wp.stats.submitted.Add(1)
	case <-ctx.Done():
		return ctx.Err()
	case <-wp.ctx.Done():
		return ErrPoolShutdown
	}

	// Wait for result
	select {
	case err := <-pt.result:
		wp.stats.totalWait.Add(time.Since(submitTime).Nanoseconds())
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-wp.ctx.Done():
		return ErrPoolShutdown
	}
}

// SubmitBatch submits multiple tasks and waits for all to complete.
// Returns errors for each task (nil if successful).
func (wp *WorkerPool) SubmitBatch(tasks []TaskFunc) []error {
	return wp.SubmitBatchContext(wp.ctx, tasks)
}

// SubmitBatchContext submits multiple tasks with context.
func (wp *WorkerPool) SubmitBatchContext(ctx context.Context, tasks []TaskFunc) []error {
	if len(tasks) == 0 {
		return nil
	}

	results := make([]error, len(tasks))
	var wg sync.WaitGroup

	for i, task := range tasks {
		if task == nil {
			continue
		}

		wg.Add(1)
		idx := i
		t := task

		go func() {
			defer wg.Done()
			results[idx] = wp.SubmitWaitContext(ctx, t)
		}()
	}

	wg.Wait()
	return results
}

// Shutdown initiates graceful shutdown without waiting.
func (wp *WorkerPool) Shutdown() {
	wp.closeOnce.Do(func() {
		wp.shutdown.Store(true)
		close(wp.taskQueue)
	})
}

// ShutdownWait initiates graceful shutdown and waits for completion.
func (wp *WorkerPool) ShutdownWait() {
	wp.Shutdown()
	wp.workerWg.Wait()
	wp.cancel()
	close(wp.doneCh)
}

// ShutdownWaitTimeout waits for shutdown with timeout.
func (wp *WorkerPool) ShutdownWaitTimeout(timeout time.Duration) error {
	wp.Shutdown()

	done := make(chan struct{})
	go func() {
		wp.workerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		wp.cancel()
		close(wp.doneCh)
		return nil
	case <-time.After(timeout):
		wp.cancel()
		return fmt.Errorf("shutdown timed out after %v", timeout)
	}
}

// Done returns a channel that's closed when the pool is fully shut down.
func (wp *WorkerPool) Done() <-chan struct{} {
	return wp.doneCh
}

// Canceled returns a channel that's closed the instant the pool's
// internal context is canceled (ShutdownWait/ShutdownWaitTimeout),
// regardless of whether the graceful worker drain has finished - unlike
// Done, which only closes once every worker has actually exited. A
// worker abandons its current select the moment this fires and does not
// drain any task still sitting in taskQueue, so a caller with its own
// WaitGroup tracking tasks submitted via SubmitBlocking should select on
// this alongside that WaitGroup to avoid blocking forever on tasks a
// canceled pool will never run.
func (wp *WorkerPool) Canceled() <-chan struct{} {
	return wp.ctx.Done()
}

// IsShutdown returns true if the pool is shutting down.
func (wp *WorkerPool) IsShutdown() bool {
	return wp.shutdown.Load()
}

// startWorker starts a new worker goroutine.
func (wp *WorkerPool) startWorker() {
	wp.workerMu.Lock()
	current := int(wp.activeWorkers.Load())
	if current >= wp.config.MaxWorkers {
		wp.workerMu.Unlock()
		return
	}
	wp.activeWorkers.Add(1)
	wp.workerWg.Add(1)
	wp.workerMu.Unlock()

	go wp.worker()
}

// worker is the main worker loop.
func (wp *WorkerPool) worker() {
	defer func() {
		wp.activeWorkers.Add(-1)
		wp.workerWg.Done()
	}()

	idleTimer := time.NewTimer(wp.config.IdleTimeout)
	defer idleTimer.Stop()

	for {
		// Reset idle timer
		if !idleTimer.Stop() {
			select {
			case <-idleTimer.C:
			default:
			}
		}
		idleTimer.Reset(wp.config.IdleTimeout)

		select {
		case task, ok := <-wp.taskQueue:
			if !ok {
				return // Channel closed, shutdown
			}

			wp.executeTask(task)

		case <-idleTimer.C:
			// Check if we can exit (more than minimum workers)
			wp.workerMu.Lock()
			current := int(wp.activeWorkers.Load())
			if current > wp.config.MinWorkers {
				wp.workerMu.Unlock()
				return // Exit this worker
			}
			wp.workerMu.Unlock()

		case <-wp.ctx.Done():
			return
		}
	}
}

// executeTask executes a single task.
func (wp *WorkerPool) executeTask(task *poolTask) {
	// Apply rate limiting if configured
	if wp.config.RateLimiter != nil {
		if err := wp.config.RateLimiter.Wait(task.ctx); err != nil {
			wp.sendResult(task, err)
			return
		}
	}

	startTime := time.Now()
	err := task.fn(task.ctx)
	execTime := time.Since(startTime)

	wp.stats.totalExec.Add(execTime.Nanoseconds())

	if err != nil {
		wp.stats.failed.Add(1)
	} else {
		wp.stats.completed.Add(1)
	}

	wp.sendResult(task, err)
}

// sendResult sends the result if a result channel exists.
func (wp *WorkerPool) sendResult(task *poolTask, err error) {
	if task.result != nil {
		select {
		case task.result <- err:
		default:
			// Result channel full or closed
		}
	}
}

// autoScaler adjusts worker count based on load.
func (wp *WorkerPool) autoScaler() {
	ticker := time.NewTicker(wp.config.ScaleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			wp.adjustWorkers()
		case <-wp.ctx.Done():
			return
		}
	}
}

// adjustWorkers scales workers based on queue depth.
func (wp *WorkerPool) adjustWorkers() {
	queueLen := len(wp.taskQueue)
	queueCap := cap(wp.taskQueue)
	currentWorkers := int(wp.activeWorkers.Load())

	// Calculate load ratio
	loadRatio := float64(queueLen) / float64(queueCap)

	// Scale up if queue is filling up
	if loadRatio > 0.7 && currentWorkers < wp.config.MaxWorkers {
		// Add workers proportionally to load
		toAdd := int((loadRatio - 0.7) * 10)
		if toAdd < 1 {
			toAdd = 1
		}
		for i := 0; i < toAdd && currentWorkers+i < wp.config.MaxWorkers; i++ {
			wp.startWorker()
		}
	}
}

// Stats returns current pool statistics.
func (wp *WorkerPool) Stats() PoolStats {
	submitted := wp.stats.submitted.Load()
	completed := wp.stats.completed.Load()
	failed := wp.stats.failed.Load()

	var avgWait, avgExec time.Duration
	if completed > 0 && completed <= uint64(math.MaxInt64) {
		avgWait = time.Duration(wp.stats.totalWait.Load() / int64(completed))
		avgExec = time.Duration(wp.stats.totalExec.Load() / int64(completed))
	}

	return PoolStats{
		ActiveWorkers:  int(wp.activeWorkers.Load()),
		MinWorkers:     wp.config.MinWorkers,
		MaxWorkers:     wp.config.MaxWorkers,
		PendingTasks:   len(wp.taskQueue),
		QueueCapacity:  cap(wp.taskQueue),
		TasksSubmitted: submitted,
		TasksCompleted: completed,
		TasksFailed:    failed,
		TasksRejected:  wp.stats.rejected.Load(),
		AvgWaitTime:    avgWait,
		AvgExecuteTime: avgExec,
	}
}

// PoolStats contains pool statistics.
type PoolStats struct {
	ActiveWorkers  int
	MinWorkers     int
	MaxWorkers     int
	PendingTasks   int
	QueueCapacity  int
	TasksSubmitted uint64
	TasksCompleted uint64
	TasksFailed    uint64
	TasksRejected  uint64
	AvgWaitTime    time.Duration
	AvgExecuteTime time.Duration
}

// ActiveWorkers implements PoolMetrics interface.
func (wp *WorkerPool) ActiveWorkers() int {
	return int(wp.activeWorkers.Load())
}

// PendingTasks implements PoolMetrics interface.
func (wp *WorkerPool) PendingTasks() int {
	return len(wp.taskQueue)
}

// QueueCapacity implements PoolMetrics interface.
func (wp *WorkerPool) QueueCapacity() int {
	return cap(wp.taskQueue)
}

// SetMonitor attaches a monitor to the pool.
func (wp *WorkerPool) SetMonitor(m *Monitor) {
	wp.monitor = m
	if m != nil {
		m.ConnectPool(wp)
	}
}
