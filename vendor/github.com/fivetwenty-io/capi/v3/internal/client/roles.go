package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// RolesClient implements capi.RolesClient
type RolesClient struct {
	httpClient *http.Client
}

// NewRolesClient creates a new roles client
func NewRolesClient(httpClient *http.Client) *RolesClient {
	return &RolesClient{
		httpClient: httpClient,
	}
}

// Create implements capi.RolesClient.Create
func (c *RolesClient) Create(ctx context.Context, request *capi.RoleCreateRequest) (*capi.Role, error) {
	path := "/v3/roles"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating role: %w", err)
	}

	var role capi.Role
	if err := json.Unmarshal(resp.Body, &role); err != nil {
		return nil, fmt.Errorf("parsing role response: %w", err)
	}

	return &role, nil
}

// Get implements capi.RolesClient.Get
func (c *RolesClient) Get(ctx context.Context, guid string) (*capi.Role, error) {
	path := fmt.Sprintf("/v3/roles/%s", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting role: %w", err)
	}

	var role capi.Role
	if err := json.Unmarshal(resp.Body, &role); err != nil {
		return nil, fmt.Errorf("parsing role: %w", err)
	}

	return &role, nil
}

// List implements capi.RolesClient.List
func (c *RolesClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.Role], error) {
	path := "/v3/roles"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing roles: %w", err)
	}

	var list capi.ListResponse[capi.Role]
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		return nil, fmt.Errorf("parsing roles list: %w", err)
	}

	return &list, nil
}

// Delete implements capi.RolesClient.Delete
func (c *RolesClient) Delete(ctx context.Context, guid string) error {
	path := fmt.Sprintf("/v3/roles/%s", guid)

	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("deleting role: %w", err)
	}

	return nil
}
