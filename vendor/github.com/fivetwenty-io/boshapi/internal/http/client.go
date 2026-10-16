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
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fivetwenty-io/boshapi/internal/auth"
	"github.com/fivetwenty-io/boshapi/internal/constants"
	"github.com/fivetwenty-io/boshapi/internal/redact"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"github.com/hashicorp/go-retryablehttp"
)

// Logger interface for HTTP client logging.
type Logger interface {
	Debug(msg string, fields map[string]interface{})
	Info(msg string, fields map[string]interface{})
	Warn(msg string, fields map[string]interface{})
	Error(msg string, fields map[string]interface{})
}

// contextIDHeader carries the BOSH context ID that ties a request to the
// director task it starts.
const contextIDHeader = "X-Bosh-Context-Id"

// maxContextIDLength is the longest context ID the director accepts.
const maxContextIDLength = 64

// Client wraps the HTTP client with retry logic and authentication.
type Client struct {
	baseURL    string
	httpClient *retryablehttp.Client
	// taskClient sends the GETs that start a director task. Its transport
	// never reuses a connection: net/http replays a GET that fails on a
	// reused connection, below CheckRetry, and that replay could start a
	// second task. It never replays a request on a fresh connection.
	taskClient *retryablehttp.Client
	// streamClient sends the downloads that return a long body. It has no
	// whole-request timeout, so the request context bounds the body read.
	// It retries only until the response headers arrive, because a body
	// that has reached the caller cannot be taken back.
	streamClient *retryablehttp.Client
	// resolve returns the credentials a request carries. It is nil when
	// the director takes no authentication.
	resolve   auth.Resolver
	logger    Logger
	debug     bool
	userAgent string
}

// Option configures the HTTP client.
type Option func(*Client)

// WithLogger sets the logger for HTTP request/response logging.
func WithLogger(logger Logger) Option {
	return func(c *Client) { c.logger = logger }
}

// WithDebug enables verbose HTTP request/response logging.
func WithDebug(debug bool) Option {
	return func(c *Client) { c.debug = debug }
}

// WithUserAgent sets the User-Agent header value.
func WithUserAgent(userAgent string) Option {
	return func(c *Client) { c.userAgent = userAgent }
}

// WithClients makes the client send requests through the given HTTP clients,
// so the director, discovery, and token requests share one TLS configuration.
// The client never follows a redirect, whatever CheckRedirect the given
// clients carry, and it sends a GET that starts a director task through a
// copy of the JSON client's transport that never reuses a connection. When
// clients, its JSON client, or its stream client is nil, the client keeps the
// default JSON, task, or stream client that NewClient gave it.
func WithClients(clients *Clients) Option {
	return func(c *Client) {
		if clients == nil {
			return
		}

		if clients.JSON != nil {
			jsonClient := withoutRedirects(clients.JSON)
			c.httpClient.HTTPClient = jsonClient
			c.taskClient.HTTPClient = withoutConnectionReuse(jsonClient)
		}

		if clients.Stream != nil {
			c.streamClient.HTTPClient = withoutRedirects(clients.Stream)
		}
	}
}

// WithRetryConfig configures the retry behavior for transient failures.
func WithRetryConfig(retryMax int, retryWaitMin, retryWaitMax time.Duration) Option {
	return func(c *Client) {
		for _, retryClient := range []*retryablehttp.Client{c.httpClient, c.taskClient, c.streamClient} {
			retryClient.RetryMax = retryMax
			retryClient.RetryWaitMin = retryWaitMin
			retryClient.RetryWaitMax = retryWaitMax
		}
	}
}

// NewClient creates a new HTTP client with retry logic, authentication, and
// BOSH-specific response handling (task redirects, range requests, etc.).
// Every request that is not marked NoAuth calls resolve for the credentials
// it carries, so resolve may discover them on the first call. A nil resolve
// sends every request without credentials.
func NewClient(baseURL string, resolve auth.Resolver, opts ...Option) *Client {
	jsonClient := withoutRedirects(retryablehttp.NewClient().HTTPClient)

	// The default stream client shares the JSON client's transport and has
	// no whole-request timeout.
	downloadClient := *jsonClient
	downloadClient.Timeout = 0

	client := &Client{
		baseURL:      baseURL,
		httpClient:   newRetryClient(jsonClient),
		taskClient:   newRetryClient(withoutConnectionReuse(jsonClient)),
		streamClient: newRetryClient(&downloadClient),
		resolve:      resolve,
		userAgent:    "boshapi-client-go/1.0.0",
	}

	for _, opt := range opts {
		opt(client)
	}

	return client
}

