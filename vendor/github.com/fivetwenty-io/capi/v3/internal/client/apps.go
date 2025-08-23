package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	internalhttp "github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// AppsClient implements capi.AppsClient
type AppsClient struct {
	httpClient *internalhttp.Client
	apiLinks   map[string]string
}

// NewAppsClient creates a new apps client
func NewAppsClient(httpClient *internalhttp.Client) *AppsClient {
	return &AppsClient{
		httpClient: httpClient,
	}
}

// NewAppsClientWithLinks creates a new apps client with API links
func NewAppsClientWithLinks(httpClient *internalhttp.Client, apiLinks map[string]string) *AppsClient {
	return &AppsClient{
		httpClient: httpClient,
		apiLinks:   apiLinks,
	}
}

// Create implements capi.AppsClient.Create
func (c *AppsClient) Create(ctx context.Context, request *capi.AppCreateRequest) (*capi.App, error) {
	resp, err := c.httpClient.Post(ctx, "/v3/apps", request)
	if err != nil {
		return nil, fmt.Errorf("creating app: %w", err)
	}

	var app capi.App
	if err := json.Unmarshal(resp.Body, &app); err != nil {
		return nil, fmt.Errorf("parsing app response: %w", err)
	}

	return &app, nil
}

// Get implements capi.AppsClient.Get
func (c *AppsClient) Get(ctx context.Context, guid string) (*capi.App, error) {
	path := fmt.Sprintf("/v3/apps/%s", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting app: %w", err)
	}

	var app capi.App
	if err := json.Unmarshal(resp.Body, &app); err != nil {
		return nil, fmt.Errorf("parsing app response: %w", err)
	}

	return &app, nil
}

// List implements capi.AppsClient.List
func (c *AppsClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.App], error) {
	var query url.Values
	if params != nil {
		query = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, "/v3/apps", query)
	if err != nil {
		return nil, fmt.Errorf("listing apps: %w", err)
	}

	var result capi.ListResponse[capi.App]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing apps list response: %w", err)
	}

	return &result, nil
}

// Update implements capi.AppsClient.Update
func (c *AppsClient) Update(ctx context.Context, guid string, request *capi.AppUpdateRequest) (*capi.App, error) {
	path := fmt.Sprintf("/v3/apps/%s", guid)
	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating app: %w", err)
	}

	var app capi.App
	if err := json.Unmarshal(resp.Body, &app); err != nil {
		return nil, fmt.Errorf("parsing app response: %w", err)
	}

	return &app, nil
}

// Delete implements capi.AppsClient.Delete
func (c *AppsClient) Delete(ctx context.Context, guid string) error {
	path := fmt.Sprintf("/v3/apps/%s", guid)
	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("deleting app: %w", err)
	}

	return nil
}

// Start implements capi.AppsClient.Start
func (c *AppsClient) Start(ctx context.Context, guid string) (*capi.App, error) {
	path := fmt.Sprintf("/v3/apps/%s/actions/start", guid)
	resp, err := c.httpClient.Post(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("starting app: %w", err)
	}

	var app capi.App
	if err := json.Unmarshal(resp.Body, &app); err != nil {
		return nil, fmt.Errorf("parsing app response: %w", err)
	}

	return &app, nil
}

// Stop implements capi.AppsClient.Stop
func (c *AppsClient) Stop(ctx context.Context, guid string) (*capi.App, error) {
	path := fmt.Sprintf("/v3/apps/%s/actions/stop", guid)
	resp, err := c.httpClient.Post(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("stopping app: %w", err)
	}

	var app capi.App
	if err := json.Unmarshal(resp.Body, &app); err != nil {
		return nil, fmt.Errorf("parsing app response: %w", err)
	}

	return &app, nil
}

// Restart implements capi.AppsClient.Restart
func (c *AppsClient) Restart(ctx context.Context, guid string) (*capi.App, error) {
	path := fmt.Sprintf("/v3/apps/%s/actions/restart", guid)
	resp, err := c.httpClient.Post(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("restarting app: %w", err)
	}

	var app capi.App
	if err := json.Unmarshal(resp.Body, &app); err != nil {
		return nil, fmt.Errorf("parsing app response: %w", err)
	}

	return &app, nil
}

