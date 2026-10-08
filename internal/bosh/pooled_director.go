package bosh

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Static error variables to satisfy err113.
var (
	ErrConnectionPoolTimeout = errors.New("timeout waiting for BOSH connection slot")
)

// PooledDirector wraps a Director with connection pooling. A mutation
// that starts a director task holds its pool slot until the task ends,
// which a follower watches under its own lifetime context, never under
// the caller's. An errand and a cloud config write hold their slot for the
// call only.
type PooledDirector struct {
	director  Director
	semaphore chan struct{} // Connection pool semaphore
	timeout   time.Duration // Timeout for acquiring connection
	logger    Logger
	follow    *taskFollower

	// mu guards owed and stopped, and the follower shares it.
	mu sync.Mutex
	// owed counts the slots Adopt claimed past the limit, which the next
	// releases pay back before any frees a place in the semaphore.
	owed    int
	stopped bool

	// Metrics
	activeConnections int32
	totalRequests     int64
	queuedRequests    int32
	rejectedRequests  int64
	avgWaitTime       int64 // in nanoseconds
}

// NewPooledDirector creates a new pooled director wrapper.
func NewPooledDirector(director Director, maxConnections int, timeout time.Duration, logger Logger) *PooledDirector {
	if maxConnections <= 0 {
		maxConnections = 4 // Default
	}

	pool := &PooledDirector{
		director:          director,
		semaphore:         make(chan struct{}, maxConnections),
		timeout:           timeout,
		logger:            logger,
		activeConnections: 0,
		totalRequests:     0,
		queuedRequests:    0,
		rejectedRequests:  0,
		avgWaitTime:       0,
	}
	pool.follow = newTaskFollower(followerConfig{
		director: director, logger: logger, mu: &pool.mu, releaseSlot: pool.releaseConnection,
		owner: "pooled director", slot: "pool slot",
	})

	return pool
}

// Stop ends every follow and waits for its watcher to return. A follow
// ended by Stop frees its slot, and its waiters get context.Canceled with
// no final task. A mutation the director accepts after Stop is not
// followed, and its slot frees when the call returns.
func (p *PooledDirector) Stop() {
	p.mu.Lock()
	p.stopped = true
	p.follow.markStoppedLocked()
	p.mu.Unlock()

	p.follow.Stop()
}

// Adopt counts, in the pool, a task that was started before a restart, and
// follows it until it ends. It takes a slot without waiting, even past the
// limit, so new mutations wait until enough adopted tasks end. A task a
// running watcher follows is not counted twice, and nothing is adopted
// after Stop.
func (p *PooledDirector) Adopt(taskID int) {
	logger := p.follow.logger

	if taskID <= 0 {
		logger.Errorf("BOSH task ID %d names no task, so it is not adopted into the pool", taskID)

		return
	}

	p.mu.Lock()
	followed := p.follow.followsLocked(taskID)
	stopped := p.stopped

	if !followed && !stopped {
		select {
		case p.semaphore <- struct{}{}:
		default:
			p.owed++
		}

		atomic.AddInt32(&p.activeConnections, 1)
		p.follow.startTaskLocked(follow{kind: "adopted task"}, taskID)
	}

	p.mu.Unlock()

	switch {
	case followed:
		logger.Infof("The pooled director already follows BOSH task %d, so it is not adopted again", taskID)
	case stopped:
		logger.Infof("The pooled director is stopped, so BOSH task %d is not adopted", taskID)
	default:
		logger.Infof("Adopted BOSH task %d into the pool (active: %d, limit: %d)",
			taskID, atomic.LoadInt32(&p.activeConnections), cap(p.semaphore))
	}
}

