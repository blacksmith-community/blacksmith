package errors

import "errors"

// Common static errors to replace dynamic error creation.
var (
	// Reconciler-related errors.
	ErrNoDataFoundAtPath     = errors.New("no data found at path")
	ErrVaultConnectionFailed = errors.New("vault connection failed")
	ErrSyncFailure           = errors.New("sync failure")
	ErrDeploymentNotFound    = errors.New("deployment not found")
	ErrEmptyServiceID        = errors.New("empty service_id")
	ErrEmptyPlanID           = errors.New("empty plan_id")
	ErrInvalidUUIDFormat     = errors.New("invalid UUID format")
	ErrMissingCredentials    = errors.New("missing credentials")
	ErrNotFound              = errors.New("not found")

	// VM Monitor-related errors.
	ErrUnexpectedDataType          = errors.New("unexpected data type in Put for db")
	ErrDirectorNonSuccessfulStatus = errors.New("director responded with non-successful status code")
)
