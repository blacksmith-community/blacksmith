package reconciler

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/sony/gobreaker"
)

// RetryManager handles retry logic with exponential backoff
type RetryManager struct {
	config  RetryConfig
	metrics *RetryMetrics
	logger  Logger
}

// RetryMetrics tracks retry performance
type RetryMetrics struct {
	totalRetries      uint64
	successfulRetries uint64
	failedRetries     uint64
	totalRetryTime    time.Duration
}

// RetryableFunc is a function that can be retried
type RetryableFunc func(context.Context) (interface{}, error)

// RetryResult contains the result of a retry operation
type RetryResult struct {
	Value       interface{}
	Error       error
	Attempts    int
	TotalDelay  time.Duration
	LastAttempt time.Time
}

// NewRetryManager creates a new retry manager
func NewRetryManager(config RetryConfig, logger Logger) *RetryManager {
	// Set defaults if not configured
	if config.MaxAttempts == 0 {
		config.MaxAttempts = 3
	}
	if config.InitialDelay == 0 {
		config.InitialDelay = 1 * time.Second
	}
	if config.MaxDelay == 0 {
		config.MaxDelay = 30 * time.Second
	}
	if config.Multiplier == 0 {
		config.Multiplier = 2.0
	}

	return &RetryManager{
		config:  config,
		metrics: &RetryMetrics{},
		logger:  logger,
	}
}

// Execute executes a function with retry logic
func (rm *RetryManager) Execute(ctx context.Context, fn RetryableFunc) RetryResult {
	return rm.ExecuteWithName(ctx, "operation", fn)
}

// ExecuteWithName executes a function with retry logic and a descriptive name
func (rm *RetryManager) ExecuteWithName(ctx context.Context, name string, fn RetryableFunc) RetryResult {
	result := RetryResult{
		Attempts: 0,
	}

	for attempt := 0; attempt < rm.config.MaxAttempts; attempt++ {
		result.Attempts = attempt + 1
		result.LastAttempt = time.Now()

		// Execute the function
		value, err := fn(ctx)

		if err == nil {
			// Success
			result.Value = value
			if attempt > 0 {
				rm.recordSuccess()
			}
			return result
		}

		// Check if error is retryable
		if !rm.isRetryableError(err) {
			result.Error = fmt.Errorf("non-retryable error: %w", err)
			return result
		}

		// Check if this was the last attempt
		if attempt == rm.config.MaxAttempts-1 {
			result.Error = fmt.Errorf("failed after %d attempts: %w", rm.config.MaxAttempts, err)
			rm.recordFailure()
			return result
		}

		// Calculate delay for next attempt
		delay := rm.calculateDelay(attempt)
		result.TotalDelay += delay

		if rm.logger != nil {
			rm.logger.Debug("Retry %s: attempt %d/%d failed with %v, retrying after %v",
				name, attempt+1, rm.config.MaxAttempts, err, delay)
		}

		// Wait before retry
		select {
		case <-time.After(delay):
			// Continue to next attempt
		case <-ctx.Done():
			result.Error = fmt.Errorf("context cancelled during retry: %w", ctx.Err())
			return result
		}

		rm.recordRetry(delay)
	}

	return result
}

// ExecuteWithBackoff executes a function with custom backoff strategy
func (rm *RetryManager) ExecuteWithBackoff(
	ctx context.Context,
	fn RetryableFunc,
	backoffStrategy BackoffStrategy,
) RetryResult {
	result := RetryResult{
		Attempts: 0,
	}

	for attempt := 0; attempt < rm.config.MaxAttempts; attempt++ {
		result.Attempts = attempt + 1
		result.LastAttempt = time.Now()

		// Execute the function
		value, err := fn(ctx)

		if err == nil {
			result.Value = value
			return result
		}

		// Check if this was the last attempt
		if attempt == rm.config.MaxAttempts-1 {
			result.Error = fmt.Errorf("failed after %d attempts: %w", rm.config.MaxAttempts, err)
			return result
		}

		// Get delay from strategy
		delay := backoffStrategy.NextDelay(attempt)
		result.TotalDelay += delay

		// Wait before retry
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			result.Error = fmt.Errorf("context cancelled: %w", ctx.Err())
			return result
		}
	}

	return result
}

