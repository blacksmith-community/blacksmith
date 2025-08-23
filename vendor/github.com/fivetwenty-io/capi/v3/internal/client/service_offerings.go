package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// ServiceOfferingsClient implements the capi.ServiceOfferingsClient interface
type ServiceOfferingsClient struct {
	httpClient *http.Client
}

// NewServiceOfferingsClient creates a new ServiceOfferingsClient
func NewServiceOfferingsClient(httpClient *http.Client) *ServiceOfferingsClient {
	return &ServiceOfferingsClient{
		httpClient: httpClient,
	}
}

// Get retrieves a specific service offering
func (c *ServiceOfferingsClient) Get(ctx context.Context, guid string) (*capi.ServiceOffering, error) {
	path := fmt.Sprintf("/v3/service_offerings/%s", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting service offering: %w", err)
	}

	var offering capi.ServiceOffering
	if err := json.Unmarshal(resp.Body, &offering); err != nil {
		return nil, fmt.Errorf("parsing service offering response: %w", err)
	}

	return &offering, nil
}

// List lists all service offerings
func (c *ServiceOfferingsClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.ServiceOffering], error) {
	path := "/v3/service_offerings"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing service offerings: %w", err)
	}

	var result capi.ListResponse[capi.ServiceOffering]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing service offerings list response: %w", err)
	}

	return &result, nil
}

// Update updates a service offering (metadata only)
func (c *ServiceOfferingsClient) Update(ctx context.Context, guid string, request *capi.ServiceOfferingUpdateRequest) (*capi.ServiceOffering, error) {
	path := fmt.Sprintf("/v3/service_offerings/%s", guid)

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating service offering: %w", err)
	}

	var offering capi.ServiceOffering
	if err := json.Unmarshal(resp.Body, &offering); err != nil {
		return nil, fmt.Errorf("parsing service offering response: %w", err)
	}

	return &offering, nil
}

// Delete deletes a service offering
// This is typically used to remove orphan service offerings from the Cloud Foundry database
// when they have been removed from the service broker catalog
func (c *ServiceOfferingsClient) Delete(ctx context.Context, guid string) error {
	path := fmt.Sprintf("/v3/service_offerings/%s", guid)

	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("deleting service offering: %w", err)
	}

	return nil
}
