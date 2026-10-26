package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/bosh/sshconn"
	"blacksmith/internal/ops/scripts"
	"blacksmith/pkg/logger"
)

const (
	runTaskID      = "7a1c0e52-3b4d-4c6e-8f90-a1b2c3d4e5f6"
	runInstanceID  = "instance-run-1"
	runDeployment  = "redis-instance-run-1"
	runVMID        = "vm-id-0"
	runTimeoutSecs = 30
	// runCommandText is the marker no log line, error, or Vault record
	// other than the task record may hold.
	runCommandText = "echo marker-9f3a71c2-secret-in-command"
)

// runCleanupDone is the cleanup record of a command that finished.
const runCleanupDone = "cleanup started=yes exit=0 killed=no"

// runCall is one call the fake session saw.
type runCall struct {
	seq         int64
	op          string
	args        []string
	stdin       []byte
	ctxErr      error
	deadline    time.Time
	hasDeadline bool
	at          time.Time
}

// runSession is a PatchSession that records every call, with a sequence
// number shared with the fake Vault.
type runSession struct {
	seq    *atomic.Int64
	hosts  []sshconn.Host
	answer func(ctx context.Context, call runCall) (ssh.Result, error)

	mu    sync.Mutex
	calls []runCall
}

func (s *runSession) VMs() []sshconn.Host { return slices.Clone(s.hosts) }

func (s *runSession) Close() error { return nil }

func (s *runSession) Run(ctx context.Context, _ int, op string, args []string, stdin io.Reader) (ssh.Result, error) {
	var input []byte

	if stdin != nil {
		read, err := io.ReadAll(stdin)
		if err != nil {
			return ssh.Result{}, fmt.Errorf("reading stdin: %w", err)
		}

		input = read
	}

	call := runCall{seq: s.seq.Add(1), op: op, args: slices.Clone(args), stdin: input, ctxErr: ctx.Err(), at: time.Now()}
	call.deadline, call.hasDeadline = ctx.Deadline()

	s.mu.Lock()
	s.calls = append(s.calls, call)
	s.mu.Unlock()

	return s.answer(ctx, call)
}

func (s *runSession) recorded() []runCall {
	s.mu.Lock()
	defer s.mu.Unlock()

	return slices.Clone(s.calls)
}

func (s *runSession) ops() []string {
	var ops []string

	for _, call := range s.recorded() {
		ops = append(ops, call.op)
	}

	return ops
}

func (s *runSession) call(t *testing.T, op string) runCall {
	t.Helper()

	for _, call := range s.recorded() {
		if call.op == op {
			return call
		}
	}

	t.Fatalf("no %s call was sent", op)

	return runCall{}
}

// runWrite is one write of the command task's record.
type runWrite struct {
	seq   int64
	phase CommandPhase
}

// runVault is the fake Vault of a command task. It numbers each write of
// the task record from the session's sequence, and refuses the writes
// whose node is in failPhase.
type runVault struct {
	*confirmVault

	seq    *atomic.Int64
	taskID string

	mu        sync.Mutex
	writes    []runWrite
	failPhase CommandPhase
}

func (v *runVault) Put(ctx context.Context, path string, data interface{}) error {
	if path == commandTasksVaultPath+"/"+v.taskID {
		raw, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}

		var task UpgradeTask

		err = json.Unmarshal(raw, &task)
		if err != nil {
			return fmt.Errorf("unmarshal: %w", err)
		}

		phase := task.Instances[0].Command.VMs[0].Phase

		v.mu.Lock()
		fail := v.failPhase != "" && v.failPhase == phase

		if !fail {
			v.writes = append(v.writes, runWrite{seq: v.seq.Add(1), phase: phase})
		}
		v.mu.Unlock()

		if fail {
			return errPutRefused
		}
	}

	return v.confirmVault.Put(ctx, path, data)
}

func (v *runVault) recorded() []runWrite {
	v.mu.Lock()
	defer v.mu.Unlock()

	return slices.Clone(v.writes)
}

