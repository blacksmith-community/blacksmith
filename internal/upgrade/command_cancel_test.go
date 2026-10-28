package upgrade

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/mutation"
)

// cancelScript plays the nodes of a task fixture whose exec runs until its
// context ends, and reports what the cancel reached.
type cancelScript struct {
	// started names each node as its exec begins.
	started chan string
	// cleanupDown makes cleanup unreachable while it is set.
	cleanupDown atomic.Bool

	mu sync.Mutex
	// execEnded holds each node whose exec ended with its context.
	execEnded map[string]bool
	// cleanupCtxErr holds the error of the context each cleanup was sent on.
	cleanupCtxErr map[string]error
}

var errNodeUnreachable = errors.New("the node is unreachable")

// blockExec makes the fixture's exec write what it has and wait for its
// context to end, and makes every cleanup record the state of its context.
func (f *taskFixture) blockExec() *cancelScript {
	script := &cancelScript{started: make(chan string, 64), execEnded: map[string]bool{}, cleanupCtxErr: map[string]error{}}

	f.override = func(ctx context.Context, deployment string, index int, op string) *opAnswer {
		key := nodeKey(deployment, index)

		switch op {
		case opRunExec:
			script.started <- key

			<-ctx.Done()

			script.mu.Lock()
			script.execEnded[key] = true
			script.mu.Unlock()

			partial := "partial output of " + key

			return &opAnswer{
				result: ssh.Result{Stdout: []byte(partial), StdoutReceived: int64(len(partial))},
				err:    fmt.Errorf("%w: %v", ssh.ErrRunnerClosed, ctx.Err()),
			}
		case opRunCleanup:
			script.mu.Lock()
			script.cleanupCtxErr[key] = ctx.Err()
			script.mu.Unlock()

			if script.cleanupDown.Load() {
				return &opAnswer{err: errNodeUnreachable}
			}
		}

		return nil
	}

	return script
}

// wait returns the next n nodes whose exec began.
func (s *cancelScript) wait(t *testing.T, n int) []string {
	t.Helper()

	var keys []string

	for range n {
		select {
		case key := <-s.started:
			keys = append(keys, key)
		case <-time.After(waitTimeout):
			t.Fatalf("only %d of %d nodes began exec", len(keys), n)
		}
	}

	return keys
}

func (s *cancelScript) cleanupCtx(key string) (error, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	err, ok := s.cleanupCtxErr[key]

	return err, ok
}

// cancel cancels the task the way an operator does.
func (f *taskFixture) cancel() {
	f.t.Helper()

	err := f.manager.CancelTask(context.Background(), f.task.ID)
	if err != nil {
		f.t.Fatalf("cancel: %v", err)
	}
}

// tryReserve asks the guard for the first instance as a certificate task
// would, and frees it again when it gets it.
func (f *taskFixture) tryReserve() error {
	f.manager.mu.Lock()
	defer f.manager.mu.Unlock()

	holder := mutation.Holder{Kind: mutation.KindCertificates, TaskID: "cert-task", Description: "certificate rotation task cert-task"}

	release, err := f.manager.guard.Acquire("inst-0", "dep-0", holder)
	if err != nil {
		return err //nolint:wrapcheck // the test matches the guard's error
	}

	release()

	return nil
}

func (f *taskFixture) waitReleased() {
	f.t.Helper()

	waitUntil(f.t, "the instance to be released", func() bool { return f.tryReserve() == nil })
}

func (f *taskFixture) wantBusy() {
	f.t.Helper()

	err := f.tryReserve()
	if !errors.Is(err, ErrInstanceBusy) {
		f.t.Fatalf("a certificate task got %v for the instance, want ErrInstanceBusy", err)
	}
}

func (f *taskFixture) waits() bool {
	f.manager.mu.RLock()
	defer f.manager.mu.RUnlock()

	return waitsCancelledLocked(f.task)
}

func (f *taskFixture) countOps(deployment string, index int, op string) int {
	n := 0

	for _, got := range f.opsOf(deployment, index) {
		if got == op {
			n++
		}
	}

	return n
}

// commandWatchers counts the goroutines that watch a node's cancel.
func commandWatchers() int {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]

	return strings.Count(string(buf), ".watchNodeCancel(")
}

func nodeOf(task UpgradeTask, instance, index int) CommandVM {
	return task.Instances[instance].Command.VMs[index]
}

// A cancel ends the context of the node's exec, the partial output the node
// wrote before it blocked is kept, cleanup runs on a context that has not
// ended, and the rest of the task is skipped.
func TestCommandCancelEndsTheNodeAndKeepsPartialOutput(t *testing.T) {
	fix := newTaskFixture(t, 2, 2)
	fix.cleanups["dep-0/0"] = "cleanup started=yes exit=- killed=yes"
	script := fix.blockExec()

	fix.queue()
	script.wait(t, 1)
	fix.cancel()

	done := fix.waitFor("the node to settle", func(task UpgradeTask) bool {
		return nodeOf(task, 0, 0).Phase == CommandPhaseSettled && task.Instances[0].Status == InstanceStatusCancelled
	})

	node := nodeOf(done, 0, 0)
	if node.Status != CommandStatusFailed || !node.Killed || node.ExitCode != nil {
		t.Errorf("node: status %s killed %v exit %v, want failed, killed, and no exit", node.Status, node.Killed, node.ExitCode)
	}

	if node.StdoutKept == 0 || node.OutputPath == "" {
		t.Errorf("node kept %d bytes at %q, want its partial output", node.StdoutKept, node.OutputPath)
	}

	out, err := fix.manager.ReadCommandOutput(context.Background(), fix.task.ID, "inst-0", "redis-0")
	if err != nil || string(out.Stdout) != "partial output of dep-0/0" {
		t.Errorf("the stored output is %q, err %v", out.Stdout, err)
	}

	if ctxErr, ok := script.cleanupCtx("dep-0/0"); !ok || ctxErr != nil {
		t.Errorf("cleanup ran on a context that had ended: %v, ran %v", ctxErr, ok)
	}

	if got := nodeOf(done, 0, 1).Phase; got != CommandPhaseSkipped {
		t.Errorf("the next node is %s, want skipped", got)
	}

	if got := done.Instances[1].Status; got != InstanceStatusSkipped {
		t.Errorf("the next instance is %s, want skipped", got)
	}

	if done.Status != TaskStatusCancelled || done.Paused {
		t.Errorf("task is %s, paused %v, want cancelled and not paused", done.Status, done.Paused)
	}

	if slices.Contains(fix.opsOf("dep-0", 1), opRunExec) || fix.sentTo("dep-1") {
		t.Error("a node after the cancelled one was sent the command")
	}

	fix.waitReleased()
	fix.assertNoCommandText()
}

