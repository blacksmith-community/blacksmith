package client

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fivetwenty-io/capi/v3/internal/auth"
	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// Client implements the capi.Client interface
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
}

// New creates a new CF API client
func New(config *capi.Config) (*Client, error) {
	if config.APIEndpoint == "" {
		return nil, fmt.Errorf("API endpoint is required")
	}

	// Create token manager based on available credentials
	var tokenManager auth.TokenManager

	if config.AccessToken != "" && config.Username != "" && config.Password != "" {
		// Use fallback token manager: try access token first, then username/password
		tokenURL := config.TokenURL
		if tokenURL == "" {
			tokenURL = config.APIEndpoint + "/oauth/token" // Fallback, but should be discovered
		}
		oauthConfig := &auth.OAuth2Config{
			TokenURL:     tokenURL,
			ClientID:     "cf", // Default CF CLI client ID
			ClientSecret: "",
			Username:     config.Username,
			Password:     config.Password,
		}
		oauthManager := auth.NewOAuth2TokenManager(oauthConfig)
		tokenManager = &fallbackTokenManager{
			staticToken:  config.AccessToken,
			oauthManager: oauthManager,
		}
	} else if config.AccessToken != "" {
		// Use provided access token only
		tokenManager = &staticTokenManager{token: config.AccessToken}
	} else if config.ClientID != "" && config.ClientSecret != "" {
		// Use OAuth2 with client credentials or password
		tokenURL := config.TokenURL
		if tokenURL == "" {
			tokenURL = config.APIEndpoint + "/oauth/token" // Fallback, but should be discovered
		}
		oauthConfig := &auth.OAuth2Config{
			TokenURL:     tokenURL,
			ClientID:     config.ClientID,
			ClientSecret: config.ClientSecret,
			Username:     config.Username,
			Password:     config.Password,
			RefreshToken: config.RefreshToken,
		}
		tokenManager = auth.NewOAuth2TokenManager(oauthConfig)
	} else if config.Username != "" && config.Password != "" {
		// Use password grant with default client
		tokenURL := config.TokenURL
		if tokenURL == "" {
			tokenURL = config.APIEndpoint + "/oauth/token" // Fallback, but should be discovered
		}
		oauthConfig := &auth.OAuth2Config{
			TokenURL:     tokenURL,
			ClientID:     "cf", // Default CF CLI client ID
			ClientSecret: "",
			Username:     config.Username,
			Password:     config.Password,
		}
		tokenManager = auth.NewOAuth2TokenManager(oauthConfig)
	} else {
		// No authentication
		tokenManager = nil
	}

	// Create HTTP client options
	httpOpts := []http.Option{}

	if config.Logger != nil {
		httpOpts = append(httpOpts, http.WithLogger(&loggerAdapter{logger: config.Logger}))
	}

	if config.Debug {
		httpOpts = append(httpOpts, http.WithDebug(true))
	}

	if config.UserAgent != "" {
		httpOpts = append(httpOpts, http.WithUserAgent(config.UserAgent))
	}

	if config.RetryMax > 0 {
		retryWaitMin := 1 * time.Second
		retryWaitMax := 30 * time.Second
		if config.RetryWaitMin > 0 {
			retryWaitMin = config.RetryWaitMin
		}
		if config.RetryWaitMax > 0 {
			retryWaitMax = config.RetryWaitMax
		}
		httpOpts = append(httpOpts, http.WithRetryConfig(config.RetryMax, retryWaitMin, retryWaitMax))
	}

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
		ctx := context.Background()
		_ = client.FetchAPILinks(ctx) // Ignore error as it's optional
	}

	return client, nil
}

// FetchAPILinks fetches and caches API links from /v3
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

// initializeResourceClients initializes all resource-specific clients
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
}

// GetInfo implements capi.Client.GetInfo
func (c *Client) GetInfo(ctx context.Context) (*capi.Info, error) {
	resp, err := c.httpClient.Get(ctx, "/v3/info", nil)
	if err != nil {
		return nil, fmt.Errorf("getting info: %w", err)
	}

	var info capi.Info
	if err := json.Unmarshal(resp.Body, &info); err != nil {
		return nil, fmt.Errorf("parsing info response: %w", err)
	}

	return &info, nil
}

// GetRootInfo implements capi.Client.GetRootInfo
func (c *Client) GetRootInfo(ctx context.Context) (*capi.RootInfo, error) {
	resp, err := c.httpClient.Get(ctx, "/v3", nil)
	if err != nil {
		return nil, fmt.Errorf("getting root info: %w", err)
	}

	var rootInfo capi.RootInfo
	if err := json.Unmarshal(resp.Body, &rootInfo); err != nil {
		return nil, fmt.Errorf("parsing root info response: %w", err)
	}

	return &rootInfo, nil
}