// writeSeq returns the sequence number of the first write that saved the
// node in the phase.
func (v *runVault) writeSeq(t *testing.T, phase CommandPhase) int64 {
	t.Helper()

	for _, write := range v.recorded() {
		if write.phase == phase {
			return write.seq
		}
	}

	t.Fatalf("no write saved the node in phase %s", phase)

	return 0
}

// stored returns the node as the task record holds it in Vault.
func (v *runVault) stored(t *testing.T) CommandVM {
	t.Helper()

	var task UpgradeTask

	found, err := v.Get(context.Background(), commandTasksVaultPath+"/"+v.taskID, &task)
	if err != nil || !found {
		t.Fatalf("reading the task record: found=%v err=%v", found, err)
	}

	return task.Instances[0].Command.VMs[0]
}

// captureLog records every line a logger is given.
type captureLog struct {
	logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func (l *captureLog) add(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *captureLog) Debugf(format string, args ...interface{})   { l.add(format, args...) }
func (l *captureLog) Infof(format string, args ...interface{})    { l.add(format, args...) }
func (l *captureLog) Warnf(format string, args ...interface{})    { l.add(format, args...) }
func (l *captureLog) Warningf(format string, args ...interface{}) { l.add(format, args...) }
func (l *captureLog) Errorf(format string, args ...interface{})   { l.add(format, args...) }
func (l *captureLog) Fatalf(format string, args ...interface{})   { l.add(format, args...) }
func (l *captureLog) Debug(msg string, args ...interface{})       { l.add(msg, args...) }
func (l *captureLog) Info(msg string, args ...interface{})        { l.add(msg, args...) }
func (l *captureLog) Warn(msg string, args ...interface{})        { l.add(msg, args...) }
func (l *captureLog) Warning(msg string, args ...interface{})     { l.add(msg, args...) }
func (l *captureLog) Error(msg string, args ...interface{})       { l.add(msg, args...) }
func (l *captureLog) Fatal(msg string, args ...interface{})       { l.add(msg, args...) }

//nolint:ireturn // the logger interface returns itself
func (l *captureLog) WithField(string, interface{}) logger.Logger { return l }

//nolint:ireturn // the logger interface returns itself
func (l *captureLog) WithFields(map[string]interface{}) logger.Logger { return l }

//nolint:ireturn // the logger interface returns itself
func (l *captureLog) Named(string) logger.Logger { return l }

func (l *captureLog) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return strings.Join(l.lines, "\n")
}

// runClock is the manager's clock. The fake session moves it forward to
// make a command take time.
type runClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *runClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *runClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// runFixture is a manager with one command task of one node, and a fake
// session that answers each operation from the fixture's script.
type runFixture struct {
	t        *testing.T
	manager  *Manager
	vault    *runVault
	log      *captureLog
	clock    *runClock
	task     *UpgradeTask
	inst     *InstanceUpgrade
	sess     *runSession
	sessions *patchSessions

	opened [][]int

	// The script. A zero value is a node that succeeds.
	sudo         ssh.Result
	sudoErr      error
	stagedRecord string
	prepare      ssh.Result
	prepareErr   error
	exec         ssh.Result
	execErr      error
	execTakes    time.Duration
	execHook     func(ctx context.Context)
	cleanup      string
	cleanupExit  int
	cleanupErr   error
}

type runOption func(*runFixture, *CommandVM)

