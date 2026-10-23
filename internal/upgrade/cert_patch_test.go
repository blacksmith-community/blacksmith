package upgrade

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/certpatch"
	"blacksmith/internal/credhub"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/upgrade/certscripts"
	"blacksmith/pkg/logger"
)

// patchCIDs are the CIDs of the test instance's three redis VMs, by
// index.
var patchCIDs = []string{"vm-cid-a", "vm-cid-b", "vm-cid-c"}

// errDivergenceDown is what the divergence vault answers while it fails.
var errDivergenceDown = errors.New("the divergence path is down")

// redisVMsDirector is the redeploy director with three redis VMs, whose
// CIDs and running tasks a test can change.
type redisVMsDirector struct {
	*redeployDirector

	pmu     sync.Mutex
	vms     []bosh.VM
	running []bosh.Task
}

func (d *redisVMsDirector) GetDeploymentVMs(context.Context, string) ([]bosh.VM, error) {
	d.pmu.Lock()
	defer d.pmu.Unlock()

	return slices.Clone(d.vms), nil
}

func (d *redisVMsDirector) FindRunningTasksForDeployment(context.Context, string) ([]bosh.Task, error) {
	d.pmu.Lock()
	defer d.pmu.Unlock()

	return slices.Clone(d.running), nil
}

func (d *redisVMsDirector) setCID(index int, cid string) {
	d.pmu.Lock()
	defer d.pmu.Unlock()

	d.vms[index].CID = cid
}

// eventLog keeps, in order, each save of the task and each command the
// fake ran, so a test can check what was saved before a command.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.events = append(l.events, event)
}

func (l *eventLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return slices.Clone(l.events)
}

// divergenceVault is the env's vault, which records each write to the
// divergence path and can fail them.
type divergenceVault struct {
	interfaces.Vault

	base   *regenVault
	events *eventLog

	mu   sync.Mutex
	fail bool
}

func (v *divergenceVault) Put(ctx context.Context, path string, data interface{}) error {
	if strings.HasPrefix(path, "certificate-divergence") {
		v.mu.Lock()
		fail := v.fail
		v.mu.Unlock()

		if fail {
			return errDivergenceDown
		}

		v.events.add("divergence")
	}

	return v.Vault.Put(ctx, path, data)
}

// Delete removes a divergence record, which the store does when no VM
// holds anything new any more.
func (v *divergenceVault) Delete(_ context.Context, path string) error {
	v.events.add("divergence-delete")

	wait := v.base.waitVault
	wait.mu.Lock()
	delete(wait.saved, path)
	wait.mu.Unlock()

	return nil
}

func (v *divergenceVault) failWrites() {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.fail = true
}

// captureLogger keeps every line the manager logs, formatted.
type captureLogger struct {
	*redeployLogger

	cmu   sync.Mutex
	lines []string
}

func (l *captureLogger) Named(string) logger.Logger { return l }

func (l *captureLogger) keep(msg string, args ...interface{}) {
	l.cmu.Lock()
	defer l.cmu.Unlock()

	l.lines = append(l.lines, fmt.Sprintf(msg, args...))
}

func (l *captureLogger) Info(msg string, args ...interface{}) {
	l.keep(msg, args...)
	l.redeployLogger.Info(msg, args...)
}

func (l *captureLogger) Debug(msg string, args ...interface{}) {
	l.keep(msg, args...)
	l.redeployLogger.Debug(msg, args...)
}

func (l *captureLogger) Warn(msg string, args ...interface{})    { l.keep(msg, args...) }
func (l *captureLogger) Error(msg string, args ...interface{})   { l.keep(msg, args...) }
func (l *captureLogger) Infof(msg string, args ...interface{})   { l.keep(msg, args...) }
func (l *captureLogger) Debugf(msg string, args ...interface{})  { l.keep(msg, args...) }
func (l *captureLogger) Warnf(msg string, args ...interface{})   { l.keep(msg, args...) }
func (l *captureLogger) Errorf(msg string, args ...interface{})  { l.keep(msg, args...) }
func (l *captureLogger) Warning(msg string, args ...interface{}) { l.keep(msg, args...) }

func (l *captureLogger) all() []string {
	l.cmu.Lock()
	defer l.cmu.Unlock()

	return slices.Clone(l.lines)
}

// patchEnv runs a patch task over an instance of three redis VMs, each
// holding the leaf's old pair, through the fake patch session.
type patchEnv struct {
	*redeployEnv

	patch    *fakePatch
	pairs    *pairCredHub
	dir      *redisVMsDirector
	divVault *divergenceVault
	store    *certinventory.DivergenceStore
	capture  *captureLogger
	events   *eventLog
	vms      []*fakeVM

	oldPair, newPair testPair
	newCert          *x509.Certificate

	// wrapSession, when set, wraps each session the runner opens.
	wrapSession func(PatchSession) PatchSession
}

