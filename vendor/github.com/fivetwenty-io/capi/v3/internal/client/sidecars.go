package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// SidecarsClient implements capi.SidecarsClient
type SidecarsClient struct {
	httpClient *http.Client
}

// NewSidecarsClient creates a new sidecars client
func NewSidecarsClient(httpClient *http.Client) *SidecarsClient {
	return &SidecarsClient{
		httpClient: httpClient,
	}
}

// Get implements capi.SidecarsClient.Get
func (c *SidecarsClient) Get(ctx context.Context, guid string) (*capi.Sidecar, error) {
	path := fmt.Sprintf("/v3/sidecars/%s", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting sidecar: %w", err)
	}

	var sidecar capi.Sidecar
	if err := json.Unmarshal(resp.Body, &sidecar); err != nil {
		return nil, fmt.Errorf("parsing sidecar response: %w", err)
	}

	return &sidecar, nil
}

// Update implements capi.SidecarsClient.Update
func (c *SidecarsClient) Update(ctx context.Context, guid string, request *capi.SidecarUpdateRequest) (*capi.Sidecar, error) {
	path := fmt.Sprintf("/v3/sidecars/%s", guid)
	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating sidecar: %w", err)
	}

	var sidecar capi.Sidecar
	if err := json.Unmarshal(resp.Body, &sidecar); err != nil {
		return nil, fmt.Errorf("parsing sidecar response: %w", err)
	}

	return &sidecar, nil
}

// Delete implements capi.SidecarsClient.Delete
func (c *SidecarsClient) Delete(ctx context.Context, guid string) error {
	path := fmt.Sprintf("/v3/sidecars/%s", guid)
	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("deleting sidecar: %w", err)
	}

	return nil
}

// ListForProcess implements capi.SidecarsClient.ListForProcess
func (c *SidecarsClient) ListForProcess(ctx context.Context, processGUID string, params *capi.QueryParams) (*capi.ListResponse[capi.Sidecar], error) {
	path := fmt.Sprintf("/v3/processes/%s/sidecars", processGUID)

	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("listing sidecars for process: %w", err)
	}

	var result capi.ListResponse[capi.Sidecar]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing sidecars list response: %w", err)
	}

	return &result, nil
}
