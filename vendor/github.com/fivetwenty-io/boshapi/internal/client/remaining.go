package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// Locks client.
type locksClient struct {
	httpClient *http.Client
}

// NewLocksClient creates a new locks client.
func NewLocksClient(httpClient *http.Client) boshapi.LocksClient {
	return &locksClient{httpClient: httpClient}
}

func (c *locksClient) List(ctx context.Context) ([]boshapi.Lock, error) {
	resp, err := c.httpClient.Get(ctx, "/locks", nil)
	if err != nil {
		return nil, fmt.Errorf("listing locks: %w", err)
	}

	var locks []boshapi.Lock

	if err := json.Unmarshal(resp.Body, &locks); err != nil {
		return nil, fmt.Errorf("parsing locks: %w", err)
	}

	return locks, nil
}

// Networks client.
type networksClient struct {
	httpClient *http.Client
}

// NewNetworksClient creates a new networks client.
func NewNetworksClient(httpClient *http.Client) boshapi.NetworksClient {
	return &networksClient{httpClient: httpClient}
}

func (c *networksClient) ListOrphaned(ctx context.Context) ([]boshapi.OrphanedNetwork, error) {
	// The director lists only orphaned networks, and answers 500 unless
	// the orphaned parameter is present and not false.
	resp, err := c.httpClient.Get(ctx, "/networks", url.Values{"orphaned": {"true"}})
	if err != nil {
		return nil, fmt.Errorf("listing orphaned networks: %w", err)
	}

	var networks []boshapi.OrphanedNetwork

	if err := json.Unmarshal(resp.Body, &networks); err != nil {
		return nil, fmt.Errorf("parsing orphaned networks: %w", err)
	}

	return networks, nil
}

func (c *networksClient) DeleteOrphaned(ctx context.Context, name string) (*boshapi.Task, error) {
	if err := checkPathSegments("Networks().DeleteOrphaned", "name", name); err != nil {
		return nil, err
	}

	path := "/networks/" + url.PathEscape(name)

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting orphaned network %s: %w", name, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing delete network task: %w", err)
	}

	return task, nil
}

// Orphaned VMs client.
type orphanedVMsClient struct {
	httpClient *http.Client
}

// NewOrphanedVMsClient creates a new orphaned VMs client.
func NewOrphanedVMsClient(httpClient *http.Client) boshapi.OrphanedVMsClient {
	return &orphanedVMsClient{httpClient: httpClient}
}

func (c *orphanedVMsClient) List(ctx context.Context) ([]boshapi.OrphanedVM, error) {
	resp, err := c.httpClient.Get(ctx, "/orphaned_vms", nil)
	if err != nil {
		return nil, fmt.Errorf("listing orphaned VMs: %w", err)
	}

	var vms []boshapi.OrphanedVM

	if err := json.Unmarshal(resp.Body, &vms); err != nil {
		return nil, fmt.Errorf("parsing orphaned VMs: %w", err)
	}

	return vms, nil
}

// Resurrection client.
type resurrectionClient struct {
	httpClient *http.Client
}

// NewResurrectionClient creates a new resurrection client.
func NewResurrectionClient(httpClient *http.Client) boshapi.ResurrectionClient {
	return &resurrectionClient{httpClient: httpClient}
}

func (c *resurrectionClient) Get(ctx context.Context) (bool, error) {
	resp, err := c.httpClient.Get(ctx, "/resurrection", nil)
	if err != nil {
		return false, fmt.Errorf("getting resurrection state: %w", err)
	}

	var state boshapi.ResurrectionState

	if err := json.Unmarshal(resp.Body, &state); err != nil {
		return false, fmt.Errorf("parsing resurrection state: %w", err)
	}

	return state.Paused, nil
}

func (c *resurrectionClient) Set(ctx context.Context, paused bool) error {
	body := &boshapi.ResurrectionRequest{ResurrectionPaused: paused}

	_, err := c.httpClient.Put(ctx, "/resurrection", body)
	if err != nil {
		return fmt.Errorf("setting resurrection state: %w", err)
	}

	return nil
}

// Cleanup client.
type cleanupClient struct {
	httpClient *http.Client
}

// NewCleanupClient creates a new cleanup client.
func NewCleanupClient(httpClient *http.Client) boshapi.CleanupClient {
	return &cleanupClient{httpClient: httpClient}
}

func (c *cleanupClient) Cleanup(ctx context.Context, req *boshapi.CleanupRequest) (*boshapi.Task, error) {
	if req == nil {
		req = &boshapi.CleanupRequest{}
	}

	resp, err := c.httpClient.Post(ctx, "/cleanup", req)
	if err != nil {
		return nil, fmt.Errorf("running cleanup: %w", err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing cleanup task: %w", err)
	}

	return task, nil
}

func (c *cleanupClient) DryRun(ctx context.Context, opts *boshapi.CleanupDryRunOptions) (*boshapi.CleanupDryRunResult, error) {
	query := url.Values{}

	if opts != nil && opts.RemoveAll {
		query.Set("remove_all", "true")
	}

	if opts != nil && opts.KeepOrphanedDisks {
		query.Set("keep_orphaned_disks", "true")
	}

	resp, err := c.httpClient.Get(ctx, "/cleanup/dryrun", query)
	if err != nil {
		return nil, fmt.Errorf("cleanup dry run: %w", err)
	}

	var result boshapi.CleanupDryRunResult

	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing cleanup dry run: %w", err)
	}

	return &result, nil
}