func newPatchEnv(t *testing.T) *patchEnv {
	t.Helper()

	base := newRedeployEnv(t)
	base.switches = switches(true, true)

	env := &patchEnv{redeployEnv: base, patch: newFakePatch(), events: &eventLog{}}

	oldPair, oldLeaf := issuePair(t, base.ca, baseSpec())
	spec := baseSpec()
	spec.notAfter = validateNow.Add(365 * 24 * time.Hour)
	newPair, newLeaf := issuePair(t, base.ca, spec)

	env.oldPair, env.newPair, env.newCert = oldPair, newPair, newLeaf
	base.oldLeaf, base.newLeaf = oldLeaf, newLeaf

	base.credhub.mu.Lock()
	base.credhub.versions[regenLeaf] = nil
	base.credhub.meta[regenLeaf] = credhub.CertificateMeta{
		ID: "cert-1", Name: regenLeaf, SignedBy: regenCAName,
		Versions: []credhub.CertificateVersionMeta{{ID: "v1", ExpiryDate: oldLeaf.NotAfter}},
	}
	base.credhub.mu.Unlock()
	base.credhub.addVersion(regenLeaf, "v1", regenCreated, oldLeaf)
	base.credhub.answer(func(string) (*credhub.RegenerateResult, error) {
		base.credhub.addVersion(regenLeaf, "v2", regenLater, newLeaf)

		return &credhub.RegenerateResult{VersionID: "v2", Name: regenLeaf}, nil
	})

	env.pairs = newPairCredHub(base.credhub)
	env.pairs.set("v1", oldPair)
	env.pairs.set("v2", newPair)

	env.patch.gateNodes = len(patchCIDs)
	env.patch.fingerprint = certinventory.Fingerprint(newLeaf)

	env.dir = &redisVMsDirector{redeployDirector: base.director}

	for i, cid := range patchCIDs {
		vm := bosh.VM{ID: "id-" + cid, CID: cid, Job: "redis", Index: i, IPs: []string{fmt.Sprintf("192.0.2.%d", 10+i)}}
		env.dir.vms = append(env.dir.vms, vm)
		env.vms = append(env.vms, env.patch.addVM(waitDeployment, "redis", vm.ID, i,
			certpatch.NormalisedHash(oldPair.cert), certpatch.NormalisedHash(oldPair.key)))
	}

	env.divVault = &divergenceVault{Vault: base.vault, base: base.vault, events: env.events}
	env.store = certinventory.NewDivergenceStore(env.divVault, func() time.Time { return validateNow })
	env.capture = &captureLogger{redeployLogger: base.logs}

	base.vault.onSaved = func(task UpgradeTask) { env.events.add("save:" + journalSummary(task, 0)) }
	env.patch.setHook(func(call patchCall) (ssh.Result, error, bool) {
		env.events.add("run:" + call.vm + ":" + call.op)

		return ssh.Result{}, nil, false
	})

	return env
}

// journalSummary is the phase and file states of the VM's journal in a
// saved task, as "phase:state,state".
func journalSummary(task UpgradeTask, index int) string {
	if len(task.Instances) == 0 || task.Instances[0].Certificates == nil {
		return "none"
	}

	vms := task.Instances[0].Certificates.VMs
	if index >= len(vms) {
		return "none"
	}

	states := make([]string, 0, len(vms[index].Files))
	for _, file := range vms[index].Files {
		states = append(states, string(file.State))
	}

	return string(vms[index].Phase) + ":" + strings.Join(states, ",")
}

// hook answers the commands the function picks, and lets the fake answer
// the rest. It keeps the event log.
func (e *patchEnv) hook(answer func(call patchCall) (ssh.Result, error, bool)) {
	e.patch.setHook(func(call patchCall) (ssh.Result, error, bool) {
		e.events.add("run:" + call.vm + ":" + call.op)

		return answer(call)
	})
}

func (e *patchEnv) setPatchSwitch(on bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.switches = switches(true, on)
}

func (e *patchEnv) patchScan(_ context.Context, instanceID string) (certinventory.InstanceScan, error) {
	return certinventory.InstanceScan{
		Ref:        certinventory.InstanceRef{ID: instanceID, Deployment: waitDeployment},
		VMs:        slices.Clone(e.dir.vms),
		HealthGate: certscripts.GateFor("redis", len(patchCIDs)),
		Facts:      redisFacts(),
		Material: certinventory.ScanMaterial{
			CredHub:    []certinventory.CredHubCert{{Name: regenLeaf, Deployed: e.oldLeaf, Versions: certinventory.Versions{SigningCA: regenCAName}}},
			SigningCAs: map[string]*x509.Certificate{regenCAName: e.ca.cert},
		},
	}, nil
}

func (e *patchEnv) runner(ctx context.Context, deployment, group string, indices []int) (PatchSession, error) {
	sess, err := e.patch.runner(ctx, deployment, group, indices)
	if err != nil || e.wrapSession == nil {
		return sess, err
	}

	return e.wrapSession(sess), nil
}

func (e *patchEnv) newManager() *Manager {
	e.t.Helper()

	manager := NewManager(e.capture, e.dir, e.vault)
	e.t.Cleanup(manager.Stop)

	err := manager.Configure(Deps{
		CredHub:      e.pairs,
		Switches:     e.currentSwitches,
		Now:          func() time.Time { return validateNow },
		ScanInstance: e.patchScan,
		Prober:       certinventory.Prober{Timeout: 100 * time.Millisecond},
		Divergence:   e.store,
		PatchRunner:  e.runner,
	})
	if err != nil {
		e.t.Fatalf("Configure: %v", err)
	}

	manager.verifyInterval = 20 * time.Millisecond
	manager.patchGateWait = 100 * time.Millisecond
	manager.patchGateInterval = 10 * time.Millisecond

	return manager
}

// task is a confirmed patch task whose one instance has not started.
func (e *patchEnv) task() UpgradeTask {
	task := e.redeployEnv.task()
	task.Certificates = &CertificateTaskSpec{Mode: RotationModePatch, PreviewID: "pv-patch", Converge: ConvergePending}
	task.Instances[0].Certificates.Approved.VMCIDs = slices.Clone(patchCIDs)

	return task
}

