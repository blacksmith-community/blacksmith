package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// OrganizationQuotasClient implements capi.OrganizationQuotasClient
type OrganizationQuotasClient struct {
	httpClient *http.Client
}

// NewOrganizationQuotasClient creates a new organization quotas client
func NewOrganizationQuotasClient(httpClient *http.Client) *OrganizationQuotasClient {
	return &OrganizationQuotasClient{
		httpClient: httpClient,
	}
}

// Create implements capi.OrganizationQuotasClient.Create
func (c *OrganizationQuotasClient) Create(ctx context.Context, request *capi.OrganizationQuotaCreateRequest) (*capi.OrganizationQuota, error) {
	resp, err := c.httpClient.Post(ctx, "/v3/organization_quotas", request)
	if err != nil {
		return nil, fmt.Errorf("creating organization quota: %w", err)
	}

	var quota capi.OrganizationQuota
	if err := json.Unmarshal(resp.Body, &quota); err != nil {
		return nil, fmt.Errorf("parsing organization quota response: %w", err)
	}

	return &quota, nil
}

// Get implements capi.OrganizationQuotasClient.Get
func (c *OrganizationQuotasClient) Get(ctx context.Context, guid string) (*capi.OrganizationQuota, error) {
	path := fmt.Sprintf("/v3/organization_quotas/%s", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting organization quota: %w", err)
	}

	var quota capi.OrganizationQuota
	if err := json.Unmarshal(resp.Body, &quota); err != nil {
		return nil, fmt.Errorf("parsing organization quota response: %w", err)
	}

	return &quota, nil
}

// List implements capi.OrganizationQuotasClient.List
func (c *OrganizationQuotasClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.OrganizationQuota], error) {
	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, "/v3/organization_quotas", query)
	if err != nil {
		return nil, fmt.Errorf("listing organization quotas: %w", err)
	}

	var result capi.ListResponse[capi.OrganizationQuota]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing organization quotas list response: %w", err)
	}

	return &result, nil
}

// Update implements capi.OrganizationQuotasClient.Update
func (c *OrganizationQuotasClient) Update(ctx context.Context, guid string, request *capi.OrganizationQuotaUpdateRequest) (*capi.OrganizationQuota, error) {
	path := fmt.Sprintf("/v3/organization_quotas/%s", guid)
	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating organization quota: %w", err)
	}

	var quota capi.OrganizationQuota
	if err := json.Unmarshal(resp.Body, &quota); err != nil {
		return nil, fmt.Errorf("parsing organization quota response: %w", err)
	}

	return &quota, nil
}

// Delete implements capi.OrganizationQuotasClient.Delete
func (c *OrganizationQuotasClient) Delete(ctx context.Context, guid string) error {
	path := fmt.Sprintf("/v3/organization_quotas/%s", guid)
	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("deleting organization quota: %w", err)
	}

	return nil
}

// ApplyToOrganizations implements capi.OrganizationQuotasClient.ApplyToOrganizations
func (c *OrganizationQuotasClient) ApplyToOrganizations(ctx context.Context, quotaGUID string, orgGUIDs []string) (*capi.ToManyRelationship, error) {
	path := fmt.Sprintf("/v3/organization_quotas/%s/relationships/organizations", quotaGUID)

	data := make([]capi.RelationshipData, len(orgGUIDs))
	for i, guid := range orgGUIDs {
		data[i] = capi.RelationshipData{GUID: guid}
	}

	body := capi.ToManyRelationship{Data: data}

	resp, err := c.httpClient.Post(ctx, path, body)
	if err != nil {
		return nil, fmt.Errorf("applying organization quota to organizations: %w", err)
	}

	var relationship capi.ToManyRelationship
	if err := json.Unmarshal(resp.Body, &relationship); err != nil {
		return nil, fmt.Errorf("parsing relationship response: %w", err)
	}

	return &relationship, nil
}
