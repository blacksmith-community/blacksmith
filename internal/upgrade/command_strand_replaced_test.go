package upgrade

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// strandSent cancels a task while the first node's exec runs and its
// cleanup can't reach the node, which leaves the node sent and the task
// cancelled and waiting for Resume cleanup.
func (f *taskFixture) strandSent() *cancelScript {
	f.t.Helper()

	script := f.blockExec()
	script.cleanupDown.Store(true)

	f.queue()
	script.wait(f.t, 1)
	f.cancel()

	f.waitFor("the task to wait for a resume", func(task UpgradeTask) bool {
		return task.Paused && nodeOf(task, 0, 0).Phase == CommandPhaseSent
	})
	waitUntil(f.t, "the processor to let go", func() bool { return !f.owned() })

	return script
}

// strandStaged cancels a task after the first node's prepare returned and
// before its sent save, while its cleanup can't reach the node, which
// leaves the node staged and the task cancelled and waiting for Resume
// cleanup.
func (f *taskFixture) strandStaged() *cancelScript {
	f.t.Helper()

	script := f.blockExec()
	script.cleanupDown.Store(true)

	var prepared, blocked atomic.Bool

	arrived := make(chan struct{})

	previous := f.onOp
	f.onOp = func(deployment string, index int, op string) {
		if previous != nil {
			previous(deployment, index, op)
		}

		if op == opRunPrepare {
			prepared.Store(true)
		}
	}
	f.authHook = func(ctx context.Context) error {
		if !prepared.Load() || !blocked.CompareAndSwap(false, true) {
			return nil
		}

		close(arrived)

		<-ctx.Done()

		return fmt.Errorf("the check was cut short: %w", ctx.Err())
	}

	f.queue()

	select {
	case <-arrived:
	case <-time.After(waitTimeout):
		f.t.Fatal("the driver never reached the check before the sent save")
	}

	f.cancel()

	f.waitFor("the task to wait for a resume", func(task UpgradeTask) bool {
		return task.Paused && nodeOf(task, 0, 0).Phase == CommandPhaseStaged
	})
	waitUntil(f.t, "the processor to let go", func() bool { return !f.owned() })

	return script
}

// A stranded node whose VM the director now lists with another CID, or
// not at all, has nothing of the command left on it. Resume cleanup
// settles the node failed with a reason that says so, sends nothing to
// the VM, and releases the instance, and the cancelled task stops waiting.
func TestStrandedNodeOnAReplacedVMIsReleased(t *testing.T) {
	cases := []struct {
		name    string
		strand  func(*taskFixture) *cancelScript
		change  func(*taskFixture)
		wantHas string
	}{
		{name: "sent, recreated", strand: (*taskFixture).strandSent, change: func(f *taskFixture) { f.replaceVM("dep-0", 0) }, wantHas: "after the command was sent"},
		{name: "sent, removed", strand: (*taskFixture).strandSent, change: func(f *taskFixture) { f.removeVM("dep-0", 0) }, wantHas: "after the command was sent"},
		{name: "staged, recreated", strand: (*taskFixture).strandStaged, change: func(f *taskFixture) { f.replaceVM("dep-0", 0) }, wantHas: "before the command was sent"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fix := newTaskFixture(t, 1, 1)
			tc.strand(fix)

			before := len(fix.opsOf("dep-0", 0))

			tc.change(fix)
			fix.resume()
			fix.waitReleased()

			done := fix.waitFor("the task to stop waiting", func(task UpgradeTask) bool {
				return !task.Paused && nodeOf(task, 0, 0).Phase == CommandPhaseSettled
			})

			if done.Status != TaskStatusCancelled {
				t.Errorf("the task is %s, want cancelled", done.Status)
			}

			node := nodeOf(done, 0, 0)
			if node.Status != CommandStatusFailed || node.ExitCode != nil {
				t.Errorf("the node is %s with exit %v, want failed with no exit", node.Status, node.ExitCode)
			}

			if !strings.Contains(node.Reason, "VM redis/0 was replaced") || !strings.Contains(node.Reason, tc.wantHas) ||
				!strings.Contains(node.Reason, "nothing of it is left to clean") {
				t.Errorf("the node's reason is %q, want it to say the VM was replaced %s", node.Reason, tc.wantHas)
			}

			if got := fix.opsOf("dep-0", 0)[before:]; len(got) != 0 {
				t.Errorf("Resume cleanup sent %v to a VM that was replaced", got)
			}

			if fix.waits() || len(fix.heldIDs()) != 0 {
				t.Errorf("the task still waits %v or holds %v", fix.waits(), fix.heldIDs())
			}

			fix.assertNoCommandText()
		})
	}
}

// A director that can't list the deployment, even because it can't find
// it, is no evidence about the stranded node, so the node stays sent, the
// instance stays held, and the task waits for another Resume cleanup.
func TestStrandedNodeKeepsTheHoldWhenTheDirectorFails(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)
	script := fix.strandSent()
	script.cleanupDown.Store(false)

	cleanups := fix.countOps("dep-0", 0, opRunCleanup)

	fix.failListing("dep-0", errors.New("deployment dep-0 not found"))
	fix.resume()

	waitUntil(t, "the resume to end", func() bool { return fix.waits() && !fix.settling() && !fix.owned() })

	snap := fix.snapshot()
	if got := nodeOf(snap, 0, 0).Phase; got != CommandPhaseSent || !fix.holds() {
		t.Fatalf("the node is %s and held %v after a director error, want sent and held", got, fix.holds())
	}

	if snap.Status != TaskStatusCancelled || !snap.Paused {
		t.Errorf("the task is %s, paused %v, want cancelled and waiting", snap.Status, snap.Paused)
	}

	if n := fix.countOps("dep-0", 0, opRunCleanup) - cleanups; n != 0 {
		t.Errorf("cleanup was sent %d times while the director could not list the deployment", n)
	}

	fix.failListing("dep-0", nil)
	fix.resume()
	fix.waitReleased()

	done := fix.waitFor("the node to settle", func(task UpgradeTask) bool { return nodeOf(task, 0, 0).Phase == CommandPhaseSettled })
	if strings.Contains(nodeOf(done, 0, 0).Reason, "replaced") {
		t.Errorf("the node's reason is %q, want the cleanup's outcome", nodeOf(done, 0, 0).Reason)
	}

	fix.assertNoCommandText()
}
