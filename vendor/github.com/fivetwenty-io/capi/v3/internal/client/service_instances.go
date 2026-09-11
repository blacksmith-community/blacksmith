package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	http_internal "github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// ServiceInstancesClient implements the capi.ServiceInstancesClient interface.
type ServiceInstancesClient struct {
	httpClient *http_internal.Client
}

// NewServiceInstancesClient creates a new ServiceInstancesClient.
func NewServiceInstancesClient(httpClient *http_internal.Client) *ServiceInstancesClient {
	return &ServiceInstancesClient{
		httpClient: httpClient,
	}
}

// Create creates a new service instance
// Returns *ServiceInstance for user-provided instances, *Job for managed instances.
func (c *ServiceInstancesClient) Create(ctx context.Context, request *capi.ServiceInstanceCreateRequest) (any, error) {
	path := "/v3/service_instances"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating service instance: %w", err)
	}

	// Check if it's a managed instance (returns 202 with Job) or user-provided (returns 201 with instance)
	if resp.StatusCode == http.StatusAccepted {
		// Managed instance - async; job in body or Location header
		return jobFromAsyncResponse(resp, "creating service instance")
	} else {
		// User-provided instance - returns the instance directly
		var instance capi.ServiceInstance

		err := json.Unmarshal(resp.Body, &instance)
		if err != nil {
			return nil, fmt.Errorf("parsing service instance response: %w", err)
		}

		return &instance, nil
	}
}

// Get retrieves a specific service instance.
func (c *ServiceInstancesClient) Get(ctx context.Context, guid string, opts ...capi.ServiceInstanceGetOption) (*capi.ServiceInstance, error) {
	path := "/v3/service_instances/" + guid

	query := capi.ApplyQueryOptions(nil, opts)

	resp, err := c.httpClient.Get(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("getting service instance: %w", err)
	}

	var instance capi.ServiceInstance

	err = json.Unmarshal(resp.Body, &instance)
	if err != nil {
		return nil, fmt.Errorf("parsing service instance response: %w", err)
	}

	return &instance, nil
}

// List lists all service instances.
func (c *ServiceInstancesClient) List(ctx context.Context, params *capi.QueryParams, opts ...capi.ServiceInstanceListOption) (*capi.ListResponse[capi.ServiceInstance], error) {
	path := "/v3/service_instances"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	queryParams = capi.ApplyQueryOptions(queryParams, opts)

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing service instances: %w", err)
	}

	var result capi.ListResponse[capi.ServiceInstance]

	err = json.Unmarshal(resp.Body, &result)
	if err != nil {
		return nil, fmt.Errorf("parsing service instances list response: %w", err)
	}

	return &result, nil
}

// Update updates a service instance
// Returns *ServiceInstance for user-provided instances, *Job for managed instances.
func (c *ServiceInstancesClient) Update(ctx context.Context, guid string, request *capi.ServiceInstanceUpdateRequest) (any, error) {
	path := "/v3/service_instances/" + guid

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating service instance: %w", err)
	}

	// Check if it's a managed instance (returns 202 with Job) or user-provided (returns 200 with instance)
	if resp.StatusCode == http.StatusAccepted {
		// Managed instance - async; job in body or Location header
		return jobFromAsyncResponse(resp, "updating service instance")
	} else {
		// User-provided instance - returns the instance directly
		var instance capi.ServiceInstance

		err := json.Unmarshal(resp.Body, &instance)
		if err != nil {
			return nil, fmt.Errorf("parsing service instance response: %w", err)
		}

		return &instance, nil
	}
}

