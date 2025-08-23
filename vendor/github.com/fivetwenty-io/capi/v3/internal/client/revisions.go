package client

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// RevisionsClient implements capi.RevisionsClient
type RevisionsClient struct {
	httpClient *http.Client
}

// NewRevisionsClient creates a new revisions client
func NewRevisionsClient(httpClient *http.Client) *RevisionsClient {
	return &RevisionsClient{
		httpClient: httpClient,
	}
}

// Get implements capi.RevisionsClient.Get
func (c *RevisionsClient) Get(ctx context.Context, guid string) (*capi.Revision, error) {
	path := fmt.Sprintf("/v3/revisions/%s", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting revision: %w", err)
	}

	var revision capi.Revision
	if err := json.Unmarshal(resp.Body, &revision); err != nil {
		return nil, fmt.Errorf("parsing revision response: %w", err)
	}

	return &revision, nil
}

// Update implements capi.RevisionsClient.Update
func (c *RevisionsClient) Update(ctx context.Context, guid string, request *capi.RevisionUpdateRequest) (*capi.Revision, error) {
	path := fmt.Sprintf("/v3/revisions/%s", guid)
	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating revision: %w", err)
	}

	var revision capi.Revision
	if err := json.Unmarshal(resp.Body, &revision); err != nil {
		return nil, fmt.Errorf("parsing revision response: %w", err)
	}

	return &revision, nil
}

// GetEnvironmentVariables implements capi.RevisionsClient.GetEnvironmentVariables
func (c *RevisionsClient) GetEnvironmentVariables(ctx context.Context, guid string) (map[string]interface{}, error) {
	path := fmt.Sprintf("/v3/revisions/%s/environment_variables", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting revision environment variables: %w", err)
	}

	// The response has a 'var' field that contains the environment variables
	var result struct {
		Var map[string]interface{} `json:"var"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing environment variables response: %w", err)
	}

	return result.Var, nil
}
