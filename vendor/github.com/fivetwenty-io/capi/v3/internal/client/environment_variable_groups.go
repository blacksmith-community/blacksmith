package client

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// EnvironmentVariableGroupsClient implements capi.EnvironmentVariableGroupsClient
type EnvironmentVariableGroupsClient struct {
	httpClient *http.Client
}

// NewEnvironmentVariableGroupsClient creates a new environment variable groups client
func NewEnvironmentVariableGroupsClient(httpClient *http.Client) *EnvironmentVariableGroupsClient {
	return &EnvironmentVariableGroupsClient{
		httpClient: httpClient,
	}
}

// Get implements capi.EnvironmentVariableGroupsClient.Get
func (c *EnvironmentVariableGroupsClient) Get(ctx context.Context, name string) (*capi.EnvironmentVariableGroup, error) {
	path := fmt.Sprintf("/v3/environment_variable_groups/%s", name)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting environment variable group: %w", err)
	}

	var group capi.EnvironmentVariableGroup
	if err := json.Unmarshal(resp.Body, &group); err != nil {
		return nil, fmt.Errorf("parsing environment variable group response: %w", err)
	}

	return &group, nil
}

// Update implements capi.EnvironmentVariableGroupsClient.Update
func (c *EnvironmentVariableGroupsClient) Update(ctx context.Context, name string, envVars map[string]interface{}) (*capi.EnvironmentVariableGroup, error) {
	path := fmt.Sprintf("/v3/environment_variable_groups/%s", name)

	// Wrap the variables in a 'var' field as required by the API
	body := map[string]interface{}{
		"var": envVars,
	}

	resp, err := c.httpClient.Patch(ctx, path, body)
	if err != nil {
		return nil, fmt.Errorf("updating environment variable group: %w", err)
	}

	var group capi.EnvironmentVariableGroup
	if err := json.Unmarshal(resp.Body, &group); err != nil {
		return nil, fmt.Errorf("parsing environment variable group response: %w", err)
	}

	return &group, nil
}
