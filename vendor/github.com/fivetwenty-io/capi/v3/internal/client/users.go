package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// UsersClient implements capi.UsersClient.
type UsersClient struct {
	httpClient *http.Client
}

// NewUsersClient creates a new users client.
func NewUsersClient(httpClient *http.Client) *UsersClient {
	return &UsersClient{
		httpClient: httpClient,
	}
}

// Create implements capi.UsersClient.Create.
func (c *UsersClient) Create(ctx context.Context, request *capi.UserCreateRequest) (*capi.User, error) {
	resp, err := c.httpClient.Post(ctx, "/v3/users", request)
	if err != nil {
		return nil, fmt.Errorf("creating user: %w", err)
	}

	var user capi.User

	err = json.Unmarshal(resp.Body, &user)
	if err != nil {
		return nil, fmt.Errorf("parsing user: %w", err)
	}

	return &user, nil
}

// Get implements capi.UsersClient.Get.
func (c *UsersClient) Get(ctx context.Context, guid string) (*capi.User, error) {
	path := "/v3/users/" + guid

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting user: %w", err)
	}

	var user capi.User

	err = json.Unmarshal(resp.Body, &user)
	if err != nil {
		return nil, fmt.Errorf("parsing user: %w", err)
	}

	return &user, nil
}

// List implements capi.UsersClient.List.
func (c *UsersClient) List(ctx context.Context, params *capi.QueryParams, opts ...capi.UserListOption) (*capi.ListResponse[capi.User], error) {
	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	queryParams = capi.ApplyQueryOptions(queryParams, opts)

	resp, err := c.httpClient.Get(ctx, "/v3/users", queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}

	var users capi.ListResponse[capi.User]

	err = json.Unmarshal(resp.Body, &users)
	if err != nil {
		return nil, fmt.Errorf("parsing users list: %w", err)
	}

	return &users, nil
}

// Update implements capi.UsersClient.Update.
func (c *UsersClient) Update(ctx context.Context, guid string, request *capi.UserUpdateRequest) (*capi.User, error) {
	path := "/v3/users/" + guid

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating user: %w", err)
	}

	var user capi.User

	err = json.Unmarshal(resp.Body, &user)
	if err != nil {
		return nil, fmt.Errorf("parsing user: %w", err)
	}

	return &user, nil
}

// Delete implements capi.UsersClient.Delete.
// CF V3 DELETE /v3/users/{guid} returns 202 Accepted with an empty body and
// the async job reference in the Location header. See Apps.Delete for the
// canonical Location-extraction pattern.
func (c *UsersClient) Delete(ctx context.Context, guid string) (*capi.Job, error) {
	path := "/v3/users/" + guid

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting user: %w", err)
	}

	return jobFromLocationHeader(resp, "deleting user")
}
