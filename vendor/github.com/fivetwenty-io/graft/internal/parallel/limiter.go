// Package parallel provides the worker pool, scheduler, rate limiters,
// and monitoring used to evaluate independent operators concurrently.
package parallel

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// RateLimiter defines the interface for rate limiting strategies.
type RateLimiter interface {
	// Wait blocks until a token is available or the context is canceled.
	Wait(ctx context.Context) error

	// TryAcquire attempts to acquire a token without blocking.
	// Returns true if a token was acquired, false otherwise.
	TryAcquire() bool

	// Stop stops the rate limiter and releases resources.
	Stop()
}

// TokenBucket implements a token bucket rate limiter.
// Tokens are added at a fixed rate up to a maximum burst size.
type TokenBucket struct {
	rate       float64       // tokens per second
	burst      int           // maximum bucket capacity
	tokens     float64       // current number of tokens
	lastUpdate time.Time     // last time tokens were updated
	mu         sync.Mutex    // protects tokens and lastUpdate
	stopCh     chan struct{} // signals shutdown
	stopped    atomic.Bool   // indicates if stopped
}

// NewTokenBucket creates a new token bucket rate limiter.
//
// Parameters:
//   - rate: tokens added per second
//   - burst: maximum number of tokens (bucket capacity)
//
// The bucket starts full (with burst tokens).
func NewTokenBucket(rate float64, burst int) *TokenBucket {
	if rate <= 0 {
		rate = 1.0
	}
	if burst <= 0 {
		burst = 1
	}

	return &TokenBucket{
		rate:       rate,
		burst:      burst,
		tokens:     float64(burst), // Start with full bucket
		lastUpdate: time.Now(),
		stopCh:     make(chan struct{}),
	}
}

// Wait blocks until a token is available or the context is canceled.
func (tb *TokenBucket) Wait(ctx context.Context) error {
	for {
		if tb.stopped.Load() {
			return context.Canceled
		}

		tb.mu.Lock()
		tb.refill()

		if tb.tokens >= 1.0 {
			tb.tokens--
			tb.mu.Unlock()
			return nil
		}

		// Calculate wait time for next token
		waitTime := time.Duration((1.0 - tb.tokens) / tb.rate * float64(time.Second))
		tb.mu.Unlock()

		// Wait with minimum granularity
		if waitTime < time.Millisecond {
			waitTime = time.Millisecond
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tb.stopCh:
			return context.Canceled
		case <-time.After(waitTime):
			// Try again
		}
	}
}

// TryAcquire attempts to acquire a token without blocking.
func (tb *TokenBucket) TryAcquire() bool {
	if tb.stopped.Load() {
		return false
	}

	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.refill()

	if tb.tokens >= 1.0 {
		tb.tokens--
		return true
	}

	return false
}

// Stop stops the rate limiter.
func (tb *TokenBucket) Stop() {
	if tb.stopped.CompareAndSwap(false, true) {
		close(tb.stopCh)
	}
}

// Rate returns the current rate (tokens per second).
func (tb *TokenBucket) Rate() float64 {
	return tb.rate
}

// Burst returns the burst capacity.
func (tb *TokenBucket) Burst() int {
	return tb.burst
}

// Available returns the current number of available tokens.
func (tb *TokenBucket) Available() float64 {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	tb.refill()
	return tb.tokens
}

// refill adds tokens based on elapsed time.
// Must be called with tb.mu held.
func (tb *TokenBucket) refill() {
	now := time.Now()
	elapsed := now.Sub(tb.lastUpdate).Seconds()
	tb.lastUpdate = now

	tb.tokens += elapsed * tb.rate
	if tb.tokens > float64(tb.burst) {
		tb.tokens = float64(tb.burst)
	}
}

// SlidingWindowLimiter implements a sliding window rate limiter.
// It tracks requests in a sliding time window for more accurate rate limiting.
type SlidingWindowLimiter struct {
	windowSize time.Duration
	limit      int
	requests   []time.Time
	mu         sync.Mutex
	stopCh     chan struct{}
	stopped    atomic.Bool
}

// NewSlidingWindowLimiter creates a new sliding window rate limiter.
//
// Parameters:
//   - limit: maximum number of requests in the window
//   - windowSize: size of the sliding window
func NewSlidingWindowLimiter(limit int, windowSize time.Duration) *SlidingWindowLimiter {
	if limit <= 0 {
		limit = 1
	}
	if windowSize <= 0 {
		windowSize = time.Second
	}

	return &SlidingWindowLimiter{
		windowSize: windowSize,
		limit:      limit,
		requests:   make([]time.Time, 0, limit),
		stopCh:     make(chan struct{}),
	}
}

// Wait blocks until a request is allowed or the context is canceled.
func (sw *SlidingWindowLimiter) Wait(ctx context.Context) error {
	for {
		if sw.stopped.Load() {
			return context.Canceled
		}

		sw.mu.Lock()
		sw.cleanup()

		if len(sw.requests) < sw.limit {
			sw.requests = append(sw.requests, time.Now())
			sw.mu.Unlock()
			return nil
		}

		// Calculate wait time
		oldest := sw.requests[0]
		waitTime := sw.windowSize - time.Since(oldest)
		sw.mu.Unlock()

		if waitTime < time.Millisecond {
			waitTime = time.Millisecond
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sw.stopCh:
			return context.Canceled
		case <-time.After(waitTime):
			// Try again
		}
	}
}

