package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/fivetwenty-io/capi/v3/internal/auth"
	"github.com/fivetwenty-io/capi/v3/internal/constants"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
	"github.com/hashicorp/go-retryablehttp"
)

// Logger interface for HTTP client logging.
type Logger interface {
	Debug(msg string, fields map[string]any)
	Info(msg string, fields map[string]any)
	Warn(msg string, fields map[string]any)
	Error(msg string, fields map[string]any)
}

// Client wraps the HTTP client with retry logic and authentication.
type Client struct {
	baseURL      string
	httpClient   *retryablehttp.Client
	tokenManager auth.TokenManager
	logger       Logger
	debug        bool
	userAgent    string
}

// Option configures the HTTP client.
// Static errors for err113 compliance.
var (
	ErrNoTokenManagerAvailable = errors.New("no token manager available")
)

type Option func(*Client)

// WithLogger sets the logger for the client.
func WithLogger(logger Logger) Option {
	return func(c *Client) {
		c.logger = logger
	}
}

// WithDebug enables debug logging.
func WithDebug(debug bool) Option {
	return func(c *Client) {
		c.debug = debug
	}
}

// WithUserAgent sets the user agent string.
func WithUserAgent(userAgent string) Option {
	return func(c *Client) {
		c.userAgent = userAgent
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		c.httpClient.HTTPClient = httpClient
	}
}

// WithRetryConfig configures retry behavior.
func WithRetryConfig(retryMax int, retryWaitMin, retryWaitMax time.Duration) Option {
	return func(c *Client) {
		c.httpClient.RetryMax = retryMax
		c.httpClient.RetryWaitMin = retryWaitMin
		c.httpClient.RetryWaitMax = retryWaitMax
	}
}

