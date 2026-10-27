package upgrade

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/ops/scripts"
)

// An exec that ends without an exit status takes the outcome from the
// cleanup record, whatever ended it, as a cancel does.
func TestRunCommandNodeTakesALostResultFromTheCleanupRecord(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		execErr    error
		cleanup    string
		status     string
		wantExit   *int
		reasonHas  string
		wantKilled bool
	}{
		{name: "exited 0", execErr: ssh.ErrNoExitStatus, cleanup: runCleanupDone, status: CommandStatusSucceeded, wantExit: intPtr(0)},
		{name: "exited 3", execErr: errors.New("connection lost"), cleanup: "cleanup started=yes exit=3 killed=no", status: CommandStatusFailed, wantExit: intPtr(3), reasonHas: "status 3"},
		{name: "never started", execErr: ssh.ErrNoExitStatus, cleanup: "cleanup started=no exit=- killed=no", status: CommandStatusFailed, reasonHas: "before the command started"},
		{name: "ended by cleanup", execErr: ssh.ErrNoExitStatus, cleanup: "cleanup started=yes exit=- killed=yes", status: CommandStatusFailed, reasonHas: "cleanup ended", wantKilled: true},
		{name: "ended before cleanup", execErr: errors.New("connection lost"), cleanup: "cleanup started=yes exit=- killed=no", status: CommandStatusFailed, reasonHas: "lost after the command ended"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fix := newRunFixture(t)
			fix.exec = ssh.Result{Stdout: []byte("partial")}
			fix.execErr = tc.execErr
			fix.cleanup = tc.cleanup

			if stopped := fix.run(context.Background()); stopped {
				t.Fatal("the task stopped")
			}

			node := fix.vault.stored(t)
			if node.Phase != CommandPhaseSettled || node.Status != tc.status {
				t.Fatalf("phase %s status %s, want settled %s", node.Phase, node.Status, tc.status)
			}

			switch {
			case tc.wantExit == nil && node.ExitCode != nil:
				t.Errorf("exit code %d reported as the command's", *node.ExitCode)
			case tc.wantExit != nil && (node.ExitCode == nil || *node.ExitCode != *tc.wantExit):
				t.Errorf("exit code %v, want %d", node.ExitCode, *tc.wantExit)
			}

			if !strings.Contains(node.Reason, tc.reasonHas) {
				t.Errorf("reason %q lacks %q", node.Reason, tc.reasonHas)
			}

			if node.Killed != tc.wantKilled {
				t.Errorf("killed = %v, want %v", node.Killed, tc.wantKilled)
			}

			if node.OutputPath == "" || node.StdoutKept != len("partial") {
				t.Errorf("output %q kept %d, want the partial output kept", node.OutputPath, node.StdoutKept)
			}
		})
	}
}

// An exec that ends without an exit status, with a cleanup that can't
// reach the node, strands the node in an unfinished task as a cancel does.
// The node stays sent, the instance stays held, the task pauses, and the
// partial output is kept. A resume runs cleanup only, never exec again,
// and the instance is released once cleanup settles the node.
func TestCommandLostResultWithUnreachableCleanupKeepsTheHold(t *testing.T) {
	fix := newTaskFixture(t, 2, 1)

	var down atomic.Bool

	down.Store(true)

	fix.override = func(_ context.Context, _ string, _ int, op string) *opAnswer {
		switch {
		case op == opRunExec:
			return &opAnswer{result: ssh.Result{Stdout: []byte("partial")}, err: ssh.ErrNoExitStatus}
		case op == opRunCleanup && down.Load():
			return &opAnswer{err: errors.New("VM unreachable")}
		}

		return nil
	}

	fix.queue()

	paused := fix.waitFor("the task to pause", func(task UpgradeTask) bool { return task.Paused })

	if paused.Status != TaskStatusPaused {
		t.Errorf("the task is %s, want paused", paused.Status)
	}

	node := nodeOf(paused, 0, 0)
	if node.Phase != CommandPhaseSent {
		t.Errorf("the node is %s, want it left sent", node.Phase)
	}

	if node.StdoutKept != len("partial") {
		t.Errorf("the node kept %d bytes of output, want the partial output", node.StdoutKept)
	}

	if got := paused.Instances[0].Error; !strings.Contains(got, "Resume cleanup") {
		t.Errorf("the instance says %q, want it to say how to resume the cleanup", got)
	}

	if _, held := fix.manager.Guard().Holder("inst-0", "dep-0"); !held {
		t.Fatal("a lost exec with a cleanup that failed released the instance")
	}

	down.Store(false)
	fix.resume()

	done := fix.waitFor("the node to settle", func(task UpgradeTask) bool {
		return nodeOf(task, 0, 0).Phase == CommandPhaseSettled && task.Paused
	})

	if got := nodeOf(done, 0, 0); got.Status != CommandStatusFailed || got.StdoutKept != len("partial") {
		t.Errorf("the node is %s with %d bytes kept, want failed with the partial output", got.Status, got.StdoutKept)
	}

	if n := fix.countOps("dep-0", 0, opRunExec); n != 1 {
		t.Errorf("exec was sent %d times, want once", n)
	}

	if ops := fix.opsOf("dep-0", 0); ops[len(ops)-1] != opRunCleanup {
		t.Errorf("the resume ended with %s, want cleanup", ops[len(ops)-1])
	}

	if got := fix.opsOf("dep-1", 0); len(got) != 0 {
		t.Errorf("the resume sent %v to the next instance", got)
	}

	fix.waitReleased()
	fix.assertNoCommandText()
}

