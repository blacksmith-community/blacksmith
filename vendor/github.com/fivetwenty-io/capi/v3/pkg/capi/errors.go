package capi

import (
	"encoding/json"
	"errors"
	"fmt"
)

// APIError represents an error from the CF API.
type APIError struct {
	Code   int    `json:"code"   yaml:"code"`
	Title  string `json:"title"  yaml:"title"`
	Detail string `json:"detail" yaml:"detail"`
}

// Error implements the error interface.
func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s (code: %d)", e.Title, e.Detail, e.Code)
}

// ResponseError represents the error response from the API.
type ResponseError struct {
	Errors []APIError `json:"errors"`
}

// Error implements the error interface for ResponseError.
func (e *ResponseError) Error() string {
	if len(e.Errors) == 0 {
		return "unknown error"
	}

	if len(e.Errors) == 1 {
		return e.Errors[0].Error()
	}

	return fmt.Sprintf("multiple errors: %v", e.Errors)
}

// FirstError returns the first error or nil.
func (e *ResponseError) FirstError() *APIError {
	if len(e.Errors) > 0 {
		return &e.Errors[0]
	}

	return nil
}

// Common error codes from the CF v3 API (cloud_controller_ng errors/v2.yml).
const (
	// ErrorCodeNotFound is CF code 10000 (NotFound / "Unknown request").
	ErrorCodeNotFound = 10000
	// ErrorCodeNotAuthenticated is CF code 10002 (NotAuthenticated).
	ErrorCodeNotAuthenticated = 10002
	// ErrorCodeNotAuthorized is CF code 10003 (NotAuthorized).
	ErrorCodeNotAuthorized = 10003
	// ErrorCodeBadRequest is CF code 10005 (BadQueryParameter). The name is
	// intentionally broad; the CF name at this code is BadQueryParameter.
	ErrorCodeBadRequest = 10005
	// ErrorCodeUnprocessableEntity is CF code 10008 (UnprocessableEntity).
	ErrorCodeUnprocessableEntity = 10008
	// ErrorCodeResourceNotFound is CF code 10010 (ResourceNotFound). Distinct
	// from ErrorCodeNotFound (10000) which maps to the generic NotFound code.
	ErrorCodeResourceNotFound = 10010
	// ErrorCodeServiceUnavailable is CF code 10015 (ServiceUnavailable).
	ErrorCodeServiceUnavailable = 10015
	// ErrorCodeTooManyRequests is CF code 10013 (TooManyRequests).
	ErrorCodeTooManyRequests = 10013
	// ErrorCodeInvalidRelation is CF code 1002 (InvalidRelation).
	ErrorCodeInvalidRelation = 1002
	// ErrorCodeMaintenanceInfo is CF code 390006 (MaintenanceInfoNotSupported).
	ErrorCodeMaintenanceInfo = 390006
	// ErrorCodeServiceInstanceQuota is CF code 60005 (ServiceInstanceQuotaExceeded).
	ErrorCodeServiceInstanceQuota = 60005
	// ErrorCodeAsyncServiceInProgress is CF code 60016
	// (AsyncServiceInstanceOperationInProgress).
	ErrorCodeAsyncServiceInProgress = 60016
)

// Common CF-API error templates keyed by CF error code. These are APIError
// values (not sentinel errors) that callers can use as prototypes when
// constructing a synthetic CF error for tests or when injecting a known CF
// error code into a *ResponseError. For detecting a failure mode in a
// returned error, use the sentinel errors in errormap.go with errors.Is
// (e.g. errors.Is(err, capi.ErrNotFound)).
var (
	ErrCFServiceUnavailable = &APIError{Code: ErrorCodeServiceUnavailable, Title: "CF-ServiceUnavailable"}
	ErrCFBadRequest         = &APIError{Code: ErrorCodeBadRequest, Title: "CF-BadRequest"}
	ErrCFTooManyRequests    = &APIError{Code: ErrorCodeTooManyRequests, Title: "CF-TooManyRequests"}
)

