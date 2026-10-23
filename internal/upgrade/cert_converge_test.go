//nolint:testpackage // the tests drive the manager's unexported converge runner
package upgrade

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"slices"
	"strconv"
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
	dv    *divergenceVault

	mu    sync.Mutex
	scans int
	// hidden makes the listener's probe fail, so no scan observes it.
	hidden bool
	// listenerless gives the scan no expected listener and no probe.
	listenerless bool
	// reconcileFirst reconciles each scan with the record before the scan
	// returns, as the inventory's scheduler does.
	reconcileFirst bool
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

	dv := &divergenceVault{Vault: base.vault, base: base.vault, events: &eventLog{}}
	env := &convergeEnv{
		redeployEnv: base,
		dv:          dv,
		store:       certinventory.NewDivergenceStore(dv, func() time.Time { return divergedBefore }),
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
func (e *convergeEnv) scan(ctx context.Context, instanceID string) (certinventory.InstanceScan, error) {
	e.mu.Lock()
	e.scans++
	scannedAt := validateNow.Add(time.Duration(e.scans) * time.Second)
	hidden, listenerless, first, gate := e.hidden, e.listenerless, e.reconcileFirst, e.gate
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

	if listenerless {
		scan.Material.Expected = nil
		hidden = true
	}

	if first {
		defer func() {
			// The scheduler logs a failed reconcile and returns the scan.
			_ = e.store.ReconcileScan(ctx, scan, certinventory.ReconcileSources{Versions: e.credhub, Tasks: e.director})
		}()
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

// expectUnconfirmedConverge fails the test unless the converge failed with
// the note that its deploy finished but the record's clearing was not
// confirmed, the record stays, and the guard is freed.
func expectUnconfirmedConverge(t *testing.T, env *convergeEnv, manager *Manager, settled bool) {
	t.Helper()

	inst := current(t, manager).Instances[0]
	if !settled || inst.Status != InstanceStatusFailed || !strings.Contains(inst.Error, "could not confirm") ||
		!strings.Contains(inst.Error, strconv.Itoa(redeployTaskID)) {
		t.Fatalf("expected a failure that names the finished deploy and the unconfirmed record, got settled=%v %s: %q",
			settled, inst.Status, inst.Error)
	}

	if !env.diverged() {
		t.Fatal("expected the divergence record to stay")
	}

	expectReleased(t, manager)
}

// A converge whose deploy is done, but whose listener no scan observes,
// can't confirm that the divergence record cleared. The instance fails
// with the note that the deploy finished, the record stays, and the guard
// is freed.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestConvergeUnconfirmedWhileListenerUnseen(t *testing.T) {
	env := newConvergeEnv(t)
	env.diverge()
	env.hidden = true

	manager := env.newManager()
	task := env.task()

	expectUnconfirmedConverge(t, env, manager, env.run(manager, &task))

	if scans := env.scanCount(); scans < verifyAttempts {
		t.Fatalf("expected %d scans before the converge gave up, got %d", verifyAttempts, scans)
	}
}

// A converge of a variable no listener serves clears the record once the
// director lists its deploy as done after the patch.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestConvergeClearsWithoutListeners(t *testing.T) {
	env := newConvergeEnv(t)
	env.diverge()
	env.listenerless = true

	manager := env.newManager()
	task := env.task()

	if !env.run(manager, &task) {
		t.Fatalf("expected the instance to settle, got %+v", current(t, manager).Instances[0])
	}

	inst := current(t, manager).Instances[0]
	if inst.Status != InstanceStatusSuccess || inst.Error != "" || env.diverged() {
		t.Fatalf("expected a clean success with the record cleared, got %s: %q (diverged %v)", inst.Status, inst.Error, env.diverged())
	}

	expectReleased(t, manager)
}

// A scan that reconciles the record before the converge does, as the
// inventory's scheduler does, counts once its write is confirmed.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestConvergeConfirmsTheScannersReconcile(t *testing.T) {
	env := newConvergeEnv(t)
	env.diverge()
	env.reconcileFirst = true

	manager := env.newManager()
	task := env.task()

	if !env.run(manager, &task) {
		t.Fatalf("expected the instance to settle, got %+v", current(t, manager).Instances[0])
	}

	inst := current(t, manager).Instances[0]
	if inst.Status != InstanceStatusSuccess || inst.Error != "" || env.diverged() {
		t.Fatalf("expected a clean success with the record cleared, got %s: %q (diverged %v)", inst.Status, inst.Error, env.diverged())
	}

	expectReleased(t, manager)
}

