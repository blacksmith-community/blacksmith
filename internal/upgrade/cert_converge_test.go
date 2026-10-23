//nolint:testpackage // the tests drive the manager's unexported converge runner
package upgrade

import (
	"context"
	"crypto/x509"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/mutation"
)

// convergeEnv is a redeploy world that a patch left diverged: CredHub
// holds the leaf at v1 and v2, the director still deploys v1, and the
// listener already serves v2, which the divergence record names as
// applied. A done deploy makes the director deploy v2. Each scan reads
// the director and the listener as they stand, so reconciliation sees
// what the deploy did.
type convergeEnv struct {
	*redeployEnv

	store *certinventory.DivergenceStore

	mu    sync.Mutex
	scans int
	// hidden makes the listener's probe fail, so no scan observes it.
	hidden bool
	// gate, when set, runs at the start of each scan.
	gate func()
}

// convergeCertID and convergeVMID are the CredHub ID of the leaf and the
// BOSH ID of the VM that the scan reads.
const (
	convergeCertID = "cert-1"
	convergeVMID   = "vm-1"
)

// divergedBefore is when the patch wrote the divergence record, well
// before any scan the tests run.
var divergedBefore = validateNow.Add(-time.Hour) //nolint:gochecknoglobals // a fixed instant the tests share, like validateNow

func newConvergeEnv(t *testing.T) *convergeEnv {
	t.Helper()

	base := newRedeployEnv(t)
	base.madeNewVersion()
	base.listener.serve(base.newLeaf, base.newKey)

	env := &convergeEnv{
		redeployEnv: base,
		store: certinventory.NewDivergenceStore(
			&divergenceVault{Vault: base.vault, base: base.vault, events: &eventLog{}},
			func() time.Time { return divergedBefore }),
	}

	base.divergence = env.store
	base.scanWith = env.scan

	return env
}

// diverge writes the record a patch of the leaf to v2 leaves behind.
func (e *convergeEnv) diverge() {
	e.t.Helper()

	err := e.store.Update(context.Background(), waitInstanceID, func(*certinventory.Divergence) (*certinventory.Divergence, error) {
		variable := certinventory.DivergentVariable{
			Name: regenLeaf, BOSHVersionID: "v1", AppliedVersionID: "v2",
			TaskID: "patch-task", Since: divergedBefore, State: certinventory.DivergenceUnconverged,
		}
		variable.VMs = map[string]certinventory.DivergentVM{
			redeployVMCID: {State: certinventory.DivergentVMApplied, Group: certJob, Index: 0},
		}

		return &certinventory.Divergence{InstanceID: waitInstanceID, Variables: []certinventory.DivergentVariable{variable}}, nil
	})
	if err != nil {
		e.t.Fatalf("writing the divergence record: %v", err)
	}
}

// diverged reports whether the instance still has a divergence record.
func (e *convergeEnv) diverged() bool {
	e.t.Helper()

	_, found, err := e.store.Get(context.Background(), waitInstanceID)
	if err != nil {
		e.t.Fatalf("reading the divergence record: %v", err)
	}

	return found
}

func (e *convergeEnv) scanCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.scans
}

