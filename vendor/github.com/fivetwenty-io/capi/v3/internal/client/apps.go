package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fivetwenty-io/capi/v3/internal/constants"
	internalhttp "github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// AppsClient implements capi.AppsClient.
// Static errors for err113 compliance.
var (
	ErrNoPackagesFound       = errors.New("no packages found for app: restaging requires at least one package")
	ErrSelfLinkNotFound      = errors.New("self link not found in CF info")
	ErrInvalidAPIURLHostname = errors.New("invalid API URL hostname")
)

type AppsClient struct {
	httpClient *internalhttp.Client
	apiLinks   map[string]string
}

// NewAppsClient creates a new apps client.
func NewAppsClient(httpClient *internalhttp.Client) *AppsClient {
	return &AppsClient{
		httpClient: httpClient,
	}
}

// NewAppsClientWithLinks creates a new apps client with API links.
func NewAppsClientWithLinks(httpClient *internalhttp.Client, apiLinks map[string]string) *AppsClient {
	return &AppsClient{
		httpClient: httpClient,
		apiLinks:   apiLinks,
	}
}

// Create implements capi.AppsClient.Create.
func (c *AppsClient) Create(ctx context.Context, request *capi.AppCreateRequest) (*capi.App, error) {
	resp, err := c.httpClient.Post(ctx, "/v3/apps", request)
	if err != nil {
		return nil, fmt.Errorf("creating app: %w", err)
	}

	var app capi.App

	err = json.Unmarshal(resp.Body, &app)
	if err != nil {
		return nil, fmt.Errorf("parsing app response: %w", err)
	}

	return &app, nil
}

// Get implements capi.AppsClient.Get.
func (c *AppsClient) Get(ctx context.Context, guid string, opts ...capi.AppGetOption) (*capi.App, error) {
	path := "/v3/apps/" + guid

	resp, err := c.httpClient.Get(ctx, path, capi.ApplyQueryOptions(nil, opts))
	if err != nil {
		return nil, fmt.Errorf("getting app: %w", err)
	}

	var app capi.App

	err = json.Unmarshal(resp.Body, &app)
	if err != nil {
		return nil, fmt.Errorf("parsing app response: %w", err)
	}

	return &app, nil
}

// List implements capi.AppsClient.List.
func (c *AppsClient) List(ctx context.Context, params *capi.QueryParams, opts ...capi.AppListOption) (*capi.ListResponse[capi.App], error) {
	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	query = capi.ApplyQueryOptions(query, opts)

	resp, err := c.httpClient.Get(ctx, "/v3/apps", query)
	if err != nil {
		return nil, fmt.Errorf("listing apps: %w", err)
	}

	var result capi.ListResponse[capi.App]

	err = json.Unmarshal(resp.Body, &result)
	if err != nil {
		return nil, fmt.Errorf("parsing apps list response: %w", err)
	}

	return &result, nil
}

// Update implements capi.AppsClient.Update.
func (c *AppsClient) Update(ctx context.Context, guid string, request *capi.AppUpdateRequest) (*capi.App, error) {
	path := "/v3/apps/" + guid

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating app: %w", err)
	}

	var app capi.App

	err = json.Unmarshal(resp.Body, &app)
	if err != nil {
		return nil, fmt.Errorf("parsing app response: %w", err)
	}

	return &app, nil
}

// Delete implements capi.AppsClient.Delete.
//
// DELETE /v3/apps/{guid} is async: CF returns 202 Accepted with an empty
// body and a Location header pointing at /v3/jobs/{jobGuid}. We extract
// the job GUID from that header and return a Job with its GUID populated;
// callers use Jobs().Get or Jobs().PollUntilComplete for full state.
//
// Location header contract is defined in the CF v3 OpenAPI spec for
// `delete` on /v3/apps/{guid}. If the header is missing or malformed we
// return a sentinel error rather than a partially-populated Job, so
// callers don't accidentally poll an empty job id.
func (c *AppsClient) Delete(ctx context.Context, guid string) (*capi.Job, error) {
	path := "/v3/apps/" + guid

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting app: %w", err)
	}

	return jobFromLocationHeader(resp, "deleting app")
}

