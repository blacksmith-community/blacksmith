package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"os"
	"time"

	"github.com/fivetwenty-io/capi/v3/internal/auth"
	"github.com/fivetwenty-io/capi/v3/internal/constants"
	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// Client implements the capi.Client interface.
// Static errors for err113 compliance.
var (
	ErrAPIEndpointRequired      = errors.New("API endpoint is required")
	ErrNoTokenManagerConfigured = errors.New("no token manager configured")
	ErrStaticTokenCannotRefresh = errors.New("static token cannot be refreshed")
)

// devModeEnabled reports whether the CAPI_DEV_MODE gate allows
// development-only behavior such as skipping TLS verification.
func devModeEnabled() bool {
	devMode := os.Getenv("CAPI_DEV_MODE")

	return devMode == "true" || devMode == "1"
}

// validateTLSSettings fails fast on TLS misconfiguration: a CACertPEM
// that parses to zero certificates, or SkipTLSVerify requested without
// the CAPI_DEV_MODE gate (which would otherwise silently degrade into
// an opaque x509 error on the first request).
func validateTLSSettings(config *capi.Config) error {
	if config.CACertPEM != "" {
		if !x509.NewCertPool().AppendCertsFromPEM([]byte(config.CACertPEM)) {
			return capi.ErrInvalidCACertPEM
		}

		return nil
	}

	if config.SkipTLSVerify && !devModeEnabled() {
		return fmt.Errorf("%w (set CAPI_DEV_MODE=true)", capi.ErrSkipTLSOnlyInDev)
	}

	return nil
}

type Client struct {
	httpClient   *http.Client
	tokenManager auth.TokenManager
	baseURL      string
	logger       capi.Logger
	apiLinks     map[string]string

	// Resource clients
	apps                      capi.AppsClient
	organizations             capi.OrganizationsClient
	spaces                    capi.SpacesClient
	domains                   capi.DomainsClient
	routes                    capi.RoutesClient
	routePolicies             capi.RoutePoliciesClient
	serviceBrokers            capi.ServiceBrokersClient
	serviceOfferings          capi.ServiceOfferingsClient
	servicePlans              capi.ServicePlansClient
	serviceInstances          capi.ServiceInstancesClient
	serviceCredentialBindings capi.ServiceCredentialBindingsClient
	serviceRouteBindings      capi.ServiceRouteBindingsClient
	builds                    capi.BuildsClient
	buildpacks                capi.BuildpacksClient
	deployments               capi.DeploymentsClient
	droplets                  capi.DropletsClient
	packages                  capi.PackagesClient
	processes                 capi.ProcessesClient
	tasks                     capi.TasksClient
	stacks                    capi.StacksClient
	users                     capi.UsersClient
	roles                     capi.RolesClient
	securityGroups            capi.SecurityGroupsClient
	isolationSegments         capi.IsolationSegmentsClient
	featureFlags              capi.FeatureFlagsClient
	jobs                      capi.JobsClient
	organizationQuotas        capi.OrganizationQuotasClient
	spaceQuotas               capi.SpaceQuotasClient
	sidecars                  capi.SidecarsClient
	revisions                 capi.RevisionsClient
	environmentVariableGroups capi.EnvironmentVariableGroupsClient
	appUsageEvents            capi.AppUsageEventsClient
	serviceUsageEvents        capi.ServiceUsageEventsClient
	auditEvents               capi.AuditEventsClient
	resourceMatches           capi.ResourceMatchesClient
	manifests                 capi.ManifestsClient
	routing                   capi.RoutingClient
}

// customTLSConfig returns a TLS config honoring the Config's trust
// settings, or nil when no adjustment was requested. CACertPEM — verify
// against the provided CA in addition to system roots — takes precedence
// over SkipTLSVerify, which disables verification entirely and is only
// honored when the CAPI_DEV_MODE environment gate allows it (the same
// gate UAA discovery uses).
func customTLSConfig(config *capi.Config) *tls.Config {
	if config.CACertPEM != "" {
		pool, err := x509.SystemCertPool()
		if pool == nil || err != nil {
			pool = x509.NewCertPool()
		}

		pool.AppendCertsFromPEM([]byte(config.CACertPEM))

		return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}

	if !config.SkipTLSVerify {
		return nil
	}

	if !devModeEnabled() {
		return nil
	}

	return &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} // #nosec G402 -- gated by CAPI_DEV_MODE
}

