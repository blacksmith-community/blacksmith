package capi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/fivetwenty-io/capi/v3/internal/constants"
)

// CacheInterceptor creates request/response interceptors for caching.
func CacheInterceptor(manager *CacheManager, policy *CachingPolicy) (RequestInterceptor, ResponseInterceptor) {
	if policy == nil {
		policy = DefaultCachingPolicy()
	}

	// Request interceptor.
	//
	// A RequestInterceptor returns only an error: it cannot short-circuit the
	// request with a cached body, and it cannot hand data to the response
	// interceptor because ctx is passed by value. Read-through cache-hit
	// serving is therefore not possible through this hook. Caching value comes
	// from the response interceptor (which stores responses) and from
	// ConditionalRequestInterceptor (which adds If-None-Match for 304s). This
	// interceptor is intentionally a no-op; a real cache-hit lookup here would
	// fetch and then discard the result, only skewing hit metrics.
	requestInterceptor := func(ctx context.Context, req *Request) error {
		return nil
	}

	// Response interceptor stores in cache
	responseInterceptor := func(ctx context.Context, req *Request, resp *Response) error {
		// Check if response was already cached
		if cachedData := ctx.Value(contextKey("cached_response")); cachedData != nil {
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
			ttl = parseCacheControl(policy)
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

// parseCacheControl parses cache control header.
func parseCacheControl(policy *CachingPolicy) time.Duration {
	// This is a simplified implementation
	// In production, you'd want proper parsing of max-age, no-cache, etc.
	return policy.MinTTL
}

// ConditionalRequestInterceptor adds conditional request headers based on cache.
func ConditionalRequestInterceptor(manager *CacheManager) RequestInterceptor {
	return func(ctx context.Context, req *Request) error {
		// Only for GET requests
		if req.Method != http.MethodGet {
			return nil
		}

		// Generate cache key
		cacheKey := manager.GetCacheKey(req.Method, req.Path, nil)

		// Check if we have an ETag in cache
		entry, err := manager.cache.Get(ctx, cacheKey)
		if err == nil && entry.ETag != "" {
			if req.Headers == nil {
				req.Headers = make(http.Header)
			}

			req.Headers.Set("If-None-Match", entry.ETag)
		}

		return nil
	}
}

// CacheInvalidationInterceptor invalidates cache based on mutations.
func CacheInvalidationInterceptor(manager *CacheManager) ResponseInterceptor {
	return func(ctx context.Context, req *Request, resp *Response) error {
		// Invalidate cache on successful mutations
		if req.Method == http.MethodPost || req.Method == http.MethodPut || req.Method == http.MethodPatch || req.Method == http.MethodDelete {
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				// Invalidate all cached entries on successful mutations.
				_ = manager.InvalidateAll(ctx)
			}
		}

		return nil
	}
}

// CacheMetricsInterceptor collects cache metrics.
func CacheMetricsInterceptor(manager *CacheManager) ResponseInterceptor {
	return func(ctx context.Context, req *Request, resp *Response) error {
		// Metrics are automatically updated by manager.Get() for both hits and misses
		_ = ctx.Value(contextKey("cached_response"))

		return nil
	}
}

// SmartCacheConfig provides intelligent cache configuration.
type SmartCacheConfig struct {
	// EnableSmartInvalidation enables smart cache invalidation
	EnableSmartInvalidation bool

	// EnableConditionalRequests enables conditional requests with ETags
	EnableConditionalRequests bool

	// EnableMetrics enables cache metrics collection
	EnableMetrics bool

	// ResourceTTLs maps resource path prefixes (e.g., "/v3/apps") to TTLs
	// applied when caching responses for matching endpoints.
	ResourceTTLs map[string]time.Duration
}

// DefaultSmartCacheConfig returns default smart cache configuration.
func DefaultSmartCacheConfig() *SmartCacheConfig {
	return &SmartCacheConfig{
		EnableSmartInvalidation:   true,
		EnableConditionalRequests: true,
		EnableMetrics:             true,
		ResourceTTLs: map[string]time.Duration{
			"/v3/organizations": constants.OrganizationsCacheTTL,
			"/v3/spaces":        constants.DefaultCacheTTL,
			"/v3/apps":          constants.AppsCacheTTL,
			"/v3/processes":     1 * time.Minute,
			"/v3/tasks":         constants.TasksCacheTTL,
		},
	}
}

// ConfigureSmartCache configures smart caching with interceptors.
func ConfigureSmartCache(chain *InterceptorChain, manager *CacheManager, config *SmartCacheConfig) {
	if config == nil {
		config = DefaultSmartCacheConfig()
	}

	// Add cache interceptors
	policy := &CachingPolicy{
		CacheGET:    true,
		CachePOST:   false,
		CacheErrors: false,
		MinTTL:      constants.CacheMinTTL,
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

// CacheWarmer warms up the cache with frequently accessed resources.
type CacheWarmer struct {
	client  Client
	manager *CacheManager
}

// NewCacheWarmer creates a new cache warmer.
func NewCacheWarmer(client Client, manager *CacheManager) *CacheWarmer {
	return &CacheWarmer{
		client:  client,
		manager: manager,
	}
}

// WarmUp warms up the cache with common resources.
func (w *CacheWarmer) WarmUp(ctx context.Context) error {
	// This is a simplified implementation
	// In production, you'd want to warm up based on usage patterns

	// Warm up organizations
	if orgs, ok := w.client.(interface {
		Organizations() OrganizationsClient
	}); ok {
		list, err := orgs.Organizations().List(ctx, nil)
		if err == nil {
			// Cache the response
			data, err := json.Marshal(list)
			if err == nil {
				cacheKey := w.manager.GetCacheKey("GET", "/v3/organizations", nil)
				_ = w.manager.Set(ctx, cacheKey, data, constants.DefaultCacheSetTTL)
			}
		}
	}

	return nil
}
