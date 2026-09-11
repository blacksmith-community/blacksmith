package capi

import (
	"context"
	"errors"
	"time"
)

// Static errors for err113 compliance.
var (
	ErrDeprecatedClientConstructor = errors.New("use github.com/fivetwenty-io/capi/v3/pkg/cfclient.New to create a client")
)

// CoreResourceClients provides access to core CF resource clients.
type CoreResourceClients interface {
	Apps() AppsClient
	Organizations() OrganizationsClient
	Spaces() SpacesClient
	Users() UsersClient
	Roles() RolesClient
}

// InfrastructureClients provides access to infrastructure resource clients.
type InfrastructureClients interface {
	Domains() DomainsClient
	Routes() RoutesClient
	RoutePolicies() RoutePoliciesClient
	SecurityGroups() SecurityGroupsClient
	IsolationSegments() IsolationSegmentsClient
	Stacks() StacksClient
	Routing() RoutingClient
}

// ServiceClients provides access to service-related resource clients.
type ServiceClients interface {
	ServiceBrokers() ServiceBrokersClient
	ServiceOfferings() ServiceOfferingsClient
	ServicePlans() ServicePlansClient
	ServiceInstances() ServiceInstancesClient
	ServiceCredentialBindings() ServiceCredentialBindingsClient
	ServiceRouteBindings() ServiceRouteBindingsClient
}

// BuildDeploymentClients provides access to build and deployment resource clients.
type BuildDeploymentClients interface {
	Builds() BuildsClient
	Buildpacks() BuildpacksClient
	Deployments() DeploymentsClient
	Droplets() DropletsClient
	Packages() PackagesClient
	Processes() ProcessesClient
	Tasks() TasksClient
	Sidecars() SidecarsClient
	Revisions() RevisionsClient
	Manifests() ManifestsClient
}

// ConfigurationClients provides access to configuration resource clients.
type ConfigurationClients interface {
	FeatureFlags() FeatureFlagsClient
	OrganizationQuotas() OrganizationQuotasClient
	SpaceQuotas() SpaceQuotasClient
	EnvironmentVariableGroups() EnvironmentVariableGroupsClient
}

// MonitoringClients provides access to monitoring and audit resource clients.
type MonitoringClients interface {
	Jobs() JobsClient
	AppUsageEvents() AppUsageEventsClient
	ServiceUsageEvents() ServiceUsageEventsClient
	AuditEvents() AuditEventsClient
	ResourceMatches() ResourceMatchesClient
}

// ResourceClients provides access to all resource-specific clients.
type ResourceClients interface {
	// Composite interfaces for resource groups
	CoreResourceClients
	InfrastructureClients
	ServiceClients
	BuildDeploymentClients
	ConfigurationClients
	MonitoringClients
}

// InfoClient provides access to CF API information endpoints.
type InfoClient interface {
	GetInfo(ctx context.Context) (*Info, error)
	GetRoot(ctx context.Context) (*RootInfo, error)
	GetRootInfo(ctx context.Context) (*RootInfo, error)
	GetUsageSummary(ctx context.Context) (*UsageSummary, error)
}

// AdminClient provides access to administrative operations.
type AdminClient interface {
	ClearBuildpackCache(ctx context.Context) (*Job, error)
}

type Client interface {
	// Composite interfaces for related resource groups
	ResourceClients
	InfoClient
	AdminClient
}

// Logger interface for logging.
type Logger interface {
	Debug(msg string, fields map[string]any)
	Info(msg string, fields map[string]any)
	Warn(msg string, fields map[string]any)
	Error(msg string, fields map[string]any)
}

