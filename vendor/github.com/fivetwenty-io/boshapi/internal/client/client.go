package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"slices"
	"strings"
	"time"

	"github.com/fivetwenty-io/boshapi/internal/auth"
	"github.com/fivetwenty-io/boshapi/internal/constants"
	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// Client implements the boshapi.Client interface.
// Static errors for err113 compliance.
var (
	ErrDirectorURLRequired = errors.New("director URL is required")
)

// Compile-time interface compliance check.
var _ boshapi.Client = (*Client)(nil)

// Client implements boshapi.Client by composing resource-specific sub-clients.
type Client struct {
	httpClient *http.Client
	baseURL    string
	logger     boshapi.Logger

	// Resource clients
	deployments       boshapi.DeploymentsClient
	instances         boshapi.InstancesClient
	vms               boshapi.VMsClient
	resources         boshapi.ResourcesClient
	releases          boshapi.ReleasesClient
	stemcells         boshapi.StemcellsClient
	stemcellUploads   boshapi.StemcellUploadsClient
	tasks             boshapi.TasksClient
	events            boshapi.EventsClient
	configs           boshapi.ConfigsClient
	cloudConfigs      boshapi.CloudConfigsClient
	runtimeConfigs    boshapi.RuntimeConfigsClient
	cpiConfigs        boshapi.CPIConfigsClient
	errands           boshapi.ErrandsClient
	cloudCheck        boshapi.CloudCheckClient
	disks             boshapi.DisksClient
	dynamicDisks      boshapi.DynamicDisksClient
	snapshots         boshapi.SnapshotsClient
	links             boshapi.LinksClient
	linkProviders     boshapi.LinkProvidersClient
	linkConsumers     boshapi.LinkConsumersClient
	locks             boshapi.LocksClient
	networks          boshapi.NetworksClient
	orphanedVMs       boshapi.OrphanedVMsClient
	resurrection      boshapi.ResurrectionClient
	cleanup           boshapi.CleanupClient
	packages          boshapi.PackagesClient
	sshClient         boshapi.SSHClient
	variables         boshapi.VariablesClient
	deployedVariables boshapi.DeployedVariablesClient
	deploymentConfigs boshapi.DeploymentConfigsClient
}

// resolver returns the function that gives each request its credentials,
// chosen from config.Auth. It sends no request.
//
// AuthFromConfig uses the first of these that config sets: AccessToken as a
// token that is never refreshed; ClientID and ClientSecret as UAA client
// credentials, at TokenURL when it is set and otherwise at the UAA that the
// director's GET /info names; and Username and Password as HTTP Basic. With
// none of them, requests carry no credentials.
//
// AuthDiscover sends ClientID and ClientSecret the way the director's
// GET /info asks: as UAA client credentials when it names UAA, and as HTTP
// Basic when it names basic.
func (c *Client) resolver(config *boshapi.Config, tokenClient *nethttp.Client) (auth.Resolver, error) {
	newTokens := func(tokenURL string) auth.TokenManager {
		return auth.NewOAuth2TokenManager(auth.OAuth2Config{
			Director:     config.DirectorURL,
			TokenURL:     tokenURL,
			ClientID:     config.ClientID,
			ClientSecret: config.ClientSecret,
			HTTPClient:   tokenClient,
			Logger:       config.Logger,
		})
	}

	discovery := func(allowBasic bool) auth.Resolver {
		return newAuthDiscovery(discoveryConfig{
			director:   config.DirectorURL,
			clientID:   config.ClientID,
			secret:     config.ClientSecret,
			allowBasic: allowBasic,
			readInfo:   c.readInfo,
			newTokens:  newTokens,
			logger:     config.Logger,
		}).credentials
	}

	switch config.Auth {
	case boshapi.AuthFromConfig:
		switch {
		case config.AccessToken != "":
			return auth.Fixed(auth.Credentials{Tokens: auth.StaticToken(config.AccessToken)}), nil
		case config.ClientID != "" && config.ClientSecret != "" && config.TokenURL != "":
			return auth.Fixed(auth.Credentials{Tokens: newTokens(config.TokenURL)}), nil
		case config.ClientID != "" && config.ClientSecret != "":
			return discovery(false), nil
		case config.Username != "" && config.Password != "":
			return auth.Fixed(auth.Credentials{Username: config.Username, Password: config.Password}), nil
		default:
			return auth.Fixed(auth.Credentials{}), nil
		}
	case boshapi.AuthDiscover:
		err := checkDiscoverConfig(config)
		if err != nil {
			return nil, err
		}

		return discovery(true), nil
	default:
		return nil, fmt.Errorf("%w for director %s: Config.Auth is %d, which is neither AuthFromConfig nor "+
			"AuthDiscover. Leave it unset to use the credentials in the config", boshapi.ErrInvalidAuthConfig,
			config.DirectorURL, config.Auth)
	}
}