// run reserves the task and runs its one instance through the patch
// runner, as the task's processor would.
func (e *patchEnv) run(manager *Manager, task *UpgradeTask) bool {
	e.t.Helper()

	err := manager.reserveCertificateTask(task)
	if err != nil {
		e.t.Fatalf("reserving the task: %v", err)
	}

	return manager.rotateInstancePatch(context.Background(), task, &task.Instances[0], make(chan struct{}))
}

// patchOps lists the patch.sh operations run on the VM, in order.
func (e *patchEnv) patchOps(vm int) []string {
	var ops []string

	for _, op := range e.patch.ops(e.vms[vm].name()) {
		switch op {
		case opCheck, opPrepare, opStage, opStageCheck, opBackup, opReplace, opRestore, opRestart, opCleanup:
			ops = append(ops, op)
		}
	}

	return ops
}

// journal returns the VM's journal as the manager holds it.
func journalOf(t *testing.T, manager *Manager, vm string) VMJournal {
	t.Helper()

	for _, journal := range current(t, manager).Instances[0].Certificates.VMs {
		if journal.VM == vm {
			return journal
		}
	}

	t.Fatalf("no journal for %s", vm)

	return VMJournal{}
}

// forwardOps is what the forward half of a patch runs on one VM.
var forwardOps = []string{
	opCheck, opCheck, opPrepare, opStage, opStage, opStageCheck, opStageCheck,
	opBackup, opBackup, opReplace, opReplace,
}

// The forward half re-checks, prepares, stages, checks the staged copies,
// backs up, and replaces each file in that order, and each replace is
// sent only after a save that shows its file replacing.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchForwardOrder(t *testing.T) {
	env := newPatchEnv(t)
	manager := env.newManager()
	task := env.task()

	env.run(manager, &task)

	ops := env.patchOps(0)
	if len(ops) < len(forwardOps) || !slices.Equal(ops[:len(forwardOps)], forwardOps) {
		t.Fatalf("expected the forward order %v, got %v", forwardOps, ops)
	}

	events := env.events.all()
	replaces := 0
	want := []string{"save:swapping:replacing,backed_up", "save:swapping:replaced,replacing"}

	for i, event := range events {
		if event != "run:"+env.vms[0].name()+":"+opReplace {
			continue
		}

		last := ""

		for j := i - 1; j >= 0; j-- {
			if strings.HasPrefix(events[j], "save:") {
				last = events[j]

				break
			}
		}

		if replaces >= len(want) || last != want[replaces] {
			t.Fatalf("replace %d followed save %q, expected %v; events %v", replaces, last, want, events)
		}

		replaces++
	}

	if replaces != 2 {
		t.Fatalf("expected two replaces, got %d", replaces)
	}

	vm := env.vms[0]
	if vm.files[vm.certPath()] != certpatch.NormalisedHash(env.newPair.cert) || vm.files[vm.keyPath()] != certpatch.NormalisedHash(env.newPair.key) {
		t.Fatal("expected both files to hold the new pair")
	}
}

// A backup that fails stops the VM before any file is replaced, leaves it
// unrecovered, and pauses the task.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchNoReplaceBeforeAllBackups(t *testing.T) {
	env := newPatchEnv(t)
	manager := env.newManager()
	task := env.task()

	backups := 0

	env.hook(func(call patchCall) (ssh.Result, error, bool) {
		if call.op != opBackup {
			return ssh.Result{}, nil, false
		}

		backups++
		if backups == 2 {
			return exitResult(certscripts.ExitDrift, "drift path="+call.args[2]), nil, true
		}

		return ssh.Result{}, nil, false
	})

	env.run(manager, &task)

	for _, call := range env.patch.recorded() {
		if call.op == opReplace {
			t.Fatalf("expected no replace after a failed backup, got %+v", call)
		}
	}

	journal := journalOf(t, manager, env.vms[0].name())
	if journal.Phase != VMPhaseUnrecovered || journal.Error == "" {
		t.Fatalf("expected the VM unrecovered with a reason, got %s %q", journal.Phase, journal.Error)
	}

	got := current(t, manager)
	if !got.Paused || got.Instances[0].Status.settled() {
		t.Fatalf("expected a paused task with an open instance, got paused=%v status=%s", got.Paused, got.Instances[0].Status)
	}

	if strings.Contains(got.Instances[0].Error, "drift path") {
		t.Fatalf("expected the error to leave out the script's output, got %q", got.Instances[0].Error)
	}
}

