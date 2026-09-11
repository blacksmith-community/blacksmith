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

// ServiceCredentialBindingsClient implements the capi.ServiceCredentialBindingsClient interface.
type ServiceCredentialBindingsClient struct {
	httpClient *http_internal.Client
}

// NewServiceCredentialBindingsClient creates a new ServiceCredentialBindingsClient.
func NewServiceCredentialBindingsClient(httpClient *http_internal.Client) *ServiceCredentialBindingsClient {
	return &ServiceCredentialBindingsClient{
		httpClient: httpClient,
	}
}

// Create creates a new service credential binding
// Returns *ServiceCredentialBinding for synchronous operations or *Job for asynchronous operations.
func (c *ServiceCredentialBindingsClient) Create(ctx context.Context, request *capi.ServiceCredentialBindingCreateRequest) (any, error) {
	path := "/v3/service_credential_bindings"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating service credential binding: %w", err)
	}

	// Check if it's an async operation (returns 202 with Job) or sync (returns 201 with binding)
	if resp.StatusCode == http.StatusAccepted {
		// Async operation - job in body or Location header
		return jobFromAsyncResponse(resp, "creating service credential binding")
	} else {
		// Sync operation - returns the binding directly
		var binding capi.ServiceCredentialBinding

		err := json.Unmarshal(resp.Body, &binding)
		if err != nil {
			return nil, fmt.Errorf("parsing service credential binding response: %w", err)
		}

		return &binding, nil
	}
}

// Get retrieves a specific service credential binding.
func (c *ServiceCredentialBindingsClient) Get(ctx context.Context, guid string, opts ...capi.ServiceCredentialBindingGetOption) (*capi.ServiceCredentialBinding, error) {
	path := "/v3/service_credential_bindings/" + guid

	resp, err := c.httpClient.Get(ctx, path, capi.ApplyQueryOptions(nil, opts))
	if err != nil {
		return nil, fmt.Errorf("getting service credential binding: %w", err)
	}

	var binding capi.ServiceCredentialBinding

	err = json.Unmarshal(resp.Body, &binding)
	if err != nil {
		return nil, fmt.Errorf("parsing service credential binding response: %w", err)
	}

	return &binding, nil
}

// List lists all service credential bindings.
func (c *ServiceCredentialBindingsClient) List(ctx context.Context, params *capi.QueryParams, opts ...capi.ServiceCredentialBindingListOption) (*capi.ListResponse[capi.ServiceCredentialBinding], error) {
	path := "/v3/service_credential_bindings"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	queryParams = capi.ApplyQueryOptions(queryParams, opts)

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing service credential bindings: %w", err)
	}

	var result capi.ListResponse[capi.ServiceCredentialBinding]

	err = json.Unmarshal(resp.Body, &result)
	if err != nil {
		return nil, fmt.Errorf("parsing service credential bindings list response: %w", err)
	}

	return &result, nil
}

// Update updates a service credential binding (primarily for metadata).
func (c *ServiceCredentialBindingsClient) Update(ctx context.Context, guid string, request *capi.ServiceCredentialBindingUpdateRequest) (*capi.ServiceCredentialBinding, error) {
	path := "/v3/service_credential_bindings/" + guid

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating service credential binding: %w", err)
	}

	var binding capi.ServiceCredentialBinding

	err = json.Unmarshal(resp.Body, &binding)
	if err != nil {
		return nil, fmt.Errorf("parsing service credential binding response: %w", err)
	}

	return &binding, nil
}

// Delete deletes a service credential binding.
//
// DELETE /v3/service_credential_bindings/{guid} is polymorphic depending on
// the underlying service instance type:
//
//   - User-provided service instance: CF deletes synchronously and returns
//     204 No Content with no body and no Location header. We return
//     (nil, nil) so callers can treat a nil Job as "no async work pending —
//     the delete is already complete."
//   - Managed service instance: CF schedules an async job and returns 202
//     Accepted with an empty body and a Location header pointing at
//     /v3/jobs/{jobGuid}. We extract the GUID from the header and return a
//     Job with it populated; callers poll via Jobs().Get or
//     Jobs().PollUntilComplete.
//
// Callers must guard against `job == nil` before polling. Missing Location
// on a 202 response is treated as a protocol violation — we return an error
// rather than a Job with an empty GUID to prevent accidentally polling
// `/v3/jobs/`.
func (c *ServiceCredentialBindingsClient) Delete(ctx context.Context, guid string) (*capi.Job, error) {
	path := "/v3/service_credential_bindings/" + guid

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting service credential binding: %w", err)
	}

	// Sync delete of a user-provided binding: 204 No Content, no Job.
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	// Async delete of a managed binding: 202 Accepted + Location header.
	return jobFromLocationHeader(resp, "deleting service credential binding")
}

// GetDetails retrieves the details (credentials) for a service credential binding.
func (c *ServiceCredentialBindingsClient) GetDetails(ctx context.Context, guid string) (*capi.ServiceCredentialBindingDetails, error) {
	path := fmt.Sprintf("/v3/service_credential_bindings/%s/details", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting service credential binding details: %w", err)
	}

	var details capi.ServiceCredentialBindingDetails

	err = json.Unmarshal(resp.Body, &details)
	if err != nil {
		return nil, fmt.Errorf("parsing service credential binding details response: %w", err)
	}

	return &details, nil
}

// GetParameters retrieves the parameters for a service credential binding.
func (c *ServiceCredentialBindingsClient) GetParameters(ctx context.Context, guid string) (*capi.ServiceCredentialBindingParameters, error) {
	path := fmt.Sprintf("/v3/service_credential_bindings/%s/parameters", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting service credential binding parameters: %w", err)
	}

	var params capi.ServiceCredentialBindingParameters

	err = json.Unmarshal(resp.Body, &params)
	if err != nil {
		return nil, fmt.Errorf("parsing service credential binding parameters response: %w", err)
	}

	return &params, nil
}