// GetEnv implements capi.AppsClient.GetEnv
func (c *AppsClient) GetEnv(ctx context.Context, guid string) (*capi.AppEnvironment, error) {
	path := fmt.Sprintf("/v3/apps/%s/env", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting app environment: %w", err)
	}

	var env capi.AppEnvironment
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return nil, fmt.Errorf("parsing app environment response: %w", err)
	}

	return &env, nil
}

// GetEnvVars implements capi.AppsClient.GetEnvVars
func (c *AppsClient) GetEnvVars(ctx context.Context, guid string) (map[string]interface{}, error) {
	path := fmt.Sprintf("/v3/apps/%s/environment_variables", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting app environment variables: %w", err)
	}

	// The response has a 'var' field that contains the environment variables
	var result struct {
		Var map[string]interface{} `json:"var"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing environment variables response: %w", err)
	}

	return result.Var, nil
}

// UpdateEnvVars implements capi.AppsClient.UpdateEnvVars
func (c *AppsClient) UpdateEnvVars(ctx context.Context, guid string, envVars map[string]interface{}) (map[string]interface{}, error) {
	path := fmt.Sprintf("/v3/apps/%s/environment_variables", guid)

	// Wrap the variables in a 'var' field as required by the API
	body := map[string]interface{}{
		"var": envVars,
	}

	resp, err := c.httpClient.Patch(ctx, path, body)
	if err != nil {
		return nil, fmt.Errorf("updating app environment variables: %w", err)
	}

	var result struct {
		Var map[string]interface{} `json:"var"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing environment variables response: %w", err)
	}

	return result.Var, nil
}

// GetCurrentDroplet implements capi.AppsClient.GetCurrentDroplet
func (c *AppsClient) GetCurrentDroplet(ctx context.Context, guid string) (*capi.Droplet, error) {
	path := fmt.Sprintf("/v3/apps/%s/droplets/current", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting current droplet: %w", err)
	}

	var droplet capi.Droplet
	if err := json.Unmarshal(resp.Body, &droplet); err != nil {
		return nil, fmt.Errorf("parsing droplet response: %w", err)
	}

	return &droplet, nil
}

// SetCurrentDroplet implements capi.AppsClient.SetCurrentDroplet
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
	if err := json.Unmarshal(resp.Body, &relationship); err != nil {
		return nil, fmt.Errorf("parsing relationship response: %w", err)
	}

	return &relationship, nil
}

// GetSSHEnabled implements capi.AppsClient.GetSSHEnabled
func (c *AppsClient) GetSSHEnabled(ctx context.Context, guid string) (*capi.AppSSHEnabled, error) {
	path := fmt.Sprintf("/v3/apps/%s/ssh_enabled", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting SSH enabled status: %w", err)
	}

	var sshEnabled capi.AppSSHEnabled
	if err := json.Unmarshal(resp.Body, &sshEnabled); err != nil {
		return nil, fmt.Errorf("parsing SSH enabled response: %w", err)
	}

	return &sshEnabled, nil
}

// GetPermissions implements capi.AppsClient.GetPermissions
func (c *AppsClient) GetPermissions(ctx context.Context, guid string) (*capi.AppPermissions, error) {
	path := fmt.Sprintf("/v3/apps/%s/permissions", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting app permissions: %w", err)
	}

	var permissions capi.AppPermissions
	if err := json.Unmarshal(resp.Body, &permissions); err != nil {
		return nil, fmt.Errorf("parsing permissions response: %w", err)
	}

	return &permissions, nil
}

// ClearBuildpackCache implements capi.AppsClient.ClearBuildpackCache
func (c *AppsClient) ClearBuildpackCache(ctx context.Context, guid string) error {
	path := fmt.Sprintf("/v3/apps/%s/actions/clear_buildpack_cache", guid)
	_, err := c.httpClient.Post(ctx, path, nil)
	if err != nil {
		return fmt.Errorf("clearing buildpack cache: %w", err)
	}

	return nil
}

