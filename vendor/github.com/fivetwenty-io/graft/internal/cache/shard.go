package cache

import (
	"container/list"
	"hash/fnv"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// shardEntry is the internal representation of a cache entry in a shard.
type shardEntry struct {
	key        string
	value      interface{}
	createdAt  time.Time
	accessedAt time.Time
	expiresAt  time.Time
	size       int
}

// shard represents a single cache shard with its own lock.
type shard struct {
	mu sync.RWMutex

	// items maps keys to list elements for O(1) lookup.
	items map[string]*list.Element

	// evictList maintains entries in LRU order (front = most recent).
	evictList *list.List

	// maxSize is the maximum number of entries in this shard.
	maxSize int

	// onEvict is called when an entry is evicted.
	onEvict func(key string, value interface{})
}

// ShardedCache is a thread-safe cache with multiple shards for reduced lock contention.
// Each shard has its own RWMutex, allowing concurrent access to different shards.
type ShardedCache struct {
	shards    []*shard
	shardMask uint32

	// Default TTL for entries (0 means no expiration).
	ttl time.Duration

	// Statistics (atomic for lock-free access).
	hits      atomic.Uint64
	misses    atomic.Uint64
	sets      atomic.Uint64
	evictions atomic.Uint64

	// Cleanup control.
	cleanupInterval time.Duration
	stopCleanup     chan struct{}
	cleanupDone     chan struct{}
}

// NewShardedCache creates a new sharded cache with the given options.
func NewShardedCache(opts Options) *ShardedCache {
	numShards := roundUpToPowerOf2(opts.Shards)
	if numShards < 1 {
		numShards = 16
	}
	// Validate upper bound to prevent integer overflow in uint32 conversion
	if numShards > math.MaxInt32 {
		numShards = 1 << 20 // Cap at 1M shards (2^20)
	}

	shardSize := opts.MaxSize / numShards
	if shardSize < 1 {
		shardSize = 1
	}

	cache := &ShardedCache{
		shards:          make([]*shard, numShards),
		shardMask:       uint32(numShards - 1), // #nosec G115 - bounds checked above
		ttl:             opts.TTL,
		cleanupInterval: opts.CleanupInterval,
		stopCleanup:     make(chan struct{}),
		cleanupDone:     make(chan struct{}),
	}

	for i := 0; i < numShards; i++ {
		cache.shards[i] = &shard{
			items:     make(map[string]*list.Element),
			evictList: list.New(),
			maxSize:   shardSize,
			onEvict:   opts.OnEvict,
		}
	}

	// Start background cleanup if interval is set.
	if opts.CleanupInterval > 0 {
		go cache.cleanupLoop()
	} else {
		close(cache.cleanupDone)
	}

	return cache
}

// getShard returns the shard for a given key using FNV-1a hashing.
func (c *ShardedCache) getShard(key string) *shard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key)) // Hash write never fails
	return c.shards[h.Sum32()&c.shardMask]
}

// Get retrieves a value from the cache.
func (c *ShardedCache) Get(key string) (interface{}, bool) {
	s := c.getShard(key)

	s.mu.Lock()
	defer s.mu.Unlock()

	elem, found := s.items[key]
	if !found {
		c.misses.Add(1)
		return nil, false
	}

	entry, ok := elem.Value.(*shardEntry)
	if !ok {
		c.misses.Add(1)
		return nil, false
	}

	// Check expiration.
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		s.removeElement(elem)
		c.evictions.Add(1)
		c.misses.Add(1)
		return nil, false
	}

	// Move to front (most recently used).
	s.evictList.MoveToFront(elem)
	entry.accessedAt = time.Now()

	c.hits.Add(1)
	return entry.value, true
}

// Set adds or updates a value in the cache using the default TTL.
func (c *ShardedCache) Set(key string, value interface{}) {
	c.SetWithTTL(key, value, c.ttl)
}

// SetWithTTL adds or updates a value with a specific TTL.
func (c *ShardedCache) SetWithTTL(key string, value interface{}, ttl time.Duration) {
	c.SetWithTTLAndSize(key, value, ttl, 0)
}

// SetWithTTLAndSize adds or updates a value with a specific TTL and size.
func (c *ShardedCache) SetWithTTLAndSize(key string, value interface{}, ttl time.Duration, size int) {
	s := c.getShard(key)

	s.mu.Lock()
	defer s.mu.Unlock()

	c.sets.Add(1)
	now := time.Now()

	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = now.Add(ttl)
	}

	// Check if key already exists.
	if elem, found := s.items[key]; found {
		s.evictList.MoveToFront(elem)
		if entry, ok := elem.Value.(*shardEntry); ok {
			entry.value = value
			entry.accessedAt = now
			entry.expiresAt = expiresAt
			entry.size = size
		}
		return
	}

	// Create new entry.
	entry := &shardEntry{
		key:        key,
		value:      value,
		createdAt:  now,
		accessedAt: now,
		expiresAt:  expiresAt,
		size:       size,
	}

	// Add to front of list.
	elem := s.evictList.PushFront(entry)
	s.items[key] = elem

	// Evict if over capacity.
	for s.evictList.Len() > s.maxSize {
		c.evictOldest(s)
	}
}

