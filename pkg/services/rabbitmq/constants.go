package rabbitmq

import "time"

// TTL and Timeout Constants.
const (
	DefaultTTL          = 5 * time.Minute
	HealthCheckInterval = 5 * time.Second
	DefaultTimeout      = 5 * time.Second
	HTTPTimeout         = 30 * time.Second
)
