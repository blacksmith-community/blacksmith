package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/certpatch"
	"blacksmith/internal/upgrade/certscripts"
)

// crashSentinel is what a simulated crash panics with.
type crashSentinel struct{}

// errListingDown is what the director answers while its VM listing fails.
var errListingDown = errors.New("the director's VM listing is down")

// resumeBoundary is one point at which the first run of a patch crashes
// on VM 1: after the nth run of op on it, after the nth write of the
// divergence record, or at the first save whose summary of VM 1's journal
// starts with saved. With afterSave the crash comes at the first save
// after that run or write instead.
type resumeBoundary struct {
	name       string
	rollback   bool
	op         string
	nth        int
	divergence int
	saved      string
	afterSave  bool
}

// resumeBoundaries lists every boundary of the forward pass, its finish,
// and its rollback on VM 1. Each command that changes the VM and each
// divergence write crashes once before the save that follows it and once
// after.
func resumeBoundaries() []resumeBoundary {
	var out []resumeBoundary

	both := func(b resumeBoundary) {
		before, after := b, b
		before.name = "after " + b.name + " before its save"
		after.name = "after " + b.name + " and its save"
		after.afterSave = true
		out = append(out, before, after)
	}

	both(resumeBoundary{name: "prepare", op: opPrepare, nth: 1})
	both(resumeBoundary{name: "stage 1", op: opStage, nth: 1})
	both(resumeBoundary{name: "stage 2", op: opStage, nth: 2})
	both(resumeBoundary{name: "backup 1", op: opBackup, nth: 1})
	both(resumeBoundary{name: "the last backup", op: opBackup, nth: 2})
	both(resumeBoundary{name: "the unknown divergence write", divergence: 1})
	out = append(out, resumeBoundary{name: "replacing saved before the rename", saved: "swapping:replacing,"})
	both(resumeBoundary{name: "the first rename", op: opReplace, nth: 1})
	both(resumeBoundary{name: "the last rename", op: opReplace, nth: 2})
	both(resumeBoundary{name: "the applied divergence write", divergence: 2})
	both(resumeBoundary{name: "the restart", op: opRestart, nth: 1})
	// The gate runs on VM 1 at discovery, before the pass, and after the
	// restart, which is the third run.
	both(resumeBoundary{name: "the gate after the restart", op: certscripts.GateRedisCluster, nth: 3})
	out = append(out, resumeBoundary{name: "done saved before cleanup", saved: "done:"})
	both(resumeBoundary{name: "cleanup", op: opCleanup, nth: 1})

	out = append(out, resumeBoundary{name: "rolling_back saved before any restore", rollback: true, saved: "rolling_back:replaced,replaced"})
	both(resumeBoundary{name: "restore 1", rollback: true, op: opRestore, nth: 1})
	both(resumeBoundary{name: "the last restore", rollback: true, op: opRestore, nth: 2})
	both(resumeBoundary{name: "the rollback restart", rollback: true, op: opRestart, nth: 2})
	both(resumeBoundary{name: "the rollback cleanup", rollback: true, op: opCleanup, nth: 1})

	return out
}

// crasher crashes the first run of a patch at one boundary, and watches
// every file of every VM for a hash that is neither the old nor the new
// one.
type crasher struct {
	env *patchEnv
	b   resumeBoundary
	vm  string

	mu      sync.Mutex
	armed   bool
	seen    int
	writes  int
	pending bool
	fired   bool
	third   string
}

// crashAt arms a crash at the boundary for the next run.
func crashAt(env *patchEnv, b resumeBoundary) *crasher {
	c := &crasher{env: env, b: b, vm: env.vms[0].name(), armed: true}

	env.wrapSession = func(sess PatchSession) PatchSession { return &crashSession{PatchSession: sess, c: c} }

	keep := env.vault.onSaved
	env.vault.onSaved = func(task UpgradeTask) {
		keep(task)
		c.saved(task)
	}

	env.divVault.mu.Lock()
	env.divVault.afterPut = func(string) { c.wrote() }
	env.divVault.mu.Unlock()

	return c
}

// fire crashes. The caller holds mu, which fire lets go of.
func (c *crasher) fire() {
	c.armed = false
	c.fired = true
	c.mu.Unlock()

	panic(crashSentinel{})
}

// ran sees each command once the VM ran it.
func (c *crasher) ran(vm, op string) {
	c.checkThird()

	c.mu.Lock()

	if !c.armed || vm != c.vm || op != c.b.op {
		c.mu.Unlock()

		return
	}

	c.seen++

	switch {
	case c.seen != c.b.nth:
	case c.b.afterSave:
		c.pending = true
	default:
		c.fire()
	}

	c.mu.Unlock()
}

// wrote sees each write of the divergence record once it is stored.
func (c *crasher) wrote() {
	c.mu.Lock()

	if !c.armed || c.b.divergence == 0 {
		c.mu.Unlock()

		return
	}

	c.writes++

	switch {
	case c.writes != c.b.divergence:
	case c.b.afterSave:
		c.pending = true
	default:
		c.fire()
	}

	c.mu.Unlock()
}

// saved sees each save of the task once it is stored.
func (c *crasher) saved(task UpgradeTask) {
	c.mu.Lock()

	if c.armed && (c.pending || (c.b.saved != "" && strings.HasPrefix(journalSummary(task, 0), c.b.saved))) {
		c.fire()
	}

	c.mu.Unlock()
}

// checkThird records the first file that holds a third hash.
func (c *crasher) checkThird() {
	allowed := map[string]bool{
		certpatch.NormalisedHash(c.env.oldPair.cert): true,
		certpatch.NormalisedHash(c.env.oldPair.key):  true,
		certpatch.NormalisedHash(c.env.newPair.cert): true,
		certpatch.NormalisedHash(c.env.newPair.key):  true,
	}

	c.env.patch.mu.Lock()

	third := ""

	for _, vm := range c.env.vms {
		for path, hash := range vm.files {
			if !allowed[hash] && third == "" {
				third = vm.name() + ":" + path
			}
		}
	}

	c.env.patch.mu.Unlock()

	if third != "" {
		c.mu.Lock()
		if c.third == "" {
			c.third = third
		}
		c.mu.Unlock()
	}
}

// expectNoThird fails when any file held a third hash.
func (c *crasher) expectNoThird(t *testing.T) {
	t.Helper()

	c.checkThird()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.third != "" {
		t.Fatalf("expected every file to hold the old or the new content, but %s held neither", c.third)
	}
}

// crashSession tells the crasher of each command its session ran.
type crashSession struct {
	PatchSession

	c *crasher
}

func (s *crashSession) Run(ctx context.Context, vm int, op string, args []string, stdin io.Reader) (ssh.Result, error) {
	hosts := s.VMs()
	result, err := s.PatchSession.Run(ctx, vm, op, args, stdin)

	if vm >= 0 && vm < len(hosts) {
		s.c.ran(hosts[vm].Job+"/"+hosts[vm].IndexOrID, op)
	}

	return result, err
}

// crashRun runs the patch in a first manager until the boundary crashes
// it, and returns the task as the vault holds it then. The first manager
// is left as the crash left it.
func (c *crasher) crashRun(t *testing.T) UpgradeTask {
	t.Helper()

	first := c.env.newManager()
	task := c.env.task()

	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(crashSentinel); !ok {
					panic(r)
				}
			}
		}()

		c.env.run(first, &task)
	}()

	c.mu.Lock()
	fired := c.fired
	c.mu.Unlock()

	if !fired {
		t.Fatalf("the run never reached the boundary %q", c.b.name)
	}

	return c.env.stored()
}