// The VM is written to the divergence record, as unknown, before its first
// file is replaced, and as applied once every file is. A record that
// can't be written stops the VM before any replace.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchDivergenceBeforeFirstReplace(t *testing.T) {
	t.Run("recorded first", func(t *testing.T) {
		env := newPatchEnv(t)
		manager := env.newManager()
		task := env.task()

		env.run(manager, &task)

		events := env.events.all()
		first := slices.Index(events, "divergence")
		replace := slices.Index(events, "run:"+env.vms[0].name()+":"+opReplace)

		if first < 0 || replace < 0 || first > replace {
			t.Fatalf("expected a divergence write before the first replace, got %v", events)
		}

		record, found, err := env.store.Get(context.Background(), waitInstanceID)
		if err != nil || !found || len(record.Variables) != 1 {
			t.Fatalf("expected one divergent variable, got %+v found=%v err=%v", record, found, err)
		}

		expectDivergent(t, record.Variables[0], env.newCert, certinventory.DivergentVMApplied)
	})

	t.Run("record down", func(t *testing.T) {
		env := newPatchEnv(t)
		env.divVault.failWrites()

		manager := env.newManager()
		task := env.task()

		env.run(manager, &task)

		if slices.Contains(env.patchOps(0), opReplace) {
			t.Fatalf("expected no replace without a divergence record, got %v", env.patchOps(0))
		}

		journal := journalOf(t, manager, env.vms[0].name())
		if journal.Phase != VMPhaseBackedUp || !current(t, manager).Paused {
			t.Fatalf("expected a paused task with the VM backed up, got %s paused=%v", journal.Phase, current(t, manager).Paused)
		}
	})
}

// expectDivergent checks the leaf's divergence entry and the state of the
// first VM in it.
func expectDivergent(t *testing.T, entry certinventory.DivergentVariable, applied *x509.Certificate, state string) {
	t.Helper()

	vm := entry.VMs[patchCIDs[0]]

	if entry.Name != regenLeaf || entry.BOSHVersionID != "v1" || entry.AppliedVersionID != "v2" || entry.TaskID != certTaskID ||
		entry.AppliedFingerprint != certinventory.Fingerprint(applied) {
		t.Fatalf("unexpected divergence entry %+v", entry)
	}

	if vm.State != state || vm.Group != "redis" || vm.Index != 0 {
		t.Fatalf("expected the first VM %s in the divergence record, got %+v", state, vm)
	}
}

// A save that is not confirmed sends nothing more, and leaves the
// journal as it was before the step.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchNothingSentAfterUnconfirmedSave(t *testing.T) {
	env := newPatchEnv(t)
	env.vault.refuse = func(task UpgradeTask) bool {
		return strings.HasPrefix(journalSummary(task, 0), "swapping:replacing")
	}

	manager := env.newManager()
	task := env.task()

	env.run(manager, &task)

	if slices.Contains(env.patchOps(0), opReplace) {
		t.Fatalf("expected no replace after an unconfirmed save, got %v", env.patchOps(0))
	}

	journal := journalOf(t, manager, env.vms[0].name())
	if journal.Phase != VMPhaseSwapping || journal.Files[0].State != FileStateBackedUp {
		t.Fatalf("expected the journal put back to swapping with the file backed up, got %s %s", journal.Phase, journal.Files[0].State)
	}

	if !current(t, manager).Paused {
		t.Fatal("expected the task to pause")
	}
}

// Turning patch_in_place off part way stops the next change on the VM.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchStopsWhenPatchTurnedOff(t *testing.T) {
	env := newPatchEnv(t)
	manager := env.newManager()
	task := env.task()

	env.hook(func(call patchCall) (ssh.Result, error, bool) {
		if call.op == opBackup {
			env.setPatchSwitch(false)
		}

		return ssh.Result{}, nil, false
	})

	env.run(manager, &task)

	ops := env.patchOps(0)
	if got := slices.Index(ops, opBackup); got < 0 || slices.Contains(ops[got+1:], opBackup) || slices.Contains(ops, opReplace) {
		t.Fatalf("expected one backup and no replace once patching was turned off, got %v", ops)
	}

	got := current(t, manager)
	if !got.Paused || !strings.Contains(got.Instances[0].Error, "patch") {
		t.Fatalf("expected a paused task that names the switch, got paused=%v %q", got.Paused, got.Instances[0].Error)
	}
}

// Once a VM's first file is replaced, turning patching off no longer
// stops that VM: its swap finishes, it restarts, verifies, passes the
// gate, and is cleaned up, so it never holds a new certificate beside an
// old key. The next VM is not touched.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchSwitchOffMidSwapFinishesVM(t *testing.T) {
	for _, after := range []int{1, 2} {
		t.Run(fmt.Sprintf("after replace %d", after), func(t *testing.T) {
			env := newPatchEnv(t)
			manager := env.newManager()
			task := env.task()
			first := env.vms[0].name()
			replaces := 0

			env.hook(func(call patchCall) (ssh.Result, error, bool) {
				if call.vm == first && call.op == opReplace {
					replaces++
					if replaces == after {
						env.setPatchSwitch(false)
					}
				}

				return ssh.Result{}, nil, false
			})

			env.run(manager, &task)

			vm := env.vms[0]
			if vm.files[vm.certPath()] != certpatch.NormalisedHash(env.newPair.cert) || vm.files[vm.keyPath()] != certpatch.NormalisedHash(env.newPair.key) {
				t.Fatal("expected VM 1 to hold the new pair")
			}

			if j := journalOf(t, manager, first); j.Phase != VMPhaseDone || j.Cleanup != CleanupDone {
				t.Fatalf("expected VM 1 done and cleaned up, got %s %q", j.Phase, j.Cleanup)
			}

			if ops := env.patchOps(1); slices.Contains(ops, opPrepare) {
				t.Fatalf("expected nothing changed on VM 2, got %v", ops)
			}

			if got := current(t, manager); !got.Paused {
				t.Fatalf("expected the task to pause before VM 2, got %s", got.Status)
			}
		})
	}
}