// Whatever ended exec without an exit status, a cleanup that can't reach
// the node leaves it sent and pauses the task.
func TestRunCommandNodeStrandsALostResultCleanupCannotReach(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		execErr error
	}{
		{name: "no exit status", execErr: ssh.ErrNoExitStatus},
		{name: "lost connection", execErr: errors.New("connection lost")},
		{name: "deadline", execErr: fmt.Errorf("%w: %w", ssh.ErrRunnerClosed, context.DeadlineExceeded)},
		{name: "stopping", execErr: fmt.Errorf("%w: context canceled", ssh.ErrRunnerClosed)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fix := newRunFixture(t)
			fix.exec = ssh.Result{Stdout: []byte("partial")}
			fix.execErr = tc.execErr
			fix.cleanupErr = errors.New("VM unreachable")

			if stopped := fix.run(context.Background()); !stopped {
				t.Fatal("the task went on")
			}

			node := fix.vault.stored(t)
			if node.Phase != CommandPhaseSent || node.StdoutKept != len("partial") {
				t.Errorf("phase %s kept %d, want sent with the partial output", node.Phase, node.StdoutKept)
			}

			fix.manager.mu.RLock()
			status, paused := fix.task.Status, fix.task.Paused
			fix.manager.mu.RUnlock()

			if status != TaskStatusPaused || !paused {
				t.Errorf("the task is %s paused=%v, want paused", status, paused)
			}
		})
	}
}

