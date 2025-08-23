package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// SpaceQuotasClient implements capi.SpaceQuotasClient
type SpaceQuotasClient struct {
	httpClient *http.Client
}

// NewSpaceQuotasClient creates a new space quotas client
func NewSpaceQuotasClient(httpClient *http.Client) *SpaceQuotasClient {
	return &SpaceQuotasClient{
		httpClient: httpClient,
	}
}

// Create implements capi.SpaceQuotasClient.Create
func (c *SpaceQuotasClient) Create(ctx context.Context, request *capi.SpaceQuotaV3CreateRequest) (*capi.SpaceQuotaV3, error) {
	resp, err := c.httpClient.Post(ctx, "/v3/space_quotas", request)
	if err != nil {
		return nil, fmt.Errorf("creating space quota: %w", err)
	}

	var quota capi.SpaceQuotaV3
	if err := json.Unmarshal(resp.Body, &quota); err != nil {
		return nil, fmt.Errorf("parsing space quota response: %w", err)
	}

	return &quota, nil
}

// Get implements capi.SpaceQuotasClient.Get
func (c *SpaceQuotasClient) Get(ctx context.Context, guid string) (*capi.SpaceQuotaV3, error) {
	path := fmt.Sprintf("/v3/space_quotas/%s", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting space quota: %w", err)
	}

	var quota capi.SpaceQuotaV3
	if err := json.Unmarshal(resp.Body, &quota); err != nil {
		return nil, fmt.Errorf("parsing space quota response: %w", err)
	}

	return &quota, nil
}

// List implements capi.SpaceQuotasClient.List
func (c *SpaceQuotasClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.SpaceQuotaV3], error) {
	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, "/v3/space_quotas", query)
	if err != nil {
		return nil, fmt.Errorf("listing space quotas: %w", err)
	}

	var result capi.ListResponse[capi.SpaceQuotaV3]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing space quotas list response: %w", err)
	}

	return &result, nil
}

// Update implements capi.SpaceQuotasClient.Update
func (c *SpaceQuotasClient) Update(ctx context.Context, guid string, request *capi.SpaceQuotaV3UpdateRequest) (*capi.SpaceQuotaV3, error) {
	path := fmt.Sprintf("/v3/space_quotas/%s", guid)
	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating space quota: %w", err)
	}

	var quota capi.SpaceQuotaV3
	if err := json.Unmarshal(resp.Body, &quota); err != nil {
		return nil, fmt.Errorf("parsing space quota response: %w", err)
	}

	return &quota, nil
}

// Delete implements capi.SpaceQuotasClient.Delete
func (c *SpaceQuotasClient) Delete(ctx context.Context, guid string) error {
	path := fmt.Sprintf("/v3/space_quotas/%s", guid)
	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("deleting space quota: %w", err)
	}

	return nil
}

// ApplyToSpaces implements capi.SpaceQuotasClient.ApplyToSpaces
func (c *SpaceQuotasClient) ApplyToSpaces(ctx context.Context, quotaGUID string, spaceGUIDs []string) (*capi.ToManyRelationship, error) {
	path := fmt.Sprintf("/v3/space_quotas/%s/relationships/spaces", quotaGUID)

	data := make([]capi.RelationshipData, len(spaceGUIDs))
	for i, guid := range spaceGUIDs {
		data[i] = capi.RelationshipData{GUID: guid}
	}

	body := capi.ToManyRelationship{Data: data}

	resp, err := c.httpClient.Post(ctx, path, body)
	if err != nil {
		return nil, fmt.Errorf("applying space quota to spaces: %w", err)
	}

	var relationship capi.ToManyRelationship
	if err := json.Unmarshal(resp.Body, &relationship); err != nil {
		return nil, fmt.Errorf("parsing relationship response: %w", err)
	}

	return &relationship, nil
}

// RemoveFromSpace implements capi.SpaceQuotasClient.RemoveFromSpace
func (c *SpaceQuotasClient) RemoveFromSpace(ctx context.Context, quotaGUID string, spaceGUID string) error {
	path := fmt.Sprintf("/v3/space_quotas/%s/relationships/spaces/%s", quotaGUID, spaceGUID)
	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("removing space from quota: %w", err)
	}

	return nil
}