// customTLSHTTPClient wraps customTLSConfig in an HTTP client; nil when
// no TLS adjustment was requested. The transport is cloned from
// http.DefaultTransport so proxy support, HTTP/2, and dial/handshake
// timeouts match the default client path; no overall Client.Timeout is
// set, matching retryablehttp's default (token-manager callers add
// their own).
func customTLSHTTPClient(config *capi.Config) *nethttp.Client {
	tlsConfig := customTLSConfig(config)
	if tlsConfig == nil {
		return nil
	}

	transport, ok := nethttp.DefaultTransport.(*nethttp.Transport)
	if !ok {
		transport = &nethttp.Transport{}
	} else {
		transport = transport.Clone()
	}

	transport.TLSClientConfig = tlsConfig

	return &nethttp.Client{Transport: transport}
}

// tokenHTTPClient is customTLSHTTPClient plus the bounded timeout the
// OAuth manager's default client uses for token requests.
func tokenHTTPClient(config *capi.Config) *nethttp.Client {
	client := customTLSHTTPClient(config)
	if client != nil {
		client.Timeout = constants.DefaultHTTPTimeout
	}

	return client
}

// createTokenManager creates appropriate token manager based on config.
func createTokenManager(config *capi.Config) auth.TokenManager {
	if config.AccessToken != "" && config.Username != "" && config.Password != "" {
		return createFallbackTokenManager(config)
	}

	if config.AccessToken != "" {
		return &staticTokenManager{token: config.AccessToken}
	}

	if config.ClientID != "" && config.ClientSecret != "" {
		return createOAuth2TokenManager(config)
	}

	if config.Username != "" && config.Password != "" {
		return createPasswordTokenManager(config)
	}

	return nil // No authentication
}

// createFallbackTokenManager creates a fallback token manager that tries access token first.
func createFallbackTokenManager(config *capi.Config) auth.TokenManager {
	tokenURL := getTokenURL(config)

	oauthConfig := &auth.OAuth2Config{
		TokenURL:     tokenURL,
		ClientID:     "cf", // Default CF CLI client ID
		ClientSecret: "",
		Username:     config.Username,
		Password:     config.Password,
		HTTPClient:   tokenHTTPClient(config),
	}

	oauthManager := auth.NewOAuth2TokenManager(oauthConfig)

	return &fallbackTokenManager{
		staticToken:  config.AccessToken,
		oauthManager: oauthManager,
	}
}

// createOAuth2TokenManager creates OAuth2 token manager with client credentials or password.
func createOAuth2TokenManager(config *capi.Config) auth.TokenManager {
	tokenURL := getTokenURL(config)

	oauthConfig := &auth.OAuth2Config{
		TokenURL:     tokenURL,
		ClientID:     config.ClientID,
		ClientSecret: config.ClientSecret,
		Username:     config.Username,
		Password:     config.Password,
		RefreshToken: config.RefreshToken,
		HTTPClient:   tokenHTTPClient(config),
	}

	return auth.NewOAuth2TokenManager(oauthConfig)
}

// createPasswordTokenManager creates password grant token manager with default client.
func createPasswordTokenManager(config *capi.Config) auth.TokenManager {
	tokenURL := getTokenURL(config)

	oauthConfig := &auth.OAuth2Config{
		TokenURL:     tokenURL,
		ClientID:     "cf", // Default CF CLI client ID
		ClientSecret: "",
		Username:     config.Username,
		Password:     config.Password,
		HTTPClient:   tokenHTTPClient(config),
	}

	return auth.NewOAuth2TokenManager(oauthConfig)
}

// getTokenURL returns token URL from config or fallback.
func getTokenURL(config *capi.Config) string {
	if config.TokenURL != "" {
		return config.TokenURL
	}

	return config.APIEndpoint + "/oauth/token" // Fallback, but should be discovered
}

