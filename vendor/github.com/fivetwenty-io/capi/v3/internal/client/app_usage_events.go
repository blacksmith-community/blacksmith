package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// AppUsageEventsClient implements capi.AppUsageEventsClient
type AppUsageEventsClient struct {
	httpClient *http.Client
}

// NewAppUsageEventsClient creates a new app usage events client
func NewAppUsageEventsClient(httpClient *http.Client) *AppUsageEventsClient {
	return &AppUsageEventsClient{
		httpClient: httpClient,
	}
}

// Get implements capi.AppUsageEventsClient.Get
func (c *AppUsageEventsClient) Get(ctx context.Context, guid string) (*capi.AppUsageEvent, error) {
	path := fmt.Sprintf("/v3/app_usage_events/%s", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting app usage event: %w", err)
	}

	var event capi.AppUsageEvent
	if err := json.Unmarshal(resp.Body, &event); err != nil {
		return nil, fmt.Errorf("parsing app usage event response: %w", err)
	}

	return &event, nil
}

// List implements capi.AppUsageEventsClient.List
func (c *AppUsageEventsClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.AppUsageEvent], error) {
	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, "/v3/app_usage_events", query)
	if err != nil {
		return nil, fmt.Errorf("listing app usage events: %w", err)
	}

	var result capi.ListResponse[capi.AppUsageEvent]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing app usage events list response: %w", err)
	}

	return &result, nil
}

// PurgeAndReseed implements capi.AppUsageEventsClient.PurgeAndReseed
func (c *AppUsageEventsClient) PurgeAndReseed(ctx context.Context) error {
	_, err := c.httpClient.Post(ctx, "/v3/app_usage_events/actions/destructively_purge_all_and_reseed", nil)
	if err != nil {
		return fmt.Errorf("purging and reseeding app usage events: %w", err)
	}

	return nil
}