func newRunFixture(t *testing.T, options ...runOption) *runFixture {
	t.Helper()

	var seq atomic.Int64

	fix := &runFixture{
		t:       t,
		log:     &captureLog{},
		clock:   &runClock{now: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)},
		cleanup: runCleanupDone,
		exec:    ssh.Result{Stdout: []byte("out"), Stderr: []byte("er"), StdoutReceived: 3, StderrReceived: 500, StderrTruncated: true},
	}

	fix.vault = &runVault{confirmVault: newConfirmVault(), seq: &seq, taskID: runTaskID}

	command := runCommandText
	digest := commandDigest(command)

	node := CommandVM{
		Reviewed: CommandVMIdentity{Group: "redis", Index: 0, ID: runVMID, CID: "cid-0"},
		AZ:       "z1",
		Phase:    CommandPhasePending,
	}

	for _, option := range options {
		option(fix, &node)
	}

	identity := node.Reviewed
	if node.Matched.Group != "" {
		identity = node.Matched
	}

	fix.sess = &runSession{
		seq:    &seq,
		hosts:  []sshconn.Host{{Job: identity.Group, IndexOrID: identity.ID, Address: "10.0.0.10"}},
		answer: fix.answer,
	}

	fix.stagedRecord = "staged bytes=" + strconv.Itoa(len(command)) + " sha256=" + digest

	fix.task = &UpgradeTask{
		ID:     runTaskID,
		Name:   "run uptime",
		Kind:   TaskKindCommand,
		Status: TaskStatusRunning,
		Command: &CommandTaskSpec{
			Command:        command,
			CommandDigest:  digest,
			CommandLength:  len(command),
			RunAs:          "vcap",
			TimeoutSeconds: runTimeoutSecs,
		},
		Instances: []InstanceUpgrade{{
			InstanceID:     runInstanceID,
			InstanceName:   "redis",
			DeploymentName: runDeployment,
			Status:         InstanceStatusRunning,
			Command:        &InstanceCommand{Deployment: runDeployment, VMs: []CommandVM{node}},
		}},
		TotalCount: 1,
		CreatedAt:  fix.clock.read(),
	}
	fix.inst = &fix.task.Instances[0]

	manager := NewManager(fix.log, newWaitDirector(), fix.vault)

	err := manager.Configure(Deps{
		Now: fix.clock.read,
		CommandRunner: func(_ context.Context, _, _ string, indices []int) (PatchSession, error) {
			fix.opened = append(fix.opened, slices.Clone(indices))

			return fix.sess, nil
		},
	})
	if err != nil {
		t.Fatalf("configuring the manager: %v", err)
	}

	t.Cleanup(manager.Stop)
	manager.Start()
	waitForLoad(t, manager)

	manager.mu.Lock()
	manager.tasks[fix.task.ID] = fix.task
	manager.mu.Unlock()

	fix.manager = manager
	fix.sessions = manager.newCommandSessions(runDeployment)

	return fix
}

// answer plays the VM for one call.
func (f *runFixture) answer(ctx context.Context, call runCall) (ssh.Result, error) {
	switch call.op {
	case opSudoCheck:
		return f.sudo, f.sudoErr
	case "prepare":
		if f.prepare.ExitCode != 0 || f.prepareErr != nil {
			return f.prepare, f.prepareErr
		}

		return ssh.Result{Stdout: []byte(f.stagedRecord + "\n")}, nil
	case "exec":
		if f.execHook != nil {
			f.execHook(ctx)
		}

		f.clock.advance(f.execTakes)

		return f.exec, f.execErr
	case "cleanup":
		if f.cleanupErr != nil {
			return ssh.Result{}, f.cleanupErr
		}

		return ssh.Result{Stdout: []byte(f.cleanup + "\n"), ExitCode: f.cleanupExit}, nil
	}

	f.t.Fatalf("unexpected op %s", call.op)

	return ssh.Result{}, nil
}

// run drives the node and returns whether the task stopped.
func (f *runFixture) run(ctx context.Context) bool {
	f.t.Helper()

	return f.manager.runCommandNode(ctx, f.sessions, f.task, f.inst, 0)
}

// node returns a copy of the node as the manager holds it.
func (f *runFixture) node() CommandVM {
	f.manager.mu.RLock()
	defer f.manager.mu.RUnlock()

	return f.inst.Command.clone().VMs[0]
}

func (f *runFixture) taskPaused() bool {
	f.manager.mu.RLock()
	defer f.manager.mu.RUnlock()

	return f.task.Paused && f.task.Status == TaskStatusPaused
}