// checkDiscoverConfig refuses an AuthDiscover config that lacks client
// credentials or sets credentials that AuthDiscover would ignore.
func checkDiscoverConfig(config *boshapi.Config) error {
	if config.ClientID == "" || config.ClientSecret == "" {
		return fmt.Errorf("%w for director %s: Config.Auth is AuthDiscover, which needs both ClientID and "+
			"ClientSecret. Set both, or use AuthFromConfig for an access token or a username and password",
			boshapi.ErrInvalidAuthConfig, config.DirectorURL)
	}

	var ignored []string

	for name, value := range map[string]string{
		"AccessToken": config.AccessToken,
		"TokenURL":    config.TokenURL,
		"Username":    config.Username,
		"Password":    config.Password,
	} {
		if value != "" {
			ignored = append(ignored, name)
		}
	}

	if len(ignored) == 0 {
		return nil
	}

	slices.Sort(ignored)

	return fmt.Errorf("%w for director %s: Config.Auth is AuthDiscover, which reads how the director "+
		"authenticates and sends ClientID and ClientSecret, but the config also sets %s. Leave them empty, "+
		"or use AuthFromConfig", boshapi.ErrInvalidAuthConfig, config.DirectorURL, strings.Join(ignored, " and "))
}

// retryWaits returns the backoff bounds from config, or the defaults.
func retryWaits(config *boshapi.Config) (time.Duration, time.Duration) {
	waitMin := constants.DefaultRetryWaitMin
	waitMax := constants.ExtendedRetryWaitMax

	if config.RetryWaitMin > 0 {
		waitMin = config.RetryWaitMin
	}

	if config.RetryWaitMax > 0 {
		waitMax = config.RetryWaitMax
	}

	return waitMin, waitMax
}

// createHTTPClientOptions builds HTTP client options from config.
func createHTTPClientOptions(config *boshapi.Config, clients *http.Clients) []http.Option {
	httpOpts := []http.Option{http.WithClients(clients)}

	if config.Logger != nil {
		httpOpts = append(httpOpts, http.WithLogger(&loggerAdapter{logger: config.Logger}))
	}

	if config.Debug {
		httpOpts = append(httpOpts, http.WithDebug(true))
	}

	if config.UserAgent != "" {
		httpOpts = append(httpOpts, http.WithUserAgent(config.UserAgent))
	}

	// RetryMax 0 means a single attempt. There is no implicit retry count.
	retryWaitMin, retryWaitMax := retryWaits(config)

	httpOpts = append(httpOpts, http.WithRetryConfig(config.RetryMax, retryWaitMin, retryWaitMax))

	return httpOpts
}

// New creates a new BOSH Director API client.
func New(ctx context.Context, config *boshapi.Config) (*Client, error) {
	if config.DirectorURL == "" {
		return nil, ErrDirectorURLRequired
	}

	clients, err := http.NewClients(config)
	if err != nil {
		return nil, fmt.Errorf("building HTTP transport for director %s: %w", config.DirectorURL, err)
	}

	return NewWithClients(config, clients)
}

// NewWithClients creates a BOSH Director API client that sends every request,
// including its discovery and token requests, through the given HTTP
// clients, so they all share one transport. It sends no request: when the
// config calls for discovery, the first request that needs credentials
// runs it.
func NewWithClients(config *boshapi.Config, clients *http.Clients) (*Client, error) {
	if config.DirectorURL == "" {
		return nil, ErrDirectorURLRequired
	}

	client := &Client{
		baseURL: config.DirectorURL,
		logger:  config.Logger,
	}

	waitMin, waitMax := retryWaits(config)
	tokenClient := http.NewTokenClient(clients, config.RetryMax, waitMin, waitMax)

	resolve, err := client.resolver(config, tokenClient)
	if err != nil {
		return nil, err
	}

	client.httpClient = http.NewClient(config.DirectorURL, resolve, createHTTPClientOptions(config, clients)...)
	client.initializeResourceClients()

	return client, nil
}