// What a cleanup record proves about a node whose exec ended without an
// exit status, whether a cancel, a lost connection, or a deadline ended it.
// An exit status, a command that never started, a group cleanup ended, and
// killed=no, which says cleanup found the command's session empty, settle
// the node. killed=unknown, or any killed value run.sh does not print, says
// cleanup could not check, and the node strands with the instance held so
// Resume cleanup can check again. started=- says the record is gone, so
// the node strands with a message that recreating the VM releases it.
func TestCommandClassifyTakesOnlyEvidenceFromTheRecord(t *testing.T) {
	t.Parallel()

	const stranded = "strand"

	cases := []struct {
		record    string
		lost      string
		cancelled string
		wantExit  *int
		reasonHas string
		strandAs  string
	}{
		{record: "cleanup started=yes exit=0 killed=no", lost: CommandStatusSucceeded, cancelled: CommandStatusSucceeded, wantExit: intPtr(0)},
		{record: "cleanup started=yes exit=3 killed=no", lost: CommandStatusFailed, cancelled: CommandStatusFailed, wantExit: intPtr(3)},
		{record: "cleanup started=yes exit=0 killed=unknown", lost: CommandStatusSucceeded, cancelled: CommandStatusSucceeded, wantExit: intPtr(0)},
		{record: "cleanup started=yes exit=3 killed=unknown", lost: CommandStatusFailed, cancelled: CommandStatusFailed, wantExit: intPtr(3)},
		{record: "cleanup started=yes exit=- killed=yes", lost: CommandStatusFailed, cancelled: CommandStatusFailed, reasonHas: "ended the command's process group"},
		{record: "cleanup started=yes exit=7 killed=yes", lost: CommandStatusFailed, cancelled: CommandStatusFailed},
		{record: "cleanup started=no exit=- killed=no", lost: CommandStatusFailed, cancelled: CommandStatusFailed, reasonHas: "before the command started"},
		{record: "cleanup started=no exit=125 killed=no", lost: CommandStatusFailed, cancelled: CommandStatusFailed},
		{record: "cleanup started=yes exit=- killed=no", lost: CommandStatusFailed, cancelled: CommandStatusFailed, reasonHas: "lost after the command ended"},
		{record: "cleanup started=yes exit=- killed=unknown", lost: stranded, cancelled: stranded, strandAs: strandUnproven},
		{record: "cleanup started=yes exit=- killed=maybe", lost: stranded, cancelled: stranded, strandAs: strandUnproven},
		{record: "cleanup started=- exit=- killed=no", lost: stranded, cancelled: stranded, strandAs: strandGone},
		{record: "cleanup started=- exit=- killed=unknown", lost: stranded, cancelled: stranded, strandAs: strandGone},
		{record: "cleanup started=yes exit=- killed=survived", lost: CommandStatusFailed, cancelled: CommandStatusFailed},
	}

	for _, tc := range cases {
		record, err := scripts.ParseCleanup([]byte(tc.record + "\n"))
		if err != nil {
			t.Fatalf("%s: %v", tc.record, err)
		}

		for _, cancelled := range []bool{false, true} {
			want := tc.lost
			if cancelled {
				want = tc.cancelled
			}

			run := &commandRun{spec: CommandTaskSpec{TimeoutSeconds: runTimeoutSecs}, cancelled: cancelled}
			outcome := run.classify(ssh.Result{}, ssh.ErrNoExitStatus, false, record, nil, false)

			got := outcome.status
			if outcome.strand {
				got = stranded
			}

			if got != want {
				t.Errorf("%s, cancelled %v: got %q, want %q", tc.record, cancelled, got, want)
			}

			if outcome.strand && outcome.reason != tc.strandAs {
				t.Errorf("%s, cancelled %v: stranded as %q, want %q", tc.record, cancelled, outcome.reason, tc.strandAs)
			}

			if !outcome.strand && !strings.Contains(outcome.reason, tc.reasonHas) {
				t.Errorf("%s, cancelled %v: reason %q lacks %q", tc.record, cancelled, outcome.reason, tc.reasonHas)
			}

			switch {
			case tc.wantExit == nil && outcome.exit != nil:
				t.Errorf("%s, cancelled %v: exit %d taken as the command's", tc.record, cancelled, *outcome.exit)
			case tc.wantExit != nil && (outcome.exit == nil || *outcome.exit != *tc.wantExit):
				t.Errorf("%s, cancelled %v: exit %v, want %d", tc.record, cancelled, outcome.exit, *tc.wantExit)
			}
		}
	}
}

// A lost result whose cleanup could not check the command, or found its
// directory gone, strands the node in an unfinished task. The instance
// stays held, the task pauses with a message that says why and how to go
// on, the node keeps what the record lacked, and the partial output is
// kept. A store of the output that fails changes none of that.
func TestRunCommandNodeStrandsALostResultTheRecordDoesNotSettle(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		record   string
		unproven string
		says     string
	}{
		{"cleanup started=yes exit=- killed=unknown", unprovenUnknown, "Use Resume cleanup to check the node again"},
		{"cleanup started=- exit=- killed=no", unprovenGone, "Recreating the VM releases it"},
	} {
		for _, storeFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s store fails %v", tc.record, storeFails), func(t *testing.T) {
				t.Parallel()

				fix := newRunFixture(t)
				fix.exec = ssh.Result{Stdout: []byte("partial")}
				fix.execErr = ssh.ErrNoExitStatus
				fix.cleanup = tc.record

				if storeFails {
					path, err := commandOutputPath(fix.task.ID, fix.inst.InstanceID, fix.node().Reviewed)
					if err != nil {
						t.Fatalf("output path: %v", err)
					}

					fix.vault.scriptPut(path, 100, 0)
				}

				if stopped := fix.run(context.Background()); !stopped {
					t.Fatal("the task went on")
				}

				node := fix.vault.stored(t)
				if node.Phase != CommandPhaseSent || node.StdoutKept != len("partial") || node.Unproven != tc.unproven {
					t.Errorf("phase %s kept %d unproven %q, want sent with the partial output and %q", node.Phase, node.StdoutKept, node.Unproven, tc.unproven)
				}

				fix.manager.mu.RLock()
				status, paused, message := fix.task.Status, fix.task.Paused, fix.inst.Error
				fix.manager.mu.RUnlock()

				if status != TaskStatusPaused || !paused {
					t.Errorf("the task is %s paused=%v, want paused", status, paused)
				}

				if !strings.Contains(message, tc.says) {
					t.Errorf("the instance says %q, want it to say %q", message, tc.says)
				}
			})
		}
	}
}