// With parallel on, a cancel ends every node in flight, each gets cleanup
// on a fresh context, and each is recorded.
func TestCommandCancelReachesEveryNodeInFlight(t *testing.T) {
	fix := newTaskFixture(t, 1, 4, withParallel(3))

	for index := range 3 {
		fix.cleanups[nodeKey("dep-0", index)] = "cleanup started=yes exit=- killed=yes"
	}

	script := fix.blockExec()

	fix.queue()
	script.wait(t, 3)
	fix.cancel()

	done := fix.waitFor("every node in flight to settle and the instance to end", func(task UpgradeTask) bool {
		return nodeOf(task, 0, 0).Phase == CommandPhaseSettled && nodeOf(task, 0, 1).Phase == CommandPhaseSettled && nodeOf(task, 0, 2).Phase == CommandPhaseSettled &&
			task.Instances[0].Status == InstanceStatusCancelled
	})

	for index := range 3 {
		key := nodeKey("dep-0", index)
		node := nodeOf(done, 0, index)

		if node.Status != CommandStatusFailed || !node.Killed {
			t.Errorf("%s: status %s killed %v, want failed and killed", key, node.Status, node.Killed)
		}

		if want := "partial output of " + key; node.OutputPath == "" || node.StdoutKept != len(want) {
			t.Errorf("%s: kept %d bytes at %q, want %d", key, node.StdoutKept, node.OutputPath, len(want))
		}

		if ctxErr, ok := script.cleanupCtx(key); !ok || ctxErr != nil {
			t.Errorf("%s: cleanup ran on a context that had ended: %v, ran %v", key, ctxErr, ok)
		}
	}

	if got := nodeOf(done, 0, 3).Phase; got != CommandPhaseSkipped {
		t.Errorf("the node that never started is %s, want skipped", got)
	}

	fix.waitReleased()
	fix.assertNoCommandText()
}

// What cleanup finds on a node decides how a cancelled node is recorded.
func TestCommandCancelCorrectsTheRecordFromCleanup(t *testing.T) {
	cases := []struct {
		name       string
		cleanup    string
		status     string
		wantExit   *int
		wantKilled bool
		reasonHas  string
	}{
		{name: "ended by cleanup", cleanup: "cleanup started=yes exit=- killed=yes", status: CommandStatusFailed, wantKilled: true, reasonHas: "cancelled"},
		{name: "finished before the cancel landed", cleanup: "cleanup started=yes exit=0 killed=no", status: CommandStatusSucceeded, wantExit: intPtr(0)},
		{name: "failed before the cancel landed", cleanup: "cleanup started=yes exit=3 killed=no", status: CommandStatusFailed, wantExit: intPtr(3), reasonHas: "status 3"},
		{name: "never started", cleanup: "cleanup started=no exit=- killed=no", status: CommandStatusFailed, reasonHas: "before the command started"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fix := newTaskFixture(t, 1, 1)
			fix.cleanups["dep-0/0"] = tc.cleanup
			script := fix.blockExec()

			fix.queue()
			script.wait(t, 1)
			fix.cancel()

			done := fix.waitFor("the node to settle", func(task UpgradeTask) bool { return nodeOf(task, 0, 0).Phase == CommandPhaseSettled })
			node := nodeOf(done, 0, 0)

			if node.Status != tc.status || node.Killed != tc.wantKilled {
				t.Errorf("status %s killed %v, want %s and %v", node.Status, node.Killed, tc.status, tc.wantKilled)
			}

			switch {
			case tc.wantExit == nil && node.ExitCode != nil:
				t.Errorf("exit code %d, want none", *node.ExitCode)
			case tc.wantExit != nil && (node.ExitCode == nil || *node.ExitCode != *tc.wantExit):
				t.Errorf("exit code %v, want %d", node.ExitCode, *tc.wantExit)
			}

			if !strings.Contains(node.Reason, tc.reasonHas) {
				t.Errorf("reason %q does not say %q", node.Reason, tc.reasonHas)
			}

			fix.waitReleased()
			fix.assertNoCommandText()
		})
	}
}

