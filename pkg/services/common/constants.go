package common

import "time"

// Default Configuration Constants.
const (
	DefaultTimeout    = 30 * time.Second
	DefaultMaxRetries = 3
	DefaultRateLimit  = 100 // requests per minute
)
