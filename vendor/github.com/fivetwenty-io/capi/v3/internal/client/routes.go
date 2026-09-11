package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// RoutesClient implements the capi.RoutesClient interface.
type RoutesClient struct {
	httpClient *http.Client
}

// NewRoutesClient creates a new RoutesClient.
func NewRoutesClient(httpClient *http.Client) *RoutesClient {
	return &RoutesClient{
		httpClient: httpClient,
	}
}

// Create creates a new route.
func (c *RoutesClient) Create(ctx context.Context, request *capi.RouteCreateRequest) (*capi.Route, error) {
	path := "/v3/routes"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating route: %w", err)
	}

	var route capi.Route

	err = json.Unmarshal(resp.Body, &route)
	if err != nil {
		return nil, fmt.Errorf("parsing route response: %w", err)
	}

	return &route, nil
}

// Get retrieves a specific route.
func (c *RoutesClient) Get(ctx context.Context, guid string, opts ...capi.RouteGetOption) (*capi.Route, error) {
	path := "/v3/routes/" + guid

	resp, err := c.httpClient.Get(ctx, path, capi.ApplyQueryOptions(nil, opts))
	if err != nil {
		return nil, fmt.Errorf("getting route: %w", err)
	}

	var route capi.Route

	err = json.Unmarshal(resp.Body, &route)
	if err != nil {
		return nil, fmt.Errorf("parsing route response: %w", err)
	}

	return &route, nil
}

// List lists all routes.
func (c *RoutesClient) List(ctx context.Context, params *capi.QueryParams, opts ...capi.RouteListOption) (*capi.ListResponse[capi.Route], error) {
	path := "/v3/routes"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	queryParams = capi.ApplyQueryOptions(queryParams, opts)

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing routes: %w", err)
	}

	var result capi.ListResponse[capi.Route]

	err = json.Unmarshal(resp.Body, &result)
	if err != nil {
		return nil, fmt.Errorf("parsing routes list response: %w", err)
	}

	return &result, nil
}

// Update updates a route's metadata.
func (c *RoutesClient) Update(ctx context.Context, guid string, request *capi.RouteUpdateRequest) (*capi.Route, error) {
	path := "/v3/routes/" + guid

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating route: %w", err)
	}

	var route capi.Route

	err = json.Unmarshal(resp.Body, &route)
	if err != nil {
		return nil, fmt.Errorf("parsing route response: %w", err)
	}

	return &route, nil
}

// Delete deletes a route.
//
// DELETE /v3/routes/{guid} is async: CF returns 202 Accepted with an empty
// body and a Location header pointing at /v3/jobs/{jobGuid}. We extract the
// job GUID from that header and return a Job with its GUID populated;
// callers use Jobs().Get or Jobs().PollUntilComplete for full state. Same
// pattern as AppsClient.Delete — both async deletes share the Location-only
// response shape.
func (c *RoutesClient) Delete(ctx context.Context, guid string) (*capi.Job, error) {
	path := "/v3/routes/" + guid

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting route: %w", err)
	}

	return jobFromLocationHeader(resp, "deleting route")
}

// ListDestinations lists all destinations for a route.
func (c *RoutesClient) ListDestinations(ctx context.Context, guid string, opts ...capi.RouteDestinationsOption) (*capi.RouteDestinations, error) {
	path := fmt.Sprintf("/v3/routes/%s/destinations", guid)

	resp, err := c.httpClient.Get(ctx, path, capi.ApplyQueryOptions(nil, opts))
	if err != nil {
		return nil, fmt.Errorf("listing route destinations: %w", err)
	}

	var destinations capi.RouteDestinations

	err = json.Unmarshal(resp.Body, &destinations)
	if err != nil {
		return nil, fmt.Errorf("parsing destinations response: %w", err)
	}

	return &destinations, nil
}

// InsertDestinations adds new destinations to a route.
func (c *RoutesClient) InsertDestinations(ctx context.Context, guid string, destinations []capi.RouteDestination) (*capi.RouteDestinations, error) {
	path := fmt.Sprintf("/v3/routes/%s/destinations", guid)

	request := struct {
		Destinations []capi.RouteDestination `json:"destinations"`
	}{
		Destinations: destinations,
	}

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("inserting route destinations: %w", err)
	}

	var result capi.RouteDestinations

	err = json.Unmarshal(resp.Body, &result)
	if err != nil {
		return nil, fmt.Errorf("parsing destinations response: %w", err)
	}

	return &result, nil
}