// Start implements capi.AppsClient.Start.
//
// POST /v3/apps/{guid}/actions/start is async per the CF v3 API spec:
// CF returns 202 Accepted with a Location header pointing at
// /v3/jobs/{jobGuid}. We extract the job GUID from that header and
// return a Job with its GUID populated; callers use Jobs().Get or
// Jobs().PollUntilComplete for full state — same pattern as Delete.
//
// The prior implementation unmarshalled the response body as App and
// discarded the Location header. CF's 202 body is effectively empty
// for job-returning actions, so callers got a zero-value App and no
// way to observe the action's completion. Returning the Job instead
// lets Stratos-style frontends surface progress / terminal state.
func (c *AppsClient) Start(ctx context.Context, guid string) (*capi.Job, error) {
	path := fmt.Sprintf("/v3/apps/%s/actions/start", guid)

	return c.postActionJob(ctx, path, "starting app")
}

// Stop implements capi.AppsClient.Stop. Same async contract as Start.
func (c *AppsClient) Stop(ctx context.Context, guid string) (*capi.Job, error) {
	path := fmt.Sprintf("/v3/apps/%s/actions/stop", guid)

	return c.postActionJob(ctx, path, "stopping app")
}

// Restart implements capi.AppsClient.Restart. Same async contract as Start.
func (c *AppsClient) Restart(ctx context.Context, guid string) (*capi.Job, error) {
	path := fmt.Sprintf("/v3/apps/%s/actions/restart", guid)

	return c.postActionJob(ctx, path, "restarting app")
}

// postActionJob POSTs to a /v3/apps/{guid}/actions/{action} path and
// extracts the async job GUID from the Location header. Shared across
// start/stop/restart/restage because CF's async-action response shape
// is identical for all four endpoints.
//
// CF v3 transitioned these actions from synchronous (200 + App body) to
// asynchronous (202 + Location → /v3/jobs/{jobGuid}) somewhere in the
// 3.18x–3.20x range. We support both shapes so the client works against
// older and newer targets:
//
//  1. If the response carries a Location header → parse it, return a
//     Job with the extracted GUID. Callers poll via Jobs().Get.
//  2. Otherwise → treat the call as synchronously complete and return
//     (nil, nil). Callers that need to know "is the action done?" can
//     treat nil-Job-nil-error as COMPLETE; callers that want to poll
//     regardless should branch on job != nil.
//
// Non-2xx is surfaced as an error by the httpClient layer and doesn't
// reach this parse path.
//
//nolint:funcorder // helper kept next to the start/stop/restart methods that call it
func (c *AppsClient) postActionJob(ctx context.Context, path, opLabel string) (*capi.Job, error) {
	resp, err := c.httpClient.Post(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", opLabel, err)
	}

	return jobFromOptionalLocation(resp, opLabel)
}

// GetEnv implements capi.AppsClient.GetEnv.
func (c *AppsClient) GetEnv(ctx context.Context, guid string) (*capi.AppEnvironment, error) {
	path := fmt.Sprintf("/v3/apps/%s/env", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting app environment: %w", err)
	}

	var env capi.AppEnvironment

	err = json.Unmarshal(resp.Body, &env)
	if err != nil {
		return nil, fmt.Errorf("parsing app environment response: %w", err)
	}

	return &env, nil
}

// GetEnvVars implements capi.AppsClient.GetEnvVars.
func (c *AppsClient) GetEnvVars(ctx context.Context, guid string) (map[string]any, error) {
	path := fmt.Sprintf("/v3/apps/%s/environment_variables", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting app environment variables: %w", err)
	}

	// The response has a 'var' field that contains the environment variables
	var result struct {
		Var map[string]any `json:"var"`
	}

	err = json.Unmarshal(resp.Body, &result)
	if err != nil {
		return nil, fmt.Errorf("parsing environment variables response: %w", err)
	}

	return result.Var, nil
}

