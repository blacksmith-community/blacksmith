package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// SecurityGroupsClient implements capi.SecurityGroupsClient
type SecurityGroupsClient struct {
	httpClient *http.Client
}

// NewSecurityGroupsClient creates a new security groups client
func NewSecurityGroupsClient(httpClient *http.Client) *SecurityGroupsClient {
	return &SecurityGroupsClient{
		httpClient: httpClient,
	}
}

// Create implements capi.SecurityGroupsClient.Create
func (c *SecurityGroupsClient) Create(ctx context.Context, request *capi.SecurityGroupCreateRequest) (*capi.SecurityGroup, error) {
	path := "/v3/security_groups"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating security group: %w", err)
	}

	var sg capi.SecurityGroup
	if err := json.Unmarshal(resp.Body, &sg); err != nil {
		return nil, fmt.Errorf("parsing security group response: %w", err)
	}

	return &sg, nil
}

// Get implements capi.SecurityGroupsClient.Get
func (c *SecurityGroupsClient) Get(ctx context.Context, guid string) (*capi.SecurityGroup, error) {
	path := fmt.Sprintf("/v3/security_groups/%s", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting security group: %w", err)
	}

	var sg capi.SecurityGroup
	if err := json.Unmarshal(resp.Body, &sg); err != nil {
		return nil, fmt.Errorf("parsing security group: %w", err)
	}

	return &sg, nil
}

// List implements capi.SecurityGroupsClient.List
func (c *SecurityGroupsClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.SecurityGroup], error) {
	path := "/v3/security_groups"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing security groups: %w", err)
	}

	var list capi.ListResponse[capi.SecurityGroup]
	if err := json.Unmarshal(resp.Body, &list); err != nil {
		return nil, fmt.Errorf("parsing security groups list: %w", err)
	}

	return &list, nil
}

// Update implements capi.SecurityGroupsClient.Update
func (c *SecurityGroupsClient) Update(ctx context.Context, guid string, request *capi.SecurityGroupUpdateRequest) (*capi.SecurityGroup, error) {
	path := fmt.Sprintf("/v3/security_groups/%s", guid)

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating security group: %w", err)
	}

	var sg capi.SecurityGroup
	if err := json.Unmarshal(resp.Body, &sg); err != nil {
		return nil, fmt.Errorf("parsing security group response: %w", err)
	}

	return &sg, nil
}

// Delete implements capi.SecurityGroupsClient.Delete
func (c *SecurityGroupsClient) Delete(ctx context.Context, guid string) (*capi.Job, error) {
	path := fmt.Sprintf("/v3/security_groups/%s", guid)

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting security group: %w", err)
	}

	var job capi.Job
	if err := json.Unmarshal(resp.Body, &job); err != nil {
		return nil, fmt.Errorf("parsing job response: %w", err)
	}

	return &job, nil
}

// BindRunningSpaces implements capi.SecurityGroupsClient.BindRunningSpaces
func (c *SecurityGroupsClient) BindRunningSpaces(ctx context.Context, guid string, spaceGUIDs []string) (*capi.ToManyRelationship, error) {
	path := fmt.Sprintf("/v3/security_groups/%s/relationships/running_spaces", guid)

	request := capi.SecurityGroupBindRequest{
		Data: make([]capi.RelationshipData, len(spaceGUIDs)),
	}
	for i, spaceGUID := range spaceGUIDs {
		request.Data[i] = capi.RelationshipData{GUID: spaceGUID}
	}

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("binding security group to running spaces: %w", err)
	}

	var relationship capi.ToManyRelationship
	if err := json.Unmarshal(resp.Body, &relationship); err != nil {
		return nil, fmt.Errorf("parsing relationship response: %w", err)
	}

	return &relationship, nil
}

// UnbindRunningSpace implements capi.SecurityGroupsClient.UnbindRunningSpace
func (c *SecurityGroupsClient) UnbindRunningSpace(ctx context.Context, guid string, spaceGUID string) error {
	path := fmt.Sprintf("/v3/security_groups/%s/relationships/running_spaces/%s", guid, spaceGUID)

	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("unbinding security group from running space: %w", err)
	}

	return nil
}

// BindStagingSpaces implements capi.SecurityGroupsClient.BindStagingSpaces
func (c *SecurityGroupsClient) BindStagingSpaces(ctx context.Context, guid string, spaceGUIDs []string) (*capi.ToManyRelationship, error) {
	path := fmt.Sprintf("/v3/security_groups/%s/relationships/staging_spaces", guid)

	request := capi.SecurityGroupBindRequest{
		Data: make([]capi.RelationshipData, len(spaceGUIDs)),
	}
	for i, spaceGUID := range spaceGUIDs {
		request.Data[i] = capi.RelationshipData{GUID: spaceGUID}
	}

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("binding security group to staging spaces: %w", err)
	}

	var relationship capi.ToManyRelationship
	if err := json.Unmarshal(resp.Body, &relationship); err != nil {
		return nil, fmt.Errorf("parsing relationship response: %w", err)
	}

	return &relationship, nil
}

// UnbindStagingSpace implements capi.SecurityGroupsClient.UnbindStagingSpace
func (c *SecurityGroupsClient) UnbindStagingSpace(ctx context.Context, guid string, spaceGUID string) error {
	path := fmt.Sprintf("/v3/security_groups/%s/relationships/staging_spaces/%s", guid, spaceGUID)

	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("unbinding security group from staging space: %w", err)
	}

	return nil
}
