package vmmonitor_test

import (
	"context"
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

// One monitor never checks an instance in two cycles at once, but two
// monitors that share one index can, as the monitors of two broker
// processes over one Vault do. When the second marks an instance deleted
// while the first is still checking it, the first finds the entry already
// marked. It must take that for the tombstone it is, and not for a young
// entry in flight, so the instance ends up with the VM status "deleted",
// marked once, and unwatched by both.
func TestOverlappingChecksLeaveADeletedInstanceDeleted(t *testing.T) {
	t.Parallel()

	held := newHeldListing()

	director := goneDirector(nil)
	director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) {
		held.list()

		return nil, errProxyNotFound
	}

	vault := &indexVault{entries: map[string]interface{}{inFlightID: oldEntry()}}
	first := vmmonitor.NewWithVault(vault, director, &heldClaims{})
	second := vmmonitor.NewWithVault(vault, director, &heldClaims{})

	for _, monitor := range []*vmmonitor.Monitor{first, second} {
		err := monitor.ScanAllServices(t.Context())
		if err != nil {
			t.Fatalf("the scan failed: %v", err)
		}
	}

	firstDone := make(chan struct{})

	go func() {
		defer close(firstDone)

		first.CheckScheduledServices(t.Context())
	}()

	<-held.entered

	second.CheckScheduledServices(t.Context())

	if marked := vault.markedDeleted(); len(marked) != 1 {
		t.Fatalf("the second monitor marked %v deleted while the first was checking, want %s marked", marked, inFlightID)
	}

	close(held.release)
	<-firstDone

	marked := vault.markedDeleted()
	_, firstWatches := first.MonitoredDeployment(inFlightID)
	_, secondWatches := second.MonitoredDeployment(inFlightID)
	status := vault.storedStatus(inFlightID)

	if entryStatus(vault.entry(inFlightID)) != statusDeleted || len(marked) != 1 || firstWatches || secondWatches || status != statusDeleted {
		t.Errorf("after overlapping checks: index status %q, marked %v, watched by the first %t and the second %t, VM status %q; want the entry marked deleted once, unwatched, and deleted",
			entryStatus(vault.entry(inFlightID)), marked, firstWatches, secondWatches, status)
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

// A rescan can stop watching an instance whose index entry is gone while a
// cycle is still checking it, and AddService can then watch the instance
// again under a new record. A later cycle must still skip the instance
// until the first check ends, and a cycle after that checks it again.
func TestOverlappingChecksSkipAnInstanceDroppedAndAddedAgain(t *testing.T) {
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

	vault.removeEntry(inFlightID)
	monitor.RescanServices(t.Context())

	if _, watched := monitor.MonitoredDeployment(inFlightID); watched {
		t.Fatalf("the rescan still watches %s after its index entry was removed", inFlightID)
	}

	addInFlight(t, monitor)
	monitor.CheckScheduledServices(t.Context())

	during := held.count()

	close(held.release)
	<-firstDone

	if during != 1 {
		t.Errorf("a cycle listed the VMs of an instance dropped and added again while the first check was still running: %d listings, want 1", during)
	}

	addInFlight(t, monitor)
	monitor.CheckScheduledServices(t.Context())

	if after := held.count(); after != 2 {
		t.Errorf("a cycle after the first check ended: %d listings in all, want 2", after)
	}
}

// addInFlight has the monitor watch the in-flight instance through
// AddService, as the broker's provision hook does.
func addInFlight(t *testing.T, monitor *vmmonitor.Monitor) {
	t.Helper()

	err := monitor.AddService(t.Context(), inFlightID, inFlightDeployment)
	if err != nil {
		t.Fatalf("adding the instance failed: %v", err)
	}
}

// A check that panics, or that runs with a cancelled context, still ends,
// so the next cycle checks the instance again.
func TestAnEndedCheckLetsTheNextCycleCheckAgain(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		listed func(listings int) ([]bosh.VM, error)
		cancel bool
	}{
		{
			name: "a check that panics",
			listed: func(listings int) ([]bosh.VM, error) {
				if listings == 1 {
					panic("the director client panicked")
				}

				return nil, errProxyNotFound
			},
		},
		{
			name:   "a check run with a cancelled context",
			listed: func(int) ([]bosh.VM, error) { return nil, errProxyNotFound },
			cancel: true,
		},
	}

	for _, ended := range cases {
		t.Run(ended.name, func(t *testing.T) {
			t.Parallel()

			var (
				listMu   sync.Mutex
				listings int
			)

			director := goneDirector(nil)
			director.GetDeploymentFn = func(string) (*bosh.DeploymentDetail, error) { return nil, errDirectorUnavailable }
			director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) {
				listMu.Lock()
				listings++
				count := listings
				listMu.Unlock()

				return ended.listed(count)
			}

			vault := &indexVault{entries: map[string]interface{}{inFlightID: oldEntry()}}
			monitor := vmmonitor.NewWithVault(vault, director, noClaims{})

			addInFlight(t, monitor)

			ctx := t.Context()
			if ended.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()

				ctx = cancelled
			}

			monitor.CheckScheduledServices(ctx)
			monitor.TriggerRefreshAll(t.Context())

			listMu.Lock()
			defer listMu.Unlock()

			if listings != 2 {
				t.Errorf("after %s: %d listings in all, want 2", ended.name, listings)
			}
		})
	}
}
