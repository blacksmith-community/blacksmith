package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// SpacesClient implements capi.SpacesClient
type SpacesClient struct {
	httpClient *http.Client
}

// NewSpacesClient creates a new spaces client
func NewSpacesClient(httpClient *http.Client) *SpacesClient {
	return &SpacesClient{
		httpClient: httpClient,
	}
}

// Create implements capi.SpacesClient.Create
func (c *SpacesClient) Create(ctx context.Context, request *capi.SpaceCreateRequest) (*capi.Space, error) {
	resp, err := c.httpClient.Post(ctx, "/v3/spaces", request)
	if err != nil {
		return nil, fmt.Errorf("creating space: %w", err)
	}

	var space capi.Space
	if err := json.Unmarshal(resp.Body, &space); err != nil {
		return nil, fmt.Errorf("parsing space response: %w", err)
	}

	return &space, nil
}

// Get implements capi.SpacesClient.Get
func (c *SpacesClient) Get(ctx context.Context, guid string) (*capi.Space, error) {
	path := fmt.Sprintf("/v3/spaces/%s", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting space: %w", err)
	}

	var space capi.Space
	if err := json.Unmarshal(resp.Body, &space); err != nil {
		return nil, fmt.Errorf("parsing space response: %w", err)
	}

	return &space, nil
}

// List implements capi.SpacesClient.List
func (c *SpacesClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.Space], error) {
	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, "/v3/spaces", query)
	if err != nil {
		return nil, fmt.Errorf("listing spaces: %w", err)
	}

	var result capi.ListResponse[capi.Space]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing spaces list response: %w", err)
	}

	return &result, nil
}

// Update implements capi.SpacesClient.Update
func (c *SpacesClient) Update(ctx context.Context, guid string, request *capi.SpaceUpdateRequest) (*capi.Space, error) {
	path := fmt.Sprintf("/v3/spaces/%s", guid)
	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating space: %w", err)
	}

	var space capi.Space
	if err := json.Unmarshal(resp.Body, &space); err != nil {
		return nil, fmt.Errorf("parsing space response: %w", err)
	}

	return &space, nil
}

// Delete implements capi.SpacesClient.Delete
func (c *SpacesClient) Delete(ctx context.Context, guid string) (*capi.Job, error) {
	path := fmt.Sprintf("/v3/spaces/%s", guid)
	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting space: %w", err)
	}

	// Space deletion returns a job for async operation
	var job capi.Job
	if err := json.Unmarshal(resp.Body, &job); err != nil {
		return nil, fmt.Errorf("parsing job response: %w", err)
	}

	return &job, nil
}

// GetIsolationSegment implements capi.SpacesClient.GetIsolationSegment
func (c *SpacesClient) GetIsolationSegment(ctx context.Context, guid string) (*capi.Relationship, error) {
	path := fmt.Sprintf("/v3/spaces/%s/relationships/isolation_segment", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting isolation segment: %w", err)
	}

	var relationship capi.Relationship
	if err := json.Unmarshal(resp.Body, &relationship); err != nil {
		return nil, fmt.Errorf("parsing relationship response: %w", err)
	}

	return &relationship, nil
}

// SetIsolationSegment implements capi.SpacesClient.SetIsolationSegment
func (c *SpacesClient) SetIsolationSegment(ctx context.Context, guid string, isolationSegmentGUID string) (*capi.Relationship, error) {
	path := fmt.Sprintf("/v3/spaces/%s/relationships/isolation_segment", guid)

	var data *capi.RelationshipData
	if isolationSegmentGUID != "" {
		data = &capi.RelationshipData{GUID: isolationSegmentGUID}
	}

	body := capi.Relationship{Data: data}

	resp, err := c.httpClient.Patch(ctx, path, body)
	if err != nil {
		return nil, fmt.Errorf("setting isolation segment: %w", err)
	}

	var relationship capi.Relationship
	if err := json.Unmarshal(resp.Body, &relationship); err != nil {
		return nil, fmt.Errorf("parsing relationship response: %w", err)
	}

	return &relationship, nil
}

// ListUsers implements capi.SpacesClient.ListUsers
func (c *SpacesClient) ListUsers(ctx context.Context, guid string, params *capi.QueryParams) (*capi.ListResponse[capi.User], error) {
	path := fmt.Sprintf("/v3/spaces/%s/users", guid)

	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("listing space users: %w", err)
	}

	var result capi.ListResponse[capi.User]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing users list response: %w", err)
	}

	return &result, nil
}