// scan scans the instance as it stands: the version the director deploys
// and, unless the listener is hidden, the leaf it serves, observed at the
// scan's start. Each scan begins a second after the one before.
func (e *convergeEnv) scan(_ context.Context, instanceID string) (certinventory.InstanceScan, error) {
	e.mu.Lock()
	e.scans++
	scannedAt := validateNow.Add(time.Duration(e.scans) * time.Second)
	hidden, gate := e.hidden, e.gate
	e.mu.Unlock()

	if gate != nil {
		gate()
	}

	e.director.mu.Lock()
	deployed := e.director.deployed
	e.director.mu.Unlock()

	e.listener.mu.Lock()
	leaf := e.listener.pair.Leaf
	e.listener.mu.Unlock()

	port := e.listener.port()

	scan := certinventory.InstanceScan{
		Ref:           certinventory.InstanceRef{ID: instanceID, Deployment: waitDeployment},
		ScannedAt:     scannedAt,
		LastSuccessAt: scannedAt,
		VMs:           []bosh.VM{{ID: convergeVMID, CID: redeployVMCID, Job: certJob, Index: 0, IPs: []string{loopback}}},
		Material: certinventory.ScanMaterial{
			CredHub: []certinventory.CredHubCert{{
				Name: regenLeaf, Deployed: e.oldLeaf,
				Versions: certinventory.Versions{CertificateID: convergeCertID, DeployedVersionID: deployed, LatestVersionID: "v2", SigningCA: regenCAName},
			}},
			SigningCAs: map[string]*x509.Certificate{regenCAName: e.ca.cert},
			Expected:   []certinventory.ExpectedListener{{Key: regenLeaf, Locator: fmt.Sprintf("redis/0:%d", port)}},
		},
	}

	if !hidden {
		scan.Material.Raw = []certinventory.RawObservation{{
			Observation: certinventory.Observation{
				Source:      certinventory.SourceProbe,
				Locator:     fmt.Sprintf("redis/0 127.0.0.1:%d", port),
				Fingerprint: certinventory.Fingerprint(leaf),
				State:       certinventory.StateOK,
				ObservedAt:  scannedAt,
			},
			Cert: leaf,
		}}
	}

	return scan, nil
}

// task returns a confirmed converge task whose one instance has not
// started, approved with the leaf's v2 waiting to be deployed.
func (e *convergeEnv) task() UpgradeTask {
	task := e.redeployEnv.task()
	task.Name = "converge the redis leaf"
	task.Certificates.Mode = RotationModeConverge

	rotation := task.Instances[0].Certificates
	rotation.Approved.Selected = nil
	rotation.Approved.Additional = []VersionPair{{Name: regenLeaf, DeployedVersionID: "v1", LatestVersionID: "v2"}}
	rotation.Variables = nil

	return task
}

// run reserves the task in the manager and runs its one instance through
// the rotation of its mode, as the task's processor would.
func (e *convergeEnv) run(manager *Manager, task *UpgradeTask) bool {
	e.t.Helper()

	err := manager.reserveCertificateTask(task)
	if err != nil {
		e.t.Fatalf("reserving the task: %v", err)
	}

	return manager.rotateInstance(context.Background(), task, &task.Instances[0], make(chan struct{}))
}

// expectOneConvergeDeploy fails the test unless the director received one
// deploy of its own manifest with the latest variables, and the instance
// keeps its context ID and BOSH task on the converge record.
func expectOneConvergeDeploy(t *testing.T, env *convergeEnv, inst InstanceUpgrade) {
	t.Helper()

	deploys := env.director.deploys()
	if len(deploys) != 1 || !deploys[0].opts.ForceLatestVariables || deploys[0].manifest != env.manifest {
		t.Fatalf("expected one deploy of the unchanged manifest with the latest variables, got %+v", deploys)
	}

	converge := inst.Certificates.Converge
	if converge == nil || converge.ContextID == "" || converge.BOSHTaskID != redeployTaskID {
		t.Fatalf("expected the converge record to keep the deploy's IDs, got %+v", converge)
	}

	if inst.ContextID != "" || inst.BOSHTaskID != 0 {
		t.Fatalf("expected the instance's own deploy IDs to stay empty, got %q and %d", inst.ContextID, inst.BOSHTaskID)
	}
}

