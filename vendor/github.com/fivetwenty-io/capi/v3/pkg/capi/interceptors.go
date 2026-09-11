package capi

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/fivetwenty-io/capi/v3/internal/constants"
)

// Request represents an HTTP request that can be intercepted.
type Request struct {
	Method   string
	Path     string
	Headers  http.Header
	Body     []byte
	Metadata map[string]any
}

// Response represents an HTTP response that can be intercepted.
type Response struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
	Error      error
}

// RequestInterceptor is called before a request is sent.
type RequestInterceptor func(ctx context.Context, req *Request) error

// ResponseInterceptor is called after a response is received.
type ResponseInterceptor func(ctx context.Context, req *Request, resp *Response) error

// InterceptorChain manages a chain of interceptors.
type InterceptorChain struct {
	requestInterceptors  []RequestInterceptor
	responseInterceptors []ResponseInterceptor
}

// NewInterceptorChain creates a new interceptor chain.
func NewInterceptorChain() *InterceptorChain {
	return &InterceptorChain{
		requestInterceptors:  make([]RequestInterceptor, 0),
		responseInterceptors: make([]ResponseInterceptor, 0),
	}
}

// AddRequestInterceptor adds a request interceptor to the chain.
func (c *InterceptorChain) AddRequestInterceptor(interceptor RequestInterceptor) {
	c.requestInterceptors = append(c.requestInterceptors, interceptor)
}

// AddResponseInterceptor adds a response interceptor to the chain.
func (c *InterceptorChain) AddResponseInterceptor(interceptor ResponseInterceptor) {
	c.responseInterceptors = append(c.responseInterceptors, interceptor)
}

// ExecuteRequestInterceptors runs all request interceptors.
func (c *InterceptorChain) ExecuteRequestInterceptors(ctx context.Context, req *Request) error {
	for _, interceptor := range c.requestInterceptors {
		err := interceptor(ctx, req)
		if err != nil {
			return fmt.Errorf("request interceptor failed: %w", err)
		}
	}

	return nil
}

// ExecuteResponseInterceptors runs all response interceptors.
func (c *InterceptorChain) ExecuteResponseInterceptors(ctx context.Context, req *Request, resp *Response) error {
	for _, interceptor := range c.responseInterceptors {
		err := interceptor(ctx, req, resp)
		if err != nil {
			return fmt.Errorf("response interceptor failed: %w", err)
		}
	}

	return nil
}

// Common Interceptors

// LoggingInterceptor logs requests and responses.
func LoggingInterceptor(logger Logger) RequestInterceptor {
	return func(ctx context.Context, req *Request) error {
		logger.Debug("API Request", map[string]any{
			"method": req.Method,
			"path":   req.Path,
		})

		return nil
	}
}

// LoggingResponseInterceptor logs responses.
func LoggingResponseInterceptor(logger Logger) ResponseInterceptor {
	return func(ctx context.Context, req *Request, resp *Response) error {
		fields := map[string]any{
			"method":      req.Method,
			"path":        req.Path,
			"status_code": resp.StatusCode,
		}

		if resp.Error != nil {
			logger.Error("API Response Error", fields)
		} else {
			logger.Debug("API Response", fields)
		}

		return nil
	}
}

