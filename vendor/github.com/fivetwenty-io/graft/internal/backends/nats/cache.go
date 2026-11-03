package natsbackend

import (
	"sync"
	"time"
)

// CacheItem represents a cached value with expiration.
type CacheItem struct {
	value     interface{}
	expiresAt time.Time
}

// TTLCache provides a time-to-live cache for NATS values.
type TTLCache struct {
	mu    sync.RWMutex
	items map[string]*CacheItem
}

// NewTTLCache creates a new TTL cache.
func NewTTLCache() *TTLCache {
	return &TTLCache{
		items: make(map[string]*CacheItem),
	}
}

// Get retrieves a value from the cache if it exists and is not expired.
func (c *TTLCache) Get(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	item, exists := c.items[key]
	if !exists {
		return nil, false
	}

	if time.Now().After(item.expiresAt) {
		// Item expired, remove it
		c.mu.RUnlock()
		c.mu.Lock()
		delete(c.items, key)
		c.mu.Unlock()
		c.mu.RLock()
		return nil, false
	}

	return item.value, true
}

// Set stores a value in the cache with a TTL.
func (c *TTLCache) Set(key string, value interface{}, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items[key] = &CacheItem{
		value:     value,
		expiresAt: time.Now().Add(ttl),
	}
}

// Cleanup removes expired items from the cache.
func (c *TTLCache) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, item := range c.items {
		if now.After(item.expiresAt) {
			delete(c.items, key)
		}
	}
}

// Clear removes all items from the cache.
func (c *TTLCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*CacheItem)
}

// Len returns the number of items in the cache.
func (c *TTLCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// Global cache instance and settings.
var (
	// Cache is the global TTL-based cache for NATS values.
	Cache = NewTTLCache()

	// DefaultCacheTTL is the default time-to-live for cached values.
	DefaultCacheTTL = 5 * time.Minute

	// CacheCleanupInterval is the interval between cache cleanup runs.
	CacheCleanupInterval = 1 * time.Minute

	// CacheStopCleanup is the channel to stop the cache cleanup goroutine.
	CacheStopCleanup = make(chan struct{})
)

// ClearCache clears the global NATS cache (useful for testing).
func ClearCache() {
	Cache.Clear()
}