// assertNoCommandText fails when the command or its digest reached a log
// line or a Vault record other than the task record. It prints where it
// looked and never the text.
func (f *runFixture) assertNoCommandText() {
	f.t.Helper()

	digest := commandDigest(runCommandText)
	logs := f.log.joined()

	if strings.Contains(logs, runCommandText) || strings.Contains(logs, digest) {
		f.t.Error("a log line holds the command text or its digest")
	}

	taskRecord := commandTasksVaultPath + "/" + runTaskID

	f.vault.waitVault.mu.Lock()
	defer f.vault.waitVault.mu.Unlock()

	for path, raw := range f.vault.saved {
		if path == taskRecord {
			continue
		}

		if bytes.Contains(raw, []byte(runCommandText)) || bytes.Contains(raw, []byte(digest)) {
			f.t.Errorf("the Vault record at %s holds the command text or its digest", path)
		}
	}
}

func withMatched(identity CommandVMIdentity) runOption {
	return func(_ *runFixture, node *CommandVM) { node.Matched = identity }
}

func wantOps(t *testing.T, sess *runSession, want ...string) {
	t.Helper()

	if got := sess.ops(); !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func intPtr(n int) *int { return &n }

func readAll(t *testing.T, reader io.Reader, err error) []byte {
	t.Helper()

	if err != nil {
		t.Fatalf("building the expected stdin: %v", err)
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading the expected stdin: %v", err)
	}

	return data
}

// A node that succeeds is sent the four operations in order with the
// arguments and stdin the wrapper expects, and its progress is saved
// before prepare and before exec.
func TestRunCommandNodeSendsTheOperationsInOrder(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)

	if stopped := fix.run(context.Background()); stopped {
		t.Fatal("the task stopped on a node that succeeded")
	}

	wantOps(t, fix.sess, opSudoCheck, "prepare", "exec", "cleanup")

	digest := commandDigest(runCommandText)

	prepare := fix.sess.call(t, "prepare")
	if !slices.Equal(prepare.args, []string{runTaskID, "vcap"}) {
		t.Errorf("prepare args = %v", prepare.args)
	}

	reader, err := scripts.RunInput([]byte(runCommandText))
	if want := readAll(t, reader, err); !bytes.Equal(prepare.stdin, want) {
		t.Errorf("prepare stdin differs from RunInput (lengths %d and %d)", len(prepare.stdin), len(want))
	}

	exec := fix.sess.call(t, "exec")
	if want := []string{runTaskID, "vcap", strconv.Itoa(runTimeoutSecs), digest}; !slices.Equal(exec.args, want) {
		t.Errorf("exec args differ from the task id, user, seconds, and digest: %d args", len(exec.args))
	}

	script, err := scripts.RunScript()
	if err != nil {
		t.Fatalf("RunScript: %v", err)
	}

	if !bytes.Equal(exec.stdin, script) {
		t.Errorf("exec stdin differs from RunScript (lengths %d and %d)", len(exec.stdin), len(script))
	}

	cleanup := fix.sess.call(t, "cleanup")
	if !slices.Equal(cleanup.args, []string{runTaskID}) {
		t.Errorf("cleanup args = %v", cleanup.args)
	}

	if len(cleanup.stdin) != len(script) {
		t.Errorf("cleanup stdin length = %d, want %d", len(cleanup.stdin), len(script))
	}

	if fix.opened[0][0] != 0 || len(fix.opened) != 1 {
		t.Errorf("the group session was opened with indices %v", fix.opened)
	}

	fix.assertNoCommandText()
}

// The staged record is saved and confirmed before prepare is sent, and the
// sent record before exec, which the shared sequence numbers prove.
func TestRunCommandNodeSavesBeforeEachSend(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	fix.run(context.Background())

	staged := fix.vault.writeSeq(t, CommandPhaseStaged)
	sent := fix.vault.writeSeq(t, CommandPhaseSent)
	settled := fix.vault.writeSeq(t, CommandPhaseSettled)

	prepare := fix.sess.call(t, "prepare").seq
	exec := fix.sess.call(t, "exec").seq
	cleanup := fix.sess.call(t, "cleanup").seq

	if !(staged < prepare && prepare < sent && sent < exec && exec < cleanup && cleanup < settled) {
		t.Fatalf("order staged=%d prepare=%d sent=%d exec=%d cleanup=%d settled=%d is not increasing",
			staged, prepare, sent, exec, cleanup, settled)
	}
}

