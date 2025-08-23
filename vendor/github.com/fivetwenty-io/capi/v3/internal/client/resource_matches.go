package client

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// ResourceMatchesClient implements capi.ResourceMatchesClient
type ResourceMatchesClient struct {
	httpClient *http.Client
}

// NewResourceMatchesClient creates a new resource matches client
func NewResourceMatchesClient(httpClient *http.Client) *ResourceMatchesClient {
	return &ResourceMatchesClient{
		httpClient: httpClient,
	}
}

// Create implements capi.ResourceMatchesClient.Create
func (c *ResourceMatchesClient) Create(ctx context.Context, request *capi.ResourceMatchesRequest) (*capi.ResourceMatches, error) {
	resp, err := c.httpClient.Post(ctx, "/v3/resource_matches", request)
	if err != nil {
		return nil, fmt.Errorf("creating resource matches: %w", err)
	}

	var matches capi.ResourceMatches
	if err := json.Unmarshal(resp.Body, &matches); err != nil {
		return nil, fmt.Errorf("parsing resource matches response: %w", err)
	}

	return &matches, nil
}
