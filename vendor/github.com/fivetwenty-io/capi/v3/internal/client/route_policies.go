package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// RoutePoliciesClient implements the capi.RoutePoliciesClient interface
// (CF v3 3.225.0, experimental).
type RoutePoliciesClient struct {
	httpClient *http.Client
}

// NewRoutePoliciesClient creates a new RoutePoliciesClient.
func NewRoutePoliciesClient(httpClient *http.Client) *RoutePoliciesClient {
	return &RoutePoliciesClient{
		httpClient: httpClient,
	}
}

// Create creates a new route policy. The route's domain must have
// enforce_route_policies set to true and must not be internal.
func (c *RoutePoliciesClient) Create(ctx context.Context, request *capi.RoutePolicyCreateRequest) (*capi.RoutePolicy, error) {
	path := "/v3/route_policies"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating route policy: %w", err)
	}

	var policy capi.RoutePolicy

	err = json.Unmarshal(resp.Body, &policy)
	if err != nil {
		return nil, fmt.Errorf("parsing route policy response: %w", err)
	}

	return &policy, nil
}

// Get retrieves a specific route policy.
func (c *RoutePoliciesClient) Get(ctx context.Context, guid string, opts ...capi.RoutePolicyGetOption) (*capi.RoutePolicy, error) {
	path := "/v3/route_policies/" + guid

	resp, err := c.httpClient.Get(ctx, path, capi.ApplyQueryOptions(nil, opts))
	if err != nil {
		return nil, fmt.Errorf("getting route policy: %w", err)
	}

	var policy capi.RoutePolicy

	err = json.Unmarshal(resp.Body, &policy)
	if err != nil {
		return nil, fmt.Errorf("parsing route policy response: %w", err)
	}

	return &policy, nil
}

// List lists all route policies.
func (c *RoutePoliciesClient) List(ctx context.Context, params *capi.QueryParams, opts ...capi.RoutePolicyListOption) (*capi.ListResponse[capi.RoutePolicy], error) {
	path := "/v3/route_policies"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	queryParams = capi.ApplyQueryOptions(queryParams, opts)

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing route policies: %w", err)
	}

	var result capi.ListResponse[capi.RoutePolicy]

	err = json.Unmarshal(resp.Body, &result)
	if err != nil {
		return nil, fmt.Errorf("parsing route policies list response: %w", err)
	}

	return &result, nil
}

// Update updates a route policy's metadata. Source and route are immutable
// after creation.
func (c *RoutePoliciesClient) Update(ctx context.Context, guid string, request *capi.RoutePolicyUpdateRequest) (*capi.RoutePolicy, error) {
	path := "/v3/route_policies/" + guid

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating route policy: %w", err)
	}

	var policy capi.RoutePolicy

	err = json.Unmarshal(resp.Body, &policy)
	if err != nil {
		return nil, fmt.Errorf("parsing route policy response: %w", err)
	}

	return &policy, nil
}

// Delete deletes a route policy. The delete is synchronous: CF returns
// 204 No Content.
func (c *RoutePoliciesClient) Delete(ctx context.Context, guid string) error {
	path := "/v3/route_policies/" + guid

	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("deleting route policy: %w", err)
	}

	return nil
}
