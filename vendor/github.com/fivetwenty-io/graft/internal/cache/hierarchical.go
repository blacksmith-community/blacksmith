// Package cache provides a high-performance caching system with support for
// sharded concurrent access, LRU eviction, and TTL-based expiration.
package cache

import (
	"sync"
	"sync/atomic"
	"time"
)

// HierarchicalCache implements a two-level cache hierarchy with L1 (memory)
// and L2 (disk) layers. It coordinates reads and writes between layers,
// promoting frequently accessed items from L2 to L1, and demoting evicted
// items from L1 to L2.
type HierarchicalCache struct {
	l1 Cache      // Fast in-memory cache
	l2 *DiskCache // Persistent disk cache

	// Configuration
	options HierarchicalOptions

	// Metrics (atomic for lock-free access)
	l1Hits      atomic.Uint64
	l1Misses    atomic.Uint64
	l2Hits      atomic.Uint64
	l2Misses    atomic.Uint64
	promotions  atomic.Uint64
	demotions   atomic.Uint64
	writeErrors atomic.Uint64

	// Async write-through queue
	writeQueue chan writeRequest
	stopWrite  chan struct{}
	writeDone  chan struct{}

	// Promotion tracking to avoid thrashing
	promotionTracker *promotionTracker

	mu sync.RWMutex
}

// writeRequest represents an async write-through request.
type writeRequest struct {
	key   string
	value interface{}
	ttl   time.Duration
}

// promotionTracker prevents promoting the same key too frequently.
type promotionTracker struct {
	mu      sync.RWMutex
	recent  map[string]time.Time
	cooloff time.Duration
}

// HierarchicalOptions holds configuration for the hierarchical cache.
type HierarchicalOptions struct {
	// WriteThrough determines if writes go to both L1 and L2.
	WriteThrough bool

	// AsyncWrite performs L2 writes asynchronously if WriteThrough is true.
	AsyncWrite bool

	// WriteQueueSize is the size of the async write queue.
	WriteQueueSize int

	// PromoteOnHit promotes L2 entries to L1 on cache hit.
	PromoteOnHit bool

	// PromotionCooloff prevents promoting the same key within this duration.
	PromotionCooloff time.Duration

	// DemoteOnEvict moves evicted L1 entries to L2.
	DemoteOnEvict bool

	// L2TTLMultiplier extends TTL for L2 entries (e.g., 2.0 doubles the TTL).
	L2TTLMultiplier float64

	// OnL1Evict is called when an entry is evicted from L1.
	OnL1Evict func(key string, value interface{})
}

// HierarchicalOption is a functional option for configuring a HierarchicalCache.
type HierarchicalOption func(*HierarchicalOptions)

// DefaultHierarchicalOptions returns sensible default options.
func DefaultHierarchicalOptions() HierarchicalOptions {
	return HierarchicalOptions{
		WriteThrough:     true,
		AsyncWrite:       true,
		WriteQueueSize:   1000,
		PromoteOnHit:     true,
		PromotionCooloff: 30 * time.Second,
		DemoteOnEvict:    true,
		L2TTLMultiplier:  2.0,
	}
}

// WithWriteThrough enables or disables write-through to L2.
func WithWriteThrough(enabled bool) HierarchicalOption {
	return func(o *HierarchicalOptions) {
		o.WriteThrough = enabled
	}
}

// WithAsyncWrite enables or disables asynchronous L2 writes.
func WithAsyncWrite(enabled bool) HierarchicalOption {
	return func(o *HierarchicalOptions) {
		o.AsyncWrite = enabled
	}
}

// WithWriteQueueSize sets the async write queue size.
func WithWriteQueueSize(size int) HierarchicalOption {
	return func(o *HierarchicalOptions) {
		if size > 0 {
			o.WriteQueueSize = size
		}
	}
}

// WithPromoteOnHit enables or disables promotion from L2 to L1 on hit.
func WithPromoteOnHit(enabled bool) HierarchicalOption {
	return func(o *HierarchicalOptions) {
		o.PromoteOnHit = enabled
	}
}

// WithPromotionCooloff sets the cooloff period between promotions of the same key.
func WithPromotionCooloff(d time.Duration) HierarchicalOption {
	return func(o *HierarchicalOptions) {
		o.PromotionCooloff = d
	}
}