// The instance stays reserved until the cleanup of every node in flight has
// returned, so a certificate task that asks in between is turned away.
func TestCommandCancelHoldsTheInstanceUntilEveryCleanupReturns(t *testing.T) {
	fix := newTaskFixture(t, 1, 2, withParallel(2))
	fix.cleanups["dep-0/0"] = "cleanup started=yes exit=- killed=yes"
	fix.cleanups["dep-0/1"] = "cleanup started=yes exit=- killed=yes"
	script := fix.blockExec()
	gate := fix.gate(opRunCleanup)

	fix.queue()
	script.wait(t, 2)
	fix.wantBusy()
	fix.cancel()

	first, second := gate.next(), gate.next()

	fix.wantBusy()
	gate.let(first)

	fix.waitFor("the first node to settle", func(task UpgradeTask) bool {
		return nodeOf(task, 0, 0).Phase == CommandPhaseSettled || nodeOf(task, 0, 1).Phase == CommandPhaseSettled
	})

	// One cleanup has returned and the other has not, so the command may
	// still be running on the second node.
	fix.wantBusy()

	gate.let(second)
	fix.waitFor("both nodes to settle", func(task UpgradeTask) bool {
		return nodeOf(task, 0, 0).Phase == CommandPhaseSettled && nodeOf(task, 0, 1).Phase == CommandPhaseSettled
	})

	fix.waitReleased()
	fix.assertNoCommandText()
}

// A cleanup that can't reach the node leaves the task cancelled and paused
// with the instance held, and says so. A resume sends cleanup only and then
// releases the instance.
func TestCommandCancelKeepsASentNodeHeldWhenCleanupFails(t *testing.T) {
	fix := newTaskFixture(t, 1, 2)
	fix.cleanups["dep-0/0"] = "cleanup started=yes exit=- killed=yes"
	script := fix.blockExec()
	script.cleanupDown.Store(true)

	fix.queue()
	script.wait(t, 1)
	fix.cancel()

	held := fix.waitFor("the task to wait for a resume", func(task UpgradeTask) bool { return task.Paused })

	if held.Status != TaskStatusCancelled {
		t.Errorf("task is %s, want cancelled", held.Status)
	}

	if got := nodeOf(held, 0, 0).Phase; got != CommandPhaseSent {
		t.Errorf("node is %s, want it left sent", got)
	}

	if !fix.waits() {
		t.Error("waitsCancelledLocked is false for a cancelled task with a node that waits for cleanup")
	}

	if got := held.Instances[0].Error; !strings.Contains(got, "held pending cleanup on redis/0") {
		t.Errorf("the status text is %q, want it to say the instance is held pending cleanup on redis/0", got)
	}

	fix.wantBusy()

	waitUntil(t, "the processor to let go", func() bool { return !fix.owned() })

	fix.wantBusy()

	script.cleanupDown.Store(false)
	fix.resume()

	done := fix.waitFor("the node to settle", func(task UpgradeTask) bool { return nodeOf(task, 0, 0).Phase == CommandPhaseSettled })

	if got := nodeOf(done, 0, 0); got.Status != CommandStatusFailed {
		t.Errorf("node is %s, want failed", got.Status)
	}

	if done.Status != TaskStatusCancelled || done.Paused {
		t.Errorf("task is %s, paused %v, want cancelled and not paused", done.Status, done.Paused)
	}

	if n := fix.countOps("dep-0", 0, opRunExec); n != 1 {
		t.Errorf("exec was sent %d times, want once", n)
	}

	if n := fix.countOps("dep-0", 0, opRunPrepare); n != 1 {
		t.Errorf("prepare was sent %d times, want once", n)
	}

	ops := fix.opsOf("dep-0", 0)
	if ops[len(ops)-1] != opRunCleanup {
		t.Errorf("the resume ended with %s, want cleanup", ops[len(ops)-1])
	}

	if got := fix.opsOf("dep-0", 1); len(got) != 0 {
		t.Errorf("the resume sent %v to a node that never started", got)
	}

	fix.waitReleased()

	if got := fix.snapshot().Instances[0].Status; got != InstanceStatusCancelled {
		t.Errorf("instance is %s, want cancelled", got)
	}

	fix.assertNoCommandText()
}

// A node cancelled after prepare returned and before its sent save is
// staged, which holds the instance as a sent node does, because the command
// text sits in cmd.sh on the VM. No exec is ever sent, and the resume sends
// cleanup only.
func TestCommandCancelKeepsAStagedNodeHeldWhenCleanupFails(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)
	script := fix.blockExec()
	script.cleanupDown.Store(true)

	var prepared atomic.Bool

	arrived := make(chan struct{})

	var blocked atomic.Bool

	previous := fix.onOp
	fix.onOp = func(deployment string, index int, op string) {
		if previous != nil {
			previous(deployment, index, op)
		}

		if op == opRunPrepare {
			prepared.Store(true)
		}
	}
	fix.authHook = func(ctx context.Context) error {
		if !prepared.Load() {
			return nil
		}

		// Only the check before the sent save is held, and a check of a
		// resume goes through.
		if !blocked.CompareAndSwap(false, true) {
			return nil
		}

		close(arrived)

		<-ctx.Done()

		return fmt.Errorf("the check was cut short: %w", ctx.Err())
	}

	fix.queue()

	select {
	case <-arrived:
	case <-time.After(waitTimeout):
		t.Fatal("the driver never reached the check before the sent save")
	}

	fix.cancel()

	held := fix.waitFor("the task to wait for a resume", func(task UpgradeTask) bool { return task.Paused })

	if got := nodeOf(held, 0, 0).Phase; got != CommandPhaseStaged {
		t.Errorf("node is %s, want it left staged", got)
	}

	if held.Status != TaskStatusCancelled {
		t.Errorf("task is %s, want cancelled", held.Status)
	}

	if !fix.waits() {
		t.Error("waitsCancelledLocked is false for a cancelled task with a staged node")
	}

	if got := held.Instances[0].Error; !strings.Contains(got, "held pending cleanup on redis/0") {
		t.Errorf("the status text is %q, want it to say the instance is held pending cleanup on redis/0", got)
	}

	fix.wantBusy()

	waitUntil(t, "the processor to let go", func() bool { return !fix.owned() })
	fix.wantBusy()

	script.cleanupDown.Store(false)
	fix.resume()

	fix.waitFor("the node to settle", func(task UpgradeTask) bool { return nodeOf(task, 0, 0).Phase == CommandPhaseSettled })

	if n := fix.countOps("dep-0", 0, opRunExec); n != 0 {
		t.Errorf("exec was sent %d times to a node cancelled before its sent save", n)
	}

	ops := fix.opsOf("dep-0", 0)
	if want := []string{opSudoCheck, opRunPrepare, opRunCleanup}; !slices.Equal(ops[:3], want) || ops[len(ops)-1] != opRunCleanup {
		t.Errorf("the node saw %v, want %v and then cleanup only", ops, want)
	}

	fix.waitReleased()
	fix.assertNoCommandText()
}

