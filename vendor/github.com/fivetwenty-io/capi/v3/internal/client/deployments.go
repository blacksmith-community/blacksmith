package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// DeploymentsClient implements the capi.DeploymentsClient interface
type DeploymentsClient struct {
	httpClient *http.Client
}

// NewDeploymentsClient creates a new DeploymentsClient
func NewDeploymentsClient(httpClient *http.Client) *DeploymentsClient {
	return &DeploymentsClient{
		httpClient: httpClient,
	}
}

// Create creates a new deployment
func (c *DeploymentsClient) Create(ctx context.Context, request *capi.DeploymentCreateRequest) (*capi.Deployment, error) {
	path := "/v3/deployments"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating deployment: %w", err)
	}

	var deployment capi.Deployment
	if err := json.Unmarshal(resp.Body, &deployment); err != nil {
		return nil, fmt.Errorf("parsing deployment response: %w", err)
	}

	return &deployment, nil
}

// Get retrieves a specific deployment
func (c *DeploymentsClient) Get(ctx context.Context, guid string) (*capi.Deployment, error) {
	path := fmt.Sprintf("/v3/deployments/%s", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting deployment: %w", err)
	}

	var deployment capi.Deployment
	if err := json.Unmarshal(resp.Body, &deployment); err != nil {
		return nil, fmt.Errorf("parsing deployment response: %w", err)
	}

	return &deployment, nil
}

// List lists all deployments
func (c *DeploymentsClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.Deployment], error) {
	path := "/v3/deployments"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing deployments: %w", err)
	}

	var result capi.ListResponse[capi.Deployment]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing deployments list response: %w", err)
	}

	return &result, nil
}

// Update updates a deployment's metadata
func (c *DeploymentsClient) Update(ctx context.Context, guid string, request *capi.DeploymentUpdateRequest) (*capi.Deployment, error) {
	path := fmt.Sprintf("/v3/deployments/%s", guid)

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating deployment: %w", err)
	}

	var deployment capi.Deployment
	if err := json.Unmarshal(resp.Body, &deployment); err != nil {
		return nil, fmt.Errorf("parsing deployment response: %w", err)
	}

	return &deployment, nil
}

// Cancel cancels a deployment
func (c *DeploymentsClient) Cancel(ctx context.Context, guid string) error {
	path := fmt.Sprintf("/v3/deployments/%s/actions/cancel", guid)

	_, err := c.httpClient.Post(ctx, path, nil)
	if err != nil {
		return fmt.Errorf("canceling deployment: %w", err)
	}

	return nil
}

// Continue continues a paused deployment
func (c *DeploymentsClient) Continue(ctx context.Context, guid string) error {
	path := fmt.Sprintf("/v3/deployments/%s/actions/continue", guid)

	_, err := c.httpClient.Post(ctx, path, nil)
	if err != nil {
		return fmt.Errorf("continuing deployment: %w", err)
	}

	return nil
}
