package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// ServiceRouteBindingsClient implements capi.ServiceRouteBindingsClient
type ServiceRouteBindingsClient struct {
	httpClient *http.Client
}

// NewServiceRouteBindingsClient creates a new service route bindings client
func NewServiceRouteBindingsClient(httpClient *http.Client) *ServiceRouteBindingsClient {
	return &ServiceRouteBindingsClient{
		httpClient: httpClient,
	}
}

// Create implements capi.ServiceRouteBindingsClient.Create
func (c *ServiceRouteBindingsClient) Create(ctx context.Context, request *capi.ServiceRouteBindingCreateRequest) (interface{}, error) {
	path := "/v3/service_route_bindings"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating service route binding: %w", err)
	}

	// Check if it's an async operation (returns 202 with Job) or sync (returns 201 with binding)
	if resp.StatusCode == 202 {
		// Async operation - returns a job
		var job capi.Job
		if err := json.Unmarshal(resp.Body, &job); err != nil {
			return nil, fmt.Errorf("parsing job response: %w", err)
		}
		return &job, nil
	} else {
		// Sync operation - returns the binding directly
		var binding capi.ServiceRouteBinding
		if err := json.Unmarshal(resp.Body, &binding); err != nil {
			return nil, fmt.Errorf("parsing service route binding response: %w", err)
		}
		return &binding, nil
	}
}

// Get implements capi.ServiceRouteBindingsClient.Get
func (c *ServiceRouteBindingsClient) Get(ctx context.Context, guid string) (*capi.ServiceRouteBinding, error) {
	path := fmt.Sprintf("/v3/service_route_bindings/%s", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting service route binding: %w", err)
	}

	var binding capi.ServiceRouteBinding
	if err := json.Unmarshal(resp.Body, &binding); err != nil {
		return nil, fmt.Errorf("parsing service route binding: %w", err)
	}

	return &binding, nil
}

// List implements capi.ServiceRouteBindingsClient.List
func (c *ServiceRouteBindingsClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.ServiceRouteBinding], error) {
	path := "/v3/service_route_bindings"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing service route bindings: %w", err)
	}

	var list capi.ListResponse[capi.ServiceRouteBinding]
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		return nil, fmt.Errorf("parsing service route bindings list: %w", err)
	}

	return &list, nil
}

// Update implements capi.ServiceRouteBindingsClient.Update
func (c *ServiceRouteBindingsClient) Update(ctx context.Context, guid string, request *capi.ServiceRouteBindingUpdateRequest) (*capi.ServiceRouteBinding, error) {
	path := fmt.Sprintf("/v3/service_route_bindings/%s", guid)

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating service route binding: %w", err)
	}

	var binding capi.ServiceRouteBinding
	if err := json.Unmarshal(resp.Body, &binding); err != nil {
		return nil, fmt.Errorf("parsing service route binding: %w", err)
	}

	return &binding, nil
}

// Delete implements capi.ServiceRouteBindingsClient.Delete
func (c *ServiceRouteBindingsClient) Delete(ctx context.Context, guid string) (*capi.Job, error) {
	path := fmt.Sprintf("/v3/service_route_bindings/%s", guid)

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting service route binding: %w", err)
	}

	var job capi.Job
	if err := json.Unmarshal(resp.Body, &job); err != nil {
		return nil, fmt.Errorf("parsing job response: %w", err)
	}

	return &job, nil
}

// GetParameters implements capi.ServiceRouteBindingsClient.GetParameters
func (c *ServiceRouteBindingsClient) GetParameters(ctx context.Context, guid string) (*capi.ServiceRouteBindingParameters, error) {
	path := fmt.Sprintf("/v3/service_route_bindings/%s/parameters", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting service route binding parameters: %w", err)
	}

	var params capi.ServiceRouteBindingParameters
	if err := json.Unmarshal(resp.Body, &params); err != nil {
		return nil, fmt.Errorf("parsing service route binding parameters: %w", err)
	}

	return &params, nil
}
