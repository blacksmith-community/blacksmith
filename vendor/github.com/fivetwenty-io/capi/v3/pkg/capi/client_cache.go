package capi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ClientWithCache wraps a client with caching capabilities
type ClientWithCache struct {
	client  Client
	manager *CacheManager
	policy  *CachingPolicy
}

// NewClientWithCache creates a new client with caching
func NewClientWithCache(client Client, cacheConfig *CacheConfig, policy *CachingPolicy) (*ClientWithCache, error) {
	if cacheConfig == nil {
		cacheConfig = DefaultCacheConfig()
	}
	if policy == nil {
		policy = DefaultCachingPolicy()
	}

	cache, err := NewCacheFromConfig(cacheConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create cache: %w", err)
	}

	manager := NewCacheManager(cache, cacheConfig.Options)

	return &ClientWithCache{
		client:  client,
		manager: manager,
		policy:  policy,
	}, nil
}

// CachedRequest represents a cacheable request
type CachedRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
	Params  interface{}
}

// Execute performs a cached request
func (c *ClientWithCache) Execute(ctx context.Context, req *CachedRequest) ([]byte, error) {
	// Generate cache key
	cacheKey := c.manager.GetCacheKey(req.Method, req.Path, req.Params)

	// Check if request should be cached
	if c.policy.ShouldCache(req.Method, req.Path, 0) {
		// Try to get from cache
		if data, err := c.manager.Get(ctx, cacheKey); err == nil {
			return data, nil
		}
	}

	// Execute the actual request
	// This would need to be integrated with the actual HTTP client
	// For now, this is a placeholder
	data, statusCode, err := c.executeRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	// Cache the response if appropriate
	if c.policy.ShouldCache(req.Method, req.Path, statusCode) {
		ttl := c.calculateTTL(req.Path)
		if err := c.manager.Set(ctx, cacheKey, data, ttl); err != nil {
			// Log cache error but don't fail the request
			// In production, you'd want proper logging here
		}
	}

	return data, nil
}

// executeRequest executes the actual HTTP request
func (c *ClientWithCache) executeRequest(ctx context.Context, req *CachedRequest) ([]byte, int, error) {
	// This is a simplified placeholder
	// In reality, this would use the actual HTTP client
	return nil, 0, fmt.Errorf("not implemented")
}

// calculateTTL calculates the TTL for a cache entry
func (c *ClientWithCache) calculateTTL(path string) time.Duration {
	// You could have path-specific TTLs
	// For now, use the default from options
	if c.manager.options != nil {
		return c.manager.options.TTL
	}
	return 5 * time.Minute
}

// InvalidateCache invalidates cache entries
func (c *ClientWithCache) InvalidateCache(ctx context.Context, pattern string) error {
	return c.manager.InvalidatePattern(ctx, pattern)
}

// ClearCache clears all cache entries
func (c *ClientWithCache) ClearCache(ctx context.Context) error {
	return c.manager.Clear(ctx)
}

// GetCacheStats returns cache statistics
func (c *ClientWithCache) GetCacheStats() *CacheStats {
	return c.manager.GetStats()
}

// CacheInterceptor creates request/response interceptors for caching
func CacheInterceptor(manager *CacheManager, policy *CachingPolicy) (RequestInterceptor, ResponseInterceptor) {
	if policy == nil {
		policy = DefaultCachingPolicy()
	}

	// Request interceptor checks cache
	requestInterceptor := func(ctx context.Context, req *Request) error {
		// Only check cache for GET requests (or as configured)
		if !policy.ShouldCache(req.Method, req.Path, 0) {
			return nil
		}

		// Generate cache key
		cacheKey := manager.GetCacheKey(req.Method, req.Path, nil)

		// Try to get from cache
		if data, err := manager.Get(ctx, cacheKey); err == nil {
			// Found in cache, store in context for response interceptor
			req.Context = context.WithValue(req.Context, contextKey("cached_response"), data)
		}

		return nil
	}

	// Response interceptor stores in cache
	responseInterceptor := func(ctx context.Context, req *Request, resp *Response) error {
		// Check if response was already cached
		if cachedData := req.Context.Value(contextKey("cached_response")); cachedData != nil {
			// Response was served from cache
			return nil
		}

		// Check if we should cache this response
		if !policy.ShouldCache(req.Method, req.Path, resp.StatusCode) {
			return nil
		}

		// Generate cache key
		cacheKey := manager.GetCacheKey(req.Method, req.Path, nil)

		// Calculate TTL
		ttl := policy.MinTTL
		if cacheControl := resp.Headers.Get("Cache-Control"); cacheControl != "" {
			// Parse cache control header for max-age
			// This is simplified, you'd want proper parsing
			ttl = parseCacheControl(cacheControl, policy)
		}

		// Store in cache
		if resp.Body != nil {
			if etag := resp.Headers.Get("ETag"); etag != "" {
				_ = manager.SetWithETag(ctx, cacheKey, resp.Body, etag, ttl)
			} else {
				_ = manager.Set(ctx, cacheKey, resp.Body, ttl)
			}
		}

		return nil
	}

	return requestInterceptor, responseInterceptor
}

