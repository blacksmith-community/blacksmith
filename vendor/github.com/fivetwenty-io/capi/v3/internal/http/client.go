package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/fivetwenty-io/capi/v3/internal/auth"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
	"github.com/hashicorp/go-retryablehttp"
)

// Logger interface for HTTP client logging
type Logger interface {
	Debug(msg string, fields map[string]interface{})
	Info(msg string, fields map[string]interface{})
	Warn(msg string, fields map[string]interface{})
	Error(msg string, fields map[string]interface{})
}

// Client wraps the HTTP client with retry logic and authentication
type Client struct {
	baseURL      string
	httpClient   *retryablehttp.Client
	tokenManager auth.TokenManager
	logger       Logger
	debug        bool
	userAgent    string
}

// Option configures the HTTP client
type Option func(*Client)

// WithLogger sets the logger for the client
func WithLogger(logger Logger) Option {
	return func(c *Client) {
		c.logger = logger
	}
}

// WithDebug enables debug logging
func WithDebug(debug bool) Option {
	return func(c *Client) {
		c.debug = debug
	}
}

// WithUserAgent sets the user agent string
func WithUserAgent(userAgent string) Option {
	return func(c *Client) {
		c.userAgent = userAgent
	}
}

// WithHTTPClient sets a custom HTTP client
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		c.httpClient.HTTPClient = httpClient
	}
}

// WithRetryConfig configures retry behavior
func WithRetryConfig(retryMax int, retryWaitMin, retryWaitMax time.Duration) Option {
	return func(c *Client) {
		c.httpClient.RetryMax = retryMax
		c.httpClient.RetryWaitMin = retryWaitMin
		c.httpClient.RetryWaitMax = retryWaitMax
	}
}

// NewClient creates a new HTTP client
func NewClient(baseURL string, tokenManager auth.TokenManager, opts ...Option) *Client {
	retryClient := retryablehttp.NewClient()
	retryClient.RetryMax = 3
	retryClient.RetryWaitMin = 1 * time.Second
	retryClient.RetryWaitMax = 30 * time.Second
	retryClient.Logger = nil // We'll do our own logging

	// Custom retry policy
	retryClient.CheckRetry = func(ctx context.Context, resp *http.Response, err error) (bool, error) {
		// Don't retry on context cancellation
		if ctx.Err() != nil {
			return false, ctx.Err()
		}

		// Retry on connection errors
		if err != nil {
			return true, nil
		}

		// Check the response code
		if resp.StatusCode == 0 || resp.StatusCode >= 500 {
			return true, nil
		}

		// Retry on rate limiting
		if resp.StatusCode == 429 {
			return true, nil
		}

		// Don't retry on client errors
		return false, nil
	}

	client := &Client{
		baseURL:      baseURL,
		httpClient:   retryClient,
		tokenManager: tokenManager,
		userAgent:    "capi-client-go/1.0.0",
	}

	// Apply options
	for _, opt := range opts {
		opt(client)
	}

	return client
}

// Request represents an HTTP request
type Request struct {
	Method  string
	Path    string
	Query   url.Values
	Body    interface{}
	Headers map[string]string
	isRetry bool
}

// Response represents an HTTP response
type Response struct {
	StatusCode int
	Body       []byte
	Headers    http.Header
}