// RateLimitInterceptor implements client-side rate limiting.
//
// It starts a background refill goroutine that lives for the lifetime of the
// process: the returned interceptor has no shutdown hook, so call this once at
// client setup and reuse the result rather than per request, which would leak
// a goroutine each call.
func RateLimitInterceptor(requestsPerSecond int) RequestInterceptor {
	// Simple token bucket implementation
	bucket := make(chan struct{}, requestsPerSecond)

	// Fill the bucket initially
	for range requestsPerSecond {
		bucket <- struct{}{}
	}

	// Refill the bucket periodically
	go func() {
		ticker := time.NewTicker(time.Second / time.Duration(requestsPerSecond))
		defer ticker.Stop()

		for range ticker.C {
			select {
			case bucket <- struct{}{}:
			default:
				// Bucket is full
			}
		}
	}()

	return func(ctx context.Context, req *Request) error {
		select {
		case <-bucket:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// RetryInterceptor adds retry logic for failed requests.
type RetryConfig struct {
	// MaxRetries is the maximum number of retry attempts after the initial try.
	MaxRetries int
	// RetryDelay is the base delay between retries (may be jittered/backed off).
	RetryDelay time.Duration
	// MaxDelay caps the maximum backoff delay between retries.
	MaxDelay time.Duration
	// RetryOnCodes lists HTTP status codes that should trigger a retry
	// (e.g., 429, 500, 502, 503, 504).
	RetryOnCodes []int
}

// DefaultRetryConfig returns default retry configuration.
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxRetries:   constants.LowRetryMax,
		RetryDelay:   1 * time.Second,
		MaxDelay:     constants.ExtendedRetryWaitMax,
		RetryOnCodes: []int{429, 500, 502, 503, 504},
	}
}

// RetryResponseInterceptor implements retry logic.
func RetryResponseInterceptor(config *RetryConfig) ResponseInterceptor {
	if config == nil {
		config = DefaultRetryConfig()
	}

	return func(ctx context.Context, req *Request, resp *Response) error {
		// Check if we should retry based on status code
		shouldRetry := false

		for _, code := range config.RetryOnCodes {
			if resp.StatusCode == code {
				shouldRetry = true

				break
			}
		}

		if !shouldRetry {
			return nil
		}

		// Set a retry marker in the response
		// The actual retry logic would be implemented in the HTTP client
		if resp.Headers == nil {
			resp.Headers = make(http.Header)
		}

		resp.Headers.Set("X-Should-Retry", "true")

		return nil
	}
}

// AuthenticationInterceptor adds authentication headers.
func AuthenticationInterceptor(tokenProvider func(context.Context) (string, error)) RequestInterceptor {
	return func(ctx context.Context, req *Request) error {
		token, err := tokenProvider(ctx)
		if err != nil {
			return fmt.Errorf("failed to get authentication token: %w", err)
		}

		if req.Headers == nil {
			req.Headers = make(http.Header)
		}

		req.Headers.Set("Authorization", "Bearer "+token)

		return nil
	}
}

// HeaderInterceptor adds custom headers to requests.
func HeaderInterceptor(headers map[string]string) RequestInterceptor {
	return func(ctx context.Context, req *Request) error {
		if req.Headers == nil {
			req.Headers = make(http.Header)
		}

		for key, value := range headers {
			req.Headers.Set(key, value)
		}

		return nil
	}
}

// contextKey is a custom type for context keys to avoid collisions.
type contextKey string

// TimeoutInterceptor adds a timeout to requests.
func TimeoutInterceptor(timeout time.Duration) RequestInterceptor {
	return func(ctx context.Context, req *Request) error {
		// The timeout context should be handled by the caller
		// This interceptor can validate timeout or prepare for it
		// but the actual timeout context should be created by the HTTP client
		return nil
	}
}

// MetricsInterceptor collects metrics about API calls.
type Metrics struct {
	TotalRequests   int64
	TotalErrors     int64
	TotalLatency    time.Duration
	AverageLatency  time.Duration
	LastRequestTime time.Time
}

// MetricsCollector collects API metrics. Safe for concurrent use: the metrics
// map and the per-endpoint counters are mutated from response interceptors that
// run on every request, so all access is guarded by mu.
type MetricsCollector struct {
	mu       sync.Mutex
	metrics  map[string]*Metrics
	onChange func(endpoint string, metrics *Metrics)
}

// NewMetricsCollector creates a new metrics collector.
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		metrics: make(map[string]*Metrics),
	}
}

// SetOnChange sets a callback for when metrics change.
func (m *MetricsCollector) SetOnChange(fn func(endpoint string, metrics *Metrics)) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.onChange = fn
}

// GetMetrics returns a copy of the metrics for an endpoint, or nil if none.
// A copy is returned so callers cannot observe a torn read of counters that
// concurrent requests are still updating.
func (m *MetricsCollector) GetMetrics(endpoint string) *Metrics {
	m.mu.Lock()
	defer m.mu.Unlock()

	if metrics, ok := m.metrics[endpoint]; ok {
		snapshot := *metrics

		return &snapshot
	}

	return nil
}

// MetricsRequestInterceptor records request start time.
func MetricsRequestInterceptor(collector *MetricsCollector) RequestInterceptor {
	return func(ctx context.Context, req *Request) error {
		// Store the start time in the request metadata
		if req.Metadata == nil {
			req.Metadata = make(map[string]any)
		}

		req.Metadata["start_time"] = time.Now()

		return nil
	}
}

