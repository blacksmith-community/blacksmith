package capi

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// CacheEntry represents a cached item
type CacheEntry struct {
	Data      []byte
	ExpiresAt time.Time
	ETag      string
}

// IsExpired checks if the cache entry has expired
func (e *CacheEntry) IsExpired() bool {
	return time.Now().After(e.ExpiresAt)
}

// Cache defines the interface for cache implementations
type Cache interface {
	// Get retrieves an item from the cache
	Get(ctx context.Context, key string) (*CacheEntry, error)

	// Set stores an item in the cache
	Set(ctx context.Context, key string, entry *CacheEntry) error

	// Delete removes an item from the cache
	Delete(ctx context.Context, key string) error

	// Clear removes all items from the cache
	Clear(ctx context.Context) error

	// Has checks if a key exists in the cache
	Has(ctx context.Context, key string) bool
}

// MemoryCache implements an in-memory cache
type MemoryCache struct {
	mu      sync.RWMutex
	items   map[string]*CacheEntry
	maxSize int
}

// NewMemoryCache creates a new in-memory cache
func NewMemoryCache(maxSize int) *MemoryCache {
	return &MemoryCache{
		items:   make(map[string]*CacheEntry),
		maxSize: maxSize,
	}
}

// Get retrieves an item from the cache
func (c *MemoryCache) Get(ctx context.Context, key string) (*CacheEntry, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, exists := c.items[key]
	if !exists {
		return nil, fmt.Errorf("key not found: %s", key)
	}

	if entry.IsExpired() {
		// Don't return expired entries
		return nil, fmt.Errorf("entry expired: %s", key)
	}

	return entry, nil
}

// Set stores an item in the cache
func (c *MemoryCache) Set(ctx context.Context, key string, entry *CacheEntry) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Simple size management - remove oldest entry if at capacity
	if c.maxSize > 0 && len(c.items) >= c.maxSize {
		// Find and remove the oldest entry
		var oldestKey string
		var oldestTime time.Time
		for k, v := range c.items {
			if oldestTime.IsZero() || v.ExpiresAt.Before(oldestTime) {
				oldestKey = k
				oldestTime = v.ExpiresAt
			}
		}
		if oldestKey != "" {
			delete(c.items, oldestKey)
		}
	}

	c.items[key] = entry
	return nil
}

// Delete removes an item from the cache
func (c *MemoryCache) Delete(ctx context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.items, key)
	return nil
}

// Clear removes all items from the cache
func (c *MemoryCache) Clear(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[string]*CacheEntry)
	return nil
}

// Has checks if a key exists in the cache
func (c *MemoryCache) Has(ctx context.Context, key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, exists := c.items[key]
	if !exists {
		return false
	}

	return !entry.IsExpired()
}

// Cleanup removes expired entries from the cache
func (c *MemoryCache) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, entry := range c.items {
		if entry.IsExpired() {
			delete(c.items, key)
		}
	}
}

// StartCleanupRoutine starts a background routine to clean up expired entries
func (c *MemoryCache) StartCleanupRoutine(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.Cleanup()
			}
		}
	}()
}

// CacheOptions configures caching behavior
type CacheOptions struct {
	// TTL is the default time-to-live for cache entries
	TTL time.Duration

	// MaxSize is the maximum number of items to store in the cache
	MaxSize int

	// EnableETags enables ETag-based caching
	EnableETags bool

	// CleanupInterval is the interval for cleaning up expired entries
	CleanupInterval time.Duration
}

// DefaultCacheOptions returns default cache options
func DefaultCacheOptions() *CacheOptions {
	return &CacheOptions{
		TTL:             5 * time.Minute,
		MaxSize:         1000,
		EnableETags:     true,
		CleanupInterval: 1 * time.Minute,
	}
}

// CacheManager manages caching for the API client
type CacheManager struct {
	cache   Cache
	options *CacheOptions
	stats   *CacheStats
}

// CacheStats tracks cache statistics
type CacheStats struct {
	Hits    int64
	Misses  int64
	Sets    int64
	Deletes int64
	mu      sync.RWMutex
}

// GetHitRate returns the cache hit rate
func (s *CacheStats) GetHitRate() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits) / float64(total)
}

// NewCacheManager creates a new cache manager
func NewCacheManager(cache Cache, options *CacheOptions) *CacheManager {
	if options == nil {
		options = DefaultCacheOptions()
	}
	if cache == nil {
		cache = NewMemoryCache(options.MaxSize)
	}

	manager := &CacheManager{
		cache:   cache,
		options: options,
		stats:   &CacheStats{},
	}

	// Start cleanup routine for memory cache
	if memCache, ok := cache.(*MemoryCache); ok && options.CleanupInterval > 0 {
		memCache.StartCleanupRoutine(context.Background(), options.CleanupInterval)
	}

	return manager
}