// reload starts a new manager that loads the stored task and waits until
// its resume stops: the instance settled, waits for a decision, or the
// task paused.
func (e *patchEnv) reload(t *testing.T) (*Manager, UpgradeTask) {
	t.Helper()

	second := e.newManager()
	second.Start()
	waitForLoad(t, second)

	return second, waitResumed(t, second)
}

// waitResumed waits until the resume of the test instance stops.
func waitResumed(t *testing.T, manager *Manager) UpgradeTask {
	t.Helper()

	waitUntil(t, "the resume to stop", func() bool {
		got := current(t, manager)
		inst := got.Instances[0]

		return got.Paused || inst.Status.settled() || waitsForDecision(&inst)
	})

	return current(t, manager)
}

// opsSince lists the operations run on the VM from the recorded call at
// mark on.
func (e *patchEnv) opsSince(mark, vm int) []string {
	var ops []string

	for _, call := range e.patch.recorded()[mark:] {
		if call.vm == e.vms[vm].name() {
			ops = append(ops, call.op)
		}
	}

	return ops
}

// storedJournal is VM 1's journal in a stored task.
func storedJournal(task UpgradeTask) VMJournal {
	return task.Instances[0].Certificates.VMs[0]
}

// divergenceJSON is the stored divergence record as JSON, or "none".
func (e *patchEnv) divergenceJSON(t *testing.T) string {
	t.Helper()

	record, found, err := e.store.Get(context.Background(), waitInstanceID)
	if err != nil {
		t.Fatalf("reading the divergence record: %v", err)
	}

	if !found {
		return "none"
	}

	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("encoding the divergence record: %v", err)
	}

	return string(raw)
}

// vmState is the VM's state in the divergence record, or "" when the
// record does not list it.
func (e *patchEnv) vmState(t *testing.T, cid string) string {
	t.Helper()

	record, _, err := e.store.Get(context.Background(), waitInstanceID)
	if err != nil {
		t.Fatalf("reading the divergence record: %v", err)
	}

	if record == nil || len(record.Variables) == 0 {
		return ""
	}

	return record.Variables[0].VMs[cid].State
}

// expectPatched checks that every VM is done and clean, holds the new
// pair, and is applied in the divergence record, and that the instance
// succeeded and was released.
func (e *patchEnv) expectPatched(t *testing.T, manager *Manager, got UpgradeTask) {
	t.Helper()

	inst := got.Instances[0]
	if inst.Status != InstanceStatusSuccess {
		t.Fatalf("expected the instance to succeed, got %s: %s", inst.Status, inst.Error)
	}

	for i, vm := range e.vms {
		j := inst.Certificates.VMs[i]
		if j.Phase != VMPhaseDone || j.Cleanup != CleanupDone {
			t.Fatalf("expected VM %d done and clean, got %s %q", i+1, j.Phase, j.Cleanup)
		}

		e.patch.mu.Lock()
		cert, key := vm.files[vm.certPath()], vm.files[vm.keyPath()]
		e.patch.mu.Unlock()

		if cert != certpatch.NormalisedHash(e.newPair.cert) || key != certpatch.NormalisedHash(e.newPair.key) {
			t.Fatalf("expected VM %d to hold the new pair", i+1)
		}

		if state := e.vmState(t, patchCIDs[i]); state != certinventory.DivergentVMApplied {
			t.Fatalf("expected VM %d applied in the divergence record, got %q", i+1, state)
		}
	}

	expectReleased(t, manager)
}

// expectRolledBack checks that VM 1 is rolled back and clean, holds the
// old pair, is old or gone from the divergence record, and that nothing
// was staged or replaced on it since mark and nothing at all ran on the
// other VMs.
func (e *patchEnv) expectRolledBack(t *testing.T, got UpgradeTask, mark int) {
	t.Helper()

	inst := got.Instances[0]
	j := inst.Certificates.VMs[0]

	if j.Phase != VMPhaseRolledBack || j.Cleanup != CleanupDone {
		t.Fatalf("expected VM 1 rolled back and clean, got %s %q (%s)", j.Phase, j.Cleanup, inst.Error)
	}

	if inst.Status == InstanceStatusSuccess {
		t.Fatal("expected the rolled back instance not to succeed")
	}

	vm := e.vms[0]

	e.patch.mu.Lock()
	cert, key := vm.files[vm.certPath()], vm.files[vm.keyPath()]
	e.patch.mu.Unlock()

	if cert != certpatch.NormalisedHash(e.oldPair.cert) || key != certpatch.NormalisedHash(e.oldPair.key) {
		t.Fatal("expected VM 1 to hold the old pair again")
	}

	if state := e.vmState(t, patchCIDs[0]); state != "" && state != certinventory.DivergentVMOld {
		t.Fatalf("expected VM 1 old in the divergence record, got %q", state)
	}

	ops := e.opsSince(mark, 0)
	if slices.Contains(ops, opReplace) || slices.Contains(ops, opStage) {
		t.Fatalf("expected no stage or replace on VM 1 after the crash, got %v", ops)
	}

	for i := 1; i < len(e.vms); i++ {
		if ops := e.opsSince(mark, i); len(ops) != 0 {
			t.Fatalf("expected nothing run on VM %d after the crash, got %v", i+1, ops)
		}
	}
}

// boundaryEnv is a patch environment for the boundary, crashed there.
func boundaryEnv(t *testing.T, b resumeBoundary) (*patchEnv, *crasher, UpgradeTask) {
	t.Helper()

	env := newPatchEnv(t)
	if b.rollback {
		env.gateFailsOn(0, nil)
	}

	c := crashAt(env, b)

	return env, c, c.crashRun(t)
}

// A crash at each boundary of a VM's patch, before and after the save
// that follows its command, resumes in a new manager to the one end the
// boundary has. A forward boundary ends with every VM done and the
// instance patched, and a rollback boundary ends with VM 1 rolled back
// and nothing replaced again. A VM recreated meanwhile ends reverted
// with the task paused, and patch in place turned off meanwhile changes
// nothing on a VM whose swap had not begun. No file ever holds a third
// hash.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchResumeAtEveryBoundary(t *testing.T) {
	for _, b := range resumeBoundaries() {
		t.Run(b.name, func(t *testing.T) {
			env, c, stored := boundaryEnv(t, b)
			mark := len(env.patch.recorded())

			second, got := env.reload(t)

			if b.rollback {
				env.expectRolledBack(t, got, mark)
			} else {
				env.expectPatched(t, second, got)
			}

			env.expectNoMix(t)
			c.expectNoThird(t)

			if b.op == opBackup && b.nth == 2 {
				expectBackupsReused(t, env, stored, mark)
			}
		})

		t.Run(b.name+" with the VM recreated", func(t *testing.T) { resumeRecreated(t, b) })

		t.Run(b.name+" with patch in place turned off", func(t *testing.T) {
			env, c, stored := boundaryEnv(t, b)
			env.setPatchSwitch(false)

			opens := env.patch.openCount()
			before := env.divergenceJSON(t)
			pair := env.pairState(0)

			_, got := env.reload(t)

			if !got.Paused {
				t.Fatalf("expected the task paused, got %s", got.Status)
			}

			j := storedJournal(stored)
			expectUnchangedWhileOff(t, env, got, stored, opens, before, !closedPhase(j.Phase) && journalSwapBegun(&j))

			if after := env.pairState(0); after != pair {
				t.Fatalf("expected VM 1 to keep the %s pair the crash left, got %s", pair, after)
			}

			c.expectNoThird(t)
		})
	}
}

