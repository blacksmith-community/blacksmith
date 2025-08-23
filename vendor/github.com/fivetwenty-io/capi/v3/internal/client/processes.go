package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// ProcessesClient implements the capi.ProcessesClient interface
type ProcessesClient struct {
	httpClient *http.Client
}

// NewProcessesClient creates a new processes client
func NewProcessesClient(httpClient *http.Client) *ProcessesClient {
	return &ProcessesClient{
		httpClient: httpClient,
	}
}

// Get retrieves a specific process by GUID
func (c *ProcessesClient) Get(ctx context.Context, guid string) (*capi.Process, error) {
	path := fmt.Sprintf("/v3/processes/%s", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting process: %w", err)
	}

	var process capi.Process
	if err := json.Unmarshal(resp.Body, &process); err != nil {
		return nil, fmt.Errorf("parsing process response: %w", err)
	}

	return &process, nil
}

// List retrieves all processes with optional filtering
func (c *ProcessesClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.Process], error) {
	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, "/v3/processes", query)
	if err != nil {
		return nil, fmt.Errorf("listing processes: %w", err)
	}

	var result capi.ListResponse[capi.Process]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing processes list response: %w", err)
	}

	return &result, nil
}

// Update modifies a process
func (c *ProcessesClient) Update(ctx context.Context, guid string, request *capi.ProcessUpdateRequest) (*capi.Process, error) {
	path := fmt.Sprintf("/v3/processes/%s", guid)
	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating process: %w", err)
	}

	var process capi.Process
	if err := json.Unmarshal(resp.Body, &process); err != nil {
		return nil, fmt.Errorf("parsing process response: %w", err)
	}

	return &process, nil
}

// Scale adjusts the instances, memory, disk, or log rate limit of a process
func (c *ProcessesClient) Scale(ctx context.Context, guid string, request *capi.ProcessScaleRequest) (*capi.Process, error) {
	path := fmt.Sprintf("/v3/processes/%s/actions/scale", guid)
	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("scaling process: %w", err)
	}

	var process capi.Process
	if err := json.Unmarshal(resp.Body, &process); err != nil {
		return nil, fmt.Errorf("parsing process response: %w", err)
	}

	return &process, nil
}

// GetStats retrieves runtime statistics for all instances of a process
func (c *ProcessesClient) GetStats(ctx context.Context, guid string) (*capi.ProcessStats, error) {
	path := fmt.Sprintf("/v3/processes/%s/stats", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting process stats: %w", err)
	}

	var stats capi.ProcessStats
	if err := json.Unmarshal(resp.Body, &stats); err != nil {
		return nil, fmt.Errorf("parsing process stats response: %w", err)
	}

	return &stats, nil
}

// TerminateInstance terminates a specific instance of a process
func (c *ProcessesClient) TerminateInstance(ctx context.Context, guid string, index int) error {
	path := fmt.Sprintf("/v3/processes/%s/instances/%d", guid, index)
	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("terminating process instance: %w", err)
	}

	return nil
}
