package reconciler

import (
	"fmt"
	"time"
)

// ReconcilerConfig contains all reconciler configuration with production defaults
type ReconcilerConfig struct {
	// Basic configuration
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"`
	Debug    bool          `yaml:"debug"`

	// Concurrency controls
	Concurrency ConcurrencyConfig `yaml:"concurrency"`

	// API-specific configurations
	APIs APIConfigs `yaml:"apis"`

	// Retry configuration
	Retry RetryConfig `yaml:"retry"`

	// Batch processing
	Batch BatchConfig `yaml:"batch"`

	// Circuit breaker configuration
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`

	// Timeouts
	Timeouts TimeoutConfig `yaml:"timeouts"`

	// Backup configuration (existing)
	Backup BackupConfig `yaml:"backup"`

	// Monitoring and metrics
	Metrics MetricsConfig `yaml:"metrics"`
}

// ConcurrencyConfig controls concurrent processing
type ConcurrencyConfig struct {
	MaxConcurrent        int           `yaml:"max_concurrent"`          // Max concurrent operations
	MaxDeploymentsPerRun int           `yaml:"max_deployments_per_run"` // Max deployments to process per run
	QueueSize            int           `yaml:"queue_size"`              // Size of work queue
	WorkerPoolSize       int           `yaml:"worker_pool_size"`        // Number of worker goroutines
	CooldownPeriod       time.Duration `yaml:"cooldown_period"`         // Time between processing batches
}

// APIConfigs contains configuration for each API
type APIConfigs struct {
	BOSH  APIConfig `yaml:"bosh"`
	CF    APIConfig `yaml:"cf"`
	Vault APIConfig `yaml:"vault"`
}

// APIConfig contains rate limiting and connection settings for an API
type APIConfig struct {
	RateLimit      RateLimitConfig      `yaml:"rate_limit"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
	Timeout        time.Duration        `yaml:"timeout"`
	MaxConnections int                  `yaml:"max_connections"`
	KeepAlive      time.Duration        `yaml:"keep_alive"`
	RetryConfig    *RetryConfig         `yaml:"retry,omitempty"` // Optional API-specific retry
}

// RateLimitConfig defines rate limiting parameters
type RateLimitConfig struct {
	RequestsPerSecond float64       `yaml:"requests_per_second"`
	Burst             int           `yaml:"burst"`
	WaitTimeout       time.Duration `yaml:"wait_timeout"` // Max time to wait for rate limit
}

// CircuitBreakerConfig defines circuit breaker parameters
type CircuitBreakerConfig struct {
	Enabled          bool          `yaml:"enabled"`
	FailureThreshold int           `yaml:"failure_threshold"` // Failures before opening
	SuccessThreshold int           `yaml:"success_threshold"` // Successes before closing
	Timeout          time.Duration `yaml:"timeout"`           // Time before attempting half-open
	MaxConcurrent    int           `yaml:"max_concurrent"`    // Max concurrent requests when half-open
}

// RetryConfig defines retry behavior with exponential backoff
type RetryConfig struct {
	MaxAttempts     int           `yaml:"max_attempts"`
	InitialDelay    time.Duration `yaml:"initial_delay"`
	MaxDelay        time.Duration `yaml:"max_delay"`
	Multiplier      float64       `yaml:"multiplier"`
	Jitter          float64       `yaml:"jitter"`           // 0.0 to 1.0
	RetryableErrors []string      `yaml:"retryable_errors"` // Error patterns to retry
}

// BatchConfig defines batch processing behavior
type BatchConfig struct {
	Size             int           `yaml:"size"`               // Items per batch
	Delay            time.Duration `yaml:"delay"`              // Delay between batches
	MaxParallelBatch int           `yaml:"max_parallel_batch"` // Max parallel batch processing
	AdaptiveScaling  bool          `yaml:"adaptive_scaling"`   // Adjust batch size based on performance
	MinSize          int           `yaml:"min_size"`           // Minimum batch size when scaling
	MaxSize          int           `yaml:"max_size"`           // Maximum batch size when scaling
}

