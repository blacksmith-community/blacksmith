package boshapi

import (
	"context"
	"time"
)

// Logger interface for logging.
type Logger interface {
	Debug(msg string, fields map[string]interface{})
	Info(msg string, fields map[string]interface{})
	Warn(msg string, fields map[string]interface{})
	Error(msg string, fields map[string]interface{})
}

// AuthMode chooses how a client authenticates its requests to the director.
type AuthMode int

const (
	// AuthFromConfig, the zero value, uses the first credentials set in
	// Config, in this order: AccessToken, then ClientID and ClientSecret,
	// then Username and Password.
	AuthFromConfig AuthMode = iota
	// AuthDiscover asks the director how it authenticates. It requires
	// ClientID and ClientSecret, and it refuses AccessToken, TokenURL,
	// Username, and Password, which it would not use. The first
	// authenticated request reads GET /info. A director whose
	// user_authentication type is "uaa" gets a UAA client_credentials token
	// from user_authentication.options.url + "/oauth/token", and one whose
	// type is "basic" gets HTTP Basic with the same ID and secret.
	AuthDiscover
)

// Config represents client configuration for building a boshapi.Client.
//
// # Authentication
//
// Auth chooses how the client authenticates. With AuthFromConfig, the zero
// value, the client uses the first of these that is set:
//  1. AccessToken: a static Bearer token, which is never refreshed.
//  2. ClientID and ClientSecret: a token from the UAA client_credentials
//     grant. With TokenURL set, the token comes from there. Without it, the
//     first authenticated request reads GET /info and asks the UAA at
//     user_authentication.options.url, and a director that does not use
//     UAA is an error.
//  3. Username and Password: HTTP Basic Authentication.
//  4. Nothing: requests are sent without authentication.
//
// AuthDiscover lets the director choose between UAA and HTTP Basic for the
// same ID and secret; see AuthDiscover.
//
// Building a client sends no request. Discovery runs under the context of
// the first authenticated request, and its result is kept for the life of
// the client. A discovery that fails is not kept, so the next request tries
// again. GetInfo sends no credentials, so it never triggers discovery. A
// token request asks for no scope, so UAA grants the client's own
// authorities. A token is replaced 30 seconds before it expires, and once
// more after the director answers 401.
type Config struct {
	// Required: base URL for the BOSH Director (e.g., "https://bosh.example.com:25555").
	DirectorURL string

	// Authentication options (provide one set)
	// ClientID and ClientSecret for UAA client_credentials grant.
	ClientID     string
	ClientSecret string
	// Username and Password for HTTP Basic Authentication.
	Username string
	Password string
	// AccessToken: if set, used directly as a Bearer token.
	AccessToken string
	// CACert: PEM-encoded CA certificate or path to CA certificate file for TLS verification.
	CACert string
	// TokenURL: full OAuth2 token endpoint URL. If empty and OAuth2 auth is needed,
	// the token URL is discovered from the Director's /info endpoint on the
	// first authenticated request.
	TokenURL string
	// Auth chooses how the client authenticates. The zero value,
	// AuthFromConfig, uses the credentials set above.
	Auth AuthMode

	// Optional configurations
	// HTTPTimeout: timeout for one HTTP request, and for the director to start
	// answering it. Zero means 60 seconds.
	HTTPTimeout time.Duration
	// RetryMax: maximum number of retries for a request that is safe to
	// resend. Reads retry on connection failures, timeouts, and HTTP 429,
	// 502, 503, and 504; mutations retry only when the request never left
	// the client. Zero means one attempt and no retry. There is no implicit
	// default, so set it explicitly; 3 suits most directors.
	RetryMax int
	// RetryWaitMin: minimum backoff between retries.
	RetryWaitMin time.Duration
	// RetryWaitMax: maximum backoff between retries.
	RetryWaitMax time.Duration
	// SkipTLSVerify: if true, TLS certificate verification is skipped.
	// Only use for development environments.
	SkipTLSVerify bool
	// UserAgent: overrides the default User-Agent header.
	UserAgent string
	// Logger: optional structured logger.
	Logger Logger
	// Debug: enables verbose HTTP request/response logging.
	Debug bool
}

// DeploymentOperationsClient provides access to deployment CRUD and related operations.
type DeploymentOperationsClient interface {
	Deployments() DeploymentsClient
	Instances() InstancesClient
	VMs() VMsClient
	Resources() ResourcesClient
	Errands() ErrandsClient
	CloudCheck() CloudCheckClient
	Snapshots() SnapshotsClient
	SSHClient() SSHClient
}

// ReleasesStemcellsClient provides access to release and stemcell operations.
type ReleasesStemcellsClient interface {
	Releases() ReleasesClient
	Stemcells() StemcellsClient
	StemcellUploads() StemcellUploadsClient
	Packages() PackagesClient
}

// ConfigurationClient provides access to configuration operations.
type ConfigurationClient interface {
	Configs() ConfigsClient
	CloudConfigs() CloudConfigsClient
	RuntimeConfigs() RuntimeConfigsClient
	CPIConfigs() CPIConfigsClient
}

// DiscoveryClient provides access to service discovery operations.
type DiscoveryClient interface {
	Links() LinksClient
	LinkProviders() LinkProvidersClient
	LinkConsumers() LinkConsumersClient
}

// OrphanedResourcesClient provides access to orphaned resource management.
type OrphanedResourcesClient interface {
	Disks() DisksClient
	OrphanedVMs() OrphanedVMsClient
	Networks() NetworksClient
}

// DynamicDiskOperationsClient provides access to dynamic disk operations.
type DynamicDiskOperationsClient interface {
	DynamicDisks() DynamicDisksClient
}

// MonitoringClient provides access to monitoring and lifecycle operations.
type MonitoringClient interface {
	Tasks() TasksClient
	Events() EventsClient
	Locks() LocksClient
	Resurrection() ResurrectionClient
	Cleanup() CleanupClient
}

// VariablesDeploymentConfigsClient provides access to variables and deployment config operations.
type VariablesDeploymentConfigsClient interface {
	Variables() VariablesClient
	DeployedVariables() DeployedVariablesClient
	DeploymentConfigs() DeploymentConfigsClient
}

// ResourceClients provides access to all resource-specific clients.
type ResourceClients interface {
	DeploymentOperationsClient
	ReleasesStemcellsClient
	ConfigurationClient
	DiscoveryClient
	OrphanedResourcesClient
	DynamicDiskOperationsClient
	MonitoringClient
	VariablesDeploymentConfigsClient
}

// InfoClient provides access to BOSH Director information endpoints.
type InfoClient interface {
	// GetInfo reads the director's GET /info. It sends no credentials, as
	// the bosh CLI does, so it never triggers auth discovery, and the
	// director leaves Info.User empty.
	GetInfo(ctx context.Context) (*Info, error)
	GetCertificateExpiry(ctx context.Context) ([]CertificateExpiry, error)
}

// DirectorClient provides access to administrative Director operations.
type DirectorClient interface {
	InfoClient
}

// Client provides access to all BOSH Director API operations.
type Client interface {
	ResourceClients
	DirectorClient
}
