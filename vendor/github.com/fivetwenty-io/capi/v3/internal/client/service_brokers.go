package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	http_internal "github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// ServiceBrokersClient implements the capi.ServiceBrokersClient interface.
type ServiceBrokersClient struct {
	httpClient *http_internal.Client
}

// NewServiceBrokersClient creates a new ServiceBrokersClient.
func NewServiceBrokersClient(httpClient *http_internal.Client) *ServiceBrokersClient {
	return &ServiceBrokersClient{
		httpClient: httpClient,
	}
}

// Create creates a new service broker.
func (c *ServiceBrokersClient) Create(ctx context.Context, request *capi.ServiceBrokerCreateRequest) (*capi.Job, error) {
	path := "/v3/service_brokers"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating service broker: %w", err)
	}

	// Async: job in body or Location header.
	return jobFromAsyncResponse(resp, "creating service broker")
}

// Get retrieves a specific service broker.
func (c *ServiceBrokersClient) Get(ctx context.Context, guid string) (*capi.ServiceBroker, error) {
	path := "/v3/service_brokers/" + guid

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting service broker: %w", err)
	}

	var broker capi.ServiceBroker

	err = json.Unmarshal(resp.Body, &broker)
	if err != nil {
		return nil, fmt.Errorf("parsing service broker response: %w", err)
	}

	return &broker, nil
}

// List lists all service brokers.
func (c *ServiceBrokersClient) List(ctx context.Context, params *capi.QueryParams, opts ...capi.ServiceBrokerListOption) (*capi.ListResponse[capi.ServiceBroker], error) {
	path := "/v3/service_brokers"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	queryParams = capi.ApplyQueryOptions(queryParams, opts)

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing service brokers: %w", err)
	}

	var result capi.ListResponse[capi.ServiceBroker]

	err = json.Unmarshal(resp.Body, &result)
	if err != nil {
		return nil, fmt.Errorf("parsing service brokers list response: %w", err)
	}

	return &result, nil
}

// Update updates a service broker
// This may return a Job if the update triggers a catalog synchronization,
// or a ServiceBroker if only metadata was updated.
func (c *ServiceBrokersClient) Update(ctx context.Context, guid string, request *capi.ServiceBrokerUpdateRequest) (*capi.Job, error) {
	path := "/v3/service_brokers/" + guid

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating service broker: %w", err)
	}

	// Check if response is a Job (202 Accepted) or ServiceBroker (200 OK)
	if resp.StatusCode == http.StatusAccepted {
		// Async catalog sync: job in body or Location header.
		return jobFromAsyncResponse(resp, "updating service broker")
	}

	// For 200 OK responses (metadata-only updates), we still return a Job
	// to match the interface, but it will be a completed job
	var broker capi.ServiceBroker

	err = json.Unmarshal(resp.Body, &broker)
	if err != nil {
		return nil, fmt.Errorf("parsing service broker response: %w", err)
	}

	// Create a synthetic completed job for consistency
	job := &capi.Job{
		Resource: capi.Resource{
			GUID:      "sync-job-" + broker.GUID,
			CreatedAt: broker.UpdatedAt,
			UpdatedAt: broker.UpdatedAt,
		},
		Operation: "service_broker.update",
		State:     "COMPLETE",
	}

	return job, nil
}

// Delete deletes a service broker.
// CF V3 DELETE /v3/service_brokers/{guid} returns 202 Accepted with an empty
// body and the async job reference in the Location header. See Apps.Delete
// for the canonical Location-extraction pattern.
func (c *ServiceBrokersClient) Delete(ctx context.Context, guid string) (*capi.Job, error) {
	path := "/v3/service_brokers/" + guid

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting service broker: %w", err)
	}

	return jobFromLocationHeader(resp, "deleting service broker")
}