// TimeoutConfig defines various timeout settings
type TimeoutConfig struct {
	ReconciliationRun   time.Duration `yaml:"reconciliation_run"`
	DeploymentScan      time.Duration `yaml:"deployment_scan"`
	InstanceDiscovery   time.Duration `yaml:"instance_discovery"`
	VaultOperations     time.Duration `yaml:"vault_operations"`
	HealthCheck         time.Duration `yaml:"health_check"`
	ShutdownGracePeriod time.Duration `yaml:"shutdown_grace_period"`
}

// MetricsConfig defines metrics collection settings
type MetricsConfig struct {
	Enabled            bool          `yaml:"enabled"`
	CollectionInterval time.Duration `yaml:"collection_interval"`
	RetentionPeriod    time.Duration `yaml:"retention_period"`
	ExportPrometheus   bool          `yaml:"export_prometheus"`
	PrometheusPort     int           `yaml:"prometheus_port"`
}

// LoadDefaults sets production-ready default values
func (c *ReconcilerConfig) LoadDefaults() {
	// Basic defaults
	if c.Interval == 0 {
		c.Interval = 5 * time.Minute
	}

	// Concurrency defaults
	if c.Concurrency.MaxConcurrent == 0 {
		c.Concurrency.MaxConcurrent = 4 // Conservative default
	}
	if c.Concurrency.MaxDeploymentsPerRun == 0 {
		c.Concurrency.MaxDeploymentsPerRun = 100 // Limit per run
	}
	if c.Concurrency.QueueSize == 0 {
		c.Concurrency.QueueSize = 1000
	}
	if c.Concurrency.WorkerPoolSize == 0 {
		c.Concurrency.WorkerPoolSize = 10
	}
	if c.Concurrency.CooldownPeriod == 0 {
		c.Concurrency.CooldownPeriod = 2 * time.Second
	}

	// BOSH API defaults
	c.loadAPIDefaults(&c.APIs.BOSH, "bosh", 10, 5, 30*time.Second)

	// CF API defaults
	c.loadAPIDefaults(&c.APIs.CF, "cf", 20, 10, 15*time.Second)

	// Vault API defaults
	c.loadAPIDefaults(&c.APIs.Vault, "vault", 50, 20, 10*time.Second)

	// Retry defaults
	if c.Retry.MaxAttempts == 0 {
		c.Retry.MaxAttempts = 3
	}
	if c.Retry.InitialDelay == 0 {
		c.Retry.InitialDelay = 1 * time.Second
	}
	if c.Retry.MaxDelay == 0 {
		c.Retry.MaxDelay = 30 * time.Second
	}
	if c.Retry.Multiplier == 0 {
		c.Retry.Multiplier = 2.0
	}
	if c.Retry.Jitter == 0 {
		c.Retry.Jitter = 0.1
	}

	// Batch defaults
	if c.Batch.Size == 0 {
		c.Batch.Size = 10
	}
	if c.Batch.Delay == 0 {
		c.Batch.Delay = 500 * time.Millisecond
	}
	if c.Batch.MaxParallelBatch == 0 {
		c.Batch.MaxParallelBatch = 2
	}
	if c.Batch.MinSize == 0 {
		c.Batch.MinSize = 5
	}
	if c.Batch.MaxSize == 0 {
		c.Batch.MaxSize = 50
	}

	// Timeout defaults
	if c.Timeouts.ReconciliationRun == 0 {
		c.Timeouts.ReconciliationRun = 15 * time.Minute
	}
	if c.Timeouts.DeploymentScan == 0 {
		c.Timeouts.DeploymentScan = 5 * time.Minute
	}
	if c.Timeouts.InstanceDiscovery == 0 {
		c.Timeouts.InstanceDiscovery = 3 * time.Minute
	}
	if c.Timeouts.VaultOperations == 0 {
		c.Timeouts.VaultOperations = 2 * time.Minute
	}
	if c.Timeouts.HealthCheck == 0 {
		c.Timeouts.HealthCheck = 30 * time.Second
	}
	if c.Timeouts.ShutdownGracePeriod == 0 {
		c.Timeouts.ShutdownGracePeriod = 30 * time.Second
	}

	// Metrics defaults
	if c.Metrics.CollectionInterval == 0 {
		c.Metrics.CollectionInterval = 30 * time.Second
	}
	if c.Metrics.RetentionPeriod == 0 {
		c.Metrics.RetentionPeriod = 24 * time.Hour
	}
	if c.Metrics.PrometheusPort == 0 {
		c.Metrics.PrometheusPort = 9090
	}
}