// A scan that reconciles the record before the converge does, but whose
// write of the cleared record fails, never counts as a success. The
// converge fails with the note that its deploy finished, and the record
// stays.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestConvergeReconcileFailureKeepsRecord(t *testing.T) {
	env := newConvergeEnv(t)
	env.diverge()
	env.reconcileFirst = true

	env.dv.mu.Lock()
	env.dv.failDeletes = true
	env.dv.mu.Unlock()

	manager := env.newManager()
	task := env.task()
	settled := env.run(manager, &task)

	env.dv.mu.Lock()
	env.dv.failDeletes = false
	env.dv.mu.Unlock()

	expectUnconfirmedConverge(t, env, manager, settled)
}

// A converge deploy that BOSH fails or cancels, or that the director
// refuses, fails the instance with a note about convergence rather than
// about a redeploy, keeps the divergence record, since nothing converged,
// and frees the instance.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestConvergeDeployFailureKeepsRecord(t *testing.T) {
	cases := []struct {
		name    string
		state   string
		refused error
		message string
	}{
		{
			name:    "the task fails",
			state:   boshTaskError,
			message: fmt.Sprintf("BOSH task %d ended in state %s: compilation failed. %s", redeployTaskID, boshTaskError, convergeUnappliedNote),
		},
		{
			name:    "the task is cancelled",
			state:   boshTaskCancelled,
			message: fmt.Sprintf("BOSH task %d was cancelled. %s", redeployTaskID, convergeUnappliedNote),
		},
		{
			name:    "the director refuses the deploy",
			refused: errDirectorRefused,
			message: fmt.Sprintf("The director did not take the deploy: %v. %s", errDirectorRefused, convergeUnappliedNote),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newConvergeEnv(t)
			env.diverge()
			env.director.deployErr = tc.refused
			env.director.finish = func(_ context.Context, taskID int) (*bosh.Task, error) {
				return &bosh.Task{ID: taskID, State: tc.state, Result: "compilation failed"}, nil
			}

			manager := env.newManager()
			task := env.task()

			if !env.run(manager, &task) {
				t.Fatalf("expected the instance to settle, got %+v", current(t, manager).Instances[0])
			}

			if inst := current(t, manager).Instances[0]; inst.Status != InstanceStatusFailed || inst.Error != tc.message {
				t.Fatalf("expected the instance failed with %q, got %s: %q", tc.message, inst.Status, inst.Error)
			}

			if !env.diverged() {
				t.Fatal("expected the divergence record to stay after the failed converge deploy")
			}

			expectReleased(t, manager)
		})
	}
}

// errDirectorRefused is the error a director refuses a deploy with.
var errDirectorRefused = errors.New("the director is down for maintenance")

// awaitingConvergeOutcome returns a paused converge task whose converge
// deploy ended but whose outcome waits for an operator's decision.
func (e *convergeEnv) awaitingConvergeOutcome() UpgradeTask {
	task := e.task()
	task.Status = TaskStatusPaused
	task.Paused = true

	started := validateNow

	inst := &task.Instances[0]
	inst.Status = InstanceStatusOutcomeUnknown
	inst.StartedAt = &started
	inst.Error = "Blacksmith could not confirm the converge."
	inst.Certificates.Phase = RotationPhaseConverge
	inst.Certificates.Converge = &ConvergeDeploy{ContextID: loadedContextID, BOSHTaskID: redeployTaskID}
	inst.Certificates.Deploy = &DeployOutcome{BOSHTaskID: redeployTaskID, State: boshTaskDone, FinishedAt: validateNow}
	inst.Certificates.Pending = &PendingDecision{Kind: DecisionResolveDeployOutcome, Since: decisionSince}

	return task
}

