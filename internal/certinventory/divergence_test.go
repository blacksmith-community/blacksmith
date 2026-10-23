package certinventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/credhub"
	"blacksmith/internal/interfaces"
)

const (
	divInstance = "i1"
	divVariable = "/ws-bosh/redis-i1/redis_crt"
	divOther    = "/ws-bosh/redis-i1/other_crt"
	divListen0  = "redis/0:6379"
	divListen1  = "redis/1:6379"
)

var divScanStart = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

// divVault is a Vault that is safe for concurrent use and stores documents
// as JSON. putFail and deleteFail decide, per path, whether a call stores
// its change and whether it returns an error.
type divVault struct {
	interfaces.Vault

	mu         sync.Mutex
	docs       map[string][]byte
	putFail    func(path string) (store bool, err error)
	deleteFail func(path string) (remove bool, err error)
	getFail    func(path string) error
}

func newDivVault() *divVault { return &divVault{docs: map[string][]byte{}} }

func (v *divVault) Get(_ context.Context, path string, result interface{}) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.getFail != nil {
		if err := v.getFail(path); err != nil {
			return false, err
		}
	}

	raw, ok := v.docs[path]
	if !ok {
		return false, nil
	}

	return true, json.Unmarshal(raw, result)
}

func (v *divVault) Put(_ context.Context, path string, data interface{}) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	store, failErr := true, error(nil)
	if v.putFail != nil {
		store, failErr = v.putFail(path)
	}

	if store {
		v.docs[path] = raw
	}

	return failErr
}

func (v *divVault) Delete(_ context.Context, path string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	remove, failErr := true, error(nil)
	if v.deleteFail != nil {
		remove, failErr = v.deleteFail(path)
	}

	if remove {
		delete(v.docs, path)
	}

	return failErr
}

// divClock is a clock a test moves by hand.
type divClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *divClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.at
}

func (c *divClock) set(t time.Time) {
	c.mu.Lock()
	c.at = t
	c.mu.Unlock()
}

// divVersions issues one certificate per version name and returns their
// fingerprints by version ID.
func divVersions(t *testing.T, ids ...string) map[string]issued {
	t.Helper()

	out := map[string]issued{}
	for i, id := range ids {
		out[id] = issue(t, issueSpec{cn: "redis-" + id, serial: int64(100 + i)})
	}

	return out
}

func fps(certs map[string]issued) map[string]string {
	out := map[string]string{}
	for id, c := range certs {
		out[id] = Fingerprint(c.cert)
	}

	return out
}

// divRecord is a record with one variable that a patch moved from v6 to
// v7 on two VMs, an hour before the scan.
func divRecord() *Divergence {
	before := divScanStart.Add(-time.Hour)

	return &Divergence{
		InstanceID: divInstance,
		Revision:   3,
		Variables: []DivergentVariable{{
			Name:             divVariable,
			BOSHVersionID:    "v6",
			AppliedVersionID: "v7",
			TaskID:           "task-1",
			Since:            before,
			State:            DivergenceUnconverged,
			UpdatedAt:        before,
			VMs: map[string]DivergentVM{
				"vm-a": {State: DivergentVMApplied, UpdatedAt: before},
				"vm-b": {State: DivergentVMApplied, UpdatedAt: before},
			},
		}},
	}
}

func divExpected() map[string][]ExpectedListener {
	return map[string][]ExpectedListener{divVariable: {
		{Key: divVariable, Locator: divListen0},
		{Key: divVariable, Locator: divListen1},
	}}
}