// createHTTPClientOptions builds HTTP client options from config.
func createHTTPClientOptions(config *capi.Config) []http.Option {
	var httpOpts []http.Option

	if config.Logger != nil {
		httpOpts = append(httpOpts, http.WithLogger(&loggerAdapter{logger: config.Logger}))
	}

	if config.Debug {
		httpOpts = append(httpOpts, http.WithDebug(true))
	}

	if config.UserAgent != "" {
		httpOpts = append(httpOpts, http.WithUserAgent(config.UserAgent))
	}

	if tlsClient := customTLSHTTPClient(config); tlsClient != nil {
		httpOpts = append(httpOpts, http.WithHTTPClient(tlsClient))
	}

	if config.RetryMax > 0 {
		retryWaitMin := 1 * time.Second
		retryWaitMax := constants.ExtendedRetryWaitMax

		if config.RetryWaitMin > 0 {
			retryWaitMin = config.RetryWaitMin
		}

		if config.RetryWaitMax > 0 {
			retryWaitMax = config.RetryWaitMax
		}

		httpOpts = append(httpOpts, http.WithRetryConfig(config.RetryMax, retryWaitMin, retryWaitMax))
	}

	return httpOpts
}

func New(ctx context.Context, config *capi.Config) (*Client, error) {
	if config.APIEndpoint == "" {
		return nil, ErrAPIEndpointRequired
	}

	err := validateTLSSettings(config)
	if err != nil {
		return nil, err
	}

	// Create token manager based on available credentials
	tokenManager := createTokenManager(config)

	// Create HTTP client options
	httpOpts := createHTTPClientOptions(config)

	// Create HTTP client
	httpClient := http.NewClient(config.APIEndpoint, tokenManager, httpOpts...)

	client := &Client{
		httpClient:   httpClient,
		tokenManager: tokenManager,
		baseURL:      config.APIEndpoint,
		logger:       config.Logger,
	}

	// Initialize resource clients
	client.initializeResourceClients()

	// Fetch API links if requested
	if config.FetchAPILinksOnInit {
		_ = client.FetchAPILinks(ctx) // Ignore error as it's optional
	}

	return client, nil
}

// NewWithTokenManager creates a new CF API client with a custom token manager.
func NewWithTokenManager(config *capi.Config, tokenManager auth.TokenManager) (*Client, error) {
	if config.APIEndpoint == "" {
		return nil, ErrAPIEndpointRequired
	}

	err := validateTLSSettings(config)
	if err != nil {
		return nil, err
	}

	// Create HTTP client options
	httpOpts := createHTTPClientOptions(config)

	// Create HTTP client with the provided token manager
	httpClient := http.NewClient(config.APIEndpoint, tokenManager, httpOpts...)

	client := &Client{
		httpClient:   httpClient,
		tokenManager: tokenManager,
		baseURL:      config.APIEndpoint,
		logger:       config.Logger,
	}

	// Initialize resource clients
	client.initializeResourceClients()

	// Fetch API links if requested
	if config.FetchAPILinksOnInit {
		ctx := context.Background()
		_ = client.FetchAPILinks(ctx) // Ignore error as it's optional
	}

	return client, nil
}

// GetTokenManager returns the token manager for this client.
func (c *Client) GetTokenManager() auth.TokenManager {
	return c.tokenManager
}

// FetchAPILinks fetches and caches API links from /v3.
func (c *Client) FetchAPILinks(ctx context.Context) error {
	rootInfo, err := c.GetRootInfo(ctx)
	if err != nil {
		return fmt.Errorf("fetching API links: %w", err)
	}

	if rootInfo.Links != nil {
		apiLinks := make(map[string]string)
		for key, link := range rootInfo.Links {
			apiLinks[key] = link.Href
		}

		c.apiLinks = apiLinks
		// Re-initialize apps client with API links
		c.apps = NewAppsClientWithLinks(c.httpClient, apiLinks)
	}

	return nil
}

// GetInfo implements capi.Client.GetInfo.
func (c *Client) GetInfo(ctx context.Context) (*capi.Info, error) {
	resp, err := c.httpClient.Get(ctx, "/v3/info", nil)
	if err != nil {
		return nil, fmt.Errorf("getting info: %w", err)
	}

	var info capi.Info

	err = json.Unmarshal(resp.Body, &info)
	if err != nil {
		return nil, fmt.Errorf("parsing info response: %w", err)
	}

	return &info, nil
}

// GetRoot implements capi.Client.GetRoot.
// It fetches the CF API root (/) which contains platform-level links
// such as app_ssh, login, uaa, cloud_controller_v2, and cloud_controller_v3.
func (c *Client) GetRoot(ctx context.Context) (*capi.RootInfo, error) {
	resp, err := c.httpClient.Get(ctx, "/", nil)
	if err != nil {
		return nil, fmt.Errorf("getting root: %w", err)
	}

	var root capi.RootInfo

	err = json.Unmarshal(resp.Body, &root)
	if err != nil {
		return nil, fmt.Errorf("parsing root response: %w", err)
	}

	return &root, nil
}