// A node that succeeds is settled with its exit code, counts, flags,
// times, and the path of its stored output.
func TestRunCommandNodeSettlesTheNode(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	fix.execTakes = 4 * time.Second
	fix.run(context.Background())

	for name, node := range map[string]CommandVM{"memory": fix.node(), "vault": fix.vault.stored(t)} {
		if node.Phase != CommandPhaseSettled || node.Status != CommandStatusSucceeded {
			t.Errorf("%s: phase %s status %s", name, node.Phase, node.Status)
		}

		if node.ExitCode == nil || *node.ExitCode != 0 {
			t.Errorf("%s: exit code %v", name, node.ExitCode)
		}

		if node.StdoutKept != 3 || node.StdoutReceived != 3 || node.StderrKept != 2 || node.StderrReceived != 500 {
			t.Errorf("%s: counts %d/%d and %d/%d", name, node.StdoutKept, node.StdoutReceived, node.StderrKept, node.StderrReceived)
		}

		if node.StdoutTruncated || !node.StderrTruncated {
			t.Errorf("%s: truncated flags %v and %v", name, node.StdoutTruncated, node.StderrTruncated)
		}

		if node.StartedAt == nil || node.EndedAt == nil || node.EndedAt.Sub(*node.StartedAt) != 4*time.Second {
			t.Errorf("%s: the times do not span the exec", name)
		}

		if want := "command-output/" + runTaskID + "/" + runInstanceID + "/redis-0"; node.OutputPath != want {
			t.Errorf("%s: output path %q, want %q", name, node.OutputPath, want)
		}

		if node.Killed || node.Error != "" {
			t.Errorf("%s: killed %v error %q", name, node.Killed, node.Error)
		}
	}

	out, err := fix.manager.ReadCommandOutput(context.Background(), runTaskID, "", "redis-0")
	if err != nil || string(out.Stdout) != "out" || string(out.Stderr) != "er" {
		t.Fatalf("the stored output did not come back: err=%v", err)
	}

	fix.assertNoCommandText()
}

// Output is stored under the identity the output route reads, which is the
// matched one when the node has it and the reviewed one otherwise.
func TestRunCommandNodeStoresOutputWhereTheRouteReads(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		options []runOption
		vm      string
	}{
		"reviewed only": {vm: "redis-0"},
		"matched":       {options: []runOption{withMatched(CommandVMIdentity{Group: "redis", Index: 1, ID: "vm-id-1", CID: "cid-1"})}, vm: "redis-1"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fix := newRunFixture(t, tc.options...)
			fix.run(context.Background())

			out, err := fix.manager.ReadCommandOutput(context.Background(), runTaskID, "", tc.vm)
			if err != nil || string(out.Stdout) != "out" {
				t.Fatalf("the route could not read the output of %s: %v", tc.vm, err)
			}
		})
	}
}

