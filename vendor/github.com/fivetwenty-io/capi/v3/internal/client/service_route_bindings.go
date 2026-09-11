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

// ServiceRouteBindingsClient implements capi.ServiceRouteBindingsClient.
type ServiceRouteBindingsClient struct {
	httpClient *http_internal.Client
}

// NewServiceRouteBindingsClient creates a new service route bindings client.
func NewServiceRouteBindingsClient(httpClient *http_internal.Client) *ServiceRouteBindingsClient {
	return &ServiceRouteBindingsClient{
		httpClient: httpClient,
	}
}

// Create implements capi.ServiceRouteBindingsClient.Create.
func (c *ServiceRouteBindingsClient) Create(ctx context.Context, request *capi.ServiceRouteBindingCreateRequest) (any, error) {
	path := "/v3/service_route_bindings"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating service route binding: %w", err)
	}

	// Check if it's an async operation (returns 202 with Job) or sync (returns 201 with binding)
	if resp.StatusCode == http.StatusAccepted {
		// Async operation - job in body or Location header
		return jobFromAsyncResponse(resp, "creating service route binding")
	} else {
		// Sync operation - returns the binding directly
		var binding capi.ServiceRouteBinding

		err := json.Unmarshal(resp.Body, &binding)
		if err != nil {
			return nil, fmt.Errorf("parsing service route binding response: %w", err)
		}

		return &binding, nil
	}
}

// Get implements capi.ServiceRouteBindingsClient.Get.
func (c *ServiceRouteBindingsClient) Get(ctx context.Context, guid string, opts ...capi.ServiceRouteBindingGetOption) (*capi.ServiceRouteBinding, error) {
	path := "/v3/service_route_bindings/" + guid

	resp, err := c.httpClient.Get(ctx, path, capi.ApplyQueryOptions(nil, opts))
	if err != nil {
		return nil, fmt.Errorf("getting service route binding: %w", err)
	}

	var binding capi.ServiceRouteBinding

	err = json.Unmarshal(resp.Body, &binding)
	if err != nil {
		return nil, fmt.Errorf("parsing service route binding: %w", err)
	}

	return &binding, nil
}

// List implements capi.ServiceRouteBindingsClient.List.
func (c *ServiceRouteBindingsClient) List(ctx context.Context, params *capi.QueryParams, opts ...capi.ServiceRouteBindingListOption) (*capi.ListResponse[capi.ServiceRouteBinding], error) {
	path := "/v3/service_route_bindings"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	queryParams = capi.ApplyQueryOptions(queryParams, opts)

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing service route bindings: %w", err)
	}

	var list capi.ListResponse[capi.ServiceRouteBinding]

	err = json.Unmarshal(resp.Body, &list)
	if err != nil {
		return nil, fmt.Errorf("parsing service route bindings list: %w", err)
	}

	return &list, nil
}

// Update implements capi.ServiceRouteBindingsClient.Update.
func (c *ServiceRouteBindingsClient) Update(ctx context.Context, guid string, request *capi.ServiceRouteBindingUpdateRequest) (*capi.ServiceRouteBinding, error) {
	path := "/v3/service_route_bindings/" + guid

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating service route binding: %w", err)
	}

	var binding capi.ServiceRouteBinding

	err = json.Unmarshal(resp.Body, &binding)
	if err != nil {
		return nil, fmt.Errorf("parsing service route binding: %w", err)
	}

	return &binding, nil
}

// Delete implements capi.ServiceRouteBindingsClient.Delete.
//
// DELETE /v3/service_route_bindings/{guid} is polymorphic depending on the
// underlying service instance type:
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
func (c *ServiceRouteBindingsClient) Delete(ctx context.Context, guid string) (*capi.Job, error) {
	path := "/v3/service_route_bindings/" + guid

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting service route binding: %w", err)
	}

	// Sync delete of a user-provided binding: 204 No Content, no Job.
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	// Async delete of a managed binding: 202 Accepted + Location header.
	return jobFromLocationHeader(resp, "deleting service route binding")
}

// GetParameters implements capi.ServiceRouteBindingsClient.GetParameters.
func (c *ServiceRouteBindingsClient) GetParameters(ctx context.Context, guid string) (*capi.ServiceRouteBindingParameters, error) {
	path := fmt.Sprintf("/v3/service_route_bindings/%s/parameters", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting service route binding parameters: %w", err)
	}

	var params capi.ServiceRouteBindingParameters

	err = json.Unmarshal(resp.Body, &params)
	if err != nil {
		return nil, fmt.Errorf("parsing service route binding parameters: %w", err)
	}

	return &params, nil
}
