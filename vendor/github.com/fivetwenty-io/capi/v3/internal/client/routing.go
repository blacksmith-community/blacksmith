package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// ErrGroupTypeRequired is returned when GetRouterGroupByType is called with an
// empty group type. Wrapped so callers can match it with errors.Is.
var ErrGroupTypeRequired = errors.New("groupType must not be empty")

// RoutingClient implements the capi.RoutingClient interface.
// It accesses the CF Routing API endpoints at /routing/v1/ which are
// typically proxied through the same base URL as the CF API v3.
type RoutingClient struct {
	httpClient *http.Client
}

// NewRoutingClient creates a new RoutingClient.
func NewRoutingClient(httpClient *http.Client) *RoutingClient {
	return &RoutingClient{
		httpClient: httpClient,
	}
}

// ListRouterGroups lists all router groups from the CF Routing API.
func (c *RoutingClient) ListRouterGroups(ctx context.Context) ([]capi.RouterGroup, error) {
	path := "/routing/v1/router_groups"

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("listing router groups: %w", err)
	}

	var groups []capi.RouterGroup

	err = json.Unmarshal(resp.Body, &groups)
	if err != nil {
		return nil, fmt.Errorf("parsing router groups response: %w", err)
	}

	return groups, nil
}

// GetRouterGroupByType returns the first router group matching the given type
// (e.g., "tcp" or "http"). Returns nil, nil if no group matches the type.
func (c *RoutingClient) GetRouterGroupByType(ctx context.Context, groupType string) (*capi.RouterGroup, error) {
	if groupType == "" {
		return nil, fmt.Errorf("getting router group by type: %w", ErrGroupTypeRequired)
	}

	path := "/routing/v1/router_groups"

	params := url.Values{}
	params.Set("type", groupType)

	resp, err := c.httpClient.Get(ctx, path, params)
	if err != nil {
		return nil, fmt.Errorf("getting router group by type %q: %w", groupType, err)
	}

	var groups []capi.RouterGroup

	err = json.Unmarshal(resp.Body, &groups)
	if err != nil {
		return nil, fmt.Errorf("parsing router groups response: %w", err)
	}

	if len(groups) == 0 {
		return nil, nil
	}

	return &groups[0], nil
}