// A node cancelled between prepare and its sent save, whose cleanup does
// answer, is cleaned and settled and the instance is released.
func TestCommandCancelBeforeTheSentSaveCleansTheStagedNode(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)

	var prepared atomic.Bool

	arrived := make(chan struct{})

	var blocked atomic.Bool

	fix.onOp = func(_ string, _ int, op string) {
		if op == opRunPrepare {
			prepared.Store(true)
		}
	}
	fix.authHook = func(ctx context.Context) error {
		if !prepared.Load() {
			return nil
		}

		// Only the check before the sent save is held, and a check of a
		// resume goes through.
		if !blocked.CompareAndSwap(false, true) {
			return nil
		}

		close(arrived)

		<-ctx.Done()

		return fmt.Errorf("the check was cut short: %w", ctx.Err())
	}

	fix.queue()

	select {
	case <-arrived:
	case <-time.After(waitTimeout):
		t.Fatal("the driver never reached the check before the sent save")
	}

	fix.cancel()

	done := fix.waitFor("the node to settle", func(task UpgradeTask) bool { return nodeOf(task, 0, 0).Phase == CommandPhaseSettled })

	if n := fix.countOps("dep-0", 0, opRunExec); n != 0 {
		t.Errorf("exec was sent %d times to a node cancelled before its sent save", n)
	}

	if got := nodeOf(done, 0, 0); got.Status != CommandStatusFailed || !strings.Contains(got.Reason, "cancel") {
		t.Errorf("node is %s with reason %q, want failed because of the cancel", got.Status, got.Reason)
	}

	fix.waitReleased()
	fix.assertNoCommandText()
}

// Every watcher of a node's cancel ends with its node, whether the node was
// cancelled or ran to its end.
func TestCommandNodeWatchersEndWithTheirNodes(t *testing.T) {
	fix := newTaskFixture(t, 1, 2, withParallel(2))
	gate := fix.gate(opRunExec)

	before := commandWatchers()

	fix.queue()
	gate.next()
	gate.next()

	if got := commandWatchers() - before; got != 2 {
		t.Fatalf("%d watchers ran for two nodes in flight, want 2", got)
	}

	gate.releaseAll()
	fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })
	waitUntil(t, "the watchers to end", func() bool { return commandWatchers() == before })

	cancelled := newTaskFixture(t, 1, 2, withParallel(2))
	script := cancelled.blockExec()

	cancelled.queue()
	script.wait(t, 2)
	cancelled.cancel()
	cancelled.waitFor("both nodes to settle", func(task UpgradeTask) bool {
		return nodeOf(task, 0, 0).Phase == CommandPhaseSettled && nodeOf(task, 0, 1).Phase == CommandPhaseSettled
	})
	cancelled.waitReleased()

	waitUntil(t, "the watchers of the cancelled nodes to end", func() bool { return commandWatchers() == before })
}

// A pause that lands while a node runs lets it finish, and no node that has
// not started begins until the resume.
func TestCommandPauseBeforeANodeStartsHoldsTheNode(t *testing.T) {
	fix := newTaskFixture(t, 1, 3)
	gate := fix.gate(opRunExec, "dep-0/0")

	fix.queue()
	gate.next()

	err := fix.manager.PauseTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}

	gate.let("dep-0/0")

	waitUntil(t, "the loop to wait for the resume", func() bool { return strings.Contains(fix.log.joined(), "is paused, waiting") })

	paused := fix.snapshot()
	if got := phases(paused.Instances[0]); !slices.Equal(got, []CommandPhase{CommandPhaseSettled, CommandPhasePending, CommandPhasePending}) {
		t.Errorf("phases are %v, want the first node settled and the others pending", got)
	}

	if fix.sentTo("dep-1") || len(fix.opsOf("dep-0", 1)) != 0 {
		t.Error("a node started while the task was paused")
	}

	fix.resume()
	fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })
	fix.assertNoCommandText()
}

// With nodes in flight, a pause takes effect once they finish.
func TestCommandPauseWhileNodesAreInFlightTakesEffectOnceTheyFinish(t *testing.T) {
	fix := newTaskFixture(t, 1, 4, withParallel(2))
	gate := fix.gate(opRunExec)

	fix.queue()
	gate.next()
	gate.next()

	err := fix.manager.PauseTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}

	for _, key := range []string{"dep-0/0", "dep-0/1"} {
		gate.let(key)
	}

	waitUntil(t, "the loop to wait for the resume", func() bool { return strings.Contains(fix.log.joined(), "is paused, waiting") })

	paused := fix.snapshot()
	if got := phases(paused.Instances[0]); !slices.Equal(got, []CommandPhase{CommandPhaseSettled, CommandPhaseSettled, CommandPhasePending, CommandPhasePending}) {
		t.Errorf("phases are %v, want the two nodes in flight settled and the others pending", got)
	}

	gate.releaseAll()
	fix.resume()
	fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })
	fix.assertNoCommandText()
}