// calculateDelay calculates the delay for the next retry attempt
func (rm *RetryManager) calculateDelay(attempt int) time.Duration {
	// Exponential backoff: delay = initialDelay * multiplier^attempt
	delay := float64(rm.config.InitialDelay) * math.Pow(rm.config.Multiplier, float64(attempt))

	// Add jitter if configured
	if rm.config.Jitter > 0 {
		jitter := rm.calculateJitter(time.Duration(delay))
		delay += float64(jitter)
	}

	// Cap at max delay
	if time.Duration(delay) > rm.config.MaxDelay {
		return rm.config.MaxDelay
	}

	return time.Duration(delay)
}

// calculateJitter adds randomized jitter to prevent thundering herd
func (rm *RetryManager) calculateJitter(baseDelay time.Duration) time.Duration {
	if rm.config.Jitter <= 0 {
		return 0
	}

	// Calculate jitter range
	maxJitter := float64(baseDelay) * rm.config.Jitter

	// Generate cryptographically secure random jitter
	jitterNanos, err := rand.Int(rand.Reader, big.NewInt(int64(maxJitter)))
	if err != nil {
		// Fallback to no jitter on error
		return 0
	}

	// Return jitter that can be positive or negative
	if randomBool() {
		return time.Duration(jitterNanos.Int64())
	}
	return -time.Duration(jitterNanos.Int64())
}

// isRetryableError determines if an error should trigger a retry
func (rm *RetryManager) isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()

	// Check against configured retryable error patterns
	for _, pattern := range rm.config.RetryableErrors {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	// Default retryable errors
	retryablePatterns := []string{
		"timeout",
		"temporary",
		"connection refused",
		"connection reset",
		"no such host",
		"network is unreachable",
		"too many requests",
		"rate limit",
		"service unavailable",
		"gateway timeout",
		"bad gateway",
	}

	for _, pattern := range retryablePatterns {
		if strings.Contains(strings.ToLower(errStr), pattern) {
			return true
		}
	}

	return false
}

// recordRetry records retry metrics
func (rm *RetryManager) recordRetry(delay time.Duration) {
	rm.metrics.totalRetries++
	rm.metrics.totalRetryTime += delay
}

// recordSuccess records successful retry
func (rm *RetryManager) recordSuccess() {
	rm.metrics.successfulRetries++
}

// recordFailure records failed retry
func (rm *RetryManager) recordFailure() {
	rm.metrics.failedRetries++
}

// GetMetrics returns retry metrics
func (rm *RetryManager) GetMetrics() RetryMetrics {
	return *rm.metrics
}

// BackoffStrategy defines a backoff strategy interface
type BackoffStrategy interface {
	NextDelay(attempt int) time.Duration
	Reset()
}

// ExponentialBackoff implements exponential backoff strategy
type ExponentialBackoff struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Multiplier   float64
	Jitter       float64
}

// NextDelay returns the next delay for exponential backoff
func (eb *ExponentialBackoff) NextDelay(attempt int) time.Duration {
	delay := float64(eb.InitialDelay) * math.Pow(eb.Multiplier, float64(attempt))

	if eb.Jitter > 0 {
		maxJitter := delay * eb.Jitter
		jitterNanos, _ := rand.Int(rand.Reader, big.NewInt(int64(maxJitter)))
		delay += float64(jitterNanos.Int64()) - maxJitter/2
	}

	if time.Duration(delay) > eb.MaxDelay {
		return eb.MaxDelay
	}

	return time.Duration(delay)
}