// Common static errors that can be wrapped with context.
var (
	ErrAPIAlreadyExists            = errors.New("API already exists")
	ErrAPINotFound                 = errors.New("API not found")
	ErrCannotDeleteOnlyAPI         = errors.New("cannot delete the only configured API")
	ErrNoHostInURL                 = errors.New("no host specified in URL")
	ErrInvalidFilePath             = errors.New("invalid file path")
	ErrPathTraversalAttempt        = errors.New("potential path traversal attempt")
	ErrPathTraversalNotAllowed     = errors.New("path traversal not allowed")
	ErrSpaceNotFound               = errors.New("space not found")
	ErrApplicationNotFound         = errors.New("application not found")
	ErrNoProcessesFound            = errors.New("no processes found")
	ErrProcessTypeNotFound         = errors.New("process type not found")
	ErrEnvironmentVariableNotFound = errors.New("environment variable not found")
	ErrInstanceIndexOutOfRange     = errors.New("instance index out of range")
	ErrOrganizationNotFound        = errors.New("organization not found")
	ErrBuildpackNotFound           = errors.New("buildpack not found")
	ErrBuildpackNameRequired       = errors.New("buildpack name is required")
	ErrNoAPIsConfigured            = errors.New("no APIs configured")
	ErrCurrentAPINotFound          = errors.New("current API not found in configuration")
	ErrAPINameOrEndpointRequired   = errors.New("API name or endpoint is required")
	ErrNoAPIEndpointConfigured     = errors.New("no API endpoint configured")
	ErrCouldNotDetermineAPIDomain  = errors.New("could not determine API domain for configuration")
	ErrStaticTokenCannotRefresh    = errors.New("static token cannot be refreshed")
	ErrCircuitBreakerOpen          = errors.New("circuit breaker is open")
	ErrNoMoreItems                 = errors.New("no more items")
	ErrConfigRequired              = errors.New("config is required")
	ErrAPIEndpointRequired         = errors.New("API endpoint is required")
	ErrSkipTLSOnlyInDev            = errors.New("skipTLS is only allowed in development environments")
	ErrInvalidCACertPEM            = errors.New("CACertPEM contains no valid PEM certificates")
	ErrRootInfoRequestFailed       = errors.New("root info request failed")
	ErrNoUAAOrLoginURL             = errors.New("no UAA or login URL found in API root response")
	ErrInvalidHealthCheckType      = errors.New("invalid health check type")
	ErrNotImplemented              = errors.New("not implemented")
	ErrNotAuthenticated            = errors.New("not authenticated")
	ErrUnknownConfigKey            = errors.New("unknown configuration key")
	ErrTokenFieldsCannotUnset      = errors.New("token fields cannot be unset via config command")
	ErrDomainNotFound              = errors.New("domain not found")
	ErrDomainNameRequired          = errors.New("domain name is required")
	ErrInvalidClientType           = errors.New("invalid client type")
)

// IsNotFound reports whether err represents a CF "resource not found"
// failure. It returns true when the error chain contains the ErrNotFound
// sentinel (the canonical check for errors produced by MapHTTPError) or
// when the chain contains an *APIError / *ResponseError whose CF error code
// equals one of the CF "not found" codes — ErrorCodeNotFound (10000,
// generic) or ErrorCodeResourceNotFound (10010) — the legacy check for
// errors constructed directly from an APIError.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, ErrNotFound) {
		return true
	}

	apiErr := &APIError{}
	if errors.As(err, &apiErr) && isNotFoundCode(apiErr.Code) {
		return true
	}

	errResp := &ResponseError{}
	if errors.As(err, &errResp) {
		first := errResp.FirstError()
		if first != nil && isNotFoundCode(first.Code) {
			return true
		}
	}

	return false
}

// isNotFoundCode reports whether a CF error code denotes a "not found"
// failure: the generic NotFound (10000) or ResourceNotFound (10010).
func isNotFoundCode(code int) bool {
	return code == ErrorCodeNotFound || code == ErrorCodeResourceNotFound
}

// IsUnauthorized reports whether err represents a CF "unauthorized"
// (HTTP 401 / not authenticated) failure. It returns true when the error
// chain contains the ErrUnauthorized sentinel, or when it contains an
// *APIError / *ResponseError whose CF error code equals
// ErrorCodeNotAuthenticated.
func IsUnauthorized(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, ErrUnauthorized) {
		return true
	}

	apiErr := &APIError{}
	if errors.As(err, &apiErr) && apiErr.Code == ErrorCodeNotAuthenticated {
		return true
	}

	errResp := &ResponseError{}
	if errors.As(err, &errResp) {
		first := errResp.FirstError()
		if first != nil && first.Code == ErrorCodeNotAuthenticated {
			return true
		}
	}

	return false
}

// IsForbidden reports whether err represents a CF "forbidden"
// (HTTP 403 / not authorized) failure. It returns true when the error
// chain contains the ErrForbidden sentinel, or when it contains an
// *APIError / *ResponseError whose CF error code equals
// ErrorCodeNotAuthorized.
func IsForbidden(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, ErrForbidden) {
		return true
	}

	apiErr := &APIError{}
	if errors.As(err, &apiErr) && apiErr.Code == ErrorCodeNotAuthorized {
		return true
	}

	errResp := &ResponseError{}
	if errors.As(err, &errResp) {
		first := errResp.FirstError()
		if first != nil && first.Code == ErrorCodeNotAuthorized {
			return true
		}
	}

	return false
}

// ParseResponseError parses an error response from JSON.
func ParseResponseError(data []byte) (*ResponseError, error) {
	var errResp ResponseError

	err := json.Unmarshal(data, &errResp)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response error: %w", err)
	}

	return &errResp, nil
}

// Test error variables for test files to comply with err113.
var (
	ErrAppNotFound = errors.New("app not found")
	ErrSomeError   = errors.New("some error")
)