// Pausing and cancelling a command task never wait on the second
// credential, so a task whose switches were turned off can still be stopped.
func TestCommandPauseAndCancelNeedNoAuthorization(t *testing.T) {
	fix := newTaskFixture(t, 1, 2)
	gate := fix.gate(opRunExec, "dep-0/0")

	fix.queue()
	gate.next()

	var checks atomic.Int32

	fix.mu.Lock()
	fix.authHook = func(context.Context) error {
		checks.Add(1)

		return errors.New("commands are off")
	}
	fix.mu.Unlock()

	err := fix.manager.PauseTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}

	fix.cancel()
	gate.releaseAll()
	fix.waitFor("the node to settle", func(task UpgradeTask) bool { return nodeOf(task, 0, 0).Phase == CommandPhaseSettled })

	if n := checks.Load(); n != 0 {
		t.Errorf("pause and cancel asked the authorizer %d times", n)
	}
}

// A node whose cleanup could not answer after a cancel keeps the output it
// wrote before the cancel, on the node's record and in the store, and the
// resume that cleans it does not lose it.
func TestCommandCancelKeepsPartialOutputWhenCleanupFails(t *testing.T) {
	fix := newTaskFixture(t, 1, 2)
	fix.cleanups["dep-0/0"] = "cleanup started=yes exit=- killed=yes"
	script := fix.blockExec()
	script.cleanupDown.Store(true)

	fix.queue()
	script.wait(t, 1)
	fix.cancel()

	held := fix.waitFor("the task to wait for a resume", func(task UpgradeTask) bool { return task.Paused })

	const want = "partial output of dep-0/0"

	node := nodeOf(held, 0, 0)
	if node.Phase != CommandPhaseSent || node.StdoutKept != len(want) || node.OutputPath == "" {
		t.Errorf("held node is %s with %d bytes at %q, want it sent with its partial output kept", node.Phase, node.StdoutKept, node.OutputPath)
	}

	out, err := fix.manager.ReadCommandOutput(context.Background(), fix.task.ID, "inst-0", "redis-0")
	if err != nil || string(out.Stdout) != want {
		t.Errorf("the stored output is %q, err %v", out.Stdout, err)
	}

	script.cleanupDown.Store(false)
	fix.resume()

	done := fix.waitFor("the node to settle", func(task UpgradeTask) bool { return nodeOf(task, 0, 0).Phase == CommandPhaseSettled })

	node = nodeOf(done, 0, 0)
	if node.StdoutKept != len(want) || node.OutputPath == "" {
		t.Errorf("settled node kept %d bytes at %q, want the partial output the cancel left", node.StdoutKept, node.OutputPath)
	}

	out, err = fix.manager.ReadCommandOutput(context.Background(), fix.task.ID, "inst-0", "redis-0")
	if err != nil || string(out.Stdout) != want {
		t.Errorf("the stored output after the resume is %q, err %v", out.Stdout, err)
	}

	fix.waitReleased()
	fix.assertNoCommandText()
}

// lastSaved returns the task as the last write to Vault holds it.
func (f *taskFixture) lastSaved() UpgradeTask {
	saves := f.vault.savedTasks()
	if len(saves) == 0 {
		f.t.Fatal("nothing was saved to Vault")
	}

	return saves[len(saves)-1].task
}

// A resume that can't reach the node holds the task again, and the hold is
// saved, so a restart finds the task waiting for a resume.
func TestCommandCancelResumeThatCannotCleanSavesTheHold(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)
	script := fix.blockExec()
	script.cleanupDown.Store(true)

	fix.queue()
	script.wait(t, 1)
	fix.cancel()
	fix.waitFor("the task to wait for a resume", func(task UpgradeTask) bool { return task.Paused })
	waitUntil(t, "the processor to let go", func() bool { return !fix.owned() })

	fix.resume()

	if fix.lastSaved().Paused {
		t.Fatal("the resume saved the task as paused")
	}

	waitUntil(t, "the resume to hold the task again", func() bool { return fix.snapshot().Paused })
	waitUntil(t, "the hold to be saved", func() bool {
		saved := fix.lastSaved()

		return saved.Paused && strings.Contains(saved.Instances[0].Error, "held pending cleanup on redis/0")
	})

	if saved := fix.lastSaved(); saved.Status != TaskStatusCancelled || nodeOf(saved, 0, 0).Phase != CommandPhaseSent {
		t.Errorf("the saved task is %s with its node %s, want cancelled with the node sent", saved.Status, nodeOf(saved, 0, 0).Phase)
	}

	fix.assertNoCommandText()
}