// newRetryClient returns a go-retryablehttp client that sends through
// httpClient and makes no retries until WithRetryConfig asks for them.
func newRetryClient(httpClient *http.Client) *retryablehttp.Client {
	retryClient := retryablehttp.NewClient()
	retryClient.HTTPClient = httpClient
	retryClient.RetryMax = 0
	retryClient.RetryWaitMin = constants.DefaultRetryWaitMin
	retryClient.RetryWaitMax = constants.DefaultRetryWaitMax
	retryClient.Logger = nil

	// Hand back the last response when retries run out. Without this,
	// go-retryablehttp drains it and returns a plain "giving up" error, so
	// the director's status and body never reach parseError.
	retryClient.ErrorHandler = retryablehttp.PassthroughErrorHandler

	// Retry only what is safe to resend; see checkRetry.
	retryClient.CheckRetry = checkRetry
	retryClient.Backoff = cappedBackoff

	return retryClient
}

// withoutRedirects returns a copy of base that never follows a redirect, or
// nil when base is nil. The caller's client is left as it was.
func withoutRedirects(base *http.Client) *http.Client {
	if base == nil {
		return nil
	}

	enforced := *base
	enforced.CheckRedirect = noRedirect

	return &enforced
}

// withoutConnectionReuse returns a copy of base whose transport opens a new
// connection for every request and closes it after the answer. net/http
// replays a GET only on a reused connection, so a GET sent this way reaches
// the director once per attempt that checkRetry allows. The transport is a
// clone of base's, so it keeps the same TLS settings, dialer, proxy, and
// timeouts. Clone copies the tls.Config rather than sharing it, because
// net/http's HTTP/2 setup writes NextProtos on the config of each transport.
// A transport that is not an *http.Transport cannot be told to drop its
// connections, so requests through the copy fail without being sent.
func withoutConnectionReuse(base *http.Client) *http.Client {
	fresh := *base

	roundTripper := base.Transport
	if roundTripper == nil {
		roundTripper = http.DefaultTransport
	}

	transport, ok := roundTripper.(*http.Transport)
	if !ok {
		fresh.Transport = reuseOnlyTransport{kind: fmt.Sprintf("%T", roundTripper)}

		return &fresh
	}

	transport = transport.Clone()
	transport.DisableKeepAlives = true
	fresh.Transport = transport

	return &fresh
}

// ErrTransportReusesConnections is returned for a GET that starts a director
// task when the HTTP client's transport cannot be held to fresh connections.
var ErrTransportReusesConnections = errors.New("transport cannot be held to fresh connections")

// reuseOnlyTransport stands in for a transport that cannot be told to drop
// its connections. It refuses every request, so a GET that starts a director
// task is never sent where net/http could replay it.
type reuseOnlyTransport struct {
	kind string
}

// RoundTrip refuses the request without sending it.
func (t reuseOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		_ = req.Body.Close()
	}

	return nil, fmt.Errorf("%w: the HTTP client's transport is %s, not *http.Transport, so a GET that "+
		"starts a director task could be replayed on a reused connection and start a second task; "+
		"build the clients with NewClients", ErrTransportReusesConnections, t.kind)
}

// StreamClient returns the HTTP client for long downloads. It shares the
// transport of the request client but has no whole-request timeout.
func (c *Client) StreamClient() *http.Client {
	return c.streamClient.HTTPClient
}