// resumeRecreated crashes at the boundary, has BOSH recreate VM 1, and
// checks that a VM the crash left open ends reverted with the task paused
// and nothing run on it.
func resumeRecreated(t *testing.T, b resumeBoundary) {
	t.Helper()

	env, c, stored := boundaryEnv(t, b)
	env.dir.setCID(0, "vm-cid-new")

	mark := len(env.patch.recorded())
	_, got := env.reload(t)

	if closedPhase(storedJournal(stored).Phase) {
		// A closed VM has no CID to check, so it ends as it would.
		return
	}

	j := got.Instances[0].Certificates.VMs[0]
	if j.Phase != VMPhaseReverted || !got.Paused {
		t.Fatalf("expected VM 1 reverted and the task paused, got %s paused=%v (%s)", j.Phase, got.Paused, got.Instances[0].Error)
	}

	for _, op := range env.opsSince(mark, 0) {
		if op != opSudoCheck {
			t.Fatalf("expected nothing run on a recreated VM, got %v", env.opsSince(mark, 0))
		}
	}

	c.expectNoThird(t)
}

// expectUnchangedWhileOff checks that a resume with patch in place off
// paused with the patch-off message, or with the message that names VM 1
// when its swap had begun, opened no session, ran nothing on any VM, and
// left the journal and the divergence record as the crash left them.
func expectUnchangedWhileOff(t *testing.T, env *patchEnv, got, stored UpgradeTask, opens int, record string, begun bool) {
	t.Helper()

	msg := got.Instances[0].Error
	if begun {
		what := "swap"
		if storedJournal(stored).Phase == VMPhaseRollingBack {
			what = "rollback"
		}

		expectSwapOpenMessage(t, msg, env.vms[0].name(), "certificates.patch_in_place.enabled", what)
	} else if msg != patchOffMessage {
		t.Fatalf("expected the patch-off message, got %q", msg)
	}

	if env.patch.openCount() != opens {
		t.Fatalf("expected no SSH session opened, got %d more", env.patch.openCount()-opens)
	}

	for i := range got.Instances[0].Certificates.VMs {
		if want, have := journalSummary(stored, i), journalSummary(got, i); want != have {
			t.Fatalf("expected VM %d's journal unchanged at %s, got %s", i+1, want, have)
		}
	}

	if after := env.divergenceJSON(t); after != record {
		t.Fatalf("expected the divergence record unchanged, got %s, was %s", after, record)
	}
}

// expectSwapOpenMessage fails the test unless the message names the VM
// whose swap had begun, warns that it may hold a mismatched pair, and
// gives both ways out: a redeploy, which restores the VM from BOSH state,
// or the switch turned back on and a resume, which finishes what, the
// swap or the rollback.
func expectSwapOpenMessage(t *testing.T, msg, vm, switchName, what string) {
	t.Helper()

	for _, want := range []string{
		"VM " + vm,
		"new certificate beside an old key",
		"Redeploy this instance",
		"BOSH state",
		switchName,
		"back on and resume",
		"finishes the " + what,
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("expected the pause message to contain %q, got %q", want, msg)
		}
	}
}

// expectBackupsReused checks that the resume made no backup of VM 1 that
// the crash had not already made.
func expectBackupsReused(t *testing.T, env *patchEnv, stored UpgradeTask, mark int) {
	t.Helper()

	backups := 0

	for _, call := range env.patch.recorded()[mark:] {
		if call.vm == env.vms[0].name() && call.op == opBackup {
			backups++
		}
	}

	if backups == 0 {
		t.Fatal("expected the resume to back up VM 1 again, which reuses each backup")
	}

	for _, file := range storedJournal(stored).Files {
		if file.BackupPath == "" {
			t.Fatalf("expected each file to name its backup, got %+v", file)
		}
	}

	if got := env.events.all(); slices.ContainsFunc(got, func(event string) bool { return strings.Contains(event, "drift") }) {
		t.Fatalf("expected no backup to drift, got %v", got)
	}
}

// A crash after the first rename whose divergence record is lost is
// listed in the record again, as unknown, before anything else is done,
// even when the director can't be read. The resume that follows applies
// it.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchResumeRebuildsDivergence(t *testing.T) {
	env, c, _ := boundaryEnv(t, resumeBoundary{name: "the first rename", op: opReplace, nth: 1})

	err := env.store.Update(context.Background(), waitInstanceID, func(*certinventory.Divergence) (*certinventory.Divergence, error) {
		return nil, nil //nolint:nilnil // a nil record deletes it
	})
	if err != nil {
		t.Fatalf("dropping the divergence record: %v", err)
	}

	if state := env.vmState(t, patchCIDs[0]); state != "" {
		t.Fatalf("expected no divergence record, got VM 1 %q", state)
	}

	env.dir.failVMListing(errListingDown)

	second, got := env.reload(t)

	if !got.Paused {
		t.Fatalf("expected the task paused while the director can't list the VMs, got %s", got.Status)
	}

	if state := env.vmState(t, patchCIDs[0]); state != certinventory.DivergentVMUnknown {
		t.Fatalf("expected VM 1 unknown in the rebuilt record, got %q", state)
	}

	env.dir.failVMListing(nil)

	err = second.ResumeTask(context.Background(), certTaskID)
	if err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}

	waitUntil(t, "the instance to settle", func() bool { return current(t, second).Instances[0].Status.settled() })

	env.expectPatched(t, second, current(t, second))
	c.expectNoThird(t)
}

// A VM whose live state matches no row, here a backup removed by hand
// while it was swapping, ends unrecovered with the task paused, and
// nothing is replaced or restored on it.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchResumeFallThroughUnrecovered(t *testing.T) {
	env, c, stored := boundaryEnv(t, resumeBoundary{name: "the first rename", op: opReplace, nth: 1, afterSave: true})

	backup := storedJournal(stored).Files[1].BackupPath

	env.patch.mu.Lock()
	delete(env.vms[0].backups, backup)
	env.patch.mu.Unlock()

	mark := len(env.patch.recorded())
	_, got := env.reload(t)

	j := got.Instances[0].Certificates.VMs[0]
	if j.Phase != VMPhaseUnrecovered || !got.Paused {
		t.Fatalf("expected VM 1 unrecovered and the task paused, got %s paused=%v", j.Phase, got.Paused)
	}

	if !strings.Contains(got.Instances[0].Error, redeployToRepair) {
		t.Fatalf("expected the redeploy advice, got %q", got.Instances[0].Error)
	}

	ops := env.opsSince(mark, 0)
	if slices.Contains(ops, opReplace) || slices.Contains(ops, opRestore) || slices.Contains(ops, opStage) {
		t.Fatalf("expected nothing replaced, restored, or staged on VM 1, got %v", ops)
	}

	if state := env.vmState(t, patchCIDs[0]); state != certinventory.DivergentVMUnknown {
		t.Fatalf("expected VM 1 unknown in the divergence record, got %q", state)
	}

	c.expectNoThird(t)
}