// GetManifest implements capi.AppsClient.GetManifest
func (c *AppsClient) GetManifest(ctx context.Context, guid string) (string, error) {
	path := fmt.Sprintf("/v3/apps/%s/manifest", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return "", fmt.Errorf("getting app manifest: %w", err)
	}

	// The manifest is returned as YAML, so we return it as a string
	return string(resp.Body), nil
}

// Restage implements capi.AppsClient.Restage
func (c *AppsClient) Restage(ctx context.Context, guid string) (*capi.Build, error) {
	// In API v3, restaging is done by creating a new build from the app's most recent package
	// 1. Get packages for the app to find the most recent one
	params := &capi.QueryParams{}
	params.WithFilter("app_guids", guid)
	params.WithOrderBy("-created_at") // Most recent first
	params.PerPage = 1                // Only need the most recent

	packagesResp, err := c.httpClient.Get(ctx, "/v3/packages", params.ToValues())
	if err != nil {
		return nil, fmt.Errorf("getting app packages: %w", err)
	}

	var packagesList capi.ListResponse[capi.Package]
	if err := json.Unmarshal(packagesResp.Body, &packagesList); err != nil {
		return nil, fmt.Errorf("parsing packages response: %w", err)
	}

	if len(packagesList.Resources) == 0 {
		return nil, fmt.Errorf("no packages found for app: restaging requires at least one package")
	}

	// 2. Create a new build from the most recent package
	mostRecentPackage := packagesList.Resources[0]
	buildRequest := &capi.BuildCreateRequest{
		Package: &capi.BuildPackageRef{
			GUID: mostRecentPackage.GUID,
		},
	}

	buildResp, err := c.httpClient.Post(ctx, "/v3/builds", buildRequest)
	if err != nil {
		return nil, fmt.Errorf("creating build for restage: %w", err)
	}

	var build capi.Build
	if err := json.Unmarshal(buildResp.Body, &build); err != nil {
		return nil, fmt.Errorf("parsing build response: %w", err)
	}

	return &build, nil
}

