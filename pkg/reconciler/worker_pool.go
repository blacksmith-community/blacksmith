package reconciler

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// WorkerPool manages a pool of workers for processing tasks
type WorkerPool struct {
	workers     int
	workFunc    WorkFunc
	taskQueue   chan Task
	resultQueue chan TaskResult
	wg          sync.WaitGroup
	ctx         context.Context
	cancel      context.CancelFunc
	metrics     *WorkerPoolMetrics
	limiter     *AdaptiveLimiter
}

// WorkFunc is the function that processes work items
type WorkFunc func(context.Context, WorkItem) WorkResult

// Task represents a unit of work with priority
type Task struct {
	WorkItem
	Priority  int
	Deadline  time.Time
	CreatedAt time.Time
}

// TaskResult represents the result of a task
type TaskResult struct {
	Task      Task
	Result    WorkResult
	StartedAt time.Time
	EndedAt   time.Time
}

// WorkerPoolMetrics tracks worker pool performance
type WorkerPoolMetrics struct {
	mu               sync.RWMutex
	tasksProcessed   uint64
	tasksFailed      uint64
	totalWaitTime    time.Duration
	totalProcessTime time.Duration
	queueDepth       int
	activeWorkers    int
}

// AdaptiveLimiter adjusts concurrency based on system performance
type AdaptiveLimiter struct {
	mu              sync.RWMutex
	currentLimit    int
	minLimit        int
	maxLimit        int
	targetLatency   time.Duration
	adjustInterval  time.Duration
	lastAdjustment  time.Time
	recentLatencies []time.Duration
}

// NewWorkerPool creates a new worker pool
func NewWorkerPool(workers int, workFunc WorkFunc) *WorkerPool {
	ctx, cancel := context.WithCancel(context.Background())

	return &WorkerPool{
		workers:     workers,
		workFunc:    workFunc,
		taskQueue:   make(chan Task, workers*10),
		resultQueue: make(chan TaskResult, workers*10),
		ctx:         ctx,
		cancel:      cancel,
		metrics:     &WorkerPoolMetrics{},
		limiter: &AdaptiveLimiter{
			currentLimit:   workers,
			minLimit:       1,
			maxLimit:       workers * 2,
			targetLatency:  5 * time.Second,
			adjustInterval: 30 * time.Second,
		},
	}
}

// Start starts the worker pool
func (wp *WorkerPool) Start(ctx context.Context) {
	wp.ctx = ctx

	// Start workers
	for i := 0; i < wp.workers; i++ {
		wp.wg.Add(1)
		go wp.worker(i)
	}

	// Start adaptive limiter
	wp.wg.Add(1)
	go wp.adaptiveLimiterLoop()

	// Start metrics collector
	wp.wg.Add(1)
	go wp.metricsCollector()
}

// Stop stops the worker pool gracefully
func (wp *WorkerPool) Stop() {
	wp.cancel()
	wp.wg.Wait()
	close(wp.taskQueue)
	close(wp.resultQueue)
}

// Submit submits a task to the worker pool
func (wp *WorkerPool) Submit(item WorkItem, priority int) error {
	task := Task{
		WorkItem:  item,
		Priority:  priority,
		CreatedAt: time.Now(),
		Deadline:  time.Now().Add(5 * time.Minute),
	}

	select {
	case wp.taskQueue <- task:
		wp.updateQueueDepth(1)
		return nil
	case <-time.After(time.Second):
		return fmt.Errorf("task queue full, cannot submit task %s", item.ID)
	}
}

// SubmitBatch submits a batch of tasks
func (wp *WorkerPool) SubmitBatch(items []WorkItem, priority int) error {
	for _, item := range items {
		if err := wp.Submit(item, priority); err != nil {
			return fmt.Errorf("failed to submit item %s: %w", item.ID, err)
		}
	}
	return nil
}

// worker is the main worker loop
func (wp *WorkerPool) worker(id int) {
	defer wp.wg.Done()

	for {
		select {
		case task, ok := <-wp.taskQueue:
			if !ok {
				return
			}

			wp.updateActiveWorkers(1)
			wp.processTask(task, id)
			wp.updateActiveWorkers(-1)

		case <-wp.ctx.Done():
			return
		}
	}
}

// processTask processes a single task
func (wp *WorkerPool) processTask(task Task, workerID int) {
	startTime := time.Now()
	waitTime := startTime.Sub(task.CreatedAt)

	// Check if task has expired
	if time.Now().After(task.Deadline) {
		wp.resultQueue <- TaskResult{
			Task: task,
			Result: WorkResult{
				Item:    task.WorkItem,
				Success: false,
				Error:   fmt.Errorf("task expired after waiting %v", waitTime),
			},
			StartedAt: startTime,
			EndedAt:   time.Now(),
		}
		wp.updateMetrics(false, waitTime, 0)
		return
	}

	// Process the task
	result := wp.workFunc(wp.ctx, task.WorkItem)
	endTime := time.Now()
	processTime := endTime.Sub(startTime)

	// Send result
	wp.resultQueue <- TaskResult{
		Task:      task,
		Result:    result,
		StartedAt: startTime,
		EndedAt:   endTime,
	}

	// Update metrics
	wp.updateMetrics(result.Success, waitTime, processTime)
	wp.limiter.recordLatency(processTime)
}