// Do executes an HTTP request with authentication and retry logic.
func (c *Client) Do(ctx context.Context, req *Request) (*Response, error) {
	err := checkContextID(boshapi.ContextIDFrom(ctx), req)
	if err != nil {
		return nil, err
	}

	fullURL, err := c.buildURL(req.Path, req.Query)
	if err != nil {
		return nil, fmt.Errorf("building URL: %w", err)
	}

	bodyReader, err := c.prepareBody(req)
	if err != nil {
		return nil, err
	}

	reqCtx, state := withRequestState(ctx, req)

	httpReq, err := retryablehttp.NewRequestWithContext(reqCtx, req.Method, fullURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	sent, err := c.setupHeaders(ctx, httpReq, req)
	if err != nil {
		return nil, err
	}

	var stream *uploadBody

	if req.Stream != nil {
		// The transport reads the caller's reader through a pipe that the
		// context and finish can close under it, and the caller closes the
		// reader itself. go-retryablehttp built the request without a body
		// and gave it a GetBody that returns an empty one. net/http calls
		// GetBody to send a request again after an HTTP/2 refused stream or
		// GOAWAY, or after a reused HTTP/1.1 connection fails before it
		// writes, so it must go, or the upload would go out a second time
		// with no body.
		stream = newUploadBody(reqCtx, req.Stream, req.StreamSize)
		httpReq.Body = stream
		httpReq.ContentLength = req.StreamSize
		httpReq.GetBody = nil
	}

	response, err := c.executeRequest(httpReq, req, state, sent)

	// Nothing reads the caller's reader once Do returns, unless the context
	// ended while a Read on it was blocked.
	stream.finish(reqCtx)

	if err != nil {
		return nil, err
	}

	response.method, response.path, response.baseURL = req.Method, req.Path, c.baseURL
	response.contextID = httpReq.Header.Get(contextIDHeader)
	response.sent = sent

	return c.handleResponse(ctx, response, req, sent)
}

// Get performs a GET request.
func (c *Client) Get(ctx context.Context, path string, query url.Values) (*Response, error) {
	return c.Do(ctx, &Request{Method: http.MethodGet, Path: path, Query: query})
}

// GetStartingTask performs a GET that makes the director start a task, such
// as a detailed VM list or a log fetch. Resending it could leave a duplicate
// task, so it follows the mutation rule and is never resent blindly.
func (c *Client) GetStartingTask(ctx context.Context, path string, query url.Values) (*Response, error) {
	return c.Do(ctx, &Request{Method: http.MethodGet, Path: path, Query: query, CreatesTask: true})
}

// Post performs a POST request with JSON body.
func (c *Client) Post(ctx context.Context, path string, body interface{}) (*Response, error) {
	return c.Do(ctx, &Request{Method: http.MethodPost, Path: path, Body: body})
}

// PostWithQuery performs a POST with JSON body and query parameters.
func (c *Client) PostWithQuery(ctx context.Context, path string, body interface{}, query url.Values) (*Response, error) {
	return c.Do(ctx, &Request{Method: http.MethodPost, Path: path, Body: body, Query: query})
}

// PostYAML performs a POST request with YAML body (Content-Type: text/yaml).
func (c *Client) PostYAML(ctx context.Context, path string, yamlBody string, query url.Values) (*Response, error) {
	return c.Do(ctx, &Request{
		Method:      http.MethodPost,
		Path:        path,
		RawBody:     []byte(yamlBody),
		ContentType: "text/yaml",
		Query:       query,
	})
}

// PostDiff performs a POST with JSON body to a diff route, whose error
// answers an APIError keeps up to maxDiffErrorDescription bytes of.
func (c *Client) PostDiff(ctx context.Context, path string, body interface{}) (*Response, error) {
	return c.Do(ctx, &Request{Method: http.MethodPost, Path: path, Body: body, KeepsDiffError: true})
}

// PostYAMLDiff performs a POST with YAML body (Content-Type: text/yaml) to a
// diff route, whose error answers an APIError keeps up to
// maxDiffErrorDescription bytes of.
func (c *Client) PostYAMLDiff(ctx context.Context, path string, yamlBody string, query url.Values) (*Response, error) {
	return c.Do(ctx, &Request{
		Method:         http.MethodPost,
		Path:           path,
		RawBody:        []byte(yamlBody),
		ContentType:    "text/yaml",
		Query:          query,
		KeepsDiffError: true,
	})
}

// PutYAML performs a PUT request with YAML body (Content-Type: text/yaml)
// and query parameters. An empty yamlBody sends a zero-length body, which
// the director treats as "use the stored manifest" on state-change routes.
func (c *Client) PutYAML(ctx context.Context, path string, yamlBody string, query url.Values) (*Response, error) {
	return c.Do(ctx, &Request{
		Method:      http.MethodPut,
		Path:        path,
		RawBody:     []byte(yamlBody),
		ContentType: "text/yaml",
		Query:       query,
	})
}

// Put performs a PUT request.
func (c *Client) Put(ctx context.Context, path string, body interface{}) (*Response, error) {
	return c.Do(ctx, &Request{Method: http.MethodPut, Path: path, Body: body})
}

// PutWithQuery performs a PUT request with query parameters.
func (c *Client) PutWithQuery(ctx context.Context, path string, body interface{}, query url.Values) (*Response, error) {
	return c.Do(ctx, &Request{Method: http.MethodPut, Path: path, Body: body, Query: query})
}

// Delete performs a DELETE request.
func (c *Client) Delete(ctx context.Context, path string) (*Response, error) {
	return c.Do(ctx, &Request{Method: http.MethodDelete, Path: path})
}

// DeleteWithQuery performs a DELETE request with query parameters.
func (c *Client) DeleteWithQuery(ctx context.Context, path string, query url.Values) (*Response, error) {
	return c.Do(ctx, &Request{Method: http.MethodDelete, Path: path, Query: query})
}

// GetWithRange performs a GET with Range header for task output streaming.
func (c *Client) GetWithRange(ctx context.Context, path string, query url.Values, rangeStart, rangeEnd int64) (*Response, error) {
	headers := map[string]string{
		"Range": fmt.Sprintf("bytes=%d-%d", rangeStart, rangeEnd),
	}

	return c.Do(ctx, &Request{Method: http.MethodGet, Path: path, Query: query, Headers: headers})
}

// prepareBody marshals the request body.
func (c *Client) prepareBody(req *Request) (io.Reader, error) {
	if req.RawBody != nil {
		return bytes.NewReader(req.RawBody), nil
	}
	if req.Body == nil {
		return nil, nil
	}
	bodyBytes, err := json.Marshal(req.Body)
	if err != nil {
		return nil, fmt.Errorf("marshaling request body: %w", err)
	}

	return bytes.NewReader(bodyBytes), nil
}

// sentAuth records the credentials a request carried. For a bearer request
// it holds the token and the manager that issued it, so a 401 can ask that
// manager to replace exactly that token. For a request sent with HTTP Basic
// it holds the username and password, which are used only to hide them in an
// error. It is empty for a request sent without credentials.
type sentAuth struct {
	tokens   auth.TokenManager
	token    string
	username string
	password string
}

// hide returns text with the credentials the request carried replaced by
// redact.Hidden, because a director, or a proxy in front of it, that echoes
// the request headers repeats them.
func (s sentAuth) hide(text string) string {
	text = redact.Bearer(text, s.token)

	return redact.Basic(text, s.username, s.password)
}

// authenticate sets the Authorization header that req carries and returns
// what it sent. A failure means the request is never sent.
func (c *Client) authenticate(ctx context.Context, httpReq *retryablehttp.Request, req *Request) (sentAuth, error) {
	if req.NoAuth || c.resolve == nil {
		return sentAuth{}, nil
	}

	creds, err := c.resolve(ctx)
	if err != nil {
		return sentAuth{}, fmt.Errorf("%s %s was not sent: %w", req.Method, req.Path, err)
	}

	if creds.Tokens == nil {
		if creds.Username != "" {
			httpReq.SetBasicAuth(creds.Username, creds.Password)

			return sentAuth{username: creds.Username, password: creds.Password}, nil
		}

		return sentAuth{}, nil
	}

	token, err := creds.Tokens.GetToken(ctx)
	if err != nil {
		return sentAuth{}, fmt.Errorf("%s %s was not sent: %w", req.Method, req.Path, err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+token)

	return sentAuth{tokens: creds.Tokens, token: token}, nil
}

// setupHeaders configures authentication and standard headers, and returns
// the credentials the request carries.
func (c *Client) setupHeaders(ctx context.Context, httpReq *retryablehttp.Request, req *Request) (sentAuth, error) {
	sent, err := c.authenticate(ctx, httpReq, req)
	if err != nil {
		return sentAuth{}, err
	}

	// Set standard headers.
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", c.userAgent)

	// Set content type.
	if req.ContentType != "" {
		httpReq.Header.Set("Content-Type", req.ContentType)
	} else if req.Body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	// Set BOSH context ID.
	if contextID := boshapi.ContextIDFrom(ctx); contextID != "" {
		httpReq.Header.Set(contextIDHeader, contextID)
	}

	// Add custom headers.
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}

	return sent, nil
}

// executeRequest performs the actual HTTP request. A request that gets no
// answer returns a *boshapi.TransportError. sent is what the request carried,
// which the debug log of the answer must not repeat.
func (c *Client) executeRequest(
	httpReq *retryablehttp.Request, req *Request, state *requestState, sent sentAuth,
) (*Response, error) {
	if c.debug && c.logger != nil {
		c.logRequest(httpReq)
	}

	retryClient := c.httpClient

	switch {
	case req.Stream != nil:
		retryClient = c.streamClient
	case req.CreatesTask:
		retryClient = c.taskClient
	}

	httpResp, err := retryClient.Do(httpReq)
	if err != nil {
		if httpResp != nil {
			_ = httpResp.Body.Close()
		}

		return nil, newTransportError(httpReq.Request, req, state, err)
	}

	defer func() {
		err := httpResp.Body.Close()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to close response body: %v\n", err)
		}
	}()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, newTransportError(httpReq.Request, req, state, fmt.Errorf("reading response body: %w", err))
	}

	response := &Response{
		StatusCode: httpResp.StatusCode,
		Body:       respBody,
		Headers:    httpResp.Header,
	}

	if c.debug && c.logger != nil {
		c.logResponse(response, sent)
	}

	return response, nil
}