// GetRootInfo implements capi.Client.GetRootInfo.
func (c *Client) GetRootInfo(ctx context.Context) (*capi.RootInfo, error) {
	resp, err := c.httpClient.Get(ctx, "/v3", nil)
	if err != nil {
		return nil, fmt.Errorf("getting root info: %w", err)
	}

	var rootInfo capi.RootInfo

	err = json.Unmarshal(resp.Body, &rootInfo)
	if err != nil {
		return nil, fmt.Errorf("parsing root info response: %w", err)
	}

	return &rootInfo, nil
}

// GetUsageSummary implements capi.Client.GetUsageSummary.
func (c *Client) GetUsageSummary(ctx context.Context) (*capi.UsageSummary, error) {
	resp, err := c.httpClient.Get(ctx, "/v3/info/usage_summary", nil)
	if err != nil {
		return nil, fmt.Errorf("getting usage summary: %w", err)
	}

	var summary capi.UsageSummary

	err = json.Unmarshal(resp.Body, &summary)
	if err != nil {
		return nil, fmt.Errorf("parsing usage summary response: %w", err)
	}

	return &summary, nil
}

// ClearBuildpackCache implements capi.Client.ClearBuildpackCache.
func (c *Client) ClearBuildpackCache(ctx context.Context) (*capi.Job, error) {
	resp, err := c.httpClient.Post(ctx, "/v3/admin/actions/clear_buildpack_cache", nil)
	if err != nil {
		return nil, fmt.Errorf("clearing buildpack cache: %w", err)
	}

	// Async: job in body or Location header.
	return jobFromAsyncResponse(resp, "clearing buildpack cache")
}

// Resource client accessors

// Apps implements capi.Client.Apps.
func (c *Client) Apps() capi.AppsClient {
	return c.apps
}

// Organizations implements capi.Client.Organizations.
func (c *Client) Organizations() capi.OrganizationsClient {
	return c.organizations
}

// Spaces implements capi.Client.Spaces.
func (c *Client) Spaces() capi.SpacesClient {
	return c.spaces
}

// Domains implements capi.Client.Domains.
func (c *Client) Domains() capi.DomainsClient {
	return c.domains
}

// Routes implements capi.Client.Routes.
func (c *Client) Routes() capi.RoutesClient {
	return c.routes
}

// RoutePolicies implements capi.Client.RoutePolicies.
func (c *Client) RoutePolicies() capi.RoutePoliciesClient {
	return c.routePolicies
}

// ServiceBrokers implements capi.Client.ServiceBrokers.
func (c *Client) ServiceBrokers() capi.ServiceBrokersClient {
	return c.serviceBrokers
}

// ServiceOfferings implements capi.Client.ServiceOfferings.
func (c *Client) ServiceOfferings() capi.ServiceOfferingsClient {
	return c.serviceOfferings
}

// ServicePlans implements capi.Client.ServicePlans.
func (c *Client) ServicePlans() capi.ServicePlansClient {
	return c.servicePlans
}

// ServiceInstances implements capi.Client.ServiceInstances.
func (c *Client) ServiceInstances() capi.ServiceInstancesClient {
	return c.serviceInstances
}

// ServiceCredentialBindings implements capi.Client.ServiceCredentialBindings.
func (c *Client) ServiceCredentialBindings() capi.ServiceCredentialBindingsClient {
	return c.serviceCredentialBindings
}

// ServiceRouteBindings implements capi.Client.ServiceRouteBindings.
func (c *Client) ServiceRouteBindings() capi.ServiceRouteBindingsClient {
	return c.serviceRouteBindings
}

// Builds implements capi.Client.Builds.
func (c *Client) Builds() capi.BuildsClient {
	return c.builds
}

// Buildpacks implements capi.Client.Buildpacks.
func (c *Client) Buildpacks() capi.BuildpacksClient {
	return c.buildpacks
}

// Deployments implements capi.Client.Deployments.
func (c *Client) Deployments() capi.DeploymentsClient {
	return c.deployments
}

// Droplets implements capi.Client.Droplets.
func (c *Client) Droplets() capi.DropletsClient {
	return c.droplets
}

// Packages implements capi.Client.Packages.
func (c *Client) Packages() capi.PackagesClient {
	return c.packages
}