// With patch in place turned off before the resume, the resume changes
// nothing, whether or not a VM's swap had begun: no session opens, the
// journal and the divergence record stay as they were, and the task
// pauses. A VM whose swap had not begun gives the patch-off message. A VM
// whose swap had begun may hold a new certificate beside an old key, so
// the message names it and says how to restore it or finish the swap.
// ResumeTask refuses with the same message, and once patch in place is on
// again a resume finishes the swap and the instance.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchResumeWithPatchOff(t *testing.T) {
	t.Run("before the swap", func(t *testing.T) {
		env, _, stored := boundaryEnv(t, resumeBoundary{name: "the last backup", op: opBackup, nth: 2})
		env.setPatchSwitch(false)

		opens := env.patch.openCount()
		before := env.divergenceJSON(t)

		second, got := env.reload(t)

		expectUnchangedWhileOff(t, env, got, stored, opens, before, false)

		err := second.ResumeTask(context.Background(), certTaskID)
		if err == nil || err.Error() != patchOffMessage {
			t.Fatalf("expected ResumeTask to refuse with the patch-off message, got %v", err)
		}
	})

	t.Run("after the first rename", func(t *testing.T) {
		env, c, stored := boundaryEnv(t, resumeBoundary{name: "the first rename", op: opReplace, nth: 1})
		env.setPatchSwitch(false)

		opens := env.patch.openCount()
		before := env.divergenceJSON(t)
		pair := env.pairState(0)

		second, got := env.reload(t)

		if !got.Paused {
			t.Fatalf("expected the task paused, got %s", got.Status)
		}

		expectUnchangedWhileOff(t, env, got, stored, opens, before, true)

		if after := env.pairState(0); after != pair {
			t.Fatalf("expected VM 1 to keep the %s pair the crash left, got %s", pair, after)
		}

		err := second.ResumeTask(context.Background(), certTaskID)
		if err == nil || err.Error() != got.Instances[0].Error {
			t.Fatalf("expected ResumeTask to refuse with the pause message, got %v", err)
		}

		env.setPatchSwitch(true)

		err = second.ResumeTask(context.Background(), certTaskID)
		if err != nil {
			t.Fatalf("ResumeTask: %v", err)
		}

		waitUntil(t, "the instance to settle", func() bool { return current(t, second).Instances[0].Status.settled() })

		env.expectPatched(t, second, current(t, second))
		c.expectNoThird(t)
	})

	t.Run("with rotation off after the first rename", func(t *testing.T) {
		env, _, stored := boundaryEnv(t, resumeBoundary{name: "the first rename", op: opReplace, nth: 1})

		env.mu.Lock()
		env.switches = switches(false, true)
		env.mu.Unlock()

		opens := env.patch.openCount()

		_, got := env.reload(t)

		if !got.Paused || env.patch.openCount() != opens {
			t.Fatalf("expected the task paused with no session opened, got paused=%v and %d new sessions", got.Paused, env.patch.openCount()-opens)
		}

		if want, have := journalSummary(stored, 0), journalSummary(got, 0); want != have {
			t.Fatalf("expected VM 1's journal unchanged at %s, got %s", want, have)
		}

		expectSwapOpenMessage(t, got.Instances[0].Error, env.vms[0].name(), "certificates.rotation.enabled", "swap")
	})

	t.Run("turned off during the resume before its first write", func(t *testing.T) {
		env, _, stored := boundaryEnv(t, resumeBoundary{name: "the first rename", op: opReplace, nth: 1})
		first := env.vms[0].name()
		before := env.divergenceJSON(t)
		pair := env.pairState(0)
		mark := len(env.patch.recorded())

		env.hook(func(call patchCall) (ssh.Result, error, bool) {
			if call.vm == first && call.op == opCheck {
				env.setPatchSwitch(false)
			}

			return ssh.Result{}, nil, false
		})

		_, got := env.reload(t)

		if !got.Paused {
			t.Fatalf("expected the task paused, got %s", got.Status)
		}

		expectSwapOpenMessage(t, got.Instances[0].Error, first, "certificates.patch_in_place.enabled", "swap")

		for _, op := range env.opsSince(mark, 0) {
			switch op {
			case opSudoCheck, opCheck, opStageCheck:
			default:
				t.Fatalf("expected only checks run on VM 1, got %v", env.opsSince(mark, 0))
			}
		}

		if after := env.pairState(0); after != pair {
			t.Fatalf("expected VM 1 to keep the %s pair the crash left, got %s", pair, after)
		}

		if want, have := journalSummary(stored, 0), journalSummary(got, 0); want != have {
			t.Fatalf("expected VM 1's journal unchanged at %s, got %s", want, have)
		}

		if after := env.divergenceJSON(t); after != before {
			t.Fatalf("expected the divergence record unchanged, got %s, was %s", after, before)
		}
	})
}

// A resume whose staged copies are gone reads the key pair again by its
// saved version, checks it against the saved hashes, and stages it
// again. A pair that does not match is never staged, and a VM with a file
// already replaced rolls back.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchResumeRefetchesStagedContent(t *testing.T) {
	t.Run("matching pair", func(t *testing.T) {
		env, c, _ := boundaryEnv(t, resumeBoundary{name: "the first rename", op: opReplace, nth: 1})

		env.patch.mu.Lock()
		clear(env.vms[0].staged)
		env.patch.mu.Unlock()

		env.pairs.mu.Lock()
		reads := len(env.pairs.reads)
		env.pairs.mu.Unlock()

		mark := len(env.patch.recorded())
		second, got := env.reload(t)

		env.pairs.mu.Lock()
		again := slices.Contains(env.pairs.reads[reads:], "v2")
		env.pairs.mu.Unlock()

		if !again {
			t.Fatal("expected the resume to read version v2 again")
		}

		if ops := env.opsSince(mark, 0); !slices.Contains(ops, opStage) {
			t.Fatalf("expected the missing copy staged again, got %v", ops)
		}

		env.expectPatched(t, second, got)
		c.expectNoThird(t)

		// The resume zeroes its pairs as it returns, just after the
		// instance settles.
		waitUntil(t, "the resume to zero its pairs", env.pairs.allZeroed)
		env.pairs.expectAllZeroed(t, "after the resume")
	})

	t.Run("pair that does not match", func(t *testing.T) {
		env, c, _ := boundaryEnv(t, resumeBoundary{name: "the first rename", op: opReplace, nth: 1})

		env.patch.mu.Lock()
		clear(env.vms[0].staged)
		env.patch.mu.Unlock()

		env.pairs.set("v2", env.oldPair)

		mark := len(env.patch.recorded())
		_, got := env.reload(t)

		if ops := env.opsSince(mark, 0); slices.Contains(ops, opStage) || slices.Contains(ops, opReplace) {
			t.Fatalf("expected nothing staged or replaced from the wrong pair, got %v", ops)
		}

		env.expectRolledBack(t, got, mark)
		c.expectNoThird(t)
	})
}

// A cancelled task that a crash stopped at any boundary of VM 1 ends its
// instance and frees it on the load. VM 1 ends closed with one whole
// pair, the new one only when it ends done, the cleanup of every VM that
// had files is done, and nothing runs on any other VM.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchResumeCancelledAtEveryBoundary(t *testing.T) {
	for _, b := range resumeBoundaries() {
		t.Run(b.name, func(t *testing.T) {
			env, c, stored := boundaryEnv(t, b)
			mark := len(env.patch.recorded())

			second := env.startCancelled(t, stored, nil)

			waitUntil(t, "the instance to settle", func() bool { return current(t, second).Instances[0].Status.settled() })

			inst := current(t, second).Instances[0]
			j := inst.Certificates.VMs[0]

			ended := []VMPhase{VMPhaseDone, VMPhaseRolledBack, VMPhasePlanned, VMPhaseStaged, VMPhaseBackedUp}
			if inst.Status != InstanceStatusCancelled || !slices.Contains(ended, j.Phase) {
				t.Fatalf("expected the instance cancelled with VM 1 closed or never swapped, got %s with VM 1 %s: %q", inst.Status, j.Phase, inst.Error)
			}

			want := pairOld
			if j.Phase == VMPhaseDone {
				want = pairNew
			}

			if got := env.pairState(0); got != want {
				t.Fatalf("expected VM 1 at %s to hold the %s pair, got %s", j.Phase, want, got)
			}

			if (j.Cleanup != "" && j.Cleanup != CleanupDone) || env.workFiles() != 0 {
				t.Fatalf("expected VM 1 cleaned up, got cleanup %q with %d files left", j.Cleanup, env.workFiles())
			}

			for i := 1; i < len(env.vms); i++ {
				if ops := env.filteredOpsSince(mark, i); len(ops) != 0 {
					t.Fatalf("expected nothing run on VM %d, got %v", i+1, ops)
				}
			}

			env.expectNoMix(t)
			c.expectNoThird(t)
			expectReleased(t, second)
		})
	}
}

