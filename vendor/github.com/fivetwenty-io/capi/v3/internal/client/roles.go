package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// RolesClient implements capi.RolesClient.
type RolesClient struct {
	httpClient *http.Client
}

// NewRolesClient creates a new roles client.
func NewRolesClient(httpClient *http.Client) *RolesClient {
	return &RolesClient{
		httpClient: httpClient,
	}
}

// Create implements capi.RolesClient.Create.
func (c *RolesClient) Create(ctx context.Context, request *capi.RoleCreateRequest) (*capi.Role, error) {
	path := "/v3/roles"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating role: %w", err)
	}

	var role capi.Role

	err = json.Unmarshal(resp.Body, &role)
	if err != nil {
		return nil, fmt.Errorf("parsing role response: %w", err)
	}

	return &role, nil
}

// Get implements capi.RolesClient.Get.
func (c *RolesClient) Get(ctx context.Context, guid string, opts ...capi.RoleGetOption) (*capi.Role, error) {
	path := "/v3/roles/" + guid

	resp, err := c.httpClient.Get(ctx, path, capi.ApplyQueryOptions(nil, opts))
	if err != nil {
		return nil, fmt.Errorf("getting role: %w", err)
	}

	var role capi.Role

	err = json.Unmarshal(resp.Body, &role)
	if err != nil {
		return nil, fmt.Errorf("parsing role: %w", err)
	}

	return &role, nil
}

// List implements capi.RolesClient.List.
func (c *RolesClient) List(ctx context.Context, params *capi.QueryParams, opts ...capi.RoleListOption) (*capi.ListResponse[capi.Role], error) {
	path := "/v3/roles"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	queryParams = capi.ApplyQueryOptions(queryParams, opts)

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing roles: %w", err)
	}

	var list capi.ListResponse[capi.Role]

	err = json.Unmarshal(resp.Body, &list)
	if err != nil {
		return nil, fmt.Errorf("parsing roles list: %w", err)
	}

	return &list, nil
}

// Delete implements capi.RolesClient.Delete.
//
// CF v3 DELETE /v3/roles/{guid} is async: 202 Accepted with a
// Location header pointing at /v3/jobs/{jobGuid}. We extract the
// job GUID from the header and return a Job with its GUID populated;
// callers use Jobs().Get or Jobs().PollUntilComplete for full state.
// Same pattern as Apps().Delete and Routes().Delete.
//
// The prior implementation discarded the Location header, leaving
// callers with no way to observe completion. For fast role deletes
// this was harmless, but slow operations would silently appear to
// succeed before V3 finished the work.
func (c *RolesClient) Delete(ctx context.Context, guid string) (*capi.Job, error) {
	path := "/v3/roles/" + guid

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting role: %w", err)
	}

	return jobFromLocationHeader(resp, "deleting role")
}
