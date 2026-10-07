package vmmonitor_test

import (
	"sync"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/vmmonitor"
)

// heldListing is a director whose first VM listing waits until the test
// releases it, so a test can run a second check cycle while the first one
// is still checking an instance. Every later listing answers at once.
type heldListing struct {
	mu       sync.Mutex
	listings int
	entered  chan struct{}
	release  chan struct{}
}

func newHeldListing() *heldListing {
	return &heldListing{entered: make(chan struct{}), release: make(chan struct{})}
}

// list counts a listing, and holds the first one until release is closed.
func (h *heldListing) list() {
	h.mu.Lock()
	h.listings++
	first := h.listings == 1
	h.mu.Unlock()

	if first {
		close(h.entered)
		<-h.release
	}
}

// Two check cycles can overlap, because the monitor loop's tick and the
// refresh that Start triggers both run one. When the second cycle marks an
// instance deleted while the first is still checking it, the first finds
// the entry already marked. It must take that for the tombstone it is, and
// not for a young entry in flight, so the instance ends up with the VM
// status "deleted" and unwatched.
func TestOverlappingChecksLeaveADeletedInstanceDeleted(t *testing.T) {
	t.Parallel()

	held := newHeldListing()

	director := goneDirector(nil)
	director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) {
		held.list()

		return nil, errProxyNotFound
	}

	vault := &indexVault{entries: map[string]interface{}{inFlightID: oldEntry()}}
	monitor := vmmonitor.NewWithVault(vault, director, &heldClaims{})

	err := monitor.ScanAllServices(t.Context())
	if err != nil {
		t.Fatalf("the scan failed: %v", err)
	}

	firstDone := make(chan struct{})

	go func() {
		defer close(firstDone)

		monitor.CheckScheduledServices(t.Context())
	}()

	<-held.entered

	monitor.CheckScheduledServices(t.Context())

	close(held.release)
	<-firstDone

	marked := vault.markedDeleted()
	_, stillMonitored := monitor.MonitoredDeployment(inFlightID)
	status := vault.storedStatus(inFlightID)

	if entryStatus(vault.entry(inFlightID)) != statusDeleted || len(marked) != 1 || stillMonitored || status != statusDeleted {
		t.Errorf("after overlapping checks: index status %q, marked %v, still monitored %t, VM status %q; want the entry marked deleted once, unmonitored, and deleted",
			entryStatus(vault.entry(inFlightID)), marked, stillMonitored, status)
	}
}

// count returns how many listings the director was asked for.
func (h *heldListing) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.listings
}

// A check cycle skips an instance that another cycle is still checking, so
// two cycles never check one instance at the same time, and the instance is
// checked again by a later cycle once the first check ends.
func TestOverlappingChecksNeverCheckOneInstanceTwice(t *testing.T) {
	t.Parallel()

	held := newHeldListing()

	director := goneDirector(nil)
	director.GetDeploymentVMsFn = func(name string) ([]bosh.VM, error) {
		held.list()

		return []bosh.VM{{ID: name + "-vm", JobState: vmStatusRunning}}, nil
	}

	vault := &indexVault{entries: map[string]interface{}{inFlightID: oldEntry()}}
	monitor := vmmonitor.NewWithVault(vault, director, &heldClaims{})

	err := monitor.ScanAllServices(t.Context())
	if err != nil {
		t.Fatalf("the scan failed: %v", err)
	}

	firstDone := make(chan struct{})

	go func() {
		defer close(firstDone)

		monitor.CheckScheduledServices(t.Context())
	}()

	<-held.entered

	monitor.CheckScheduledServices(t.Context())

	during := held.count()

	close(held.release)
	<-firstDone

	if during != 1 {
		t.Errorf("a second cycle listed the VMs of an instance the first cycle was still checking: %d listings, want 1", during)
	}

	monitor.TriggerRefreshAll(t.Context())

	after := held.count()
	status := vault.storedStatus(inFlightID)

	if after != 2 || status != vmStatusRunning {
		t.Errorf("a cycle after the first check ended: %d listings in all, status %q; want 2 listings and running", after, status)
	}
}
