package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// UsersClient implements capi.UsersClient
type UsersClient struct {
	httpClient *http.Client
}

// NewUsersClient creates a new users client
func NewUsersClient(httpClient *http.Client) *UsersClient {
	return &UsersClient{
		httpClient: httpClient,
	}
}

// Create implements capi.UsersClient.Create
func (c *UsersClient) Create(ctx context.Context, request *capi.UserCreateRequest) (*capi.User, error) {
	resp, err := c.httpClient.Post(ctx, "/v3/users", request)
	if err != nil {
		return nil, fmt.Errorf("creating user: %w", err)
	}

	var user capi.User
	if err := json.Unmarshal(resp.Body, &user); err != nil {
		return nil, fmt.Errorf("parsing user: %w", err)
	}

	return &user, nil
}

// Get implements capi.UsersClient.Get
func (c *UsersClient) Get(ctx context.Context, guid string) (*capi.User, error) {
	path := fmt.Sprintf("/v3/users/%s", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting user: %w", err)
	}

	var user capi.User
	if err := json.Unmarshal(resp.Body, &user); err != nil {
		return nil, fmt.Errorf("parsing user: %w", err)
	}

	return &user, nil
}

// List implements capi.UsersClient.List
func (c *UsersClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.User], error) {
	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, "/v3/users", queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing users: %w", err)
	}

	var users capi.ListResponse[capi.User]
	if err := json.Unmarshal(resp.Body, &users); err != nil {
		return nil, fmt.Errorf("parsing users list: %w", err)
	}

	return &users, nil
}

// Update implements capi.UsersClient.Update
func (c *UsersClient) Update(ctx context.Context, guid string, request *capi.UserUpdateRequest) (*capi.User, error) {
	path := fmt.Sprintf("/v3/users/%s", guid)

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating user: %w", err)
	}

	var user capi.User
	if err := json.Unmarshal(resp.Body, &user); err != nil {
		return nil, fmt.Errorf("parsing user: %w", err)
	}

	return &user, nil
}

// Delete implements capi.UsersClient.Delete
func (c *UsersClient) Delete(ctx context.Context, guid string) (*capi.Job, error) {
	path := fmt.Sprintf("/v3/users/%s", guid)

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting user: %w", err)
	}

	// User deletion returns a job for async processing
	var job capi.Job
	if err := json.Unmarshal(resp.Body, &job); err != nil {
		return nil, fmt.Errorf("parsing job: %w", err)
	}

	return &job, nil
}
