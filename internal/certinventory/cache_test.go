package certinventory

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func cachedScan(id string, at time.Time) InstanceScan {
	return InstanceScan{
		Ref: InstanceRef{ID: id, Name: "name-" + id},
		Material: ScanMaterial{Raw: []RawObservation{
			{Observation: Observation{Source: SourceProbe, Locator: id + "/0 10.0.0.1:6379", State: StateOK, ObservedAt: at}},
			{Observation: Observation{Source: SourceProbe, Locator: id + "/1 10.0.0.2:6379", State: StateFailed, Error: "refused", ObservedAt: at}},
		}},
		ScannedAt:     at,
		LastSuccessAt: at,
	}
}

func TestCachePutGetSnapshotGeneration(t *testing.T) {
	t.Parallel()

	var c Cache

	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

	if c.Generation() != 0 || len(c.Snapshot()) != 0 {
		t.Fatal("a new cache is not empty")
	}

	c.Put(cachedScan("b", at))
	c.Put(cachedScan("a", at))

	if c.Generation() != 2 {
		t.Errorf("generation = %d, want 2", c.Generation())
	}

	snap := c.Snapshot()
	if len(snap) != 2 || snap[0].Ref.ID != "a" || snap[1].Ref.ID != "b" {
		t.Errorf("snapshot = %v, want a then b", snap)
	}

	got, ok := c.Get("a")
	if !ok || got.Ref.Name != "name-a" {
		t.Errorf("Get(a) = %+v, %v", got, ok)
	}

	if _, ok := c.Get("zzz"); ok {
		t.Error("Get found an unknown ID")
	}

	c.Remove("zzz")

	if c.Generation() != 2 {
		t.Errorf("removing an unknown ID changed the generation to %d", c.Generation())
	}

	c.Remove("b")

	if c.Generation() != 3 || len(c.Snapshot()) != 1 {
		t.Errorf("after Remove: generation %d, entries %d", c.Generation(), len(c.Snapshot()))
	}
}

func TestCacheMarkFailedKeepsMaterial(t *testing.T) {
	t.Parallel()

	var c Cache

	first := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	failed := first.Add(time.Hour)
	scan := cachedScan("a", first)

	c.Put(scan)
	c.MarkFailed("a", "the instance scan timed out after 1m0s", failed)

	if c.Generation() != 2 {
		t.Errorf("generation = %d, want 2", c.Generation())
	}

	got, _ := c.Get("a")
	if !got.Stale || got.Err != "the instance scan timed out after 1m0s" {
		t.Errorf("stale %v err %q", got.Stale, got.Err)
	}

	if len(got.Material.Raw) != 2 {
		t.Fatalf("raw observations = %d, want the 2 kept", len(got.Material.Raw))
	}

	for _, r := range got.Material.Raw {
		if r.State != StateStale || !r.ObservedAt.Equal(first) {
			t.Errorf("%s: state %q observed %v, want stale with its original time", r.Locator, r.State, r.ObservedAt)
		}
	}

	if !got.LastSuccessAt.Equal(first) || !got.ScannedAt.Equal(first) {
		t.Errorf("last success %v scanned %v, want the first scan's time", got.LastSuccessAt, got.ScannedAt)
	}

	// The caller's slice and earlier copies are never changed in place.
	if scan.Material.Raw[0].State != StateOK {
		t.Error("MarkFailed changed the slice that was put")
	}
}

func TestCacheMarkFailedWithoutData(t *testing.T) {
	t.Parallel()

	var c Cache

	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	c.MarkFailed("new", "list the VMs: refused", at)

	got, ok := c.Get("new")
	if !ok || got.Err != "list the VMs: refused" || got.Stale || !got.LastSuccessAt.IsZero() || len(got.Material.Raw) != 0 {
		t.Errorf("first failure = %+v, %v", got, ok)
	}

	if c.Generation() != 1 {
		t.Errorf("generation = %d, want 1", c.Generation())
	}
}

func TestCacheConcurrentUse(t *testing.T) {
	t.Parallel()

	var c Cache

	at := time.Now()

	var wg sync.WaitGroup

	for w := 0; w < 8; w++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for i := 0; i < 200; i++ {
				id := fmt.Sprintf("i%d", i%5)

				switch (w + i) % 5 {
				case 0:
					c.Put(cachedScan(id, at))
				case 1:
					c.MarkFailed(id, "boom", at)
				case 2:
					for _, s := range c.Snapshot() {
						for _, r := range s.Material.Raw {
							_ = r.State
						}
					}
				case 3:
					if s, ok := c.Get(id); ok {
						for _, r := range s.Material.Raw {
							_ = r.State
						}
					}
				default:
					c.Remove(id)
				}

				_ = c.Generation()
			}
		}()
	}

	wg.Wait()
}