// divInput is a scan in which BOSH deploys deployed and each VM serves the
// version named for it. v5 to v9 were created an hour apart in order.
func divInput(t *testing.T, deployed string, served map[string]string) (ReconcileInput, map[string]string) {
	t.Helper()

	certs := divVersions(t, "v5", "v6", "v7", "v9")
	fp := fps(certs)

	in := ReconcileInput{
		ScanStartedAt:      divScanStart,
		Deployed:           map[string]string{divVariable: deployed},
		Expected:           divExpected(),
		Observed:           map[string]ObservedLeaf{},
		VersionFingerprint: fp,
		VersionCreated: map[string]time.Time{
			"v5": divScanStart.Add(-50 * time.Hour),
			"v6": divScanStart.Add(-40 * time.Hour),
			"v7": divScanStart.Add(-30 * time.Hour),
			"v9": divScanStart.Add(-20 * time.Hour),
		},
	}

	listeners := map[string]string{"vm-a": divListen0, "vm-b": divListen1}

	for cid, version := range served {
		in.Observed[listeners[cid]] = ObservedLeaf{VMCID: cid, Fingerprint: fp[version], At: divScanStart.Add(time.Second)}
	}

	return in, fp
}

// unconvergedFinding builds the fleet for one CredHub certificate under the
// record, and reports whether it carries the unconverged finding.
func unconvergedFinding(t *testing.T, d *Divergence) bool {
	t.Helper()

	leaf := issue(t, issueSpec{cn: "redis", serial: 1})
	s := fleetScan(divInstance, fleetNow)
	s.Material.CredHub = []CredHubCert{{Name: divVariable, Owner: instOwner(divInstance), Deployed: leaf.cert,
		Versions: Versions{DeployedVersionID: "v6"}}}

	c := Aggregate([]InstanceScan{s}, DefaultSettings(), fleetCaps, map[string]*Divergence{divInstance: d}, fleetNow).Certificates()[0]

	return hasFinding(c, FindingUnconverged)
}

func TestReconcileClearsWhenDeployedAndServed(t *testing.T) {
	t.Parallel()

	in, _ := divInput(t, "v7", map[string]string{"vm-a": "v7", "vm-b": "v7"})

	if got := Reconcile(divRecord(), in); got != nil {
		t.Fatalf("a deployed and served patch kept %+v", got)
	}
}

func TestReconcileKeepsWhenOneListenerMissing(t *testing.T) {
	t.Parallel()

	in, _ := divInput(t, "v7", map[string]string{"vm-a": "v7"})

	got := Reconcile(divRecord(), in)
	if got == nil || len(got.Variables) != 1 {
		t.Fatalf("an unobserved listener cleared the entry: %+v", got)
	}

	if ref := DivergenceRefOf(got, divVariable); ref == nil || ref.State != DivergenceUnconverged {
		t.Fatalf("ref %+v, want unconverged", ref)
	}

	if !unconvergedFinding(t, got) {
		t.Error("the kept entry raised no unconverged finding")
	}
}

func TestReconcileKeepsWhenOneListenerOld(t *testing.T) {
	t.Parallel()

	in, fp := divInput(t, "v7", map[string]string{"vm-a": "v7", "vm-b": "v7"})
	in.Observed[divListen1] = ObservedLeaf{VMCID: "vm-b", Fingerprint: fp["v7"], At: divScanStart.Add(-time.Second)}

	if got := Reconcile(divRecord(), in); got == nil {
		t.Fatal("an observation from before the scan cleared the entry")
	}
}

func TestReconcileMarksReverted(t *testing.T) {
	t.Parallel()

	in, _ := divInput(t, "v6", map[string]string{"vm-a": "v6", "vm-b": "v7"})
	rec := divRecord()

	got := Reconcile(rec, in)
	if got == nil {
		t.Fatal("a reverted VM cleared the record")
	}

	vms := got.Variables[0].VMs
	if vms["vm-a"].State != DivergentVMReverted {
		t.Errorf("vm-a %+v, want reverted", vms["vm-a"])
	}

	if vms["vm-b"].State != DivergentVMApplied {
		t.Errorf("vm-b %+v, want applied", vms["vm-b"])
	}

	if ref := DivergenceRefOf(got, divVariable); ref == nil || ref.State != DivergenceReverted || ref.TaskID != "task-1" {
		t.Errorf("ref %+v, want reverted for task-1", ref)
	}

	if rec.Variables[0].VMs["vm-a"].State != DivergentVMApplied {
		t.Error("Reconcile changed the record it was given")
	}
}

