package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// ServiceBrokersClient implements the capi.ServiceBrokersClient interface
type ServiceBrokersClient struct {
	httpClient *http.Client
}

// NewServiceBrokersClient creates a new ServiceBrokersClient
func NewServiceBrokersClient(httpClient *http.Client) *ServiceBrokersClient {
	return &ServiceBrokersClient{
		httpClient: httpClient,
	}
}

// Create creates a new service broker
func (c *ServiceBrokersClient) Create(ctx context.Context, request *capi.ServiceBrokerCreateRequest) (*capi.Job, error) {
	path := "/v3/service_brokers"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating service broker: %w", err)
	}

	var job capi.Job
	if err := json.Unmarshal(resp.Body, &job); err != nil {
		return nil, fmt.Errorf("parsing job response: %w", err)
	}

	return &job, nil
}

// Get retrieves a specific service broker
func (c *ServiceBrokersClient) Get(ctx context.Context, guid string) (*capi.ServiceBroker, error) {
	path := fmt.Sprintf("/v3/service_brokers/%s", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting service broker: %w", err)
	}

	var broker capi.ServiceBroker
	if err := json.Unmarshal(resp.Body, &broker); err != nil {
		return nil, fmt.Errorf("parsing service broker response: %w", err)
	}

	return &broker, nil
}

// List lists all service brokers
func (c *ServiceBrokersClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.ServiceBroker], error) {
	path := "/v3/service_brokers"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing service brokers: %w", err)
	}

	var result capi.ListResponse[capi.ServiceBroker]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing service brokers list response: %w", err)
	}

	return &result, nil
}

// Update updates a service broker
// This may return a Job if the update triggers a catalog synchronization,
// or a ServiceBroker if only metadata was updated
func (c *ServiceBrokersClient) Update(ctx context.Context, guid string, request *capi.ServiceBrokerUpdateRequest) (*capi.Job, error) {
	path := fmt.Sprintf("/v3/service_brokers/%s", guid)

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating service broker: %w", err)
	}

	// Check if response is a Job (202 Accepted) or ServiceBroker (200 OK)
	if resp.StatusCode == 202 {
		var job capi.Job
		if err := json.Unmarshal(resp.Body, &job); err != nil {
			return nil, fmt.Errorf("parsing job response: %w", err)
		}
		return &job, nil
	}

	// For 200 OK responses (metadata-only updates), we still return a Job
	// to match the interface, but it will be a completed job
	var broker capi.ServiceBroker
	if err := json.Unmarshal(resp.Body, &broker); err != nil {
		return nil, fmt.Errorf("parsing service broker response: %w", err)
	}

	// Create a synthetic completed job for consistency
	job := &capi.Job{
		Resource: capi.Resource{
			GUID:      fmt.Sprintf("sync-job-%s", broker.GUID),
			CreatedAt: broker.UpdatedAt,
			UpdatedAt: broker.UpdatedAt,
		},
		Operation: "service_broker.update",
		State:     "COMPLETE",
	}

	return job, nil
}

// Delete deletes a service broker
func (c *ServiceBrokersClient) Delete(ctx context.Context, guid string) (*capi.Job, error) {
	path := fmt.Sprintf("/v3/service_brokers/%s", guid)

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting service broker: %w", err)
	}

	var job capi.Job
	if err := json.Unmarshal(resp.Body, &job); err != nil {
		return nil, fmt.Errorf("parsing job response: %w", err)
	}

	return &job, nil
}