// A resume that finds processes of the command alive after KILL settles the
// node, and the instance stays held, with the task waiting for Resume
// cleanup. The Resume cleanup that finds the processes ended ends the
// instance as cancelled, skips its pending nodes, and releases it.
func TestCommandCancelResumeKeepsAnInstanceWhoseProcessesSurvived(t *testing.T) {
	fix := newTaskFixture(t, 1, 2)
	script := fix.blockExec()
	script.cleanupDown.Store(true)

	fix.queue()
	script.wait(t, 1)
	fix.cancel()
	fix.waitFor("the task to wait for a resume", func(task UpgradeTask) bool { return task.Paused })
	waitUntil(t, "the processor to let go", func() bool { return !fix.owned() })

	fix.setCleanup("dep-0/0", cleanupSurvived)
	script.cleanupDown.Store(false)

	fix.resume()

	held := fix.waitFor("the survivor to hold the task", func(task UpgradeTask) bool {
		return task.Paused && nodeOf(task, 0, 0).Phase == CommandPhaseSettled
	})

	if got := nodeOf(held, 0, 0); !got.Survived {
		t.Errorf("node 0 is %s, survived %v, want settled with survivors recorded", got.Phase, got.Survived)
	}

	if held.Status != TaskStatusCancelled || held.Instances[0].Status != InstanceStatusRunning {
		t.Errorf("the task is %s with its instance %s, want cancelled with the instance open", held.Status, held.Instances[0].Status)
	}

	waitUntil(t, "the cleanup to end", func() bool { return !fix.settling() })
	fix.wantBusy()

	fix.setCleanup("dep-0/0", cleanupEnded)
	fix.resume()

	done := fix.waitFor("the instance to end", func(task UpgradeTask) bool { return task.Instances[0].Status == InstanceStatusCancelled })

	if got := nodeOf(done, 0, 1).Phase; got != CommandPhaseSkipped {
		t.Errorf("node 1 is %s, want skipped", got)
	}

	if done.Status != TaskStatusCancelled || done.Paused || done.CancelledCount != 1 {
		t.Errorf("task is %s, paused %v, cancelled count %d, want cancelled, not paused, and 1", done.Status, done.Paused, done.CancelledCount)
	}

	fix.waitReleased()
	waitUntil(t, "the end to be saved", func() bool { return fix.lastSaved().Instances[0].Status == InstanceStatusCancelled })
	fix.assertNoCommandText()
}

// signalsPending counts the values on the task's cancel channel that no
// receiver has taken.
func (f *taskFixture) signalsPending() int {
	f.manager.taskCancelLock.RLock()
	defer f.manager.taskCancelLock.RUnlock()

	return len(f.manager.taskCancelCh[f.task.ID])
}

// A pause lets the nodes in flight run on, so a command running as root is
// never killed by a pause. The value the pause puts on the task's channel
// stays there for the processor, because no node's watcher reads it.
func TestCommandPauseLeavesTheNodesInFlightRunning(t *testing.T) {
	fix := newTaskFixture(t, 1, 3, withParallel(2))
	release := make(chan struct{})
	started := make(chan string, 8)

	var ended sync.Map

	fix.override = func(ctx context.Context, deployment string, index int, op string) *opAnswer {
		if op != opRunExec {
			return nil
		}

		key := nodeKey(deployment, index)
		started <- key

		select {
		case <-ctx.Done():
			ended.Store(key, ctx.Err())
		case <-release:
			if err := ctx.Err(); err != nil {
				ended.Store(key, err)
			}
		}

		return nil
	}

	fix.queue()

	for range 2 {
		select {
		case <-started:
		case <-time.After(waitTimeout):
			t.Fatal("two nodes never began exec")
		}
	}

	err := fix.manager.PauseTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}

	// The processor waits for a slot, not on the channel, so only a
	// watcher could have taken the value.
	if got := fix.signalsPending(); got != 1 {
		t.Errorf("%d values wait on the task's channel after the pause, want 1", got)
	}

	close(release)

	waitUntil(t, "the loop to wait for the resume", func() bool { return strings.Contains(fix.log.joined(), "is paused, waiting") })

	ended.Range(func(key, err any) bool {
		t.Errorf("the pause ended the context of node %v: %v", key, err)

		return true
	})

	paused := fix.snapshot()
	for index := range 2 {
		if got := nodeOf(paused, 0, index); got.Status != CommandStatusSucceeded {
			t.Errorf("node %d is %s, want succeeded", index, got.Status)
		}
	}

	if got := nodeOf(paused, 0, 2).Phase; got != CommandPhasePending {
		t.Errorf("node 2 is %s, want pending", got)
	}

	fix.assertNoCommandText()
}

// A node that was about to start when the task was cancelled is skipped
// because of the cancel, and nothing is sent to it, whatever the next step
// of the node would have said.
func TestCommandCancelBeforeANodeStartsSkipsItForTheCancel(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)

	arrived := make(chan struct{})
	hold := make(chan struct{})

	var blocked atomic.Bool

	fix.authHook = func(context.Context) error {
		// Only the first check of the node's driver is held, not the
		// dispatcher's.
		buf := make([]byte, 1<<14)
		if !strings.Contains(string(buf[:runtime.Stack(buf, false)]), "driveCommandNode") || !blocked.CompareAndSwap(false, true) {
			return nil
		}

		close(arrived)

		<-hold

		return nil
	}
	t.Cleanup(func() {
		select {
		case <-hold:
		default:
			close(hold)
		}
	})

	fix.queue()

	select {
	case <-arrived:
	case <-time.After(waitTimeout):
		t.Fatal("the driver never reached its first check")
	}

	fix.cancel()
	close(hold)

	done := fix.waitFor("the instance to end", func(task UpgradeTask) bool { return task.Instances[0].Status == InstanceStatusCancelled })

	node := nodeOf(done, 0, 0)
	if node.Phase != CommandPhaseSkipped || !strings.Contains(node.Reason, "cancelled") {
		t.Errorf("node is %s with reason %q, want skipped because of the cancel", node.Phase, node.Reason)
	}

	if got := fix.opsOf("dep-0", 0); len(got) != 0 {
		t.Errorf("a cancelled node was sent %v", got)
	}

	fix.waitReleased()
	fix.assertNoCommandText()
}

