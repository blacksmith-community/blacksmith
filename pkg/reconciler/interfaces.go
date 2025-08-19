package reconciler

import (
	"context"
	"time"
)

// Manager manages the reconciliation lifecycle
type Manager interface {
	Start(ctx context.Context) error
	Stop() error
	GetStatus() Status
	ForceReconcile() error
}

// Scanner retrieves deployments from BOSH
type Scanner interface {
	ScanDeployments(ctx context.Context) ([]DeploymentInfo, error)
	GetDeploymentDetails(ctx context.Context, name string) (*DeploymentDetail, error)
}

// Matcher matches deployments to services/plans
type Matcher interface {
	MatchDeployment(deployment DeploymentInfo, services []Service) (*MatchResult, error)
	ValidateMatch(match *MatchResult) error
}

// Updater updates Vault with deployment info
type Updater interface {
	UpdateInstance(ctx context.Context, instance *InstanceData) error
	GetInstance(ctx context.Context, instanceID string) (*InstanceData, error)
}

// Synchronizer ensures index consistency
type Synchronizer interface {
	SyncIndex(ctx context.Context, instances []InstanceData) error
	ValidateIndex(ctx context.Context) error
}

// MetricsCollector collects reconciliation metrics
type MetricsCollector interface {
	ReconciliationStarted()
	ReconciliationCompleted(duration time.Duration)
	ReconciliationError(err error)
	DeploymentsScanned(count int)
	InstancesMatched(count int)
	InstancesUpdated(count int)
	GetMetrics() Metrics
}

// DeploymentInfo contains basic deployment information
type DeploymentInfo struct {
	Name        string
	Manifest    string
	Releases    []ReleaseInfo
	Stemcells   []StemcellInfo
	Teams       []string
	CloudConfig string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// DeploymentDetail contains detailed deployment information
type DeploymentDetail struct {
	DeploymentInfo
	VMs        []VMInfo
	Variables  map[string]interface{}
	Properties map[string]interface{}
}

// ReleaseInfo contains release information
type ReleaseInfo struct {
	Name    string
	Version string
	URL     string
	SHA1    string
}

// StemcellInfo contains stemcell information
type StemcellInfo struct {
	Name    string
	Version string
	OS      string
	CID     string
}

// VMInfo contains VM information
type VMInfo struct {
	CID          string
	Name         string
	JobName      string
	Index        int
	State        string
	AZ           string
	IPs          []string
	ResourcePool string
}

// MatchResult contains the result of matching a deployment
type MatchResult struct {
	ServiceID   string
	PlanID      string
	InstanceID  string
	Confidence  float64
	MatchReason string
}

// MatchedDeployment combines deployment details with match result
type MatchedDeployment struct {
	Deployment DeploymentDetail
	Match      MatchResult
}

// InstanceData contains instance data to be stored in Vault
type InstanceData struct {
	ID             string
	ServiceID      string
	PlanID         string
	DeploymentName string
	Manifest       string
	Metadata       map[string]interface{}
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastSyncedAt   time.Time
}

// Status contains reconciler status information
type Status struct {
	Running         bool
	LastRunTime     time.Time
	LastRunDuration time.Duration
	InstancesFound  int
	InstancesSynced int
	Errors          []error
}

// Metrics contains reconciliation metrics
type Metrics struct {
	TotalRuns            int64
	SuccessfulRuns       int64
	FailedRuns           int64
	TotalDuration        time.Duration
	AverageDuration      time.Duration
	LastRunDuration      time.Duration
	TotalDeployments     int64
	TotalInstancesFound  int64
	TotalInstancesSynced int64
	TotalErrors          int64
}

// ReconcilerConfig contains reconciler configuration
type ReconcilerConfig struct {
	Enabled        bool
	Interval       time.Duration
	MaxConcurrency int
	BatchSize      int
	RetryAttempts  int
	RetryDelay     time.Duration
	CacheTTL       time.Duration
	Debug          bool
}

// Service represents a service from the broker
type Service struct {
	ID          string
	Name        string
	Description string
	Plans       []Plan
	Tags        []string
	Metadata    map[string]interface{}
}

// Plan represents a service plan
type Plan struct {
	ID          string
	Name        string
	Description string
	Free        bool
	Metadata    map[string]interface{}
}

// VaultInterface defines the Vault operations interface
type VaultInterface interface {
	Put(path string, data interface{}) error
	Get(path string, out interface{}) (bool, error)
	Delete(path string) error
	GetIndex(name string) (*VaultIndex, error)
	UpdateIndex(name string, instanceID string, data interface{}) error
}

// VaultIndex represents a vault index
type VaultIndex struct {
	Data     map[string]interface{}
	SaveFunc func() error // Function to save the index back to vault
}

// Save saves the index
func (idx *VaultIndex) Save() error {
	if idx.SaveFunc != nil {
		return idx.SaveFunc()
	}
	return nil
}

// Lookup looks up a value in the index
func (idx *VaultIndex) Lookup(key string) (interface{}, bool) {
	val, exists := idx.Data[key]
	return val, exists
}

// Logger defines the logging interface
type Logger interface {
	Debug(format string, args ...interface{})
	Info(format string, args ...interface{})
	Warning(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// BrokerInterface defines the broker operations interface
type BrokerInterface interface {
	GetServices() []Service
}

type NotFoundError struct {
	Resource string
	ID       string
}

func (e NotFoundError) Error() string {
	return "resource not found: " + e.Resource + " " + e.ID
}

// IsNotFoundError checks if an error is a NotFoundError
func IsNotFoundError(err error) bool {
	_, ok := err.(NotFoundError)
	return ok
}