// A cancelled task whose VM a crash left part way through its swap is
// finished on the load, whether or not its first rename was sent, and its
// instance then ends cancelled between VMs and is released. With patch
// in place turned off, a VM whose first rename was not sent stays open
// with nothing replaced, and the task waits for a resume, which finishes
// the VM once patch in place is on again.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchResumeCancelledTask(t *testing.T) {
	swapping := resumeBoundary{name: "the unknown divergence write", divergence: 1, afterSave: true}

	cases := []struct {
		name string
		b    resumeBoundary
	}{
		{name: "after the first rename", b: resumeBoundary{name: "the first rename", op: opReplace, nth: 1}},
		{name: "swapping before the first rename", b: swapping},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, c, stored := boundaryEnv(t, tc.b)

			if j := storedJournal(stored); j.Phase != VMPhaseSwapping {
				t.Fatalf("expected the crash to leave VM 1 swapping, got %s", j.Phase)
			}

			second := env.startCancelled(t, stored, nil)

			env.expectCancelledFinished(t, second)
			c.expectNoThird(t)
		})
	}

	t.Run("after the first rename with patch in place off", func(t *testing.T) {
		env, c, stored := boundaryEnv(t, cases[0].b)
		env.setPatchSwitch(false)

		opens := env.patch.openCount()
		before := env.divergenceJSON(t)
		mark := len(env.patch.recorded())
		second := env.startCancelled(t, stored, nil)

		waitUntil(t, "the task to wait for a resume", func() bool { return current(t, second).Paused })

		got := current(t, second)
		if got.Status != TaskStatusCancelled {
			t.Fatalf("expected the task to stay cancelled, got %s", got.Status)
		}

		expectSwapOpenMessage(t, got.Instances[0].Error, env.vms[0].name(), "certificates.patch_in_place.enabled", "swap")
		expectHeldOpen(t, second)

		if ops := env.opsSince(mark, 0); len(ops) != 0 || env.patch.openCount() != opens {
			t.Fatalf("expected no session and nothing run on VM 1, got %v", ops)
		}

		if want, have := journalSummary(stored, 0), journalSummary(got, 0); want != have {
			t.Fatalf("expected VM 1's journal unchanged at %s, got %s", want, have)
		}

		if after := env.divergenceJSON(t); after != before {
			t.Fatalf("expected the divergence record unchanged, got %s, was %s", after, before)
		}

		err := second.ResumeTask(context.Background(), certTaskID)
		if err == nil {
			t.Fatal("expected a resume to be refused while patch in place is off")
		}

		env.setPatchSwitch(true)

		err = second.ResumeTask(context.Background(), certTaskID)
		if err != nil {
			t.Fatalf("ResumeTask: %v", err)
		}

		env.expectCancelledFinished(t, second)
		c.expectNoThird(t)
	})

	t.Run("swapping with patch in place off", func(t *testing.T) {
		env, c, stored := boundaryEnv(t, swapping)
		env.setPatchSwitch(false)

		mark := len(env.patch.recorded())
		second := env.startCancelled(t, stored, nil)

		waitUntil(t, "the task to wait for a resume", func() bool { return current(t, second).Paused })

		got := current(t, second)
		if got.Status != TaskStatusCancelled || !strings.Contains(got.Instances[0].Error, patchOffMessage) {
			t.Fatalf("expected the cancelled task to wait with the patch-off message, got %s: %q", got.Status, got.Instances[0].Error)
		}

		expectHeldOpen(t, second)

		if ops := env.opsSince(mark, 0); slices.Contains(ops, opReplace) {
			t.Fatalf("expected nothing replaced with patch in place off, got %v", ops)
		}

		err := second.ResumeTask(context.Background(), certTaskID)
		if err == nil {
			t.Fatal("expected a resume to be refused while patch in place is off")
		}

		env.setPatchSwitch(true)

		err = second.ResumeTask(context.Background(), certTaskID)
		if err != nil {
			t.Fatalf("ResumeTask: %v", err)
		}

		env.expectCancelledFinished(t, second)
		c.expectNoThird(t)
	})
}

// What a VM's certificate and key hold, as pairState names it.
const (
	pairOld   = "old"
	pairNew   = "new"
	pairMixed = "mixed"
)

// pairState names what VM vm holds: pairOld or pairNew when its
// certificate and key are both the old or both the new pair, and
// pairMixed otherwise.
func (e *patchEnv) pairState(vm int) string {
	v := e.vms[vm]

	e.patch.mu.Lock()
	cert, key := v.files[v.certPath()], v.files[v.keyPath()]
	e.patch.mu.Unlock()

	switch {
	case cert == certpatch.NormalisedHash(e.oldPair.cert) && key == certpatch.NormalisedHash(e.oldPair.key):
		return pairOld
	case cert == certpatch.NormalisedHash(e.newPair.cert) && key == certpatch.NormalisedHash(e.newPair.key):
		return pairNew
	default:
		return pairMixed
	}
}

// expectNoMix fails the test unless every VM holds the old pair or the
// new one, never a certificate of one with the key of the other.
func (e *patchEnv) expectNoMix(t *testing.T) {
	t.Helper()

	for i := range e.vms {
		if state := e.pairState(i); state == pairMixed {
			t.Fatalf("expected VM %d to hold one whole pair, got a certificate and key of different pairs", i+1)
		}
	}
}

// startCancelled stores the crashed task as cancelled and starts a new
// manager, which setup may change first, that loads it.
func (e *patchEnv) startCancelled(t *testing.T, stored UpgradeTask, setup func(*Manager)) *Manager {
	t.Helper()

	stored.Status = TaskStatusCancelled
	storeCertificateTask(t, e.vault.waitVault, stored)

	second := e.newManager()
	if setup != nil {
		setup(second)
	}

	second.Start()
	waitForLoad(t, second)

	return second
}

// expectHeldOpen fails the test unless VM 1 is still part way through
// its swap and the instance is still running and reserved.
func expectHeldOpen(t *testing.T, manager *Manager) {
	t.Helper()

	inst := current(t, manager).Instances[0]
	if j := inst.Certificates.VMs[0]; !midVM(j.Phase) || inst.Status != InstanceStatusRunning {
		t.Fatalf("expected VM 1 left part way through its swap and the instance running, got %s and %s (%s)", j.Phase, inst.Status, inst.Error)
	}

	if _, held := manager.Guard().Holder(waitInstanceID, waitDeployment); !held {
		t.Fatal("expected the instance to stay reserved while VM 1 is open")
	}
}

// expectCancelledFinished fails the test unless VM 1 finished its swap
// and holds the new pair, VM 2 was never started, the instance was
// cancelled between VMs, and its reservation was freed.
func (e *patchEnv) expectCancelledFinished(t *testing.T, manager *Manager) {
	t.Helper()

	waitUntil(t, "the instance to settle", func() bool { return current(t, manager).Instances[0].Status.settled() })

	inst := current(t, manager).Instances[0]
	if j := inst.Certificates.VMs[0]; j.Phase != VMPhaseDone || j.Cleanup != CleanupDone {
		t.Fatalf("expected VM 1 done and clean, got %s %q (%s)", j.Phase, j.Cleanup, inst.Error)
	}

	if state := e.pairState(0); state != pairNew {
		t.Fatalf("expected VM 1 to hold the new pair, got %s", state)
	}

	if j := inst.Certificates.VMs[1]; j.Phase != VMPhasePlanned {
		t.Fatalf("expected VM 2 left planned, got %s", j.Phase)
	}

	if inst.Status != InstanceStatusCancelled || inst.Error != cancelledBetweenVMs {
		t.Fatalf("expected the instance cancelled between VMs, got %s %q", inst.Status, inst.Error)
	}

	e.expectNoMix(t)
	expectReleased(t, manager)
}