// How the exit codes map to an outcome, with the cleanup record that
// separates a wrapper refusal from the command's own exit.
func TestRunCommandNodeClassifiesTheExit(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		exit       int
		cleanup    string
		takes      time.Duration
		status     string
		wantExit   *int
		reasonHas  string
		wantKilled bool
	}{
		{name: "success", exit: 0, cleanup: runCleanupDone, status: CommandStatusSucceeded, wantExit: intPtr(0)},
		{name: "plain failure", exit: 3, cleanup: "cleanup started=yes exit=3 killed=no", status: CommandStatusFailed, wantExit: intPtr(3), reasonHas: "status 3"},
		{name: "timeout", exit: 124, cleanup: "cleanup started=yes exit=124 killed=no", status: CommandStatusTimedOut, wantExit: intPtr(124)},
		{name: "kill after the timeout", exit: 137, takes: runTimeoutSecs * time.Second, cleanup: "cleanup started=yes exit=137 killed=no", status: CommandStatusTimedOut, wantExit: intPtr(137)},
		{name: "kill before the timeout", exit: 137, takes: 5 * time.Second, cleanup: "cleanup started=yes exit=137 killed=no", status: CommandStatusFailed, wantExit: intPtr(137), reasonHas: "status 137"},
		{name: "wrapper failed after the marker", exit: 125, cleanup: "cleanup started=yes exit=- killed=no", status: CommandStatusFailed, reasonHas: "before the command started"},
		{name: "wrapper refusal", exit: 13, cleanup: "cleanup started=no exit=- killed=no", status: CommandStatusRefused, reasonHas: "refused"},
		{name: "the command's own 13", exit: 13, cleanup: "cleanup started=yes exit=13 killed=no", status: CommandStatusFailed, wantExit: intPtr(13), reasonHas: "status 13"},
		{name: "ended by cleanup", exit: 124, cleanup: "cleanup started=yes exit=- killed=yes", status: CommandStatusTimedOut, wantExit: intPtr(124), wantKilled: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fix := newRunFixture(t)
			fix.exec.ExitCode = tc.exit
			fix.execTakes = tc.takes
			fix.cleanup = tc.cleanup

			if stopped := fix.run(context.Background()); stopped {
				t.Fatal("the task stopped")
			}

			wantOps(t, fix.sess, opSudoCheck, "prepare", "exec", "cleanup")

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
		})
	}
}

// A confirmed save that fails stops the task before the send. For the
// staged save nothing at all is sent after the sudo check, and for the
// sent save only prepare was.
func TestRunCommandNodePausesWhenASaveIsNotConfirmed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		phase CommandPhase
		ops   []string
		last  CommandPhase
	}{
		{name: "staged", phase: CommandPhaseStaged, ops: []string{opSudoCheck}},
		{name: "sent", phase: CommandPhaseSent, ops: []string{opSudoCheck, "prepare"}, last: CommandPhaseStaged},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fix := newRunFixture(t)
			fix.vault.failPhase = tc.phase

			if stopped := fix.run(context.Background()); !stopped {
				t.Fatal("the task did not stop")
			}

			wantOps(t, fix.sess, tc.ops...)

			if !fix.taskPaused() {
				t.Error("the task is not paused")
			}

			if fix.inst.Error == "" {
				t.Error("the instance records no reason")
			}

			if tc.last == "" {
				var held UpgradeTask

				found, _ := fix.vault.Get(context.Background(), commandTasksVaultPath+"/"+runTaskID, &held)
				if found {
					t.Error("Vault holds a record of a task whose first save was not confirmed")
				}

				return
			}

			if got := fix.vault.stored(t).Phase; got != tc.last {
				t.Errorf("Vault holds phase %s, want %s", got, tc.last)
			}
		})
	}
}

// When the save after exec fails, Vault still says sent, the task pauses,
// and nothing is sent again.
func TestRunCommandNodeKeepsSentWhenTheFinalSaveFails(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	fix.vault.failPhase = CommandPhaseSettled

	if stopped := fix.run(context.Background()); !stopped {
		t.Fatal("the task did not stop")
	}

	wantOps(t, fix.sess, opSudoCheck, "prepare", "exec", "cleanup")

	if got := fix.vault.stored(t).Phase; got != CommandPhaseSent {
		t.Errorf("Vault holds phase %s, want sent", got)
	}

	if got := fix.node().Phase; got != CommandPhaseSent {
		t.Errorf("the manager holds phase %s, want sent", got)
	}

	if !fix.taskPaused() || fix.inst.Error == "" {
		t.Error("the task is not paused with a reason on the instance")
	}
}