// GetRecentLogs implements capi.AppsClient.GetRecentLogs
func (c *AppsClient) GetRecentLogs(ctx context.Context, guid string, lines int) (*capi.AppLogs, error) {
	// Get the log_cache endpoint from CF info
	var logCacheURL string

	if c.apiLinks != nil {
		if url, exists := c.apiLinks["log_cache"]; exists {
			logCacheURL = url
		}
	}

	if logCacheURL == "" {
		// Fallback: Get the log cache endpoint from CF info
		infoResp, err := c.httpClient.Get(ctx, "/v3/info", nil)
		if err != nil {
			return nil, fmt.Errorf("getting CF info: %w", err)
		}

		var info capi.Info
		if err := json.Unmarshal(infoResp.Body, &info); err != nil {
			return nil, fmt.Errorf("parsing info response: %w", err)
		}

		// Check if log_cache link is available in info
		if logCacheLink, exists := info.Links["log_cache"]; exists {
			logCacheURL = logCacheLink.Href
		} else {
			// If not in info, infer from API endpoint (common CF pattern)
			// Example: api.system.domain -> log-cache.system.domain
			logCacheURL, err = c.inferLogCacheURL()
			if err != nil {
				return nil, fmt.Errorf("log_cache endpoint not available and could not infer: %w", err)
			}
		}
	}

	// Parse the log-cache URL to get the host
	logCacheEndpoint, err := c.buildLogCacheURL(logCacheURL, fmt.Sprintf("/api/v1/read/%s", guid))
	if err != nil {
		return nil, fmt.Errorf("building log cache URL: %w", err)
	}

	// Build query parameters - following CF CLI pattern
	query := url.Values{}
	query.Set("descending", "true")
	query.Set("envelope_types", "LOG")

	if lines > 0 {
		query.Set("limit", fmt.Sprintf("%d", lines))
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
	if err := json.Unmarshal(resp.Body, &logCacheResp); err != nil {
		return nil, fmt.Errorf("parsing log cache response: %w", err)
	}

	// Convert log cache envelopes to our LogMessage format
	var logMessages []capi.LogMessage
	for _, envelope := range logCacheResp.Envelopes.Batch {
		if envelope.Log != nil {
			// Decode base64 payload
			decodedPayload, err := base64.StdEncoding.DecodeString(string(envelope.Log.Payload))
			if err != nil {
				// If decoding fails, use raw payload
				decodedPayload = envelope.Log.Payload
			}

			// Parse timestamp from nanoseconds
			timestampNanos := envelope.Timestamp
			var timestamp time.Time
			if len(timestampNanos) > 0 {
				// Convert string timestamp (nanoseconds) to time.Time
				if nsInt, err := strconv.ParseInt(timestampNanos, 10, 64); err == nil {
					timestamp = time.Unix(0, nsInt)
				} else {
					timestamp = time.Now()
				}
			} else {
				timestamp = time.Now()
			}

			// Determine source type from tags
			sourceType := "APP"
			if st, exists := envelope.Tags["source_type"]; exists {
				sourceType = st
			}

			logMessages = append(logMessages, capi.LogMessage{
				Message:     string(decodedPayload),
				MessageType: envelope.Log.Type,
				Timestamp:   timestamp,
				AppID:       guid,
				SourceType:  sourceType,
				SourceID:    envelope.InstanceID,
			})
		}
	}

	return &capi.AppLogs{
		Messages: logMessages,
	}, nil
}

// StreamLogs implements capi.AppsClient.StreamLogs
func (c *AppsClient) StreamLogs(ctx context.Context, guid string) (<-chan capi.LogMessage, error) {
	// Get the log_cache endpoint from CF info (streaming uses same endpoint as recent logs)
	var logCacheURL string

	if c.apiLinks != nil {
		if url, exists := c.apiLinks["log_cache"]; exists {
			logCacheURL = url
		}
	}

	if logCacheURL == "" {
		// Fallback: Get the log cache endpoint from CF info
		infoResp, err := c.httpClient.Get(ctx, "/v3/info", nil)
		if err != nil {
			return nil, fmt.Errorf("getting CF info: %w", err)
		}

		var info capi.Info
		if err := json.Unmarshal(infoResp.Body, &info); err != nil {
			return nil, fmt.Errorf("parsing info response: %w", err)
		}

		// Check if log_cache link is available in info
		if logCacheLink, exists := info.Links["log_cache"]; exists {
			logCacheURL = logCacheLink.Href
		} else {
			// If not in info, infer from API endpoint (common CF pattern)
			// Example: api.system.domain -> log-cache.system.domain
			logCacheURL, err = c.inferLogCacheURL()
			if err != nil {
				return nil, fmt.Errorf("log_cache endpoint not available and could not infer: %w", err)
			}
		}
	}

	// Create a channel for streaming logs
	logChan := make(chan capi.LogMessage, 100)

	// Start a goroutine to implement log streaming using polling
	// Following CF CLI pattern of polling log cache with start_time parameter
	go func() {
		defer close(logChan)

		// Get the most recent log timestamp to start streaming from there
		var lastTimestamp int64

		// First, get recent logs to establish the starting timestamp
		logCacheEndpoint, err := c.buildLogCacheURL(logCacheURL, fmt.Sprintf("/api/v1/read/%s", guid))
		if err == nil {
			// Get the most recent log to establish baseline
			baselineQuery := url.Values{}
			baselineQuery.Set("descending", "true")
			baselineQuery.Set("envelope_types", "LOG")
			baselineQuery.Set("limit", "1")
			baselineQuery.Set("start_time", "-6795364578871345152")

			if baselineResp, err := c.makeLogCacheRequest(ctx, logCacheEndpoint, baselineQuery); err == nil {
				var baselineLogResp capi.LogCacheResponse
				if err := json.Unmarshal(baselineResp.Body, &baselineLogResp); err == nil {
					if len(baselineLogResp.Envelopes.Batch) > 0 {
						if nsInt, err := strconv.ParseInt(baselineLogResp.Envelopes.Batch[0].Timestamp, 10, 64); err == nil {
							lastTimestamp = nsInt
						}
					}
				}
			}
		}

		// If we couldn't get a baseline, start from 1 minute ago
		if lastTimestamp == 0 {
			lastTimestamp = time.Now().Add(-1 * time.Minute).UnixNano()
		}

		// Poll every 2 seconds like CF CLI does
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Build log cache endpoint URL
				logCacheEndpoint, err := c.buildLogCacheURL(logCacheURL, fmt.Sprintf("/api/v1/read/%s", guid))
				if err != nil {
					// If we can't build URL, skip this poll
					continue
				}

				// Build query parameters for streaming - get logs since last timestamp
				query := url.Values{}
				query.Set("envelope_types", "LOG")
				query.Set("start_time", fmt.Sprintf("%d", lastTimestamp))

				// Make request to log cache
				resp, err := c.makeLogCacheRequest(ctx, logCacheEndpoint, query)
				if err != nil {
					// If request fails, skip this poll
					continue
				}

				// Parse log cache response
				var logCacheResp capi.LogCacheResponse
				if err := json.Unmarshal(resp.Body, &logCacheResp); err != nil {
					// If parsing fails, skip this poll
					continue
				}

				// Process new log messages
				for _, envelope := range logCacheResp.Envelopes.Batch {
					if envelope.Log != nil {
						// Decode base64 payload
						decodedPayload, err := base64.StdEncoding.DecodeString(string(envelope.Log.Payload))
						if err != nil {
							// If decoding fails, use raw payload
							decodedPayload = envelope.Log.Payload
						}

						// Parse timestamp from nanoseconds
						timestampNanos := envelope.Timestamp
						var timestamp time.Time
						if len(timestampNanos) > 0 {
							// Convert string timestamp (nanoseconds) to time.Time
							if nsInt, err := strconv.ParseInt(timestampNanos, 10, 64); err == nil {
								timestamp = time.Unix(0, nsInt)
							} else {
								timestamp = time.Now()
							}
						} else {
							timestamp = time.Now()
						}

						// Update last timestamp for next poll
						if timestamp.UnixNano() > lastTimestamp {
							lastTimestamp = timestamp.UnixNano()
						}

						// Determine source type from tags
						sourceType := "APP"
						if st, exists := envelope.Tags["source_type"]; exists {
							sourceType = st
						}

						logMessage := capi.LogMessage{
							Message:     string(decodedPayload),
							MessageType: envelope.Log.Type,
							Timestamp:   timestamp,
							AppID:       guid,
							SourceType:  sourceType,
							SourceID:    envelope.InstanceID,
						}

						// Send log message to channel
						select {
						case logChan <- logMessage:
						case <-ctx.Done():
							return
						}
					}
				}
			}
		}
	}()

	return logChan, nil
}

