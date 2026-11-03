package cache

import (
	"container/list"
	"sync"
	"sync/atomic"
	"time"
)

// lruEntry is the internal representation of a cache entry in the LRU list.
type lruEntry struct {
	key        string
	value      interface{}
	createdAt  time.Time
	accessedAt time.Time
	size       int
}

// LRUCache implements a thread-safe LRU (Least Recently Used) cache.
// It maintains entries in a doubly-linked list for O(1) eviction and
// uses a map for O(1) lookups.
type LRUCache struct {
	mu sync.RWMutex

	// maxEntries is the maximum number of entries (0 means no limit).
	maxEntries int

	// maxSizeBytes is the maximum total size in bytes (0 means no limit).
	maxSizeBytes int64

	// currentSizeBytes tracks the current total size.
	currentSizeBytes int64

	// items maps keys to list elements for O(1) lookup.
	items map[string]*list.Element

	// evictList maintains entries in LRU order (front = most recent).
	evictList *list.List

	// onEvict is called when an entry is evicted.
	onEvict func(key string, value interface{})

	// Statistics
	hits      atomic.Uint64
	misses    atomic.Uint64
	evictions atomic.Uint64
	sets      atomic.Uint64
}

// NewLRUCache creates a new LRU cache with the specified maximum number of entries.
// If maxEntries is 0 or negative, the cache has no entry limit.
func NewLRUCache(maxEntries int) *LRUCache {
	return &LRUCache{
		maxEntries: maxEntries,
		items:      make(map[string]*list.Element),
		evictList:  list.New(),
	}
}

// NewLRUCacheWithOptions creates a new LRU cache with the given options.
func NewLRUCacheWithOptions(opts Options) *LRUCache {
	return &LRUCache{
		maxEntries:   opts.MaxSize,
		maxSizeBytes: opts.MaxSizeBytes,
		items:        make(map[string]*list.Element),
		evictList:    list.New(),
		onEvict:      opts.OnEvict,
	}
}

// Get retrieves a value from the cache and marks it as recently used.
func (c *LRUCache) Get(key string) (interface{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, found := c.items[key]
	if !found {
		c.misses.Add(1)
		return nil, false
	}

	// Move to front (most recently used)
	c.evictList.MoveToFront(elem)

	// Update access time
	entry, ok := elem.Value.(*lruEntry)
	if !ok {
		return nil, false
	}
	entry.accessedAt = time.Now()

	c.hits.Add(1)
	return entry.value, true
}

// Peek retrieves a value without updating its position in the LRU list.
func (c *LRUCache) Peek(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	elem, found := c.items[key]
	if !found {
		return nil, false
	}

	entry, ok := elem.Value.(*lruEntry)
	if !ok {
		return nil, false
	}
	return entry.value, true
}

// Set adds or updates a value in the cache.
func (c *LRUCache) Set(key string, value interface{}) {
	c.SetWithSize(key, value, 0)
}

// SetWithSize adds or updates a value with an explicit size.
func (c *LRUCache) SetWithSize(key string, value interface{}, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.sets.Add(1)
	now := time.Now()

	// Check if key already exists
	if elem, found := c.items[key]; found {
		// Update existing entry
		c.evictList.MoveToFront(elem)
		entry, ok := elem.Value.(*lruEntry)
		if !ok {
			return
		}

		// Update size tracking
		c.currentSizeBytes -= int64(entry.size)
		c.currentSizeBytes += int64(size)

		entry.value = value
		entry.accessedAt = now
		entry.size = size
		return
	}

	// Create new entry
	entry := &lruEntry{
		key:        key,
		value:      value,
		createdAt:  now,
		accessedAt: now,
		size:       size,
	}

	// Add to front of list
	elem := c.evictList.PushFront(entry)
	c.items[key] = elem
	c.currentSizeBytes += int64(size)

	// Evict if necessary
	c.evictExcess()
}

// evictExcess removes entries until within limits.
// Must be called with lock held.
func (c *LRUCache) evictExcess() {
	// Evict by entry count
	for c.maxEntries > 0 && c.evictList.Len() > c.maxEntries {
		c.evictOldest()
	}

	// Evict by size
	for c.maxSizeBytes > 0 && c.currentSizeBytes > c.maxSizeBytes {
		c.evictOldest()
	}
}

// evictOldest removes the least recently used entry.
// Must be called with lock held.
func (c *LRUCache) evictOldest() {
	elem := c.evictList.Back()
	if elem == nil {
		return
	}

	c.removeElement(elem)
	c.evictions.Add(1)
}

// removeElement removes an element from the cache.
// Must be called with lock held.
func (c *LRUCache) removeElement(elem *list.Element) {
	c.evictList.Remove(elem)
	entry, ok := elem.Value.(*lruEntry)
	if !ok {
		return
	}
	delete(c.items, entry.key)
	c.currentSizeBytes -= int64(entry.size)

	// Call eviction callback
	if c.onEvict != nil {
		c.onEvict(entry.key, entry.value)
	}
}

// Delete removes a value from the cache.
func (c *LRUCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, found := c.items[key]
	if found {
		c.removeElement(elem)
	}
}

// Clear removes all entries from the cache.
func (c *LRUCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Call eviction callbacks if set
	if c.onEvict != nil {
		for _, elem := range c.items {
			if entry, ok := elem.Value.(*lruEntry); ok {
				c.onEvict(entry.key, entry.value)
			}
		}
	}

	c.items = make(map[string]*list.Element)
	c.evictList.Init()
	c.currentSizeBytes = 0
}

// Size returns the number of entries in the cache.
func (c *LRUCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.evictList.Len()
}

// SizeBytes returns the total size of all entries in bytes.
func (c *LRUCache) SizeBytes() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.currentSizeBytes
}

