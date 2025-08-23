package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// BuildsClient implements the capi.BuildsClient interface
type BuildsClient struct {
	httpClient *http.Client
}

// NewBuildsClient creates a new BuildsClient
func NewBuildsClient(httpClient *http.Client) *BuildsClient {
	return &BuildsClient{
		httpClient: httpClient,
	}
}

// Create creates a new build
func (c *BuildsClient) Create(ctx context.Context, request *capi.BuildCreateRequest) (*capi.Build, error) {
	path := "/v3/builds"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating build: %w", err)
	}

	var build capi.Build
	if err := json.Unmarshal(resp.Body, &build); err != nil {
		return nil, fmt.Errorf("parsing build response: %w", err)
	}

	return &build, nil
}

// Get retrieves a specific build
func (c *BuildsClient) Get(ctx context.Context, guid string) (*capi.Build, error) {
	path := fmt.Sprintf("/v3/builds/%s", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting build: %w", err)
	}

	var build capi.Build
	if err := json.Unmarshal(resp.Body, &build); err != nil {
		return nil, fmt.Errorf("parsing build response: %w", err)
	}

	return &build, nil
}

// List lists all builds
func (c *BuildsClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.Build], error) {
	path := "/v3/builds"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing builds: %w", err)
	}

	var result capi.ListResponse[capi.Build]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing builds list response: %w", err)
	}

	return &result, nil
}

// ListForApp lists builds for a specific app
func (c *BuildsClient) ListForApp(ctx context.Context, appGUID string, params *capi.QueryParams) (*capi.ListResponse[capi.Build], error) {
	path := fmt.Sprintf("/v3/apps/%s/builds", appGUID)

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing builds for app: %w", err)
	}

	var result capi.ListResponse[capi.Build]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing builds list response: %w", err)
	}

	return &result, nil
}

// Update updates a build's metadata
func (c *BuildsClient) Update(ctx context.Context, guid string, request *capi.BuildUpdateRequest) (*capi.Build, error) {
	path := fmt.Sprintf("/v3/builds/%s", guid)

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating build: %w", err)
	}

	var build capi.Build
	if err := json.Unmarshal(resp.Body, &build); err != nil {
		return nil, fmt.Errorf("parsing build response: %w", err)
	}

	return &build, nil
}