// handleResponse processes the response, handling redirects and errors.
// sent is the token the request carried, which a 401 asks to replace.
func (c *Client) handleResponse(ctx context.Context, response *Response, req *Request, sent sentAuth) (*Response, error) {
	// Handle redirects. The client never follows one. The director's 302 or
	// 303 to /tasks/<id> answers a request that starts a task. Any other
	// redirect is an error.
	if response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < constants.HTTPStatusBadRequest {
		return c.handleRedirect(response, req)
	}

	// Handle success.
	if response.StatusCode < http.StatusMultipleChoices {
		return response, nil
	}

	if response.StatusCode == http.StatusUnauthorized && sent.tokens != nil && !req.isRetry {
		rejected := newAPIError(response.StatusCode, response.Body, req, sent)

		switch {
		case !boshapi.IsUnauthorized(rejected):
			// A 401 with code 600000 refuses a valid token that lacks a
			// scope. A new token for the same client has the same scopes,
			// so neither a refresh nor a resend can help.
			return response, rejected
		case req.Stream != nil:
			return response, refreshAfterStreamRejected(ctx, rejected, sent)
		}

		return c.resendWithNewToken(ctx, response, rejected, req, sent)
	}

	return response, c.parseError(response, req, sent)
}

// resendWithNewToken answers a 401 to a request that carried a bearer token,
// which rejected describes. It asks the token manager to replace the token
// the director rejected, not whatever token is cached now, and sends the
// request once more under the same context, so the resend keeps its context
// ID and retry rules. A copy of
// the request keeps CreatesTask. A director rejects a request before acting
// on it, so the resend cannot repeat a change. When the refresh fails, the
// error carries both the 401 and the reason the refresh failed.
func (c *Client) resendWithNewToken(
	ctx context.Context, response *Response, rejected *boshapi.APIError, req *Request, sent sentAuth,
) (*Response, error) {
	err := refreshRejectedToken(ctx, rejected, sent)
	if err != nil {
		return response, err
	}

	resend := *req
	resend.isRetry = true

	resent, err := c.Do(ctx, &resend)

	return resent, explainFreshTokenRejected(err)
}