// The private key reaches the VM only on stdin, and never a command line,
// a log line, or a saved record.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchKeyNeverInCommandOrLog(t *testing.T) {
	env := newPatchEnv(t)
	manager := env.newManager()
	task := env.task()

	env.run(manager, &task)

	secrets := keyFragments(t, env.newPair.key)
	secrets = append(secrets, keyFragments(t, env.oldPair.key)...)

	var staged int

	for _, call := range env.patch.recorded() {
		text := call.line + " " + strings.Join(call.args, " ")

		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Fatalf("a command carried the private key: %s", call.op)
			}
		}

		if call.op == opStage {
			staged++

			if call.stdin <= len(env.newPair.key) {
				t.Fatalf("expected the stage to carry the script and the content on stdin, got %d bytes", call.stdin)
			}
		}
	}

	if staged == 0 {
		t.Fatal("expected a stage")
	}

	vm := env.vms[0]
	if vm.files[vm.keyPath()] != certpatch.NormalisedHash(env.newPair.key) {
		t.Fatal("expected the key file to hold the new key")
	}

	for _, line := range env.capture.all() {
		for _, secret := range secrets {
			if strings.Contains(line, secret) {
				t.Fatalf("a log line carried the private key: %q", line)
			}
		}
	}

	env.vault.script.Lock()
	records := slices.Clone(env.vault.records)
	env.vault.script.Unlock()

	for _, record := range records {
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		for _, secret := range secrets {
			if strings.Contains(string(raw), secret) {
				t.Fatal("a saved record carried the private key")
			}
		}
	}
}

// keyFragments returns pieces of a PEM key's body that no other text
// would hold.
func keyFragments(t *testing.T, key []byte) []string {
	t.Helper()

	var body []string

	for _, line := range strings.Split(string(key), "\n") {
		if line != "" && !strings.HasPrefix(line, "-----") {
			body = append(body, line)
		}
	}

	if len(body) == 0 {
		t.Fatal("the key has no body")
	}

	return body
}

// closeCheckSession checks, when it is closed, that no new pair was
// zeroed yet. The old pairs are zeroed as soon as they are hashed.
type closeCheckSession struct {
	PatchSession

	pairs  *pairCredHub
	zeroed *bool
}

func (s *closeCheckSession) Close() error {
	s.pairs.mu.Lock()

	for _, pair := range s.pairs.handed {
		if pair.VersionID == "v2" && len(pair.PrivateKey) > 0 && pair.PrivateKey[0] == 0 {
			*s.zeroed = true
		}
	}

	s.pairs.mu.Unlock()

	return s.PatchSession.Close()
}

// Every pair read is zeroed once the instance ends, after the sessions
// closed, whether it ended well, failed, or panicked.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchZeroesKeyPairs(t *testing.T) {
	cases := map[string]func(env *patchEnv){
		"ends":  func(*patchEnv) {},
		"fails": func(env *patchEnv) { env.failOp(opReplace) },
		"panics": func(env *patchEnv) {
			env.hook(func(call patchCall) (ssh.Result, error, bool) {
				if call.op == opReplace {
					panic("the session broke")
				}

				return ssh.Result{}, nil, false
			})
		},
	}

	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			env := newPatchEnv(t)
			setup(env)

			zeroedBeforeClose := false
			env.wrapSession = func(sess PatchSession) PatchSession {
				return &closeCheckSession{PatchSession: sess, pairs: env.pairs, zeroed: &zeroedBeforeClose}
			}

			manager := env.newManager()
			task := env.task()

			func() {
				defer func() { _ = recover() }()

				env.run(manager, &task)
			}()

			env.pairs.expectAllZeroed(t, name)

			if zeroedBeforeClose {
				t.Fatal("a pair was zeroed before its session closed")
			}

			if env.patch.closeCount() != env.patch.openCount() {
				t.Fatalf("expected every session closed, opened %d closed %d", env.patch.openCount(), env.patch.closeCount())
			}
		})
	}
}

// failOp makes every run of the operation exit with the drift status.
func (e *patchEnv) failOp(op string) {
	e.hook(func(call patchCall) (ssh.Result, error, bool) {
		if call.op == op {
			return exitResult(certscripts.ExitDrift), nil, true
		}

		return ssh.Result{}, nil, false
	})
}

// The re-check before a VM is changed stops it when a file drifted or the
// VM was recreated, and sends nothing that changes it.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchRecheckDriftStopsVM(t *testing.T) {
	t.Run("file drifted", recheckFileDrifted)
	t.Run("VM recreated", recheckVMRecreated)
	t.Run("BOSH task running", recheckTaskRunning)
}

// recheckFileDrifted drifts a file during the re-check.
func recheckFileDrifted(t *testing.T) {
	env := newPatchEnv(t)
	manager := env.newManager()
	task := env.task()

	vm := env.vms[0]

	env.hook(func(call patchCall) (ssh.Result, error, bool) {
		if call.op == opCheck {
			env.patch.mu.Lock()
			vm.files[vm.keyPath()] = "sha256:someone-else"
			env.patch.mu.Unlock()
		}

		return ssh.Result{}, nil, false
	})

	env.run(manager, &task)

	if ops := env.patchOps(0); slices.Contains(ops, opPrepare) || slices.Contains(ops, opStage) {
		t.Fatalf("expected nothing sent after the drift, got %v", ops)
	}

	journal := journalOf(t, manager, vm.name())
	if journal.Phase != VMPhaseUnrecovered || journal.Error == "" || !current(t, manager).Paused {
		t.Fatalf("expected the VM unrecovered and the task paused, got %s %q paused=%v", journal.Phase, journal.Error, current(t, manager).Paused)
	}
}

