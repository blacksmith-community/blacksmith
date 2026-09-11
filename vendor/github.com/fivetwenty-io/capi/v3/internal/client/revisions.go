package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// RevisionsClient implements capi.RevisionsClient.
type RevisionsClient struct {
	httpClient *http.Client
}

// NewRevisionsClient creates a new revisions client.
func NewRevisionsClient(httpClient *http.Client) *RevisionsClient {
	return &RevisionsClient{
		httpClient: httpClient,
	}
}

// Get implements capi.RevisionsClient.Get.
func (c *RevisionsClient) Get(ctx context.Context, guid string) (*capi.Revision, error) {
	path := "/v3/revisions/" + guid

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting revision: %w", err)
	}

	var revision capi.Revision

	err = json.Unmarshal(resp.Body, &revision)
	if err != nil {
		return nil, fmt.Errorf("parsing revision response: %w", err)
	}

	return &revision, nil
}

// Update implements capi.RevisionsClient.Update.
func (c *RevisionsClient) Update(ctx context.Context, guid string, request *capi.RevisionUpdateRequest) (*capi.Revision, error) {
	path := "/v3/revisions/" + guid

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating revision: %w", err)
	}

	var revision capi.Revision

	err = json.Unmarshal(resp.Body, &revision)
	if err != nil {
		return nil, fmt.Errorf("parsing revision response: %w", err)
	}

	return &revision, nil
}

// GetEnvironmentVariables implements capi.RevisionsClient.GetEnvironmentVariables.
func (c *RevisionsClient) GetEnvironmentVariables(ctx context.Context, guid string) (map[string]any, error) {
	path := fmt.Sprintf("/v3/revisions/%s/environment_variables", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting revision environment variables: %w", err)
	}

	// The response has a 'var' field that contains the environment variables
	var result struct {
		Var map[string]any `json:"var"`
	}

	err = json.Unmarshal(resp.Body, &result)
	if err != nil {
		return nil, fmt.Errorf("parsing environment variables response: %w", err)
	}

	return result.Var, nil
}

// ListForApp implements capi.RevisionsClient.ListForApp.
func (c *RevisionsClient) ListForApp(ctx context.Context, appGUID string, params *capi.QueryParams) (*capi.ListResponse[capi.Revision], error) {
	path := fmt.Sprintf("/v3/apps/%s/revisions", appGUID)

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing revisions for app: %w", err)
	}

	var result capi.ListResponse[capi.Revision]

	err = json.Unmarshal(resp.Body, &result)
	if err != nil {
		return nil, fmt.Errorf("parsing revisions list response: %w", err)
	}

	return &result, nil
}

// GetDeployedForApp implements capi.RevisionsClient.GetDeployedForApp.
func (c *RevisionsClient) GetDeployedForApp(ctx context.Context, appGUID string) (*capi.ListResponse[capi.Revision], error) {
	path := fmt.Sprintf("/v3/apps/%s/revisions/deployed", appGUID)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting deployed revisions for app: %w", err)
	}

	var result capi.ListResponse[capi.Revision]

	err = json.Unmarshal(resp.Body, &result)
	if err != nil {
		return nil, fmt.Errorf("parsing deployed revisions response: %w", err)
	}

	return &result, nil
}
