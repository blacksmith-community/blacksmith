package capi

import (
	"errors"
	"net/http"
	"net/url"
)

// RawMaxResponseBytes is the largest response body Raw returns. A larger
// body is cut at this size and the response is marked as truncated.
const RawMaxResponseBytes = 16 << 20

// Static errors returned by Raw.
var (
	// ErrRawInvalidPath is returned when a raw request path is not a safe,
	// absolute API path.
	ErrRawInvalidPath = errors.New("invalid raw request path")
	// ErrRawInvalidMethod is returned when a raw request uses an unsupported
	// HTTP method.
	ErrRawInvalidMethod = errors.New("invalid raw request method")
	// ErrRawRedirectRefused is returned when a response redirects somewhere
	// other than the same scheme, host, and port.
	ErrRawRedirectRefused = errors.New("raw request redirect refused")
)

// RawRequest describes a request to an API endpoint that the typed client
// methods do not cover.
//
// Path must be an absolute path such as "/v3/apps". It may not carry a
// scheme, host, user information, query string, fragment, backslash,
// control character, or a ".." segment, whether written plainly or
// percent-encoded. An empty Method means GET.
type RawRequest struct {
	Method string
	Path   string
	Query  url.Values
	Body   []byte
}

// RawResponse is the result of a raw request. Any HTTP status is returned
// to the caller rather than turned into an error, including a 5xx or 429
// that is still failing after the configured retries. Truncated is true when the
// body was longer than RawMaxResponseBytes and was cut at that size.
type RawResponse struct {
	Status    int
	Header    http.Header
	Body      []byte
	Truncated bool
}
