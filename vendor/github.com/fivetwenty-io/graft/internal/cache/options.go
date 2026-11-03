package cache

import (
	"time"
)

// Options holds configuration for creating a cache.
type Options struct {
	// MaxSize is the maximum number of entries the cache can hold.
	// When this limit is reached, entries are evicted according to the
	// eviction policy.
	MaxSize int

	// TTL is the default time-to-live for cache entries.
	// A zero value means entries never expire.
	TTL time.Duration

	// Shards is the number of shards for the sharded cache.
	// More shards reduce lock contention but increase memory overhead.
	// Should be a power of 2 for optimal performance.
	Shards int

	// OnEvict is called when an entry is evicted from the cache.
	// This can be used for cleanup or logging.
	OnEvict func(key string, value interface{})

	// CleanupInterval is how often the background cleanup goroutine runs.
	// A zero value disables background cleanup.
	CleanupInterval time.Duration

	// MaxSizeBytes is the maximum total size in bytes (optional).
	// A zero value means no byte-based limit.
	MaxSizeBytes int64
}

// Option is a functional option for configuring a cache.
type Option func(*Options)

// DefaultOptions returns the default cache options.
// Default configuration:
//   - MaxSize: 10000 entries
//   - TTL: 0 (no expiration)
//   - Shards: 16
//   - CleanupInterval: 1 minute
func DefaultOptions() Options {
	return Options{
		MaxSize:         10000,
		TTL:             0,
		Shards:          16,
		OnEvict:         nil,
		CleanupInterval: time.Minute,
		MaxSizeBytes:    0,
	}
}

// WithMaxSize sets the maximum number of entries the cache can hold.
func WithMaxSize(size int) Option {
	return func(o *Options) {
		if size > 0 {
			o.MaxSize = size
		}
	}
}

// WithTTL sets the default time-to-live for cache entries.
func WithTTL(ttl time.Duration) Option {
	return func(o *Options) {
		o.TTL = ttl
	}
}

// WithShards sets the number of shards for the sharded cache.
// The value will be rounded up to the nearest power of 2.
func WithShards(n int) Option {
	return func(o *Options) {
		if n > 0 {
			o.Shards = n
		}
	}
}

// WithOnEvict sets a callback function that is called when an entry is evicted.
func WithOnEvict(fn func(key string, value interface{})) Option {
	return func(o *Options) {
		o.OnEvict = fn
	}
}

// WithCleanupInterval sets how often the background cleanup runs.
// Set to 0 to disable background cleanup.
func WithCleanupInterval(interval time.Duration) Option {
	return func(o *Options) {
		o.CleanupInterval = interval
	}
}

// WithMaxSizeBytes sets the maximum total size in bytes for the cache.
// This is an optional limit in addition to the entry count limit.
func WithMaxSizeBytes(bytes int64) Option {
	return func(o *Options) {
		if bytes > 0 {
			o.MaxSizeBytes = bytes
		}
	}
}

// WithNoExpiration is a convenience option that disables TTL.
func WithNoExpiration() Option {
	return func(o *Options) {
		o.TTL = 0
	}
}

// roundUpToPowerOf2 rounds up n to the nearest power of 2.
func roundUpToPowerOf2(n int) int {
	if n <= 1 {
		return 1
	}
	// Handle case where n is already a power of 2
	if n&(n-1) == 0 {
		return n
	}
	// Find the next power of 2
	result := 1
	for result < n {
		result <<= 1
	}
	return result
}