// Stats returns current cache statistics.
func (c *LRUCache) Stats() Stats {
	c.mu.RLock()
	size := c.evictList.Len()
	c.mu.RUnlock()

	return Stats{
		Hits:      c.hits.Load(),
		Misses:    c.misses.Load(),
		Size:      size,
		Evictions: c.evictions.Load(),
		Sets:      c.sets.Load(),
	}
}

// Keys returns all keys in the cache, from most to least recently used.
func (c *LRUCache) Keys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	keys := make([]string, 0, c.evictList.Len())
	for elem := c.evictList.Front(); elem != nil; elem = elem.Next() {
		if entry, ok := elem.Value.(*lruEntry); ok {
			keys = append(keys, entry.key)
		}
	}
	return keys
}

// Contains checks if a key exists in the cache without updating its position.
func (c *LRUCache) Contains(key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, found := c.items[key]
	return found
}

// Oldest returns the oldest (least recently used) entry without removing it.
// Returns nil if the cache is empty.
func (c *LRUCache) Oldest() *Entry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	elem := c.evictList.Back()
	if elem == nil {
		return nil
	}

	e, ok := elem.Value.(*lruEntry)
	if !ok {
		return nil
	}
	return &Entry{
		Key:        e.key,
		Value:      e.value,
		CreatedAt:  e.createdAt,
		AccessedAt: e.accessedAt,
		Size:       e.size,
	}
}

// Newest returns the newest (most recently used) entry without removing it.
// Returns nil if the cache is empty.
func (c *LRUCache) Newest() *Entry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	elem := c.evictList.Front()
	if elem == nil {
		return nil
	}

	e, ok := elem.Value.(*lruEntry)
	if !ok {
		return nil
	}
	return &Entry{
		Key:        e.key,
		Value:      e.value,
		CreatedAt:  e.createdAt,
		AccessedAt: e.accessedAt,
		Size:       e.size,
	}
}

// SetOnEvict sets the eviction callback function.
func (c *LRUCache) SetOnEvict(fn func(key string, value interface{})) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onEvict = fn
}

// Resize changes the maximum number of entries.
// If the new size is smaller, excess entries are evicted.
func (c *LRUCache) Resize(maxEntries int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.maxEntries = maxEntries
	c.evictExcess()
}