// GetCacheKey generates a cache key for a request
func (m *CacheManager) GetCacheKey(method, path string, params interface{}) string {
	key := fmt.Sprintf("%s:%s", method, path)
	if params != nil {
		if data, err := json.Marshal(params); err == nil {
			key = fmt.Sprintf("%s:%s", key, string(data))
		}
	}
	return key
}

// Get retrieves an item from the cache
func (m *CacheManager) Get(ctx context.Context, key string) ([]byte, error) {
	entry, err := m.cache.Get(ctx, key)
	if err != nil {
		m.stats.mu.Lock()
		m.stats.Misses++
		m.stats.mu.Unlock()
		return nil, err
	}

	m.stats.mu.Lock()
	m.stats.Hits++
	m.stats.mu.Unlock()

	return entry.Data, nil
}

// Set stores an item in the cache
func (m *CacheManager) Set(ctx context.Context, key string, data []byte, ttl time.Duration) error {
	if ttl == 0 {
		ttl = m.options.TTL
	}

	entry := &CacheEntry{
		Data:      data,
		ExpiresAt: time.Now().Add(ttl),
	}

	m.stats.mu.Lock()
	m.stats.Sets++
	m.stats.mu.Unlock()

	return m.cache.Set(ctx, key, entry)
}

// SetWithETag stores an item in the cache with an ETag
func (m *CacheManager) SetWithETag(ctx context.Context, key string, data []byte, etag string, ttl time.Duration) error {
	if ttl == 0 {
		ttl = m.options.TTL
	}

	entry := &CacheEntry{
		Data:      data,
		ExpiresAt: time.Now().Add(ttl),
		ETag:      etag,
	}

	m.stats.mu.Lock()
	m.stats.Sets++
	m.stats.mu.Unlock()

	return m.cache.Set(ctx, key, entry)
}

// Delete removes an item from the cache
func (m *CacheManager) Delete(ctx context.Context, key string) error {
	m.stats.mu.Lock()
	m.stats.Deletes++
	m.stats.mu.Unlock()

	return m.cache.Delete(ctx, key)
}

// Clear removes all items from the cache
func (m *CacheManager) Clear(ctx context.Context) error {
	return m.cache.Clear(ctx)
}

// GetStats returns cache statistics
func (m *CacheManager) GetStats() *CacheStats {
	return m.stats
}

// InvalidatePattern removes all cache entries matching a pattern
func (m *CacheManager) InvalidatePattern(ctx context.Context, pattern string) error {
	// This is a simplified implementation
	// In a production system, you might want to use a more sophisticated pattern matching
	return m.cache.Clear(ctx)
}

// CachingPolicy defines when to cache responses
type CachingPolicy struct {
	// CacheGET enables caching for GET requests
	CacheGET bool

	// CachePOST enables caching for POST requests (be careful!)
	CachePOST bool

	// CacheErrors enables caching of error responses
	CacheErrors bool

	// MinTTL is the minimum TTL for cache entries
	MinTTL time.Duration

	// MaxTTL is the maximum TTL for cache entries
	MaxTTL time.Duration

	// ExcludePaths lists paths that should not be cached
	ExcludePaths []string

	// IncludePaths lists paths that should always be cached
	IncludePaths []string
}

// DefaultCachingPolicy returns a default caching policy
func DefaultCachingPolicy() *CachingPolicy {
	return &CachingPolicy{
		CacheGET:    true,
		CachePOST:   false,
		CacheErrors: false,
		MinTTL:      30 * time.Second,
		MaxTTL:      1 * time.Hour,
		ExcludePaths: []string{
			"/v3/jobs",
			"/v3/deployments",
		},
	}
}

// ShouldCache determines if a response should be cached
func (p *CachingPolicy) ShouldCache(method, path string, statusCode int) bool {
	// Check if the method is cacheable
	switch method {
	case "GET":
		if !p.CacheGET {
			return false
		}
	case "POST":
		if !p.CachePOST {
			return false
		}
	default:
		return false
	}

	// Check if errors should be cached
	if statusCode >= 400 && !p.CacheErrors {
		return false
	}

	// Check excluded paths
	for _, excludedPath := range p.ExcludePaths {
		if path == excludedPath {
			return false
		}
	}

	// Check included paths (if specified, only these paths are cached)
	if len(p.IncludePaths) > 0 {
		for _, includedPath := range p.IncludePaths {
			if path == includedPath {
				return true
			}
		}
		return false
	}

	return true
}
