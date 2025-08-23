package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// ServiceUsageEventsClient implements capi.ServiceUsageEventsClient
type ServiceUsageEventsClient struct {
	httpClient *http.Client
}

// NewServiceUsageEventsClient creates a new service usage events client
func NewServiceUsageEventsClient(httpClient *http.Client) *ServiceUsageEventsClient {
	return &ServiceUsageEventsClient{
		httpClient: httpClient,
	}
}

// Get implements capi.ServiceUsageEventsClient.Get
func (c *ServiceUsageEventsClient) Get(ctx context.Context, guid string) (*capi.ServiceUsageEvent, error) {
	path := fmt.Sprintf("/v3/service_usage_events/%s", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting service usage event: %w", err)
	}

	var event capi.ServiceUsageEvent
	if err := json.Unmarshal(resp.Body, &event); err != nil {
		return nil, fmt.Errorf("parsing service usage event response: %w", err)
	}

	return &event, nil
}

// List implements capi.ServiceUsageEventsClient.List
func (c *ServiceUsageEventsClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.ServiceUsageEvent], error) {
	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, "/v3/service_usage_events", query)
	if err != nil {
		return nil, fmt.Errorf("listing service usage events: %w", err)
	}

	var result capi.ListResponse[capi.ServiceUsageEvent]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing service usage events list response: %w", err)
	}

	return &result, nil
}

// PurgeAndReseed implements capi.ServiceUsageEventsClient.PurgeAndReseed
func (c *ServiceUsageEventsClient) PurgeAndReseed(ctx context.Context) error {
	_, err := c.httpClient.Post(ctx, "/v3/service_usage_events/actions/destructively_purge_all_and_reseed", nil)
	if err != nil {
		return fmt.Errorf("purging and reseeding service usage events: %w", err)
	}

	return nil
}