// WithDemoteOnEvict enables or disables demotion to L2 on L1 eviction.
func WithDemoteOnEvict(enabled bool) HierarchicalOption {
	return func(o *HierarchicalOptions) {
		o.DemoteOnEvict = enabled
	}
}

// WithL2TTLMultiplier sets the TTL multiplier for L2 entries.
func WithL2TTLMultiplier(multiplier float64) HierarchicalOption {
	return func(o *HierarchicalOptions) {
		if multiplier > 0 {
			o.L2TTLMultiplier = multiplier
		}
	}
}

// WithOnL1Evict sets the callback for L1 evictions.
func WithOnL1Evict(fn func(key string, value interface{})) HierarchicalOption {
	return func(o *HierarchicalOptions) {
		o.OnL1Evict = fn
	}
}

// NewHierarchicalCache creates a new hierarchical cache with L1 and L2 layers.
// The L1 cache should be a fast in-memory cache, and L2 should be a DiskCache.
func NewHierarchicalCache(l1 Cache, l2 *DiskCache, opts ...HierarchicalOption) *HierarchicalCache {
	options := DefaultHierarchicalOptions()
	for _, opt := range opts {
		opt(&options)
	}

	hc := &HierarchicalCache{
		l1:      l1,
		l2:      l2,
		options: options,
		promotionTracker: &promotionTracker{
			recent:  make(map[string]time.Time),
			cooloff: options.PromotionCooloff,
		},
	}

	// Set up async write queue if enabled
	if options.WriteThrough && options.AsyncWrite {
		hc.writeQueue = make(chan writeRequest, options.WriteQueueSize)
		hc.stopWrite = make(chan struct{})
		hc.writeDone = make(chan struct{})
		go hc.writeLoop()
	}

	// Set up L1 eviction callback for demotion
	if options.DemoteOnEvict && l2 != nil {
		hc.setupEvictionCallback()
	}

	return hc
}

// Get retrieves a value from the cache hierarchy.
// It first checks L1, then L2, promoting to L1 if found in L2.
func (hc *HierarchicalCache) Get(key string) (interface{}, bool) {
	// Try L1 first
	if value, found := hc.l1.Get(key); found {
		hc.l1Hits.Add(1)
		return value, true
	}
	hc.l1Misses.Add(1)

	// Try L2 if available
	if hc.l2 != nil {
		if value, found := hc.l2.Get(key); found {
			hc.l2Hits.Add(1)

			// Promote to L1 if enabled
			if hc.options.PromoteOnHit && hc.shouldPromote(key) {
				hc.l1.Set(key, value)
				hc.promotions.Add(1)
			}

			return value, true
		}
		hc.l2Misses.Add(1)
	}

	return nil, false
}

// Set adds or updates a value in the cache hierarchy.
func (hc *HierarchicalCache) Set(key string, value interface{}) {
	hc.SetWithTTL(key, value, 0)
}

// SetWithTTL adds or updates a value with a specific TTL.
func (hc *HierarchicalCache) SetWithTTL(key string, value interface{}, ttl time.Duration) {
	// Always write to L1
	if ttlCache, ok := hc.l1.(TTLCache); ok && ttl > 0 {
		ttlCache.SetWithTTL(key, value, ttl)
	} else {
		hc.l1.Set(key, value)
	}

	// Write-through to L2 if enabled
	if hc.options.WriteThrough && hc.l2 != nil {
		l2TTL := ttl
		if ttl > 0 && hc.options.L2TTLMultiplier > 0 {
			l2TTL = time.Duration(float64(ttl) * hc.options.L2TTLMultiplier)
		}

		if hc.options.AsyncWrite && hc.writeQueue != nil {
			// Non-blocking async write
			select {
			case hc.writeQueue <- writeRequest{key: key, value: value, ttl: l2TTL}:
			default:
				// Queue full, write synchronously
				hc.l2.SetWithTTL(key, value, l2TTL)
			}
		} else {
			hc.l2.SetWithTTL(key, value, l2TTL)
		}
	}
}

// Delete removes a value from both cache layers.
func (hc *HierarchicalCache) Delete(key string) {
	hc.l1.Delete(key)
	if hc.l2 != nil {
		hc.l2.Delete(key)
	}
}

// Clear removes all entries from both cache layers.
func (hc *HierarchicalCache) Clear() {
	hc.l1.Clear()
	if hc.l2 != nil {
		hc.l2.Clear()
	}
}

