// Package http provides the internal HTTP client used by capi/v3.
//
// This file defines authRetryTransport, an http.RoundTripper that sits on
// top of the retryablehttp-managed base transport and transparently handles
// HTTP 401 Unauthorized responses by refreshing the auth token once and
// replaying the request. It is installed by NewClient so that every request
// executed via Client.Do (and the retryablehttp retry machinery) benefits
// from the retry behaviour without any per-request bookkeeping.
//
// Design notes:
//
//   - The retry is attempted at most once per original request. The replayed
//     request goes directly through the base transport (not back through
//     this transport), so a second 401 on the retry is returned to the
//     caller unchanged.
//   - If the request body is a streaming body (Body != nil and GetBody ==
//     nil, i.e. the body cannot be rewound), the original 401 response is
//     returned as-is because a retry would require re-reading a consumed
//     reader.
//   - Any failure during the refresh or token fetch causes the original 401
//     response to be returned unchanged so the caller observes the same
//     semantics they would see without a token manager.
//   - A nil token manager disables the retry entirely; this matches the
//     behaviour of the legacy inline retry in handleResponseError.
package http

import (
	"net/http"
	"strings"
	"sync"

	"github.com/fivetwenty-io/capi/v3/internal/auth"
)

// authRetryTransport is an http.RoundTripper that transparently refreshes
// the auth token and replays a request once when the base transport returns
// HTTP 401 Unauthorized. It is installed by NewClient on the retryablehttp
// client's underlying *http.Client so that all requests issued through the
// retryablehttp machinery pick up the retry automatically.
//
// Concurrency: refreshMu serializes the token-refresh section of RoundTrip
// so that N goroutines hitting a simultaneous 401 result in exactly one
// upstream RefreshToken call. Callers that observe a refreshed token has
// already been published by a sibling goroutine (i.e. the token currently
// cached by the token manager differs from the one they sent with their
// failing request) skip the refresh and replay immediately with the cached
// token. This mirrors the single-flight semantics of golang.org/x/oauth2's
// reuseTokenSource without adding a new module dependency.
type authRetryTransport struct {
	base         http.RoundTripper
	tokenManager auth.TokenManager

	refreshMu sync.Mutex
}

// newAuthRetryTransport returns an authRetryTransport wrapping the provided
// base RoundTripper and TokenManager.
//
// If base is nil, http.DefaultTransport is used so that the returned
// transport is always usable. A nil tokenManager is permitted; in that case
// RoundTrip degrades to a pure pass-through (no refresh, no retry).
func newAuthRetryTransport(base http.RoundTripper, tokenManager auth.TokenManager) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}

	return &authRetryTransport{base: base, tokenManager: tokenManager}
}

// RoundTrip executes the request against the base transport and, on an HTTP
// 401 Unauthorized response, attempts exactly one token refresh + retry
// cycle before returning. See the package doc on authRetryTransport for the
// full set of conditions under which the retry is skipped.
//
// On any condition that prevents retry (nil token manager, non-401 status,
// refresh failure, token fetch failure, or a non-rewindable streaming body)
// the original response is returned unchanged so the caller observes the
// same semantics they would see without this wrapper.
func (t *authRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		//nolint:wrapcheck // transport errors must pass through unwrapped so net/http wraps them in *url.Error itself
		return resp, err
	}

	// Fast path: only 401 with a token manager is eligible for retry.
	if resp.StatusCode != http.StatusUnauthorized || t.tokenManager == nil {
		return resp, nil
	}

	// If the request has a non-rewindable body, we cannot retry. Return
	// the original 401 untouched (body still open for the caller to read).
	if req.Body != nil && req.GetBody == nil {
		return resp, nil
	}

	ctx := req.Context()

	// Serialize the refresh section so that concurrent 401s from multiple
	// goroutines issue at most one upstream RefreshToken call. Before
	// refreshing we compare the Authorization bearer value the caller used
	// (attached to req) against the token currently cached by the token
	// manager. If they differ, a sibling goroutine already refreshed the
	// token between the time this request was dispatched and the time it
	// came back 401 — so we retry with the cached token instead of
	// triggering another upstream refresh.
	t.refreshMu.Lock()

	callerToken := bearerFromHeader(req.Header.Get("Authorization"))

	cachedToken, cachedErr := t.tokenManager.GetToken(ctx)
	if cachedErr == nil && cachedToken != "" && cachedToken != callerToken {
		// A sibling goroutine already refreshed. Use the cached token
		// directly without another RefreshToken round trip.
		t.refreshMu.Unlock()

		return t.replay(req, cachedToken, resp)
	}

	refreshErr := t.tokenManager.RefreshToken(ctx)
	if refreshErr != nil {
		// Refresh failed — return the original 401 unchanged.
		t.refreshMu.Unlock()

		return resp, nil //nolint:nilerr // intentional: surface the original 401, not the refresh error
	}

	token, tokenErr := t.tokenManager.GetToken(ctx)
	t.refreshMu.Unlock()

	if tokenErr != nil {
		return resp, nil //nolint:nilerr // intentional: surface the original 401, not the token-fetch error
	}

	return t.replay(req, token, resp)
}

// replay builds a cloned request with the refreshed Authorization header and
// issues it directly against the base transport (bypassing this wrapper so a
// second 401 is returned unchanged). The caller is responsible for having
// obtained token from the token manager and must not hold refreshMu.
//
// replay closes the original 401 response body on success so the underlying
// connection is not leaked. On any failure building the retry request (a
// non-rewindable body that slipped past the caller's check, or GetBody
// returning an error) replay returns the original 401 unchanged.
func (t *authRetryTransport) replay(req *http.Request, token string, original *http.Response) (*http.Response, error) {
	ctx := req.Context()

	// Build a cloned request with the refreshed Authorization header. We
	// clone (rather than mutate) so the caller's original *http.Request is
	// left untouched, matching the standard RoundTripper contract.
	retryReq := req.Clone(ctx)
	retryReq.Header.Set("Authorization", "Bearer "+token)

	// Rewind the request body if present. GetBody is guaranteed non-nil
	// at this point by the streaming-body check in RoundTrip, so any error
	// here is a genuine IO/wrap failure and we fall back to returning the
	// original 401 response unchanged.
	if req.Body != nil {
		newBody, getBodyErr := req.GetBody()
		if getBodyErr != nil {
			return original, nil //nolint:nilerr // intentional: fall back to the original 401 on rewind failure
		}

		retryReq.Body = newBody
	}

	// We are committed to the retry. Drain and close the original 401
	// response body so we do not leak the underlying connection, then
	// issue the replay directly against the base transport (bypassing
	// this wrapper so a second 401 is returned unchanged).
	_ = original.Body.Close()

	//nolint:wrapcheck // transport errors must pass through unwrapped so net/http wraps them in *url.Error itself
	return t.base.RoundTrip(retryReq)
}

// bearerFromHeader extracts the bearer-token payload from an Authorization
// header value. Returns the raw header value unchanged if it does not carry
// a "Bearer " prefix, so comparisons still work for non-bearer auth schemes.
func bearerFromHeader(header string) string {
	const prefix = "Bearer "
	if strings.HasPrefix(header, prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}

	return header
}