// recheckVMRecreated recreates the first VM once the patch phase is saved.
func recheckVMRecreated(t *testing.T) {
	env := newPatchEnv(t)
	manager := env.newManager()
	task := env.task()

	once := sync.Once{}
	env.vault.onSaved = func(saved UpgradeTask) {
		if saved.Instances[0].Certificates.Phase == RotationPhasePatch {
			once.Do(func() { env.dir.setCID(0, "vm-cid-new") })
		}
	}

	env.run(manager, &task)

	if ops := env.patchOps(0); len(ops) != 0 {
		t.Fatalf("expected nothing run on a recreated VM, got %v", ops)
	}

	journal := journalOf(t, manager, env.vms[0].name())
	if journal.Phase != VMPhaseReverted || !current(t, manager).Paused {
		t.Fatalf("expected the VM reverted and the task paused, got %s paused=%v", journal.Phase, current(t, manager).Paused)
	}
}

// recheckTaskRunning starts a BOSH task once the patch phase is saved.
func recheckTaskRunning(t *testing.T) {
	env := newPatchEnv(t)
	manager := env.newManager()
	task := env.task()

	env.vault.onSaved = func(saved UpgradeTask) {
		if saved.Instances[0].Certificates.Phase == RotationPhasePatch {
			env.dir.pmu.Lock()
			env.dir.running = []bosh.Task{{ID: 99}}
			env.dir.pmu.Unlock()
		}
	}

	env.run(manager, &task)

	if ops := env.patchOps(0); len(ops) != 0 {
		t.Fatalf("expected nothing run while a BOSH task runs, got %v", ops)
	}

	got := current(t, manager)
	if !got.Paused || !strings.Contains(got.Instances[0].Error, "99") {
		t.Fatalf("expected a paused task that names BOSH task 99, got paused=%v %q", got.Paused, got.Instances[0].Error)
	}
}

// Every command on the instance's VMs goes through one session per
// instance group, opened once and closed once.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchOneSessionPerGroup(t *testing.T) {
	env := newPatchEnv(t)
	manager := env.newManager()
	task := env.task()

	env.run(manager, &task)

	if env.patch.openCount() != 1 || env.patch.closeCount() != 1 {
		t.Fatalf("expected one session opened and closed, got %d and %d", env.patch.openCount(), env.patch.closeCount())
	}

	for _, call := range env.patch.recorded() {
		if call.session != 1 {
			t.Fatalf("expected every command on session 1, got %+v", call)
		}
	}
}

// afterDiscovery lists the operations run on the VM after its discovery
// and the gate discovery runs, which are the gate, verify.sh, and
// patch.sh operations of the patch.
func (e *patchEnv) afterDiscovery(vm int) []string {
	ops := e.patch.ops(e.vms[vm].name())

	last := -1

	for i, op := range ops {
		if op == opDiscover {
			last = i
		}
	}

	ops = ops[last+1:]
	if len(ops) > 0 && ops[0] == certscripts.GateRedisCluster {
		ops = ops[1:]
	}

	return ops
}

// gateFailsOn makes the health gate fail on the VM from its first restart
// until a file is restored on it.
func (e *patchEnv) gateFailsOn(vm int, also func(call patchCall) (ssh.Result, error, bool)) {
	name := e.vms[vm].name()
	restarted, restored := false, false

	e.hook(func(call patchCall) (ssh.Result, error, bool) {
		if call.vm == name {
			switch call.op {
			case opRestart:
				restarted = true
			case opRestore:
				restored = true
			case certscripts.GateRedisCluster:
				if restarted && !restored {
					return exitResult(1), nil, true
				}
			}
		}

		if also != nil {
			return also(call)
		}

		return ssh.Result{}, nil, false
	})
}

// finishOps is what the finish of a patch runs on one VM after its
// replacements.
var finishOps = []string{opRestart, verifyScript, certscripts.GateRedisCluster, opCleanup}

// Each VM is patched in turn: forward, restart, verify, gate, and cleanup.
// Every VM ends done and applied in the divergence record.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchHappyPathOrder(t *testing.T) {
	env := newPatchEnv(t)
	manager := env.newManager()
	task := env.task()

	if !env.run(manager, &task) {
		t.Fatalf("expected the instance to settle, got %+v", current(t, manager).Instances[0])
	}

	for i := range env.vms {
		want := slices.Concat(forwardOps, finishOps)
		if i == 0 {
			want = slices.Concat([]string{certscripts.GateRedisCluster}, want)
		}

		if got := env.afterDiscovery(i); !slices.Equal(got, want) {
			t.Fatalf("VM %d: expected %v, got %v", i, want, got)
		}

		journal := journalOf(t, manager, env.vms[i].name())
		if journal.Phase != VMPhaseDone || journal.Cleanup != CleanupDone || journal.GateAfter != "passed" {
			t.Fatalf("VM %d: expected done and clean with the gate passed, got %s %q %q", i, journal.Phase, journal.Cleanup, journal.GateAfter)
		}

		if len(journal.Targets) == 0 || !journal.Targets[0].OK {
			t.Fatalf("VM %d: expected a passing target, got %+v", i, journal.Targets)
		}
	}

	inst := current(t, manager).Instances[0]
	if inst.Status != InstanceStatusSuccess {
		t.Fatalf("expected the instance to succeed, got %s: %s", inst.Status, inst.Error)
	}

	record, found, err := env.store.Get(context.Background(), waitInstanceID)
	if err != nil || !found || len(record.Variables) != 1 {
		t.Fatalf("expected one divergent variable, got %+v found=%v err=%v", record, found, err)
	}

	for _, cid := range patchCIDs {
		if state := record.Variables[0].VMs[cid].State; state != certinventory.DivergentVMApplied {
			t.Fatalf("expected %s applied, got %q", cid, state)
		}
	}
}