// loadAPIDefaults sets defaults for a specific API configuration
func (c *ReconcilerConfig) loadAPIDefaults(api *APIConfig, name string, rps float64, burst int, timeout time.Duration) {
	// Rate limit defaults
	if api.RateLimit.RequestsPerSecond == 0 {
		api.RateLimit.RequestsPerSecond = rps
	}
	if api.RateLimit.Burst == 0 {
		api.RateLimit.Burst = burst
	}
	if api.RateLimit.WaitTimeout == 0 {
		api.RateLimit.WaitTimeout = 5 * time.Second
	}

	// Circuit breaker defaults
	if api.CircuitBreaker.FailureThreshold == 0 {
		api.CircuitBreaker.FailureThreshold = 5
	}
	if api.CircuitBreaker.SuccessThreshold == 0 {
		api.CircuitBreaker.SuccessThreshold = 2
	}
	if api.CircuitBreaker.Timeout == 0 {
		api.CircuitBreaker.Timeout = 60 * time.Second
	}
	if api.CircuitBreaker.MaxConcurrent == 0 {
		api.CircuitBreaker.MaxConcurrent = 1
	}

	// Connection defaults
	if api.Timeout == 0 {
		api.Timeout = timeout
	}
	if api.MaxConnections == 0 {
		api.MaxConnections = 10
	}
	if api.KeepAlive == 0 {
		api.KeepAlive = 30 * time.Second
	}
}

// Validate checks configuration for errors
func (c *ReconcilerConfig) Validate() error {
	if c.Interval < 10*time.Second {
		return fmt.Errorf("interval must be at least 10 seconds")
	}

	if c.Concurrency.MaxConcurrent < 1 {
		return fmt.Errorf("max_concurrent must be at least 1")
	}

	if c.Concurrency.MaxConcurrent > 100 {
		return fmt.Errorf("max_concurrent should not exceed 100 to prevent resource exhaustion")
	}

	if c.Batch.Size < 1 {
		return fmt.Errorf("batch size must be at least 1")
	}

	if c.Retry.MaxAttempts < 1 {
		return fmt.Errorf("max_attempts must be at least 1")
	}

	if c.Retry.Multiplier < 1.0 {
		return fmt.Errorf("retry multiplier must be at least 1.0")
	}

	if c.Retry.Jitter < 0 || c.Retry.Jitter > 1.0 {
		return fmt.Errorf("retry jitter must be between 0.0 and 1.0")
	}

	// Validate API configurations
	apis := map[string]*APIConfig{
		"bosh":  &c.APIs.BOSH,
		"cf":    &c.APIs.CF,
		"vault": &c.APIs.Vault,
	}

	for name, api := range apis {
		if api.RateLimit.RequestsPerSecond <= 0 {
			return fmt.Errorf("%s rate limit requests_per_second must be positive", name)
		}
		if api.RateLimit.Burst < 1 {
			return fmt.Errorf("%s rate limit burst must be at least 1", name)
		}
		if api.MaxConnections < 1 {
			return fmt.Errorf("%s max_connections must be at least 1", name)
		}
	}

	return nil
}

// GetEffectiveBatchSize returns the batch size to use based on adaptive scaling
func (c *ReconcilerConfig) GetEffectiveBatchSize(performanceScore float64) int {
	if !c.Batch.AdaptiveScaling {
		return c.Batch.Size
	}

	// Adjust batch size based on performance (0.0 = poor, 1.0 = excellent)
	scaledSize := int(float64(c.Batch.MinSize) + performanceScore*float64(c.Batch.MaxSize-c.Batch.MinSize))

	if scaledSize < c.Batch.MinSize {
		return c.Batch.MinSize
	}
	if scaledSize > c.Batch.MaxSize {
		return c.Batch.MaxSize
	}

	return scaledSize
}