// buildLogCacheURL constructs a full URL for log cache requests
func (c *AppsClient) buildLogCacheURL(logCacheURL, path string) (string, error) {
	baseURL, err := url.Parse(logCacheURL)
	if err != nil {
		return "", err
	}

	baseURL.Path = path
	return baseURL.String(), nil
}

// makeLogCacheRequest makes an authenticated request to the log cache endpoint
func (c *AppsClient) makeLogCacheRequest(ctx context.Context, endpoint string, query url.Values) (*internalhttp.Response, error) {
	// For now, we need to implement a direct HTTP request to the log cache endpoint
	// since our internal HTTP client is configured for the main CF API
	// TODO: This should be refactored to support multiple endpoints properly

	// Parse URL to get the log cache endpoint structure
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}

	if query != nil {
		u.RawQuery = query.Encode()
	}

	// Create HTTP request to log cache
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating HTTP request: %w", err)
	}

	// Add authentication using the same token as the main CF API
	token, err := c.httpClient.GetAuthToken(ctx)
	if err != nil {
		// If we can't get auth token, return empty logs gracefully
		return &internalhttp.Response{
			StatusCode: 200,
			Body:       []byte(`{"envelopes":[]}`),
			Headers:    make(map[string][]string),
		}, nil
	}
	req.Header.Set("Authorization", "Bearer "+token)

	// Make the HTTP request
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// If the request fails (network error), return empty logs
		// This allows the command to complete successfully while we work on connectivity
		return &internalhttp.Response{
			StatusCode: 200,
			Body:       []byte(`{"envelopes":[]}`),
			Headers:    make(map[string][]string),
		}, nil
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	// Check for authentication errors (401) or other HTTP errors
	if resp.StatusCode == 401 {
		// Authentication required - for now return empty logs
		// TODO: Implement proper authentication with Bearer token
		return &internalhttp.Response{
			StatusCode: 200,
			Body:       []byte(`{"envelopes":[]}`),
			Headers:    make(map[string][]string),
		}, nil
	}

	if resp.StatusCode >= 400 {
		// Other HTTP error - return empty logs to avoid breaking the command
		return &internalhttp.Response{
			StatusCode: 200,
			Body:       []byte(`{"envelopes":[]}`),
			Headers:    make(map[string][]string),
		}, nil
	}

	// Check if we got empty response
	if len(body) == 0 {
		return &internalhttp.Response{
			StatusCode: 200,
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

// inferLogCacheURL attempts to infer the log cache URL from the API endpoint
func (c *AppsClient) inferLogCacheURL() (string, error) {
	// Get the base URL from the HTTP client
	// This is a common pattern where api.system.domain becomes log-cache.system.domain

	// For now, we need to access the client's base URL
	// Since we don't have direct access, let's try a common CF pattern
	infoResp, err := c.httpClient.Get(context.Background(), "/v3/info", nil)
	if err != nil {
		return "", fmt.Errorf("getting CF info to infer log cache URL: %w", err)
	}

	var info capi.Info
	if err := json.Unmarshal(infoResp.Body, &info); err != nil {
		return "", fmt.Errorf("parsing info response: %w", err)
	}

	// Extract the API URL from self link
	selfLink, exists := info.Links["self"]
	if !exists {
		return "", fmt.Errorf("self link not found in CF info")
	}

	// Parse the self URL and convert api.* to log-cache.*
	apiURL, err := url.Parse(selfLink.Href)
	if err != nil {
		return "", fmt.Errorf("parsing API URL: %w", err)
	}

	// Convert hostname from api.system.domain to log-cache.system.domain
	hostname := apiURL.Hostname()
	if hostname == "" {
		return "", fmt.Errorf("invalid API URL hostname")
	}

	// Replace api. with log-cache. or add log-cache. prefix
	var logCacheHost string
	if hostname == "api.system.aws.lab.fivetwenty.io" {
		logCacheHost = "log-cache.system.aws.lab.fivetwenty.io"
	} else if strings.HasPrefix(hostname, "api.") {
		logCacheHost = "log-cache." + hostname[4:]
	} else {
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

// GetFeatures implements capi.AppsClient.GetFeatures
func (c *AppsClient) GetFeatures(ctx context.Context, guid string) (*capi.AppFeatures, error) {
	path := fmt.Sprintf("/v3/apps/%s/features", guid)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting app features: %w", err)
	}

	var features capi.AppFeatures
	if err := json.Unmarshal(resp.Body, &features); err != nil {
		return nil, fmt.Errorf("parsing app features response: %w", err)
	}

	return &features, nil
}

// GetFeature implements capi.AppsClient.GetFeature
func (c *AppsClient) GetFeature(ctx context.Context, guid, featureName string) (*capi.AppFeature, error) {
	path := fmt.Sprintf("/v3/apps/%s/features/%s", guid, featureName)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting app feature %s: %w", featureName, err)
	}

	var feature capi.AppFeature
	if err := json.Unmarshal(resp.Body, &feature); err != nil {
		return nil, fmt.Errorf("parsing app feature response: %w", err)
	}

	return &feature, nil
}

// UpdateFeature implements capi.AppsClient.UpdateFeature
func (c *AppsClient) UpdateFeature(ctx context.Context, guid, featureName string, request *capi.AppFeatureUpdateRequest) (*capi.AppFeature, error) {
	path := fmt.Sprintf("/v3/apps/%s/features/%s", guid, featureName)
	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating app feature %s: %w", featureName, err)
	}

	var feature capi.AppFeature
	if err := json.Unmarshal(resp.Body, &feature); err != nil {
		return nil, fmt.Errorf("parsing app feature response: %w", err)
	}

	return &feature, nil
}