// explainFreshTokenRejected adds the likely causes to a 401 that answers the
// resend of a request, because the resend carried a token that was issued a
// moment before. The *APIError stays in the chain for errors.As. Any other
// error, and a nil error, is returned as it is.
func explainFreshTokenRejected(err error) error {
	var apiErr *boshapi.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		return err
	}

	return fmt.Errorf("%w; the resend carried a freshly issued token, and the director rejected that token too. "+
		"Check that the UAA client has the authority the director requires, such as bosh.admin, which is "+
		"likelier now that the token request asks for no scope and UAA grants only the client's own authorities. "+
		"Also check that the director trusts the UAA that issued the token, because a director that trusts a "+
		"different UAA rejects every token this one issues", err)
}

// refreshRejectedToken asks the token manager to replace the token a 401
// rejected. When the refresh fails, the error carries both the 401 and the
// reason the refresh failed, and says the request was not resent.
func refreshRejectedToken(ctx context.Context, rejected *boshapi.APIError, sent sentAuth) error {
	_, err := sent.tokens.RefreshToken(ctx, sent.token)
	if err != nil {
		return fmt.Errorf("%w; the request was not resent, because refreshing the token failed: %w", rejected, err)
	}

	return nil
}

// refreshAfterStreamRejected answers a 401 to a streamed request. The body
// was read as it went out and cannot be read again, so the request is never
// resent, but the token the director rejected is replaced, so that the
// caller's next request carries a new one. A director checks the token only
// after its nginx has taken the whole body, so without the refresh every
// retry of the upload would send the whole body again with the same rejected
// token until that token expired. The error always wraps the 401.
func refreshAfterStreamRejected(ctx context.Context, rejected *boshapi.APIError, sent sentAuth) error {
	_, err := sent.tokens.RefreshToken(ctx, sent.token)
	if err != nil {
		return fmt.Errorf("%w; the upload was not resent, because a streamed body cannot be read twice, "+
			"and replacing the rejected token failed: %w", rejected, err)
	}

	return fmt.Errorf("%w; the upload was not resent, because a streamed body cannot be read twice, "+
		"but the rejected token was replaced, so sending the upload again carries a new token", rejected)
}

