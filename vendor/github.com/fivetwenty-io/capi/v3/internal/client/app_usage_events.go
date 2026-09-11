package client

import (
	"context"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// AppUsageEventsClient implements capi.AppUsageEventsClient.
type AppUsageEventsClient struct {
	*UsageEventsClient[capi.AppUsageEvent]
}

// NewAppUsageEventsClient creates a new app usage events client.
func NewAppUsageEventsClient(httpClient *http.Client) *AppUsageEventsClient {
	return &AppUsageEventsClient{
		UsageEventsClient: NewUsageEventsClient[capi.AppUsageEvent](
			httpClient,
			"/v3/app_usage_events",
			"app",
		),
	}
}

// List implements capi.AppUsageEventsClient.List with typed filter options.
func (c *AppUsageEventsClient) List(ctx context.Context, params *capi.QueryParams, opts ...capi.AppUsageEventListOption) (*capi.ListResponse[capi.AppUsageEvent], error) {
	return c.listWithOptions(ctx, params, widenUsageEventOptions(opts))
}
