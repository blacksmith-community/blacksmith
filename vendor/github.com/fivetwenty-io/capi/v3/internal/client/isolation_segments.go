package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// IsolationSegmentsClient implements capi.IsolationSegmentsClient
type IsolationSegmentsClient struct {
	httpClient *http.Client
}

// NewIsolationSegmentsClient creates a new isolation segments client
func NewIsolationSegmentsClient(httpClient *http.Client) *IsolationSegmentsClient {
	return &IsolationSegmentsClient{
		httpClient: httpClient,
	}
}

// Create implements capi.IsolationSegmentsClient.Create
func (c *IsolationSegmentsClient) Create(ctx context.Context, request *capi.IsolationSegmentCreateRequest) (*capi.IsolationSegment, error) {
	path := "/v3/isolation_segments"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating isolation segment: %w", err)
	}

	var is capi.IsolationSegment
	if err := json.Unmarshal(resp.Body, &is); err != nil {
		return nil, fmt.Errorf("parsing isolation segment response: %w", err)
	}

	return &is, nil
}

// Get implements capi.IsolationSegmentsClient.Get
func (c *IsolationSegmentsClient) Get(ctx context.Context, guid string) (*capi.IsolationSegment, error) {
	path := fmt.Sprintf("/v3/isolation_segments/%s", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting isolation segment: %w", err)
	}

	var is capi.IsolationSegment
	if err := json.Unmarshal(resp.Body, &is); err != nil {
		return nil, fmt.Errorf("parsing isolation segment: %w", err)
	}

	return &is, nil
}

// List implements capi.IsolationSegmentsClient.List
func (c *IsolationSegmentsClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.IsolationSegment], error) {
	path := "/v3/isolation_segments"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing isolation segments: %w", err)
	}

	var list capi.ListResponse[capi.IsolationSegment]
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		return nil, fmt.Errorf("parsing isolation segments list: %w", err)
	}

	return &list, nil
}

// Update implements capi.IsolationSegmentsClient.Update
func (c *IsolationSegmentsClient) Update(ctx context.Context, guid string, request *capi.IsolationSegmentUpdateRequest) (*capi.IsolationSegment, error) {
	path := fmt.Sprintf("/v3/isolation_segments/%s", guid)

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating isolation segment: %w", err)
	}

	var is capi.IsolationSegment
	if err := json.Unmarshal(resp.Body, &is); err != nil {
		return nil, fmt.Errorf("parsing isolation segment response: %w", err)
	}

	return &is, nil
}

// Delete implements capi.IsolationSegmentsClient.Delete
func (c *IsolationSegmentsClient) Delete(ctx context.Context, guid string) error {
	path := fmt.Sprintf("/v3/isolation_segments/%s", guid)

	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("deleting isolation segment: %w", err)
	}

	return nil
}

// EntitleOrganizations implements capi.IsolationSegmentsClient.EntitleOrganizations
func (c *IsolationSegmentsClient) EntitleOrganizations(ctx context.Context, guid string, orgGUIDs []string) (*capi.ToManyRelationship, error) {
	path := fmt.Sprintf("/v3/isolation_segments/%s/relationships/organizations", guid)

	request := capi.IsolationSegmentEntitleOrganizationsRequest{
		Data: make([]capi.RelationshipData, len(orgGUIDs)),
	}
	for i, orgGUID := range orgGUIDs {
		request.Data[i] = capi.RelationshipData{GUID: orgGUID}
	}

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("entitling organizations for isolation segment: %w", err)
	}

	var relationship capi.ToManyRelationship
	if err := json.Unmarshal(resp.Body, &relationship); err != nil {
		return nil, fmt.Errorf("parsing relationship response: %w", err)
	}

	return &relationship, nil
}

// RevokeOrganization implements capi.IsolationSegmentsClient.RevokeOrganization
func (c *IsolationSegmentsClient) RevokeOrganization(ctx context.Context, guid string, orgGUID string) error {
	path := fmt.Sprintf("/v3/isolation_segments/%s/relationships/organizations/%s", guid, orgGUID)

	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("revoking organization from isolation segment: %w", err)
	}

	return nil
}

// ListOrganizations implements capi.IsolationSegmentsClient.ListOrganizations
func (c *IsolationSegmentsClient) ListOrganizations(ctx context.Context, guid string) (*capi.ListResponse[capi.Organization], error) {
	path := fmt.Sprintf("/v3/isolation_segments/%s/organizations", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("listing organizations for isolation segment: %w", err)
	}

	var list capi.ListResponse[capi.Organization]
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		return nil, fmt.Errorf("parsing organizations list: %w", err)
	}

	return &list, nil
}

// ListSpaces implements capi.IsolationSegmentsClient.ListSpaces
func (c *IsolationSegmentsClient) ListSpaces(ctx context.Context, guid string) (*capi.ListResponse[capi.Space], error) {
	path := fmt.Sprintf("/v3/isolation_segments/%s/relationships/spaces", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("listing spaces for isolation segment: %w", err)
	}

	var list capi.ListResponse[capi.Space]
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		return nil, fmt.Errorf("parsing spaces list: %w", err)
	}

	return &list, nil
}