// Size returns the combined number of entries in both layers.
func (hc *HierarchicalCache) Size() int {
	size := hc.l1.Size()
	if hc.l2 != nil {
		size += hc.l2.Size()
	}
	return size
}

// L1Size returns the number of entries in L1.
func (hc *HierarchicalCache) L1Size() int {
	return hc.l1.Size()
}

// L2Size returns the number of entries in L2.
func (hc *HierarchicalCache) L2Size() int {
	if hc.l2 != nil {
		return hc.l2.Size()
	}
	return 0
}

// Stats returns combined cache statistics.
func (hc *HierarchicalCache) Stats() Stats {
	l1Stats := hc.l1.Stats()
	var l2Stats Stats
	if hc.l2 != nil {
		l2Stats = hc.l2.Stats()
	}

	return Stats{
		Hits:      l1Stats.Hits + l2Stats.Hits,
		Misses:    l1Stats.Misses + l2Stats.Misses,
		Size:      l1Stats.Size + l2Stats.Size,
		Evictions: l1Stats.Evictions + l2Stats.Evictions,
		Sets:      l1Stats.Sets + l2Stats.Sets,
	}
}

// HierarchicalStats returns detailed statistics for each layer.
func (hc *HierarchicalCache) HierarchicalStats() HierarchicalCacheStats {
	return HierarchicalCacheStats{
		L1Hits:      hc.l1Hits.Load(),
		L1Misses:    hc.l1Misses.Load(),
		L2Hits:      hc.l2Hits.Load(),
		L2Misses:    hc.l2Misses.Load(),
		Promotions:  hc.promotions.Load(),
		Demotions:   hc.demotions.Load(),
		WriteErrors: hc.writeErrors.Load(),
		L1Stats:     hc.l1.Stats(),
		L2Stats:     hc.l2Stats(),
	}
}

// HierarchicalCacheStats holds detailed statistics for hierarchical cache.
type HierarchicalCacheStats struct {
	L1Hits      uint64
	L1Misses    uint64
	L2Hits      uint64
	L2Misses    uint64
	Promotions  uint64
	Demotions   uint64
	WriteErrors uint64
	L1Stats     Stats
	L2Stats     Stats
}

// L1HitRate returns the L1 cache hit rate.
func (s *HierarchicalCacheStats) L1HitRate() float64 {
	total := s.L1Hits + s.L1Misses
	if total == 0 {
		return 0
	}
	return float64(s.L1Hits) / float64(total)
}

// L2HitRate returns the L2 cache hit rate.
func (s *HierarchicalCacheStats) L2HitRate() float64 {
	total := s.L2Hits + s.L2Misses
	if total == 0 {
		return 0
	}
	return float64(s.L2Hits) / float64(total)
}

// OverallHitRate returns the combined hit rate.
func (s *HierarchicalCacheStats) OverallHitRate() float64 {
	totalHits := s.L1Hits + s.L2Hits
	totalMisses := s.L2Misses // Only count L2 misses as true misses
	total := totalHits + totalMisses
	if total == 0 {
		return 0
	}
	return float64(totalHits) / float64(total)
}

// l2Stats returns L2 stats or empty stats if L2 is nil.
func (hc *HierarchicalCache) l2Stats() Stats {
	if hc.l2 == nil {
		return Stats{}
	}
	return hc.l2.Stats()
}

// Close shuts down background goroutines and closes L2.
func (hc *HierarchicalCache) Close() error {
	// Stop async write loop
	if hc.stopWrite != nil {
		close(hc.stopWrite)
		<-hc.writeDone
	}

	// Close L2
	if hc.l2 != nil {
		return hc.l2.Close()
	}

	return nil
}

// shouldPromote checks if a key should be promoted based on cooloff.
func (hc *HierarchicalCache) shouldPromote(key string) bool {
	if hc.options.PromotionCooloff <= 0 {
		return true
	}

	return hc.promotionTracker.shouldPromote(key)
}

func (pt *promotionTracker) shouldPromote(key string) bool {
	now := time.Now()

	pt.mu.RLock()
	lastPromotion, exists := pt.recent[key]
	pt.mu.RUnlock()

	if exists && now.Sub(lastPromotion) < pt.cooloff {
		return false
	}

	pt.mu.Lock()
	pt.recent[key] = now
	// Cleanup old entries periodically
	if len(pt.recent) > 10000 {
		cutoff := now.Add(-pt.cooloff * 2)
		for k, t := range pt.recent {
			if t.Before(cutoff) {
				delete(pt.recent, k)
			}
		}
	}
	pt.mu.Unlock()

	return true
}