// Processes implements capi.Client.Processes.
func (c *Client) Processes() capi.ProcessesClient {
	return c.processes
}

// Tasks implements capi.Client.Tasks.
func (c *Client) Tasks() capi.TasksClient {
	return c.tasks
}

// Stacks implements capi.Client.Stacks.
func (c *Client) Stacks() capi.StacksClient {
	return c.stacks
}

// Users implements capi.Client.Users.
func (c *Client) Users() capi.UsersClient {
	return c.users
}

// Roles implements capi.Client.Roles.
func (c *Client) Roles() capi.RolesClient {
	return c.roles
}

// SecurityGroups implements capi.Client.SecurityGroups.
func (c *Client) SecurityGroups() capi.SecurityGroupsClient {
	return c.securityGroups
}

// IsolationSegments implements capi.Client.IsolationSegments.
func (c *Client) IsolationSegments() capi.IsolationSegmentsClient {
	return c.isolationSegments
}

// FeatureFlags implements capi.Client.FeatureFlags.
func (c *Client) FeatureFlags() capi.FeatureFlagsClient {
	return c.featureFlags
}

// Jobs implements capi.Client.Jobs.
func (c *Client) Jobs() capi.JobsClient {
	return c.jobs
}

// OrganizationQuotas implements capi.Client.OrganizationQuotas.
func (c *Client) OrganizationQuotas() capi.OrganizationQuotasClient {
	return c.organizationQuotas
}

// SpaceQuotas implements capi.Client.SpaceQuotas.
func (c *Client) SpaceQuotas() capi.SpaceQuotasClient {
	return c.spaceQuotas
}

// Sidecars implements capi.Client.Sidecars.
func (c *Client) Sidecars() capi.SidecarsClient {
	return c.sidecars
}

// Revisions implements capi.Client.Revisions.
func (c *Client) Revisions() capi.RevisionsClient {
	return c.revisions
}

// EnvironmentVariableGroups implements capi.Client.EnvironmentVariableGroups.
func (c *Client) EnvironmentVariableGroups() capi.EnvironmentVariableGroupsClient {
	return c.environmentVariableGroups
}

// AppUsageEvents implements capi.Client.AppUsageEvents.
func (c *Client) AppUsageEvents() capi.AppUsageEventsClient {
	return c.appUsageEvents
}

// ServiceUsageEvents implements capi.Client.ServiceUsageEvents.
func (c *Client) ServiceUsageEvents() capi.ServiceUsageEventsClient {
	return c.serviceUsageEvents
}

// AuditEvents implements capi.Client.AuditEvents.
func (c *Client) AuditEvents() capi.AuditEventsClient {
	return c.auditEvents
}

// ResourceMatches implements capi.Client.ResourceMatches.
func (c *Client) ResourceMatches() capi.ResourceMatchesClient {
	return c.resourceMatches
}

// Manifests implements capi.Client.Manifests.
func (c *Client) Manifests() capi.ManifestsClient {
	return c.manifests
}

// Routing implements capi.Client.Routing.
func (c *Client) Routing() capi.RoutingClient {
	return c.routing
}

// GetToken returns the current access token from the token manager.
func (c *Client) GetToken(ctx context.Context) (string, error) {
	if c.tokenManager == nil {
		return "", ErrNoTokenManagerConfigured
	}

	token, err := c.tokenManager.GetToken(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get token: %w", err)
	}

	return token, nil
}