// UpdateEnvVars implements capi.AppsClient.UpdateEnvVars.
func (c *AppsClient) UpdateEnvVars(ctx context.Context, guid string, envVars map[string]any) (map[string]any, error) {
	path := fmt.Sprintf("/v3/apps/%s/environment_variables", guid)

	// Wrap the variables in a 'var' field as required by the API
	body := map[string]any{
		"var": envVars,
	}

	resp, err := c.httpClient.Patch(ctx, path, body)
	if err != nil {
		return nil, fmt.Errorf("updating app environment variables: %w", err)
	}

	var result struct {
		Var map[string]any `json:"var"`
	}

	err = json.Unmarshal(resp.Body, &result)
	if err != nil {
		return nil, fmt.Errorf("parsing environment variables response: %w", err)
	}

	return result.Var, nil
}

// GetCurrentDroplet implements capi.AppsClient.GetCurrentDroplet.
func (c *AppsClient) GetCurrentDroplet(ctx context.Context, guid string) (*capi.Droplet, error) {
	path := fmt.Sprintf("/v3/apps/%s/droplets/current", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting current droplet: %w", err)
	}

	var droplet capi.Droplet

	err = json.Unmarshal(resp.Body, &droplet)
	if err != nil {
		return nil, fmt.Errorf("parsing droplet response: %w", err)
	}

	return &droplet, nil
}

// SetCurrentDroplet implements capi.AppsClient.SetCurrentDroplet.
func (c *AppsClient) SetCurrentDroplet(ctx context.Context, guid string, dropletGUID string) (*capi.Relationship, error) {
	path := fmt.Sprintf("/v3/apps/%s/relationships/current_droplet", guid)

	body := capi.Relationship{
		Data: &capi.RelationshipData{GUID: dropletGUID},
	}

	resp, err := c.httpClient.Patch(ctx, path, body)
	if err != nil {
		return nil, fmt.Errorf("setting current droplet: %w", err)
	}

	var relationship capi.Relationship

	err = json.Unmarshal(resp.Body, &relationship)
	if err != nil {
		return nil, fmt.Errorf("parsing relationship response: %w", err)
	}

	return &relationship, nil
}

// GetSSHEnabled implements capi.AppsClient.GetSSHEnabled.
func (c *AppsClient) GetSSHEnabled(ctx context.Context, guid string) (*capi.AppSSHEnabled, error) {
	path := fmt.Sprintf("/v3/apps/%s/ssh_enabled", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting SSH enabled status: %w", err)
	}

	var sshEnabled capi.AppSSHEnabled

	err = json.Unmarshal(resp.Body, &sshEnabled)
	if err != nil {
		return nil, fmt.Errorf("parsing SSH enabled response: %w", err)
	}

	return &sshEnabled, nil
}

// GetPermissions implements capi.AppsClient.GetPermissions.
func (c *AppsClient) GetPermissions(ctx context.Context, guid string) (*capi.AppPermissions, error) {
	path := fmt.Sprintf("/v3/apps/%s/permissions", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting app permissions: %w", err)
	}

	var permissions capi.AppPermissions

	err = json.Unmarshal(resp.Body, &permissions)
	if err != nil {
		return nil, fmt.Errorf("parsing permissions response: %w", err)
	}

	return &permissions, nil
}

// ClearBuildpackCache implements capi.AppsClient.ClearBuildpackCache.
func (c *AppsClient) ClearBuildpackCache(ctx context.Context, guid string) error {
	path := fmt.Sprintf("/v3/apps/%s/actions/clear_buildpack_cache", guid)

	_, err := c.httpClient.Post(ctx, path, nil)
	if err != nil {
		return fmt.Errorf("clearing buildpack cache: %w", err)
	}

	return nil
}

// GetManifest implements capi.AppsClient.GetManifest.
func (c *AppsClient) GetManifest(ctx context.Context, guid string) (string, error) {
	path := fmt.Sprintf("/v3/apps/%s/manifest", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return "", fmt.Errorf("getting app manifest: %w", err)
	}

	// The manifest is returned as YAML, so we return it as a string
	return string(resp.Body), nil
}

