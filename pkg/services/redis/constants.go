package redis

import "time"

// TTL and Timeout Constants.
const (
	DefaultTTL          = 5 * time.Minute
	PingTimeout         = 5 * time.Second
	LongPingTimeout     = 10 * time.Second
	DialTimeout         = 10 * time.Second
	ReadTimeout         = 30 * time.Second
	WriteTimeout        = 30 * time.Second
	HealthCheckInterval = 5 * time.Second
	ShortContextTimeout = 2 * time.Second
)

// Connection Pool Constants.
const (
	DefaultPoolSize   = 10
	DefaultMaxRetries = 3
)

// Parsing Constants.
const (
	InfoFieldParts = 2
)