// A cancelled task resumes a VM left part way through its swap as a live
// task does. A director that can't list the VMs, or that names no CID for
// the VM, for a while leaves the VM open and the instance reserved, and
// the resume tries again until the VM finishes.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchResumeCancelledRetries(t *testing.T) {
	afterRename := resumeBoundary{name: "the first rename", op: opReplace, nth: 1}
	swapping := resumeBoundary{name: "the unknown divergence write", divergence: 1, afterSave: true}

	listingDown := func(env *patchEnv) { env.dir.failVMListing(errListingDown) }
	listingUp := func(env *patchEnv) { env.dir.failVMListing(nil) }
	noCID := func(env *patchEnv) { env.dir.setCID(0, "") }
	cidBack := func(env *patchEnv) { env.dir.setCID(0, patchCIDs[0]) }

	cases := []struct {
		name          string
		b             resumeBoundary
		fail, recover func(*patchEnv)
	}{
		{"the VM listing fails after the first rename", afterRename, listingDown, listingUp},
		{"no CID after the first rename", afterRename, noCID, cidBack},
		{"no CID before the first rename", swapping, noCID, cidBack},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, c, stored := boundaryEnv(t, tc.b)
			tc.fail(env)

			second := env.startCancelled(t, stored, nil)

			waitUntil(t, "the resume to fail once", func() bool { return current(t, second).Instances[0].Error != "" })
			expectHeldOpen(t, second)

			tc.recover(env)

			env.expectCancelledFinished(t, second)
			c.expectNoThird(t)
		})
	}
}

// A cancelled task whose open VM could not be finished before the
// instance's deadline leaves the VM open and the instance reserved, and
// waits for a resume. A resume then finishes the VM.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchResumeCancelledWaitsForResume(t *testing.T) {
	env, c, stored := boundaryEnv(t, resumeBoundary{name: "the first rename", op: opReplace, nth: 1})
	env.dir.setCID(0, "")

	second := env.startCancelled(t, stored, func(m *Manager) { m.cancelledResumeWait = 100 * time.Millisecond })

	waitUntil(t, "the task to wait for a resume", func() bool { return current(t, second).Paused })

	got := current(t, second)
	if got.Status != TaskStatusCancelled || !strings.Contains(got.Instances[0].Error, "Resume the task") {
		t.Fatalf("expected the cancelled task to wait for a resume, got %s: %q", got.Status, got.Instances[0].Error)
	}

	expectHeldOpen(t, second)

	env.dir.setCID(0, patchCIDs[0])

	err := second.ResumeTask(context.Background(), certTaskID)
	if err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}

	env.expectCancelledFinished(t, second)

	if current(t, second).Paused {
		t.Fatal("expected the resume to clear the wait")
	}

	c.expectNoThird(t)
}

// A panic while a VM is part way through its patch pauses the task with
// advice to resume, rather than failing it, so the resume that checks the
// VM's files can finish or roll it back.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchResumeAfterPanicPauses(t *testing.T) {
	env, _, stored := boundaryEnv(t, resumeBoundary{name: "the first rename", op: opReplace, nth: 1})

	manager := env.newManager()

	func() {
		defer manager.recoverTask(context.Background(), &stored)

		panic("a test panic")
	}()

	if stored.Status != TaskStatusPaused || !stored.Paused {
		t.Fatalf("expected the task paused, got %s paused=%v", stored.Status, stored.Paused)
	}

	if msg := stored.Instances[0].Error; !strings.Contains(msg, "Resume to check its files") {
		t.Fatalf("expected the advice to resume, got %q", msg)
	}
}

// cancelledCleanupBoundaries are the boundaries that leave VM 1 closed,
// or not yet swapping, with its staging and backup files still on it and
// its cleanup pending.
func cancelledCleanupBoundaries() []resumeBoundary {
	return []resumeBoundary{
		{name: "VM 1 staged", op: opStage, nth: 2, afterSave: true},
		{name: "VM 1 backed up", op: opBackup, nth: 2, afterSave: true},
		{name: "VM 1 done", saved: "done:"},
		{name: "VM 1 rolled back", rollback: true, saved: "rolled_back:"},
	}
}

// cancelledWithFiles crashes at the boundary and checks that VM 1 was
// left holding staged or backed-up files with its cleanup pending.
func cancelledWithFiles(t *testing.T, b resumeBoundary) (*patchEnv, *crasher, UpgradeTask) {
	t.Helper()

	env, c, stored := boundaryEnv(t, b)

	if j := storedJournal(stored); midVM(j.Phase) || j.Cleanup != CleanupPending || env.workFiles() == 0 {
		t.Fatalf("expected VM 1 left with its files and its cleanup pending, got %s with cleanup %q and %d files", j.Phase, j.Cleanup, env.workFiles())
	}

	return env, c, stored
}

// workFiles counts the staged copies and backups on VM 1.
func (e *patchEnv) workFiles() int {
	e.patch.mu.Lock()
	defer e.patch.mu.Unlock()

	return len(e.vms[0].staged) + len(e.vms[0].backups)
}

// filteredOpsSince lists the patch.sh operations run on the VM from the
// recorded call at mark on.
func (e *patchEnv) filteredOpsSince(mark, vm int) []string {
	var ops []string

	for _, op := range e.opsSince(mark, vm) {
		switch op {
		case opCheck, opPrepare, opStage, opStageCheck, opBackup, opReplace, opRestore, opRestart, opCleanup:
			ops = append(ops, op)
		}
	}

	return ops
}

// expectCancelledSettled waits until the instance of the cancelled task
// settles, and fails the test unless VM 1 kept its phase, no VM holds a
// mixed pair, no file held a third hash, and the instance was freed.
func expectCancelledSettled(t *testing.T, env *patchEnv, c *crasher, manager *Manager, stored UpgradeTask) VMJournal {
	t.Helper()

	waitUntil(t, "the instance to settle", func() bool { return current(t, manager).Instances[0].Status.settled() })

	j := current(t, manager).Instances[0].Certificates.VMs[0]
	if want := storedJournal(stored).Phase; j.Phase != want {
		t.Fatalf("expected VM 1 to stay %s, got %s", want, j.Phase)
	}

	env.expectNoMix(t)
	c.expectNoThird(t)
	expectReleased(t, manager)

	return j
}

// A cancelled task removes the staging and backup files from a VM that
// it left closed or not yet swapping, the staged new private key among
// them, before it ends the instance. Nothing else runs on any VM.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchCancelledCleansUpClosedVMs(t *testing.T) {
	for _, b := range cancelledCleanupBoundaries() {
		t.Run(b.name, func(t *testing.T) {
			env, c, stored := cancelledWithFiles(t, b)
			mark := len(env.patch.recorded())

			second := env.startCancelled(t, stored, nil)
			j := expectCancelledSettled(t, env, c, second, stored)

			if j.Cleanup != CleanupDone || env.workFiles() != 0 {
				t.Fatalf("expected VM 1 cleaned up, got cleanup %q with %d files left", j.Cleanup, env.workFiles())
			}

			want := cancelledBetweenVMs
			if !closedPhase(j.Phase) {
				want = cancelledUnpatched + " " + patchPendingNote + " " + cleanedUpNote
			}

			if inst := current(t, second).Instances[0]; inst.Status != InstanceStatusCancelled || inst.Error != want {
				t.Fatalf("expected the instance cancelled with %q, got %s: %q", want, inst.Status, inst.Error)
			}

			if ops := env.filteredOpsSince(mark, 0); !slices.Equal(ops, []string{opCleanup}) {
				t.Fatalf("expected only a cleanup on VM 1, got %v", ops)
			}

			for i := 1; i < len(env.vms); i++ {
				if ops := env.filteredOpsSince(mark, i); len(ops) != 0 {
					t.Fatalf("expected nothing run on VM %d, got %v", i+1, ops)
				}
			}
		})
	}
}

