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
// An exit status, a command that never started, and a group cleanup ended
// settle the node. killed=no with no exit only says the wrapper did not
// signal the session, which it also says when it could not check it, and
// started=- says the record is gone, so neither proves the command ended
// and the node strands with the instance held.
func TestCommandClassifyTakesOnlyEvidenceFromTheRecord(t *testing.T) {
	t.Parallel()

	const stranded = "strand"

	cases := []struct {
		record    string
		lost      string
		cancelled string
		wantExit  *int
	}{
		{record: "cleanup started=yes exit=0 killed=no", lost: CommandStatusSucceeded, cancelled: CommandStatusSucceeded, wantExit: intPtr(0)},
		{record: "cleanup started=yes exit=3 killed=no", lost: CommandStatusFailed, cancelled: CommandStatusFailed, wantExit: intPtr(3)},
		{record: "cleanup started=yes exit=- killed=yes", lost: CommandStatusFailed, cancelled: CommandStatusFailed},
		{record: "cleanup started=yes exit=7 killed=yes", lost: CommandStatusFailed, cancelled: CommandStatusFailed},
		{record: "cleanup started=no exit=- killed=no", lost: CommandStatusFailed, cancelled: CommandStatusFailed},
		{record: "cleanup started=no exit=125 killed=no", lost: CommandStatusFailed, cancelled: CommandStatusFailed},
		{record: "cleanup started=yes exit=- killed=no", lost: stranded, cancelled: stranded},
		{record: "cleanup started=- exit=- killed=no", lost: stranded, cancelled: stranded},
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

			switch {
			case tc.wantExit == nil && outcome.exit != nil:
				t.Errorf("%s, cancelled %v: exit %d taken as the command's", tc.record, cancelled, *outcome.exit)
			case tc.wantExit != nil && (outcome.exit == nil || *outcome.exit != *tc.wantExit):
				t.Errorf("%s, cancelled %v: exit %v, want %d", tc.record, cancelled, outcome.exit, *tc.wantExit)
			}
		}
	}
}

// A lost result whose cleanup reports that it ended nothing, with no exit,
// strands the node in an unfinished task. The instance stays held, the
// task pauses with a message that says the command was not shown to have
// ended, and the partial output is kept.
func TestRunCommandNodeStrandsALostResultTheRecordDoesNotSettle(t *testing.T) {
	t.Parallel()

	for _, record := range []string{"cleanup started=yes exit=- killed=no", "cleanup started=- exit=- killed=no"} {
		t.Run(record, func(t *testing.T) {
			t.Parallel()

			fix := newRunFixture(t)
			fix.exec = ssh.Result{Stdout: []byte("partial")}
			fix.execErr = ssh.ErrNoExitStatus
			fix.cleanup = record

			if stopped := fix.run(context.Background()); !stopped {
				t.Fatal("the task went on")
			}

			node := fix.vault.stored(t)
			if node.Phase != CommandPhaseSent || node.StdoutKept != len("partial") {
				t.Errorf("phase %s kept %d, want sent with the partial output", node.Phase, node.StdoutKept)
			}

			fix.manager.mu.RLock()
			status, paused, message := fix.task.Status, fix.task.Paused, fix.inst.Error
			fix.manager.mu.RUnlock()

			if status != TaskStatusPaused || !paused {
				t.Errorf("the task is %s paused=%v, want paused", status, paused)
			}

			if !strings.Contains(message, "no evidence that the command ended") || !strings.Contains(message, "Resume cleanup") {
				t.Errorf("the instance says %q, want it to say why it is held and how to go on", message)
			}
		})
	}
}