// GetInfo implements boshapi.Client.GetInfo. It sends no credentials, as
// bosh-cli does, so it never triggers discovery, and the director leaves
// Info.User empty.
func (c *Client) GetInfo(ctx context.Context) (*boshapi.Info, error) {
	return c.readInfo(ctx)
}

// readInfo reads the director's GET /info without credentials.
func (c *Client) readInfo(ctx context.Context) (*boshapi.Info, error) {
	resp, err := c.httpClient.Do(ctx, &http.Request{Method: nethttp.MethodGet, Path: "/info", NoAuth: true})
	if err != nil {
		return nil, fmt.Errorf("getting info: %w", err)
	}

	var info boshapi.Info

	err = json.Unmarshal(resp.Body, &info)
	if err != nil {
		return nil, fmt.Errorf("parsing info response: %w", err)
	}

	return &info, nil
}

// GetCertificateExpiry implements boshapi.Client.GetCertificateExpiry.
func (c *Client) GetCertificateExpiry(ctx context.Context) ([]boshapi.CertificateExpiry, error) {
	resp, err := c.httpClient.Get(ctx, "/director/certificate_expiry", nil)
	if err != nil {
		return nil, fmt.Errorf("getting certificate expiry: %w", err)
	}

	var expiry []boshapi.CertificateExpiry

	err = json.Unmarshal(resp.Body, &expiry)
	if err != nil {
		return nil, fmt.Errorf("parsing certificate expiry response: %w", err)
	}

	return expiry, nil
}

// Resource client accessors

// Deployments implements boshapi.Client.Deployments.
func (c *Client) Deployments() boshapi.DeploymentsClient {
	return c.deployments
}

// Instances implements boshapi.Client.Instances.
func (c *Client) Instances() boshapi.InstancesClient {
	return c.instances
}

// VMs implements boshapi.Client.VMs.
func (c *Client) VMs() boshapi.VMsClient {
	return c.vms
}

// Resources implements boshapi.Client.Resources.
func (c *Client) Resources() boshapi.ResourcesClient {
	return c.resources
}

// Releases implements boshapi.Client.Releases.
func (c *Client) Releases() boshapi.ReleasesClient {
	return c.releases
}

// Stemcells implements boshapi.Client.Stemcells.
func (c *Client) Stemcells() boshapi.StemcellsClient {
	return c.stemcells
}

// StemcellUploads implements boshapi.Client.StemcellUploads.
func (c *Client) StemcellUploads() boshapi.StemcellUploadsClient {
	return c.stemcellUploads
}

// Tasks implements boshapi.Client.Tasks.
func (c *Client) Tasks() boshapi.TasksClient {
	return c.tasks
}

// Events implements boshapi.Client.Events.
func (c *Client) Events() boshapi.EventsClient {
	return c.events
}

// Configs implements boshapi.Client.Configs.
func (c *Client) Configs() boshapi.ConfigsClient {
	return c.configs
}

// CloudConfigs implements boshapi.Client.CloudConfigs.
func (c *Client) CloudConfigs() boshapi.CloudConfigsClient {
	return c.cloudConfigs
}

// RuntimeConfigs implements boshapi.Client.RuntimeConfigs.
func (c *Client) RuntimeConfigs() boshapi.RuntimeConfigsClient {
	return c.runtimeConfigs
}

// CPIConfigs implements boshapi.Client.CPIConfigs.
func (c *Client) CPIConfigs() boshapi.CPIConfigsClient {
	return c.cpiConfigs
}

// Errands implements boshapi.Client.Errands.
func (c *Client) Errands() boshapi.ErrandsClient {
	return c.errands
}

// CloudCheck implements boshapi.Client.CloudCheck.
func (c *Client) CloudCheck() boshapi.CloudCheckClient {
	return c.cloudCheck
}

// Disks implements boshapi.Client.Disks.
func (c *Client) Disks() boshapi.DisksClient {
	return c.disks
}

// DynamicDisks implements boshapi.Client.DynamicDisks.
func (c *Client) DynamicDisks() boshapi.DynamicDisksClient {
	return c.dynamicDisks
}

// Snapshots implements boshapi.Client.Snapshots.
func (c *Client) Snapshots() boshapi.SnapshotsClient {
	return c.snapshots
}