// parseCacheControl parses cache control header
func parseCacheControl(header string, policy *CachingPolicy) time.Duration {
	// This is a simplified implementation
	// In production, you'd want proper parsing of max-age, no-cache, etc.
	return policy.MinTTL
}

// ConditionalRequestInterceptor adds conditional request headers based on cache
func ConditionalRequestInterceptor(manager *CacheManager) RequestInterceptor {
	return func(ctx context.Context, req *Request) error {
		// Only for GET requests
		if req.Method != "GET" {
			return nil
		}

		// Generate cache key
		cacheKey := manager.GetCacheKey(req.Method, req.Path, nil)

		// Check if we have an ETag in cache
		if entry, err := manager.cache.Get(ctx, cacheKey); err == nil && entry.ETag != "" {
			if req.Headers == nil {
				req.Headers = make(http.Header)
			}
			req.Headers.Set("If-None-Match", entry.ETag)
		}

		return nil
	}
}

// CacheInvalidationInterceptor invalidates cache based on mutations
func CacheInvalidationInterceptor(manager *CacheManager) ResponseInterceptor {
	return func(ctx context.Context, req *Request, resp *Response) error {
		// Invalidate cache on successful mutations
		if req.Method == "POST" || req.Method == "PUT" || req.Method == "PATCH" || req.Method == "DELETE" {
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				// Invalidate related cache entries
				// This is simplified - you'd want more sophisticated invalidation
				pattern := fmt.Sprintf("GET:%s*", req.Path)
				_ = manager.InvalidatePattern(ctx, pattern)
			}
		}

		return nil
	}
}

// CacheMetricsInterceptor collects cache metrics
func CacheMetricsInterceptor(manager *CacheManager) ResponseInterceptor {
	return func(ctx context.Context, req *Request, resp *Response) error {
		// Check if response was served from cache
		if cachedData := req.Context.Value(contextKey("cached_response")); cachedData != nil {
			// This was a cache hit, metrics already updated by manager.Get()
		} else {
			// This was a cache miss, metrics already updated by manager.Get()
		}

		return nil
	}
}

// SmartCacheConfig provides intelligent cache configuration
type SmartCacheConfig struct {
	// EnableSmartInvalidation enables smart cache invalidation
	EnableSmartInvalidation bool

	// EnableConditionalRequests enables conditional requests with ETags
	EnableConditionalRequests bool

	// EnableMetrics enables cache metrics collection
	EnableMetrics bool

	// ResourceTTLs maps resource types to TTLs
	ResourceTTLs map[string]time.Duration
}

// DefaultSmartCacheConfig returns default smart cache configuration
func DefaultSmartCacheConfig() *SmartCacheConfig {
	return &SmartCacheConfig{
		EnableSmartInvalidation:   true,
		EnableConditionalRequests: true,
		EnableMetrics:             true,
		ResourceTTLs: map[string]time.Duration{
			"/v3/organizations": 10 * time.Minute,
			"/v3/spaces":        5 * time.Minute,
			"/v3/apps":          2 * time.Minute,
			"/v3/processes":     1 * time.Minute,
			"/v3/tasks":         30 * time.Second,
		},
	}
}

// ConfigureSmartCache configures smart caching with interceptors
func ConfigureSmartCache(chain *InterceptorChain, manager *CacheManager, config *SmartCacheConfig) {
	if config == nil {
		config = DefaultSmartCacheConfig()
	}

	// Add cache interceptors
	policy := &CachingPolicy{
		CacheGET:    true,
		CachePOST:   false,
		CacheErrors: false,
		MinTTL:      30 * time.Second,
		MaxTTL:      1 * time.Hour,
	}

	reqInterceptor, respInterceptor := CacheInterceptor(manager, policy)
	chain.AddRequestInterceptor(reqInterceptor)
	chain.AddResponseInterceptor(respInterceptor)

	// Add conditional request support
	if config.EnableConditionalRequests {
		chain.AddRequestInterceptor(ConditionalRequestInterceptor(manager))
	}

	// Add smart invalidation
	if config.EnableSmartInvalidation {
		chain.AddResponseInterceptor(CacheInvalidationInterceptor(manager))
	}

	// Add metrics collection
	if config.EnableMetrics {
		chain.AddResponseInterceptor(CacheMetricsInterceptor(manager))
	}
}

// CacheWarmer warms up the cache with frequently accessed resources
type CacheWarmer struct {
	client  Client
	manager *CacheManager
}

// NewCacheWarmer creates a new cache warmer
func NewCacheWarmer(client Client, manager *CacheManager) *CacheWarmer {
	return &CacheWarmer{
		client:  client,
		manager: manager,
	}
}

// WarmUp warms up the cache with common resources
func (w *CacheWarmer) WarmUp(ctx context.Context) error {
	// This is a simplified implementation
	// In production, you'd want to warm up based on usage patterns

	// Warm up organizations
	if orgs, ok := w.client.(interface {
		Organizations() OrganizationsClient
	}); ok {
		if list, err := orgs.Organizations().List(ctx, nil); err == nil {
			// Cache the response
			if data, err := json.Marshal(list); err == nil {
				cacheKey := w.manager.GetCacheKey("GET", "/v3/organizations", nil)
				_ = w.manager.Set(ctx, cacheKey, data, 10*time.Minute)
			}
		}
	}

	return nil
}
