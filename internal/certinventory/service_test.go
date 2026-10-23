package certinventory

import (
	"context"
	"crypto/x509"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"
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

	svc.aggregate = func(scans []InstanceScan, s Settings, caps Capabilities, div map[string]*Divergence, now time.Time) *Fleet {
		builds.Add(1)

		return Aggregate(scans, s, caps, div, now)
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

func TestServiceForcedScanWithoutCredHubClient(t *testing.T) {
	t.Parallel()

	served := issue(t, issueSpec{cn: "rabbitmq.bosh", serial: 7})
	port := serveTLS(t, served)

	dir := &fakeDirector{
		manifest: rabbitManifest(port),
		vars: []bosh.Variable{
			{ID: "v1", Name: "/ws-bosh/rabbitmq-1/rabbitmq_crt"},
			{ID: "v2", Name: "/ws-bosh/rabbitmq-1/rabbitmq_ca"},
		},
		vms: []bosh.VM{{CID: "vm-0", Job: "standalone", Index: 0, IPs: []string{"127.0.0.1"}}},
	}

	svc := NewService(ServiceDeps{
		Scanner:          ScannerDeps{Director: dir, Vault: newFakeVault(), Prober: Prober{Timeout: 2 * time.Second}, DirectorName: testDirector},
		List:             func(context.Context) ([]InstanceRef, error) { return []InstanceRef{testRef("rabbitmq-1")}, nil },
		InventoryEnabled: true,
	})

	scan, err := svc.ScanInstance(context.Background(), testInstanceID)
	if err != nil {
		t.Fatal(err)
	}

	if len(scan.Material.CredHub) == 0 {
		t.Fatal("the scan found no CredHub variables")
	}

	for _, ch := range scan.Material.CredHub {
		if ch.Err != "CredHub not configured" || ch.Deployed != nil {
			t.Errorf("%s: err %q deployed %v, want CredHub not configured", ch.Name, ch.Err, ch.Deployed != nil)
		}
	}
}

func TestServiceWithoutDirectorDegrades(t *testing.T) {
	t.Parallel()

	svc := NewService(ServiceDeps{
		Scanner:          ScannerDeps{Vault: newFakeVault(), Prober: Prober{Timeout: time.Second}, DirectorName: testDirector},
		List:             func(context.Context) ([]InstanceRef, error) { return []InstanceRef{testRef("rabbitmq-1")}, nil },
		InventoryEnabled: true,
		RotationEnabled:  true,
		Now:              func() time.Time { return fleetNow },
	})

	if !svc.Enabled() {
		t.Fatal("a broker with no director must keep the inventory on")
	}

	resp := svc.Response(context.Background(), Filter{})
	if resp.Capabilities.Director.ForceLatestVariables {
		t.Error("force_latest_variables should be false without a director")
	}

	if rot := resp.Capabilities.Rotation; rot.Enabled || rot.Reason != "BOSH director unavailable" {
		t.Errorf("rotation %+v, want unavailable for BOSH director unavailable", rot)
	}

	scan, err := svc.ScanInstance(context.Background(), testInstanceID)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(scan.Err, "BOSH director unavailable") {
		t.Errorf("instance scan error %q should name the missing director", scan.Err)
	}
}

// infoDirector is a director whose GetInfo can block or fail.
type infoDirector struct {
	DirectorReader

	entered chan struct{}
	release chan struct{}
	err     error
	calls   atomic.Int32
}

func (d *infoDirector) GetInfo(context.Context) (*bosh.Info, error) {
	d.calls.Add(1)

	if d.entered != nil {
		select {
		case d.entered <- struct{}{}:
		default:
		}

		<-d.release
	}

	if d.err != nil {
		return nil, d.err
	}

	return &bosh.Info{Name: testDirector, Version: "282.0.0"}, nil
}

func TestServiceSlowDirectorDoesNotBlockOthers(t *testing.T) {
	t.Parallel()

	dir := &infoDirector{entered: make(chan struct{}, 1), release: make(chan struct{})}

	svc := NewService(ServiceDeps{
		Scanner:          ScannerDeps{Director: dir, Vault: newFakeVault(), DirectorName: testDirector},
		List:             func(context.Context) ([]InstanceRef, error) { return nil, nil },
		InventoryEnabled: true,
		Now:              func() time.Time { return fleetNow },
	})
	svc.deps.CredHubClient = &credhub.Client{}

	first := make(chan struct{})

	go func() {
		defer close(first)

		svc.Response(context.Background(), Filter{})
	}()

	<-dir.entered

	others := make(chan struct{})

	go func() {
		defer close(others)

		svc.SetCredHubProbe(nil)
		svc.credhubState()
		svc.Response(context.Background(), Filter{})
	}()

	select {
	case <-others:
	case <-time.After(3 * time.Second):
		close(dir.release)
		t.Fatal("other callers blocked behind the slow director")
	}

	close(dir.release)
	<-first
}

func TestServiceCachesDirectorFailure(t *testing.T) {
	t.Parallel()

	dir := &infoDirector{err: errors.New("director down")}
	now := fleetNow

	svc := NewService(ServiceDeps{
		Scanner:          ScannerDeps{Director: dir, Vault: newFakeVault(), DirectorName: testDirector},
		List:             func(context.Context) ([]InstanceRef, error) { return nil, nil },
		InventoryEnabled: true,
		Now:              func() time.Time { return now },
	})

	ctx := context.Background()
	svc.Response(ctx, Filter{})
	svc.Response(ctx, Filter{Attention: true})

	if got := dir.calls.Load(); got != 1 {
		t.Fatalf("two reads called GetInfo %d times, want 1", got)
	}

	now = now.Add(31 * time.Second)

	svc.Response(ctx, Filter{})

	if got := dir.calls.Load(); got != 2 {
		t.Errorf("after the backoff GetInfo ran %d times, want 2", got)
	}
}

func TestServiceRescansBrokerOnceWhenServicesCAIsLearned(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32

	svc := NewService(ServiceDeps{
		Scanner: ScannerDeps{Director: &fakeDirector{manifest: "name: d\n"}, Vault: newFakeVault(), Prober: Prober{Timeout: time.Second}, DirectorName: testDirector},
		List:    func(context.Context) ([]InstanceRef, error) { return nil, nil },
		Broker: func() BrokerSources {
			calls.Add(1)

			return BrokerSources{}
		},
		InventoryEnabled: true,
		Now:              func() time.Time { return fleetNow },
	})

	ctx := context.Background()

	svc.Response(ctx, Filter{})

	if got := calls.Load(); got != 0 {
		t.Fatalf("a fleet with no services CA made %d broker calls, want 0", got)
	}

	svc.cache.Put(InstanceScan{
		Ref: InstanceRef{ID: "i1"}, ScannedAt: fleetNow, LastSuccessAt: fleetNow,
		Material: ScanMaterial{SigningCAs: map[string]*x509.Certificate{"/ws-bosh/ws-blacksmith/blacksmith_services_ca": nil}},
	})

	svc.Response(ctx, Filter{})

	// One call reads the configured sources once, and the rescan makes the
	// second.
	deadline := time.Now().Add(3 * time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if got := calls.Load(); got != 2 {
		t.Fatalf("learning the services CA name gave %d broker calls, want 2", got)
	}

	svc.cache.Put(InstanceScan{Ref: InstanceRef{ID: "i2"}, ScannedAt: fleetNow, LastSuccessAt: fleetNow})
	svc.Response(ctx, Filter{})
	svc.Response(ctx, Filter{Attention: true})
	time.Sleep(150 * time.Millisecond)

	if got := calls.Load(); got != 2 {
		t.Errorf("the broker was rescanned again: %d calls", got)
	}
}

func TestCredHubStateIsExported(t *testing.T) {
	t.Parallel()

	svc := serviceFixture(t, &fakeDirector{})

	state, _ := svc.CredHubState()
	if state != credhub.StateNotConfigured {
		t.Fatalf("state = %q, want not_configured for a service with no CredHub client", state)
	}
}

func TestServiceRebuildsOnDivergenceChange(t *testing.T) {
	t.Parallel()

	svc := NewService(ServiceDeps{
		Scanner:          ScannerDeps{Director: &fakeDirector{manifest: "name: d\n"}, Vault: newDivVault(), DirectorName: testDirector},
		List:             func(context.Context) ([]InstanceRef, error) { return nil, nil },
		InventoryEnabled: true,
		Now:              func() time.Time { return fleetNow },
	})

	var (
		builds atomic.Int32
		seen   atomic.Pointer[map[string]*Divergence]
	)

	svc.aggregate = func(scans []InstanceScan, s Settings, caps Capabilities, div map[string]*Divergence, now time.Time) *Fleet {
		builds.Add(1)
		seen.Store(&div)

		return Aggregate(scans, s, caps, div, now)
	}

	ctx := context.Background()
	svc.Response(ctx, Filter{})
	svc.Response(ctx, Filter{})

	if got := builds.Load(); got != 1 {
		t.Fatalf("two reads built %d times, want 1", got)
	}

	store := svc.Divergence()
	if store == nil {
		t.Fatal("a service with Vault has no divergence store")
	}

	before := store.Generation()

	if err := store.Update(ctx, divInstance, func(*Divergence) (*Divergence, error) { return divRecord(), nil }); err != nil {
		t.Fatal(err)
	}

	if store.Generation() <= before {
		t.Fatalf("a confirmed update left the generation at %d", store.Generation())
	}

	svc.Response(ctx, Filter{})

	if got := builds.Load(); got != 2 {
		t.Fatalf("a divergence change gave %d builds, want 2", got)
	}

	if div := *seen.Load(); div[divInstance] == nil || div[divInstance].entry(divVariable) == nil {
		t.Errorf("the rebuild did not get the new record: %+v", div)
	}

	svc.Response(ctx, Filter{})

	if got := builds.Load(); got != 2 {
		t.Errorf("an unchanged store gave %d builds, want 2", got)
	}
}

func TestServiceNilCredHubVersionLookupStaysNil(t *testing.T) {
	t.Parallel()

	served := issue(t, issueSpec{cn: "rabbitmq.bosh", serial: 7})
	port := serveTLS(t, served)

	dir := &fakeDirector{
		manifest: rabbitManifest(port),
		vars:     []bosh.Variable{{ID: "v1", Name: "/ws-bosh/rabbitmq-1/rabbitmq_crt"}},
		vms:      []bosh.VM{{CID: "vm-0", Job: "standalone", Index: 0, IPs: []string{"127.0.0.1"}}},
	}

	svc := NewService(ServiceDeps{
		Scanner:          ScannerDeps{Director: dir, Vault: newDivVault(), Prober: Prober{Timeout: 2 * time.Second}, DirectorName: testDirector},
		List:             func(context.Context) ([]InstanceRef, error) { return []InstanceRef{testRef("rabbitmq-1")}, nil },
		InventoryEnabled: true,
	})

	svc.sched.mu.Lock()
	versions := svc.sched.sources.Versions
	svc.sched.mu.Unlock()

	if versions != nil {
		t.Fatal("a missing CredHub client became a typed-nil VersionLookup")
	}

	ctx := context.Background()
	store := svc.Divergence()

	err := store.Update(ctx, testInstanceID, func(*Divergence) (*Divergence, error) {
		return &Divergence{Variables: []DivergentVariable{{
			Name: "/ws-bosh/rabbitmq-1/rabbitmq_crt", BOSHVersionID: "v0", AppliedVersionID: "v1", TaskID: "task-1",
			State: DivergenceUnconverged, VMs: map[string]DivergentVM{"vm-0": {State: DivergentVMApplied}},
		}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	before, _, _ := store.Get(ctx, testInstanceID)

	if _, err := svc.ScanInstance(ctx, testInstanceID); err != nil {
		t.Fatal(err)
	}

	after, found, err := store.Get(ctx, testInstanceID)
	if err != nil || !found || !reflect.DeepEqual(before, after) {
		t.Errorf("a scan without CredHub changed the record: %+v, %v, %v", after, found, err)
	}
}

// taskDirector is a fakeDirector that also lists a deployment's tasks.
type taskDirector struct {
	*fakeDirector
	divTasks
}

func TestServiceReconcilesWithTheDirectorsTasks(t *testing.T) {
	t.Parallel()

	sources := func(dir DirectorReader) ReconcileSources {
		svc := NewService(ServiceDeps{
			Scanner:          ScannerDeps{Director: dir, Vault: newDivVault(), DirectorName: testDirector},
			List:             func(context.Context) ([]InstanceRef, error) { return nil, nil },
			InventoryEnabled: true,
		})

		svc.sched.mu.Lock()
		defer svc.sched.mu.Unlock()

		return svc.sched.sources
	}

	if src := sources(&taskDirector{fakeDirector: &fakeDirector{}}); src.Tasks == nil {
		t.Error("a director that lists a deployment's tasks was not handed to reconciliation")
	}

	if src := sources(&fakeDirector{}); src.Tasks != nil {
		t.Errorf("a director without the call became task reader %T", src.Tasks)
	}

	if src := sources(nil); src.Tasks != nil {
		t.Errorf("a missing director became task reader %T", src.Tasks)
	}
}