// An operator who marks a converge deploy failed, or abandons its
// outcome, ends the instance with the note about convergence, and the
// divergence record stays.
//
//nolint:paralleltest // the manager's loader shares test seams
func TestResolveConvergeOutcomeKeepsRecord(t *testing.T) {
	cases := []struct {
		action  string
		message string
	}{
		{action: actionFailed, message: "The operator marked the deploy failed. " + convergeUnappliedNote},
		{action: actionAbandon, message: "The operator abandoned this deploy, so its outcome stays unknown. Check which certificates the deployment serves. " + convergeUnappliedNote},
	}

	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			env := newConvergeEnv(t)
			env.diverge()

			manager := env.load(env.awaitingConvergeOutcome())

			err := manager.ResolveDeployOutcome(context.Background(), certTaskID, waitInstanceID, ResolveRequest{Action: tc.action})
			if err != nil {
				t.Fatalf("ResolveDeployOutcome: %v", err)
			}

			if inst := current(t, manager).Instances[0]; inst.Error != tc.message {
				t.Fatalf("expected the instance to end with %q, got %s: %q", tc.message, inst.Status, inst.Error)
			}

			if !env.diverged() {
				t.Fatal("expected the divergence record to stay")
			}

			expectReleased(t, manager)
		})
	}
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

// patchListenerPort is the port each patched VM's listener serves on in
// the scan that follows a converge after a patch.
const patchListenerPort = "6379"

// convergedPatchScan returns the scan of a patched instance once BOSH
// deploys what the director lists: every VM's listener serves the new
// leaf, observed at the scan's start, a second after the patch.
func (e *patchEnv) convergedPatchScan(ctx context.Context, instanceID string) (certinventory.InstanceScan, error) {
	scan, err := e.patchScan(ctx, instanceID)
	if err != nil {
		return scan, err
	}

	e.director.mu.Lock()
	deployed := e.director.deployed
	e.director.mu.Unlock()

	scannedAt := validateNow.Add(time.Second)
	scan.ScannedAt, scan.LastSuccessAt = scannedAt, scannedAt
	scan.Material.CredHub[0].Versions = certinventory.Versions{
		CertificateID: convergeCertID, DeployedVersionID: deployed, LatestVersionID: "v2", SigningCA: regenCAName,
	}

	for _, machine := range scan.VMs {
		scan.Material.Expected = append(scan.Material.Expected, certinventory.ExpectedListener{
			Key: regenLeaf, Locator: fmt.Sprintf("%s/%d:%s", machine.Job, machine.Index, patchListenerPort),
		})
		scan.Material.Raw = append(scan.Material.Raw, certinventory.RawObservation{
			Observation: certinventory.Observation{
				Source:      certinventory.SourceProbe,
				Locator:     fmt.Sprintf("%s/%d %s:%s", machine.Job, machine.Index, machine.IPs[0], patchListenerPort),
				Fingerprint: certinventory.Fingerprint(e.newCert),
				State:       certinventory.StateOK,
				ObservedAt:  scannedAt,
			},
			Cert: e.newCert,
		})
	}

	return scan, nil
}

// immediateManager returns the env's manager for a patch task with the
// immediate converge policy, whose scans after the patch see every
// listener serving the new leaf.
func (e *patchEnv) immediateManager(task *UpgradeTask) *Manager {
	e.t.Helper()

	task.Certificates.Converge = ConvergeImmediate

	manager := e.newManager()
	deps := manager.Dependencies()
	deps.ScanInstance = func(ctx context.Context, instanceID string) (certinventory.InstanceScan, error) {
		if len(e.director.deploys()) == 0 {
			return e.patchScan(ctx, instanceID)
		}

		return e.convergedPatchScan(ctx, instanceID)
	}

	err := manager.Configure(deps)
	if err != nil {
		e.t.Fatalf("Configure: %v", err)
	}

	return manager
}

// expectAllDone fails the test unless every VM's journal is done.
func (e *patchEnv) expectAllDone(t *testing.T, manager *Manager) {
	t.Helper()

	for _, machine := range e.vms {
		if journal := journalOf(t, manager, machine.name()); journal.Phase != VMPhaseDone {
			t.Fatalf("expected %s done, got %s", machine.name(), journal.Phase)
		}
	}
}