// ReplaceDestinations replaces all destinations for a route.
func (c *RoutesClient) ReplaceDestinations(ctx context.Context, guid string, destinations []capi.RouteDestination) (*capi.RouteDestinations, error) {
	path := fmt.Sprintf("/v3/routes/%s/destinations", guid)

	request := struct {
		Destinations []capi.RouteDestination `json:"destinations"`
	}{
		Destinations: destinations,
	}

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("replacing route destinations: %w", err)
	}

	var result capi.RouteDestinations

	err = json.Unmarshal(resp.Body, &result)
	if err != nil {
		return nil, fmt.Errorf("parsing destinations response: %w", err)
	}

	return &result, nil
}

// UpdateDestination updates a specific destination.
func (c *RoutesClient) UpdateDestination(ctx context.Context, guid string, destGUID string, protocol string) (*capi.RouteDestination, error) {
	path := fmt.Sprintf("/v3/routes/%s/destinations/%s", guid, destGUID)

	request := struct {
		Protocol string `json:"protocol"`
	}{
		Protocol: protocol,
	}

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating route destination: %w", err)
	}

	var destination capi.RouteDestination

	err = json.Unmarshal(resp.Body, &destination)
	if err != nil {
		return nil, fmt.Errorf("parsing destination response: %w", err)
	}

	return &destination, nil
}

// RemoveDestination removes a specific destination from a route.
func (c *RoutesClient) RemoveDestination(ctx context.Context, guid string, destGUID string) error {
	path := fmt.Sprintf("/v3/routes/%s/destinations/%s", guid, destGUID)

	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("removing route destination: %w", err)
	}

	return nil
}

// ListSharedSpaces lists spaces that a route is shared with.
func (c *RoutesClient) ListSharedSpaces(ctx context.Context, guid string) (*capi.ListResponse[capi.Space], error) {
	path := fmt.Sprintf("/v3/routes/%s/relationships/shared_spaces", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("listing shared spaces: %w", err)
	}

	var result capi.ListResponse[capi.Space]

	err = json.Unmarshal(resp.Body, &result)
	if err != nil {
		return nil, fmt.Errorf("parsing shared spaces response: %w", err)
	}

	return &result, nil
}

// ShareWithSpace shares a route with specified spaces.
func (c *RoutesClient) ShareWithSpace(ctx context.Context, guid string, spaceGUIDs []string) (*capi.ToManyRelationship, error) {
	path := fmt.Sprintf("/v3/routes/%s/relationships/shared_spaces", guid)

	// Build the request body with space GUIDs
	data := make([]capi.RelationshipData, len(spaceGUIDs))
	for i, spaceGUID := range spaceGUIDs {
		data[i] = capi.RelationshipData{GUID: spaceGUID}
	}

	request := struct {
		Data []capi.RelationshipData `json:"data"`
	}{
		Data: data,
	}

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("sharing route with spaces: %w", err)
	}

	var relationship capi.ToManyRelationship

	err = json.Unmarshal(resp.Body, &relationship)
	if err != nil {
		return nil, fmt.Errorf("parsing relationship response: %w", err)
	}

	return &relationship, nil
}

// UnshareFromSpace unshares a route from a specific space.
func (c *RoutesClient) UnshareFromSpace(ctx context.Context, guid string, spaceGUID string) error {
	path := fmt.Sprintf("/v3/routes/%s/relationships/shared_spaces/%s", guid, spaceGUID)

	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("unsharing route from space: %w", err)
	}

	return nil
}

// TransferOwnership transfers route ownership to a different space.
func (c *RoutesClient) TransferOwnership(ctx context.Context, guid string, spaceGUID string) (*capi.Route, error) {
	path := "/v3/routes/" + guid

	request := struct {
		Relationships capi.RouteRelationships `json:"relationships"`
	}{
		Relationships: capi.RouteRelationships{
			Space: capi.Relationship{
				Data: &capi.RelationshipData{
					GUID: spaceGUID,
				},
			},
		},
	}

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("transferring route ownership: %w", err)
	}

	var route capi.Route

	err = json.Unmarshal(resp.Body, &route)
	if err != nil {
		return nil, fmt.Errorf("parsing route response: %w", err)
	}

	return &route, nil
}