// Packages client.
type packagesClient struct {
	httpClient *http.Client
}

// NewPackagesClient creates a new packages client.
func NewPackagesClient(httpClient *http.Client) boshapi.PackagesClient {
	return &packagesClient{httpClient: httpClient}
}

func (c *packagesClient) MatchPackages(ctx context.Context, yaml string) ([]string, error) {
	resp, err := c.httpClient.PostYAML(ctx, "/packages/matches", yaml, nil)
	if err != nil {
		return nil, fmt.Errorf("matching packages: %w", err)
	}

	var matches []string

	if err := json.Unmarshal(resp.Body, &matches); err != nil {
		return nil, fmt.Errorf("parsing package matches: %w", err)
	}

	return matches, nil
}

func (c *packagesClient) MatchCompiledPackages(ctx context.Context, yaml string) ([]string, error) {
	resp, err := c.httpClient.PostYAML(ctx, "/packages/matches_compiled", yaml, nil)
	if err != nil {
		return nil, fmt.Errorf("matching compiled packages: %w", err)
	}

	var matches []string

	if err := json.Unmarshal(resp.Body, &matches); err != nil {
		return nil, fmt.Errorf("parsing compiled package matches: %w", err)
	}

	return matches, nil
}

// DownloadBlob streams the package or compiled package blob with the given
// blobstore ID. It is a plain read of GET /packages/blobs/{id}, so it follows
// the same rules as Resources().Download. The director answers 400 for an ID
// that matches neither a package nor a compiled package.
func (c *packagesClient) DownloadBlob(ctx context.Context, blobstoreID string) (io.ReadCloser, error) {
	if strings.TrimSpace(blobstoreID) == "" {
		return nil, fmt.Errorf("downloading a package blob: %w; nothing was sent. Pass the blobstore ID "+
			"of a package or compiled package", boshapi.ErrBlobstoreIDRequired)
	}

	if blobstoreID == "." || blobstoreID == ".." {
		return nil, fmt.Errorf("downloading package blob %q: %w, so GET /packages/blobs/%s would not reach a blob, "+
			"since dots are read as the current or the parent path, and nothing was sent. Pass the blobstore ID of a package or compiled package",
			blobstoreID, boshapi.ErrPathSegmentInvalid, blobstoreID)
	}

	body, err := c.httpClient.GetStream(ctx, "/packages/blobs/"+url.PathEscape(blobstoreID), nil)
	if err != nil {
		return nil, fmt.Errorf("downloading package blob %s: %w", blobstoreID, err)
	}

	return body, nil
}

// Variables client.
type variablesClient struct {
	httpClient *http.Client
}

// NewVariablesClient creates a new variables client.
func NewVariablesClient(httpClient *http.Client) boshapi.VariablesClient {
	return &variablesClient{httpClient: httpClient}
}

func (c *variablesClient) List(ctx context.Context, deployment string) ([]boshapi.Variable, error) {
	if err := checkPathSegments("Variables().List", "deployment", deployment); err != nil {
		return nil, err
	}

	path := "/deployments/" + url.PathEscape(deployment) + "/variables"

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("listing variables for %s: %w", deployment, err)
	}

	var variables []boshapi.Variable

	if err := json.Unmarshal(resp.Body, &variables); err != nil {
		return nil, fmt.Errorf("parsing variables: %w", err)
	}

	return variables, nil
}

// Deployed variables client.
type deployedVariablesClient struct {
	httpClient *http.Client
}

// NewDeployedVariablesClient creates a new deployed variables client.
func NewDeployedVariablesClient(httpClient *http.Client) boshapi.DeployedVariablesClient {
	return &deployedVariablesClient{httpClient: httpClient}
}

func (c *deployedVariablesClient) Get(ctx context.Context, variableName string) (*boshapi.DeployedVariableUsage, error) {
	if err := checkPathSegments("DeployedVariables().Get", "variableName", variableName); err != nil {
		return nil, err
	}

	path := "/deployed_variables/" + url.PathEscape(variableName)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting deployed variable %s: %w", variableName, err)
	}

	var usage boshapi.DeployedVariableUsage

	if err := json.Unmarshal(resp.Body, &usage); err != nil {
		return nil, fmt.Errorf("parsing deployed variable: %w", err)
	}

	return &usage, nil
}

// Deployment configs client.
type deploymentConfigsClient struct {
	httpClient *http.Client
}

// NewDeploymentConfigsClient creates a new deployment configs client.
func NewDeploymentConfigsClient(httpClient *http.Client) boshapi.DeploymentConfigsClient {
	return &deploymentConfigsClient{httpClient: httpClient}
}

func (c *deploymentConfigsClient) List(ctx context.Context, opts *boshapi.DeploymentConfigListOptions) ([]boshapi.DeploymentConfig, error) {
	if opts == nil || len(opts.Deployments) == 0 {
		return nil, boshapi.ErrDeploymentsRequired
	}

	query := url.Values{}

	for _, deployment := range opts.Deployments {
		query.Add("deployment[]", deployment)
	}

	resp, err := c.httpClient.Get(ctx, "/deployment_configs", query)
	if err != nil {
		return nil, fmt.Errorf("listing deployment configs: %w", err)
	}

	var configs []boshapi.DeploymentConfig

	if err := json.Unmarshal(resp.Body, &configs); err != nil {
		return nil, fmt.Errorf("parsing deployment configs: %w", err)
	}

	return configs, nil
}
