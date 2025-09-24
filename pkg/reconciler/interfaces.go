package reconciler

import (
	"context"
	"time"
)

// Manager is the main interface for the reconciler.
type Manager interface {
	Start(ctx context.Context) error
	Stop() error
	ForceReconcile() error
	GetStatus() Status
}

// Scanner discovers deployments from BOSH.
type Scanner interface {
	ScanDeployments(ctx context.Context) ([]DeploymentInfo, error)
	GetDeploymentDetails(ctx context.Context, name string) (*DeploymentDetail, error)
}

// Matcher matches deployments to services.
type Matcher interface {
	MatchDeployment(deployment DeploymentDetail, services []Service) (*MatchResult, error)
}

// BindingInfo represents metadata about a service binding.
type BindingInfo struct {
	ID             string
	InstanceID     string
	ServiceID      string
	PlanID         string
	AppGUID        string
	CredentialType string
	CreatedAt      time.Time
	LastVerified   time.Time
	Status         string
}

// Updater updates instance data in Vault.
type Updater interface {
	UpdateInstance(ctx context.Context, instance InstanceData) (*InstanceData, error)
	UpdateBatch(ctx context.Context, instances []InstanceData) ([]InstanceData, error)
	CheckBindingHealth(instanceID string) ([]BindingInfo, []string, error)
	ReconstructBindingWithBroker(instanceID, bindingID string, broker BrokerInterface) error
	RepairInstanceBindings(instanceID string, broker BrokerInterface) error
	UpdateInstanceWithBindingRepair(ctx context.Context, instance InstanceData, broker BrokerInterface) (*InstanceData, error)
}

// Synchronizer synchronizes the vault index.
type Synchronizer interface {
	SyncIndex(ctx context.Context, instances []InstanceData) error
}

// Logger provides logging functionality.
type Logger interface {
	Debugf(format string, args ...interface{})
	Infof(format string, args ...interface{})
	Warningf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

// MetricsCollector collects reconciler metrics.
type MetricsCollector interface {
	ReconciliationStarted()
	ReconciliationCompleted(duration time.Duration)
	ReconciliationError(err error)
	ReconciliationSkipped()
	DeploymentsScanned(count int)
	InstancesMatched(count int)
	InstancesUpdated(count int)
	Collect()
}

// BrokerInterface defines the broker interface.
type BrokerInterface interface {
	GetServices() []Service
	// Provide reconstructed binding credentials using the broker's logic
	GetBindingCredentials(instanceID, bindingID string) (*BindingCredentials, error)
}

// CFManagerInterface is defined in credential_vcap_recovery.go

// Status represents the reconciler status.
type Status struct {
	Running         bool
	LastRunTime     time.Time
	LastRunDuration time.Duration
	InstancesFound  int
	InstancesSynced int
	Errors          []error
}

// DeploymentInfo contains basic deployment information.
type DeploymentInfo struct {
	Name        string
	UUID        string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Stemcells   []string
	Releases    []string
	CloudConfig string
}

// DeploymentDetail contains detailed deployment information.
type DeploymentDetail struct {
	DeploymentInfo

	Manifest   string
	Properties map[string]interface{}
	Networks   []string
	VMs        []VMInfo

	// Deployment status tracking
	NotFound       bool   // True if deployment was not found in BOSH (404)
	NotFoundReason string // Reason for not found status
}

// VMInfo contains VM information.
type VMInfo struct {
	Name  string
	State string
	IPs   []string
	AZ    string
}

// MatchResult contains the result of matching a deployment.
type MatchResult struct {
	ServiceID   string
	PlanID      string
	InstanceID  string
	Confidence  float64
	MatchReason string
}

// InstanceData contains complete instance information.
type InstanceData struct {
	ID         string
	ServiceID  string
	PlanID     string
	Deployment DeploymentDetail
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Metadata   map[string]interface{}
}

// Service represents a service in the catalog.
type Service struct {
	ID          string
	Name        string
	Description string
	Plans       []Plan
}

// Plan represents a service plan.
type Plan struct {
	ID          string
	Name        string
	Description string
	Properties  map[string]interface{}
}

// CFServiceInstanceDetails contains CF service instance details.
type CFServiceInstanceDetails struct {
	GUID            string
	Name            string
	ServiceID       string
	PlanID          string
	OrganizationID  string
	SpaceID         string
	DashboardURL    string
	Type            string
	Tags            []string
	LastOperation   string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	MaintenanceInfo map[string]interface{}
}

// HistoryRetentionConfig holds configuration for history retention policy.
type HistoryRetentionConfig struct {
	Enabled       bool
	RetentionDays int
	MaxEntries    int
}

// BackupConfig contains backup configuration.
type BackupConfig struct {
	Enabled          bool
	RetentionCount   int
	RetentionDays    int
	CompressionLevel int
	CleanupEnabled   bool
	BackupOnUpdate   bool
	BackupOnDelete   bool
}

// VaultInterface defines the vault interface.
type VaultInterface interface {
	Get(path string) (map[string]interface{}, error)
	Put(path string, secret map[string]interface{}) error
	GetSecret(path string) (map[string]interface{}, error)
	SetSecret(path string, secret map[string]interface{}) error
	DeleteSecret(path string) error
	ListSecrets(path string) ([]string, error)
}

// BindingCredentials represents credentials for a service binding.
type BindingCredentials struct {
	Host            string
	Port            int
	Username        string
	Password        string
	CredentialType  string
	ReconstructedAt string
	Raw             map[string]interface{}
}

// Metrics contains reconciler metrics.
type Metrics struct {
	ReconciliationRuns     int64
	ReconciliationFailures int64
	InstancesProcessed     int64
	InstancesUpdated       int64
	InstancesFailed        int64
	LastRunTime            time.Time
	LastRunDuration        time.Duration
}