// GetPoolStats returns current pool statistics.
func (p *PooledDirector) GetPoolStats() (*PoolStats, error) {
	stats := &PoolStats{
		ActiveConnections: atomic.LoadInt32(&p.activeConnections),
		QueuedRequests:    atomic.LoadInt32(&p.queuedRequests),
		TotalRequests:     atomic.LoadInt64(&p.totalRequests),
		RejectedRequests:  atomic.LoadInt64(&p.rejectedRequests),
		AvgWaitTime:       time.Duration(atomic.LoadInt64(&p.avgWaitTime)),
		MaxConnections:    cap(p.semaphore),
	}

	return stats, nil
}

// acquireConnection waits for an available connection slot.
func (p *PooledDirector) acquireConnection(ctx context.Context) error {
	atomic.AddInt32(&p.queuedRequests, 1)
	defer atomic.AddInt32(&p.queuedRequests, -1)

	start := time.Now()

	select {
	case p.semaphore <- struct{}{}:
		atomic.AddInt32(&p.activeConnections, 1)

		waitTime := time.Since(start)
		atomic.StoreInt64(&p.avgWaitTime, int64(waitTime))

		if p.logger != nil {
			p.logger.Debugf("Acquired BOSH connection slot (active: %d, queued: %d, wait: %v)",
				atomic.LoadInt32(&p.activeConnections),
				atomic.LoadInt32(&p.queuedRequests),
				waitTime)
		}

		return nil

	case <-time.After(p.timeout):
		atomic.AddInt64(&p.rejectedRequests, 1)

		return fmt.Errorf("%w after %v", ErrConnectionPoolTimeout, p.timeout)

	case <-ctx.Done():
		atomic.AddInt64(&p.rejectedRequests, 1)

		return fmt.Errorf("context cancelled while waiting for BOSH connection slot: %w", ctx.Err())
	}
}

// releaseConnection releases a connection slot back to the pool. A slot
// Adopt claimed past the limit is paid back first, so the semaphore frees
// a place only once the pool is back within its limit.
func (p *PooledDirector) releaseConnection() {
	p.mu.Lock()

	if p.owed > 0 {
		p.owed--
	} else {
		<-p.semaphore
	}

	atomic.AddInt32(&p.activeConnections, -1)
	p.mu.Unlock()

	if p.logger != nil {
		p.logger.Debugf("Released BOSH connection slot (active: %d, queued: %d)",
			atomic.LoadInt32(&p.activeConnections),
			atomic.LoadInt32(&p.queuedRequests))
	}
}

// withConnection executes a function with connection pooling.
func (p *PooledDirector) withConnection(ctx context.Context, callback func() error) error {
	atomic.AddInt64(&p.totalRequests, 1)

	err := p.acquireConnection(ctx)
	if err != nil {
		return err
	}
	defer p.releaseConnection()

	return callback()
}

// followMutation runs a mutation that starts a director task. It takes a
// slot under the caller's context, calls the base director, and hands the
// result to the follower, which keeps the slot until the task ends, or
// frees it at once when the error shows that no task started. A mutation
// whose answer was lost keeps its slot while the follow's lookup looks for
// its task.
func (p *PooledDirector) followMutation(ctx context.Context, call follow, run func(context.Context) (*Task, error)) (*Task, error) {
	atomic.AddInt64(&p.totalRequests, 1)

	err := p.acquireConnection(ctx)
	if err != nil {
		return nil, err
	}

	// handedOff is set, under mu, in the same step that starts the watcher
	// which owns the slot. Until then the deferred release returns the slot
	// however the call ends, a panic in the base director included.
	handedOff := false

	defer func() {
		if !handedOff {
			p.releaseConnection()
		}
	}()

	task, err := run(ctx)

	task, err = p.follow.settleCall(call, task, err, &handedOff)
	if err != nil {
		return nil, err
	}

	return task, nil
}

// withConnectionResult executes a function with connection pooling and returns a result.
func (p *PooledDirector) withConnectionResult(ctx context.Context, callback func() (interface{}, error)) (interface{}, error) {
	atomic.AddInt64(&p.totalRequests, 1)

	err := p.acquireConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer p.releaseConnection()

	return callback()
}
