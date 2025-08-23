package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// OrganizationsClient implements capi.OrganizationsClient
type OrganizationsClient struct {
	httpClient *http.Client
}

// NewOrganizationsClient creates a new organizations client
func NewOrganizationsClient(httpClient *http.Client) *OrganizationsClient {
	return &OrganizationsClient{
		httpClient: httpClient,
	}
}

// Create implements capi.OrganizationsClient.Create
func (c *OrganizationsClient) Create(ctx context.Context, request *capi.OrganizationCreateRequest) (*capi.Organization, error) {
	resp, err := c.httpClient.Post(ctx, "/v3/organizations", request)
	if err != nil {
		return nil, fmt.Errorf("creating organization: %w", err)
	}

	var org capi.Organization
	if err := json.Unmarshal(resp.Body, &org); err != nil {
		return nil, fmt.Errorf("parsing organization response: %w", err)
	}

	return &org, nil
}

// Get implements capi.OrganizationsClient.Get
func (c *OrganizationsClient) Get(ctx context.Context, guid string) (*capi.Organization, error) {
	path := fmt.Sprintf("/v3/organizations/%s", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting organization: %w", err)
	}

	var org capi.Organization
	if err := json.Unmarshal(resp.Body, &org); err != nil {
		return nil, fmt.Errorf("parsing organization response: %w", err)
	}

	return &org, nil
}

// List implements capi.OrganizationsClient.List
func (c *OrganizationsClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.Organization], error) {
	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, "/v3/organizations", query)
	if err != nil {
		return nil, fmt.Errorf("listing organizations: %w", err)
	}

	var result capi.ListResponse[capi.Organization]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing organizations list response: %w", err)
	}

	return &result, nil
}

// Update implements capi.OrganizationsClient.Update
func (c *OrganizationsClient) Update(ctx context.Context, guid string, request *capi.OrganizationUpdateRequest) (*capi.Organization, error) {
	path := fmt.Sprintf("/v3/organizations/%s", guid)
	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating organization: %w", err)
	}

	var org capi.Organization
	if err := json.Unmarshal(resp.Body, &org); err != nil {
		return nil, fmt.Errorf("parsing organization response: %w", err)
	}

	return &org, nil
}

// Delete implements capi.OrganizationsClient.Delete
func (c *OrganizationsClient) Delete(ctx context.Context, guid string) (*capi.Job, error) {
	path := fmt.Sprintf("/v3/organizations/%s", guid)
	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting organization: %w", err)
	}

	// Organization deletion returns a job for async operation
	var job capi.Job
	if err := json.Unmarshal(resp.Body, &job); err != nil {
		return nil, fmt.Errorf("parsing job response: %w", err)
	}

	return &job, nil
}

// GetDefaultIsolationSegment implements capi.OrganizationsClient.GetDefaultIsolationSegment
func (c *OrganizationsClient) GetDefaultIsolationSegment(ctx context.Context, guid string) (*capi.Relationship, error) {
	path := fmt.Sprintf("/v3/organizations/%s/relationships/default_isolation_segment", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting default isolation segment: %w", err)
	}

	var relationship capi.Relationship
	if err := json.Unmarshal(resp.Body, &relationship); err != nil {
		return nil, fmt.Errorf("parsing relationship response: %w", err)
	}

	return &relationship, nil
}

// SetDefaultIsolationSegment implements capi.OrganizationsClient.SetDefaultIsolationSegment
func (c *OrganizationsClient) SetDefaultIsolationSegment(ctx context.Context, guid string, isolationSegmentGUID string) (*capi.Relationship, error) {
	path := fmt.Sprintf("/v3/organizations/%s/relationships/default_isolation_segment", guid)

	var data *capi.RelationshipData
	if isolationSegmentGUID != "" {
		data = &capi.RelationshipData{GUID: isolationSegmentGUID}
	}

	body := capi.Relationship{Data: data}

	resp, err := c.httpClient.Patch(ctx, path, body)
	if err != nil {
		return nil, fmt.Errorf("setting default isolation segment: %w", err)
	}

	var relationship capi.Relationship
	if err := json.Unmarshal(resp.Body, &relationship); err != nil {
		return nil, fmt.Errorf("parsing relationship response: %w", err)
	}

	return &relationship, nil
}

// GetDefaultDomain implements capi.OrganizationsClient.GetDefaultDomain
func (c *OrganizationsClient) GetDefaultDomain(ctx context.Context, guid string) (*capi.Domain, error) {
	path := fmt.Sprintf("/v3/organizations/%s/domains/default", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting default domain: %w", err)
	}

	var domain capi.Domain
	if err := json.Unmarshal(resp.Body, &domain); err != nil {
		return nil, fmt.Errorf("parsing domain response: %w", err)
	}

	return &domain, nil
}

// GetUsageSummary implements capi.OrganizationsClient.GetUsageSummary
func (c *OrganizationsClient) GetUsageSummary(ctx context.Context, guid string) (*capi.OrganizationUsageSummary, error) {
	path := fmt.Sprintf("/v3/organizations/%s/usage_summary", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting usage summary: %w", err)
	}

	var summary capi.OrganizationUsageSummary
	if err := json.Unmarshal(resp.Body, &summary); err != nil {
		return nil, fmt.Errorf("parsing usage summary response: %w", err)
	}

	return &summary, nil
}

// ListUsers implements capi.OrganizationsClient.ListUsers
func (c *OrganizationsClient) ListUsers(ctx context.Context, guid string, params *capi.QueryParams) (*capi.ListResponse[capi.User], error) {
	path := fmt.Sprintf("/v3/organizations/%s/users", guid)

	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("listing organization users: %w", err)
	}

	var result capi.ListResponse[capi.User]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing users list response: %w", err)
	}

	return &result, nil
}

// ListDomains implements capi.OrganizationsClient.ListDomains
func (c *OrganizationsClient) ListDomains(ctx context.Context, guid string, params *capi.QueryParams) (*capi.ListResponse[capi.Domain], error) {
	path := fmt.Sprintf("/v3/organizations/%s/domains", guid)

	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("listing organization domains: %w", err)
	}

	var result capi.ListResponse[capi.Domain]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing domains list response: %w", err)
	}

	return &result, nil
}