// A cancelled task whose cleanup fails, or that may not clean up because
// patch in place is off, still ends the instance and frees it. The
// cleanup is tried once and recorded as failed, or not tried at all.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchCancelledCleanupNeverHolds(t *testing.T) {
	b := cancelledCleanupBoundaries()[0]

	t.Run("the cleanup fails", func(t *testing.T) {
		env, c, stored := cancelledWithFiles(t, b)
		mark := len(env.patch.recorded())

		env.hook(func(call patchCall) (ssh.Result, error, bool) {
			if call.op == opCleanup {
				return exitResult(1), nil, true
			}

			return ssh.Result{}, nil, false
		})

		second := env.startCancelled(t, stored, nil)
		j := expectCancelledSettled(t, env, c, second, stored)

		if j.Cleanup != CleanupFailed {
			t.Fatalf("expected VM 1's cleanup recorded as failed, got %q", j.Cleanup)
		}

		env.expectFilesLeft(t, second)

		if ops := env.filteredOpsSince(mark, 0); !slices.Equal(ops, []string{opCleanup}) {
			t.Fatalf("expected one cleanup tried on VM 1, got %v", ops)
		}
	})

	t.Run("patch in place is off", func(t *testing.T) {
		env, c, stored := cancelledWithFiles(t, b)
		env.setPatchSwitch(false)

		opens := env.patch.openCount()

		second := env.startCancelled(t, stored, nil)
		j := expectCancelledSettled(t, env, c, second, stored)

		if j.Cleanup != CleanupPending || env.patch.openCount() != opens {
			t.Fatalf("expected no session and the cleanup left pending, got %d new sessions and cleanup %q", env.patch.openCount()-opens, j.Cleanup)
		}

		env.expectFilesLeft(t, second)
	})
}

// expectFilesLeft fails the test unless the instance was cancelled with
// nothing replaced and the note that VM 1 keeps its staged copies and
// backups.
func (e *patchEnv) expectFilesLeft(t *testing.T, manager *Manager) {
	t.Helper()

	want := cancelledUnpatched + " " + patchPendingNote + " " +
		fmt.Sprintf(filesLeftNote, "VM "+e.vms[0].name(), patchWorkRoot+certTaskID+"/")

	if inst := current(t, manager).Instances[0]; inst.Status != InstanceStatusCancelled || inst.Error != want {
		t.Fatalf("expected the instance cancelled with %q, got %s: %q", want, inst.Status, inst.Error)
	}
}

// A resume that makes VM 1's missing backup reads the director again
// before its first replace, and a BOSH task that started meanwhile
// pauses the task with VM 1 backed up and every file old.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchResumeRechecksBeforeSwap(t *testing.T) {
	env, c, _ := boundaryEnv(t, resumeBoundary{name: "the first backup", op: opBackup, nth: 1, afterSave: true})
	task, _, _, _ := directorTurns()

	mark := len(env.patch.recorded())
	env.turnOn(opBackup, 1, task)

	_, got := env.reload(t)

	if ops := env.opsSince(mark, 0); !slices.Contains(ops, opBackup) || slices.Contains(ops, opReplace) {
		t.Fatalf("expected the missing backup made and no replace on VM 1, got %v", ops)
	}

	j := storedJournal(got)
	if j.Phase != VMPhaseBackedUp || !got.Paused || !strings.Contains(got.Instances[0].Error, "BOSH task 41") {
		t.Fatalf("expected VM 1 backed up and the task paused on BOSH task 41, got %s paused=%v %q", j.Phase, got.Paused, got.Instances[0].Error)
	}

	env.expectPairOn(t, env.oldPair, "old")
	c.expectNoThird(t)
}

// rawArgCounts are the argument counts of each file operation of patch.sh
// without raw hashes and with them.
func rawArgCounts() map[string][2]int {
	return map[string][2]int{
		opCheck: {6, 8}, opStageCheck: {3, 5}, opBackup: {5, 6}, opReplace: {6, 9}, opRestore: {6, 8},
	}
}

// padVMs has discovery report every file of every VM with a blank line
// before its PEM and two after it, as a job template renders them.
func (e *patchEnv) padVMs() {
	e.patch.mu.Lock()
	defer e.patch.mu.Unlock()

	for _, fake := range e.vms {
		fake.lead, fake.tail = "n", "nnn"
	}
}

// padded is the bytes a patch writes for pem over a padded file.
func padded(pem []byte) string {
	return "\n" + strings.TrimSpace(string(pem)) + "\n\n\n"
}

// rawOfFile is the raw hash the fake holds for the VM's file.
func (e *patchEnv) rawOfFile(index int, path string) string {
	e.patch.mu.Lock()
	defer e.patch.mu.Unlock()

	return e.vms[index].rawOf(e.vms[index].files, path)
}

// expectPaddedPair fails unless every VM's files hold the raw bytes of
// the pair, the new one written between the padding or the old one as
// discovery read it.
func (e *patchEnv) expectPaddedPair(t *testing.T, vms int, fresh bool) {
	t.Helper()

	for index := range vms {
		fake := e.vms[index]

		wantCert, wantKey := fakeRaw(certpatch.NormalisedHash(e.oldPair.cert)), fakeRaw(certpatch.NormalisedHash(e.oldPair.key))
		if fresh {
			wantCert, wantKey = certpatch.RawHash([]byte(padded(e.newPair.cert))), certpatch.RawHash([]byte(padded(e.newPair.key)))
		}

		if e.rawOfFile(index, fake.certPath()) != wantCert || e.rawOfFile(index, fake.keyPath()) != wantKey {
			t.Fatalf("expected VM %d to hold the exact bytes of the %s pair", index+1, map[bool]string{true: "new", false: "old"}[fresh])
		}
	}
}

// A patch records the raw hash of each target as discovery read it and of
// the exact bytes that replace it, sends both with every file operation,
// stages the target's padding around the new PEM, and leaves each file
// holding exactly those bytes.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchRecordsAndSendsRawHashes(t *testing.T) {
	env := newPatchEnv(t)
	env.padVMs()

	manager := env.newManager()
	task := env.task()

	if !env.run(manager, &task) {
		t.Fatalf("expected the instance to settle, got %+v", current(t, manager).Instances[0])
	}

	if got := current(t, manager); got.Instances[0].Status != InstanceStatusSuccess {
		t.Fatalf("expected the patch to succeed, got %s: %s", got.Instances[0].Status, got.Instances[0].Error)
	}

	for _, file := range journalOf(t, manager, env.vms[0].name()).Files {
		content := env.newPair.cert
		if file.Role == FileRoleKey {
			content = env.newPair.key
		}

		if file.OldRawHash != fakeRaw(file.OldHash) || file.NewRawHash != certpatch.RawHash([]byte(padded(content))) ||
			file.Lead != "\n" || file.Tail != "\n\n\n" {
			t.Fatalf("expected %s to record its raw hashes and padding, got %+v", file.Path, file)
		}
	}

	for _, call := range env.patch.recorded() {
		if counts, ok := rawArgCounts()[call.op]; ok && len(call.args) != counts[1] {
			t.Fatalf("expected %s on %s to carry raw hashes, got %d arguments", call.op, call.vm, len(call.args))
		}
	}

	env.expectPaddedPair(t, len(env.vms), true)
}