func TestReconcileRevertedByCreationTime(t *testing.T) {
	t.Parallel()

	t.Run("older version that is neither BOSH's nor applied", func(t *testing.T) {
		t.Parallel()

		in, _ := divInput(t, "v6", map[string]string{"vm-a": "v5"})

		got := Reconcile(divRecord(), in)
		if got == nil || got.Variables[0].VMs["vm-a"].State != DivergentVMReverted {
			t.Fatalf("serving v5 after v7 was applied did not revert vm-a: %+v", got)
		}
	})

	t.Run("fingerprint of no known version", func(t *testing.T) {
		t.Parallel()

		in, _ := divInput(t, "v6", nil)
		stranger := issue(t, issueSpec{cn: "stranger", serial: 77})
		in.Observed[divListen0] = ObservedLeaf{VMCID: "vm-a", Fingerprint: Fingerprint(stranger.cert), At: divScanStart.Add(time.Second)}

		rec := divRecord()
		if got := Reconcile(rec, in); !reflect.DeepEqual(got, rec) {
			t.Fatalf("an unknown certificate changed the record: %+v", got)
		}
	})

	t.Run("served version has no creation time", func(t *testing.T) {
		t.Parallel()

		in, _ := divInput(t, "v6", map[string]string{"vm-a": "v5"})
		delete(in.VersionCreated, "v5")

		rec := divRecord()
		if got := Reconcile(rec, in); !reflect.DeepEqual(got, rec) {
			t.Fatalf("a version of unknown age changed the record: %+v", got)
		}
	})

	t.Run("applied version has no creation time", func(t *testing.T) {
		t.Parallel()

		in, _ := divInput(t, "v6", map[string]string{"vm-a": "v5"})
		delete(in.VersionCreated, "v7")

		rec := divRecord()
		if got := Reconcile(rec, in); !reflect.DeepEqual(got, rec) {
			t.Fatalf("an applied version of unknown age changed the record: %+v", got)
		}
	})
}

func TestReconcileLaterVersionClears(t *testing.T) {
	t.Parallel()

	in, _ := divInput(t, "v9", map[string]string{"vm-a": "v9", "vm-b": "v9"})

	if got := Reconcile(divRecord(), in); got != nil {
		t.Fatalf("a later deployed version kept %+v", got)
	}

	in, _ = divInput(t, "v5", map[string]string{"vm-a": "v5", "vm-b": "v5"})

	if got := Reconcile(divRecord(), in); got == nil {
		t.Fatal("an earlier deployed version cleared the record")
	}
}

func TestReconcileOpaqueVersionIDs(t *testing.T) {
	t.Parallel()

	rec := divRecord()
	rec.Variables[0].BOSHVersionID, rec.Variables[0].AppliedVersionID = "yy-0", "zz-1"

	in := ReconcileInput{
		ScanStartedAt:      divScanStart,
		Deployed:           map[string]string{divVariable: "aa-2"},
		Expected:           map[string][]ExpectedListener{},
		Observed:           map[string]ObservedLeaf{},
		VersionFingerprint: map[string]string{},
		VersionCreated:     map[string]time.Time{},
	}

	if got := Reconcile(rec, in); !reflect.DeepEqual(got, rec) {
		t.Fatalf("missing creation times changed the record: %+v", got)
	}

	in.VersionCreated = map[string]time.Time{"zz-1": divScanStart.Add(-2 * time.Hour), "aa-2": divScanStart.Add(-time.Hour)}

	if got := Reconcile(rec, in); got != nil {
		t.Fatalf("a newer aa-2 did not clear the record: %+v", got)
	}

	in.VersionCreated = map[string]time.Time{"zz-1": divScanStart.Add(-time.Hour), "aa-2": divScanStart.Add(-2 * time.Hour)}

	if got := Reconcile(rec, in); got == nil {
		t.Fatal("an older aa-2 cleared the record")
	}
}

