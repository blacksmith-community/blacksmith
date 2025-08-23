package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// StacksClient implements capi.StacksClient
type StacksClient struct {
	httpClient *http.Client
}

// NewStacksClient creates a new stacks client
func NewStacksClient(httpClient *http.Client) *StacksClient {
	return &StacksClient{
		httpClient: httpClient,
	}
}

// Create implements capi.StacksClient.Create
func (c *StacksClient) Create(ctx context.Context, request *capi.StackCreateRequest) (*capi.Stack, error) {
	path := "/v3/stacks"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating stack: %w", err)
	}

	var stack capi.Stack
	if err := json.Unmarshal(resp.Body, &stack); err != nil {
		return nil, fmt.Errorf("parsing stack response: %w", err)
	}

	return &stack, nil
}

// Get implements capi.StacksClient.Get
func (c *StacksClient) Get(ctx context.Context, guid string) (*capi.Stack, error) {
	path := fmt.Sprintf("/v3/stacks/%s", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting stack: %w", err)
	}

	var stack capi.Stack
	if err := json.Unmarshal(resp.Body, &stack); err != nil {
		return nil, fmt.Errorf("parsing stack: %w", err)
	}

	return &stack, nil
}

// List implements capi.StacksClient.List
func (c *StacksClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.Stack], error) {
	path := "/v3/stacks"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing stacks: %w", err)
	}

	var list capi.ListResponse[capi.Stack]
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		return nil, fmt.Errorf("parsing stacks list: %w", err)
	}

	return &list, nil
}

// Update implements capi.StacksClient.Update
func (c *StacksClient) Update(ctx context.Context, guid string, request *capi.StackUpdateRequest) (*capi.Stack, error) {
	path := fmt.Sprintf("/v3/stacks/%s", guid)

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating stack: %w", err)
	}

	var stack capi.Stack
	if err := json.Unmarshal(resp.Body, &stack); err != nil {
		return nil, fmt.Errorf("parsing stack: %w", err)
	}

	return &stack, nil
}

// Delete implements capi.StacksClient.Delete
func (c *StacksClient) Delete(ctx context.Context, guid string) error {
	path := fmt.Sprintf("/v3/stacks/%s", guid)

	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("deleting stack: %w", err)
	}

	return nil
}

// ListApps implements capi.StacksClient.ListApps
func (c *StacksClient) ListApps(ctx context.Context, guid string, params *capi.QueryParams) (*capi.ListResponse[capi.App], error) {
	path := fmt.Sprintf("/v3/stacks/%s/apps", guid)

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing apps for stack: %w", err)
	}

	var list capi.ListResponse[capi.App]
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		return nil, fmt.Errorf("parsing apps list: %w", err)
	}

	return &list, nil
}