// A node stranded by a record that does not show the command ended is
// settled by the next Resume cleanup once a record does, and the instance
// then ends with the node's reason in place of the message of the strand.
func TestCommandUnprovenLostResultSettlesOnTheNextResume(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)

	fix.override = func(_ context.Context, _ string, _ int, op string) *opAnswer {
		if op == opRunExec {
			return &opAnswer{result: ssh.Result{Stdout: []byte("partial")}, err: ssh.ErrNoExitStatus}
		}

		return nil
	}
	fix.setCleanup("dep-0/0", "cleanup started=yes exit=- killed=unknown")

	fix.queue()

	paused := fix.waitFor("the task to pause", func(task UpgradeTask) bool { return task.Paused })
	if got := paused.Instances[0].Error; got != strandUnproven {
		t.Fatalf("the instance says %q, want the unproven strand message", got)
	}

	if !fix.holds() {
		t.Fatal("an unproven lost result released the instance")
	}

	fix.setCleanup("dep-0/0", cleanupEnded)
	fix.resume()

	ended := fix.waitFor("the instance to end", func(task UpgradeTask) bool { return task.Instances[0].Status.settled() })

	inst := ended.Instances[0]
	if inst.Status != InstanceStatusFailed || inst.Error != inst.Command.VMs[0].Reason {
		t.Errorf("the instance is %s saying %q, want failed with the node's reason %q", inst.Status, inst.Error, inst.Command.VMs[0].Reason)
	}

	if got := inst.Command.VMs[0].Unproven; got != "" {
		t.Errorf("the settled node still says the record was unproven: %q", got)
	}

	if n := fix.countOps("dep-0", 0, opRunExec); n != 1 {
		t.Errorf("exec was sent %d times, want once", n)
	}

	fix.waitReleased()
	fix.assertNoCommandText()
}

// strandUnprovenNode runs a task whose only node loses its result and whose
// cleanup can't check the command, and waits for the strand's pause.
func (f *taskFixture) strandUnprovenNode() {
	f.t.Helper()

	f.override = func(_ context.Context, _ string, _ int, op string) *opAnswer {
		if op == opRunExec {
			return &opAnswer{result: ssh.Result{Stdout: []byte("partial")}, err: ssh.ErrNoExitStatus}
		}

		return nil
	}
	f.setCleanup("dep-0/0", "cleanup started=yes exit=- killed=unknown")

	f.queue()
	f.waitFor("the strand to pause the task", func(task UpgradeTask) bool {
		return task.Paused && task.Instances[0].Error == strandUnproven
	})
}

// Resume cleanup settles a stranded sent node only on evidence that nothing
// the command started still runs: an exit status, killed=yes, killed=no, or
// started=no. A record that says cleanup could not check keeps the hold for
// another Resume cleanup, and started=- keeps it with a message that
// recreating the VM releases it, because no later cleanup can find a record
// that is gone.
func TestCommandResumeSettlesAStrandedNodeOnlyOnEvidence(t *testing.T) {
	cases := []struct {
		record   string
		settles  bool
		message  string
		unproven string
	}{
		{record: "cleanup started=yes exit=- killed=no", settles: true},
		{record: "cleanup started=yes exit=- killed=yes", settles: true},
		{record: "cleanup started=yes exit=4 killed=unknown", settles: true},
		{record: "cleanup started=no exit=- killed=no", settles: true},
		{record: "cleanup started=yes exit=- killed=unknown", message: strandUnproven, unproven: unprovenUnknown},
		{record: "cleanup started=yes exit=- killed=maybe", message: strandUnproven, unproven: unprovenUnknown},
		{record: "cleanup started=- exit=- killed=no", message: strandGone, unproven: unprovenGone},
	}

	for _, tc := range cases {
		t.Run(tc.record, func(t *testing.T) {
			fix := newTaskFixture(t, 1, 1)
			fix.strandUnprovenNode()

			fix.setCleanup("dep-0/0", tc.record)
			fix.resume()

			waitUntil(t, "the resume's cleanup", func() bool { return fix.countOps("dep-0", 0, opRunCleanup) == 2 })

			if tc.settles {
				ended := fix.waitFor("the instance to end", func(task UpgradeTask) bool { return task.Instances[0].Status.settled() })
				if node := nodeOf(ended, 0, 0); node.Phase != CommandPhaseSettled || node.Unproven != "" {
					t.Errorf("the node is %s unproven %q, want settled", node.Phase, node.Unproven)
				}

				fix.waitReleased()

				return
			}

			held := fix.waitFor("the task to pause again", func(task UpgradeTask) bool {
				return task.Paused && task.Instances[0].Error == tc.message
			})

			if node := nodeOf(held, 0, 0); node.Phase != CommandPhaseSent || node.Unproven != tc.unproven {
				t.Errorf("the node is %s unproven %q, want sent and %q", node.Phase, node.Unproven, tc.unproven)
			}

			if !fix.holds() {
				t.Error("a record that proves nothing released the instance")
			}

			if n := fix.countOps("dep-0", 0, opRunExec); n != 1 {
				t.Errorf("exec was sent %d times, want once", n)
			}
		})
	}
}