// NewClient creates a new HTTP client.
func NewClient(baseURL string, tokenManager auth.TokenManager, opts ...Option) *Client {
	retryClient := retryablehttp.NewClient()
	retryClient.RetryMax = 3
	retryClient.RetryWaitMin = 1 * time.Second
	retryClient.RetryWaitMax = constants.ExtendedRetryWaitMax
	retryClient.Logger = nil // We'll do our own logging

	// Custom retry policy
	retryClient.CheckRetry = func(ctx context.Context, resp *http.Response, err error) (bool, error) {
		// Don't retry on context cancellation
		if ctx.Err() != nil {
			return false, ctx.Err()
		}

		// Retry on connection errors
		if err != nil {
			return true, err
		}

		// Check the response code
		if resp.StatusCode == 0 || resp.StatusCode >= 500 {
			return true, nil
		}

		// Retry on rate limiting
		if resp.StatusCode == http.StatusTooManyRequests {
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

	// Apply options. WithHTTPClient may replace retryClient.HTTPClient, so
	// the authRetryTransport must be installed AFTER options are applied so
	// it wraps whatever transport is ultimately in effect.
	for _, opt := range opts {
		opt(client)
	}

	// Install the 401-refresh-and-retry RoundTripper on the inner
	// *http.Client's Transport so every request (including those driven
	// by retryablehttp's CheckRetry loop) transparently picks up a token
	// refresh when the server returns 401.
	retryClient.HTTPClient.Transport = newAuthRetryTransport(
		retryClient.HTTPClient.Transport, tokenManager)

	return client
}

// Request represents an HTTP request.
type Request struct {
	Method  string
	Path    string
	Query   url.Values
	Body    any
	Headers map[string]string
}

// Response represents an HTTP response.
type Response struct {
	StatusCode int
	Body       []byte
	Headers    http.Header
}

// Do executes an HTTP request with authentication and retry logic.
func (c *Client) Do(ctx context.Context, req *Request) (*Response, error) {
	// Build full URL
	fullURL, err := c.buildURL(req.Path, req.Query)
	if err != nil {
		return nil, fmt.Errorf("building URL: %w", err)
	}

	// Prepare body
	bodyReader, err := c.prepareRequestBody(req.Body)
	if err != nil {
		return nil, err
	}

	// Create retryable request
	httpReq, err := retryablehttp.NewRequestWithContext(ctx, req.Method, fullURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	// Setup authentication and headers
	err = c.setupAuthAndHeaders(ctx, httpReq, req)
	if err != nil {
		return nil, err
	}

	// Execute request
	response, err := c.executeHTTPRequest(httpReq)
	if err != nil {
		return nil, err
	}

	// Handle error responses with retry logic
	return c.handleResponseError(ctx, response, req)
}

// Get performs a GET request.
func (c *Client) Get(ctx context.Context, path string, query url.Values) (*Response, error) {
	return c.Do(ctx, &Request{
		Method: "GET",
		Path:   path,
		Query:  query,
	})
}

// Post performs a POST request.
func (c *Client) Post(ctx context.Context, path string, body any) (*Response, error) {
	return c.Do(ctx, &Request{
		Method: "POST",
		Path:   path,
		Body:   body,
	})
}

// Put performs a PUT request.
func (c *Client) Put(ctx context.Context, path string, body any) (*Response, error) {
	return c.Do(ctx, &Request{
		Method: "PUT",
		Path:   path,
		Body:   body,
	})
}

// Patch performs a PATCH request.
func (c *Client) Patch(ctx context.Context, path string, body any) (*Response, error) {
	return c.Do(ctx, &Request{
		Method: "PATCH",
		Path:   path,
		Body:   body,
	})
}

// Delete performs a DELETE request.
func (c *Client) Delete(ctx context.Context, path string) (*Response, error) {
	return c.Do(ctx, &Request{
		Method: "DELETE",
		Path:   path,
	})
}

// DeleteWithQuery performs a DELETE request with query parameters.
func (c *Client) DeleteWithQuery(ctx context.Context, path string, queryParams url.Values) (*Response, error) {
	return c.Do(ctx, &Request{
		Method: "DELETE",
		Path:   path,
		Query:  queryParams,
	})
}

// PostRaw performs a POST request with raw body data and content type.
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

	defer func() {
		err := resp.Body.Close()
		if err != nil && c.logger != nil {
			c.logger.Warn("failed to close response body", map[string]any{"error": err.Error()})
		}
	}()

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
	if resp.StatusCode >= constants.HTTPStatusBadRequest {
		return response, c.parseError(response)
	}

	return response, nil
}

// GetAuthToken returns the current authentication token.
func (c *Client) GetAuthToken(ctx context.Context) (string, error) {
	if c.tokenManager == nil {
		return "", ErrNoTokenManagerAvailable
	}

	token, err := c.tokenManager.GetToken(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get token: %w", err)
	}

	return token, nil
}

// prepareRequestBody marshals the request body to JSON if present.
func (c *Client) prepareRequestBody(body any) (io.Reader, error) {
	if body == nil {
		return nil, nil
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshaling request body: %w", err)
	}

	return bytes.NewReader(bodyBytes), nil
}

// setupAuthAndHeaders configures authentication and headers for the request.
func (c *Client) setupAuthAndHeaders(ctx context.Context, httpReq *retryablehttp.Request, req *Request) error {
	// Add authentication
	if c.tokenManager != nil {
		token, err := c.tokenManager.GetToken(ctx)
		if err != nil {
			return fmt.Errorf("getting auth token: %w", err)
		}

		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	// Set standard headers
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", c.userAgent)

	if req.Body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	// Add custom headers
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}

	return nil
}

// executeHTTPRequest performs the actual HTTP request with error handling.
func (c *Client) executeHTTPRequest(httpReq *retryablehttp.Request) (*Response, error) {
	// Log request if debug is enabled
	if c.debug && c.logger != nil {
		c.logRequest(httpReq)
	}

	// Execute request
	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}

	defer func() {
		err := httpResp.Body.Close()
		if err != nil && c.logger != nil {
			c.logger.Warn("failed to close response body", map[string]any{"error": err.Error()})
		}
	}()

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

	return response, nil
}

// handleResponseError processes HTTP error responses. The 401 refresh +
// retry cycle is handled transparently inside the authRetryTransport
// installed in NewClient, so this method only needs to turn error status
// codes into sentinel-wrapping errors via parseError.
func (c *Client) handleResponseError(_ context.Context, response *Response, _ *Request) (*Response, error) {
	if response.StatusCode < constants.HTTPStatusBadRequest {
		return response, nil
	}

	return response, c.parseError(response)
}

// buildURL constructs the full URL for a request.
func (c *Client) buildURL(path string, query url.Values) (string, error) {
	parsedURL, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse base URL: %w", err)
	}

	parsedURL.Path = path
	if query != nil {
		parsedURL.RawQuery = query.Encode()
	}

	return parsedURL.String(), nil
}

// parseError converts an HTTP error response into a sentinel-wrapped error
// by delegating to capi.MapHTTPError. This is the ONLY call site inside
// capi/v3 that constructs error values from HTTP responses, so every
// method on the CAPI client transparently returns sentinel-wrapping errors
// that callers can detect with errors.Is(err, capi.ErrNotFound) and
// friends, while still being able to inspect the underlying CF error
// envelope via errors.As(err, &capi.ResponseError{}).
func (c *Client) parseError(resp *Response) error {
	//nolint:wrapcheck // MapHTTPError already returns a sentinel-wrapping error; wrapping again would double-wrap it.
	return capi.MapHTTPError(resp.StatusCode, resp.Body)
}

// logRequest logs the HTTP request details.
func (c *Client) logRequest(req *retryablehttp.Request) {
	fields := map[string]any{
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

// logResponse logs the HTTP response details.
func (c *Client) logResponse(resp *Response) {
	fields := map[string]any{
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