// ListManagers implements capi.SpacesClient.ListManagers
func (c *SpacesClient) ListManagers(ctx context.Context, guid string, params *capi.QueryParams) (*capi.ListResponse[capi.User], error) {
	path := fmt.Sprintf("/v3/spaces/%s/managers", guid)

	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("listing space managers: %w", err)
	}

	var result capi.ListResponse[capi.User]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing users list response: %w", err)
	}

	return &result, nil
}

// ListDevelopers implements capi.SpacesClient.ListDevelopers
func (c *SpacesClient) ListDevelopers(ctx context.Context, guid string, params *capi.QueryParams) (*capi.ListResponse[capi.User], error) {
	path := fmt.Sprintf("/v3/spaces/%s/developers", guid)

	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("listing space developers: %w", err)
	}

	var result capi.ListResponse[capi.User]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing users list response: %w", err)
	}

	return &result, nil
}

// ListAuditors implements capi.SpacesClient.ListAuditors
func (c *SpacesClient) ListAuditors(ctx context.Context, guid string, params *capi.QueryParams) (*capi.ListResponse[capi.User], error) {
	path := fmt.Sprintf("/v3/spaces/%s/auditors", guid)

	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("listing space auditors: %w", err)
	}

	var result capi.ListResponse[capi.User]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing users list response: %w", err)
	}

	return &result, nil
}

// ListSupporters implements capi.SpacesClient.ListSupporters
func (c *SpacesClient) ListSupporters(ctx context.Context, guid string, params *capi.QueryParams) (*capi.ListResponse[capi.User], error) {
	path := fmt.Sprintf("/v3/spaces/%s/supporters", guid)

	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("listing space supporters: %w", err)
	}

	var result capi.ListResponse[capi.User]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing users list response: %w", err)
	}

	return &result, nil
}

// GetFeature implements capi.SpacesClient.GetFeature
func (c *SpacesClient) GetFeature(ctx context.Context, guid string, feature string) (*capi.SpaceFeature, error) {
	path := fmt.Sprintf("/v3/spaces/%s/features/%s", guid, feature)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting space feature: %w", err)
	}

	var spaceFeature capi.SpaceFeature
	if err := json.Unmarshal(resp.Body, &spaceFeature); err != nil {
		return nil, fmt.Errorf("parsing space feature response: %w", err)
	}

	return &spaceFeature, nil
}

// UpdateFeature implements capi.SpacesClient.UpdateFeature
func (c *SpacesClient) UpdateFeature(ctx context.Context, guid string, feature string, enabled bool) (*capi.SpaceFeature, error) {
	path := fmt.Sprintf("/v3/spaces/%s/features/%s", guid, feature)

	body := map[string]bool{"enabled": enabled}

	resp, err := c.httpClient.Patch(ctx, path, body)
	if err != nil {
		return nil, fmt.Errorf("updating space feature: %w", err)
	}

	var spaceFeature capi.SpaceFeature
	if err := json.Unmarshal(resp.Body, &spaceFeature); err != nil {
		return nil, fmt.Errorf("parsing space feature response: %w", err)
	}

	return &spaceFeature, nil
}

// GetQuota implements capi.SpacesClient.GetQuota
func (c *SpacesClient) GetQuota(ctx context.Context, guid string) (*capi.SpaceQuota, error) {
	path := fmt.Sprintf("/v3/spaces/%s/quota", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting space quota: %w", err)
	}

	var quota capi.SpaceQuota
	if err := json.Unmarshal(resp.Body, &quota); err != nil {
		return nil, fmt.Errorf("parsing space quota response: %w", err)
	}

	return &quota, nil
}

// ApplyQuota implements capi.SpacesClient.ApplyQuota
func (c *SpacesClient) ApplyQuota(ctx context.Context, guid string, quotaGUID string) (*capi.Relationship, error) {
	path := fmt.Sprintf("/v3/spaces/%s/relationships/quota", guid)

	body := capi.Relationship{
		Data: &capi.RelationshipData{GUID: quotaGUID},
	}

	resp, err := c.httpClient.Patch(ctx, path, body)
	if err != nil {
		return nil, fmt.Errorf("applying space quota: %w", err)
	}

	var relationship capi.Relationship
	if err := json.Unmarshal(resp.Body, &relationship); err != nil {
		return nil, fmt.Errorf("parsing relationship response: %w", err)
	}

	return &relationship, nil
}

