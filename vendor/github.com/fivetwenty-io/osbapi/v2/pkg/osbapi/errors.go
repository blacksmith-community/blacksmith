package osbapi

import (
	"errors"
	"fmt"
)

// OSBError represents an error response as defined by the OSB API specification.
type OSBError struct {
	ErrorCode        string `json:"error,omitempty"`
	Description      string `json:"description,omitempty"`
	InstanceUsable   *bool  `json:"instance_usable,omitempty"`
	UpdateRepeatable *bool  `json:"update_repeatable,omitempty"`
	StatusCode       int    `json:"-"`
}

// Error implements the error interface.
func (e *OSBError) Error() string {
	if e.ErrorCode != "" {
		return fmt.Sprintf("%s: %s", e.ErrorCode, e.Description)
	}
	return e.Description
}

// Sentinel errors for err113 compliance.
var (
	ErrAsyncRequired           = errors.New("broker requires async operation")
	ErrConcurrencyError        = errors.New("concurrent operation in progress")
	ErrMaintenanceInfoConflict = errors.New("maintenance info version mismatch")
	ErrRequiresApp             = errors.New("binding requires app_guid")
	ErrInstanceNotFound        = errors.New("service instance not found")
	ErrBindingNotFound         = errors.New("service binding not found")
	ErrInstanceAlreadyExists   = errors.New("service instance already exists")
	ErrBindingAlreadyExists    = errors.New("service binding already exists")
	ErrGoneError               = errors.New("resource has been deleted")
	ErrBadRequest              = errors.New("bad request")
	ErrUnauthorized            = errors.New("unauthorized")
	ErrPlanQuotaExceeded       = errors.New("plan quota exceeded")
	ErrInvalidParameters       = errors.New("invalid parameters")
)

// OSB API error code constants.
const (
	ErrorCodeAsyncRequired           = "AsyncRequired"
	ErrorCodeConcurrencyError        = "ConcurrencyError"
	ErrorCodeMaintenanceInfoConflict = "MaintenanceInfoConflict"
	ErrorCodeRequiresApp             = "RequiresApp"
)

// IsAsyncRequired checks if the error indicates that the broker requires async operations.
func IsAsyncRequired(err error) bool {
	if errors.Is(err, ErrAsyncRequired) {
		return true
	}

	var osbErr *OSBError
	if errors.As(err, &osbErr) {
		return osbErr.ErrorCode == ErrorCodeAsyncRequired
	}

	return false
}

// IsConcurrencyError checks if the error indicates a concurrent operation conflict.
func IsConcurrencyError(err error) bool {
	if errors.Is(err, ErrConcurrencyError) {
		return true
	}

	var osbErr *OSBError
	if errors.As(err, &osbErr) {
		return osbErr.ErrorCode == ErrorCodeConcurrencyError
	}

	return false
}

// IsNotFound checks if the error indicates a resource was not found.
func IsNotFound(err error) bool {
	if errors.Is(err, ErrInstanceNotFound) || errors.Is(err, ErrBindingNotFound) {
		return true
	}

	var osbErr *OSBError
	if errors.As(err, &osbErr) {
		return osbErr.StatusCode == 404 || osbErr.StatusCode == 410
	}

	return false
}

// IsGone checks if the error indicates a resource has been deleted.
func IsGone(err error) bool {
	if errors.Is(err, ErrGoneError) {
		return true
	}

	var osbErr *OSBError
	if errors.As(err, &osbErr) {
		return osbErr.StatusCode == 410
	}

	return false
}

// IsRequiresApp checks if the error indicates a binding requires an app_guid.
func IsRequiresApp(err error) bool {
	if errors.Is(err, ErrRequiresApp) {
		return true
	}

	var osbErr *OSBError
	if errors.As(err, &osbErr) {
		return osbErr.ErrorCode == ErrorCodeRequiresApp
	}

	return false
}

// IsMaintenanceInfoConflict checks if the error indicates a maintenance info version mismatch.
func IsMaintenanceInfoConflict(err error) bool {
	if errors.Is(err, ErrMaintenanceInfoConflict) {
		return true
	}

	var osbErr *OSBError
	if errors.As(err, &osbErr) {
		return osbErr.ErrorCode == ErrorCodeMaintenanceInfoConflict
	}

	return false
}

// IsConflict checks if the error indicates a resource already exists (409).
func IsConflict(err error) bool {
	if errors.Is(err, ErrInstanceAlreadyExists) || errors.Is(err, ErrBindingAlreadyExists) {
		return true
	}

	var osbErr *OSBError
	if errors.As(err, &osbErr) {
		return osbErr.StatusCode == 409
	}

	return false
}

// BoolPtr returns a pointer to the given bool value.
func BoolPtr(b bool) *bool {
	return &b
}