// A converge task has no variables. It makes no new certificate version,
// deploys the director's manifest once with the latest variables, settles
// the instance as a success, and frees the guard.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestConvergeWithoutVariablesDeploysOnce(t *testing.T) {
	env := newRedeployEnv(t)
	manager := env.newManager()
	task := env.task()
	task.Certificates.Mode = RotationModeConverge
	task.Instances[0].Certificates.Approved.Selected = nil
	task.Instances[0].Certificates.Variables = nil

	err := manager.reserveCertificateTask(&task)
	if err != nil {
		t.Fatalf("reserving the task: %v", err)
	}

	if !manager.rotateInstance(context.Background(), &task, &task.Instances[0], make(chan struct{})) {
		inst := current(t, manager).Instances[0]
		t.Fatalf("settle: instance %s at phase %s: %s", inst.Status, inst.Certificates.Phase, inst.Error)
	}

	inst := current(t, manager).Instances[0]
	if inst.Status != InstanceStatusSuccess || inst.Certificates.Phase != RotationPhaseDone {
		t.Fatalf("expected a completed instance at phase done, got %s at %s: %s", inst.Status, inst.Certificates.Phase, inst.Error)
	}

	if count := env.credhub.regenerateCount(); count != 0 {
		t.Fatalf("expected no regeneration, got %d", count)
	}

	deploys := env.director.deploys()
	if len(deploys) != 1 || !deploys[0].opts.ForceLatestVariables {
		t.Fatalf("expected one deploy with the latest variables, got %+v", deploys)
	}

	expectReleased(t, manager)
}

// The converge preview selects no variable. It binds every variable whose
// latest version differs from the deployed one, lists an instance that
// has a divergence record even when nothing differs, and blocks one that
// has neither.
func TestConvergePreviewBindsAllPendingVariables(t *testing.T) {
	t.Parallel()

	const (
		idleInstance   = "instance-3"
		idleDeployment = "redis-instance-3"
	)

	env := newPreviewEnv(t)
	env.addInstance(t, idleInstance, idleDeployment, "vm-q")

	store := certinventory.NewDivergenceStore(newWaitVault(), func() time.Time { return divergedBefore })

	env.configure(t, func(d *Deps) { d.Divergence = store })

	env.credhub.setLatest(previewLeaf(waitDeployment), "v2")
	env.credhub.setLatest(previewPassword(waitDeployment), "p2")

	err := store.Update(context.Background(), previewInstance2, func(*certinventory.Divergence) (*certinventory.Divergence, error) {
		return &certinventory.Divergence{InstanceID: previewInstance2, Variables: []certinventory.DivergentVariable{{
			Name: previewLeaf(previewDeployment2), BOSHVersionID: "v1", AppliedVersionID: "v2",
			State: certinventory.DivergenceUnconverged, Since: divergedBefore,
		}}}, nil
	})
	if err != nil {
		t.Fatalf("writing the divergence record: %v", err)
	}

	preview := previewOf(t, env, PreviewRequest{Mode: RotationModeConverge, Selections: []Selection{
		{InstanceID: waitInstanceID}, {InstanceID: previewInstance2}, {InstanceID: idleInstance},
	}})

	pending, diverged, idle := preview.Instances[0], preview.Instances[1], preview.Instances[2]

	if pending.Blocked != nil || len(pending.Selected) != 0 {
		t.Fatalf("expected the instance with pending versions to be listed with nothing selected, got %+v", pending)
	}

	want := []VersionPair{
		{Name: previewLeaf(waitDeployment), DeployedVersionID: "v1", LatestVersionID: "v2"},
		{Name: previewPassword(waitDeployment), DeployedVersionID: "p1", LatestVersionID: "p2"},
	}

	if fmt.Sprint(pending.approved.Additional) != fmt.Sprint(want) || fmt.Sprint(pending.Additional) != fmt.Sprint(want) {
		t.Fatalf("expected the leaf and the password bound, got %+v and %+v", pending.Additional, pending.approved.Additional)
	}

	if diverged.Blocked != nil || len(diverged.Additional) != 0 {
		t.Fatalf("expected the diverged instance to be listed with nothing pending, got %+v", diverged)
	}

	if idle.Blocked == nil || idle.Blocked.Code != apierr.CodeNotRotatable {
		t.Fatalf("expected an instance with nothing to converge to be blocked, got %+v", idle)
	}

	task := env.manager.newCertificateTask(createRequest(preview.ID), "digest", preview)
	if len(task.Instances) != 2 || task.Certificates.Mode != RotationModeConverge {
		t.Fatalf("expected a converge task of the two listed instances, got %+v", task.Instances)
	}

	if bound := task.Instances[0].Certificates.Approved.Additional; fmt.Sprint(bound) != fmt.Sprint(want) {
		t.Fatalf("expected the task to approve both pending variables, got %+v", bound)
	}

	if holder := taskHolder(task); holder.Kind != mutation.KindConverge {
		t.Fatalf("expected a converge task to hold its instances as a converge, got %+v", holder)
	}
}