// Links implements boshapi.Client.Links.
func (c *Client) Links() boshapi.LinksClient {
	return c.links
}

// LinkProviders implements boshapi.Client.LinkProviders.
func (c *Client) LinkProviders() boshapi.LinkProvidersClient {
	return c.linkProviders
}

// LinkConsumers implements boshapi.Client.LinkConsumers.
func (c *Client) LinkConsumers() boshapi.LinkConsumersClient {
	return c.linkConsumers
}

// Locks implements boshapi.Client.Locks.
func (c *Client) Locks() boshapi.LocksClient {
	return c.locks
}

// Networks implements boshapi.Client.Networks.
func (c *Client) Networks() boshapi.NetworksClient {
	return c.networks
}

// OrphanedVMs implements boshapi.Client.OrphanedVMs.
func (c *Client) OrphanedVMs() boshapi.OrphanedVMsClient {
	return c.orphanedVMs
}

// Resurrection implements boshapi.Client.Resurrection.
func (c *Client) Resurrection() boshapi.ResurrectionClient {
	return c.resurrection
}

// Cleanup implements boshapi.Client.Cleanup.
func (c *Client) Cleanup() boshapi.CleanupClient {
	return c.cleanup
}

// Packages implements boshapi.Client.Packages.
func (c *Client) Packages() boshapi.PackagesClient {
	return c.packages
}

// SSHClient implements boshapi.Client.SSHClient.
func (c *Client) SSHClient() boshapi.SSHClient {
	return c.sshClient
}

// Variables implements boshapi.Client.Variables.
func (c *Client) Variables() boshapi.VariablesClient {
	return c.variables
}

// DeployedVariables implements boshapi.Client.DeployedVariables.
func (c *Client) DeployedVariables() boshapi.DeployedVariablesClient {
	return c.deployedVariables
}

// DeploymentConfigs implements boshapi.Client.DeploymentConfigs.
func (c *Client) DeploymentConfigs() boshapi.DeploymentConfigsClient {
	return c.deploymentConfigs
}

// initializeResourceClients initializes all resource-specific clients.
func (c *Client) initializeResourceClients() {
	c.deployments = NewDeploymentsClient(c.httpClient)
	c.instances = NewInstancesClient(c.httpClient)
	c.vms = NewVMsClient(c.httpClient)
	c.resources = NewResourcesClient(c.httpClient)
	c.releases = NewReleasesClient(c.httpClient)
	c.stemcells = NewStemcellsClient(c.httpClient)
	c.stemcellUploads = NewStemcellUploadsClient(c.httpClient)
	c.tasks = NewTasksClient(c.httpClient)
	c.events = NewEventsClient(c.httpClient)
	c.configs = NewConfigsClient(c.httpClient)
	c.cloudConfigs = NewCloudConfigsClient(c.httpClient)
	c.runtimeConfigs = NewRuntimeConfigsClient(c.httpClient)
	c.cpiConfigs = NewCPIConfigsClient(c.httpClient)
	c.errands = NewErrandsClient(c.httpClient)
	c.cloudCheck = NewCloudCheckClient(c.httpClient)
	c.disks = NewDisksClient(c.httpClient)
	c.dynamicDisks = NewDynamicDisksClient(c.httpClient)
	c.snapshots = NewSnapshotsClient(c.httpClient)
	c.links = NewLinksClient(c.httpClient)
	c.linkProviders = NewLinkProvidersClient(c.httpClient)
	c.linkConsumers = NewLinkConsumersClient(c.httpClient)
	c.locks = NewLocksClient(c.httpClient)
	c.networks = NewNetworksClient(c.httpClient)
	c.orphanedVMs = NewOrphanedVMsClient(c.httpClient)
	c.resurrection = NewResurrectionClient(c.httpClient)
	c.cleanup = NewCleanupClient(c.httpClient)
	c.packages = NewPackagesClient(c.httpClient)
	c.sshClient = NewSSHClient(c.httpClient)
	c.variables = NewVariablesClient(c.httpClient)
	c.deployedVariables = NewDeployedVariablesClient(c.httpClient)
	c.deploymentConfigs = NewDeploymentConfigsClient(c.httpClient)
}

// loggerAdapter adapts boshapi.Logger to http.Logger.
type loggerAdapter struct {
	logger boshapi.Logger
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