// evictOldest removes the least recently used entry from a shard.
// Must be called with shard lock held.
func (c *ShardedCache) evictOldest(s *shard) {
	elem := s.evictList.Back()
	if elem == nil {
		return
	}

	s.removeElement(elem)
	c.evictions.Add(1)
}

// removeElement removes an element from a shard.
// Must be called with shard lock held.
func (s *shard) removeElement(elem *list.Element) {
	s.evictList.Remove(elem)
	entry, ok := elem.Value.(*shardEntry)
	if !ok {
		return
	}
	delete(s.items, entry.key)

	// Call eviction callback.
	if s.onEvict != nil {
		s.onEvict(entry.key, entry.value)
	}
}

// Delete removes a value from the cache.
func (c *ShardedCache) Delete(key string) {
	s := c.getShard(key)

	s.mu.Lock()
	defer s.mu.Unlock()

	elem, found := s.items[key]
	if found {
		s.removeElement(elem)
	}
}

// Clear removes all entries from the cache.
func (c *ShardedCache) Clear() {
	for _, s := range c.shards {
		s.mu.Lock()
		// Call eviction callbacks if set.
		if s.onEvict != nil {
			for _, elem := range s.items {
				if entry, ok := elem.Value.(*shardEntry); ok {
					s.onEvict(entry.key, entry.value)
				}
			}
		}
		s.items = make(map[string]*list.Element)
		s.evictList.Init()
		s.mu.Unlock()
	}
}

// Size returns the total number of entries in the cache.
func (c *ShardedCache) Size() int {
	total := 0
	for _, s := range c.shards {
		s.mu.RLock()
		total += s.evictList.Len()
		s.mu.RUnlock()
	}
	return total
}

// Stats returns current cache statistics.
func (c *ShardedCache) Stats() Stats {
	return Stats{
		Hits:      c.hits.Load(),
		Misses:    c.misses.Load(),
		Size:      c.Size(),
		Evictions: c.evictions.Load(),
		Sets:      c.sets.Load(),
	}
}

// cleanupLoop runs periodic cleanup of expired entries.
func (c *ShardedCache) cleanupLoop() {
	defer close(c.cleanupDone)

	ticker := time.NewTicker(c.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCleanup:
			return
		case <-ticker.C:
			c.cleanupExpired()
		}
	}
}

// cleanupExpired removes all expired entries from the cache.
func (c *ShardedCache) cleanupExpired() {
	now := time.Now()

	for _, s := range c.shards {
		s.mu.Lock()

		var toRemove []*list.Element
		for elem := s.evictList.Front(); elem != nil; elem = elem.Next() {
			if entry, ok := elem.Value.(*shardEntry); ok {
				if !entry.expiresAt.IsZero() && now.After(entry.expiresAt) {
					toRemove = append(toRemove, elem)
				}
			}
		}

		for _, elem := range toRemove {
			s.removeElement(elem)
			c.evictions.Add(1)
		}

		s.mu.Unlock()
	}
}

// Close stops the background cleanup goroutine.
// Should be called when the cache is no longer needed.
func (c *ShardedCache) Close() {
	select {
	case <-c.stopCleanup:
		// Already closed
	default:
		close(c.stopCleanup)
	}
	<-c.cleanupDone
}

// ShardStats returns statistics for a specific shard.
func (c *ShardedCache) ShardStats() []ShardInfo {
	stats := make([]ShardInfo, len(c.shards))
	for i, s := range c.shards {
		s.mu.RLock()
		stats[i] = ShardInfo{
			Index:   i,
			Size:    s.evictList.Len(),
			MaxSize: s.maxSize,
		}
		s.mu.RUnlock()
	}
	return stats
}

// ShardInfo holds information about a single shard.
type ShardInfo struct {
	Index   int
	Size    int
	MaxSize int
}

// Keys returns all keys in the cache.
// Note: This operation requires locking all shards and should be used sparingly.
func (c *ShardedCache) Keys() []string {
	var keys []string
	for _, s := range c.shards {
		s.mu.RLock()
		for key := range s.items {
			keys = append(keys, key)
		}
		s.mu.RUnlock()
	}
	return keys
}

// Contains checks if a key exists in the cache without updating its access time.
func (c *ShardedCache) Contains(key string) bool {
	s := c.getShard(key)

	s.mu.RLock()
	defer s.mu.RUnlock()

	elem, found := s.items[key]
	if !found {
		return false
	}

	// Check expiration without removing.
	entry, ok := elem.Value.(*shardEntry)
	if !ok {
		return false
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		return false
	}

	return true
}

// GetEntry retrieves the full entry metadata for a key.
func (c *ShardedCache) GetEntry(key string) (*Entry, bool) {
	s := c.getShard(key)

	s.mu.RLock()
	defer s.mu.RUnlock()

	elem, found := s.items[key]
	if !found {
		return nil, false
	}

	e, ok := elem.Value.(*shardEntry)
	if !ok {
		return nil, false
	}
	return &Entry{
		Key:        e.key,
		Value:      e.value,
		CreatedAt:  e.createdAt,
		AccessedAt: e.accessedAt,
		ExpiresAt:  e.expiresAt,
		Size:       e.size,
	}, true
}

// NumShards returns the number of shards in the cache.
func (c *ShardedCache) NumShards() int {
	return len(c.shards)
}
