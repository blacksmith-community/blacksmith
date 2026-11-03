package vault

import (
	"sync"

	"github.com/fivetwenty-io/graft/internal/backends/reqdedup"
)

// SecretCache provides thread-safe caching for vault secrets keyed by path.
// It replaces the per-engine vault cache that was previously on OperatorState.
var SecretCache = &secretCache{
	data: make(map[string]map[string]interface{}),
}

type secretCache struct {
	mu    sync.RWMutex
	data  map[string]map[string]interface{}
	group reqdedup.Group[map[string]interface{}]
}

// Get returns cached secret data for the given path, or nil and false.
func (c *secretCache) Get(path string) (map[string]interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.data[path]
	return v, ok
}

// Set stores secret data in the cache for the given path.
func (c *secretCache) Set(path string, data map[string]interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[path] = data
}

// GetOrFetch returns the cached secret for path if present, otherwise calls
// fetch to read it from Vault and caches the result. Concurrent callers
// for the same path are coalesced onto a single fetch
// via the cache's reqdedup.Group, so N references to the same secret
// within one merge produce one Vault request. A failed fetch is never
// cached, so a later call retries rather than replaying the error.
func (c *secretCache) GetOrFetch(path string, fetch func() (map[string]interface{}, error)) (map[string]interface{}, error) {
	if v, ok := c.Get(path); ok {
		return v, nil
	}

	v, err := c.group.Do(path, fetch)
	if err != nil {
		return nil, err
	}

	c.Set(path, v)
	return v, nil
}

// Reset clears the entire cache.
func (c *secretCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = make(map[string]map[string]interface{})
}