// A converge deploys the director's manifest once with the latest
// variables, reconciles the instance with a scan taken after the deploy,
// which clears the divergence record, and only then completes it.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestConvergeClearsDivergence(t *testing.T) {
	env := newConvergeEnv(t)
	env.diverge()

	manager := env.newManager()
	task := env.task()

	if !env.run(manager, &task) {
		inst := current(t, manager).Instances[0]
		t.Fatalf("settle: instance %s at phase %s: %s", inst.Status, inst.Certificates.Phase, inst.Error)
	}

	inst := current(t, manager).Instances[0]
	if inst.Status != InstanceStatusSuccess || inst.Certificates.Phase != RotationPhaseDone || inst.Error != "" {
		t.Fatalf("expected a clean success at phase done, got %s at %s: %q", inst.Status, inst.Certificates.Phase, inst.Error)
	}

	expectOneConvergeDeploy(t, env, inst)

	if env.credhub.regenerateCount() != 0 {
		t.Fatalf("expected no regeneration, got %d", env.credhub.regenerateCount())
	}

	if env.diverged() {
		t.Fatal("expected the converge to clear the divergence record")
	}

	expectReleased(t, manager)
}

// A converge whose deploy is done, but whose listener no scan observes
// yet, keeps the divergence record and completes the instance with the
// note that reconciliation waits for every listener.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestConvergeKeepsRecordUntilListenersServe(t *testing.T) {
	env := newConvergeEnv(t)
	env.diverge()
	env.hidden = true

	manager := env.newManager()
	task := env.task()

	if !env.run(manager, &task) {
		t.Fatalf("expected the instance to settle, got %+v", current(t, manager).Instances[0])
	}

	inst := current(t, manager).Instances[0]
	if inst.Status != InstanceStatusSuccess || !strings.Contains(inst.Error, "every listener") {
		t.Fatalf("expected a success that names the wait for every listener, got %s: %q", inst.Status, inst.Error)
	}

	if !env.diverged() {
		t.Fatal("expected the divergence record to stay until a scan observes the listener")
	}

	expectReleased(t, manager)
}

// A converge keeps the guard through the reconciliation that follows its
// deploy, so nothing else may change the instance until the record is
// reconciled.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestConvergeHoldsGuardUntilReconciled(t *testing.T) {
	env := newConvergeEnv(t)
	env.diverge()

	entered := make(chan struct{})
	release := make(chan struct{})

	var once sync.Once

	env.mu.Lock()
	env.gate = func() {
		once.Do(func() { close(entered) })
		<-release
	}
	env.mu.Unlock()

	manager := env.newManager()
	task := env.task()

	done := make(chan bool, 1)

	go func() { done <- env.run(manager, &task) }()

	select {
	case <-entered:
	case <-time.After(waitTimeout):
		close(release)
		t.Fatal("the converge never scanned the instance after its deploy")
	}

	inst := current(t, manager).Instances[0]
	if inst.Status != InstanceStatusRunning || inst.Certificates.Phase != RotationPhaseConverge || len(env.director.deploys()) != 1 {
		close(release)
		t.Fatalf("expected the instance running at phase converge after its deploy, got %s at %s", inst.Status, inst.Certificates.Phase)
	}

	holder, held := manager.Guard().Holder(waitInstanceID, waitDeployment)
	if !held || holder.TaskID != certTaskID || holder.Kind != mutation.KindConverge {
		close(release)
		t.Fatalf("expected the converge to hold the instance while it reconciles, got %+v held %v", holder, held)
	}

	close(release)

	select {
	case settled := <-done:
		if !settled {
			t.Fatalf("expected the instance to settle, got %+v", current(t, manager).Instances[0])
		}
	case <-time.After(waitTimeout):
		t.Fatal("the converge never finished")
	}

	if inst := current(t, manager).Instances[0]; inst.Status != InstanceStatusSuccess || env.diverged() {
		t.Fatalf("expected a success with the record cleared, got %s: %q", inst.Status, inst.Error)
	}

	expectReleased(t, manager)
}