// A task that paused because a node could not be reached and is then
// cancelled says its instance is held pending cleanup, in place of the
// message of the pause.
func TestCommandCancelOfAPausedTaskRewordsTheHeldInstance(t *testing.T) {
	fix := newTaskFixture(t, 1, 2)

	const pauseText = "Blacksmith could not reach the node to clean up after an earlier run, so it is still waiting for a resume."

	fix.manager.mu.Lock()
	fix.task.Status = TaskStatusPaused
	fix.task.Paused = true
	fix.task.Instances[0].Status = InstanceStatusRunning
	fix.task.Instances[0].Error = pauseText
	fix.task.Instances[0].Command.VMs[0].Phase = CommandPhaseSent
	fix.manager.mu.Unlock()

	fix.cancel()

	got := fix.snapshot()
	if got.Status != TaskStatusCancelled || !got.Paused {
		t.Errorf("task is %s, paused %v, want cancelled and waiting for a resume", got.Status, got.Paused)
	}

	if text := got.Instances[0].Error; !strings.Contains(text, "held pending cleanup on redis/0") {
		t.Errorf("the status text is %q, want it to say the instance is held pending cleanup on redis/0", text)
	}

	if text := got.Instances[0].Error; !strings.Contains(text, "Resume cleanup") {
		t.Errorf("the status text is %q, want it to point at the Resume cleanup control", text)
	}

	if ids := got.ToSummary().HeldInstanceIDs; !slices.Equal(ids, []string{got.Instances[0].InstanceID}) {
		t.Errorf("the summary names %v, want the instance held for cleanup", ids)
	}

	fix.wantBusy()
	fix.assertNoCommandText()
}

// A paused task that nothing processes and that holds nothing on a VM is
// cancelled. Its instance ends and its nodes are skipped, so nothing is
// left for a resume to do, and the task no longer reads as paused, in
// memory or in the saved record.
func TestCommandCancelOfAPausedTaskThatHoldsNothingEndsThePause(t *testing.T) {
	fix := newTaskFixture(t, 1, 2)

	fix.manager.mu.Lock()
	fix.task.Status = TaskStatusPaused
	fix.task.Paused = true
	fix.task.Instances[0].Status = InstanceStatusRunning
	fix.manager.mu.Unlock()

	fix.cancel()

	got := fix.snapshot()
	if got.Status != TaskStatusCancelled || got.Paused {
		t.Errorf("task is %s, paused %v, want cancelled and not paused", got.Status, got.Paused)
	}

	if got.Instances[0].Status != InstanceStatusCancelled {
		t.Errorf("instance is %s, want cancelled", got.Instances[0].Status)
	}

	for vm, node := range got.Instances[0].Command.VMs {
		if node.Phase != CommandPhaseSkipped {
			t.Errorf("node %d is %s, want skipped", vm, node.Phase)
		}
	}

	if saved := fix.lastSaved(); saved.Paused {
		t.Error("the saved record still says the task is paused")
	}

	err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err == nil {
		t.Error("a resume of a cancelled task with nothing to clean was accepted")
	}

	if got := fix.opsOf("dep-0", 0); len(got) != 0 {
		t.Errorf("a cancelled node was sent %v", got)
	}

	fix.waitReleased()
	fix.assertNoCommandText()
}

// A hold that Vault will not confirm is said on the instance, so the
// operator knows a restart may not find the task waiting.
func TestCommandCancelSaysWhenTheHoldIsNotConfirmed(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)
	script := fix.blockExec()
	script.cleanupDown.Store(true)

	fix.queue()
	script.wait(t, 1)

	fix.vault.mu.Lock()
	fix.vault.failPhase = CommandPhaseSent
	fix.vault.mu.Unlock()

	fix.cancel()

	held := fix.waitFor("the task to wait for a resume", func(task UpgradeTask) bool {
		return task.Paused && strings.Contains(task.Instances[0].Error, "not confirmed")
	})

	if text := held.Instances[0].Error; !strings.Contains(text, "held pending cleanup on redis/0") {
		t.Errorf("the status text is %q, want it to keep saying what the instance waits for", text)
	}

	fix.wantBusy()
	fix.assertNoCommandText()
}

func (f *taskFixture) setEnabled(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.cfg.Operation.On = &on
}

// holdCancelledTask cancels a task whose node can't be cleaned and waits for
// it to wait for a resume with nothing processing it.
func (f *taskFixture) holdCancelledTask(script *cancelScript) {
	f.t.Helper()

	script.cleanupDown.Store(true)

	f.queue()
	script.wait(f.t, 1)
	f.cancel()
	f.waitFor("the task to wait for a resume", func(task UpgradeTask) bool { return task.Paused })
	waitUntil(f.t, "the processor to let go", func() bool { return !f.owned() })

	script.cleanupDown.Store(false)
}