// TryAcquire attempts to make a request without blocking.
func (sw *SlidingWindowLimiter) TryAcquire() bool {
	if sw.stopped.Load() {
		return false
	}

	sw.mu.Lock()
	defer sw.mu.Unlock()

	sw.cleanup()

	if len(sw.requests) < sw.limit {
		sw.requests = append(sw.requests, time.Now())
		return true
	}

	return false
}

// Stop stops the rate limiter.
func (sw *SlidingWindowLimiter) Stop() {
	if sw.stopped.CompareAndSwap(false, true) {
		close(sw.stopCh)
	}
}

// cleanup removes requests outside the window.
// Must be called with sw.mu held.
func (sw *SlidingWindowLimiter) cleanup() {
	cutoff := time.Now().Add(-sw.windowSize)
	start := 0
	for i, t := range sw.requests {
		if t.After(cutoff) {
			start = i
			break
		}
		start = i + 1
	}
	if start > 0 {
		sw.requests = sw.requests[start:]
	}
}

// OperationLimiter provides per-operation rate limiting.
type OperationLimiter struct {
	limiters map[string]RateLimiter
	mu       sync.RWMutex
	defaults struct {
		rate  float64
		burst int
	}
}

// NewOperationLimiter creates a new operation limiter with default rate limits.
func NewOperationLimiter(defaultRate float64, defaultBurst int) *OperationLimiter {
	ol := &OperationLimiter{
		limiters: make(map[string]RateLimiter),
	}
	ol.defaults.rate = defaultRate
	ol.defaults.burst = defaultBurst
	return ol
}

// SetLimit sets a specific rate limit for an operation.
func (ol *OperationLimiter) SetLimit(operation string, rate float64, burst int) {
	ol.mu.Lock()
	defer ol.mu.Unlock()

	// Stop existing limiter if any
	if existing, exists := ol.limiters[operation]; exists {
		existing.Stop()
	}

	ol.limiters[operation] = NewTokenBucket(rate, burst)
}

// Wait waits for permission to perform the operation.
func (ol *OperationLimiter) Wait(ctx context.Context, operation string) error {
	limiter := ol.getLimiter(operation)
	return limiter.Wait(ctx)
}

// TryAcquire attempts to acquire permission without blocking.
func (ol *OperationLimiter) TryAcquire(operation string) bool {
	limiter := ol.getLimiter(operation)
	return limiter.TryAcquire()
}

// Stop stops all limiters.
func (ol *OperationLimiter) Stop() {
	ol.mu.Lock()
	defer ol.mu.Unlock()

	for _, limiter := range ol.limiters {
		limiter.Stop()
	}
}

// getLimiter returns the limiter for an operation, creating one if needed.
func (ol *OperationLimiter) getLimiter(operation string) RateLimiter {
	ol.mu.RLock()
	limiter, exists := ol.limiters[operation]
	ol.mu.RUnlock()

	if exists {
		return limiter
	}

	ol.mu.Lock()
	defer ol.mu.Unlock()

	// Double-check after acquiring write lock
	if limiter, exists = ol.limiters[operation]; exists {
		return limiter
	}

	limiter = NewTokenBucket(ol.defaults.rate, ol.defaults.burst)
	ol.limiters[operation] = limiter
	return limiter
}

// ConcurrencyLimiter limits the number of concurrent operations.
type ConcurrencyLimiter struct {
	sem     chan struct{}
	stopped atomic.Bool
}

// NewConcurrencyLimiter creates a new concurrency limiter.
func NewConcurrencyLimiter(maxConcurrent int) *ConcurrencyLimiter {
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}

	return &ConcurrencyLimiter{
		sem: make(chan struct{}, maxConcurrent),
	}
}

// Acquire acquires a slot, blocking until one is available.
func (cl *ConcurrencyLimiter) Acquire(ctx context.Context) error {
	if cl.stopped.Load() {
		return context.Canceled
	}

	select {
	case cl.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TryAcquire attempts to acquire a slot without blocking.
func (cl *ConcurrencyLimiter) TryAcquire() bool {
	if cl.stopped.Load() {
		return false
	}

	select {
	case cl.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

// Release releases a previously acquired slot.
func (cl *ConcurrencyLimiter) Release() {
	select {
	case <-cl.sem:
	default:
		// No slot to release
	}
}

// Wait implements RateLimiter interface (alias for Acquire).
func (cl *ConcurrencyLimiter) Wait(ctx context.Context) error {
	return cl.Acquire(ctx)
}

// Stop stops the limiter.
func (cl *ConcurrencyLimiter) Stop() {
	cl.stopped.Store(true)
}

// Available returns the number of available slots.
func (cl *ConcurrencyLimiter) Available() int {
	return cap(cl.sem) - len(cl.sem)
}

// InUse returns the number of slots currently in use.
func (cl *ConcurrencyLimiter) InUse() int {
	return len(cl.sem)
}
