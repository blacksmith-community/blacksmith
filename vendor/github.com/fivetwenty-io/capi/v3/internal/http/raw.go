package http

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/fivetwenty-io/capi/v3/pkg/capi"
	"github.com/hashicorp/go-retryablehttp"
)

const (
	maxRawRedirects  = 10
	maxRawDecodeIter = 4
)

// RawResult is the outcome of DoRaw.
type RawResult struct {
	Status    int
	Header    http.Header
	Body      []byte
	Truncated bool
}

// validateRawMethod returns the canonical method or an error.
func validateRawMethod(method string) (string, error) {
	if method == "" {
		return http.MethodGet, nil
	}

	switch m := strings.ToUpper(method); m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return m, nil
	}

	return "", capi.ErrRawInvalidMethod
}

func hasControlOrBackslash(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return true
		}
	}

	return false
}

func hasDotDotSegment(s string) bool {
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return true
		}
	}

	return false
}

// validateRawPath refuses any path that is not a plain absolute API path.
// It returns the once-decoded path.
func validateRawPath(raw string) (string, error) {
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "", capi.ErrRawInvalidPath
	}

	if strings.ContainsAny(raw, "?#") || hasControlOrBackslash(raw) {
		return "", capi.ErrRawInvalidPath
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil {
		return "", capi.ErrRawInvalidPath
	}

	return decodeRawPath(raw)
}

func unsafeDecoded(decoded string) bool {
	return hasControlOrBackslash(decoded) || hasDotDotSegment(decoded) ||
		!strings.HasPrefix(decoded, "/") || strings.HasPrefix(decoded, "//")
}

// decodeRawPath decodes repeatedly so double-encoded forms are checked too.
// An encoded slash is refused outright because it changes the path once
// decoded. It returns the once-decoded path.
func decodeRawPath(raw string) (string, error) {
	current := raw
	firstDecoded := ""

	for round := range maxRawDecodeIter {
		lower := strings.ToLower(current)
		if strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") {
			return "", capi.ErrRawInvalidPath
		}

		decoded, err := url.PathUnescape(current)
		if err != nil || unsafeDecoded(decoded) {
			return "", capi.ErrRawInvalidPath
		}

		if round == 0 {
			firstDecoded = decoded
		}

		if decoded == current {
			break
		}

		current = decoded
	}

	return firstDecoded, nil
}

func sameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && a.Hostname() == b.Hostname() && effectivePort(a) == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}

	if u.Scheme == "https" {
		return "443"
	}

	return "80"
}

// DoRaw sends a request to an arbitrary API path using the client's token
// handling and retry rules. See capi.RawRequest for the path rules.
func (c *Client) DoRaw(ctx context.Context, req capi.RawRequest) (*RawResult, error) {
	method, err := validateRawMethod(req.Method)
	if err != nil {
		return nil, err
	}

	decoded, err := validateRawPath(req.Path)
	if err != nil {
		return nil, err
	}

	target, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, errors.New("parsing base URL failed") //nolint:err113 // message must not echo the URL
	}

	target.Path = decoded
	target.RawPath = req.Path
	target.RawQuery = req.Query.Encode()

	origin := *target

	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}

	httpReq, err := retryablehttp.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, errors.New("creating raw request failed") //nolint:err113 // message must not echo the request
	}

	if c.tokenManager != nil {
		token, terr := c.tokenManager.GetToken(ctx)
		if terr != nil {
			return nil, fmt.Errorf("getting auth token: %w", terr)
		}

		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", c.userAgent)

	if len(req.Body) > 0 {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.rawClient(&origin).Do(httpReq)
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
		}

		return nil, rawTransportError(err)
	}

	defer func() { _ = resp.Body.Close() }()

	limit := int64(capi.RawMaxResponseBytes)

	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading raw response: %w", err)
	}

	result := &RawResult{Status: resp.StatusCode, Header: resp.Header}

	if int64(len(data)) > limit {
		data = data[:limit]
		result.Truncated = true
	}

	result.Body = data

	if c.debug && c.logger != nil {
		c.logger.Debug("raw request", map[string]any{
			"method": method, "path": req.Path, "status_code": resp.StatusCode,
		})
	}

	return result, nil
}

// rawClient copies the retry settings and transport of the shared client but
// installs a redirect policy that only stays on the original origin.
func (c *Client) rawClient(origin *url.URL) *retryablehttp.Client {
	inner := *c.httpClient.HTTPClient
	inner.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= maxRawRedirects {
			return capi.ErrRawRedirectRefused
		}

		if !sameOrigin(origin, next.URL) {
			return capi.ErrRawRedirectRefused
		}

		return nil
	}

	return &retryablehttp.Client{
		HTTPClient:   &inner,
		RetryWaitMin: c.httpClient.RetryWaitMin,
		RetryWaitMax: c.httpClient.RetryWaitMax,
		RetryMax:     c.httpClient.RetryMax,
		CheckRetry:   refusedRedirectNotRetried(c.httpClient.CheckRetry),
		Backoff:      c.httpClient.Backoff,
		// Return the last response after retries are exhausted so the
		// caller sees the status instead of a generic failure.
		ErrorHandler: retryablehttp.PassthroughErrorHandler,
	}
}

// refusedRedirectNotRetried wraps a retry policy so a redirect we refused is
// final. Without it the refusal looks like a transport error and the request
// would be sent to the API again.
func refusedRedirectNotRetried(next retryablehttp.CheckRetry) retryablehttp.CheckRetry {
	return func(ctx context.Context, resp *http.Response, err error) (bool, error) {
		if errors.Is(err, capi.ErrRawRedirectRefused) {
			return false, err
		}

		return next(ctx, resp, err)
	}
}

// rawTransportError drops the URL that net/http embeds in its errors.
func rawTransportError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("executing raw request: %w", urlErr.Err)
	}

	return fmt.Errorf("executing raw request: %w", err)
}