// Delete deletes a service instance.
// By default no purge query parameter is sent. Pass capi.WithPurge(true) to
// bypass the service broker and forcibly remove the record from the database.
func (c *ServiceInstancesClient) Delete(ctx context.Context, guid string, opts ...capi.DeleteOption) (*capi.Job, error) {
	path := "/v3/service_instances/" + guid

	deleteOpts := capi.ApplyDeleteOptions(opts)

	var queryParams url.Values
	if deleteOpts.Purge {
		queryParams = url.Values{}
		queryParams.Set("purge", "true")
	}

	resp, err := c.httpClient.DeleteWithQuery(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("deleting service instance: %w", err)
	}

	// CF V3 DELETE /v3/service_instances/{guid} returns 202 Accepted +
	// Location: /v3/jobs/{jobGuid} for the normal async path. When
	// purge=true, CF V3 responds 204 No Content with no Location header
	// (sync delete bypassing the broker); callers get a nil job + nil
	// error and treat that as "delete completed synchronously, no polling
	// needed."
	if deleteOpts.Purge {
		return jobFromOptionalLocation(resp, "deleting service instance")
	}

	return jobFromLocationHeader(resp, "deleting service instance")
}

// GetParameters retrieves parameters for a managed service instance.
func (c *ServiceInstancesClient) GetParameters(ctx context.Context, guid string) (*capi.ServiceInstanceParameters, error) {
	path := fmt.Sprintf("/v3/service_instances/%s/parameters", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting service instance parameters: %w", err)
	}

	// CF returns the parameters as a bare top-level JSON object
	// ({"key":"value", ...}), not wrapped in {"parameters": ...}, so
	// unmarshal into the map directly rather than the envelope struct.
	var params map[string]any

	err = json.Unmarshal(resp.Body, &params)
	if err != nil {
		return nil, fmt.Errorf("parsing service instance parameters response: %w", err)
	}

	return &capi.ServiceInstanceParameters{Parameters: params}, nil
}

// GetCredentials retrieves the credentials of a user-provided service instance.
func (c *ServiceInstancesClient) GetCredentials(ctx context.Context, guid string) (*capi.ServiceInstanceCredentials, error) {
	path := fmt.Sprintf("/v3/service_instances/%s/credentials", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting service instance credentials: %w", err)
	}

	// As with parameters, CF returns the credentials as a bare top-level
	// JSON object ({"username":"...", ...}), not wrapped in
	// {"credentials": ...}; unmarshal into the map directly.
	var creds map[string]any

	err = json.Unmarshal(resp.Body, &creds)
	if err != nil {
		return nil, fmt.Errorf("parsing service instance credentials response: %w", err)
	}

	return &capi.ServiceInstanceCredentials{Credentials: creds}, nil
}

// ListSharedSpaces lists the spaces a service instance is shared with.
func (c *ServiceInstancesClient) ListSharedSpaces(ctx context.Context, guid string) (*capi.ServiceInstanceSharedSpacesRelationships, error) {
	path := fmt.Sprintf("/v3/service_instances/%s/relationships/shared_spaces", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("listing shared spaces for service instance: %w", err)
	}

	var relationships capi.ServiceInstanceSharedSpacesRelationships

	err = json.Unmarshal(resp.Body, &relationships)
	if err != nil {
		return nil, fmt.Errorf("parsing shared spaces relationships response: %w", err)
	}

	return &relationships, nil
}

// ShareWithSpaces shares a service instance with additional spaces.
func (c *ServiceInstancesClient) ShareWithSpaces(ctx context.Context, guid string, request *capi.ServiceInstanceShareRequest) (*capi.ServiceInstanceSharedSpacesRelationships, error) {
	path := fmt.Sprintf("/v3/service_instances/%s/relationships/shared_spaces", guid)

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("sharing service instance with spaces: %w", err)
	}

	var relationships capi.ServiceInstanceSharedSpacesRelationships

	err = json.Unmarshal(resp.Body, &relationships)
	if err != nil {
		return nil, fmt.Errorf("parsing shared spaces relationships response: %w", err)
	}

	return &relationships, nil
}

// UnshareFromSpace unshares a service instance from a specific space.
func (c *ServiceInstancesClient) UnshareFromSpace(ctx context.Context, guid string, spaceGUID string) error {
	path := fmt.Sprintf("/v3/service_instances/%s/relationships/shared_spaces/%s", guid, spaceGUID)

	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("unsharing service instance from space: %w", err)
	}

	return nil
}