// Reset resets the backoff strategy
func (eb *ExponentialBackoff) Reset() {
	// No state to reset for exponential backoff
}

// LinearBackoff implements linear backoff strategy
type LinearBackoff struct {
	InitialDelay time.Duration
	Increment    time.Duration
	MaxDelay     time.Duration
}

// NextDelay returns the next delay for linear backoff
func (lb *LinearBackoff) NextDelay(attempt int) time.Duration {
	delay := lb.InitialDelay + lb.Increment*time.Duration(attempt)

	if delay > lb.MaxDelay {
		return lb.MaxDelay
	}

	return delay
}

// Reset resets the backoff strategy
func (lb *LinearBackoff) Reset() {
	// No state to reset for linear backoff
}

// FibonacciBackoff implements Fibonacci backoff strategy
type FibonacciBackoff struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
	fibonacci    []int
}

// NewFibonacciBackoff creates a new Fibonacci backoff strategy
func NewFibonacciBackoff(initialDelay, maxDelay time.Duration) *FibonacciBackoff {
	return &FibonacciBackoff{
		InitialDelay: initialDelay,
		MaxDelay:     maxDelay,
		fibonacci:    []int{1, 1},
	}
}

// NextDelay returns the next delay for Fibonacci backoff
func (fb *FibonacciBackoff) NextDelay(attempt int) time.Duration {
	// Ensure we have enough Fibonacci numbers
	for len(fb.fibonacci) <= attempt {
		n := len(fb.fibonacci)
		fb.fibonacci = append(fb.fibonacci, fb.fibonacci[n-1]+fb.fibonacci[n-2])
	}

	delay := fb.InitialDelay * time.Duration(fb.fibonacci[attempt])

	if delay > fb.MaxDelay {
		return fb.MaxDelay
	}

	return delay
}

// Reset resets the backoff strategy
func (fb *FibonacciBackoff) Reset() {
	fb.fibonacci = []int{1, 1}
}

// DecorrelatedJitterBackoff implements AWS-style decorrelated jitter backoff
type DecorrelatedJitterBackoff struct {
	BaseDelay     time.Duration
	MaxDelay      time.Duration
	previousDelay time.Duration
}

// NextDelay returns the next delay for decorrelated jitter backoff
func (djb *DecorrelatedJitterBackoff) NextDelay(attempt int) time.Duration {
	if djb.previousDelay == 0 {
		djb.previousDelay = djb.BaseDelay
	}

	// Calculate next delay with decorrelated jitter
	// sleep = min(cap, random_between(base, sleep * 3))
	minDelay := djb.BaseDelay
	maxDelay := djb.previousDelay * 3

	if maxDelay > djb.MaxDelay {
		maxDelay = djb.MaxDelay
	}

	// Random delay between min and max
	delayRange := int64(maxDelay - minDelay)
	if delayRange <= 0 {
		djb.previousDelay = minDelay
		return minDelay
	}

	randomDelay, _ := rand.Int(rand.Reader, big.NewInt(delayRange))
	delay := minDelay + time.Duration(randomDelay.Int64())

	djb.previousDelay = delay
	return delay
}

// Reset resets the backoff strategy
func (djb *DecorrelatedJitterBackoff) Reset() {
	djb.previousDelay = 0
}

// Helper functions

func randomBool() bool {
	b, _ := rand.Int(rand.Reader, big.NewInt(2))
	return b.Int64() == 1
}

// RetryWithCircuitBreaker combines retry logic with circuit breaker
func RetryWithCircuitBreaker(
	ctx context.Context,
	retryManager *RetryManager,
	breaker *gobreaker.CircuitBreaker,
	fn func() (interface{}, error),
) (interface{}, error) {

	retryFunc := func(ctx context.Context) (interface{}, error) {
		return breaker.Execute(fn)
	}

	result := retryManager.Execute(ctx, retryFunc)
	return result.Value, result.Error
}