// Resuming a cancelled task only cleans its nodes, so it needs only the
// broker's own credential, as the cancel did. It is allowed with commands
// off and root runs off, never asks the second credential or the stored
// task's authorizer, and sends cleanup and nothing else, with the switches
// on as much as off.
func TestCommandCancelledResumeNeedsNoCommandAuthorization(t *testing.T) {
	for _, switches := range []string{"on", "off"} {
		t.Run(switches, func(t *testing.T) {
			fix := newTaskFixture(t, 1, 2, withRoot())
			fix.cleanups["dep-0/0"] = "cleanup started=yes exit=- killed=yes"
			script := fix.blockExec()

			fix.holdCancelledTask(script)

			if switches == "off" {
				fix.setEnabled(false)
				fix.setRoot(false)
			}

			var checks, gates atomic.Int32

			fix.mu.Lock()
			fix.authHook = func(context.Context) error {
				checks.Add(1)

				return errors.New("commands are off")
			}
			fix.mu.Unlock()

			err := fix.manager.ResumeTaskGated(context.Background(), fix.task.ID, func(context.Context) error {
				gates.Add(1)

				return errors.New("the second credential is wrong")
			})
			if err != nil {
				t.Fatalf("resume: %v", err)
			}

			done := fix.waitFor("the instance to end", func(task UpgradeTask) bool { return task.Instances[0].Status == InstanceStatusCancelled })

			if got := nodeOf(done, 0, 0); got.Phase != CommandPhaseSettled {
				t.Errorf("node is %s, want settled by the cleanup", got.Phase)
			}

			if n := checks.Load(); n != 0 {
				t.Errorf("the resume asked the authorizer %d times", n)
			}

			if n := gates.Load(); n != 0 {
				t.Errorf("the resume asked for the second credential %d times", n)
			}

			for index := range 2 {
				if n := fix.countOps("dep-0", index, opRunExec); n > 1 || (index == 1 && n != 0) {
					t.Errorf("node %d was sent exec %d times", index, n)
				}
			}

			if n := fix.countOps("dep-0", 0, opRunPrepare); n != 1 {
				t.Errorf("prepare was sent %d times, want only the one before the cancel", n)
			}

			if got := fix.opsOf("dep-0", 1); len(got) != 0 {
				t.Errorf("the resume sent %v to a node that never started", got)
			}

			ops := fix.opsOf("dep-0", 0)
			if ops[len(ops)-1] != opRunCleanup || fix.countOps("dep-0", 0, opRunExec) != 1 {
				t.Errorf("the node saw %v, want cleanup last and exec only from before the cancel", ops)
			}

			fix.waitReleased()
			fix.assertNoCommandText()
		})
	}
}

// A resume of a task that is not cancelled still needs the command switches
// and the second credential.
func TestCommandResumeOfAPausedTaskStillNeedsAuthorization(t *testing.T) {
	cases := []struct {
		name    string
		options []taskOption
		turnOff func(*taskFixture)
		code    string
	}{
		{name: "commands off", turnOff: func(f *taskFixture) { f.setEnabled(false) }, code: apierr.CodeCommandsDisabled},
		{name: "root off", options: []taskOption{withRoot()}, turnOff: func(f *taskFixture) { f.setRoot(false) }, code: apierr.CodeRootDisabled},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fix := newTaskFixture(t, 1, 2, tc.options...)
			gate := fix.gate(opRunExec, "dep-0/0")

			fix.queue()
			gate.next()

			err := fix.manager.PauseTask(context.Background(), fix.task.ID)
			if err != nil {
				t.Fatalf("pause: %v", err)
			}

			gate.let("dep-0/0")
			waitUntil(t, "the loop to wait for the resume", func() bool { return strings.Contains(fix.log.joined(), "is paused, waiting") })

			tc.turnOff(fix)

			err = fix.manager.ResumeTaskGated(context.Background(), fix.task.ID, nil)

			var refusal *apierr.Error
			if !errors.As(err, &refusal) || refusal.Code != tc.code {
				t.Fatalf("resume answered %v, want the %s refusal", err, tc.code)
			}

			if got := fix.snapshot(); !got.Paused || got.Status != TaskStatusPaused {
				t.Errorf("task is %s, paused %v, want it left paused", got.Status, got.Paused)
			}

			gated := errors.New("the second credential is wrong")

			fix.setEnabled(true)
			fix.setRoot(true)

			err = fix.manager.ResumeTaskGated(context.Background(), fix.task.ID, func(context.Context) error { return gated })
			if !errors.Is(err, gated) {
				t.Fatalf("resume answered %v, want the refusal of the second credential", err)
			}

			if got := fix.snapshot(); !got.Paused {
				t.Error("a resume the gate refused lifted the pause")
			}

			fix.assertNoCommandText()
		})
	}
}

// parkedIn counts the goroutines blocked in a select whose stack names fn.
func parkedIn(fn string) int {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]

	count := 0

	for _, stack := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(stack, "[select") && strings.Contains(stack, "."+fn+"(") {
			count++
		}
	}

	return count
}

// A resume wakes a paused processor at once while a node of the task is
// still in flight. The node's watcher parks first, so on a channel they
// shared it would take the resume's signal, and the processor would wait
// for its next poll, which here is an hour away.
func TestCommandResumeWithANodeInFlightWakesTheProcessorAtOnce(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)
	manager := fix.manager
	manager.pausePoll = time.Hour

	cancelCh := make(chan struct{}, 1)

	manager.taskCancelLock.Lock()
	manager.taskCancelCh[fix.task.ID] = cancelCh
	manager.taskDoneCh[fix.task.ID] = make(chan struct{})
	manager.taskCancelLock.Unlock()

	manager.mu.Lock()
	fix.task.Status = TaskStatusPaused
	fix.task.Paused = true
	manager.mu.Unlock()

	watchers := parkedIn("watchNodeCancel")

	nodeCtx, stop := manager.commandNodeContext(context.Background(), fix.task)
	defer stop()

	waitUntil(t, "the node's watcher to park", func() bool { return parkedIn("watchNodeCancel") > watchers })

	waiters := parkedIn("waitWhilePaused")
	woke := make(chan bool, 1)

	go func() {
		goOn, _ := manager.waitWhilePaused(context.Background(), fix.task, cancelCh)
		woke <- goOn
	}()

	waitUntil(t, "the processor to wait for the resume", func() bool { return parkedIn("waitWhilePaused") > waiters })

	manager.mu.Lock()
	fix.task.Status = TaskStatusRunning
	fix.task.Paused = false
	manager.mu.Unlock()

	manager.signalTask(fix.task.ID)

	select {
	case goOn := <-woke:
		if !goOn {
			t.Error("the processor stopped after the resume, want it to go on")
		}
	case <-time.After(waitTimeout):
		t.Fatal("the processor never woke for the resume")
	}

	if err := nodeCtx.Err(); err != nil {
		t.Errorf("the resume ended the node's context: %v", err)
	}
}
