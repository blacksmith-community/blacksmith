package certinventory

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/credhub"
)

func serviceFixture(t *testing.T, dir *fakeDirector) *Service {
	t.Helper()

	svc := NewService(ServiceDeps{
		Scanner: ScannerDeps{
			Director:     dir,
			Vault:        newFakeVault(),
			Prober:       Prober{Timeout: time.Second},
			DirectorName: testDirector,
		},
		List:             func(context.Context) ([]InstanceRef, error) { return nil, nil },
		InventoryEnabled: true,
		Now:              func() time.Time { return fleetNow },
	})

	return svc
}

func TestServiceNilCredHubClientStaysNilInterface(t *testing.T) {
	t.Parallel()

	svc := serviceFixture(t, &fakeDirector{manifest: "name: d\n"})
	if svc.scanner.deps.CredHub != nil {
		t.Fatal("a missing CredHub client became a typed-nil interface")
	}

	if svc.finder != nil {
		t.Error("a missing CredHub client became a typed-nil finder")
	}
}

func TestServiceCapabilitiesWithoutCredHub(t *testing.T) {
	t.Parallel()

	svc := serviceFixture(t, &fakeDirector{manifest: "name: d\n"})

	resp := svc.Response(context.Background(), Filter{})
	if resp.Capabilities.CredHub.State != credhub.StateNotConfigured {
		t.Errorf("credhub state %q", resp.Capabilities.CredHub.State)
	}

	if resp.Capabilities.Rotation.Enabled || resp.Capabilities.Rotation.Reason != "CredHub not configured" {
		t.Errorf("rotation %+v", resp.Capabilities.Rotation)
	}

	if resp.Capabilities.Director.Version != "282.0.0" || !resp.Capabilities.Director.ForceLatestVariables {
		t.Errorf("director %+v", resp.Capabilities.Director)
	}
}

func TestServiceRebuildsFleetOnlyOnChange(t *testing.T) {
	t.Parallel()

	svc := serviceFixture(t, &fakeDirector{manifest: "name: d\n"})

	var builds atomic.Int32

	svc.aggregate = func(scans []InstanceScan, s Settings, caps Capabilities, now time.Time) *Fleet {
		builds.Add(1)

		return Aggregate(scans, s, caps, now)
	}

	ctx := context.Background()
	svc.Response(ctx, Filter{})
	svc.Response(ctx, Filter{Attention: true})

	if got := builds.Load(); got != 1 {
		t.Fatalf("two reads built %d times, want 1", got)
	}

	svc.cache.Put(InstanceScan{Ref: InstanceRef{ID: "i1"}, ScannedAt: fleetNow, LastSuccessAt: fleetNow})
	svc.Response(ctx, Filter{})

	if got := builds.Load(); got != 2 {
		t.Errorf("a cache change gave %d builds, want 2", got)
	}

	svc.deps.CredHubClient = &credhub.Client{}
	svc.SetCredHubProbe(nil)
	svc.Response(ctx, Filter{})

	if got := builds.Load(); got != 3 {
		t.Errorf("a probe result gave %d builds, want 3", got)
	}
}

func TestServiceDisabledDoesNotStartScheduler(t *testing.T) {
	t.Parallel()

	svc := serviceFixture(t, &fakeDirector{manifest: "name: d\n"})
	svc.deps.InventoryEnabled = false

	svc.Start(context.Background())
	svc.Stop()

	if svc.Enabled() {
		t.Error("Enabled should follow the switch")
	}
}
