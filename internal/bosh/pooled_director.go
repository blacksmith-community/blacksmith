package bosh

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// Static error variables to satisfy err113.
var (
	ErrConnectionPoolTimeout = errors.New("timeout waiting for BOSH connection slot")
)

// PoolStats contains statistics about pool usage.
type PoolStats struct {
	ActiveConnections int32         `json:"active_connections"`
	QueuedRequests    int32         `json:"queued_requests"`
	TotalRequests     int64         `json:"total_requests"`
	RejectedRequests  int64         `json:"rejected_requests"`
	AvgWaitTime       time.Duration `json:"avg_wait_time"`
	MaxConnections    int           `json:"max_connections"`
}

// PooledDirector wraps a Director with connection pooling.
type PooledDirector struct {
	director  Director
	semaphore chan struct{} // Connection pool semaphore
	timeout   time.Duration // Timeout for acquiring connection
	logger    Logger

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

	return &PooledDirector{
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
}

// GetPoolStats returns current pool statistics.
func (p *PooledDirector) GetPoolStats() PoolStats {
	return PoolStats{
		ActiveConnections: atomic.LoadInt32(&p.activeConnections),
		QueuedRequests:    atomic.LoadInt32(&p.queuedRequests),
		TotalRequests:     atomic.LoadInt64(&p.totalRequests),
		RejectedRequests:  atomic.LoadInt64(&p.rejectedRequests),
		AvgWaitTime:       time.Duration(atomic.LoadInt64(&p.avgWaitTime)),
		MaxConnections:    cap(p.semaphore),
	}
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

// releaseConnection releases a connection slot back to the pool.
func (p *PooledDirector) releaseConnection() {
	<-p.semaphore
	atomic.AddInt32(&p.activeConnections, -1)

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
