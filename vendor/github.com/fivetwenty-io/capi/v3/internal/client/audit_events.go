package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// AuditEventsClient implements capi.AuditEventsClient
type AuditEventsClient struct {
	httpClient *http.Client
}

// NewAuditEventsClient creates a new audit events client
func NewAuditEventsClient(httpClient *http.Client) *AuditEventsClient {
	return &AuditEventsClient{
		httpClient: httpClient,
	}
}

// Get implements capi.AuditEventsClient.Get
func (c *AuditEventsClient) Get(ctx context.Context, guid string) (*capi.AuditEvent, error) {
	path := fmt.Sprintf("/v3/audit_events/%s", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting audit event: %w", err)
	}

	var event capi.AuditEvent
	if err := json.Unmarshal(resp.Body, &event); err != nil {
		return nil, fmt.Errorf("parsing audit event response: %w", err)
	}

	return &event, nil
}

// List implements capi.AuditEventsClient.List
func (c *AuditEventsClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.AuditEvent], error) {
	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, "/v3/audit_events", query)
	if err != nil {
		return nil, fmt.Errorf("listing audit events: %w", err)
	}

	var result capi.ListResponse[capi.AuditEvent]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing audit events list response: %w", err)
	}

	return &result, nil
}
