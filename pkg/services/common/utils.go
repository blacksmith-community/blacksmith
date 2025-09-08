package common

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// WithRetry executes a function with retry logic.
func WithRetry(ctx context.Context, fn func() error, maxRetries int, baseDelay time.Duration) error {
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		err := fn()
		if err == nil {
			return nil
		} else {
			lastErr = err

			// Check if context is done
			if ctx.Err() != nil {
				return fmt.Errorf("context cancelled: %w", ctx.Err())
			}

			// Check if this is the last attempt
			if attempt == maxRetries {
				break
			}

			// Check if error is retryable
			var retryErr *RetryableError
			if errors.As(err, &retryErr) {
				if !retryErr.Retryable {
					return err
				}

				// Use custom retry delay if specified
				delay := retryErr.RetryAfter
				if delay == 0 {
					const backoffBase = 2
					delay = time.Duration(math.Pow(backoffBase, float64(attempt))) * baseDelay
				}

				select {
				case <-time.After(delay):
					// Continue to next retry
				case <-ctx.Done():
					return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
				}
			} else {
				// For non-retryable errors, apply exponential backoff
				const backoffBase = 2
				delay := time.Duration(math.Pow(backoffBase, float64(attempt))) * baseDelay
				select {
				case <-time.After(delay):
					// Continue to next retry
				case <-ctx.Done():
					return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
				}
			}
		}
	}

	return fmt.Errorf("max retries exceeded: %w", lastErr)
}

// IsRetryableError checks if an error is retryable.
func IsRetryableError(err error) bool {
	var retryErr *RetryableError
	if errors.As(err, &retryErr) {
		return retryErr.Retryable
	}

	var serviceErr *ServiceError
	if errors.As(err, &serviceErr) {
		return serviceErr.Retryable
	}

	return false
}

// MaskCredentials replaces sensitive credential values with masked strings.
func MaskCredentials(creds Credentials) Credentials {
	masked := make(Credentials)

	sensitiveKeys := map[string]bool{
		"password":    true,
		"secret":      true,
		"token":       true,
		"key":         true,
		"private_key": true,
		"certificate": false, // Keep certificates visible for debugging
	}

	for key, value := range creds {
		if shouldMask, exists := sensitiveKeys[key]; exists && shouldMask {
			if str, ok := value.(string); ok && len(str) > 0 {
				masked[key] = "***MASKED***"
			} else {
				masked[key] = value
			}
		} else {
			masked[key] = value
		}
	}

	return masked
}

// GetConnectionTimeout returns the connection timeout, with a default if not specified.
func GetConnectionTimeout(opts ConnectionOptions) time.Duration {
	if opts.Timeout > 0 {
		return opts.Timeout
	}

	return DefaultTimeout // Default timeout
}

// GetMaxRetries returns the max retries, with a default if not specified.
func GetMaxRetries(opts ConnectionOptions) int {
	if opts.MaxRetries > 0 {
		return opts.MaxRetries
	}

	return DefaultMaxRetries // Default retries
}