func TestReconcileIgnoresChangesAfterScanStart(t *testing.T) {
	t.Parallel()

	t.Run("VM entry changed after the scan began", func(t *testing.T) {
		t.Parallel()

		in, _ := divInput(t, "v6", map[string]string{"vm-a": "v6"})
		rec := divRecord()
		vm := rec.Variables[0].VMs["vm-a"]
		vm.UpdatedAt = divScanStart.Add(time.Minute)
		rec.Variables[0].VMs["vm-a"] = vm

		if got := Reconcile(rec, in); !reflect.DeepEqual(got, rec) {
			t.Fatalf("a VM entry newer than the scan changed: %+v", got)
		}
	})

	t.Run("observation from before the scan began", func(t *testing.T) {
		t.Parallel()

		in, fp := divInput(t, "v6", nil)
		in.Observed[divListen0] = ObservedLeaf{VMCID: "vm-a", Fingerprint: fp["v6"], At: divScanStart.Add(-time.Second)}

		rec := divRecord()
		if got := Reconcile(rec, in); !reflect.DeepEqual(got, rec) {
			t.Fatalf("an observation older than the scan changed the record: %+v", got)
		}
	})

	t.Run("converged entry changed after the scan began", func(t *testing.T) {
		t.Parallel()

		in, _ := divInput(t, "v7", map[string]string{"vm-a": "v7", "vm-b": "v7"})

		rec := divRecord()
		rec.Variables[0].UpdatedAt = divScanStart.Add(time.Minute)

		if got := Reconcile(rec, in); !reflect.DeepEqual(got, rec) {
			t.Fatalf("an entry newer than the scan was cleared: %+v", got)
		}

		rec = divRecord()
		vm := rec.Variables[0].VMs["vm-b"]
		vm.UpdatedAt = divScanStart.Add(time.Minute)
		rec.Variables[0].VMs["vm-b"] = vm

		if got := Reconcile(rec, in); !reflect.DeepEqual(got, rec) {
			t.Fatalf("an entry with a VM newer than the scan was cleared: %+v", got)
		}
	})
}

// divScan is a scan of the instance, begun at start, in which BOSH deploys
// the given versions and each VM serves the given certificate.
func divScan(start time.Time, deployed map[string]string, served map[string]*issued) InstanceScan {
	scan := fleetScan(divInstance, start)
	scan.VMs = []bosh.VM{
		{CID: "vm-a", Job: "redis", Index: 0, IPs: []string{"10.0.0.1"}},
		{CID: "vm-b", Job: "redis", Index: 1, IPs: []string{"10.0.0.2"}},
	}

	for name, id := range deployed {
		scan.Material.CredHub = append(scan.Material.CredHub, CredHubCert{Name: name, Owner: instOwner(divInstance),
			Versions: Versions{DeployedVersionID: id}})
	}

	scan.Material.Expected = []ExpectedListener{{Key: divVariable, Locator: divListen0}, {Key: divVariable, Locator: divListen1}}

	locators := map[string]string{"vm-a": "redis/0 10.0.0.1:6379", "vm-b": "redis/1 10.0.0.2:6379"}

	for cid, c := range served {
		r := RawObservation{
			Observation: certObservation(SourceProbe, instOwner(divInstance), locators[cid], c.cert, start.Add(time.Second)),
			Cert:        c.cert,
			Listener:    &Listener{Variable: "redis_crt", Port: 6379, Group: "redis"},
		}
		scan.Material.Raw = append(scan.Material.Raw, r)
	}

	return scan
}

// divCredHub serves the versions, created an hour apart in the order given.
func divCredHub(certs map[string]issued, order ...string) *fakeCredHub {
	f := &fakeCredHub{versions: map[string]*credhub.CertificatePublic{}, created: map[string][]credhub.CredentialVersion{}}

	for i, id := range order {
		at := divScanStart.Add(time.Duration(i-len(order)-10) * time.Hour)
		f.versions[id] = &credhub.CertificatePublic{VersionID: id, Certificate: certPEM(certs[id].cert), CreatedAt: at}

		for _, name := range []string{divVariable, divOther} {
			f.created[name] = append(f.created[name], credhub.CredentialVersion{ID: id, Type: "certificate", CreatedAt: at})
		}
	}

	return f
}