// A gate that fails after VM 2's restart restores both its files,
// restarts it, and runs the gate again. VM 2 ends rolled back and old in
// the record, VM 1 stays done and applied, and the task pauses.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchRollbackOnGateFailure(t *testing.T) {
	env := newPatchEnv(t)
	env.gateFailsOn(1, nil)

	manager := env.newManager()
	task := env.task()

	env.run(manager, &task)

	ops := env.afterDiscovery(1)
	first := slices.Index(ops, opRestore)

	if first < 0 || !slices.Equal(ops[first:], []string{opRestore, opRestore, opRestart, certscripts.GateRedisCluster, opCleanup}) {
		t.Fatalf("expected restore twice, a restart, the gate, and cleanup, got %v", ops)
	}

	vm := env.vms[1]
	if vm.files[vm.certPath()] != certpatch.NormalisedHash(env.oldPair.cert) || vm.files[vm.keyPath()] != certpatch.NormalisedHash(env.oldPair.key) {
		t.Fatal("expected VM 2 to hold the old pair again")
	}

	second := journalOf(t, manager, vm.name())
	if second.Phase != VMPhaseRolledBack || second.Files[0].State != FileStateRestored || second.Files[1].State != FileStateRestored {
		t.Fatalf("expected VM 2 rolled back with both files restored, got %s %+v", second.Phase, second.Files)
	}

	if first := journalOf(t, manager, env.vms[0].name()); first.Phase != VMPhaseDone {
		t.Fatalf("expected VM 1 to stay done, got %s", first.Phase)
	}

	if ops := env.afterDiscovery(2); len(ops) != 0 {
		t.Fatalf("expected nothing run on VM 3, got %v", ops)
	}

	got := current(t, manager)
	if !got.Paused || got.Instances[0].Status.settled() {
		t.Fatalf("expected a paused task with an open instance, got paused=%v %s", got.Paused, got.Instances[0].Status)
	}

	env.expectRolledBackRecord(t)
}

// expectRolledBackRecord checks that the divergence record shows VM 1
// on the new certificate, VM 2 back on the old one, and the applied
// fingerprint kept.
func (e *patchEnv) expectRolledBackRecord(t *testing.T) {
	t.Helper()

	record, _, err := e.store.Get(context.Background(), waitInstanceID)
	if err != nil || record == nil {
		t.Fatalf("expected a divergence record, got %v", err)
	}

	vms := record.Variables[0].VMs
	if vms[patchCIDs[0]].State != certinventory.DivergentVMApplied || vms[patchCIDs[1]].State != certinventory.DivergentVMOld {
		t.Fatalf("expected VM 1 applied and VM 2 old, got %+v", vms)
	}

	if record.Variables[0].AppliedFingerprint != certinventory.Fingerprint(e.newCert) {
		t.Fatal("expected the rollback to keep the applied fingerprint")
	}
}

// Once a rollback starts, nothing on that VM is staged or replaced again.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchRollbackNeverReplaces(t *testing.T) {
	env := newPatchEnv(t)
	env.patch.fingerprint = certinventory.Fingerprint(env.oldLeaf)

	manager := env.newManager()
	task := env.task()

	env.run(manager, &task)

	ops := env.patchOps(0)
	first := slices.Index(ops, opRestore)

	if first < 0 {
		t.Fatalf("expected a failed verification to roll VM 1 back, got %v", ops)
	}

	for _, op := range ops[first:] {
		if op == opStage || op == opReplace || op == opPrepare || op == opBackup {
			t.Fatalf("expected no stage or replace after the rollback started, got %v", ops)
		}
	}

	journal := journalOf(t, manager, env.vms[0].name())
	if journal.Phase != VMPhaseRolledBack || !strings.Contains(journal.Error, "different certificate") {
		t.Fatalf("expected VM 1 rolled back for its verification, got %s %q", journal.Phase, journal.Error)
	}
}

// A restore that fails leaves the VM unrecovered with its backups, and
// fails the instance with the redeploy advice.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchUnrecoveredWhenRestoreFails(t *testing.T) {
	env := newPatchEnv(t)
	env.gateFailsOn(0, func(call patchCall) (ssh.Result, error, bool) {
		if call.op == opRestore {
			return exitResult(certscripts.ExitDrift, "unrecovered path="+call.args[2]), nil, true
		}

		return ssh.Result{}, nil, false
	})

	manager := env.newManager()
	task := env.task()

	env.run(manager, &task)

	journal := journalOf(t, manager, env.vms[0].name())
	if journal.Phase != VMPhaseUnrecovered {
		t.Fatalf("expected VM 1 unrecovered, got %s", journal.Phase)
	}

	inst := current(t, manager).Instances[0]
	if inst.Status != InstanceStatusFailed || !strings.Contains(inst.Error, redeployToRepair) {
		t.Fatalf("expected a failed instance with the redeploy advice, got %s %q", inst.Status, inst.Error)
	}

	ops := env.patchOps(0)
	if slices.Contains(ops[slices.Index(ops, opRestore):], opCleanup) {
		t.Fatalf("expected the backups kept, got %v", ops)
	}

	if len(env.vms[0].backups) != 2 {
		t.Fatalf("expected both backups on the VM, got %d", len(env.vms[0].backups))
	}
}

