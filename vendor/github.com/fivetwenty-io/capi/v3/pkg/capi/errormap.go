// Package capi contains the exported types and errors for the CF v3 API client.
//
// This file defines sentinel errors for the most common CF v3 HTTP failure
// modes and the MapHTTPError helper, which converts an HTTP status code and
// response body into a single error value that simultaneously:
//
//   - unwraps to a sentinel (so callers use errors.Is(err, capi.ErrNotFound))
//   - unwraps to a *ResponseError when the body was a well-formed CF v3
//     error envelope (so callers can still use errors.As to read
//     APIError.Code / Title / Detail)
//
// MapHTTPError is the single point in capi/v3 where HTTP-response error
// values are constructed. The internal HTTP client (internal/http.Client)
// delegates to it from parseError; any other code that constructs errors
// from an HTTP response body should do the same.
package capi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Sentinel errors for common CF v3 API failure modes. Callers use errors.Is
// to detect these regardless of whether the underlying *ResponseError wraps a
// single APIError or a multi-error response.
//
// These are intentionally plain sentinel values (created via errors.New) so
// that they can be compared with errors.Is across wrapping boundaries.
var (
	// ErrNotFound is returned when the CF v3 API responds with HTTP 404.
	ErrNotFound = errors.New("capi: resource not found")

	// ErrUnauthorized is returned when the CF v3 API responds with HTTP 401.
	ErrUnauthorized = errors.New("capi: unauthorized")

	// ErrForbidden is returned when the CF v3 API responds with HTTP 403.
	ErrForbidden = errors.New("capi: forbidden")

	// ErrConflict is returned when the CF v3 API responds with HTTP 409.
	ErrConflict = errors.New("capi: conflict")

	// ErrUnprocessable is returned when the CF v3 API responds with HTTP 422.
	ErrUnprocessable = errors.New("capi: unprocessable entity")

	// ErrRateLimited is returned when the CF v3 API responds with HTTP 429.
	ErrRateLimited = errors.New("capi: rate limited")

	// ErrServerError is returned when the CF v3 API responds with any 5xx
	// status code.
	ErrServerError = errors.New("capi: server error")

	// ErrBadRequest is returned when the CF v3 API responds with any 4xx
	// status code that is not explicitly enumerated by the mapper (for
	// example 400 Bad Request, 405 Method Not Allowed, 418 I'm a Teapot,
	// 431 Request Header Fields Too Large). It carries "client error; do
	// not retry" semantics: the request was rejected by the server for a
	// reason specific to the request itself, and automatically retrying the
	// same request is not expected to succeed. Callers that need finer
	// grain can still inspect the embedded *ResponseError via errors.As or
	// the raw status via the error message.
	ErrBadRequest = errors.New("capi: bad request")

	// ErrUnexpectedStatus is the sentinel for any error status not matched by
	// a more specific sentinel above. The exact status is wrapped into the
	// message; callers can match the class with errors.Is.
	ErrUnexpectedStatus = errors.New("capi: unexpected HTTP status")
)

// MapHTTPError constructs an error value from the HTTP status code and
// response body returned by the CF v3 API.
//
// The returned error always unwraps (via errors.Is) to one of the sentinel
// errors above. If the body is a well-formed CF v3 error envelope (a JSON
// object containing a non-empty "errors" array matching *ResponseError), the
// returned error additionally unwraps (via errors.As) to the parsed
// *ResponseError so that callers can inspect the underlying APIError entries.
//
// If the body cannot be parsed as an error envelope (malformed JSON, empty
// body, or a well-formed envelope with no entries), the returned error still
// wraps the correct sentinel and the raw body is included in the error
// message for debugging.
//
// MapHTTPError returns nil for any status code less than 400.
//
// Inputs:
//   - status: the HTTP response status code. Any value < 400 yields nil.
//     Known 4xx/5xx codes map to the matching sentinel. Any other 4xx code
//     (400/405/418/431/...) maps to ErrBadRequest so callers have a stable
//     sentinel to test for "client error, do not retry" via errors.Is.
//     Any code >= 500 maps to ErrServerError.
//   - body: the raw response body bytes. May be nil or empty; MapHTTPError
//     does not panic on either.
func MapHTTPError(status int, body []byte) error {
	if status < http.StatusBadRequest {
		return nil
	}

	sentinel := mapStatusToSentinel(status)

	// Attempt to parse the body as a CF v3 error envelope. If successful,
	// join the sentinel and the envelope so that both errors.Is and
	// errors.As work on the returned value.
	if len(body) > 0 {
		envelope := &ResponseError{}

		jsonErr := json.Unmarshal(body, envelope)
		if jsonErr == nil && len(envelope.Errors) > 0 {
			return errors.Join(sentinel, envelope)
		}
	}

	// Body is missing, malformed, or does not match the CF error envelope.
	// Still wrap the sentinel so callers using errors.Is continue to work,
	// and include the raw body for human debuggability.
	if len(body) == 0 {
		return fmt.Errorf("%w (status %d)", sentinel, status)
	}

	return fmt.Errorf("%w (status %d): %s", sentinel, status, string(body))
}

// mapStatusToSentinel returns the sentinel error that matches the given HTTP
// status code. Known 4xx codes (401/403/404/409/422/429) map directly. Any
// 5xx code maps to ErrServerError. Any other 4xx code (including client
// errors this library does not yet enumerate, such as 400 Bad Request, 405
// Method Not Allowed, 418 I'm a Teapot, or 431 Request Header Fields Too
// Large) maps to ErrBadRequest so callers have a stable sentinel to test
// for "client error, do not retry" semantics via errors.Is. The raw status
// is still surfaced via the wrapping fmt.Errorf message in MapHTTPError.
// Any other status (unknown, below 400 or above 599) yields a generic
// "capi: HTTP <code>" error so the caller still observes a non-nil error
// without a misleading sentinel classification.
//
// mapStatusToSentinel is intentionally unexported: callers use MapHTTPError
// which composes this helper with body parsing.
func mapStatusToSentinel(status int) error {
	switch status {
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusConflict:
		return ErrConflict
	case http.StatusUnprocessableEntity:
		return ErrUnprocessable
	case http.StatusTooManyRequests:
		return ErrRateLimited
	}

	if status >= 500 && status <= 599 {
		return ErrServerError
	}

	// Unknown 4xx — classify as a client error so callers can use
	// errors.Is(err, capi.ErrBadRequest) to detect "do not retry" failures
	// without needing to enumerate every non-standard status code the
	// server might return.
	if status >= 400 && status <= 499 {
		return ErrBadRequest
	}

	// Any other non-success status not otherwise matched. Return a distinct
	// error value so callers still observe a non-nil error and can inspect
	// the status via the message.
	return fmt.Errorf("%w %d", ErrUnexpectedStatus, status)
}