// initializeResourceClients initializes all resource-specific clients.
func (c *Client) initializeResourceClients() {
	c.organizations = NewOrganizationsClient(c.httpClient)
	c.spaces = NewSpacesClient(c.httpClient)
	c.apps = NewAppsClient(c.httpClient)
	c.processes = NewProcessesClient(c.httpClient)
	c.tasks = NewTasksClient(c.httpClient)
	c.packages = NewPackagesClient(c.httpClient)
	c.droplets = NewDropletsClient(c.httpClient)
	c.builds = NewBuildsClient(c.httpClient)
	c.buildpacks = NewBuildpacksClient(c.httpClient)
	c.deployments = NewDeploymentsClient(c.httpClient)
	c.domains = NewDomainsClient(c.httpClient)
	c.routes = NewRoutesClient(c.httpClient)
	c.routePolicies = NewRoutePoliciesClient(c.httpClient)
	c.serviceBrokers = NewServiceBrokersClient(c.httpClient)
	c.serviceOfferings = NewServiceOfferingsClient(c.httpClient)
	c.servicePlans = NewServicePlansClient(c.httpClient)
	c.serviceInstances = NewServiceInstancesClient(c.httpClient)
	c.serviceCredentialBindings = NewServiceCredentialBindingsClient(c.httpClient)
	c.serviceRouteBindings = NewServiceRouteBindingsClient(c.httpClient)
	c.stacks = NewStacksClient(c.httpClient)
	c.users = NewUsersClient(c.httpClient)
	c.roles = NewRolesClient(c.httpClient)
	c.securityGroups = NewSecurityGroupsClient(c.httpClient)
	c.isolationSegments = NewIsolationSegmentsClient(c.httpClient)
	c.featureFlags = NewFeatureFlagsClient(c.httpClient)
	c.jobs = NewJobsClient(c.httpClient)
	c.organizationQuotas = NewOrganizationQuotasClient(c.httpClient)
	c.spaceQuotas = NewSpaceQuotasClient(c.httpClient)
	c.sidecars = NewSidecarsClient(c.httpClient)
	c.revisions = NewRevisionsClient(c.httpClient)
	c.environmentVariableGroups = NewEnvironmentVariableGroupsClient(c.httpClient)
	c.appUsageEvents = NewAppUsageEventsClient(c.httpClient)
	c.serviceUsageEvents = NewServiceUsageEventsClient(c.httpClient)
	c.auditEvents = NewAuditEventsClient(c.httpClient)
	c.resourceMatches = NewResourceMatchesClient(c.httpClient)
	c.manifests = NewManifestsClient(c.httpClient)
	c.routing = NewRoutingClient(c.httpClient)
}

// staticTokenManager provides a static token.
type staticTokenManager struct {
	token string
}

func (m *staticTokenManager) GetToken(ctx context.Context) (string, error) {
	return m.token, nil
}

func (m *staticTokenManager) RefreshToken(ctx context.Context) error {
	return ErrStaticTokenCannotRefresh
}

func (m *staticTokenManager) SetToken(token string, expiresAt time.Time) {
	m.token = token
}

// loggerAdapter adapts capi.Logger to http.Logger.
type loggerAdapter struct {
	logger capi.Logger
}

func (l *loggerAdapter) Debug(msg string, fields map[string]any) {
	l.logger.Debug(msg, fields)
}

func (l *loggerAdapter) Info(msg string, fields map[string]any) {
	l.logger.Info(msg, fields)
}

func (l *loggerAdapter) Warn(msg string, fields map[string]any) {
	l.logger.Warn(msg, fields)
}

func (l *loggerAdapter) Error(msg string, fields map[string]any) {
	l.logger.Error(msg, fields)
}

// fallbackTokenManager tries static token first, then falls back to OAuth2.
type fallbackTokenManager struct {
	staticToken      string
	oauthManager     auth.TokenManager
	usingOAuth       bool
	staticTokenTried bool
}

func (m *fallbackTokenManager) GetToken(ctx context.Context) (string, error) {
	// If we're already using OAuth (static token failed), continue with OAuth
	if m.usingOAuth {
		token, err := m.oauthManager.GetToken(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get OAuth token: %w", err)
		}

		return token, nil
	}

	// Try static token first, but only if we haven't tried it yet
	if m.staticToken != "" && !m.staticTokenTried {
		m.staticTokenTried = true

		return m.staticToken, nil
	}

	// Fall back to OAuth
	m.usingOAuth = true

	token, err := m.oauthManager.GetToken(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get OAuth token: %w", err)
	}

	return token, nil
}

func (m *fallbackTokenManager) RefreshToken(ctx context.Context) error {
	// If static token needs refresh, switch to OAuth and get a fresh token
	if !m.usingOAuth {
		m.usingOAuth = true
		// Get a fresh token instead of trying to refresh the static one
		_, err := m.oauthManager.GetToken(ctx)
		if err != nil {
			return fmt.Errorf("failed to get OAuth token during refresh: %w", err)
		}

		return nil
	}

	err := m.oauthManager.RefreshToken(ctx)
	if err != nil {
		return fmt.Errorf("failed to refresh OAuth token: %w", err)
	}

	return nil
}

func (m *fallbackTokenManager) SetToken(token string, expiresAt time.Time) {
	if m.usingOAuth {
		m.oauthManager.SetToken(token, expiresAt)
	} else {
		m.staticToken = token
	}
}