// MetricsResponseInterceptor records response metrics.
func MetricsResponseInterceptor(collector *MetricsCollector) ResponseInterceptor {
	return func(ctx context.Context, req *Request, resp *Response) error {
		endpoint := fmt.Sprintf("%s %s", req.Method, req.Path)

		collector.mu.Lock()

		// Get or create metrics for this endpoint
		metrics, ok := collector.metrics[endpoint]
		if !ok {
			metrics = &Metrics{}
			collector.metrics[endpoint] = metrics
		}

		// Update metrics
		metrics.TotalRequests++
		metrics.LastRequestTime = time.Now()

		// Calculate latency if start time is available in request metadata
		if req.Metadata != nil {
			if startTime, ok := req.Metadata["start_time"].(time.Time); ok {
				latency := time.Since(startTime)
				metrics.TotalLatency += latency
				metrics.AverageLatency = metrics.TotalLatency / time.Duration(metrics.TotalRequests)
			}
		}

		// Count errors
		if resp.Error != nil || resp.StatusCode >= 400 {
			metrics.TotalErrors++
		}

		// Snapshot for the callback, then release the lock before invoking it
		// so an onChange handler that calls back into the collector cannot
		// deadlock.
		onChange := collector.onChange
		snapshot := *metrics
		collector.mu.Unlock()

		if onChange != nil {
			onChange(endpoint, &snapshot)
		}

		return nil
	}
}

// CircuitBreakerInterceptor implements circuit breaker pattern.
type CircuitBreakerConfig struct {
	Threshold        int           // Number of failures before opening
	Timeout          time.Duration // Time before trying again
	SuccessThreshold int           // Number of successes to close
}

// circuitStateClosed is the CircuitBreaker.state value for the closed state
// (requests pass through normally). The open and half-open states are
// constants.StatusOpen and constants.StatusHalfOpen; closed has no analog in
// the shared constants package because it is local to this breaker.
const circuitStateClosed = "closed"

// CircuitBreaker tracks circuit state. Safe for concurrent use: the request
// and response interceptors that read and mutate the state below run on every
// request, so all access is guarded by mu.
type CircuitBreaker struct {
	mu          sync.Mutex
	config      *CircuitBreakerConfig
	failures    int
	successes   int
	state       string // circuitStateClosed, constants.StatusOpen, constants.StatusHalfOpen
	lastFailure time.Time
	// now returns the current time. It is an unexported seam defaulting to
	// time.Now; tests override it to drive the timeout transition
	// deterministically without sleeping on the wall clock.
	now func() time.Time
}

// NewCircuitBreaker creates a new circuit breaker.
func NewCircuitBreaker(config *CircuitBreakerConfig) *CircuitBreaker {
	if config == nil {
		config = &CircuitBreakerConfig{
			Threshold:        constants.CircuitBreakerThreshold,
			Timeout:          constants.CircuitBreakerTimeout,
			SuccessThreshold: constants.CircuitBreakerSuccessThreshold,
		}
	}

	return &CircuitBreaker{
		config: config,
		state:  circuitStateClosed,
		now:    time.Now,
	}
}

// CircuitBreakerRequestInterceptor checks circuit state before requests.
func CircuitBreakerRequestInterceptor(breaker *CircuitBreaker) RequestInterceptor {
	return func(ctx context.Context, req *Request) error {
		breaker.mu.Lock()
		defer breaker.mu.Unlock()

		if breaker.state == constants.StatusOpen {
			// Check if timeout has passed
			if breaker.now().Sub(breaker.lastFailure) > breaker.config.Timeout {
				breaker.state = constants.StatusHalfOpen
				breaker.successes = 0
			} else {
				return ErrCircuitBreakerOpen
			}
		}

		return nil
	}
}

// CircuitBreakerResponseInterceptor updates circuit state based on responses.
func CircuitBreakerResponseInterceptor(breaker *CircuitBreaker) ResponseInterceptor {
	return func(ctx context.Context, req *Request, resp *Response) error {
		breaker.mu.Lock()
		defer breaker.mu.Unlock()

		if resp.Error != nil || resp.StatusCode >= 500 {
			// Record failure
			breaker.failures++
			breaker.lastFailure = breaker.now()

			if breaker.failures >= breaker.config.Threshold {
				breaker.state = constants.StatusOpen
			}

			if breaker.state == constants.StatusHalfOpen {
				breaker.state = constants.StatusOpen
			}
		} else {
			// Record success
			switch breaker.state {
			case constants.StatusHalfOpen:
				breaker.successes++
				if breaker.successes >= breaker.config.SuccessThreshold {
					breaker.state = circuitStateClosed
					breaker.failures = 0
				}
			case circuitStateClosed:
				breaker.failures = 0
			}
		}

		return nil
	}
}
