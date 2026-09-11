package capi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fivetwenty-io/capi/v3/internal/constants"
)

// CacheEntry represents a cached item.
// Static errors for err113 compliance.
var (
	ErrKeyNotFound  = errors.New("key not found")
	ErrEntryExpired = errors.New("entry expired")
)

type CacheEntry struct {
	Data      []byte
	ExpiresAt time.Time
	ETag      string
}

// IsExpired checks if the cache entry has expired.
func (e *CacheEntry) IsExpired() bool {
	return time.Now().After(e.ExpiresAt)
}

// Cache defines the interface for cache implementations.
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

// MemoryCache implements an in-memory cache.
type MemoryCache struct {
	mu      sync.RWMutex
	items   map[string]*CacheEntry
	maxSize int
}

// NewMemoryCache creates a new in-memory cache.
func NewMemoryCache(maxSize int) *MemoryCache {
	return &MemoryCache{
		items:   make(map[string]*CacheEntry),
		maxSize: maxSize,
	}
}

// Get retrieves an item from the cache.
func (c *MemoryCache) Get(ctx context.Context, key string) (*CacheEntry, error) {
	err := ctx.Err()
	if err != nil {
		return nil, fmt.Errorf("cache get: %w", err)
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, exists := c.items[key]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrKeyNotFound, key)
	}

	if entry.IsExpired() {
		// Don't return expired entries
		return nil, fmt.Errorf("%w: %s", ErrEntryExpired, key)
	}

	return entry, nil
}