// A patch task with the immediate policy deploys the director's manifest
// with the latest variables once every VM is done, under the reservation
// the patch held, reconciles the divergence record away, and only then
// completes the instance.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchImmediateConverges(t *testing.T) {
	env := newPatchEnv(t)
	task := env.task()
	manager := env.immediateManager(&task)

	var (
		heldDuringDeploy bool
		cleanedFirst     bool
	)

	serve := env.director.onDone
	env.director.onDone = func() {
		holder, held := manager.Guard().Holder(waitInstanceID, waitDeployment)
		heldDuringDeploy = held && holder.TaskID == certTaskID
		cleanedFirst = slices.Contains(env.patchOps(len(env.vms)-1), opCleanup)

		serve()
	}

	if !env.run(manager, &task) {
		t.Fatalf("expected the instance to settle, got %+v", current(t, manager).Instances[0])
	}

	inst := current(t, manager).Instances[0]
	if inst.Status != InstanceStatusSuccess || inst.Certificates.Phase != RotationPhaseDone || inst.Error != "" {
		t.Fatalf("expected a clean success at phase done, got %s at %s: %q", inst.Status, inst.Certificates.Phase, inst.Error)
	}

	env.expectAllDone(t, manager)

	deploys := env.director.deploys()
	if len(deploys) != 1 || !deploys[0].opts.ForceLatestVariables || deploys[0].manifest != env.manifest {
		t.Fatalf("expected one deploy of the unchanged manifest with the latest variables, got %+v", deploys)
	}

	if converge := inst.Certificates.Converge; converge == nil || converge.BOSHTaskID != redeployTaskID {
		t.Fatalf("expected the converge record to keep the deploy's task, got %+v", converge)
	}

	if !cleanedFirst || !heldDuringDeploy {
		t.Fatalf("expected the deploy after the last VM's cleanup with the instance still held, got cleaned=%v held=%v", cleanedFirst, heldDuringDeploy)
	}

	_, found, err := env.store.Get(context.Background(), waitInstanceID)
	if err != nil || found {
		t.Fatalf("expected the converge to clear the divergence record, got found=%v err=%v", found, err)
	}

	expectReleased(t, manager)
}

// pauseBeforeConverge runs an immediate patch whose converge deploy
// finds no manifest once the converge saved its phase, so the task pauses
// at phase converge with every VM done and no converge deploy sent.
func (e *patchEnv) pauseBeforeConverge(t *testing.T) (*Manager, UpgradeTask) {
	t.Helper()

	task := e.task()
	manager := e.immediateManager(&task)

	e.director.mu.Lock()
	e.director.onGetDeployment = func() {
		manager.mu.RLock()
		converging := task.Instances[0].Certificates.Phase == RotationPhaseConverge
		manager.mu.RUnlock()

		if converging {
			e.director.empty = true
		}
	}
	e.director.mu.Unlock()

	e.run(manager, &task)

	got := current(t, manager)
	if inst := got.Instances[0]; !got.Paused || inst.Status != InstanceStatusRunning || inst.Certificates.Phase != RotationPhaseConverge {
		t.Fatalf("expected the task paused at phase converge, got paused=%v %s at %s: %q", got.Paused, inst.Status, inst.Certificates.Phase, inst.Error)
	}

	e.expectAllDone(t, manager)

	return manager, got
}

// expectCancelledUnconverged fails the test unless the instance ended
// cancelled before its converge deploy, with no deploy sent, the record
// kept, and the instance freed.
func (e *patchEnv) expectCancelledUnconverged(t *testing.T, manager *Manager) {
	t.Helper()

	got := eventuallyInstance(t, manager, InstanceStatusCancelled)
	if inst := got.Instances[0]; got.Status != TaskStatusCancelled || inst.Error != cancelledUnconverged {
		t.Fatalf("expected the instance cancelled before its converge deploy, got %s: %q", got.Status, inst.Error)
	}

	if deploys := e.director.deploys(); len(deploys) != 0 {
		t.Fatalf("expected no converge deploy, got %+v", deploys)
	}

	_, found, err := e.store.Get(context.Background(), waitInstanceID)
	if err != nil || !found {
		t.Fatalf("expected the divergence record kept, got found=%v err=%v", found, err)
	}

	expectReleased(t, manager)
}

