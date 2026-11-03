// Package cache provides a high-performance caching system with support for
// sharded concurrent access, LRU eviction, and TTL-based expiration.
//
// The package offers two main cache implementations:
//   - ShardedCache: A concurrent cache with configurable shards for reduced lock contention
//   - LRUCache: A cache with least-recently-used eviction policy
//
// Basic usage:
//
//	// Create a cache with defaults
//	c := cache.New()
//	c.Set("key", "value")
//	value, found := c.Get("key")
//
//	// Create a cache with custom options
//	c := cache.NewCache(
//	    cache.WithMaxSize(10000),
//	    cache.WithTTL(5 * time.Minute),
//	    cache.WithShards(32),
//	)
package cache

import (
	"time"
)

// Cache defines the interface for all cache implementations.
// Implementations must be safe for concurrent use.
type Cache interface {
	// Get retrieves a value from the cache.
	// Returns the value and true if found, nil and false otherwise.
	Get(key string) (interface{}, bool)

	// Set adds or updates a value in the cache.
	Set(key string, value interface{})

	// Delete removes a value from the cache.
	Delete(key string)

	// Clear removes all entries from the cache.
	Clear()

	// Size returns the number of entries in the cache.
	Size() int

	// Stats returns current cache statistics.
	Stats() Stats
}

// TTLCache extends Cache with TTL-specific operations.
type TTLCache interface {
	Cache

	// SetWithTTL adds or updates a value with a specific TTL.
	SetWithTTL(key string, value interface{}, ttl time.Duration)
}

// Stats holds runtime statistics for a cache.
type Stats struct {
	// Hits is the number of successful cache lookups.
	Hits uint64

	// Misses is the number of cache lookups that found no entry.
	Misses uint64

	// Size is the current number of entries in the cache.
	Size int

	// Evictions is the number of entries evicted due to capacity or TTL.
	Evictions uint64

	// Sets is the total number of Set operations performed.
	Sets uint64
}

// HitRate calculates the cache hit rate as a value between 0 and 1.
// Returns 0 if no operations have been performed.
func (s Stats) HitRate() float64 {
	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits) / float64(total)
}

// HitRatePercent calculates the cache hit rate as a percentage (0-100).
// Returns 0 if no operations have been performed.
func (s Stats) HitRatePercent() float64 {
	return s.HitRate() * 100
}

// Entry represents a single cache entry with metadata.
type Entry struct {
	// Key is the cache key for this entry.
	Key string

	// Value is the cached value.
	Value interface{}

	// CreatedAt is when the entry was first added to the cache.
	CreatedAt time.Time

	// AccessedAt is when the entry was last accessed.
	AccessedAt time.Time

	// ExpiresAt is when the entry expires (zero value means no expiration).
	ExpiresAt time.Time

	// Size is the approximate size of the entry in bytes (optional).
	Size int
}

// IsExpired returns true if the entry has expired.
func (e *Entry) IsExpired() bool {
	if e.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(e.ExpiresAt)
}

// New creates a new cache with default options.
// This is a convenience function that returns a ShardedCache
// with sensible default settings.
func New() Cache {
	return NewCache()
}

// NewCache creates a new cache with the given options.
// If no options are provided, default options are used.
// Returns a ShardedCache by default.
func NewCache(opts ...Option) Cache {
	options := DefaultOptions()
	for _, opt := range opts {
		opt(&options)
	}
	return NewShardedCache(options)
}

// NewTTLCache creates a new TTL-capable cache with the given options.
// If no options are provided, default options are used.
// Returns a ShardedCache which implements TTLCache.
func NewTTLCache(opts ...Option) TTLCache {
	options := DefaultOptions()
	for _, opt := range opts {
		opt(&options)
	}
	return NewShardedCache(options)
}