// RemoveQuota implements capi.SpacesClient.RemoveQuota
func (c *SpacesClient) RemoveQuota(ctx context.Context, guid string) error {
	path := fmt.Sprintf("/v3/spaces/%s/relationships/quota", guid)

	// Setting data to nil removes the quota
	body := capi.Relationship{
		Data: nil,
	}

	_, err := c.httpClient.Patch(ctx, path, body)
	if err != nil {
		return fmt.Errorf("removing space quota: %w", err)
	}

	return nil
}

// GetFeatures implements capi.SpacesClient.GetFeatures
func (c *SpacesClient) GetFeatures(ctx context.Context, guid string) (*capi.SpaceFeatures, error) {
	path := fmt.Sprintf("/v3/spaces/%s/features", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting space features: %w", err)
	}

	var features capi.SpaceFeatures
	if err := json.Unmarshal(resp.Body, &features); err != nil {
		return nil, fmt.Errorf("parsing space features response: %w", err)
	}

	return &features, nil
}

// GetUsageSummary implements capi.SpacesClient.GetUsageSummary
func (c *SpacesClient) GetUsageSummary(ctx context.Context, guid string) (*capi.SpaceUsageSummary, error) {
	path := fmt.Sprintf("/v3/spaces/%s/usage_summary", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting space usage summary: %w", err)
	}

	var summary capi.SpaceUsageSummary
	if err := json.Unmarshal(resp.Body, &summary); err != nil {
		return nil, fmt.Errorf("parsing space usage summary response: %w", err)
	}

	return &summary, nil
}

// ListRunningSecurityGroups implements capi.SpacesClient.ListRunningSecurityGroups
func (c *SpacesClient) ListRunningSecurityGroups(ctx context.Context, guid string, params *capi.QueryParams) (*capi.ListResponse[capi.SecurityGroup], error) {
	path := fmt.Sprintf("/v3/spaces/%s/running_security_groups", guid)

	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("listing running security groups: %w", err)
	}

	var result capi.ListResponse[capi.SecurityGroup]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing security groups list response: %w", err)
	}

	return &result, nil
}

// ListStagingSecurityGroups implements capi.SpacesClient.ListStagingSecurityGroups
func (c *SpacesClient) ListStagingSecurityGroups(ctx context.Context, guid string, params *capi.QueryParams) (*capi.ListResponse[capi.SecurityGroup], error) {
	path := fmt.Sprintf("/v3/spaces/%s/staging_security_groups", guid)

	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("listing staging security groups: %w", err)
	}

	var result capi.ListResponse[capi.SecurityGroup]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing security groups list response: %w", err)
	}

	return &result, nil
}

// ApplyManifest implements capi.SpacesClient.ApplyManifest
func (c *SpacesClient) ApplyManifest(ctx context.Context, guid string, manifest string) (*capi.Job, error) {
	path := fmt.Sprintf("/v3/spaces/%s/actions/apply_manifest", guid)

	// The manifest should be sent as YAML in the request body
	resp, err := c.httpClient.PostRaw(ctx, path, []byte(manifest), "application/x-yaml")
	if err != nil {
		return nil, fmt.Errorf("applying manifest: %w", err)
	}

	var job capi.Job
	if err := json.Unmarshal(resp.Body, &job); err != nil {
		return nil, fmt.Errorf("parsing job response: %w", err)
	}

	return &job, nil
}

// CreateManifestDiff implements capi.SpacesClient.CreateManifestDiff
func (c *SpacesClient) CreateManifestDiff(ctx context.Context, guid string, manifest string) (*capi.ManifestDiff, error) {
	path := fmt.Sprintf("/v3/spaces/%s/manifest_diff", guid)

	// The manifest should be sent as YAML in the request body
	resp, err := c.httpClient.PostRaw(ctx, path, []byte(manifest), "application/x-yaml")
	if err != nil {
		return nil, fmt.Errorf("creating manifest diff: %w", err)
	}

	var diff capi.ManifestDiff
	if err := json.Unmarshal(resp.Body, &diff); err != nil {
		return nil, fmt.Errorf("parsing manifest diff response: %w", err)
	}

	return &diff, nil
}

// DeleteUnmappedRoutes implements capi.SpacesClient.DeleteUnmappedRoutes
func (c *SpacesClient) DeleteUnmappedRoutes(ctx context.Context, guid string) (*capi.Job, error) {
	path := fmt.Sprintf("/v3/spaces/%s/routes?unmapped=true", guid)

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting unmapped routes: %w", err)
	}

	var job capi.Job
	if err := json.Unmarshal(resp.Body, &job); err != nil {
		return nil, fmt.Errorf("parsing job response: %w", err)
	}

	return &job, nil
}