// GetUsageSummary implements capi.Client.GetUsageSummary
func (c *Client) GetUsageSummary(ctx context.Context) (*capi.UsageSummary, error) {
	resp, err := c.httpClient.Get(ctx, "/v3/info/usage_summary", nil)
	if err != nil {
		return nil, fmt.Errorf("getting usage summary: %w", err)
	}

	var summary capi.UsageSummary
	if err := json.Unmarshal(resp.Body, &summary); err != nil {
		return nil, fmt.Errorf("parsing usage summary response: %w", err)
	}

	return &summary, nil
}

// ClearBuildpackCache implements capi.Client.ClearBuildpackCache
func (c *Client) ClearBuildpackCache(ctx context.Context) (*capi.Job, error) {
	resp, err := c.httpClient.Post(ctx, "/v3/admin/actions/clear_buildpack_cache", nil)
	if err != nil {
		return nil, fmt.Errorf("clearing buildpack cache: %w", err)
	}

	var job capi.Job
	if err := json.Unmarshal(resp.Body, &job); err != nil {
		return nil, fmt.Errorf("parsing job response: %w", err)
	}

	return &job, nil
}

// Resource client accessors

// Apps implements capi.Client.Apps
func (c *Client) Apps() capi.AppsClient {
	return c.apps
}

// Organizations implements capi.Client.Organizations
func (c *Client) Organizations() capi.OrganizationsClient {
	return c.organizations
}

// Spaces implements capi.Client.Spaces
func (c *Client) Spaces() capi.SpacesClient {
	return c.spaces
}

// Domains implements capi.Client.Domains
func (c *Client) Domains() capi.DomainsClient {
	return c.domains
}

// Routes implements capi.Client.Routes
func (c *Client) Routes() capi.RoutesClient {
	return c.routes
}

// ServiceBrokers implements capi.Client.ServiceBrokers
func (c *Client) ServiceBrokers() capi.ServiceBrokersClient {
	return c.serviceBrokers
}

// ServiceOfferings implements capi.Client.ServiceOfferings
func (c *Client) ServiceOfferings() capi.ServiceOfferingsClient {
	return c.serviceOfferings
}

// ServicePlans implements capi.Client.ServicePlans
func (c *Client) ServicePlans() capi.ServicePlansClient {
	return c.servicePlans
}

// ServiceInstances implements capi.Client.ServiceInstances
func (c *Client) ServiceInstances() capi.ServiceInstancesClient {
	return c.serviceInstances
}

// ServiceCredentialBindings implements capi.Client.ServiceCredentialBindings
func (c *Client) ServiceCredentialBindings() capi.ServiceCredentialBindingsClient {
	return c.serviceCredentialBindings
}

// ServiceRouteBindings implements capi.Client.ServiceRouteBindings
func (c *Client) ServiceRouteBindings() capi.ServiceRouteBindingsClient {
	return c.serviceRouteBindings
}

// Builds implements capi.Client.Builds
func (c *Client) Builds() capi.BuildsClient {
	return c.builds
}

// Buildpacks implements capi.Client.Buildpacks
func (c *Client) Buildpacks() capi.BuildpacksClient {
	return c.buildpacks
}

// Deployments implements capi.Client.Deployments
func (c *Client) Deployments() capi.DeploymentsClient {
	return c.deployments
}

// Droplets implements capi.Client.Droplets
func (c *Client) Droplets() capi.DropletsClient {
	return c.droplets
}

// Packages implements capi.Client.Packages
func (c *Client) Packages() capi.PackagesClient {
	return c.packages
}

// Processes implements capi.Client.Processes
func (c *Client) Processes() capi.ProcessesClient {
	return c.processes
}

// Tasks implements capi.Client.Tasks
func (c *Client) Tasks() capi.TasksClient {
	return c.tasks
}

// Stacks implements capi.Client.Stacks
func (c *Client) Stacks() capi.StacksClient {
	return c.stacks
}

// Users implements capi.Client.Users
func (c *Client) Users() capi.UsersClient {
	return c.users
}

// Roles implements capi.Client.Roles
func (c *Client) Roles() capi.RolesClient {
	return c.roles
}

// SecurityGroups implements capi.Client.SecurityGroups
func (c *Client) SecurityGroups() capi.SecurityGroupsClient {
	return c.securityGroups
}

// IsolationSegments implements capi.Client.IsolationSegments
func (c *Client) IsolationSegments() capi.IsolationSegmentsClient {
	return c.isolationSegments
}

