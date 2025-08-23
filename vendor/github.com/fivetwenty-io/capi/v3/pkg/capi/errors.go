package capi

import (
	"encoding/json"
	"fmt"
)

// APIError represents an error from the CF API
type APIError struct {
	Code   int    `json:"code"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// Error implements the error interface
func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s (code: %d)", e.Title, e.Detail, e.Code)
}

// ErrorResponse represents the error response from the API
type ErrorResponse struct {
	Errors []APIError `json:"errors"`
}

// Error implements the error interface for ErrorResponse
func (e *ErrorResponse) Error() string {
	if len(e.Errors) == 0 {
		return "unknown error"
	}
	if len(e.Errors) == 1 {
		return e.Errors[0].Error()
	}
	return fmt.Sprintf("multiple errors: %v", e.Errors)
}

// FirstError returns the first error or nil
func (e *ErrorResponse) FirstError() *APIError {
	if len(e.Errors) > 0 {
		return &e.Errors[0]
	}
	return nil
}

// Common error codes
const (
	ErrorCodeNotFound               = 10010
	ErrorCodeNotAuthenticated       = 10002
	ErrorCodeNotAuthorized          = 10003
	ErrorCodeUnprocessableEntity    = 10008
	ErrorCodeServiceUnavailable     = 10001
	ErrorCodeBadRequest             = 10005
	ErrorCodeUniquenessError        = 10016
	ErrorCodeResourceNotFound       = 10010
	ErrorCodeInvalidRelation        = 10020
	ErrorCodeTooManyRequests        = 10013
	ErrorCodeMaintenanceInfo        = 10012
	ErrorCodeServiceInstanceQuota   = 10003
	ErrorCodeAsyncServiceInProgress = 10001
)

// Common error types
var (
	ErrNotFound           = &APIError{Code: ErrorCodeNotFound, Title: "CF-ResourceNotFound"}
	ErrUnauthorized       = &APIError{Code: ErrorCodeNotAuthenticated, Title: "CF-NotAuthenticated"}
	ErrForbidden          = &APIError{Code: ErrorCodeNotAuthorized, Title: "CF-NotAuthorized"}
	ErrUnprocessable      = &APIError{Code: ErrorCodeUnprocessableEntity, Title: "CF-UnprocessableEntity"}
	ErrServiceUnavailable = &APIError{Code: ErrorCodeServiceUnavailable, Title: "CF-ServiceUnavailable"}
	ErrBadRequest         = &APIError{Code: ErrorCodeBadRequest, Title: "CF-BadRequest"}
	ErrTooManyRequests    = &APIError{Code: ErrorCodeTooManyRequests, Title: "CF-TooManyRequests"}
)

// IsNotFound checks if the error is a not found error
func IsNotFound(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.Code == ErrorCodeNotFound
	}
	if errResp, ok := err.(*ErrorResponse); ok {
		if first := errResp.FirstError(); first != nil {
			return first.Code == ErrorCodeNotFound
		}
	}
	return false
}

// IsUnauthorized checks if the error is an unauthorized error
func IsUnauthorized(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.Code == ErrorCodeNotAuthenticated
	}
	if errResp, ok := err.(*ErrorResponse); ok {
		if first := errResp.FirstError(); first != nil {
			return first.Code == ErrorCodeNotAuthenticated
		}
	}
	return false
}

// IsForbidden checks if the error is a forbidden error
func IsForbidden(err error) bool {
	if apiErr, ok := err.(*APIError); ok {
		return apiErr.Code == ErrorCodeNotAuthorized
	}
	if errResp, ok := err.(*ErrorResponse); ok {
		if first := errResp.FirstError(); first != nil {
			return first.Code == ErrorCodeNotAuthorized
		}
	}
	return false
}

// ParseErrorResponse parses an error response from JSON
func ParseErrorResponse(data []byte) (*ErrorResponse, error) {
	var errResp ErrorResponse
	if err := json.Unmarshal(data, &errResp); err != nil {
		return nil, err
	}
	return &errResp, nil
}