// Config represents client configuration for building a capi.Client.
//
// # Authentication precedence
//
// The following precedence is applied by the concrete client implementation
// (see pkg/cfclient and internal/client):
//  1. AccessToken: if set, it is used directly as a static Bearer token.
//  2. AccessToken + Username/Password: token is tried first; if it expires or
//     fails with 401, the client falls back to obtaining a fresh token using
//     the password grant (useful during migrations).
//  3. ClientID/ClientSecret: uses the OAuth2 client_credentials grant. If a
//     RefreshToken, Username, or Password is also provided, the OAuth2 manager
//     can refresh or use an alternate grant as appropriate.
//  4. Username/Password: uses the OAuth2 password grant with the default CF
//     client ID ("cf").
//  5. No credentials: requests are sent without authentication.
//
// # Token URL discovery
//
// If authentication is required and TokenURL is not provided, cfclient.New will
// discover the UAA endpoint from the CF API root ("/" → links.uaa/login) and
// construct TokenURL automatically as "<uaa>/oauth/token".
//
// # Timeouts, retries, and TLS
//
// Per-request timeouts should generally be controlled via context passed to
// client methods. Retry behavior can be tuned via RetryMax/RetryWaitMin/
// RetryWaitMax. For foundations with self-signed or private-CA certificates,
// set CACertPEM (verification stays enabled). SkipTLSVerify disables
// verification entirely and is only honored when the environment variable
// CAPI_DEV_MODE is set to "true" or "1"; do not use it in production.
type Config struct {
	// Required fields
	// APIEndpoint: base URL for the CF API (e.g., "https://api.example.com").
	// cfclient.New normalizes this value by trimming a trailing slash and
	// adding "https://" if no scheme is present.
	APIEndpoint string

	// Authentication options (provide one)
	// ClientID: OAuth2 client ID for the UAA client_credentials (or other) grant.
	ClientID string
	// ClientSecret: OAuth2 client secret used with ClientID.
	ClientSecret string
	// Username: account username for the OAuth2 password grant.
	Username string
	// Password: account password for the OAuth2 password grant.
	Password string
	// RefreshToken: optional refresh token used by the OAuth2 manager to renew access tokens.
	RefreshToken string
	// AccessToken: if set, used directly as a Bearer token. When combined with
	// Username/Password, the token is tried first, then the client can fall
	// back to the password grant if a 401 is encountered.
	AccessToken string
	// TokenURL: full OAuth2 token endpoint. If empty and authentication is
	// required, cfclient.New discovers it from the API root (preferred).
	TokenURL string

	// Optional configurations
	// HTTPTimeout: optional default HTTP timeout where supported. Most client
	// calls should rely on context timeouts; this may be used by helpers.
	HTTPTimeout time.Duration
	// RetryMax: maximum number of retries for transient failures (>=500, 429,
	// and connection errors). If 0, a sensible default is used by the client.
	RetryMax int
	// RetryWaitMin: minimum backoff between retries. Applied when RetryMax > 0.
	RetryWaitMin time.Duration
	// RetryWaitMax: maximum backoff between retries. Applied when RetryMax > 0.
	RetryWaitMax time.Duration
	// Debug: enables verbose HTTP request/response logging when a Logger is provided.
	Debug bool
	// Logger: optional structured logger used by the HTTP layer and helpers.
	Logger Logger
	// CACertPEM: optional PEM-encoded CA certificate(s) appended to the
	// system roots for verifying the API and UAA endpoints. The preferred
	// way to talk to foundations with self-signed or private-CA
	// certificates: verification stays enabled. Takes precedence over
	// SkipTLSVerify.
	CACertPEM string
	// SkipTLSVerify: if true, TLS certificate verification is skipped for
	// UAA discovery, token requests, and API requests — but only when the
	// environment variable CAPI_DEV_MODE is set to "true" or "1". Intended
	// for local development; prefer CACertPEM for real foundations.
	SkipTLSVerify bool
	// UserAgent: overrides the default User-Agent header sent by the client.
	UserAgent string
	// FetchAPILinksOnInit: when true, the client fetches /v3 on initialization
	// to cache API links for nicer logs and link-aware resource clients.
	FetchAPILinksOnInit bool
}

// NewClient creates a new CF API client.
//
// Deprecated: Use github.com/fivetwenty-io/capi/v3/pkg/cfclient.New instead.
func NewClient(config *Config) (Client, error) {
	return nil, ErrDeprecatedClientConstructor
}

// Info represents the /v3/info response.
type Info struct {
	Build       string         `json:"build"       yaml:"build"`
	CLIVersion  CLIVersion     `json:"cli_version" yaml:"cli_version"`
	Custom      map[string]any `json:"custom"      yaml:"custom"`
	Description string         `json:"description" yaml:"description"`
	Name        string         `json:"name"        yaml:"name"`
	Version     int            `json:"version"     yaml:"version"`
	RateLimits  RateLimits     `json:"rate_limits" yaml:"rate_limits"`
	Links       Links          `json:"links"       yaml:"links"`
	CFOnK8s     bool           `json:"cf_on_k8s"   yaml:"cf_on_k8s"`
}

// CLIVersion represents CLI version information.
type CLIVersion struct {
	Minimum     string `json:"minimum"     yaml:"minimum"`
	Recommended string `json:"recommended" yaml:"recommended"`
}

// RateLimits represents rate limiting configuration.
type RateLimits struct {
	Enabled                bool `json:"enabled"                   yaml:"enabled"`
	GeneralLimit           int  `json:"general_limit"             yaml:"general_limit"`
	ResetIntervalInMinutes int  `json:"reset_interval_in_minutes" yaml:"reset_interval_in_minutes"`
}

// RootInfo represents the root / response.
type RootInfo struct {
	Links Links `json:"links" yaml:"links"`
}

// UsageSummary represents platform usage summary.
type UsageSummary struct {
	UsageSummary UsageSummaryData `json:"usage_summary" yaml:"usage_summary"`
	Links        Links            `json:"links"         yaml:"links"`
}

// UsageSummaryData contains the actual usage data.
type UsageSummaryData struct {
	StartedInstances int `json:"started_instances" yaml:"started_instances"`
	MemoryInMB       int `json:"memory_in_mb"      yaml:"memory_in_mb"`
}

// Job represents an asynchronous job.
type Job struct {
	Resource

	Operation string     `json:"operation"          yaml:"operation"`
	State     string     `json:"state"              yaml:"state"`
	Errors    []APIError `json:"errors,omitempty"   yaml:"errors,omitempty"`
	Warnings  []Warning  `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

// Warning represents a warning in API responses.
type Warning struct {
	Detail string `json:"detail" yaml:"detail"`
}