// adaptiveLimiterLoop adjusts concurrency based on performance
func (wp *WorkerPool) adaptiveLimiterLoop() {
	defer wp.wg.Done()

	ticker := time.NewTicker(wp.limiter.adjustInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			wp.adjustConcurrency()
		case <-wp.ctx.Done():
			return
		}
	}
}

// adjustConcurrency adjusts the worker pool size based on performance
func (wp *WorkerPool) adjustConcurrency() {
	avgLatency := wp.limiter.getAverageLatency()
	currentLimit := wp.limiter.getCurrentLimit()

	if avgLatency > wp.limiter.targetLatency && currentLimit > wp.limiter.minLimit {
		// Reduce concurrency if latency is too high
		newLimit := currentLimit - 1
		wp.limiter.setCurrentLimit(newLimit)
		wp.scaleWorkers(newLimit)
	} else if avgLatency < time.Duration(float64(wp.limiter.targetLatency)*0.5) && currentLimit < wp.limiter.maxLimit {
		// Increase concurrency if latency is low
		newLimit := currentLimit + 1
		wp.limiter.setCurrentLimit(newLimit)
		wp.scaleWorkers(newLimit)
	}
}

// scaleWorkers adjusts the number of active workers
func (wp *WorkerPool) scaleWorkers(newCount int) {
	currentCount := wp.workers

	if newCount > currentCount {
		// Add workers
		for i := currentCount; i < newCount; i++ {
			wp.wg.Add(1)
			go wp.worker(i)
		}
	}
	// Note: Reducing workers is handled by workers exiting naturally

	wp.workers = newCount
}

// metricsCollector collects and reports metrics
func (wp *WorkerPool) metricsCollector() {
	defer wp.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			wp.reportMetrics()
		case <-wp.ctx.Done():
			return
		}
	}
}

// reportMetrics reports current metrics
func (wp *WorkerPool) reportMetrics() {
	wp.metrics.mu.RLock()
	defer wp.metrics.mu.RUnlock()

	// Calculate averages for potential future use
	if wp.metrics.tasksProcessed > 0 {
		// Safe conversion from uint64 to int64 for time.Duration
		// If tasksProcessed is larger than max int64, cap it
		processedCount := int64(wp.metrics.tasksProcessed) // #nosec G115
		if processedCount < 0 {
			// Overflow occurred, use max safe value
			processedCount = int64(^uint64(0) >> 1)
		}
		_ = wp.metrics.totalWaitTime / time.Duration(processedCount)    // #nosec G115
		_ = wp.metrics.totalProcessTime / time.Duration(processedCount) // #nosec G115
	}

	// Metrics are collected - could log or export to Prometheus
	// For now, just update the metrics which can be retrieved via GetMetrics()
}

// GetMetrics returns current metrics
func (wp *WorkerPool) GetMetrics() WorkerPoolMetrics {
	wp.metrics.mu.RLock()
	defer wp.metrics.mu.RUnlock()

	// Create a copy without the mutex
	return WorkerPoolMetrics{
		tasksProcessed:   wp.metrics.tasksProcessed,
		tasksFailed:      wp.metrics.tasksFailed,
		totalWaitTime:    wp.metrics.totalWaitTime,
		totalProcessTime: wp.metrics.totalProcessTime,
		queueDepth:       wp.metrics.queueDepth,
		activeWorkers:    wp.metrics.activeWorkers,
	}
}

// updateMetrics updates worker pool metrics
func (wp *WorkerPool) updateMetrics(success bool, waitTime, processTime time.Duration) {
	wp.metrics.mu.Lock()
	defer wp.metrics.mu.Unlock()

	wp.metrics.tasksProcessed++
	if !success {
		wp.metrics.tasksFailed++
	}
	wp.metrics.totalWaitTime += waitTime
	wp.metrics.totalProcessTime += processTime
}

// updateQueueDepth updates the queue depth metric
func (wp *WorkerPool) updateQueueDepth(delta int) {
	wp.metrics.mu.Lock()
	defer wp.metrics.mu.Unlock()
	wp.metrics.queueDepth += delta
}

// updateActiveWorkers updates the active workers count
func (wp *WorkerPool) updateActiveWorkers(delta int) {
	wp.metrics.mu.Lock()
	defer wp.metrics.mu.Unlock()
	wp.metrics.activeWorkers += delta
}

// AdaptiveLimiter methods

func (al *AdaptiveLimiter) recordLatency(latency time.Duration) {
	al.mu.Lock()
	defer al.mu.Unlock()

	al.recentLatencies = append(al.recentLatencies, latency)

	// Keep only recent latencies (last 100)
	if len(al.recentLatencies) > 100 {
		al.recentLatencies = al.recentLatencies[1:]
	}
}

func (al *AdaptiveLimiter) getAverageLatency() time.Duration {
	al.mu.RLock()
	defer al.mu.RUnlock()

	if len(al.recentLatencies) == 0 {
		return 0
	}

	var total time.Duration
	for _, l := range al.recentLatencies {
		total += l
	}

	return total / time.Duration(len(al.recentLatencies))
}

func (al *AdaptiveLimiter) getCurrentLimit() int {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.currentLimit
}

func (al *AdaptiveLimiter) setCurrentLimit(limit int) {
	al.mu.Lock()
	defer al.mu.Unlock()

	if limit < al.minLimit {
		limit = al.minLimit
	}
	if limit > al.maxLimit {
		limit = al.maxLimit
	}

	al.currentLimit = limit
	al.lastAdjustment = time.Now()
}