// isTaskRedirectStatus reports whether status is one the director uses to
// point a task-creating request at its task. Sinatra's redirect sends a 303
// for any request that is not a GET over HTTP/1.1 and a 302 otherwise. Every
// other 3xx status is refused.
func isTaskRedirectStatus(status int) bool {
	return status == http.StatusFound || status == http.StatusSeeOther
}

// handleRedirect reads the task ID from the Location header of a 302 or 303
// answer to a request that is not safe to resend, which may be absolute or
// relative.
// Only its path is used: the client always talks to the configured director
// URL, whatever host the director put in Location. No request is made for the
// task, so a failure after the director answered cannot lose its ID. Callers
// that want the task description ask the director for the task by ID. Any
// other redirect, and a 302 or 303 whose Location does not end in
// /tasks/<id>, returns a *boshapi.RedirectError. A request marked
// NoTaskRedirect has no task to read, so its error is the one for a call
// that expects the director's own answer.
func (c *Client) handleRedirect(response *Response, req *Request) (*Response, error) {
	location := response.Headers.Get("Location")
	mutation := isMutation(req)

	if mutation && !req.NoTaskRedirect && isTaskRedirectStatus(response.StatusCode) {
		taskID, ok := taskIDFromLocation(location)
		if ok {
			response.Task = &boshapi.Task{ID: taskID, State: constants.TaskStateQueued}

			return response, nil
		}
	}

	return nil, &boshapi.RedirectError{
		Method:      req.Method,
		Path:        req.Path,
		Status:      response.StatusCode,
		Location:    location,
		DirectorURL: c.baseURL,
		ContextID:   response.contextID,
		Mutation:    mutation && !req.NoTaskRedirect,
	}
}

// errContextID reports a context ID that the director would reject.
var errContextID = errors.New("the context ID is not valid")

// checkContextID refuses a context ID the director would reject, before any
// request is sent. An ID holds printable ASCII only, from 0x20 to 0x7E, so a
// control character cannot reach the header and the 64-character limit is
// the same as a byte limit.
func checkContextID(contextID string, req *Request) error {
	for offset := range len(contextID) {
		if contextID[offset] < 0x20 || contextID[offset] > 0x7e {
			return fmt.Errorf("%w: %q for %s %s has a byte outside printable ASCII at offset %d, "+
				"and nothing was sent. The %s header takes only the characters from space to tilde. "+
				"Use an ID such as a UUID in boshapi.WithContextID",
				errContextID, contextID, req.Method, req.Path, offset, contextIDHeader)
		}
	}

	if len(contextID) <= maxContextIDLength {
		return nil
	}

	return fmt.Errorf("%w: %q for %s %s is %d characters long, but the BOSH director accepts at most %d "+
		"in the %s header, and nothing was sent. Use a shorter ID, such as a UUID, in boshapi.WithContextID",
		errContextID, contextID, req.Method, req.Path, len(contextID), maxContextIDLength, contextIDHeader)
}

