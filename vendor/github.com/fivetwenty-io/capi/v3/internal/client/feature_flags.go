package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// FeatureFlagsClient implements capi.FeatureFlagsClient
type FeatureFlagsClient struct {
	httpClient *http.Client
}

// NewFeatureFlagsClient creates a new feature flags client
func NewFeatureFlagsClient(httpClient *http.Client) *FeatureFlagsClient {
	return &FeatureFlagsClient{
		httpClient: httpClient,
	}
}

// Get implements capi.FeatureFlagsClient.Get
func (c *FeatureFlagsClient) Get(ctx context.Context, name string) (*capi.FeatureFlag, error) {
	path := fmt.Sprintf("/v3/feature_flags/%s", name)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting feature flag: %w", err)
	}

	var ff capi.FeatureFlag
	if err := json.Unmarshal(resp.Body, &ff); err != nil {
		return nil, fmt.Errorf("parsing feature flag: %w", err)
	}

	return &ff, nil
}

// List implements capi.FeatureFlagsClient.List
func (c *FeatureFlagsClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.FeatureFlag], error) {
	path := "/v3/feature_flags"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing feature flags: %w", err)
	}

	var list capi.ListResponse[capi.FeatureFlag]
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		return nil, fmt.Errorf("parsing feature flags list: %w", err)
	}

	return &list, nil
}

// Update implements capi.FeatureFlagsClient.Update
func (c *FeatureFlagsClient) Update(ctx context.Context, name string, request *capi.FeatureFlagUpdateRequest) (*capi.FeatureFlag, error) {
	path := fmt.Sprintf("/v3/feature_flags/%s", name)

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating feature flag: %w", err)
	}

	var ff capi.FeatureFlag
	if err := json.Unmarshal(resp.Body, &ff); err != nil {
		return nil, fmt.Errorf("parsing feature flag response: %w", err)
	}

	return &ff, nil
}