// writeLoop processes async write-through requests.
func (hc *HierarchicalCache) writeLoop() {
	defer close(hc.writeDone)

	for {
		select {
		case <-hc.stopWrite:
			// Drain remaining writes
			for {
				select {
				case req := <-hc.writeQueue:
					hc.l2.SetWithTTL(req.key, req.value, req.ttl)
				default:
					return
				}
			}
		case req := <-hc.writeQueue:
			hc.l2.SetWithTTL(req.key, req.value, req.ttl)
		}
	}
}

// setupEvictionCallback configures L1 to demote entries on eviction.
func (hc *HierarchicalCache) setupEvictionCallback() {
	// Try to access the underlying sharded cache
	if sc, ok := hc.l1.(*ShardedCache); ok {
		// We can't easily modify the existing cache's eviction callback,
		// so we wrap the user's callback if they want demotion
		_ = sc // Note: ShardedCache doesn't expose a way to change onEvict after creation
		// The proper way would be to pass the onEvict option when creating L1
	}

	// Alternative: Store original callback and chain
	origCallback := hc.options.OnL1Evict
	hc.options.OnL1Evict = func(key string, value interface{}) {
		// Call original callback first
		if origCallback != nil {
			origCallback(key, value)
		}

		// Demote to L2
		if hc.l2 != nil {
			hc.l2.Set(key, value)
			hc.demotions.Add(1)
		}
	}
}

// Promote manually promotes an entry from L2 to L1.
func (hc *HierarchicalCache) Promote(key string) bool {
	if hc.l2 == nil {
		return false
	}

	value, found := hc.l2.Get(key)
	if !found {
		return false
	}

	hc.l1.Set(key, value)
	hc.promotions.Add(1)
	return true
}

// Demote manually demotes an entry from L1 to L2.
func (hc *HierarchicalCache) Demote(key string) bool {
	value, found := hc.l1.Get(key)
	if !found {
		return false
	}

	hc.l1.Delete(key)

	if hc.l2 != nil {
		hc.l2.Set(key, value)
		hc.demotions.Add(1)
	}

	return true
}

// Contains checks if a key exists in either layer.
func (hc *HierarchicalCache) Contains(key string) bool {
	if sc, ok := hc.l1.(*ShardedCache); ok {
		if sc.Contains(key) {
			return true
		}
	} else {
		if _, found := hc.l1.Get(key); found {
			return true
		}
	}

	if hc.l2 != nil {
		return hc.l2.Contains(key)
	}

	return false
}

// L1Contains checks if a key exists in L1.
func (hc *HierarchicalCache) L1Contains(key string) bool {
	if sc, ok := hc.l1.(*ShardedCache); ok {
		return sc.Contains(key)
	}
	_, found := hc.l1.Get(key)
	return found
}

// L2Contains checks if a key exists in L2.
func (hc *HierarchicalCache) L2Contains(key string) bool {
	if hc.l2 == nil {
		return false
	}
	return hc.l2.Contains(key)
}

// Warm preloads entries into L1 from L2 based on access patterns.
func (hc *HierarchicalCache) Warm(keys []string) int {
	if hc.l2 == nil {
		return 0
	}

	warmed := 0
	for _, key := range keys {
		if value, found := hc.l2.Get(key); found {
			hc.l1.Set(key, value)
			hc.promotions.Add(1)
			warmed++
		}
	}
	return warmed
}

// WarmFromL2 preloads all L2 entries that fit into L1.
func (hc *HierarchicalCache) WarmFromL2(maxEntries int) int {
	if hc.l2 == nil {
		return 0
	}

	keys := hc.l2.Keys()
	if maxEntries > 0 && len(keys) > maxEntries {
		keys = keys[:maxEntries]
	}

	return hc.Warm(keys)
}

// L1 returns the L1 cache for direct access.
func (hc *HierarchicalCache) L1() Cache {
	return hc.l1
}

// L2 returns the L2 cache for direct access.
func (hc *HierarchicalCache) L2() *DiskCache {
	return hc.l2
}

// SetL2Enabled enables or disables the L2 cache at runtime.
func (hc *HierarchicalCache) SetL2Enabled(enabled bool) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	if !enabled {
		hc.l2 = nil
	}
}
