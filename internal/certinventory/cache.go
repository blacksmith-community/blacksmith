package certinventory

import (
	"sort"
	"sync"
	"time"
)

// Cache holds the latest scan of each instance, keyed by instance ID, plus
// the broker scan under BrokerScanID. The zero value is ready to use, and
// every method is safe for concurrent use. Every change raises the
// generation, so a reader can rebuild what it derives from the cache only
// when something changed.
//
// Scans are stored and returned by value. A stored scan's observation slice
// is never changed in place, so readers may range over what they get back
// without holding any lock.
type Cache struct {
	mu         sync.RWMutex
	scans      map[string]InstanceScan
	generation uint64
}

// Put stores scan as its instance's latest.
func (c *Cache) Put(scan InstanceScan) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.scans == nil {
		c.scans = map[string]InstanceScan{}
	}

	c.scans[scan.Ref.ID] = scan
	c.generation++
}

// MarkFailed records a failed rescan. The instance keeps its previous
// material, now marked stale with every observation's original time, and
// keeps the time of its last successful scan. An instance with no previous
// scan gets an entry that holds only the error, and one that has never
// scanned cleanly is not marked stale.
func (c *Cache) MarkFailed(id string, err string, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.scans == nil {
		c.scans = map[string]InstanceScan{}
	}

	prev, ok := c.scans[id]
	if !ok {
		c.scans[id] = InstanceScan{Ref: InstanceRef{ID: id}, ScannedAt: at, Err: err}
		c.generation++

		return
	}

	// Copy the observations so a reader holding the previous slice never
	// sees it change.
	raw := make([]RawObservation, len(prev.Material.Raw))
	copy(raw, prev.Material.Raw)

	for i := range raw {
		raw[i].State = StateStale
	}

	prev.Material.Raw = raw
	prev.Err = err
	// Only data from a successful scan can be stale; an instance that has
	// never scanned cleanly has nothing to keep.
	prev.Stale = !prev.LastSuccessAt.IsZero()
	c.scans[id] = prev
	c.generation++
}

// Remove drops an instance. Removing an unknown ID changes nothing.
func (c *Cache) Remove(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.scans[id]; !ok {
		return
	}

	delete(c.scans, id)
	c.generation++
}

// Get returns an instance's latest scan.
func (c *Cache) Get(id string) (InstanceScan, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	scan, ok := c.scans[id]

	return scan, ok
}

// Snapshot returns every stored scan, sorted by instance ID.
func (c *Cache) Snapshot() []InstanceScan {
	c.mu.RLock()

	out := make([]InstanceScan, 0, len(c.scans))
	for _, scan := range c.scans {
		out = append(out, scan)
	}

	c.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].Ref.ID < out[j].Ref.ID })

	return out
}

// Generation returns a number that rises with every change to the cache.
func (c *Cache) Generation() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.generation
}

// ids returns the stored instance IDs.
func (c *Cache) ids() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]string, 0, len(c.scans))
	for id := range c.scans {
		out = append(out, id)
	}

	return out
}