// A variable whose new version appeared after the preview stops the
// converge before its deploy, and the instance waits for the operator to
// approve the changed effects.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestConvergeStopsOnChangedSet(t *testing.T) {
	env := newConvergeEnv(t)
	env.diverge()

	manager := env.newManager()
	task := env.task()
	// The preview found nothing pending, so the leaf's v2 is new.
	task.Instances[0].Certificates.Approved.Additional = nil

	if env.run(manager, &task) {
		t.Fatalf("expected the instance to wait for a decision, got %+v", current(t, manager).Instances[0])
	}

	got := current(t, manager)
	inst := got.Instances[0]

	if inst.Status != StatusAwaitingDecision || inst.Certificates.Pending == nil ||
		inst.Certificates.Pending.Kind != DecisionApproveChangedEffects {
		t.Fatalf("expected the instance to await approval of the changed effects, got %s %+v", inst.Status, inst.Certificates.Pending)
	}

	if !strings.Contains(strings.Join(inst.Certificates.Pending.Changes, " "), "redis_crt") {
		t.Fatalf("expected the change to name the leaf, got %v", inst.Certificates.Pending.Changes)
	}

	if !got.Paused || len(env.director.deploys()) != 0 {
		t.Fatalf("expected a paused task with no deploy sent, got paused %v and %d deploys", got.Paused, len(env.director.deploys()))
	}

	expectHeld(t, manager)
}

// A converge whose deploy was sent and saved before Blacksmith stopped is
// followed by the loader, not sent again, and is reconciled and settled
// once it is done.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestConvergeResumeAdoptsSavedDeploy(t *testing.T) {
	env := newConvergeEnv(t)
	env.diverge()

	task := env.task()
	started := validateNow

	inst := &task.Instances[0]
	inst.Status = InstanceStatusRunning
	inst.StartedAt = &started
	inst.Certificates.Phase = RotationPhaseConverge
	inst.Certificates.Converge = &ConvergeDeploy{ContextID: loadedContextID, BOSHTaskID: redeployTaskID}

	manager := env.load(task)

	settled := eventuallyInstance(t, manager, InstanceStatusSuccess).Instances[0]
	if settled.Certificates.Phase != RotationPhaseDone || settled.Error != "" {
		t.Fatalf("expected a clean success at phase done, got %s: %q", settled.Certificates.Phase, settled.Error)
	}

	if deploys := env.director.deploys(); len(deploys) != 0 {
		t.Fatalf("expected the saved deploy to be followed, not sent again, got %+v", deploys)
	}

	if waited := env.director.waited(); len(waited) == 0 || waited[0] != redeployTaskID {
		t.Fatalf("expected the loader to follow BOSH task %d, got %v", redeployTaskID, waited)
	}

	if env.scanCount() == 0 || env.diverged() {
		t.Fatalf("expected a scan to reconcile the record away, got %d scans and the record kept", env.scanCount())
	}

	expectReleased(t, manager)
}
