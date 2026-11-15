package reconciler

import "time"

// MetadataUnclaimedDeployment marks an InstanceData built from a BOSH
// deployment that has neither a vault index entry nor a Cloud Foundry service
// instance. The reconciler reports such deployments instead of adopting them.
const MetadataUnclaimedDeployment = "unclaimed_deployment"

// OrphanRemovalAfter is how long an index entry stays marked orphaned before
// the reconciler removes it, and OrphanRemovalChecks is how many reconciler
// runs may find it orphaned before that removal. Whichever comes first wins.
const (
	OrphanRemovalAfter  = 24 * time.Hour
	OrphanRemovalChecks = 24
)

// UnclaimedDeployment is a BOSH deployment that has neither a vault index
// entry nor a Cloud Foundry service instance.
type UnclaimedDeployment struct {
	InstanceID     string
	DeploymentName string
	// FirstSeen is when a run first found the deployment unclaimed.
	FirstSeen time.Time
}

// UnclaimedReport lists the unclaimed deployments the last run with complete
// CF discovery found. Known is false until such a run has happened, because an
// empty list from a run that could not ask CF would be a guess.
type UnclaimedReport struct {
	Deployments []UnclaimedDeployment
	AsOf        time.Time
	Known       bool
}

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
