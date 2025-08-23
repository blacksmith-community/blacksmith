package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// ServicePlansClient implements the capi.ServicePlansClient interface
type ServicePlansClient struct {
	httpClient *http.Client
}

// NewServicePlansClient creates a new ServicePlansClient
func NewServicePlansClient(httpClient *http.Client) *ServicePlansClient {
	return &ServicePlansClient{
		httpClient: httpClient,
	}
}

// Get retrieves a specific service plan
func (c *ServicePlansClient) Get(ctx context.Context, guid string) (*capi.ServicePlan, error) {
	path := fmt.Sprintf("/v3/service_plans/%s", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting service plan: %w", err)
	}

	var plan capi.ServicePlan
	if err := json.Unmarshal(resp.Body, &plan); err != nil {
		return nil, fmt.Errorf("parsing service plan response: %w", err)
	}

	return &plan, nil
}

// List lists all service plans
func (c *ServicePlansClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.ServicePlan], error) {
	path := "/v3/service_plans"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing service plans: %w", err)
	}

	var result capi.ListResponse[capi.ServicePlan]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing service plans list response: %w", err)
	}

	return &result, nil
}

// Update updates a service plan (metadata only)
func (c *ServicePlansClient) Update(ctx context.Context, guid string, request *capi.ServicePlanUpdateRequest) (*capi.ServicePlan, error) {
	path := fmt.Sprintf("/v3/service_plans/%s", guid)

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating service plan: %w", err)
	}

	var plan capi.ServicePlan
	if err := json.Unmarshal(resp.Body, &plan); err != nil {
		return nil, fmt.Errorf("parsing service plan response: %w", err)
	}

	return &plan, nil
}

// Delete deletes a service plan
func (c *ServicePlansClient) Delete(ctx context.Context, guid string) error {
	path := fmt.Sprintf("/v3/service_plans/%s", guid)

	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("deleting service plan: %w", err)
	}

	return nil
}

// GetVisibility retrieves the visibility settings for a service plan
func (c *ServicePlansClient) GetVisibility(ctx context.Context, guid string) (*capi.ServicePlanVisibility, error) {
	path := fmt.Sprintf("/v3/service_plans/%s/visibility", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting service plan visibility: %w", err)
	}

	var visibility capi.ServicePlanVisibility
	if err := json.Unmarshal(resp.Body, &visibility); err != nil {
		return nil, fmt.Errorf("parsing service plan visibility response: %w", err)
	}

	return &visibility, nil
}

// UpdateVisibility updates the visibility settings for a service plan
func (c *ServicePlansClient) UpdateVisibility(ctx context.Context, guid string, request *capi.ServicePlanVisibilityUpdateRequest) (*capi.ServicePlanVisibility, error) {
	path := fmt.Sprintf("/v3/service_plans/%s/visibility", guid)

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating service plan visibility: %w", err)
	}

	var visibility capi.ServicePlanVisibility
	if err := json.Unmarshal(resp.Body, &visibility); err != nil {
		return nil, fmt.Errorf("parsing service plan visibility response: %w", err)
	}

	return &visibility, nil
}

// ApplyVisibility applies visibility settings to a service plan
func (c *ServicePlansClient) ApplyVisibility(ctx context.Context, guid string, request *capi.ServicePlanVisibilityApplyRequest) (*capi.ServicePlanVisibility, error) {
	path := fmt.Sprintf("/v3/service_plans/%s/visibility", guid)

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("applying service plan visibility: %w", err)
	}

	var visibility capi.ServicePlanVisibility
	if err := json.Unmarshal(resp.Body, &visibility); err != nil {
		return nil, fmt.Errorf("parsing service plan visibility response: %w", err)
	}

	return &visibility, nil
}

// RemoveOrgFromVisibility removes an organization from the service plan visibility
func (c *ServicePlansClient) RemoveOrgFromVisibility(ctx context.Context, guid string, orgGUID string) error {
	path := fmt.Sprintf("/v3/service_plans/%s/visibility/%s", guid, orgGUID)

	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("removing organization from service plan visibility: %w", err)
	}

	return nil
}