// A stranded node whose record is gone stays held through every Resume
// cleanup, and is released by the first one after the VM is recreated.
func TestCommandGoneRecordHeldUntilTheVMIsRecreated(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)
	fix.strandUnprovenNode()

	fix.setCleanup("dep-0/0", "cleanup started=- exit=- killed=no")

	for round := 2; round <= 3; round++ {
		fix.resume()
		waitUntil(t, "the resume's cleanup", func() bool { return fix.countOps("dep-0", 0, opRunCleanup) == round })
		fix.waitFor("the task to pause again", func(task UpgradeTask) bool {
			return task.Paused && task.Instances[0].Error == strandGone
		})

		if !fix.holds() {
			t.Fatalf("Resume cleanup %d released the instance on a record that is gone", round-1)
		}
	}

	fix.replaceVM("dep-0", 0)
	fix.resume()

	ended := fix.waitFor("the instance to end", func(task UpgradeTask) bool { return task.Instances[0].Status.settled() })
	if node := nodeOf(ended, 0, 0); !strings.Contains(node.Reason, "was replaced") || node.Unproven != "" {
		t.Errorf("the node says %q unproven %q, want it settled as replaced", node.Reason, node.Unproven)
	}

	fix.waitReleased()

	if n := fix.countOps("dep-0", 0, opRunExec); n != 1 {
		t.Errorf("exec was sent %d times, want once", n)
	}
}

// A cancelled task whose node strands on a record that proves nothing holds
// the instance with text that says why, in place of the text for a cleanup
// that did not finish. The node keeps why in Vault, so after a restart a
// Resume cleanup that can't reach the node still says it.
func TestCancelledHoldSaysWhyTheRecordProvesNothing(t *testing.T) {
	cases := []struct {
		record   string
		unproven string
		says     string
	}{
		{"cleanup started=yes exit=- killed=unknown", unprovenUnknown, "could not check whether the command still runs on redis/0"},
		{"cleanup started=- exit=- killed=no", unprovenGone, "Recreating the VM releases it"},
	}

	for _, tc := range cases {
		t.Run(tc.record, func(t *testing.T) {
			fix := newTaskFixture(t, 1, 1)
			script := fix.blockExec()
			fix.setCleanup("dep-0/0", tc.record)

			fix.queue()
			script.wait(t, 1)
			fix.cancel()

			held := fix.waitFor("the task to wait for a resume", func(task UpgradeTask) bool {
				return task.Paused && nodeOf(task, 0, 0).Phase == CommandPhaseSent && nodeOf(task, 0, 0).Unproven == tc.unproven
			})

			got := held.Instances[0].Error
			if held.Status != TaskStatusCancelled || !strings.Contains(got, tc.says) || strings.Contains(got, "did not finish") {
				t.Errorf("the %s task says %q, want it to say %q", held.Status, got, tc.says)
			}

			waitUntil(t, "the processor to let go", func() bool { return !fix.owned() })

			saved := fix.storedTask()
			if nodeOf(saved, 0, 0).Unproven != tc.unproven {
				t.Fatalf("Vault holds unproven %q, want %q", nodeOf(saved, 0, 0).Unproven, tc.unproven)
			}

			loaded := newTaskFixtureFrom(t, 1, 1, func(_ *taskFixture, task *UpgradeTask) {
				*task = saved
				task.CreationConfirmed = true
			})
			loaded.override = func(_ context.Context, _ string, _ int, op string) *opAnswer {
				if op == opRunCleanup {
					return &opAnswer{err: errNodeUnreachable}
				}

				return nil
			}

			loaded.resume()
			waitUntil(t, "the resume's cleanup", func() bool { return loaded.countOps("dep-0", 0, opRunCleanup) == 1 })
			waitUntil(t, "the cleanup to end", func() bool { return !loaded.settling() })

			again := loaded.snapshot()
			if got := again.Instances[0].Error; !again.Paused || !strings.Contains(got, tc.says) || strings.Contains(got, "did not finish") {
				t.Errorf("after a Resume cleanup that could not reach the node the task is paused=%v saying %q, want it to say %q", again.Paused, got, tc.says)
			}
		})
	}
}