func TestDivergenceStaleScanCannotDelete(t *testing.T) {
	t.Parallel()

	clock := &divClock{at: divScanStart.Add(-time.Hour)}
	store := NewDivergenceStore(newDivVault(), clock.now)
	ctx := context.Background()

	err := store.Update(ctx, divInstance, func(*Divergence) (*Divergence, error) {
		rec := divRecord()
		rec.Variables = append(rec.Variables, DivergentVariable{
			Name: divOther, BOSHVersionID: "v6", AppliedVersionID: "v7", TaskID: "task-1",
			State: DivergenceUnconverged, VMs: map[string]DivergentVM{"vm-a": {State: DivergentVMApplied}},
		})

		return rec, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	start, _, _ := store.Get(ctx, divInstance)

	// The scan begins, then the runner adds a VM entry to the other
	// variable before the scan reconciles.
	certs := divVersions(t, "v6", "v7")
	scan := divScan(divScanStart, map[string]string{divVariable: "v7", divOther: "v6"},
		map[string]*issued{"vm-a": ptr(certs["v7"]), "vm-b": ptr(certs["v7"])})

	clock.set(divScanStart.Add(time.Second))

	err = store.Update(ctx, divInstance, func(cur *Divergence) (*Divergence, error) {
		cur.entry(divOther).VMs["vm-b"] = DivergentVM{State: DivergentVMApplied}

		return cur, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	clock.set(divScanStart.Add(2 * time.Second))

	if err := store.ReconcileScan(ctx, scan, divCredHub(certs, "v6", "v7")); err != nil {
		t.Fatal(err)
	}

	got, found, err := store.Get(ctx, divInstance)
	if err != nil || !found {
		t.Fatalf("the record is gone: %v, %v", found, err)
	}

	if got.entry(divVariable) != nil {
		t.Error("the converged variable was not cleared")
	}

	other := got.entry(divOther)
	if other == nil || other.VMs["vm-b"].State != DivergentVMApplied {
		t.Fatalf("the runner's new VM entry was lost: %+v", got)
	}

	if got.Revision != start.Revision+2 {
		t.Errorf("revision %d, want %d", got.Revision, start.Revision+2)
	}
}

func ptr[T any](v T) *T { return &v }

func TestDivergenceUpdateConfirmsAmbiguousWrite(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	add := func(*Divergence) (*Divergence, error) { return divRecord(), nil }

	v := newDivVault()
	v.putFail = func(path string) (bool, error) {
		if path == divergenceRecordPath(divInstance) {
			return true, errors.New("connection reset after the write")
		}

		return true, nil
	}

	store := NewDivergenceStore(v, nil)
	if err := store.Update(ctx, divInstance, add); err != nil {
		t.Fatalf("a write that landed was not confirmed: %v", err)
	}

	if store.Generation() != 1 {
		t.Errorf("generation %d after a confirmed write, want 1", store.Generation())
	}

	lost := newDivVault()
	lost.putFail = func(path string) (bool, error) {
		if path == divergenceRecordPath(divInstance) {
			return false, errors.New("connection reset before the write")
		}

		return true, nil
	}

	store = NewDivergenceStore(lost, nil)
	if err := store.Update(ctx, divInstance, add); !errors.Is(err, ErrSaveUnconfirmed) {
		t.Fatalf("a write that did not land returned %v, want ErrSaveUnconfirmed", err)
	}

	if store.Generation() != 0 {
		t.Errorf("generation %d after an unconfirmed write, want 0", store.Generation())
	}

	unreadable := newDivVault()
	unreadable.putFail = lost.putFail
	unreadable.getFail = func(path string) error {
		if path == divergenceRecordPath(divInstance) && len(unreadable.docs) > 0 {
			return errors.New("vault sealed")
		}

		return nil
	}

	store = NewDivergenceStore(unreadable, nil)
	if err := store.Update(ctx, divInstance, add); !errors.Is(err, ErrSaveUnconfirmed) {
		t.Fatalf("a failed read-back returned %v, want ErrSaveUnconfirmed", err)
	}
}

func TestDivergenceDeleteAndList(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	v := newDivVault()
	store := NewDivergenceStore(v, nil)

	for _, id := range []string{"i2", "i1"} {
		if err := store.Update(ctx, id, func(*Divergence) (*Divergence, error) { return divRecord(), nil }); err != nil {
			t.Fatal(err)
		}
	}

	list, err := store.List(ctx)
	if err != nil || len(list) != 2 || list[0].InstanceID != "i1" || list[1].InstanceID != "i2" {
		t.Fatalf("list %+v, %v", list, err)
	}

	gen := store.Generation()

	if err := store.Update(ctx, "i1", func(*Divergence) (*Divergence, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}

	if _, found, _ := store.Get(ctx, "i1"); found {
		t.Error("the record survived its delete")
	}

	if store.Generation() != gen+1 {
		t.Errorf("a delete left the generation at %d", store.Generation())
	}

	list, err = store.List(ctx)
	if err != nil || len(list) != 1 || list[0].InstanceID != "i2" {
		t.Fatalf("list after delete %+v, %v", list, err)
	}

	if err := store.Update(ctx, "../x", func(*Divergence) (*Divergence, error) { return divRecord(), nil }); !errors.Is(err, ErrInvalidInstanceID) {
		t.Errorf("a path-like instance ID returned %v", err)
	}

	same := store.Generation()
	if err := store.Update(ctx, "i2", func(cur *Divergence) (*Divergence, error) { return cur, nil }); err != nil {
		t.Fatal(err)
	}

	if store.Generation() != same {
		t.Error("an unchanged record was written again")
	}
}

func TestDivergenceUpdateRace(t *testing.T) {
	t.Parallel()

	store := NewDivergenceStore(newDivVault(), nil)
	ctx := context.Background()

	const writers = 50

	var wg sync.WaitGroup

	errs := make(chan error, writers)

	for n := range writers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			errs <- store.Update(ctx, divInstance, func(cur *Divergence) (*Divergence, error) {
				if cur == nil {
					cur = &Divergence{Variables: []DivergentVariable{{Name: divVariable, VMs: map[string]DivergentVM{}}}}
				}

				cur.entry(divVariable).VMs[fmt.Sprintf("vm-%02d", n)] = DivergentVM{State: DivergentVMApplied}

				return cur, nil
			})
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	got, _, err := store.Get(ctx, divInstance)
	if err != nil {
		t.Fatal(err)
	}

	if n := len(got.entry(divVariable).VMs); n != writers {
		t.Errorf("%d VM entries survived, want %d", n, writers)
	}

	if got.Revision != writers || store.Generation() != writers {
		t.Errorf("revision %d and generation %d, want %d", got.Revision, store.Generation(), writers)
	}
}

func TestScanRunsReconcile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	certs := divVersions(t, "v6", "v7")
	store := NewDivergenceStore(newDivVault(), nil)

	for _, id := range []string{"i1", "i2"} {
		err := store.Update(ctx, id, func(*Divergence) (*Divergence, error) {
			rec := divRecord()
			rec.Variables[0].UpdatedAt = time.Time{}

			return rec, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	sc := newFakeScanner()
	sc.result = func(ref InstanceRef, _ int) InstanceScan {
		scan := divScan(time.Now(), map[string]string{divVariable: "v7"},
			map[string]*issued{"vm-a": ptr(certs["v7"]), "vm-b": ptr(certs["v7"])})
		scan.Ref = ref

		return scan
	}

	var cache Cache

	s := newTestScheduler(t, sc, &cache, staticLister("i1", "i2"), nil, settingsWith(4, 60))
	s.SetReconciler(store, divCredHub(certs, "v6", "v7"))

	if _, err := s.ScanInstanceNow(ctx, "i1"); err != nil {
		t.Fatal(err)
	}

	if _, found, _ := store.Get(ctx, "i1"); found {
		t.Error("a forced scan of a converged instance kept its record")
	}

	id, _ := s.RequestScan([]string{"i2"})
	waitScan(t, s, id)

	if _, found, _ := store.Get(ctx, "i2"); found {
		t.Error("a fleet scan of a converged instance kept its record")
	}
}