// GetRecentLogs implements capi.AppsClient.GetRecentLogs.
func (c *AppsClient) GetRecentLogs(ctx context.Context, guid string, lines int) (*capi.AppLogs, error) {
	// Get the log_cache endpoint URL
	logCacheURL, err := c.resolveLogCacheURL(ctx)
	if err != nil {
		return nil, err
	}

	// Parse the log-cache URL to get the host
	logCacheEndpoint, err := c.buildLogCacheURL(logCacheURL, "/api/v1/read/"+guid)
	if err != nil {
		return nil, fmt.Errorf("building log cache URL: %w", err)
	}

	// Build query parameters - following CF CLI pattern
	query := url.Values{}
	query.Set("descending", "true")
	query.Set("envelope_types", "LOG")

	if lines > 0 {
		query.Set("limit", strconv.Itoa(lines))
	} else {
		query.Set("limit", "1000") // Default limit like CF CLI
	}

	// Use a very large negative start_time to get all available logs like CF CLI
	query.Set("start_time", "-6795364578871345152")

	// Make request to log cache
	resp, err := c.makeLogCacheRequest(ctx, logCacheEndpoint, query)
	if err != nil {
		return nil, fmt.Errorf("fetching logs from log cache: %w", err)
	}

	// Parse log cache response
	var logCacheResp capi.LogCacheResponse

	err = json.Unmarshal(resp.Body, &logCacheResp)
	if err != nil {
		return nil, fmt.Errorf("parsing log cache response: %w", err)
	}

	// Convert log cache envelopes to our LogMessage format
	var logMessages []capi.LogMessage

	for _, envelope := range logCacheResp.Envelopes.Batch {
		if envelope.Log != nil {
			if message := c.processLogEnvelope(envelope, guid); message != nil {
				logMessages = append(logMessages, *message)
			}
		}
	}

	return &capi.AppLogs{
		Messages: logMessages,
	}, nil
}

// StreamLogs implements capi.AppsClient.StreamLogs.

func (c *AppsClient) StreamLogs(ctx context.Context, guid string) (<-chan capi.LogMessage, error) {
	// Get the log_cache endpoint URL
	logCacheURL, err := c.getLogCacheURL(ctx)
	if err != nil {
		return nil, err
	}

	// Create a channel for streaming logs
	logChan := make(chan capi.LogMessage, constants.BufferSize)

	// Start a goroutine to implement log streaming using polling
	go c.streamLogsWorker(ctx, logCacheURL, guid, logChan)

	return logChan, nil
}

// GetFeatures implements capi.AppsClient.GetFeatures.
func (c *AppsClient) GetFeatures(ctx context.Context, guid string) (*capi.AppFeatures, error) {
	path := fmt.Sprintf("/v3/apps/%s/features", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting app features: %w", err)
	}

	var features capi.AppFeatures

	err = json.Unmarshal(resp.Body, &features)
	if err != nil {
		return nil, fmt.Errorf("parsing app features response: %w", err)
	}

	return &features, nil
}

// GetFeature implements capi.AppsClient.GetFeature.
func (c *AppsClient) GetFeature(ctx context.Context, guid, featureName string) (*capi.AppFeature, error) {
	path := fmt.Sprintf("/v3/apps/%s/features/%s", guid, featureName)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting app feature %s: %w", featureName, err)
	}

	var feature capi.AppFeature

	err = json.Unmarshal(resp.Body, &feature)
	if err != nil {
		return nil, fmt.Errorf("parsing app feature response: %w", err)
	}

	return &feature, nil
}

// UpdateFeature implements capi.AppsClient.UpdateFeature.
func (c *AppsClient) UpdateFeature(ctx context.Context, guid, featureName string, request *capi.AppFeatureUpdateRequest) (*capi.AppFeature, error) {
	path := fmt.Sprintf("/v3/apps/%s/features/%s", guid, featureName)

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating app feature %s: %w", featureName, err)
	}

	var feature capi.AppFeature

	err = json.Unmarshal(resp.Body, &feature)
	if err != nil {
		return nil, fmt.Errorf("parsing app feature response: %w", err)
	}

	return &feature, nil
}

