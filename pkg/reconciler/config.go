package reconciler

import (
	"time"
)

// Constants for reconciler configuration defaults.
const (
	// Default intervals and durations.
	defaultReconcileInterval        = 5 * time.Minute
	defaultCooldownPeriod           = 2 * time.Second
	defaultRetryMaxDelay            = 30 * time.Second
	defaultBatchDelay               = 500 * time.Millisecond
	defaultReconciliationTimeout    = 15 * time.Minute
	defaultDeploymentScanTimeout    = 5 * time.Minute
	defaultInstanceDiscoveryTimeout = 3 * time.Minute
	defaultVaultOperationsTimeout   = 2 * time.Minute
	defaultHealthCheckTimeout       = 30 * time.Second
	defaultShutdownGracePeriod      = 30 * time.Second
	defaultMetricsRetentionPeriod   = 24 * time.Hour
	minReconcileInterval            = 10 * time.Second
	defaultCircuitBreakerTimeout    = 60 * time.Second

	// Default multipliers and ratios.
	queueSizeMultiplier      = 10
	batchMaxSizeMultiplier   = 10
	retryJitterFactor        = 0.5
	releaseConfidenceFactor  = 0.5
	groupConfidenceFactor    = 0.7
	groupConfidenceWeight    = 0.3
	deploymentConfidenceHigh = 0.9
	deploymentConfidenceMid  = 0.7
	deploymentConfidenceLow  = 0.5
	deploymentConfidenceMin  = 0.3
	historyMaxSize           = 100
	compressionRatioPercent  = 100

	// Deployment type weights.
	weightBOSH  = 4.0
	weightCF    = 20.0
	weightVault = 50.0

	// Matcher confidence thresholds.
	confidenceThresholdMin = 2

	// Default RabbitMQ ports.
	defaultRabbitMQPort     = 5672
	defaultRabbitMQMgmtPort = 15672

	// Default buffer sizes.
	maxErrorsBuffer        = 50
	defaultDurationsBuffer = 100
	maxRecentLatencies     = 100

	// Worker pool defaults.
	workerMaxLimitMultiplier = 2
	healthCheckInterval      = 30 * time.Second

	// Retry defaults.
	defaultRetryAttempts   = 3
	defaultInitialDelay    = 1 * time.Second
	defaultRetryMultiplier = 2.0
	defaultRetryJitter     = 0.1

	// Batch processing defaults.
	defaultBatchSize        = 10
	defaultMaxParallelBatch = 2
	defaultMinBatchSize     = 5
	defaultMaxBatchSize     = 50

	// Metrics defaults.
	defaultPrometheusPort = 9090
)

// ReconcilerConfig contains all reconciler configuration with production defaults.
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

// ConcurrencyConfig controls concurrent processing.
type ConcurrencyConfig struct {
	MaxConcurrent        int           `yaml:"max_concurrent"`          // Max concurrent operations
	MaxDeploymentsPerRun int           `yaml:"max_deployments_per_run"` // Max deployments to process per run
	QueueSize            int           `yaml:"queue_size"`              // Size of work queue
	WorkerPoolSize       int           `yaml:"worker_pool_size"`        // Number of worker goroutines
	CooldownPeriod       time.Duration `yaml:"cooldown_period"`         // Time between processing batches
}

// APIConfigs contains configuration for each API.
type APIConfigs struct {
	BOSH  APIConfig `yaml:"bosh"`
	CF    APIConfig `yaml:"cf"`
	Vault APIConfig `yaml:"vault"`
}