// taskIDFromLocation returns the ID in a Location of the form
// [scheme://host]/tasks/<id>, with an optional trailing slash and query.
func taskIDFromLocation(location string) (int, bool) {
	parsed, err := url.Parse(location)
	if err != nil {
		return 0, false
	}

	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) < 2 || segments[len(segments)-2] != "tasks" {
		return 0, false
	}

	taskID, err := strconv.Atoi(segments[len(segments)-1])
	if err != nil || taskID < 1 {
		return 0, false
	}

	return taskID, true
}

// buildURL constructs the full URL.
func (c *Client) buildURL(path string, query url.Values) (string, error) {
	parsedURL, err := url.Parse(c.baseURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse base URL: %w", err)
	}

	ref, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("failed to parse request path: %w", err)
	}

	parsedURL.Path, parsedURL.RawPath = ref.Path, ref.RawPath

	if len(query) > 0 {
		q := parsedURL.Query()
		for key, values := range query {
			q[key] = append([]string(nil), values...)
		}
		parsedURL.RawQuery = q.Encode()
	}

	return parsedURL.String(), nil
}

// maxErrorDescription is the most of a non-JSON error body that an APIError
// keeps, so a director fronted by a proxy cannot flood an error message with
// an HTML page.
const maxErrorDescription = 1024

// maxDiffErrorDescription is the most of a JSON error body that an APIError
// keeps for a diff route. The director sends {"diff":[],"error":"..."} with
// text built from the content it failed to diff, which can pass
// maxErrorDescription, and the caller reads that text from the body.
const maxDiffErrorDescription = 64 * 1024

// truncationMarker ends a description that was cut to maxErrorDescription
// bytes, so a reader can tell the page was longer.
const truncationMarker = "…(truncated)"

// parseError builds an APIError from an error response. The director's JSON
// error shape is {"code": int, "description": string}. Any other body becomes
// the description, trimmed and cut to maxErrorDescription bytes. sent is what
// the request carried, which the description must not repeat.
func (c *Client) parseError(resp *Response, req *Request, sent sentAuth) error {
	return newAPIError(resp.StatusCode, resp.Body, req, sent)
}

// newAPIError builds the APIError for an answer with the given status and
// body, as parseError describes. A director, or a proxy in front of it, that
// echoes the request headers repeats the bearer token or the Basic
// credentials, so sent.hide replaces them in the description, before the
// description is cut.
func newAPIError(status int, respBody []byte, req *Request, sent sentAuth) *boshapi.APIError {
	apiErr := &boshapi.APIError{
		StatusCode: status,
		Method:     req.Method,
		Path:       req.Path,
		Mutation:   isMutation(req),
	}

	var body struct {
		Code        int    `json:"code"`
		Description string `json:"description"`
	}

	err := json.Unmarshal(respBody, &body)
	if err == nil && (body.Code != 0 || body.Description != "") {
		apiErr.Code = body.Code
		apiErr.Description = sent.hide(body.Description)

		return apiErr
	}

	text := sent.hide(strings.TrimSpace(string(respBody)))
	limit := maxErrorDescription
	if req.KeepsDiffError && strings.HasPrefix(text, "{") {
		limit = maxDiffErrorDescription
	}

	if len(text) > limit {
		text = strings.ToValidUTF8(text[:limit], "") + truncationMarker
	}

	apiErr.Description = text

	return apiErr
}

// logRequest logs HTTP request details.
func (c *Client) logRequest(req *retryablehttp.Request) {
	fields := map[string]interface{}{
		"method": req.Method,
		"url":    req.URL.String(),
	}

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

// logResponse logs HTTP response details. A director that echoes the request
// headers repeats the credentials in the body, so sent.hide replaces them.
func (c *Client) logResponse(resp *Response, sent sentAuth) {
	fields := map[string]interface{}{
		"status_code": resp.StatusCode,
		"body_size":   len(resp.Body),
	}

	headers := make(map[string]string)
	for key, values := range resp.Headers {
		headers[key] = values[0]
	}
	fields["headers"] = headers

	if len(resp.Body) > 0 && len(resp.Body) < 1000 {
		fields["body"] = sent.hide(string(resp.Body))
	}

	c.logger.Debug("HTTP Response", fields)
}