// A staged record that does not match what the task sent fails the node
// before exec, and cleanup removes what was staged.
func TestRunCommandNodeRefusesAStagedRecordThatDiffers(t *testing.T) {
	t.Parallel()

	good := commandDigest(runCommandText)
	count := strconv.Itoa(len(runCommandText))

	cases := map[string]string{
		"other digest":    "staged bytes=" + count + " sha256=" + strings.Repeat("a", 64),
		"other length":    "staged bytes=" + strconv.Itoa(len(runCommandText)-1) + " sha256=" + good,
		"malformed":       "staged bytes=" + count,
		"two records":     "staged bytes=" + count + " sha256=" + good + "\nstaged bytes=" + count + " sha256=" + good,
		"another kind":    "cleanup started=yes exit=0 killed=no",
		"not a record":    "something else entirely",
		"unknown field":   "staged bytes=" + count + " sha256=" + good + " extra=1",
		"uppercase value": "staged bytes=" + count + " sha256=" + strings.ToUpper(good),
	}

	for name, record := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fix := newRunFixture(t)
			fix.stagedRecord = record

			if stopped := fix.run(context.Background()); stopped {
				t.Fatal("the task stopped")
			}

			wantOps(t, fix.sess, opSudoCheck, "prepare", "cleanup")

			node := fix.vault.stored(t)
			if node.Phase != CommandPhaseSettled || node.Status != CommandStatusFailed {
				t.Errorf("phase %s status %s", node.Phase, node.Status)
			}

			fix.assertNoCommandText()
		})
	}
}

// A prepare that exits with a failure stops the node with nothing run.
func TestRunCommandNodeFailsWhenPrepareFails(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	fix.prepare = ssh.Result{ExitCode: scripts.ExitRefused}

	fix.run(context.Background())

	wantOps(t, fix.sess, opSudoCheck, "prepare")

	node := fix.vault.stored(t)
	if node.Phase != CommandPhaseSettled || node.Status != CommandStatusFailed || !strings.Contains(node.Reason, "prepare") {
		t.Errorf("phase %s status %s reason %q", node.Phase, node.Status, node.Reason)
	}
}

// A failed sudo check records the node failed with the sanitised error and
// sends nothing else.
func TestRunCommandNodeFailsOnTheSudoCheck(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 2000)

	cases := map[string]struct {
		result ssh.Result
		err    error
	}{
		"exit status": {result: ssh.Result{ExitCode: 1}},
		"error":       {err: errors.New("dial tcp 10.0.0.10:22:\n\x1b[31m refused\x00 " + long)},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fix := newRunFixture(t)
			fix.sudo, fix.sudoErr = tc.result, tc.err

			if stopped := fix.run(context.Background()); stopped {
				t.Fatal("the task stopped")
			}

			wantOps(t, fix.sess, opSudoCheck)

			node := fix.vault.stored(t)
			if node.Phase != CommandPhaseSettled || node.Status != CommandStatusFailed {
				t.Fatalf("phase %s status %s", node.Phase, node.Status)
			}

			if !strings.Contains(node.Reason, "sudo") {
				t.Errorf("the reason does not name the sudo check: %q", node.Reason)
			}

			if len(node.Error) > 300 || strings.ContainsAny(node.Error, "\n\x00\x1b") {
				t.Errorf("the error is not sanitised (length %d)", len(node.Error))
			}
		})
	}
}

// Processes that outlive KILL fail the node and pause the task, and
// nothing is sent again.
func TestRunCommandNodePausesWhenProcessesSurvive(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	fix.cleanup = "cleanup started=yes exit=- killed=survived"

	if stopped := fix.run(context.Background()); !stopped {
		t.Fatal("the task did not stop")
	}

	wantOps(t, fix.sess, opSudoCheck, "prepare", "exec", "cleanup")

	node := fix.vault.stored(t)
	if node.Phase != CommandPhaseSettled || node.Status != CommandStatusFailed || !strings.Contains(node.Reason, "survived") {
		t.Errorf("phase %s status %s reason %q", node.Phase, node.Status, node.Reason)
	}

	if !fix.taskPaused() {
		t.Error("the task is not paused")
	}
}

