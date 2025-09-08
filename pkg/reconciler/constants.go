package reconciler

import "time"

// API Configuration Constants.
const (
	// BOSH API defaults.
	DefaultBOSHMaxConcurrent  = 1
	DefaultBOSHWorkerPoolSize = 5
	DefaultBOSHTimeout        = 30 * time.Second

	// CF API defaults.
	DefaultCFMaxConcurrent  = 2
	DefaultCFWorkerPoolSize = 10
	DefaultCFTimeout        = 15 * time.Second

	// Vault API defaults.
	DefaultVaultMaxConcurrent  = 3
	DefaultVaultWorkerPoolSize = 20
	DefaultVaultTimeout        = 10 * time.Second

	// Concurrency limits.
	MaxConcurrentLimit = 100

	// Confidence thresholds.
	LowConfidenceThreshold = 0.5

	// Metrics configuration.
	MetricsDurationBufferSize = 100
	MetricsErrorBufferSize    = 50

	// Time calculations.
	HoursPerDay = 24

	// Worker pool latency adjustment.
	LatencyAdjustmentFactor = 0.5

	// Credential parsing.
	MinCredentialParts = 2
	MinPasswordLength  = 8

	// Confidence thresholds.
	VeryLowConfidenceThreshold = 0.3

	// Orphaned deployment settings.
	DaysBeforeStale = 30

	// Worker pool settings.
	WorkerQueueMultiplier = 10
	DefaultTaskTimeout    = 5 * time.Minute

	// Metrics limits.
	MaxMetricsDurations = 100
)