// FeatureFlags implements capi.Client.FeatureFlags
func (c *Client) FeatureFlags() capi.FeatureFlagsClient {
	return c.featureFlags
}

// Jobs implements capi.Client.Jobs
func (c *Client) Jobs() capi.JobsClient {
	return c.jobs
}

// OrganizationQuotas implements capi.Client.OrganizationQuotas
func (c *Client) OrganizationQuotas() capi.OrganizationQuotasClient {
	return c.organizationQuotas
}

// SpaceQuotas implements capi.Client.SpaceQuotas
func (c *Client) SpaceQuotas() capi.SpaceQuotasClient {
	return c.spaceQuotas
}

// Sidecars implements capi.Client.Sidecars
func (c *Client) Sidecars() capi.SidecarsClient {
	return c.sidecars
}

// Revisions implements capi.Client.Revisions
func (c *Client) Revisions() capi.RevisionsClient {
	return c.revisions
}

// EnvironmentVariableGroups implements capi.Client.EnvironmentVariableGroups
func (c *Client) EnvironmentVariableGroups() capi.EnvironmentVariableGroupsClient {
	return c.environmentVariableGroups
}

// AppUsageEvents implements capi.Client.AppUsageEvents
func (c *Client) AppUsageEvents() capi.AppUsageEventsClient {
	return c.appUsageEvents
}

// ServiceUsageEvents implements capi.Client.ServiceUsageEvents
func (c *Client) ServiceUsageEvents() capi.ServiceUsageEventsClient {
	return c.serviceUsageEvents
}

// AuditEvents implements capi.Client.AuditEvents
func (c *Client) AuditEvents() capi.AuditEventsClient {
	return c.auditEvents
}

// ResourceMatches implements capi.Client.ResourceMatches
func (c *Client) ResourceMatches() capi.ResourceMatchesClient {
	return c.resourceMatches
}

// Manifests implements capi.Client.Manifests
func (c *Client) Manifests() capi.ManifestsClient {
	return c.manifests
}

// GetToken returns the current access token from the token manager
func (c *Client) GetToken(ctx context.Context) (string, error) {
	if c.tokenManager == nil {
		return "", fmt.Errorf("no token manager configured")
	}
	return c.tokenManager.GetToken(ctx)
}

// staticTokenManager provides a static token
type staticTokenManager struct {
	token string
}

func (m *staticTokenManager) GetToken(ctx context.Context) (string, error) {
	return m.token, nil
}

func (m *staticTokenManager) RefreshToken(ctx context.Context) error {
	return fmt.Errorf("static token cannot be refreshed")
}

func (m *staticTokenManager) SetToken(token string, expiresAt time.Time) {
	m.token = token
}

// loggerAdapter adapts capi.Logger to http.Logger
type loggerAdapter struct {
	logger capi.Logger
}

func (l *loggerAdapter) Debug(msg string, fields map[string]interface{}) {
	l.logger.Debug(msg, fields)
}

func (l *loggerAdapter) Info(msg string, fields map[string]interface{}) {
	l.logger.Info(msg, fields)
}

func (l *loggerAdapter) Warn(msg string, fields map[string]interface{}) {
	l.logger.Warn(msg, fields)
}

func (l *loggerAdapter) Error(msg string, fields map[string]interface{}) {
	l.logger.Error(msg, fields)
}

// fallbackTokenManager tries static token first, then falls back to OAuth2
type fallbackTokenManager struct {
	staticToken      string
	oauthManager     auth.TokenManager
	usingOAuth       bool
	staticTokenTried bool
}

func (m *fallbackTokenManager) GetToken(ctx context.Context) (string, error) {
	// If we're already using OAuth (static token failed), continue with OAuth
	if m.usingOAuth {
		return m.oauthManager.GetToken(ctx)
	}

	// Try static token first, but only if we haven't tried it yet
	if m.staticToken != "" && !m.staticTokenTried {
		m.staticTokenTried = true
		return m.staticToken, nil
	}

	// Fall back to OAuth
	m.usingOAuth = true
	return m.oauthManager.GetToken(ctx)
}

func (m *fallbackTokenManager) RefreshToken(ctx context.Context) error {
	// If static token needs refresh, switch to OAuth and get a fresh token
	if !m.usingOAuth {
		m.usingOAuth = true
		// Get a fresh token instead of trying to refresh the static one
		_, err := m.oauthManager.GetToken(ctx)
		return err
	}
	return m.oauthManager.RefreshToken(ctx)
}

func (m *fallbackTokenManager) SetToken(token string, expiresAt time.Time) {
	if m.usingOAuth {
		m.oauthManager.SetToken(token, expiresAt)
	} else {
		m.staticToken = token
	}
}