// A cleanup record the parser refuses, or a cleanup that fails, fails the
// node.
func TestRunCommandNodeFailsOnABadCleanup(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*runFixture){
		"not a record":  func(f *runFixture) { f.cleanup = "all done" },
		"bad value":     func(f *runFixture) { f.cleanup = "cleanup started=maybe exit=0 killed=no" },
		"exit status":   func(f *runFixture) { f.cleanupExit = 1 },
		"transport":     func(f *runFixture) { f.cleanupErr = errors.New("connection reset") },
		"extra records": func(f *runFixture) { f.cleanup = runCleanupDone + "\n" + runCleanupDone },
	}

	for name, script := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fix := newRunFixture(t)
			script(fix)

			fix.run(context.Background())

			node := fix.vault.stored(t)
			if node.Phase != CommandPhaseSettled || node.Status != CommandStatusFailed || !strings.Contains(node.Reason, "cleanup") {
				t.Errorf("phase %s status %s reason %q", node.Phase, node.Status, node.Reason)
			}
		})
	}
}

// Cleanup runs on a fresh context even when the node's context ended, and
// each operation carries its own deadline.
func TestRunCommandNodeBoundsEachCallAndCleansUpAfterCancel(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	ctx, cancel := context.WithCancel(context.Background())

	t.Cleanup(cancel)

	fix.execHook = func(execCtx context.Context) {
		cancel()
		<-execCtx.Done()
	}
	fix.execErr = fmt.Errorf("%w: context canceled", ssh.ErrRunnerClosed)

	fix.run(ctx)

	wantOps(t, fix.sess, opSudoCheck, "prepare", "exec", "cleanup")

	within := func(call runCall, want time.Duration) {
		t.Helper()

		if !call.hasDeadline {
			t.Errorf("%s has no deadline", call.op)

			return
		}

		if got := call.deadline.Sub(call.at); got > want || got < want-10*time.Second {
			t.Errorf("%s deadline is %v out, want about %v", call.op, got, want)
		}
	}

	within(fix.sess.call(t, "prepare"), patchCommandTimeout)
	within(fix.sess.call(t, "exec"), runTimeoutSecs*time.Second+patchCleanupTimeout)
	within(fix.sess.call(t, "cleanup"), patchCommandTimeout)

	if err := fix.sess.call(t, "cleanup").ctxErr; err != nil {
		t.Errorf("cleanup ran on a context that had ended: %v", err)
	}

	node := fix.vault.stored(t)
	if node.Phase != CommandPhaseSettled || node.Status != CommandStatusFailed {
		t.Errorf("phase %s status %s", node.Phase, node.Status)
	}
}

// An exec that never answers within its deadline is a timeout.
func TestRunCommandNodeTimesOutWhenExecOutlastsItsDeadline(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	fix.execErr = fmt.Errorf("%w: %w", ssh.ErrRunnerClosed, context.DeadlineExceeded)
	fix.execTakes = runTimeoutSecs*time.Second + patchCleanupTimeout

	fix.run(context.Background())

	node := fix.vault.stored(t)
	if node.Phase != CommandPhaseSettled || node.Status != CommandStatusTimedOut {
		t.Errorf("phase %s status %s", node.Phase, node.Status)
	}
}

// A transport error before the exec result is a failure that keeps what
// the node printed.
func TestRunCommandNodeKeepsPartialOutputOnATransportError(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	fix.execErr = errors.New("connection lost")

	fix.run(context.Background())

	node := fix.vault.stored(t)
	if node.Status != CommandStatusFailed || node.ExitCode != nil || node.OutputPath == "" {
		t.Errorf("status %s exit %v output %q", node.Status, node.ExitCode, node.OutputPath)
	}
}

// A failed output save fails the node and the instance keeps its record.
func TestRunCommandNodeFailsWhenTheOutputCannotBeStored(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	outputPath := "command-output/" + runTaskID + "/" + runInstanceID + "/redis-0"
	fix.vault.scriptPut(outputPath, 100, 0)

	fix.run(context.Background())

	node := fix.vault.stored(t)
	if node.Phase != CommandPhaseSettled || node.Status != CommandStatusFailed || node.OutputPath != "" {
		t.Errorf("phase %s status %s output %q", node.Phase, node.Status, node.OutputPath)
	}

	if !strings.Contains(node.Reason, "output") {
		t.Errorf("the reason does not mention the output: %q", node.Reason)
	}
}
