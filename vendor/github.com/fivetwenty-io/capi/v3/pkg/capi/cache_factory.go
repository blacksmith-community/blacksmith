package capi

import (
	"context"
	"errors"
	"fmt"

	"github.com/fivetwenty-io/capi/v3/internal/constants"
)

// CacheType represents the type of cache backend.
type CacheType string

const (
	// CacheTypeMemory represents in-memory cache.
	CacheTypeMemory CacheType = "memory"

	// CacheTypeNone represents no caching.
	CacheTypeNone CacheType = "none"
)

// CacheConfig configures cache backend.
// Static errors for err113 compliance.
var (
	ErrUnsupportedCacheType  = errors.New("unsupported cache type")
	ErrCacheDisabled         = errors.New("cache disabled")
	ErrKeyNotFoundInAnyCache = errors.New("key not found in any cache")
)

type CacheConfig struct {
	// Type is the cache backend type
	Type CacheType

	// Memory cache configuration
	Memory *MemoryCacheConfig

	// Common options applied to any backend. If nil, DefaultCacheOptions() is used.
	Options *CacheOptions
}

// MemoryCacheConfig configures memory cache.
type MemoryCacheConfig struct {
	// MaxSize is the maximum number of items in the cache
	MaxSize int

	// CleanupInterval is the interval for cleaning up expired entries
	CleanupInterval string // Duration string like "1m", "5s"
}

// DefaultCacheConfig returns default cache configuration.
func DefaultCacheConfig() *CacheConfig {
	return &CacheConfig{
		Type: CacheTypeMemory,
		Memory: &MemoryCacheConfig{
			MaxSize:         constants.DefaultCacheSize,
			CleanupInterval: "1m",
		},
		Options: DefaultCacheOptions(),
	}
}

// NewCacheFromConfig creates a cache backend from configuration.
func NewCacheFromConfig(config *CacheConfig) (Cache, error) {
	if config == nil {
		config = DefaultCacheConfig()
	}

	switch config.Type {
	case CacheTypeMemory:
		return NewMemoryCacheFromConfig(config.Memory)

	case CacheTypeNone:
		return NewNoOpCache(), nil

	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCacheType, config.Type)
	}
}

// NewMemoryCacheFromConfig creates a memory cache from configuration.
func NewMemoryCacheFromConfig(config *MemoryCacheConfig) (Cache, error) {
	if config == nil {
		config = &MemoryCacheConfig{
			MaxSize:         constants.DefaultCacheSize,
			CleanupInterval: "1m",
		}
	}

	cache := NewMemoryCache(config.MaxSize)

	return cache, nil
}

// NoOpCache is a cache that does nothing (no caching).
type NoOpCache struct{}

// NewNoOpCache creates a new no-op cache.
func NewNoOpCache() *NoOpCache {
	return &NoOpCache{}
}

// Get always returns an error (nothing cached).
func (c *NoOpCache) Get(ctx context.Context, key string) (*CacheEntry, error) {
	return nil, ErrCacheDisabled
}

// Set does nothing.
func (c *NoOpCache) Set(ctx context.Context, key string, entry *CacheEntry) error {
	return nil
}

// Delete does nothing.
func (c *NoOpCache) Delete(ctx context.Context, key string) error {
	return nil
}

// Clear does nothing.
func (c *NoOpCache) Clear(ctx context.Context) error {
	return nil
}

// Has always returns false.
func (c *NoOpCache) Has(ctx context.Context, key string) bool {
	return false
}

// CacheBuilder helps build cache configurations.
type CacheBuilder struct {
	config *CacheConfig
}

// NewCacheBuilder creates a new cache builder.
func NewCacheBuilder() *CacheBuilder {
	return &CacheBuilder{
		config: &CacheConfig{
			Type:    CacheTypeMemory,
			Options: DefaultCacheOptions(),
		},
	}
}

// WithType sets the cache type.
func (b *CacheBuilder) WithType(cacheType CacheType) *CacheBuilder {
	b.config.Type = cacheType

	return b
}

// WithMemoryConfig sets memory cache configuration.
func (b *CacheBuilder) WithMemoryConfig(maxSize int, cleanupInterval string) *CacheBuilder {
	b.config.Memory = &MemoryCacheConfig{
		MaxSize:         maxSize,
		CleanupInterval: cleanupInterval,
	}

	return b
}

// WithOptions sets cache options.
func (b *CacheBuilder) WithOptions(options *CacheOptions) *CacheBuilder {
	b.config.Options = options

	return b
}

// Build creates the cache from the configuration.
func (b *CacheBuilder) Build() (Cache, error) {
	return NewCacheFromConfig(b.config)
}

// CacheChain implements a chain of cache backends (L1, L2, etc.)
type CacheChain struct {
	caches []Cache
}

// NewCacheChain creates a new cache chain.
func NewCacheChain(caches ...Cache) *CacheChain {
	return &CacheChain{
		caches: caches,
	}
}

// Get retrieves an item from the cache chain.
func (c *CacheChain) Get(ctx context.Context, key string) (*CacheEntry, error) {
	for i, cache := range c.caches {
		entry, err := cache.Get(ctx, key)
		if err == nil {
			// Found in this cache, populate earlier caches
			for j := range i {
				_ = c.caches[j].Set(ctx, key, entry)
			}

			return entry, nil
		}
	}

	return nil, ErrKeyNotFoundInAnyCache
}

// Set stores an item in all caches.
func (c *CacheChain) Set(ctx context.Context, key string, entry *CacheEntry) error {
	var lastErr error

	for _, cache := range c.caches {
		err := cache.Set(ctx, key, entry)
		if err != nil {
			lastErr = err
		}
	}

	return lastErr
}

// Delete removes an item from all caches.
func (c *CacheChain) Delete(ctx context.Context, key string) error {
	var lastErr error

	for _, cache := range c.caches {
		err := cache.Delete(ctx, key)
		if err != nil {
			lastErr = err
		}
	}

	return lastErr
}

// Clear removes all items from all caches.
func (c *CacheChain) Clear(ctx context.Context) error {
	var lastErr error

	for _, cache := range c.caches {
		err := cache.Clear(ctx)
		if err != nil {
			lastErr = err
		}
	}

	return lastErr
}

// Has checks if a key exists in any cache.
func (c *CacheChain) Has(ctx context.Context, key string) bool {
	for _, cache := range c.caches {
		if cache.Has(ctx, key) {
			return true
		}
	}

	return false
}