// Set stores an item in the cache.
func (c *MemoryCache) Set(ctx context.Context, key string, entry *CacheEntry) error {
	err := ctx.Err()
	if err != nil {
		return fmt.Errorf("cache set: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Simple size management — remove soonest-to-expire entry if at capacity.
	if c.maxSize > 0 && len(c.items) >= c.maxSize {
		// Find and remove the soonest-to-expire entry.
		var (
			soonestKey  string
			soonestTime time.Time
		)

		for k, v := range c.items {
			if soonestTime.IsZero() || v.ExpiresAt.Before(soonestTime) {
				soonestKey = k
				soonestTime = v.ExpiresAt
			}
		}

		if soonestKey != "" {
			delete(c.items, soonestKey)
		}
	}

	c.items[key] = entry

	return nil
}

// Delete removes an item from the cache.
func (c *MemoryCache) Delete(ctx context.Context, key string) error {
	err := ctx.Err()
	if err != nil {
		return fmt.Errorf("cache delete: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.items, key)

	return nil
}

// Clear removes all items from the cache.
func (c *MemoryCache) Clear(ctx context.Context) error {
	err := ctx.Err()
	if err != nil {
		return fmt.Errorf("cache clear: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[string]*CacheEntry)

	return nil
}

// Has checks if a key exists in the cache.
func (c *MemoryCache) Has(ctx context.Context, key string) bool {
	if ctx.Err() != nil {
		return false
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, exists := c.items[key]
	if !exists {
		return false
	}

	return !entry.IsExpired()
}

// Cleanup removes expired entries from the cache.
func (c *MemoryCache) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, entry := range c.items {
		if entry.IsExpired() {
			delete(c.items, key)
		}
	}
}

// StartCleanupRoutine starts a background routine to clean up expired entries.
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

// CacheOptions configures caching behavior.
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

// DefaultCacheOptions returns default cache options.
func DefaultCacheOptions() *CacheOptions {
	return &CacheOptions{
		TTL:             constants.DefaultCacheTTL,
		MaxSize:         constants.DefaultCacheSize,
		EnableETags:     true,
		CleanupInterval: 1 * time.Minute,
	}
}

// CacheManager manages caching for the API client.
type CacheManager struct {
	cache   Cache
	options *CacheOptions
	stats   *CacheStats
	// cancel stops the background cleanup goroutine started in
	// NewCacheManager. nil when no cleanup routine was started.
	cancel context.CancelFunc
}

// CacheStats tracks cache statistics using atomic counters so callers can read
// individual fields without holding a lock.
type CacheStats struct {
	hits    atomic.Int64
	misses  atomic.Int64
	sets    atomic.Int64
	deletes atomic.Int64
}

// Hits returns the total number of cache hits.
func (s *CacheStats) Hits() int64 {
	return s.hits.Load()
}

// Misses returns the total number of cache misses.
func (s *CacheStats) Misses() int64 {
	return s.misses.Load()
}

// Sets returns the total number of cache set operations.
func (s *CacheStats) Sets() int64 {
	return s.sets.Load()
}

// Deletes returns the total number of cache delete operations.
func (s *CacheStats) Deletes() int64 {
	return s.deletes.Load()
}

// GetHitRate returns the cache hit rate.
func (s *CacheStats) GetHitRate() float64 {
	hits := s.hits.Load()
	misses := s.misses.Load()
	total := hits + misses

	if total == 0 {
		return 0
	}

	return float64(hits) / float64(total)
}

// NewCacheManager creates a new cache manager.
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

	// Start cleanup routine for memory cache, tied to a cancelable context so
	// Close can stop the goroutine instead of leaking it for process lifetime.
	if memCache, ok := cache.(*MemoryCache); ok && options.CleanupInterval > 0 {
		ctx, cancel := context.WithCancel(context.Background())
		manager.cancel = cancel

		memCache.StartCleanupRoutine(ctx, options.CleanupInterval)
	}

	return manager
}

// Close stops the background cleanup goroutine, if one was started. It is safe
// to call more than once and on a manager that never started a routine.
func (m *CacheManager) Close() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

// GetCacheKey generates a cache key for a request.
func (m *CacheManager) GetCacheKey(method, path string, params any) string {
	key := fmt.Sprintf("%s:%s", method, path)

	if params != nil {
		data, err := json.Marshal(params)
		if err == nil {
			key = fmt.Sprintf("%s:%s", key, string(data))
		}
	}

	return key
}

// Get retrieves an item from the cache.
func (m *CacheManager) Get(ctx context.Context, key string) ([]byte, error) {
	entry, err := m.cache.Get(ctx, key)
	if err != nil {
		m.stats.misses.Add(1)

		return nil, fmt.Errorf("failed to get cached entry: %w", err)
	}

	m.stats.hits.Add(1)

	return entry.Data, nil
}

// Set stores an item in the cache.
func (m *CacheManager) Set(ctx context.Context, key string, data []byte, ttl time.Duration) error {
	if ttl == 0 {
		ttl = m.options.TTL
	}

	entry := &CacheEntry{
		Data:      data,
		ExpiresAt: time.Now().Add(ttl),
	}

	m.stats.sets.Add(1)

	err := m.cache.Set(ctx, key, entry)
	if err != nil {
		return fmt.Errorf("failed to set cache entry: %w", err)
	}

	return nil
}

// SetWithETag stores an item in the cache with an ETag.
func (m *CacheManager) SetWithETag(ctx context.Context, key string, data []byte, etag string, ttl time.Duration) error {
	if ttl == 0 {
		ttl = m.options.TTL
	}

	entry := &CacheEntry{
		Data:      data,
		ExpiresAt: time.Now().Add(ttl),
		ETag:      etag,
	}

	m.stats.sets.Add(1)

	err := m.cache.Set(ctx, key, entry)
	if err != nil {
		return fmt.Errorf("failed to set cache entry: %w", err)
	}

	return nil
}

// Delete removes an item from the cache.
func (m *CacheManager) Delete(ctx context.Context, key string) error {
	m.stats.deletes.Add(1)

	err := m.cache.Delete(ctx, key)
	if err != nil {
		return fmt.Errorf("failed to delete cache entry: %w", err)
	}

	return nil
}

// Clear removes all items from the cache.
func (m *CacheManager) Clear(ctx context.Context) error {
	err := m.cache.Clear(ctx)
	if err != nil {
		return fmt.Errorf("failed to clear cache: %w", err)
	}

	return nil
}

// GetStats returns cache statistics.
func (m *CacheManager) GetStats() *CacheStats {
	return m.stats
}

// InvalidateAll removes all entries from the cache.
func (m *CacheManager) InvalidateAll(ctx context.Context) error {
	err := m.cache.Clear(ctx)
	if err != nil {
		return fmt.Errorf("failed to clear cache: %w", err)
	}

	return nil
}

// CachingPolicy defines when to cache responses.
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

// DefaultCachingPolicy returns a default caching policy.
func DefaultCachingPolicy() *CachingPolicy {
	return &CachingPolicy{
		CacheGET:    true,
		CachePOST:   false,
		CacheErrors: false,
		MinTTL:      constants.CacheMinTTL,
		MaxTTL:      1 * time.Hour,
		ExcludePaths: []string{
			"/v3/jobs",
			"/v3/deployments",
		},
	}
}

// ShouldCache determines if a response should be cached.
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