// Do executes an HTTP request with authentication and retry logic
func (c *Client) Do(ctx context.Context, req *Request) (*Response, error) {
	// Build full URL
	fullURL, err := c.buildURL(req.Path, req.Query)
	if err != nil {
		return nil, fmt.Errorf("building URL: %w", err)
	}

	// Prepare body
	var bodyReader io.Reader
	if req.Body != nil {
		bodyBytes, err := json.Marshal(req.Body)
		if err != nil {
			return nil, fmt.Errorf("marshaling request body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	// Create retryable request
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, req.Method, fullURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	// Add authentication
	if c.tokenManager != nil {
		token, err := c.tokenManager.GetToken(ctx)
		if err != nil {
			return nil, fmt.Errorf("getting auth token: %w", err)
		}
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	// Set headers
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", c.userAgent)
	if req.Body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	// Add custom headers
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}

	// Log request if debug is enabled
	if c.debug && c.logger != nil {
		c.logRequest(httpReq)
	}

	// Execute request
	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer httpResp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	response := &Response{
		StatusCode: httpResp.StatusCode,
		Body:       respBody,
		Headers:    httpResp.Header,
	}

	// Log response if debug is enabled
	if c.debug && c.logger != nil {
		c.logResponse(response)
	}

	// Check for errors
	if httpResp.StatusCode >= 400 {
		// Handle authentication errors with retry
		if httpResp.StatusCode == 401 && c.tokenManager != nil && !req.isRetry {
			// Try to refresh token and retry once
			if err := c.tokenManager.RefreshToken(ctx); err == nil {
				retryReq := *req
				retryReq.isRetry = true
				return c.Do(ctx, &retryReq)
			}
		}
		return response, c.parseError(response)
	}

	return response, nil
}

// Get performs a GET request
func (c *Client) Get(ctx context.Context, path string, query url.Values) (*Response, error) {
	return c.Do(ctx, &Request{
		Method: "GET",
		Path:   path,
		Query:  query,
	})
}

// Post performs a POST request
func (c *Client) Post(ctx context.Context, path string, body interface{}) (*Response, error) {
	return c.Do(ctx, &Request{
		Method: "POST",
		Path:   path,
		Body:   body,
	})
}

// Put performs a PUT request
func (c *Client) Put(ctx context.Context, path string, body interface{}) (*Response, error) {
	return c.Do(ctx, &Request{
		Method: "PUT",
		Path:   path,
		Body:   body,
	})
}

// Patch performs a PATCH request
func (c *Client) Patch(ctx context.Context, path string, body interface{}) (*Response, error) {
	return c.Do(ctx, &Request{
		Method: "PATCH",
		Path:   path,
		Body:   body,
	})
}

// Delete performs a DELETE request
func (c *Client) Delete(ctx context.Context, path string) (*Response, error) {
	return c.Do(ctx, &Request{
		Method: "DELETE",
		Path:   path,
	})
}

// DeleteWithQuery performs a DELETE request with query parameters
func (c *Client) DeleteWithQuery(ctx context.Context, path string, queryParams url.Values) (*Response, error) {
	return c.Do(ctx, &Request{
		Method: "DELETE",
		Path:   path,
		Query:  queryParams,
	})
}

// PostRaw performs a POST request with raw body data and content type
func (c *Client) PostRaw(ctx context.Context, path string, body []byte, contentType string) (*Response, error) {
	// Build full URL
	fullURL, err := c.buildURL(path, nil)
	if err != nil {
		return nil, fmt.Errorf("building URL: %w", err)
	}

	// Create retryable request with raw body
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	// Add authentication
	if c.tokenManager != nil {
		token, err := c.tokenManager.GetToken(ctx)
		if err != nil {
			return nil, fmt.Errorf("getting auth token: %w", err)
		}
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	// Set headers
	httpReq.Header.Set("Content-Type", contentType)
	httpReq.Header.Set("Accept", "application/json")
	if c.userAgent != "" {
		httpReq.Header.Set("User-Agent", c.userAgent)
	}

	// Log request if debug is enabled
	if c.debug {
		c.logRequest(httpReq)
	}

	// Execute request
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	// Create Response object first for logging
	response := &Response{
		StatusCode: resp.StatusCode,
		Body:       respBody,
		Headers:    resp.Header,
	}

	// Log response if debug is enabled
	if c.debug {
		c.logResponse(response)
	}

	// Check for errors
	if resp.StatusCode >= 400 {
		return response, c.parseError(response)
	}

	return response, nil
}

// buildURL constructs the full URL for a request
func (c *Client) buildURL(path string, query url.Values) (string, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return "", err
	}

	u.Path = path
	if query != nil {
		u.RawQuery = query.Encode()
	}

	return u.String(), nil
}

// parseError parses an error response from the API
func (c *Client) parseError(resp *Response) error {
	var errResp capi.ErrorResponse
	if err := json.Unmarshal(resp.Body, &errResp); err != nil {
		// If we can't parse as ErrorResponse, return a generic error
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(resp.Body))
	}

	if len(errResp.Errors) == 0 {
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(resp.Body))
	}

	return &errResp
}

// logRequest logs the HTTP request details
func (c *Client) logRequest(req *retryablehttp.Request) {
	fields := map[string]interface{}{
		"method": req.Method,
		"url":    req.URL.String(),
	}

	// Log headers (excluding sensitive ones)
	headers := make(map[string]string)
	for key, values := range req.Header {
		if key != "Authorization" {
			headers[key] = values[0]
		} else {
			headers[key] = "[REDACTED]"
		}
	}
	fields["headers"] = headers

	c.logger.Debug("HTTP Request", fields)
}

// logResponse logs the HTTP response details
func (c *Client) logResponse(resp *Response) {
	fields := map[string]interface{}{
		"status_code": resp.StatusCode,
		"body_size":   len(resp.Body),
	}

	// Log headers
	headers := make(map[string]string)
	for key, values := range resp.Headers {
		headers[key] = values[0]
	}
	fields["headers"] = headers

	// Log body snippet if not too large
	if len(resp.Body) > 0 && len(resp.Body) < 1000 {
		fields["body"] = string(resp.Body)
	}

	c.logger.Debug("HTTP Response", fields)
}

// GetAuthToken returns the current authentication token
func (c *Client) GetAuthToken(ctx context.Context) (string, error) {
	if c.tokenManager == nil {
		return "", fmt.Errorf("no token manager available")
	}
	return c.tokenManager.GetToken(ctx)
}
