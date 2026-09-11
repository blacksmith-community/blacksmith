package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	http_internal "github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// Async job reference errors. Wrapped (not formatted inline) so callers can
// match them with errors.Is while the opLabel prefix and original wording are
// preserved.
var (
	ErrMalformedLocationHeader = errors.New("malformed Location header")
	ErrNoLocationHeader        = errors.New("no Location header on async delete response")
)

// CF v3 async responses carry the job reference in the Location header
// (202 Accepted + Location: .../v3/jobs/{jobGuid}, empty body — per the
// CF v3 OpenAPI spec). The helpers below are the single implementation
// of that contract; pick the one matching the endpoint's semantics:
//
//   - jobFromAsyncResponse: endpoints documented to return a Job body
//     historically (creates/updates/admin actions). Prefers a parseable
//     body (full job state), falls back to the Location header.
//   - jobFromLocationHeader: async deletes — Location is REQUIRED;
//     its absence is a contract violation.
//   - jobFromOptionalLocation: actions that older CF versions completed
//     synchronously (app start/stop/restart/restage, process scale) —
//     no Location means sync-complete, callers get (nil, nil).

// jobRefFromLocation parses the trailing job GUID out of a non-empty
// Location header value and returns a Job carrying just that GUID;
// callers poll via Jobs().Get for full state.
func jobRefFromLocation(location, opLabel string) (*capi.Job, error) {
	jobGUID := location
	if idx := strings.LastIndex(location, "/"); idx >= 0 {
		jobGUID = location[idx+1:]
	}

	if jobGUID == "" {
		return nil, fmt.Errorf("%s: %w %q", opLabel, ErrMalformedLocationHeader, location)
	}

	return &capi.Job{Resource: capi.Resource{GUID: jobGUID}}, nil
}

// jobFromLocationHeader extracts the async job reference for endpoints
// where CF always responds 202 + Location (async deletes). A missing
// header is an error.
func jobFromLocationHeader(resp *http_internal.Response, opLabel string) (*capi.Job, error) {
	location := resp.Headers.Get("Location")
	if location == "" {
		return nil, fmt.Errorf("%s: %w", opLabel, ErrNoLocationHeader)
	}

	return jobRefFromLocation(location, opLabel)
}

// jobFromOptionalLocation extracts the async job reference for endpoints
// that CF transitioned from synchronous to asynchronous over time. A
// missing Location header means the operation completed synchronously;
// callers treat (nil, nil) as COMPLETE.
func jobFromOptionalLocation(resp *http_internal.Response, opLabel string) (*capi.Job, error) {
	location := resp.Headers.Get("Location")
	if location == "" {
		return nil, nil
	}

	return jobRefFromLocation(location, opLabel)
}

// jobFromAsyncResponse extracts the async job reference from a CF v3
// 202 Accepted response on endpoints that historically returned a Job
// body. Real CF sends an EMPTY body with the job link in the Location
// header; some proxies, emulators and older servers include the Job
// resource as the response body.
//
// Resolution order:
//  1. Non-empty body that parses as a Job → return it (full job state:
//     GUID, operation, state).
//  2. Location header → Job with the GUID from its trailing segment.
//  3. Neither → the original body parse error, so contract violations
//     stay visible.
func jobFromAsyncResponse(resp *http_internal.Response, opLabel string) (*capi.Job, error) {
	var parseErr error

	if len(strings.TrimSpace(string(resp.Body))) > 0 {
		var job capi.Job

		parseErr = json.Unmarshal(resp.Body, &job)
		if parseErr == nil {
			return &job, nil
		}
	}

	if resp.Headers.Get("Location") != "" {
		return jobFromLocationHeader(resp, opLabel)
	}

	if parseErr == nil {
		// Empty body and no Location header — keep the historical error
		// shape so existing callers' error matching continues to work.
		var job capi.Job

		parseErr = json.Unmarshal(resp.Body, &job)
	}

	return nil, fmt.Errorf("parsing job response: %w", parseErr)
}