// A staged copy that openssl cannot read in its role stops the VM before
// anything on it changed, the way any failed check of a staged copy does,
// and the error says why without repeating the script's output.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchStopsOnUnreadableNewFile(t *testing.T) {
	env := newPatchEnv(t)
	manager := env.newManager()
	task := env.task()
	name := env.vms[0].name()

	env.hook(func(call patchCall) (ssh.Result, error, bool) {
		if call.vm == name && call.op == opStageCheck {
			return exitResult(certscripts.ExitInvalid, "drift path="+call.args[1]), nil, true
		}

		return ssh.Result{}, nil, false
	})

	env.run(manager, &task)

	for _, call := range env.patch.recorded() {
		if call.op == opBackup || call.op == opReplace {
			t.Fatalf("expected nothing backed up or replaced after the check failed, got %s on %s", call.op, call.vm)
		}
	}

	if state := env.pairState(0); state != pairOld {
		t.Fatalf("expected VM 1 to keep the old pair, got %s", state)
	}

	journal := journalOf(t, manager, name)
	if journal.Phase != VMPhaseUnrecovered || !strings.Contains(journal.Error, "openssl cannot read the new file") {
		t.Fatalf("expected VM 1 stopped with the openssl reason, got %s %q", journal.Phase, journal.Error)
	}

	if got := current(t, manager); !got.Paused || strings.Contains(got.Instances[0].Error, "drift path") {
		t.Fatalf("expected a paused task whose error leaves out the output, got paused=%v %q", got.Paused, got.Instances[0].Error)
	}
}

// A target that gains only a blank line after discovery read it keeps
// its normalised hash, but its raw hash changes, so the patch finds the
// drift before it backs the file up and replaces nothing.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchCatchesWhitespaceDrift(t *testing.T) {
	env := newPatchEnv(t)
	manager := env.newManager()
	task := env.task()
	target := env.vms[0]
	certPath := target.certPath()

	env.hook(func(call patchCall) (ssh.Result, error, bool) {
		if call.vm == target.name() && call.op == opStageCheck {
			env.patch.mu.Lock()
			old := target.files[certPath]
			target.raws[certPath] = rawEntry{norm: old, raw: certpatch.RawHash([]byte("\n" + string(env.oldPair.cert)))}
			env.patch.mu.Unlock()
		}

		return ssh.Result{}, nil, false
	})

	env.run(manager, &task)

	for _, call := range env.patch.recorded() {
		if call.op == opReplace {
			t.Fatalf("expected no replace after the drift, got one on %s", call.vm)
		}
	}

	journal := journalOf(t, manager, target.name())
	if journal.Phase != VMPhaseUnrecovered || !strings.Contains(journal.Error, "changed under it") {
		t.Fatalf("expected VM 1 stopped for drift, got %s %q", journal.Phase, journal.Error)
	}

	if got := env.rawOfFile(0, certPath); got != certpatch.RawHash([]byte("\n"+string(env.oldPair.cert))) {
		t.Fatal("expected the drifted target to keep its bytes")
	}
}

// A rollback whose backup changed by one blank line refuses to restore
// from it, leaves the file holding the new content, and leaves the VM
// unrecovered.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchRollbackRefusesTamperedBackup(t *testing.T) {
	env := newPatchEnv(t)
	manager := env.newManager()
	task := env.task()
	target := env.vms[0]

	env.gateFailsOn(0, func(call patchCall) (ssh.Result, error, bool) {
		if call.vm == target.name() && call.op == opRestore {
			env.patch.mu.Lock()
			for path, norm := range target.backups {
				target.raws[path] = rawEntry{norm: norm, raw: certpatch.RawHash([]byte("tampered"))}
			}
			env.patch.mu.Unlock()
		}

		return ssh.Result{}, nil, false
	})

	env.run(manager, &task)

	journal := journalOf(t, manager, target.name())
	if journal.Phase != VMPhaseUnrecovered {
		t.Fatalf("expected VM 1 unrecovered, got %s %q", journal.Phase, journal.Error)
	}

	if state := env.pairState(0); state != pairNew {
		t.Fatalf("expected VM 1 to keep the new pair rather than a tampered backup, got %s", state)
	}
}

// A journal that a broker from before raw hashes saved has none, and its
// resume patches by normalised hashes as that broker did, at every
// boundary: each file operation goes without raw hashes, and the patch
// ends as it would have.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchResumesLegacyJournal(t *testing.T) {
	for _, boundary := range resumeBoundaries() {
		t.Run(boundary.name, func(t *testing.T) {
			env, crash, stored := boundaryEnv(t, boundary)

			legacy := legacyRecord(t, stored)
			storeCertificateTask(t, env.vault.waitVault, legacy)

			mark := len(env.patch.recorded())
			second, got := env.reload(t)

			if boundary.rollback {
				env.expectRolledBack(t, got, mark)
			} else {
				env.expectPatched(t, second, got)
			}

			env.expectNoMix(t)
			crash.expectNoThird(t)

			for _, call := range env.patch.recorded()[mark:] {
				if counts, ok := rawArgCounts()[call.op]; ok && len(call.args) != counts[0] {
					t.Fatalf("expected %s on %s without raw hashes, got %d arguments", call.op, call.vm, len(call.args))
				}
			}

			for _, file := range storedJournal(env.stored()).Files {
				if file.rawHashes() {
					t.Fatalf("expected the resumed journal to stay without raw hashes, got %+v", file)
				}
			}
		})
	}
}

// legacyRecord is the task as a broker from before raw hashes would have
// saved it, read back from JSON that has none of their keys.
func legacyRecord(t *testing.T, task UpgradeTask) UpgradeTask {
	t.Helper()

	for index := range task.Instances {
		if task.Instances[index].Certificates == nil {
			continue
		}

		for vmIndex := range task.Instances[index].Certificates.VMs {
			files := task.Instances[index].Certificates.VMs[vmIndex].Files
			for n := range files {
				files[n].OldRawHash, files[n].NewRawHash, files[n].Lead, files[n].Tail = "", "", "", ""
			}
		}
	}

	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"old_raw_sha256", "new_raw_sha256", `"lead"`, `"tail"`} {
		if bytes.Contains(raw, []byte(key)) {
			t.Fatalf("expected the legacy record to have no %s key", key)
		}
	}

	var legacy UpgradeTask

	err = json.Unmarshal(raw, &legacy)
	if err != nil {
		t.Fatal(err)
	}

	return legacy
}

// A crash at each boundary of a patch of padded files resumes to the end
// the boundary has, with each file holding exactly the bytes discovery
// read or exactly the bytes the replacement wrote.
//
//nolint:paralleltest // the runner shares the manager's test seams
func TestPatchResumePaddedAtEveryBoundary(t *testing.T) {
	for _, boundary := range resumeBoundaries() {
		t.Run(boundary.name, func(t *testing.T) {
			env := newPatchEnv(t)
			env.padVMs()

			if boundary.rollback {
				env.gateFailsOn(0, nil)
			}

			crash := crashAt(env, boundary)
			stored := crash.crashRun(t)

			for _, file := range storedJournal(stored).Files {
				if !file.rawHashes() {
					t.Fatalf("expected the crashed journal to hold raw hashes, got %+v", file)
				}
			}

			mark := len(env.patch.recorded())
			second, got := env.reload(t)

			if boundary.rollback {
				env.expectRolledBack(t, got, mark)
				env.expectPaddedPair(t, 1, false)
			} else {
				env.expectPatched(t, second, got)
				env.expectPaddedPair(t, len(env.vms), true)
			}

			crash.expectNoThird(t)
		})
	}
}