// A patch task with the immediate policy that is cancelled while it waits
// before its converge deploy, every VM done, ends its instance as the
// live path does when the cancel comes before the deploy: patched and
// unconverged.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchCancelledIdleBeforeConverge(t *testing.T) {
	t.Run("at phase converge", func(t *testing.T) {
		env := newPatchEnv(t)
		manager, _ := env.pauseBeforeConverge(t)

		err := manager.CancelTask(context.Background(), certTaskID)
		if err != nil {
			t.Fatalf("CancelTask: %v", err)
		}

		env.expectCancelledUnconverged(t, manager)
	})

	t.Run("before phase converge", func(t *testing.T) {
		env := newPatchEnv(t)
		_, stored := env.pauseBeforeConverge(t)

		// The converge was refused before it saved its phase, so the
		// instance is still at phase patch with every VM done.
		stored.Instances[0].Certificates.Phase = RotationPhasePatch
		stored.Instances[0].Certificates.Converge = nil
		stored.Paused = false

		env.expectCancelledUnconverged(t, env.startCancelled(t, stored, nil))
	})
}

// A patch task with the pending policy settles the instance once every
// VM is done, sends no deploy, and leaves the record unconverged for a
// later converge.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchPendingLeavesUnconverged(t *testing.T) {
	env := newPatchEnv(t)
	manager := env.newManager()
	task := env.task()
	task.Certificates.Converge = ConvergePending

	if !env.run(manager, &task) {
		t.Fatalf("expected the instance to settle, got %+v", current(t, manager).Instances[0])
	}

	inst := current(t, manager).Instances[0]
	if inst.Status != InstanceStatusSuccess || inst.Certificates.Phase != RotationPhaseDone {
		t.Fatalf("expected a success at phase done, got %s at %s: %q", inst.Status, inst.Certificates.Phase, inst.Error)
	}

	if deploys := env.director.deploys(); len(deploys) != 0 {
		t.Fatalf("expected no deploy, got %+v", deploys)
	}

	record, found, err := env.store.Get(context.Background(), waitInstanceID)
	if err != nil || !found || len(record.Variables) != 1 || record.Variables[0].State != certinventory.DivergenceUnconverged {
		t.Fatalf("expected the record to stay unconverged, got %+v found=%v err=%v", record, found, err)
	}

	expectReleased(t, manager)
}

// A patch whose VMs did not all reach done never converges, even with
// the immediate policy: not when a rollback pauses the task, and not when
// the next pass fails the instance over the rolled-back VM.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchPartialNeverConverges(t *testing.T) {
	env := newPatchEnv(t)
	env.gateFailsOn(1, nil)

	task := env.task()
	manager := env.immediateManager(&task)

	env.run(manager, &task)

	if got := current(t, manager); !got.Paused || got.Instances[0].Status.settled() {
		t.Fatalf("expected a paused task with an open instance, got paused=%v %s", got.Paused, got.Instances[0].Status)
	}

	if deploys := env.director.deploys(); len(deploys) != 0 {
		t.Fatalf("expected no deploy after the rollback, got %+v", deploys)
	}

	manager.mu.Lock()
	task.Paused = false
	manager.mu.Unlock()

	manager.rotateInstancePatch(context.Background(), &task, &task.Instances[0], make(chan struct{}))

	inst := current(t, manager).Instances[0]
	if inst.Status != InstanceStatusFailed {
		t.Fatalf("expected the next pass to fail the instance over the rolled-back VM, got %s: %s", inst.Status, inst.Error)
	}

	if deploys := env.director.deploys(); len(deploys) != 0 {
		t.Fatalf("expected no deploy, got %+v", deploys)
	}

	record, found, err := env.store.Get(context.Background(), waitInstanceID)
	if err != nil || !found || record.Variables[0].State != certinventory.DivergenceUnconverged {
		t.Fatalf("expected the record to stay unconverged, got %+v found=%v err=%v", record, found, err)
	}
}
