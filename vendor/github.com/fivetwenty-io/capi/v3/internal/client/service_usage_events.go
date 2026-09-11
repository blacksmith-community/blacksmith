package client

import (
	"context"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// ServiceUsageEventsClient implements capi.ServiceUsageEventsClient.
type ServiceUsageEventsClient struct {
	*UsageEventsClient[capi.ServiceUsageEvent]
}

// NewServiceUsageEventsClient creates a new service usage events client.
func NewServiceUsageEventsClient(httpClient *http.Client) *ServiceUsageEventsClient {
	return &ServiceUsageEventsClient{
		UsageEventsClient: NewUsageEventsClient[capi.ServiceUsageEvent](
			httpClient,
			"/v3/service_usage_events",
			"service",
		),
	}
}

// List implements capi.ServiceUsageEventsClient.List with typed filter options.
func (c *ServiceUsageEventsClient) List(ctx context.Context, params *capi.QueryParams, opts ...capi.ServiceUsageEventListOption) (*capi.ListResponse[capi.ServiceUsageEvent], error) {
	return c.listWithOptions(ctx, params, widenUsageEventOptions(opts))
}
