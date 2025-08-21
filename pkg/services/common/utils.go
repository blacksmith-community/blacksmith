package common

import (
	"context"
	"fmt"
	"math"
	"time"
)

// WithRetry executes a function with retry logic
func WithRetry(ctx context.Context, fn func() error, maxRetries int, baseDelay time.Duration) error {
	var lastErr error

	for i := 0; i <= maxRetries; i++ {
		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err

			// Check if context is done
			if ctx.Err() != nil {
				return ctx.Err()
			}

			// Check if this is the last attempt
			if i == maxRetries {
				break
			}

			// Check if error is retryable
			if retryErr, ok := err.(*RetryableError); ok {
				if !retryErr.Retryable {
					return err
				}

				// Use custom retry delay if specified
				delay := retryErr.RetryAfter
				if delay == 0 {
					delay = time.Duration(math.Pow(2, float64(i))) * baseDelay
				}

				select {
				case <-time.After(delay):
					// Continue to next retry
				case <-ctx.Done():
					return ctx.Err()
				}
			} else {
				// For non-retryable errors, apply exponential backoff
				delay := time.Duration(math.Pow(2, float64(i))) * baseDelay
				select {
				case <-time.After(delay):
					// Continue to next retry
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}

	return fmt.Errorf("max retries exceeded: %w", lastErr)
}

// IsRetryableError checks if an error is retryable
func IsRetryableError(err error) bool {
	if retryErr, ok := err.(*RetryableError); ok {
		return retryErr.Retryable
	}
	if serviceErr, ok := err.(*ServiceError); ok {
		return serviceErr.Retryable
	}
	return false
}

// MaskCredentials replaces sensitive credential values with masked strings
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

	for k, v := range creds {
		if shouldMask, exists := sensitiveKeys[k]; exists && shouldMask {
			if str, ok := v.(string); ok && len(str) > 0 {
				masked[k] = "***MASKED***"
			} else {
				masked[k] = v
			}
		} else {
			masked[k] = v
		}
	}

	return masked
}

// GetConnectionTimeout returns the connection timeout, with a default if not specified
func GetConnectionTimeout(opts ConnectionOptions) time.Duration {
	if opts.Timeout > 0 {
		return opts.Timeout
	}
	return 30 * time.Second // Default timeout
}

// GetMaxRetries returns the max retries, with a default if not specified
func GetMaxRetries(opts ConnectionOptions) int {
	if opts.MaxRetries > 0 {
		return opts.MaxRetries
	}
	return 3 // Default retries
}
