package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// ServiceCredentialBindingsClient implements the capi.ServiceCredentialBindingsClient interface
type ServiceCredentialBindingsClient struct {
	httpClient *http.Client
}

// NewServiceCredentialBindingsClient creates a new ServiceCredentialBindingsClient
func NewServiceCredentialBindingsClient(httpClient *http.Client) *ServiceCredentialBindingsClient {
	return &ServiceCredentialBindingsClient{
		httpClient: httpClient,
	}
}

// Create creates a new service credential binding
// Returns *ServiceCredentialBinding for synchronous operations or *Job for asynchronous operations
func (c *ServiceCredentialBindingsClient) Create(ctx context.Context, request *capi.ServiceCredentialBindingCreateRequest) (interface{}, error) {
	path := "/v3/service_credential_bindings"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating service credential binding: %w", err)
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
		var binding capi.ServiceCredentialBinding
		if err := json.Unmarshal(resp.Body, &binding); err != nil {
			return nil, fmt.Errorf("parsing service credential binding response: %w", err)
		}
		return &binding, nil
	}
}

// Get retrieves a specific service credential binding
func (c *ServiceCredentialBindingsClient) Get(ctx context.Context, guid string) (*capi.ServiceCredentialBinding, error) {
	path := fmt.Sprintf("/v3/service_credential_bindings/%s", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting service credential binding: %w", err)
	}

	var binding capi.ServiceCredentialBinding
	if err := json.Unmarshal(resp.Body, &binding); err != nil {
		return nil, fmt.Errorf("parsing service credential binding response: %w", err)
	}

	return &binding, nil
}

// List lists all service credential bindings
func (c *ServiceCredentialBindingsClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.ServiceCredentialBinding], error) {
	path := "/v3/service_credential_bindings"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing service credential bindings: %w", err)
	}

	var result capi.ListResponse[capi.ServiceCredentialBinding]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing service credential bindings list response: %w", err)
	}

	return &result, nil
}

// Update updates a service credential binding (primarily for metadata)
func (c *ServiceCredentialBindingsClient) Update(ctx context.Context, guid string, request *capi.ServiceCredentialBindingUpdateRequest) (*capi.ServiceCredentialBinding, error) {
	path := fmt.Sprintf("/v3/service_credential_bindings/%s", guid)

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating service credential binding: %w", err)
	}

	var binding capi.ServiceCredentialBinding
	if err := json.Unmarshal(resp.Body, &binding); err != nil {
		return nil, fmt.Errorf("parsing service credential binding response: %w", err)
	}

	return &binding, nil
}

// Delete deletes a service credential binding
func (c *ServiceCredentialBindingsClient) Delete(ctx context.Context, guid string) (*capi.Job, error) {
	path := fmt.Sprintf("/v3/service_credential_bindings/%s", guid)

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting service credential binding: %w", err)
	}

	var job capi.Job
	if err := json.Unmarshal(resp.Body, &job); err != nil {
		return nil, fmt.Errorf("parsing job response: %w", err)
	}

	return &job, nil
}

// GetDetails retrieves the details (credentials) for a service credential binding
func (c *ServiceCredentialBindingsClient) GetDetails(ctx context.Context, guid string) (*capi.ServiceCredentialBindingDetails, error) {
	path := fmt.Sprintf("/v3/service_credential_bindings/%s/details", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting service credential binding details: %w", err)
	}

	var details capi.ServiceCredentialBindingDetails
	if err := json.Unmarshal(resp.Body, &details); err != nil {
		return nil, fmt.Errorf("parsing service credential binding details response: %w", err)
	}

	return &details, nil
}

// GetParameters retrieves the parameters for a service credential binding
func (c *ServiceCredentialBindingsClient) GetParameters(ctx context.Context, guid string) (*capi.ServiceCredentialBindingParameters, error) {
	path := fmt.Sprintf("/v3/service_credential_bindings/%s/parameters", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting service credential binding parameters: %w", err)
	}

	var params capi.ServiceCredentialBindingParameters
	if err := json.Unmarshal(resp.Body, &params); err != nil {
		return nil, fmt.Errorf("parsing service credential binding parameters response: %w", err)
	}

	return &params, nil
}