// getLogCacheURL resolves the log cache URL from various sources.
func (c *AppsClient) getLogCacheURL(ctx context.Context) (string, error) {
	// Check if we have cached API links
	if c.apiLinks != nil {
		if url, exists := c.apiLinks["log_cache"]; exists {
			return url, nil
		}
	}

	// Fallback: Get the log cache endpoint from CF info
	infoResp, err := c.httpClient.Get(ctx, "/v3/info", nil)
	if err != nil {
		return "", fmt.Errorf("getting CF info: %w", err)
	}

	var info capi.Info

	err = json.Unmarshal(infoResp.Body, &info)
	if err != nil {
		return "", fmt.Errorf("parsing info response: %w", err)
	}

	// Check if log_cache link is available in info
	if logCacheLink, exists := info.Links["log_cache"]; exists {
		return logCacheLink.Href, nil
	}

	// If not in info, infer from API endpoint (common CF pattern)
	logCacheURL, err := c.inferLogCacheURL(ctx)
	if err != nil {
		return "", fmt.Errorf("log_cache endpoint not available and could not infer: %w", err)
	}

	return logCacheURL, nil
}

// getBaselineTimestamp gets the most recent log timestamp to start streaming from.
func (c *AppsClient) getBaselineTimestamp(ctx context.Context, logCacheURL, guid string) int64 {
	logCacheEndpoint, err := c.buildLogCacheURL(logCacheURL, "/api/v1/read/"+guid)
	if err != nil {
		return time.Now().Add(-1 * time.Minute).UnixNano()
	}

	baselineQuery := url.Values{}
	baselineQuery.Set("descending", "true")
	baselineQuery.Set("envelope_types", "LOG")
	baselineQuery.Set("limit", "1")
	baselineQuery.Set("start_time", "-6795364578871345152")

	baselineResp, err := c.makeLogCacheRequest(ctx, logCacheEndpoint, baselineQuery)
	if err != nil {
		return time.Now().Add(-1 * time.Minute).UnixNano()
	}

	var baselineLogResp capi.LogCacheResponse

	err = json.Unmarshal(baselineResp.Body, &baselineLogResp)
	if err != nil {
		return time.Now().Add(-1 * time.Minute).UnixNano()
	}

	if len(baselineLogResp.Envelopes.Batch) > 0 {
		nsInt, err := strconv.ParseInt(baselineLogResp.Envelopes.Batch[0].Timestamp, 10, 64)
		if err == nil {
			return nsInt
		}
	}

	return time.Now().Add(-1 * time.Minute).UnixNano()
}

// processLogEnvelopes converts log envelopes to LogMessages.
func (c *AppsClient) processLogEnvelopes(envelopes []capi.LogCacheEnvelope, guid string, lastTimestamp *int64) []capi.LogMessage {
	messages := make([]capi.LogMessage, 0, len(envelopes))

	for _, envelope := range envelopes {
		if envelope.Log == nil {
			continue
		}

		message := c.processLogEnvelope(envelope, guid)
		if message == nil {
			continue
		}

		// Update last timestamp for next poll
		if message.Timestamp.UnixNano() > *lastTimestamp {
			*lastTimestamp = message.Timestamp.UnixNano()
		}

		messages = append(messages, *message)
	}

	return messages
}

// processLogEnvelope converts a single log envelope to LogMessage.
func (c *AppsClient) processLogEnvelope(envelope capi.LogCacheEnvelope, guid string) *capi.LogMessage {
	// Decode base64 payload
	decodedPayload, err := base64.StdEncoding.DecodeString(string(envelope.Log.Payload))
	if err != nil {
		// If decoding fails, use raw payload
		decodedPayload = envelope.Log.Payload
	}

	// Parse timestamp from nanoseconds
	timestamp := c.parseLogTimestamp(envelope.Timestamp)

	// Determine source type from tags
	sourceType := "APP"
	if st, exists := envelope.Tags["source_type"]; exists {
		sourceType = st
	}

	return &capi.LogMessage{
		Message:     string(decodedPayload),
		MessageType: envelope.Log.Type,
		Timestamp:   timestamp,
		AppID:       guid,
		SourceType:  sourceType,
		SourceID:    envelope.InstanceID,
	}
}

// parseLogTimestamp converts timestamp string to time.Time.
func (c *AppsClient) parseLogTimestamp(timestampNanos string) time.Time {
	if len(timestampNanos) > 0 {
		nsInt, err := strconv.ParseInt(timestampNanos, 10, 64)
		if err == nil {
			return time.Unix(0, nsInt)
		}
	}

	return time.Now()
}