// APIConfig contains rate limiting and connection settings for an API.
type APIConfig struct {
	RateLimit      RateLimitConfig      `yaml:"rate_limit"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
	Timeout        time.Duration        `yaml:"timeout"`
	MaxConnections int                  `yaml:"max_connections"`
	KeepAlive      time.Duration        `yaml:"keep_alive"`
	RetryConfig    *RetryConfig         `yaml:"retry,omitempty"` // Optional API-specific retry
}

// RateLimitConfig defines rate limiting parameters.
type RateLimitConfig struct {
	RequestsPerSecond float64       `yaml:"requests_per_second"`
	Burst             int           `yaml:"burst"`
	WaitTimeout       time.Duration `yaml:"wait_timeout"` // Max time to wait for rate limit
}

// CircuitBreakerConfig defines circuit breaker parameters.
type CircuitBreakerConfig struct {
	Enabled          bool          `yaml:"enabled"`
	FailureThreshold int           `yaml:"failure_threshold"` // Failures before opening
	SuccessThreshold int           `yaml:"success_threshold"` // Successes before closing
	Timeout          time.Duration `yaml:"timeout"`           // Time before attempting half-open
	MaxConcurrent    int           `yaml:"max_concurrent"`    // Max concurrent requests when half-open
}

// RetryConfig defines retry behavior with exponential backoff.
type RetryConfig struct {
	MaxAttempts     int           `yaml:"max_attempts"`
	InitialDelay    time.Duration `yaml:"initial_delay"`
	MaxDelay        time.Duration `yaml:"max_delay"`
	Multiplier      float64       `yaml:"multiplier"`
	Jitter          float64       `yaml:"jitter"`           // 0.0 to 1.0
	RetryableErrors []string      `yaml:"retryable_errors"` // Error patterns to retry
}

// BatchConfig defines batch processing behavior.
type BatchConfig struct {
	Size             int           `yaml:"size"`               // Items per batch
	Delay            time.Duration `yaml:"delay"`              // Delay between batches
	MaxParallelBatch int           `yaml:"max_parallel_batch"` // Max parallel batch processing
	AdaptiveScaling  bool          `yaml:"adaptive_scaling"`   // Adjust batch size based on performance
	MinSize          int           `yaml:"min_size"`           // Minimum batch size when scaling
	MaxSize          int           `yaml:"max_size"`           // Maximum batch size when scaling
}

// TimeoutConfig defines various timeout settings.
type TimeoutConfig struct {
	ReconciliationRun   time.Duration `yaml:"reconciliation_run"`
	DeploymentScan      time.Duration `yaml:"deployment_scan"`
	InstanceDiscovery   time.Duration `yaml:"instance_discovery"`
	VaultOperations     time.Duration `yaml:"vault_operations"`
	HealthCheck         time.Duration `yaml:"health_check"`
	ShutdownGracePeriod time.Duration `yaml:"shutdown_grace_period"`
}

// MetricsConfig defines metrics collection settings.
type MetricsConfig struct {
	Enabled            bool          `yaml:"enabled"`
	CollectionInterval time.Duration `yaml:"collection_interval"`
	RetentionPeriod    time.Duration `yaml:"retention_period"`
	ExportPrometheus   bool          `yaml:"export_prometheus"`
	PrometheusPort     int           `yaml:"prometheus_port"`
}

// LoadDefaults sets production-ready default values.
func (c *ReconcilerConfig) LoadDefaults() {
	c.loadBasicDefaults()
	c.loadConcurrencyDefaults()
	c.loadAPIDefaults(&c.APIs.BOSH, "bosh", DefaultBOSHMaxConcurrent, DefaultBOSHWorkerPoolSize, DefaultBOSHTimeout)
	c.loadAPIDefaults(&c.APIs.CF, "cf", DefaultCFMaxConcurrent, DefaultCFWorkerPoolSize, DefaultCFTimeout)
	c.loadAPIDefaults(&c.APIs.Vault, "vault", DefaultVaultMaxConcurrent, DefaultVaultWorkerPoolSize, DefaultVaultTimeout)
	c.loadRetryDefaults()
	c.loadBatchDefaults()
	c.loadTimeoutDefaults()
	c.loadMetricsDefaults()
}

// GetEffectiveBatchSize calculates the effective batch size based on performance score.
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

// Validate checks configuration for errors and sets safe defaults.
func (c *ReconcilerConfig) Validate() error {
	c.validateBasicSettings()
	c.validateConcurrencySettings()
	c.validateBatchSettings()
	c.validateRetrySettings()
	c.validateAPIConfigurations()
	c.validateTimeoutSettings()

	return nil
}

// loadBasicDefaults sets basic configuration defaults.
func (c *ReconcilerConfig) loadBasicDefaults() {
	if c.Interval == 0 {
		c.Interval = defaultReconcileInterval
	}
}

// loadConcurrencyDefaults sets concurrency-related defaults.
func (c *ReconcilerConfig) loadConcurrencyDefaults() {
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
		c.Concurrency.CooldownPeriod = defaultCooldownPeriod
	}
}

// loadRetryDefaults sets retry-related defaults.
func (c *ReconcilerConfig) loadRetryDefaults() {
	if c.Retry.MaxAttempts == 0 {
		c.Retry.MaxAttempts = defaultRetryAttempts
	}

	if c.Retry.InitialDelay == 0 {
		c.Retry.InitialDelay = defaultInitialDelay
	}

	if c.Retry.MaxDelay == 0 {
		c.Retry.MaxDelay = defaultRetryMaxDelay
	}

	if c.Retry.Multiplier == 0 {
		c.Retry.Multiplier = defaultRetryMultiplier
	}

	if c.Retry.Jitter == 0 {
		c.Retry.Jitter = defaultRetryJitter
	}
}

// loadBatchDefaults sets batch processing defaults.
func (c *ReconcilerConfig) loadBatchDefaults() {
	if c.Batch.Size == 0 {
		c.Batch.Size = defaultBatchSize
	}

	if c.Batch.Delay == 0 {
		c.Batch.Delay = defaultBatchDelay
	}

	if c.Batch.MaxParallelBatch == 0 {
		c.Batch.MaxParallelBatch = defaultMaxParallelBatch
	}

	if c.Batch.MinSize == 0 {
		c.Batch.MinSize = defaultMinBatchSize
	}

	if c.Batch.MaxSize == 0 {
		c.Batch.MaxSize = 50
	}
}

// loadTimeoutDefaults sets timeout-related defaults.
func (c *ReconcilerConfig) loadTimeoutDefaults() {
	if c.Timeouts.ReconciliationRun == 0 {
		c.Timeouts.ReconciliationRun = defaultReconciliationTimeout
	}

	if c.Timeouts.DeploymentScan == 0 {
		c.Timeouts.DeploymentScan = defaultDeploymentScanTimeout
	}

	if c.Timeouts.InstanceDiscovery == 0 {
		c.Timeouts.InstanceDiscovery = defaultInstanceDiscoveryTimeout
	}

	if c.Timeouts.VaultOperations == 0 {
		c.Timeouts.VaultOperations = defaultVaultOperationsTimeout
	}

	if c.Timeouts.HealthCheck == 0 {
		c.Timeouts.HealthCheck = defaultHealthCheckTimeout
	}

	if c.Timeouts.ShutdownGracePeriod == 0 {
		c.Timeouts.ShutdownGracePeriod = defaultShutdownGracePeriod
	}
}

// loadMetricsDefaults sets metrics-related defaults.
func (c *ReconcilerConfig) loadMetricsDefaults() {
	if c.Metrics.CollectionInterval == 0 {
		c.Metrics.CollectionInterval = defaultHealthCheckTimeout
	}

	if c.Metrics.RetentionPeriod == 0 {
		c.Metrics.RetentionPeriod = defaultMetricsRetentionPeriod
	}

	if c.Metrics.PrometheusPort == 0 {
		c.Metrics.PrometheusPort = defaultPrometheusPort
	}
}

// validateBasicSettings validates basic configuration settings.
func (c *ReconcilerConfig) validateBasicSettings() {
	if c.Interval < 10*time.Second {
		c.Interval = minReconcileInterval // Set safe minimum
	}
}

// validateConcurrencySettings validates concurrency-related settings.
func (c *ReconcilerConfig) validateConcurrencySettings() {
	// Critical: Ensure channel buffer sizes are valid to prevent panics
	if c.Concurrency.MaxConcurrent < 1 {
		c.Concurrency.MaxConcurrent = 1 // Set safe default to prevent deadlock
	}

	if c.Concurrency.MaxConcurrent > MaxConcurrentLimit {
		c.Concurrency.MaxConcurrent = MaxConcurrentLimit // Cap to prevent resource exhaustion
	}
	// Ensure queue sizes are positive to prevent channel creation issues
	if c.Concurrency.QueueSize <= 0 {
		c.Concurrency.QueueSize = c.Concurrency.MaxConcurrent * queueSizeMultiplier // Safe default
	}

	if c.Concurrency.WorkerPoolSize <= 0 {
		c.Concurrency.WorkerPoolSize = c.Concurrency.MaxConcurrent // Match max concurrent
	}

	if c.Concurrency.MaxDeploymentsPerRun <= 0 {
		c.Concurrency.MaxDeploymentsPerRun = 100 // Safe default
	}

	if c.Concurrency.CooldownPeriod <= 0 {
		c.Concurrency.CooldownPeriod = 1 * time.Second
	}
}

// validateBatchSettings validates batch processing settings.
func (c *ReconcilerConfig) validateBatchSettings() {
	if c.Batch.Size < 1 {
		c.Batch.Size = 10 // Set safe default
	}

	if c.Batch.MinSize < 1 {
		c.Batch.MinSize = 1
	}

	if c.Batch.MaxSize < c.Batch.MinSize {
		c.Batch.MaxSize = c.Batch.MinSize * batchMaxSizeMultiplier
	}
}

// validateRetrySettings validates retry configuration settings.
func (c *ReconcilerConfig) validateRetrySettings() {
	if c.Retry.MaxAttempts < 1 {
		c.Retry.MaxAttempts = defaultRetryAttempts // Set safe default
	}

	if c.Retry.Multiplier < 1.0 {
		c.Retry.Multiplier = defaultRetryMultiplier // Set safe default
	}

	if c.Retry.Jitter < 0 || c.Retry.Jitter > 1.0 {
		c.Retry.Jitter = defaultRetryJitter // Set safe default
	}
}

// validateAPIConfigurations validates API configuration settings.
func (c *ReconcilerConfig) validateAPIConfigurations() {
	apis := map[string]*APIConfig{
		"bosh":  &c.APIs.BOSH,
		"cf":    &c.APIs.CF,
		"vault": &c.APIs.Vault,
	}
	defaultRates := map[string]float64{
		"bosh":  weightBOSH,
		"cf":    weightCF,
		"vault": weightVault,
	}

	for name, api := range apis {
		if api.RateLimit.RequestsPerSecond <= 0 {
			api.RateLimit.RequestsPerSecond = defaultRates[name] // Set safe defaults
		}

		if api.RateLimit.Burst < 1 {
			const burstMultiplier = 2

			api.RateLimit.Burst = int(api.RateLimit.RequestsPerSecond * burstMultiplier) // Burst = 2x rate
		}

		if api.MaxConnections < 1 {
			api.MaxConnections = 10 // Safe default
		}
	}
}

// validateTimeoutSettings validates timeout configuration settings.
func (c *ReconcilerConfig) validateTimeoutSettings() {
	if c.Timeouts.ReconciliationRun <= 0 {
		c.Timeouts.ReconciliationRun = defaultReconciliationTimeout
	}

	if c.Timeouts.DeploymentScan <= 0 {
		c.Timeouts.DeploymentScan = defaultDeploymentScanTimeout
	}

	if c.Timeouts.InstanceDiscovery <= 0 {
		c.Timeouts.InstanceDiscovery = defaultVaultOperationsTimeout
	}

	if c.Timeouts.VaultOperations <= 0 {
		c.Timeouts.VaultOperations = defaultHealthCheckTimeout
	}

	if c.Timeouts.HealthCheck <= 0 {
		c.Timeouts.HealthCheck = 1 * time.Minute
	}

	if c.Timeouts.ShutdownGracePeriod <= 0 {
		c.Timeouts.ShutdownGracePeriod = defaultShutdownGracePeriod
	}
}

// loadAPIDefaults sets defaults for a specific API configuration.
func (c *ReconcilerConfig) loadAPIDefaults(api *APIConfig, _ string, rps float64, burst int, timeout time.Duration) {
	// Rate limit defaults
	if api.RateLimit.RequestsPerSecond == 0 {
		api.RateLimit.RequestsPerSecond = rps
	}

	if api.RateLimit.Burst == 0 {
		api.RateLimit.Burst = burst
	}

	if api.RateLimit.WaitTimeout == 0 {
		const defaultWaitTimeout = 5

		api.RateLimit.WaitTimeout = defaultWaitTimeout * time.Second
	}

	// Circuit breaker defaults
	if api.CircuitBreaker.FailureThreshold == 0 {
		api.CircuitBreaker.FailureThreshold = 5
	}

	if api.CircuitBreaker.SuccessThreshold == 0 {
		api.CircuitBreaker.SuccessThreshold = 2
	}

	if api.CircuitBreaker.Timeout == 0 {
		api.CircuitBreaker.Timeout = defaultCircuitBreakerTimeout
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
		api.KeepAlive = defaultHealthCheckTimeout
	}
}