// A cleanup that fails marks the journal and leaves the VM done.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchCleanupFailureKeepsDone(t *testing.T) {
	env := newPatchEnv(t)
	name := env.vms[0].name()

	env.hook(func(call patchCall) (ssh.Result, error, bool) {
		if call.vm == name && call.op == opCleanup {
			return exitResult(1), nil, true
		}

		return ssh.Result{}, nil, false
	})

	manager := env.newManager()
	task := env.task()

	env.run(manager, &task)

	journal := journalOf(t, manager, name)
	if journal.Phase != VMPhaseDone || journal.Cleanup != CleanupFailed {
		t.Fatalf("expected VM 1 done with a failed cleanup, got %s %q", journal.Phase, journal.Cleanup)
	}

	if inst := current(t, manager).Instances[0]; inst.Status != InstanceStatusSuccess {
		t.Fatalf("expected the instance to succeed, got %s: %s", inst.Status, inst.Error)
	}
}

// A pause asked for during VM 1's restart lets VM 1 finish, and the task
// pauses before VM 2 starts.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchPauseBetweenVMsOnly(t *testing.T) {
	env := newPatchEnv(t)
	manager := env.newManager()
	task := env.task()
	name := env.vms[0].name()

	env.hook(func(call patchCall) (ssh.Result, error, bool) {
		if call.vm == name && call.op == opRestart {
			err := manager.PauseTask(context.Background(), certTaskID)
			if err != nil {
				t.Errorf("pausing the task: %v", err)
			}
		}

		return ssh.Result{}, nil, false
	})

	if env.run(manager, &task) {
		t.Fatal("expected the instance to stay open")
	}

	journal := journalOf(t, manager, name)
	if journal.Phase != VMPhaseDone || journal.Cleanup != CleanupDone {
		t.Fatalf("expected VM 1 done and clean before the pause, got %s %q", journal.Phase, journal.Cleanup)
	}

	if ops := env.afterDiscovery(1); len(ops) != 0 {
		t.Fatalf("expected nothing run on VM 2, got %v", ops)
	}

	if second := journalOf(t, manager, env.vms[1].name()); second.Phase != VMPhasePlanned {
		t.Fatalf("expected VM 2 still planned, got %s", second.Phase)
	}

	if !current(t, manager).Paused {
		t.Fatal("expected the task to pause")
	}
}

// The forward, finish, and rollback commands of every VM go through the
// one session the instance opened.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchFinishAndRollbackShareSession(t *testing.T) {
	env := newPatchEnv(t)
	env.gateFailsOn(1, nil)

	manager := env.newManager()
	task := env.task()

	env.run(manager, &task)

	if env.patch.openCount() != 1 || env.patch.closeCount() != 1 {
		t.Fatalf("expected one session opened and closed, got %d and %d", env.patch.openCount(), env.patch.closeCount())
	}

	seen := make(map[string]bool)

	for _, call := range env.patch.recorded() {
		if call.session != 1 {
			t.Fatalf("expected every command on session 1, got %+v", call)
		}

		seen[call.op] = true
	}

	for _, op := range []string{opReplace, opRestart, verifyScript, opRestore, opCleanup} {
		if !seen[op] {
			t.Fatalf("expected %s on the session, saw %v", op, seen)
		}
	}
}

// Cancelling a task that paused after a rollback ends its instance and
// frees it, since nothing else follows the instance once its processor
// stopped.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchCancelAfterRollbackPauseEndsInstance(t *testing.T) {
	env := newPatchEnv(t)
	env.gateFailsOn(1, nil)

	manager := env.newManager()
	task := env.task()

	env.run(manager, &task)

	if got := current(t, manager); !got.Paused {
		t.Fatalf("expected the rollback to pause the task, got %s", got.Status)
	}

	err := manager.CancelTask(context.Background(), certTaskID)
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	got := eventuallyInstance(t, manager, InstanceStatusCancelled)
	if got.Status != TaskStatusCancelled || got.Instances[0].Error != cancelledBetweenVMs {
		t.Fatalf("expected a cancelled task whose instance ended between VMs, got %s %q", got.Status, got.Instances[0].Error)
	}

	expectReleased(t, manager)
}

// A journal that refuses the move to unrecovered keeps its phase, and the
// refusal is logged rather than dropped.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchLogsRefusedUnrecoveredMove(t *testing.T) {
	env := newPatchEnv(t)
	manager := env.newManager()
	task := env.task()

	env.run(manager, &task)

	j := &task.Instances[0].Certificates.VMs[0]
	if j.Phase != VMPhaseDone {
		t.Fatalf("expected VM 1 done, got %s", j.Phase)
	}

	manager.unrecovered(context.Background(), &task, &task.Instances[0], j, "a test asked") //nolint:errcheck,gosec // the test reads the log

	if j.Phase != VMPhaseDone {
		t.Fatalf("expected the refused move to keep the phase, got %s", j.Phase)
	}

	if !slices.ContainsFunc(env.capture.all(), func(line string) bool {
		return strings.Contains(line, "unrecovered") && strings.Contains(line, string(VMPhaseDone)+" to "+string(VMPhaseUnrecovered))
	}) {
		t.Fatalf("expected the refused move logged, got %v", env.capture.all())
	}
}