// streamLogsWorker handles the actual log streaming in a separate goroutine.
func (c *AppsClient) streamLogsWorker(ctx context.Context, logCacheURL, guid string, logChan chan<- capi.LogMessage) {
	defer close(logChan)

	// Get the baseline timestamp to start streaming from
	lastTimestamp := c.getBaselineTimestamp(ctx, logCacheURL, guid)

	// Poll every 2 seconds like CF CLI does
	ticker := time.NewTicker(constants.DefaultPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.pollForLogs(ctx, logCacheURL, guid, &lastTimestamp, logChan)
		}
	}
}

// pollForLogs fetches and processes new logs for a single poll cycle.
func (c *AppsClient) pollForLogs(ctx context.Context, logCacheURL, guid string, lastTimestamp *int64, logChan chan<- capi.LogMessage) {
	// Build log cache endpoint URL
	logCacheEndpoint, err := c.buildLogCacheURL(logCacheURL, "/api/v1/read/"+guid)
	if err != nil {
		return // Skip this poll if URL building fails
	}

	// Build query parameters for streaming
	query := url.Values{}
	query.Set("envelope_types", "LOG")
	query.Set("start_time", strconv.FormatInt(*lastTimestamp, 10))

	// Make request to log cache
	resp, err := c.makeLogCacheRequest(ctx, logCacheEndpoint, query)
	if err != nil {
		return // Skip this poll if request fails
	}

	// Parse log cache response
	var logCacheResp capi.LogCacheResponse

	err = json.Unmarshal(resp.Body, &logCacheResp)
	if err != nil {
		return // Skip this poll if parsing fails
	}

	// Process and send new log messages
	messages := c.processLogEnvelopes(logCacheResp.Envelopes.Batch, guid, lastTimestamp)
	for _, message := range messages {
		select {
		case logChan <- message:
		case <-ctx.Done():
			return
		}
	}
}

// buildLogCacheURL constructs a full URL for log cache requests.
func (c *AppsClient) buildLogCacheURL(logCacheURL, path string) (string, error) {
	baseURL, err := url.Parse(logCacheURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse log cache URL: %w", err)
	}

	baseURL.Path = path

	return baseURL.String(), nil
}

// makeLogCacheRequest makes an authenticated request to the log cache endpoint.
// createLogCacheRequest creates an HTTP request for the log cache endpoint.
func (c *AppsClient) createLogCacheRequest(ctx context.Context, endpoint string, query url.Values) (*http.Request, error) {
	parsedURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL: %w", err)
	}

	if query != nil {
		parsedURL.RawQuery = query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating HTTP request: %w", err)
	}

	// Add authentication using the same token as the main CF API
	token, err := c.httpClient.GetAuthToken(ctx)
	if err == nil {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return req, nil
}

// handleLogCacheResponse processes the HTTP response from log cache.
func (c *AppsClient) handleLogCacheResponse(resp *http.Response) (*internalhttp.Response, error) {
	defer func() {
		// Silently discard close error: no logger on AppsClient; request already completed.
		_ = resp.Body.Close()
	}()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	// Handle various error cases by returning empty logs
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode >= constants.HTTPStatusBadRequest || len(body) == 0 {
		return &internalhttp.Response{
			StatusCode: constants.HTTPStatusOK,
			Body:       []byte(`{"envelopes":[]}`),
			Headers:    make(map[string][]string),
		}, nil
	}

	// Convert to internal response format
	return &internalhttp.Response{
		StatusCode: resp.StatusCode,
		Body:       body,
		Headers:    resp.Header,
	}, nil
}

// emptyLogCacheResponse returns an empty log cache response.
func (c *AppsClient) emptyLogCacheResponse() *internalhttp.Response {
	return &internalhttp.Response{
		StatusCode: constants.HTTPStatusOK,
		Body:       []byte(`{"envelopes":[]}`),
		Headers:    make(map[string][]string),
	}
}

