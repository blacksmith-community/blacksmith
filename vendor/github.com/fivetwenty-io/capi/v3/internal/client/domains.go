package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// DomainsClient implements the capi.DomainsClient interface
type DomainsClient struct {
	httpClient *http.Client
}

// NewDomainsClient creates a new DomainsClient
func NewDomainsClient(httpClient *http.Client) *DomainsClient {
	return &DomainsClient{
		httpClient: httpClient,
	}
}

// Create creates a new domain
func (c *DomainsClient) Create(ctx context.Context, request *capi.DomainCreateRequest) (*capi.Domain, error) {
	path := "/v3/domains"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating domain: %w", err)
	}

	var domain capi.Domain
	if err := json.Unmarshal(resp.Body, &domain); err != nil {
		return nil, fmt.Errorf("parsing domain response: %w", err)
	}

	return &domain, nil
}

// Get retrieves a specific domain
func (c *DomainsClient) Get(ctx context.Context, guid string) (*capi.Domain, error) {
	path := fmt.Sprintf("/v3/domains/%s", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting domain: %w", err)
	}

	var domain capi.Domain
	if err := json.Unmarshal(resp.Body, &domain); err != nil {
		return nil, fmt.Errorf("parsing domain response: %w", err)
	}

	return &domain, nil
}

// List lists all domains
func (c *DomainsClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.Domain], error) {
	path := "/v3/domains"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing domains: %w", err)
	}

	var result capi.ListResponse[capi.Domain]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing domains list response: %w", err)
	}

	return &result, nil
}

// Update updates a domain's metadata
func (c *DomainsClient) Update(ctx context.Context, guid string, request *capi.DomainUpdateRequest) (*capi.Domain, error) {
	path := fmt.Sprintf("/v3/domains/%s", guid)

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating domain: %w", err)
	}

	var domain capi.Domain
	if err := json.Unmarshal(resp.Body, &domain); err != nil {
		return nil, fmt.Errorf("parsing domain response: %w", err)
	}

	return &domain, nil
}

// Delete deletes a domain
func (c *DomainsClient) Delete(ctx context.Context, guid string) (*capi.Job, error) {
	path := fmt.Sprintf("/v3/domains/%s", guid)

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting domain: %w", err)
	}

	var job capi.Job
	if err := json.Unmarshal(resp.Body, &job); err != nil {
		return nil, fmt.Errorf("parsing job response: %w", err)
	}

	return &job, nil
}

// ShareWithOrganization shares a domain with specified organizations
func (c *DomainsClient) ShareWithOrganization(ctx context.Context, guid string, orgGUIDs []string) (*capi.ToManyRelationship, error) {
	path := fmt.Sprintf("/v3/domains/%s/relationships/shared_organizations", guid)

	// Build the request body with organization GUIDs
	data := make([]capi.RelationshipData, len(orgGUIDs))
	for i, orgGUID := range orgGUIDs {
		data[i] = capi.RelationshipData{GUID: orgGUID}
	}

	request := struct {
		Data []capi.RelationshipData `json:"data"`
	}{
		Data: data,
	}

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("sharing domain with organizations: %w", err)
	}

	var relationship capi.ToManyRelationship
	if err := json.Unmarshal(resp.Body, &relationship); err != nil {
		return nil, fmt.Errorf("parsing relationship response: %w", err)
	}

	return &relationship, nil
}

// UnshareFromOrganization unshares a domain from a specific organization
func (c *DomainsClient) UnshareFromOrganization(ctx context.Context, guid string, orgGUID string) error {
	path := fmt.Sprintf("/v3/domains/%s/relationships/shared_organizations/%s", guid, orgGUID)

	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("unsharing domain from organization: %w", err)
	}

	return nil
}

// CheckRouteReservations checks if a route is reserved for a domain
func (c *DomainsClient) CheckRouteReservations(ctx context.Context, guid string, request *capi.RouteReservationRequest) (*capi.RouteReservation, error) {
	path := fmt.Sprintf("/v3/domains/%s/route_reservations", guid)

	// Build query parameters from the request
	queryParams := url.Values{}
	if request.Host != "" {
		queryParams.Set("host", request.Host)
	}
	if request.Path != "" {
		queryParams.Set("path", request.Path)
	}
	if request.Port != nil {
		queryParams.Set("port", fmt.Sprintf("%d", *request.Port))
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("checking route reservations: %w", err)
	}

	var reservation capi.RouteReservation
	if err := json.Unmarshal(resp.Body, &reservation); err != nil {
		return nil, fmt.Errorf("parsing route reservation response: %w", err)
	}

	return &reservation, nil
}
