package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// ServiceInstancesClient implements the capi.ServiceInstancesClient interface
type ServiceInstancesClient struct {
	httpClient *http.Client
}

// NewServiceInstancesClient creates a new ServiceInstancesClient
func NewServiceInstancesClient(httpClient *http.Client) *ServiceInstancesClient {
	return &ServiceInstancesClient{
		httpClient: httpClient,
	}
}

// Create creates a new service instance
// Returns *ServiceInstance for user-provided instances, *Job for managed instances
func (c *ServiceInstancesClient) Create(ctx context.Context, request *capi.ServiceInstanceCreateRequest) (interface{}, error) {
	path := "/v3/service_instances"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating service instance: %w", err)
	}

	// Check if it's a managed instance (returns 202 with Job) or user-provided (returns 201 with instance)
	if resp.StatusCode == 202 {
		// Managed instance - returns a job
		var job capi.Job
		if err := json.Unmarshal(resp.Body, &job); err != nil {
			return nil, fmt.Errorf("parsing job response: %w", err)
		}
		return &job, nil
	} else {
		// User-provided instance - returns the instance directly
		var instance capi.ServiceInstance
		if err := json.Unmarshal(resp.Body, &instance); err != nil {
			return nil, fmt.Errorf("parsing service instance response: %w", err)
		}
		return &instance, nil
	}
}

// Get retrieves a specific service instance
func (c *ServiceInstancesClient) Get(ctx context.Context, guid string) (*capi.ServiceInstance, error) {
	path := fmt.Sprintf("/v3/service_instances/%s", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting service instance: %w", err)
	}

	var instance capi.ServiceInstance
	if err := json.Unmarshal(resp.Body, &instance); err != nil {
		return nil, fmt.Errorf("parsing service instance response: %w", err)
	}

	return &instance, nil
}

// List lists all service instances
func (c *ServiceInstancesClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.ServiceInstance], error) {
	path := "/v3/service_instances"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing service instances: %w", err)
	}

	var result capi.ListResponse[capi.ServiceInstance]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing service instances list response: %w", err)
	}

	return &result, nil
}

// Update updates a service instance
// Returns *ServiceInstance for user-provided instances, *Job for managed instances
func (c *ServiceInstancesClient) Update(ctx context.Context, guid string, request *capi.ServiceInstanceUpdateRequest) (interface{}, error) {
	path := fmt.Sprintf("/v3/service_instances/%s", guid)

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating service instance: %w", err)
	}

	// Check if it's a managed instance (returns 202 with Job) or user-provided (returns 200 with instance)
	if resp.StatusCode == 202 {
		// Managed instance - returns a job
		var job capi.Job
		if err := json.Unmarshal(resp.Body, &job); err != nil {
			return nil, fmt.Errorf("parsing job response: %w", err)
		}
		return &job, nil
	} else {
		// User-provided instance - returns the instance directly
		var instance capi.ServiceInstance
		if err := json.Unmarshal(resp.Body, &instance); err != nil {
			return nil, fmt.Errorf("parsing service instance response: %w", err)
		}
		return &instance, nil
	}
}

// Delete deletes a service instance
func (c *ServiceInstancesClient) Delete(ctx context.Context, guid string) (*capi.Job, error) {
	path := fmt.Sprintf("/v3/service_instances/%s", guid)

	// Add purge query parameter by default to force delete
	queryParams := url.Values{}
	queryParams.Set("purge", "true")

	resp, err := c.httpClient.DeleteWithQuery(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("deleting service instance: %w", err)
	}

	var job capi.Job
	if err := json.Unmarshal(resp.Body, &job); err != nil {
		return nil, fmt.Errorf("parsing job response: %w", err)
	}

	return &job, nil
}

// GetParameters retrieves parameters for a managed service instance
func (c *ServiceInstancesClient) GetParameters(ctx context.Context, guid string) (*capi.ServiceInstanceParameters, error) {
	path := fmt.Sprintf("/v3/service_instances/%s/parameters", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting service instance parameters: %w", err)
	}

	var params capi.ServiceInstanceParameters
	if err := json.Unmarshal(resp.Body, &params); err != nil {
		return nil, fmt.Errorf("parsing service instance parameters response: %w", err)
	}

	return &params, nil
}

// ListSharedSpaces lists the spaces a service instance is shared with
func (c *ServiceInstancesClient) ListSharedSpaces(ctx context.Context, guid string) (*capi.ServiceInstanceSharedSpacesRelationships, error) {
	path := fmt.Sprintf("/v3/service_instances/%s/relationships/shared_spaces", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("listing shared spaces for service instance: %w", err)
	}

	var relationships capi.ServiceInstanceSharedSpacesRelationships
	if err := json.Unmarshal(resp.Body, &relationships); err != nil {
		return nil, fmt.Errorf("parsing shared spaces relationships response: %w", err)
	}

	return &relationships, nil
}

// ShareWithSpaces shares a service instance with additional spaces
func (c *ServiceInstancesClient) ShareWithSpaces(ctx context.Context, guid string, request *capi.ServiceInstanceShareRequest) (*capi.ServiceInstanceSharedSpacesRelationships, error) {
	path := fmt.Sprintf("/v3/service_instances/%s/relationships/shared_spaces", guid)

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("sharing service instance with spaces: %w", err)
	}

	var relationships capi.ServiceInstanceSharedSpacesRelationships
	if err := json.Unmarshal(resp.Body, &relationships); err != nil {
		return nil, fmt.Errorf("parsing shared spaces relationships response: %w", err)
	}

	return &relationships, nil
}

// UnshareFromSpace unshares a service instance from a specific space
func (c *ServiceInstancesClient) UnshareFromSpace(ctx context.Context, guid string, spaceGUID string) error {
	path := fmt.Sprintf("/v3/service_instances/%s/relationships/shared_spaces/%s", guid, spaceGUID)

	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("unsharing service instance from space: %w", err)
	}

	return nil
}