func (c *AppsClient) makeLogCacheRequest(ctx context.Context, endpoint string, query url.Values) (*internalhttp.Response, error) {
	// Create the HTTP request
	req, err := c.createLogCacheRequest(ctx, endpoint, query)
	if err != nil {
		return c.emptyLogCacheResponse(), nil
	}

	// Make the HTTP request
	client := &http.Client{Timeout: constants.DefaultHTTPTimeout}

	resp, err := client.Do(req)
	if err != nil {
		// If the request fails (network error), return empty logs
		return c.emptyLogCacheResponse(), nil
	}

	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	// Handle the response
	return c.handleLogCacheResponse(resp)
}

// inferLogCacheURL attempts to infer the log cache URL from the API endpoint.
func (c *AppsClient) inferLogCacheURL(ctx context.Context) (string, error) {
	// Get the base URL from the HTTP client
	// This is a common pattern where api.system.domain becomes log-cache.system.domain

	// For now, we need to access the client's base URL
	// Since we don't have direct access, let's try a common CF pattern
	infoResp, err := c.httpClient.Get(ctx, "/v3/info", nil)
	if err != nil {
		return "", fmt.Errorf("getting CF info to infer log cache URL: %w", err)
	}

	var info capi.Info

	err = json.Unmarshal(infoResp.Body, &info)
	if err != nil {
		return "", fmt.Errorf("parsing info response: %w", err)
	}

	// Extract the API URL from self link
	selfLink, exists := info.Links["self"]
	if !exists {
		return "", ErrSelfLinkNotFound
	}

	// Parse the self URL and convert api.* to log-cache.*
	apiURL, err := url.Parse(selfLink.Href)
	if err != nil {
		return "", fmt.Errorf("parsing API URL: %w", err)
	}

	// Convert hostname from api.system.domain to log-cache.system.domain
	hostname := apiURL.Hostname()
	if hostname == "" {
		return "", ErrInvalidAPIURLHostname
	}

	// Replace api. with log-cache. or add log-cache. prefix
	var logCacheHost string

	switch {
	case hostname == "api.system.aws.lab.fivetwenty.io":
		logCacheHost = "log-cache.system.aws.lab.fivetwenty.io"
	case strings.HasPrefix(hostname, "api."):
		logCacheHost = "log-cache." + hostname[4:]
	default:
		// If no api prefix, assume we need to add log-cache prefix
		logCacheHost = "log-cache." + hostname
	}

	// Construct log cache URL with same scheme and port
	logCacheURL := &url.URL{
		Scheme: apiURL.Scheme,
		Host:   logCacheHost,
	}
	if apiURL.Port() != "" {
		logCacheURL.Host = logCacheHost + ":" + apiURL.Port()
	}

	return logCacheURL.String(), nil
}

// resolveLogCacheURL determines the log cache URL from various sources.
func (c *AppsClient) resolveLogCacheURL(ctx context.Context) (string, error) {
	// Check if we have cached API links first
	if logCacheURL := c.getLogCacheURLFromLinks(); logCacheURL != "" {
		return logCacheURL, nil
	}

	// Fallback to getting from CF info
	return c.getLogCacheURLFromInfo(ctx)
}

// getLogCacheURLFromLinks gets log cache URL from cached API links.
func (c *AppsClient) getLogCacheURLFromLinks() string {
	if c.apiLinks != nil {
		if url, exists := c.apiLinks["log_cache"]; exists {
			return url
		}
	}

	return ""
}

// getLogCacheURLFromInfo gets log cache URL from CF info endpoint.
func (c *AppsClient) getLogCacheURLFromInfo(ctx context.Context) (string, error) {
	infoResp, err := c.httpClient.Get(ctx, "/v3/info", nil)
	if err != nil {
		return "", fmt.Errorf("getting CF info: %w", err)
	}

	var info capi.Info

	err = json.Unmarshal(infoResp.Body, &info)
	if err != nil {
		return "", fmt.Errorf("parsing info response: %w", err)
	}

	// Check if log_cache link is available in info
	if logCacheLink, exists := info.Links["log_cache"]; exists {
		return logCacheLink.Href, nil
	}

	// If not in info, infer from API endpoint (common CF pattern)
	logCacheURL, err := c.inferLogCacheURL(ctx)
	if err != nil {
		return "", fmt.Errorf("log_cache endpoint not available and could not infer: %w", err)
	}

	return logCacheURL, nil
}
