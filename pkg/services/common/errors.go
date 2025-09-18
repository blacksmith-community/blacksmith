package common

import (
	"errors"
	"fmt"
	"time"
)

var (
	// ErrConnectionFailed indicates a connection failure.
	ErrConnectionFailed = errors.New("connection failed")

	// ErrAuthenticationFailed indicates authentication failure.
	ErrAuthenticationFailed = errors.New("authentication failed")

	// ErrOperationTimeout indicates an operation timeout.
	ErrOperationTimeout = errors.New("operation timeout")

	// ErrInvalidCredentials indicates invalid credentials format.
	ErrInvalidCredentials = errors.New("invalid credentials")

	// ErrServiceUnavailable indicates the service is unavailable.
	ErrServiceUnavailable = errors.New("service unavailable")

	// ErrUnsupportedOperation indicates an unsupported operation.
	ErrUnsupportedOperation = errors.New("unsupported operation")
)

// RetryableError wraps an error with retry information.
type RetryableError struct {
	Err        error
	Retryable  bool
	RetryAfter time.Duration
}

// NewRetryableError creates a new retryable error.
func NewRetryableError(err error, retryable bool, retryAfter time.Duration) *RetryableError {
	return &RetryableError{
		Err:        err,
		Retryable:  retryable,
		RetryAfter: retryAfter,
	}
}

func (e *RetryableError) Error() string {
	return e.Err.Error()
}

func (e *RetryableError) Unwrap() error {
	return e.Err
}

// ServiceError represents a service-specific error.
type ServiceError struct {
	Code        string
	Message     string
	ServiceType string
	Retryable   bool
}

// NewServiceError creates a new service error.
func NewServiceError(serviceType, code, message string, retryable bool) *ServiceError {
	return &ServiceError{
		Code:        code,
		Message:     message,
		ServiceType: serviceType,
		Retryable:   retryable,
	}
}

func (e *ServiceError) Error() string {
	return fmt.Sprintf("[%s] %s: %s", e.ServiceType, e.Code, e.Message)
}
