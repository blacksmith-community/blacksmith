package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/bosh/sshconn"
	"blacksmith/internal/config"
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
	seq     int64
	phase   CommandPhase
	matched CommandVMIdentity
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
	saves     []runSave
	failPhase CommandPhase
	// failWhen refuses each save of the task whose record it accepts.
	failWhen func(UpgradeTask) bool
}

// refuseWhen makes the vault refuse each save of the task whose record
// accept accepts, or no save when accept is nil.
func (v *runVault) refuseWhen(accept func(UpgradeTask) bool) {
	v.mu.Lock()
	v.failWhen = accept
	v.mu.Unlock()
}

// runSave is one write of the whole command task's record, numbered from
// the session's sequence.
type runSave struct {
	seq  int64
	task UpgradeTask
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

		node := task.Instances[0].Command.VMs[0]
		phase := node.Phase

		v.mu.Lock()
		v.saves = append(v.saves, runSave{seq: v.seq.Add(1), task: task})
		fail := v.failPhase != "" && v.failPhase == phase || v.failWhen != nil && v.failWhen(task)

		if !fail {
			v.writes = append(v.writes, runWrite{seq: v.seq.Add(1), phase: phase, matched: node.Matched})
		}
		v.mu.Unlock()

		if fail {
			return errPutRefused
		}
	}

	return v.confirmVault.Put(ctx, path, data)
}

func (v *runVault) savedTasks() []runSave {
	v.mu.Lock()
	defer v.mu.Unlock()

	return slices.Clone(v.saves)
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
	// hook sees each line after it is kept, outside the lock.
	hook func(line string)
}

func (l *captureLog) add(format string, args ...interface{}) {
	line := fmt.Sprintf(format, args...)

	l.mu.Lock()
	l.lines = append(l.lines, line)
	hook := l.hook
	l.mu.Unlock()

	if hook != nil {
		hook(line)
	}
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

// runDirector lists the VMs of the fixture's deployment and counts the
// listings. Its list function answers each call, and the default answers
// with the VM of every node as it was reviewed.
type runDirector struct {
	*waitDirector

	list func(call int) ([]bosh.VM, error)

	mu    sync.Mutex
	calls int
}

func (d *runDirector) GetDeploymentVMs(context.Context, string) ([]bosh.VM, error) {
	d.mu.Lock()
	d.calls++
	call := d.calls
	d.mu.Unlock()

	return d.list(call)
}

func (d *runDirector) listings() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.calls
}

// vmOf is the VM the director lists for an identity.
func vmOf(identity CommandVMIdentity) bosh.VM {
	return bosh.VM{ID: identity.ID, CID: identity.CID, Job: identity.Group, Index: identity.Index, AZ: identity.AZ, IPs: slices.Clone(identity.IPs), State: identity.State, JobState: identity.JobState}
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
	director *runDirector

	opened [][]int

	// noRunner leaves Deps.CommandRunner nil.
	noRunner bool

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

	fix.sess = &runSession{
		seq:    &seq,
		hosts:  []sshconn.Host{{Job: node.Reviewed.Group, IndexOrID: node.Reviewed.ID, Address: "10.0.0.10"}},
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

	fix.director = &runDirector{waitDirector: newWaitDirector()}
	fix.director.list = func(int) ([]bosh.VM, error) {
		fix.manager.mu.RLock()
		defer fix.manager.mu.RUnlock()

		var vms []bosh.VM

		for i := range fix.inst.Command.VMs {
			vms = append(vms, vmOf(fix.inst.Command.VMs[i].Reviewed))
		}

		return vms, nil
	}

	manager := NewManager(fix.log, fix.director, fix.vault)

	deps := Deps{Now: fix.clock.read, Commands: func() config.CommandsConfig { return config.CommandsConfig{} }}

	if !fix.noRunner {
		deps.CommandRunner = func(_ context.Context, _, _ string, indices []int) (PatchSession, error) {
			fix.opened = append(fix.opened, slices.Clone(indices))

			return fix.sess, nil
		}
	}

	err := manager.Configure(deps)
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

	stopped, err := f.manager.runCommandNode(ctx, f.sessions, f.task, f.inst, 0)
	if err != nil {
		f.t.Fatalf("runCommandNode refused the node: %v", err)
	}

	return stopped
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

// assertNoCommandText fails when the command reached a log line, or its
// digest reached any line but a command audit line, or the command or its
// digest reached a Vault record other than the task record and the command
// audit records, which hold them on purpose. It prints where it looked and
// never the text.
func (f *runFixture) assertNoCommandText() {
	f.t.Helper()

	digest := commandDigest(runCommandText)

	f.log.mu.Lock()
	lines := slices.Clone(f.log.lines)
	f.log.mu.Unlock()

	for _, line := range lines {
		if strings.Contains(line, runCommandText) {
			f.t.Error("a log line holds the command text")
		}

		if strings.Contains(line, digest) && !strings.HasPrefix(line, "audit=command-run ") {
			f.t.Error("a log line other than a command audit line holds the command digest")
		}
	}

	taskRecord := commandTasksVaultPath + "/" + runTaskID

	f.vault.waitVault.mu.Lock()
	defer f.vault.waitVault.mu.Unlock()

	for path, raw := range f.vault.saved {
		if path == taskRecord || strings.HasPrefix(path, commandAuditVaultPath+"/") {
			continue
		}

		if bytes.Contains(raw, []byte(runCommandText)) || bytes.Contains(raw, []byte(digest)) {
			f.t.Errorf("the Vault record at %s holds the command text or its digest", path)
		}
	}
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
		{name: "wrapper launch failure", exit: 125, cleanup: "cleanup started=no exit=125 killed=no", status: CommandStatusFailed, reasonHas: "could not launch"},
		{name: "the command's own 125", exit: 125, cleanup: "cleanup started=yes exit=125 killed=no", status: CommandStatusFailed, wantExit: intPtr(125), reasonHas: "status 125"},
		{name: "wrapper refusal", exit: 13, cleanup: "cleanup started=no exit=- killed=no", status: CommandStatusRefused, reasonHas: "refused"},
		{name: "usage error before the marker", exit: 2, cleanup: "cleanup started=no exit=- killed=no", status: CommandStatusFailed, reasonHas: "did not start"},
		{name: "trap exit before the marker", exit: 130, cleanup: "cleanup started=no exit=- killed=no", status: CommandStatusFailed, reasonHas: "did not start"},
		{name: "exit with the directory gone", exit: 143, cleanup: "cleanup started=- exit=- killed=no", status: CommandStatusFailed, reasonHas: "did not start"},
		{name: "the command's own 13", exit: 13, cleanup: "cleanup started=yes exit=13 killed=no", status: CommandStatusFailed, wantExit: intPtr(13), reasonHas: "status 13"},
		{name: "ended by cleanup", exit: 124, cleanup: "cleanup started=yes exit=- killed=yes", status: CommandStatusTimedOut, wantExit: intPtr(124), wantKilled: true},
		{name: "success with children cleanup ended", exit: 0, cleanup: "cleanup started=yes exit=0 killed=yes", status: CommandStatusSucceeded, wantExit: intPtr(0), wantKilled: true},
		{name: "failure with children cleanup ended", exit: 3, cleanup: "cleanup started=yes exit=3 killed=yes", status: CommandStatusFailed, wantExit: intPtr(3), reasonHas: "status 3", wantKilled: true},
		{name: "success with the session unchecked", exit: 0, cleanup: "cleanup started=yes exit=0 killed=unknown", status: CommandStatusSucceeded, wantExit: intPtr(0)},
		{name: "failure with the session unchecked", exit: 3, cleanup: "cleanup started=yes exit=3 killed=unknown", status: CommandStatusFailed, wantExit: intPtr(3), reasonHas: "status 3"},
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
		held  CommandPhase
	}{
		{name: "staged", phase: CommandPhaseStaged, ops: []string{opSudoCheck}, held: CommandPhasePending},
		{name: "sent", phase: CommandPhaseSent, ops: []string{opSudoCheck, "prepare"}, last: CommandPhaseStaged, held: CommandPhaseStaged},
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

			if got := fix.node().Phase; got != tc.held {
				t.Errorf("the manager holds phase %s, want the confirmed %s", got, tc.held)
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

			if tc.err != nil && node.Error == "" {
				t.Error("the error case recorded no error text")
			}

			if len(node.Error) > commandErrorMax || strings.ContainsAny(node.Error, "\n\x00\x1b") {
				t.Errorf("the error is not sanitised (length %d)", len(node.Error))
			}
		})
	}
}

// Processes that outlive KILL fail the node, hold it as a survivor, and
// pause the task, and nothing is sent again. That holds even when the
// command exited, because cleanup checks its session whatever the exit.
func TestRunCommandNodePausesWhenProcessesSurvive(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		exit    int
		cleanup string
	}{
		{exit: 124, cleanup: "cleanup started=yes exit=- killed=survived"},
		{exit: 0, cleanup: "cleanup started=yes exit=0 killed=survived"},
		{exit: 3, cleanup: "cleanup started=yes exit=3 killed=survived"},
	} {
		t.Run(tc.cleanup, func(t *testing.T) {
			t.Parallel()

			fix := newRunFixture(t)
			fix.exec.ExitCode = tc.exit
			fix.cleanup = tc.cleanup

			if stopped := fix.run(context.Background()); !stopped {
				t.Fatal("the task did not stop")
			}

			wantOps(t, fix.sess, opSudoCheck, "prepare", "exec", "cleanup")

			node := fix.vault.stored(t)
			if node.Phase != CommandPhaseSettled || node.Status != CommandStatusFailed || !strings.Contains(node.Reason, "survived") {
				t.Errorf("phase %s status %s reason %q", node.Phase, node.Status, node.Reason)
			}

			if !node.Survived {
				t.Error("the node is not held as a survivor")
			}

			if !fix.taskPaused() {
				t.Error("the task is not paused")
			}
		})
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
	fix.cleanup = "cleanup started=yes exit=- killed=yes"

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

// An exec that never answers within its deadline, and whose group cleanup
// had to end, is a timeout.
func TestRunCommandNodeTimesOutWhenExecOutlastsItsDeadline(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	fix.execErr = fmt.Errorf("%w: %w", ssh.ErrRunnerClosed, context.DeadlineExceeded)
	fix.cleanup = "cleanup started=yes exit=- killed=yes"
	fix.execTakes = runTimeoutSecs*time.Second + patchCleanupTimeout

	fix.run(context.Background())

	node := fix.vault.stored(t)
	if node.Phase != CommandPhaseSettled || node.Status != CommandStatusTimedOut {
		t.Errorf("phase %s status %s", node.Phase, node.Status)
	}
}

// A transport error before the exec result, on a node whose group cleanup
// had to end, is a failure that keeps what the node printed.
func TestRunCommandNodeKeepsPartialOutputOnATransportError(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	fix.execErr = errors.New("connection lost")
	fix.cleanup = "cleanup started=yes exit=- killed=yes"

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

// A node that is not pending is refused before anything is sent, not even
// the sudo check, and the error names its phase.
func TestRunCommandNodeRefusesANodeThatIsNotPending(t *testing.T) {
	t.Parallel()

	for _, phase := range []CommandPhase{CommandPhaseStaged, CommandPhaseSent, CommandPhaseSettled, CommandPhaseSkipped} {
		t.Run(string(phase), func(t *testing.T) {
			t.Parallel()

			fix := newRunFixture(t)
			fix.inst.Command.VMs[0].Phase = phase

			stopped, err := fix.manager.runCommandNode(context.Background(), fix.sessions, fix.task, fix.inst, 0)
			if err == nil || !strings.Contains(err.Error(), string(phase)) || !errors.Is(err, errCommandNodeNotPending) {
				t.Fatalf("err = %v, want a refusal that names %s", err, phase)
			}

			if stopped || len(fix.sess.recorded()) != 0 || len(fix.opened) != 0 {
				t.Fatalf("stopped=%v with %d calls and %d sessions opened", stopped, len(fix.sess.recorded()), len(fix.opened))
			}

			if len(fix.vault.recorded()) != 0 {
				t.Fatal("a refused node was saved")
			}
		})
	}
}

// A node left in sent by an unconfirmed final save is not driven to a
// second exec when it is run again.
func TestRunCommandNodeNeverExecsTwice(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	fix.vault.failPhase = CommandPhaseSettled

	if stopped := fix.run(context.Background()); !stopped {
		t.Fatal("the task did not stop")
	}

	fix.vault.mu.Lock()
	fix.vault.failPhase = ""
	fix.vault.mu.Unlock()

	_, err := fix.manager.runCommandNode(context.Background(), fix.sessions, fix.task, fix.inst, 0)
	if err == nil {
		t.Fatal("a node left in sent was run again")
	}

	execs := 0

	for _, op := range fix.sess.ops() {
		if op == "exec" {
			execs++
		}
	}

	if execs != 1 {
		t.Fatalf("%d execs were sent, want 1", execs)
	}
}

// A script that can't be built fails the node before the first save, so
// nothing is staged, sent, or saved as staged or sent.
func TestRunCommandNodeBuildsItsInputsBeforeTheFirstSave(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	fix.manager.commandScript = func() ([]byte, error) { return nil, errors.New("no script") }

	if stopped := fix.run(context.Background()); stopped {
		t.Fatal("the task stopped")
	}

	wantOps(t, fix.sess)

	for _, write := range fix.vault.recorded() {
		if write.phase == CommandPhaseStaged || write.phase == CommandPhaseSent {
			t.Fatalf("a write saved the node in phase %s", write.phase)
		}
	}

	node := fix.vault.stored(t)
	if node.Phase != CommandPhaseSettled || node.Status != CommandStatusFailed || node.Error == "" {
		t.Errorf("phase %s status %s error %q", node.Phase, node.Status, node.Error)
	}
}

// A survived record still fails the node and pauses the task when cleanup
// then exits non-zero.
func TestRunCommandNodePausesWhenSurvivedCleanupExitsNonZero(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	fix.cleanup = "cleanup started=yes exit=- killed=survived"
	fix.cleanupExit = 1

	if stopped := fix.run(context.Background()); !stopped {
		t.Fatal("the task did not stop")
	}

	node := fix.vault.stored(t)
	if node.Status != CommandStatusFailed || !strings.Contains(node.Reason, "survived") {
		t.Errorf("status %s reason %q", node.Status, node.Reason)
	}

	if !fix.taskPaused() {
		t.Error("the task is not paused")
	}
}

// Processes that survive KILL pause a task that is still running. A task
// that has already ended keeps its status, and one whose node ran waits
// for Resume cleanup with the instance held. A cancelled task never sends
// the command, so nothing survives there and it does not wait.
func TestRunCommandNodePausesOnlyAUnfinishedTask(t *testing.T) {
	t.Parallel()

	for status, waits := range map[TaskStatus]bool{TaskStatusCancelled: false, TaskStatusCompleted: true, TaskStatusFailed: true} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()

			fix := newRunFixture(t)
			fix.task.Status = status
			fix.cleanup = "cleanup started=yes exit=- killed=survived"

			fix.run(context.Background())

			fix.manager.mu.RLock()
			defer fix.manager.mu.RUnlock()

			if fix.task.Status != status || fix.task.Paused != waits || waitsCancelledLocked(fix.task) != waits {
				t.Errorf("status %s paused %v, want %s and paused %v", fix.task.Status, fix.task.Paused, status, waits)
			}
		})
	}
}

// A broker with no command runner fails the node with an error that says
// so for commands, not for patch mode.
func TestRunCommandNodeNamesAMissingCommandRunner(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t, func(f *runFixture, _ *CommandVM) { f.noRunner = true })

	fix.run(context.Background())

	wantOps(t, fix.sess)

	node := fix.vault.stored(t)
	if node.Status != CommandStatusFailed || !strings.Contains(node.Error, "for commands") || strings.Contains(node.Error, "patch") {
		t.Errorf("status %s error %q", node.Status, node.Error)
	}
}

// The deadline flag settles a timeout even when the transport error does
// not carry the context error, and without it the same error is a failure.
func TestCommandRunClassifiesAnExecThatHitItsDeadline(t *testing.T) {
	t.Parallel()

	run := &commandRun{spec: CommandTaskSpec{TimeoutSeconds: runTimeoutSecs}}
	closed := fmt.Errorf("%w: connection lost", ssh.ErrRunnerClosed)

	cases := []struct {
		name     string
		deadline bool
		want     string
	}{
		{name: "deadline hit", deadline: true, want: CommandStatusTimedOut},
		{name: "deadline not hit", want: CommandStatusFailed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := run.classify(ssh.Result{}, closed, tc.deadline, scripts.Cleanup{Started: "yes", Exit: "-", Killed: "yes"}, nil, false)
			if got.status != tc.want {
				t.Errorf("status %s, want %s", got.status, tc.want)
			}
		})
	}
}

// A fresh fixture's node has been reviewed as redis-0 with no Matched
// tuple. These helpers build the listings the binding tests change.
func reviewedListing(fix *runFixture) []bosh.VM {
	return []bosh.VM{vmOf(fix.node().Reviewed)}
}

func listsOnly(vms ...bosh.VM) func(int) ([]bosh.VM, error) {
	return func(int) ([]bosh.VM, error) { return slices.Clone(vms), nil }
}

// wantNothingSent fails unless the node failed with the changed message,
// nothing reached the VM or the SSH setup, and the node was never staged.
func wantNothingSent(t *testing.T, fix *runFixture, stopped bool, err error) {
	t.Helper()

	if !errors.Is(err, errCommandVMChanged) {
		t.Fatalf("err = %v, want errCommandVMChanged", err)
	}

	if stopped {
		t.Error("the task stopped, but only the deployment should")
	}

	wantOps(t, fix.sess)

	if len(fix.opened) != 0 {
		t.Errorf("%d SSH sessions were opened for a node that changed", len(fix.opened))
	}

	node := fix.vault.stored(t)
	if node.Status != CommandStatusFailed || node.Phase != CommandPhaseSettled {
		t.Errorf("phase %s status %s", node.Phase, node.Status)
	}

	if want := "VM redis/0 changed since the review, so nothing ran on it"; !strings.Contains(node.Reason, want) {
		t.Errorf("reason %q lacks %q", node.Reason, want)
	}

	if node.Matched.Group != "" {
		t.Error("a node that changed has a matched tuple")
	}

	for _, write := range fix.vault.recorded() {
		if write.phase == CommandPhaseStaged || write.phase == CommandPhaseSent {
			t.Errorf("the node was saved in phase %s", write.phase)
		}
	}

	fix.assertNoCommandText()
}

// The node is bound to the VM the review showed. A listing with a new
// CID, a new ID at the same index, or no row for the node fails the node,
// sends nothing, and tells the caller to stop the deployment.
func TestRunCommandNodeFailsWhenTheVMChanged(t *testing.T) {
	t.Parallel()

	cases := map[string]func(reviewed bosh.VM) []bosh.VM{
		"new CID":     func(vm bosh.VM) []bosh.VM { vm.CID = "cid-recreated"; return []bosh.VM{vm} },
		"new ID":      func(vm bosh.VM) []bosh.VM { vm.ID = "vm-id-other"; return []bosh.VM{vm} },
		"no row":      func(bosh.VM) []bosh.VM { return nil },
		"other group": func(vm bosh.VM) []bosh.VM { vm.Job = "rabbit"; return []bosh.VM{vm} },
		"other index": func(vm bosh.VM) []bosh.VM { vm.Index = 1; return []bosh.VM{vm} },
		"CID gone":    func(vm bosh.VM) []bosh.VM { vm.CID = ""; return []bosh.VM{vm} },
	}

	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fix := newRunFixture(t)
			fix.director.list = listsOnly(change(reviewedListing(fix)[0])...)

			stopped, err := fix.manager.runCommandNode(context.Background(), fix.sessions, fix.task, fix.inst, 0)

			wantNothingSent(t, fix, stopped, err)
		})
	}
}

// A listing that fails fails the node with the director's error cut down
// to one line, sends nothing, and never falls back to the reviewed tuple.
func TestRunCommandNodeFailsWhenTheListingFails(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	fix.director.list = func(int) ([]bosh.VM, error) {
		return nil, errors.New("director unavailable\nsecond line") //nolint:err113 // test fake
	}

	stopped, err := fix.manager.runCommandNode(context.Background(), fix.sessions, fix.task, fix.inst, 0)
	if err != nil || stopped {
		t.Fatalf("stopped=%v err=%v, want the node failed and nothing more", stopped, err)
	}

	wantOps(t, fix.sess)

	if len(fix.opened) != 0 {
		t.Errorf("%d SSH sessions were opened without a listing", len(fix.opened))
	}

	node := fix.vault.stored(t)
	if node.Status != CommandStatusFailed || node.Matched.Group != "" {
		t.Errorf("status %s matched %q", node.Status, node.Matched.Group)
	}

	if !strings.Contains(node.Error, "director unavailable") || strings.ContainsAny(node.Error, "\n\r") {
		t.Errorf("error %q is not the sanitised director error", node.Error)
	}

	if strings.Contains(node.Reason, "changed since the review") {
		t.Errorf("reason %q blames a change the listing never showed", node.Reason)
	}

	fix.assertNoCommandText()
}

// A listing that differs only in fields the tuple does not hold, such as
// the IPs and the job state, still binds the node, and the matched tuple
// records what the director listed this time.
func TestRunCommandNodeRecordsTheMatchedTuple(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)

	listed := reviewedListing(fix)[0]
	listed.AZ, listed.IPs, listed.State, listed.JobState = "z2", []string{"10.0.0.99"}, "running", "failing"
	fix.director.list = listsOnly(listed)

	if fix.run(context.Background()) {
		t.Fatal("the task stopped")
	}

	wantOps(t, fix.sess, opSudoCheck, "prepare", "exec", "cleanup")

	want := CommandVMIdentity{Group: "redis", Index: 0, ID: runVMID, CID: "cid-0", AZ: "z2", IPs: []string{"10.0.0.99"}, State: "running", JobState: "failing"}

	for name, node := range map[string]CommandVM{"memory": fix.node(), "vault": fix.vault.stored(t)} {
		if !reflect.DeepEqual(node.Matched, want) {
			t.Errorf("%s: matched = %+v, want %+v", name, node.Matched, want)
		}

		if node.Reviewed.AZ != "" || node.Reviewed.CID != "cid-0" || len(node.Reviewed.IPs) != 0 {
			t.Errorf("%s: the reviewed tuple was changed: %+v", name, node.Reviewed)
		}
	}

	// The matched tuple is already in the record saved at the staged
	// phase, which is before prepare is sent.
	staged := false

	for _, write := range fix.vault.recorded() {
		if write.phase == CommandPhaseStaged {
			staged = true

			if !reflect.DeepEqual(write.matched, want) {
				t.Errorf("the staged save holds matched %+v, want %+v", write.matched, want)
			}
		}
	}

	if !staged {
		t.Fatal("no staged save")
	}

	// The output is stored where the route that reads it looks, and the
	// route finds it. The path keys only on the group and the index, which
	// the matched and reviewed tuples share, so this does not tell the
	// matched identity from the reviewed one.
	path, err := commandOutputPath(runTaskID, runInstanceID, identityOf(fix.node()))
	if err != nil || fix.node().OutputPath != path {
		t.Fatalf("output path %q, want %q (%v)", fix.node().OutputPath, path, err)
	}

	out, err := fix.manager.ReadCommandOutput(context.Background(), runTaskID, "", "redis-0")
	if err != nil || string(out.Stdout) != "out" {
		t.Fatalf("the route could not read the output of the node: %v", err)
	}

	fix.assertNoCommandText()
}

// A node that fails the sudo check settles with the matched tuple already
// recorded, because the binding happens before anything is sent.
func TestRunCommandNodeKeepsTheMatchedTupleWhenSudoFails(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	fix.sudo = ssh.Result{ExitCode: 1}

	listed := reviewedListing(fix)[0]
	listed.AZ = "z2"
	fix.director.list = listsOnly(listed)

	fix.run(context.Background())

	wantOps(t, fix.sess, opSudoCheck)

	node := fix.vault.stored(t)
	if node.Status != CommandStatusFailed || node.Matched.Group != "redis" || node.Matched.AZ != "z2" {
		t.Errorf("status %s matched %+v", node.Status, node.Matched)
	}

	fix.assertNoCommandText()
}

// A session whose host has the wrong group, or an IndexOrID that is
// neither the node's index nor its ID, is refused before the sudo check,
// though the host count and uniqueness checkGroupHosts looks at are right.
func TestRunCommandNodeRefusesAHostThatIsNotTheNode(t *testing.T) {
	t.Parallel()

	cases := map[string]sshconn.Host{
		"other group": {Job: "rabbit", IndexOrID: runVMID, Address: "10.0.0.10"},
		"other ID":    {Job: "redis", IndexOrID: "vm-id-other", Address: "10.0.0.10"},
		"other index": {Job: "redis", IndexOrID: "3", Address: "10.0.0.10"},
	}

	for name, host := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fix := newRunFixture(t)
			fix.sess.hosts = []sshconn.Host{host}

			// TestCheckGroupHostsAcceptsHostsOfOtherNodes shows the setup's
			// own check passes a list like this one.
			if fix.run(context.Background()) {
				t.Fatal("the task stopped")
			}

			wantOps(t, fix.sess)

			node := fix.vault.stored(t)
			if node.Status != CommandStatusFailed || node.Phase != CommandPhaseSettled || node.Matched.Group != "" {
				t.Errorf("phase %s status %s matched %q", node.Phase, node.Status, node.Matched.Group)
			}

			for _, write := range fix.vault.recorded() {
				if write.phase == CommandPhaseStaged {
					t.Error("the node was staged on a host that is not it")
				}
			}

			fix.assertNoCommandText()
		})
	}
}

// A host list that holds two hosts for one node cannot be told apart, so
// the node is refused rather than given the first.
func TestRunCommandNodeRefusesAmbiguousHosts(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	fix.sess.hosts = []sshconn.Host{
		{Job: "redis", IndexOrID: "0", Address: "10.0.0.11"},
		{Job: "redis", IndexOrID: runVMID, Address: "10.0.0.10"},
	}

	fix.run(context.Background())

	wantOps(t, fix.sess)

	if node := fix.vault.stored(t); node.Status != CommandStatusFailed || node.Matched.Group != "" {
		t.Errorf("status %s matched %q", node.Status, node.Matched.Group)
	}

	fix.assertNoCommandText()
}

// Hosts come back in the director's order, which need not be the node
// order. Each node reaches the host that carries its own identity, and the
// session is asked for both indices.
func TestRunCommandNodeMatchesHostsByIdentityNotPosition(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)

	fix.inst.Command.VMs = append(fix.inst.Command.VMs, CommandVM{
		Reviewed: CommandVMIdentity{Group: "redis", Index: 1, ID: "vm-id-1", CID: "cid-1"},
		Phase:    CommandPhasePending,
	})

	fix.sess.hosts = []sshconn.Host{
		{Job: "redis", IndexOrID: "vm-id-1", Address: "10.0.0.11"},
		{Job: "redis", IndexOrID: runVMID, Address: "10.0.0.10"},
	}

	// Record which host each call went to.
	sess := &hostRecorder{runSession: fix.sess}
	fix.sessions = &patchSessions{open: func(context.Context, string, string, []int) (PatchSession, error) { return sess, nil }, deployment: runDeployment, byGroup: make(map[string]PatchSession)}

	for index := range 2 {
		stopped, err := fix.manager.runCommandNode(context.Background(), fix.sessions, fix.task, fix.inst, index)
		if err != nil || stopped {
			t.Fatalf("node %d: stopped=%v err=%v", index, stopped, err)
		}
	}

	if !slices.Equal(sess.used("prepare"), []int{1, 0}) {
		t.Errorf("prepare went to hosts %v, want [1 0] for nodes 0 and 1", sess.used("prepare"))
	}

	fix.assertNoCommandText()
}

// hostRecorder notes which host each call went to.
type hostRecorder struct {
	*runSession

	mu    sync.Mutex
	hosts map[string][]int
}

func (h *hostRecorder) Run(ctx context.Context, vm int, op string, args []string, stdin io.Reader) (ssh.Result, error) {
	h.mu.Lock()

	if h.hosts == nil {
		h.hosts = make(map[string][]int)
	}

	h.hosts[op] = append(h.hosts[op], vm)
	h.mu.Unlock()

	return h.runSession.Run(ctx, vm, op, args, stdin)
}

func (h *hostRecorder) used(op string) []int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return slices.Clone(h.hosts[op])
}

// The driver keeps no listing between calls. Two nodes of one deployment
// driven one after the other list the deployment twice, and a listing that
// changes between them fails only the second.
func TestRunCommandNodeListsAgainBeforeEachNode(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)

	fix.inst.Command.VMs = append(fix.inst.Command.VMs, CommandVM{
		Reviewed: CommandVMIdentity{Group: "redis", Index: 1, ID: "vm-id-1", CID: "cid-1"},
		Phase:    CommandPhasePending,
	})
	fix.sess.hosts = append(fix.sess.hosts, sshconn.Host{Job: "redis", IndexOrID: "vm-id-1", Address: "10.0.0.11"})

	first := vmOf(fix.inst.Command.VMs[0].Reviewed)
	second := vmOf(fix.inst.Command.VMs[1].Reviewed)

	// The second node is recreated while the first runs.
	fix.director.list = func(call int) ([]bosh.VM, error) {
		if call == 1 {
			return []bosh.VM{first, second}, nil
		}

		recreated := second
		recreated.CID = "cid-recreated"

		return []bosh.VM{first, recreated}, nil
	}

	stopped, err := fix.manager.runCommandNode(context.Background(), fix.sessions, fix.task, fix.inst, 0)
	if err != nil || stopped {
		t.Fatalf("first node: stopped=%v err=%v", stopped, err)
	}

	if got := fix.director.listings(); got != 1 {
		t.Fatalf("after one node the director was listed %d times, want 1", got)
	}

	opsAfterFirst := len(fix.sess.recorded())

	_, err = fix.manager.runCommandNode(context.Background(), fix.sessions, fix.task, fix.inst, 1)
	if !errors.Is(err, errCommandVMChanged) {
		t.Fatalf("second node: err = %v, want errCommandVMChanged", err)
	}

	if got := fix.director.listings(); got != 2 {
		t.Fatalf("after two nodes the director was listed %d times, want 2", got)
	}

	if len(fix.sess.recorded()) != opsAfterFirst {
		t.Error("something was sent to the node that changed")
	}

	fix.manager.mu.RLock()
	second2 := fix.inst.Command.VMs[1]
	fix.manager.mu.RUnlock()

	if second2.Status != CommandStatusFailed || second2.Phase != CommandPhaseSettled {
		t.Errorf("second node: phase %s status %s", second2.Phase, second2.Status)
	}

	fix.assertNoCommandText()
}

func identityOf(node CommandVM) CommandVMIdentity { return node.identity() }

// nodeKey names a node the way the fixture's maps do.
func nodeKey(deployment string, index int) string { return fmt.Sprintf("%s/%d", deployment, index) }

// nodeGate holds nodes of a task fixture at one operation until the test
// lets each go, and tells the test when a node arrives. A gate that names
// no node holds every node.
type nodeGate struct {
	t       *testing.T
	op      string
	held    map[string]bool
	arrived chan string

	mu      sync.Mutex
	open    bool
	release map[string]chan struct{}
}

// gate holds the fixture's nodes at op. It stays in front of any hook the
// fixture already has, and lets every node go when the test ends, so a
// failed test never strands a worker.
func (f *taskFixture) gate(op string, keys ...string) *nodeGate {
	g := &nodeGate{t: f.t, op: op, held: map[string]bool{}, arrived: make(chan string, 64), release: map[string]chan struct{}{}}

	for _, key := range keys {
		g.held[key] = true
	}

	previous := f.onOp

	f.onOp = func(deployment string, index int, called string) {
		if previous != nil {
			previous(deployment, index, called)
		}

		key := nodeKey(deployment, index)
		if called != g.op || (len(g.held) > 0 && !g.held[key]) {
			return
		}

		g.arrived <- key

		<-g.channel(key)
	}

	f.t.Cleanup(g.releaseAll)

	return g
}

// channel returns the channel the node waits on.
func (g *nodeGate) channel(key string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()

	ch, ok := g.release[key]
	if !ok {
		ch = make(chan struct{})
		if g.open {
			close(ch)
		}

		g.release[key] = ch
	}

	return ch
}

// let sends the node on its way.
func (g *nodeGate) let(key string) {
	ch := g.channel(key)

	g.mu.Lock()
	defer g.mu.Unlock()

	select {
	case <-ch:
	default:
		close(ch)
	}
}

func (g *nodeGate) releaseAll() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.open = true

	for _, ch := range g.release {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

// next waits for a node to arrive and returns its key.
func (g *nodeGate) next() string {
	g.t.Helper()

	select {
	case key := <-g.arrived:
		return key
	case <-time.After(waitTimeout):
		g.t.Fatalf("no node reached %s", g.op)

		return ""
	}
}

// wantNext requires the next node to arrive to be the named one.
func (g *nodeGate) wantNext(want string) {
	g.t.Helper()

	if got := g.next(); got != want {
		g.t.Fatalf("%s was reached by %s, want %s", g.op, got, want)
	}
}

// nodeWatch follows the fixture's nodes from their first operation to
// their cleanup, without holding any of them.
type nodeWatch struct {
	mu sync.Mutex
	// started is every node, in the order its first operation arrived.
	started []string
	// active counts the nodes of each deployment between their first
	// operation and their cleanup.
	active map[string]int
	// pastPrepare counts the nodes between prepare and cleanup.
	pastPrepare, peakPastPrepare int
	// overlaps lists each node that began while another deployment had a
	// node active.
	overlaps []string
}

// watch records every node of the fixture. It stays in front of any hook
// the fixture already has.
func (f *taskFixture) watch() *nodeWatch {
	w := &nodeWatch{active: map[string]int{}}
	previous := f.onOp

	f.onOp = func(deployment string, index int, op string) {
		w.see(deployment, index, op)

		if previous != nil {
			previous(deployment, index, op)
		}
	}

	return w
}

func (w *nodeWatch) see(deployment string, index int, op string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	switch op {
	case opSudoCheck:
		for other, count := range w.active {
			if other != deployment && count > 0 {
				w.overlaps = append(w.overlaps, nodeKey(deployment, index))
			}
		}

		w.started = append(w.started, nodeKey(deployment, index))
		w.active[deployment]++
	case opRunPrepare:
		w.pastPrepare++
		w.peakPastPrepare = max(w.peakPastPrepare, w.pastPrepare)
	case opRunCleanup:
		w.pastPrepare--
		w.active[deployment]--
	}
}

func (w *nodeWatch) peak() int {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.peakPastPrepare
}

func (w *nodeWatch) overlapping() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return slices.Clone(w.overlaps)
}

func (w *nodeWatch) order() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return slices.Clone(w.started)
}

// phaseRank orders the phases a node moves through. A skipped node never
// left pending, and ends where a settled one does.
func phaseRank(phase CommandPhase) int {
	switch phase {
	case CommandPhasePending:
		return 0
	case CommandPhaseStaged:
		return 1
	case CommandPhaseSent:
		return 2
	case CommandPhaseSettled, CommandPhaseSkipped:
		return 3
	}

	return -1
}

// wantSavesInOrder decodes every save of the task and requires each node's
// phase never to go backwards from one save to the next, the staged save of
// every node that was sent prepare to land before the prepare, and the sent
// save of every node that was sent exec to land before the exec.
func (f *taskFixture) wantSavesInOrder() {
	f.t.Helper()

	last := map[string]CommandPhase{}
	firstSeen := map[string]map[CommandPhase]int64{}

	for _, save := range f.vault.savedTasks() {
		for _, inst := range save.task.Instances {
			if inst.Command == nil {
				continue
			}

			for _, vm := range inst.Command.VMs {
				key := nodeKey(inst.Command.Deployment, vm.Reviewed.Index)

				if before, ok := last[key]; ok && phaseRank(vm.Phase) < phaseRank(before) {
					f.t.Errorf("a save wrote node %s in phase %s after it was saved in phase %s", key, vm.Phase, before)
				}

				last[key] = vm.Phase

				if firstSeen[key] == nil {
					firstSeen[key] = map[CommandPhase]int64{}
				}

				if _, ok := firstSeen[key][vm.Phase]; !ok {
					firstSeen[key][vm.Phase] = save.seq
				}
			}
		}
	}

	f.mu.Lock()
	calls, stamps := slices.Clone(f.calls), slices.Clone(f.stamps)
	f.mu.Unlock()

	for i, call := range calls {
		key := nodeKey(call.deployment, call.index)

		switch call.op {
		case opRunPrepare:
			if seq, ok := firstSeen[key][CommandPhaseStaged]; !ok || seq > stamps[i] {
				f.t.Errorf("node %s was sent prepare before its staged save landed", key)
			}
		case opRunExec:
			if seq, ok := firstSeen[key][CommandPhaseSent]; !ok || seq > stamps[i] {
				f.t.Errorf("node %s was sent exec before its sent save landed", key)
			}
		}
	}
}

// wantEachNodeSentOnce requires every named node to have been sent each
// operation of a run exactly once.
func (f *taskFixture) wantEachNodeSentOnce(deployment string, indices ...int) {
	f.t.Helper()

	want := []string{opSudoCheck, opRunPrepare, opRunExec, opRunCleanup}

	for _, index := range indices {
		if got := f.opsOf(deployment, index); !slices.Equal(got, want) {
			f.t.Errorf("node %s saw %v, want %v", nodeKey(deployment, index), got, want)
		}
	}
}

// outputOf reads what a node's settled record points at.
func (f *taskFixture) outputOf(node CommandVM) string {
	f.t.Helper()

	entry, found, err := f.manager.loadCommandOutput(context.Background(), node.OutputPath)
	if err != nil || !found {
		f.t.Fatalf("reading the output of node %d: found=%v err=%v", node.Reviewed.Index, found, err)
	}

	return string(entry.Stdout)
}

// stdoutOf is what the fixture's node prints.
func (f *taskFixture) stdoutOf(index int) string {
	if out, ok := f.stdouts[nodeKey("dep-0", index)]; ok {
		return out
	}

	return "out"
}

// waitForStop waits for the loop to say a deployment stopped, which it does
// before the nodes in flight have finished.
func (f *taskFixture) waitForStop() {
	f.t.Helper()

	waitUntil(f.t, "the deployment to stop", func() bool { return strings.Contains(f.log.joined(), "so the deployment stops") })
}

func TestCommandParallelRunsAtMostMaxInFlightNodes(t *testing.T) {
	fix := newTaskFixture(t, 1, 5, withParallel(2))

	for i := 0; i < 5; i++ {
		fix.stdouts[nodeKey("dep-0", i)] = "out-" + strconv.Itoa(i)
	}

	watch := fix.watch()
	gate := fix.gate(opRunExec)
	fix.queue()

	first := []string{gate.next(), gate.next()}
	slices.Sort(first)

	if want := []string{"dep-0/0", "dep-0/1"}; !slices.Equal(first, want) {
		t.Fatalf("the first two nodes in flight are %v, want %v", first, want)
	}

	// Each node that finishes frees one slot, which the next node in order
	// takes. The one still in flight stays held, so nothing else can start.
	gate.let("dep-0/1")
	gate.wantNext("dep-0/2")
	gate.let("dep-0/0")
	gate.wantNext("dep-0/3")
	gate.let("dep-0/2")
	gate.wantNext("dep-0/4")
	gate.let("dep-0/3")
	gate.let("dep-0/4")

	done := fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })

	if got := watch.peak(); got != 2 {
		t.Errorf("%d nodes were past prepare at once, want 2", got)
	}

	if got, want := watch.order()[2:], []string{"dep-0/2", "dep-0/3", "dep-0/4"}; !slices.Equal(got, want) {
		t.Errorf("the nodes after the first two started as %v, want %v", got, want)
	}

	paths := map[string]bool{}

	for i, node := range done.Instances[0].Command.VMs {
		if node.Phase != CommandPhaseSettled || node.Status != CommandStatusSucceeded {
			t.Errorf("node %d is %s/%s, want settled/succeeded", i, node.Phase, node.Status)
		}

		want, err := commandOutputPath(done.ID, done.Instances[0].InstanceID, node.Reviewed)
		if err != nil || node.OutputPath != want {
			t.Errorf("node %d keeps its output at %q, want %q", i, node.OutputPath, want)
		}

		paths[node.OutputPath] = true

		if got := fix.outputOf(node); got != fix.stdoutOf(i) {
			t.Errorf("node %d's entry holds %q, want %q", i, got, fix.stdoutOf(i))
		}
	}

	if len(paths) != 5 {
		t.Errorf("the nodes kept their output at %d paths, want 5", len(paths))
	}

	fix.wantEachNodeSentOnce("dep-0", 0, 1, 2, 3, 4)
	fix.wantSavesInOrder()
	fix.assertNoCommandText()
}

// A cap above the node count lets every node run at once.
func TestCommandParallelCapAboveTheNodeCountRunsThemAllAtOnce(t *testing.T) {
	fix := newTaskFixture(t, 1, 3, withParallel(10))
	watch := fix.watch()
	gate := fix.gate(opRunExec)
	fix.queue()

	got := []string{gate.next(), gate.next(), gate.next()}
	slices.Sort(got)

	if want := []string{"dep-0/0", "dep-0/1", "dep-0/2"}; !slices.Equal(got, want) {
		t.Fatalf("the nodes in flight are %v, want %v", got, want)
	}

	for _, key := range got {
		gate.let(key)
	}

	fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })

	if got := watch.peak(); got != 3 {
		t.Errorf("%d nodes were past prepare at once, want 3", got)
	}

	fix.wantEachNodeSentOnce("dep-0", 0, 1, 2)
	fix.wantSavesInOrder()
	fix.assertNoCommandText()
}

// With the option off the cap is ignored, and the nodes run one at a time
// in order.
func TestCommandParallelOffRunsOneNodeAtATime(t *testing.T) {
	fix := newTaskFixture(t, 1, 3, func(_ *taskFixture, task *UpgradeTask) { task.Command.MaxInFlight = 3 })
	watch := fix.watch()
	fix.queue()

	fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })

	if got := watch.peak(); got != 1 {
		t.Errorf("%d nodes were past prepare at once, want 1", got)
	}

	if got, want := watch.order(), []string{"dep-0/0", "dep-0/1", "dep-0/2"}; !slices.Equal(got, want) {
		t.Errorf("the nodes started as %v, want %v", got, want)
	}

	fix.wantSavesInOrder()
	fix.assertNoCommandText()
}

// Of any number of workers that reach one pending node, one drives it and
// the rest are refused with nothing sent.
func TestRunCommandNodeClaimsAPendingNodeForOneWorker(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)
	inst := &fix.task.Instances[0]
	gate := fix.gate(opSudoCheck)

	type result struct {
		stopped bool
		err     error
	}

	const workers = 4

	results := make(chan result, workers)

	for i := 0; i < workers; i++ {
		go func() {
			sessions := fix.manager.newCommandSessions(inst.Command.Deployment)
			defer sessions.close()

			stopped, err := fix.manager.runCommandNode(fix.manager.ctx, sessions, fix.task, inst, 0)
			results <- result{stopped, err}
		}()
	}

	// The winner waits at its sudo check. A second worker that got past the
	// claim would reach it too, and the refused ones answer at once.
	gate.next()

	for i := 0; i < workers-1; i++ {
		select {
		case got := <-results:
			if !errors.Is(got.err, errCommandNodeNotPending) {
				t.Fatalf("a second worker was not refused: stopped=%v err=%v", got.stopped, got.err)
			}
		case second := <-gate.arrived:
			t.Fatalf("two workers drove node %s", second)
		case <-time.After(waitTimeout):
			t.Fatal("the refused workers never returned")
		}
	}

	gate.let("dep-0/0")

	got := <-results
	if got.err != nil || got.stopped {
		t.Errorf("the winner returned stopped=%v err=%v", got.stopped, got.err)
	}

	fix.wantEachNodeSentOnce("dep-0", 0)
	fix.assertNoCommandText()
}

// A node left pending by a save that could not be confirmed is free to be
// claimed again, so a resume may drive it.
func TestRunCommandNodeGivesTheClaimBackWhenTheNodeIsPendingAgain(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)
	inst := &fix.task.Instances[0]

	fix.vault.mu.Lock()
	fix.vault.failPhase = CommandPhaseStaged
	fix.vault.mu.Unlock()

	sessions := fix.manager.newCommandSessions(inst.Command.Deployment)
	defer sessions.close()

	stopped, err := fix.manager.runCommandNode(fix.manager.ctx, sessions, fix.task, inst, 0)
	if err != nil || !stopped {
		t.Fatalf("the unconfirmed stage returned stopped=%v err=%v, want stopped", stopped, err)
	}

	err = fix.manager.claimCommandNode(inst, 0)
	if err != nil {
		t.Errorf("the node is pending again, but its claim was kept: %v", err)
	}

	fix.assertNoCommandText()
}

// An error that is not a changed VM stops the deployment, and here it is
// the refusal of a node that another driver holds.
func TestCommandParallelNodeRefusedByAClaimStopsTheDeployment(t *testing.T) {
	fix := newTaskFixture(t, 2, 4, withParallel(2))
	inst := &fix.task.Instances[0]
	rivalGate := fix.gate(opSudoCheck, "dep-0/2")
	exec := fix.gate(opRunExec, "dep-0/0", "dep-0/1")

	rival := make(chan error, 1)

	go func() {
		sessions := fix.manager.newCommandSessions(inst.Command.Deployment)
		defer sessions.close()

		_, err := fix.manager.runCommandNode(fix.manager.ctx, sessions, fix.task, inst, 2)
		rival <- err
	}()

	rivalGate.wantNext("dep-0/2")

	fix.queue()

	exec.next()
	exec.next()

	// Node 0 finishes, and the loop reaches node 2, which is still pending
	// and held by the rival.
	exec.let("dep-0/0")
	fix.waitForStop()

	// The node in flight is let go and recorded with its real result.
	exec.let("dep-0/1")

	paused := fix.waitFor("the task to pause", func(task UpgradeTask) bool { return task.Status == TaskStatusPaused && task.Paused })

	first := paused.Instances[0]
	if first.Status != InstanceStatusFailed {
		t.Errorf("the instance is %s, want failed", first.Status)
	}

	for _, index := range []int{0, 1} {
		if node := first.Command.VMs[index]; node.Phase != CommandPhaseSettled || node.Status != CommandStatusSucceeded {
			t.Errorf("node %d is %s/%s, want settled/succeeded", index, node.Phase, node.Status)
		}
	}

	if node := first.Command.VMs[2]; node.Phase != CommandPhasePending {
		t.Errorf("the node the rival holds is %s, want pending", node.Phase)
	}

	if node := first.Command.VMs[3]; node.Phase != CommandPhaseSkipped {
		t.Errorf("the node after the refused one is %s, want skipped", node.Phase)
	}

	if ops := fix.opsOf("dep-0", 3); len(ops) != 0 {
		t.Errorf("the node after the refused one saw %v", ops)
	}

	if paused.Instances[1].Status != InstanceStatusPending || fix.sentTo("dep-1") {
		t.Errorf("the second instance is %s and was sent to: %v", paused.Instances[1].Status, fix.sentTo("dep-1"))
	}

	rivalGate.let("dep-0/2")

	err := <-rival
	if err != nil {
		t.Errorf("the rival returned %v", err)
	}

	fix.assertNoCommandText()
}

// A failure on the second node, with the first and third in flight, starts
// no fourth node, lets the others finish with their real results, skips the
// rest, fails the instance, and pauses the task.
func TestCommandParallelFailureLetsTheNodesInFlightFinish(t *testing.T) {
	fix := newTaskFixture(t, 2, 5, withParallel(3))
	fix.exits["dep-0/1"] = 2
	fix.exits["dep-0/2"] = 5
	fix.stdouts["dep-0/0"] = "out-0"
	fix.stdouts["dep-0/2"] = "out-2"

	gate := fix.gate(opRunExec, "dep-0/0", "dep-0/1", "dep-0/2")
	fix.queue()

	gate.next()
	gate.next()
	gate.next()

	// Only the second node ends, while the first and third are in flight.
	gate.let("dep-0/1")

	fix.waitFor("the second node to settle", func(task UpgradeTask) bool {
		return task.Instances[0].Command.VMs[1].Phase == CommandPhaseSettled
	})

	gate.let("dep-0/0")
	gate.let("dep-0/2")

	paused := fix.waitFor("the task to pause", func(task UpgradeTask) bool { return task.Status == TaskStatusPaused && task.Paused })

	first := paused.Instances[0]
	if first.Status != InstanceStatusFailed {
		t.Fatalf("the instance is %s, want failed", first.Status)
	}

	want := []struct {
		status string
		exit   int
	}{{CommandStatusSucceeded, 0}, {CommandStatusFailed, 2}, {CommandStatusFailed, 5}}

	for i, w := range want {
		node := first.Command.VMs[i]
		if node.Phase != CommandPhaseSettled || node.Status != w.status || node.ExitCode == nil || *node.ExitCode != w.exit {
			t.Errorf("node %d is %s/%s with exit %v, want settled/%s with exit %d", i, node.Phase, node.Status, node.ExitCode, w.status, w.exit)
		}

		if got := fix.outputOf(node); got != fix.stdoutOf(i) {
			t.Errorf("node %d's entry holds %q, want %q", i, got, fix.stdoutOf(i))
		}
	}

	for _, index := range []int{3, 4} {
		if node := first.Command.VMs[index]; node.Phase != CommandPhaseSkipped {
			t.Errorf("node %d is %s, want skipped", index, node.Phase)
		}

		if ops := fix.opsOf("dep-0", index); len(ops) != 0 {
			t.Errorf("node %d saw %v, want nothing", index, ops)
		}
	}

	if paused.Instances[1].Status != InstanceStatusPending || fix.sentTo("dep-1") {
		t.Errorf("the second instance is %s and was sent to: %v", paused.Instances[1].Status, fix.sentTo("dep-1"))
	}

	fix.wantEachNodeSentOnce("dep-0", 0, 1, 2)
	fix.wantSavesInOrder()
	fix.assertNoCommandText()
}

// Processes that outlive KILL on one node stop the deployment like any
// failure, and keep the task paused with the instance held.
func TestCommandParallelSurvivedKillStopsTheDeploymentAndHoldsIt(t *testing.T) {
	fix := newTaskFixture(t, 1, 4, withParallel(2))
	fix.cleanups["dep-0/1"] = "cleanup started=yes exit=- killed=survived"

	gate := fix.gate(opRunExec, "dep-0/0", "dep-0/1")
	fix.queue()

	gate.next()
	gate.next()
	gate.let("dep-0/1")

	fix.waitFor("the surviving node to settle", func(task UpgradeTask) bool {
		return task.Instances[0].Command.VMs[1].Phase == CommandPhaseSettled
	})

	gate.let("dep-0/0")

	fix.waitFor("the instance to fail", func(task UpgradeTask) bool { return task.Instances[0].Status == InstanceStatusFailed })

	done := fix.snapshot()
	if done.Status != TaskStatusPaused || !done.Paused {
		t.Errorf("the task is %s paused=%v, want paused, since processes survived KILL", done.Status, done.Paused)
	}

	got := phases(done.Instances[0])
	if want := []CommandPhase{CommandPhaseSettled, CommandPhaseSettled, CommandPhaseSkipped, CommandPhaseSkipped}; !slices.Equal(got, want) {
		t.Errorf("phases = %v, want %v", got, want)
	}

	if node := done.Instances[0].Command.VMs[0]; node.Status != CommandStatusSucceeded {
		t.Errorf("the node in flight is %s, want its real result, succeeded", node.Status)
	}

	if node := done.Instances[0].Command.VMs[1]; !node.Survived {
		t.Error("the node whose processes survived is not recorded as such")
	}

	for _, index := range []int{2, 3} {
		if ops := fix.opsOf("dep-0", index); len(ops) != 0 {
			t.Errorf("node %d saw %v, want nothing", index, ops)
		}
	}

	other := &UpgradeTask{ID: "other", Kind: TaskKindUpgrade, Status: TaskStatusPending, Instances: []InstanceUpgrade{pendingInstance("inst-0", "dep-0")}}

	fix.manager.mu.Lock()
	_, reserved := fix.manager.reservations[fix.task.ID][fix.task.Instances[0].InstanceID]
	err := fix.manager.reserveNewLocked(other)
	fix.manager.mu.Unlock()

	if !reserved || err == nil {
		t.Errorf("the guard let go of the instance with surviving processes: reserved=%v, another task's reserve = %v", reserved, err)
	}

	fix.wantSavesInOrder()
	fix.assertNoCommandText()
}

// Both gates of a node still run under parallel. Root runs turned off while
// two nodes are in flight fail those two at their own gate, and no node
// starts after them.
func TestCommandParallelRootTurnedOffFailsTheNodesInFlightAtTheirGate(t *testing.T) {
	fix := newTaskFixture(t, 1, 4, withRoot(), withParallel(2))
	gate := fix.gate(opRunPrepare, "dep-0/0", "dep-0/1")
	fix.queue()

	gate.next()
	gate.next()

	fix.setRoot(false)

	gate.let("dep-0/0")
	gate.let("dep-0/1")

	done := fix.waitFor("the task to finish", func(task UpgradeTask) bool { return task.Status == TaskStatusFailed })

	got := phases(done.Instances[0])
	if want := []CommandPhase{CommandPhaseSettled, CommandPhaseSettled, CommandPhaseSkipped, CommandPhaseSkipped}; !slices.Equal(got, want) {
		t.Errorf("phases = %v, want %v", got, want)
	}

	for _, index := range []int{0, 1} {
		node := done.Instances[0].Command.VMs[index]
		if node.Status != CommandStatusFailed || !strings.Contains(node.Reason, "no longer allowed") {
			t.Errorf("node %d is %s with reason %q, want failed at its gate", index, node.Status, node.Reason)
		}

		if ops := fix.opsOf("dep-0", index); slices.Contains(ops, opRunExec) {
			t.Errorf("node %d was sent exec after the gate refused it: %v", index, ops)
		}
	}

	for _, index := range []int{2, 3} {
		if ops := fix.opsOf("dep-0", index); len(ops) != 0 {
			t.Errorf("node %d saw %v, want nothing", index, ops)
		}
	}

	fix.wantSavesInOrder()
	fix.assertNoCommandText()
}

// A deployment waits for every node of its pool before the next
// deployment starts.
func TestCommandParallelDeploymentsNeverOverlap(t *testing.T) {
	fix := newTaskFixture(t, 2, 2, withParallel(2))
	watch := fix.watch()
	gate := fix.gate(opRunExec, "dep-0/0", "dep-0/1")
	fix.queue()

	gate.next()
	gate.next()

	// One node of the first deployment ends and the other stays in flight.
	gate.let("dep-0/1")

	fix.waitFor("a node of the first deployment to settle", func(task UpgradeTask) bool {
		return task.Instances[0].Command.VMs[1].Phase == CommandPhaseSettled
	})

	gate.let("dep-0/0")

	fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })

	if got := watch.overlapping(); len(got) != 0 {
		t.Errorf("nodes %v began while another deployment had a node in flight", got)
	}

	// Every call to the first deployment comes before any call to the
	// second, so the second never began while the first was in flight.
	seenSecond := false

	for _, call := range fix.recorded() {
		switch {
		case call.deployment == "dep-1":
			seenSecond = true
		case seenSecond:
			t.Errorf("%s was sent to %s/%d after the second deployment began", call.op, call.deployment, call.index)
		}
	}

	fix.wantSavesInOrder()
	fix.assertNoCommandText()
}

// owned reports whether a processor holds the task.
func (f *taskFixture) owned() bool {
	f.manager.mu.RLock()
	defer f.manager.mu.RUnlock()

	return f.manager.owned[f.task.ID]
}

// resume lifts the pause the way an operator does.
func (f *taskFixture) resume() {
	f.t.Helper()

	err := f.manager.ResumeTask(context.Background(), f.task.ID)
	if err != nil {
		f.t.Fatalf("resume: %v", err)
	}
}

// wantEverySettled requires every node of the instance to be settled.
func wantEverySettled(t *testing.T, inst InstanceUpgrade) {
	t.Helper()

	for i, node := range inst.Command.VMs {
		if node.Phase != CommandPhaseSettled {
			t.Errorf("node %d is %s, want settled", i, node.Phase)
		}
	}
}

// A task that a worker stopped while a sibling was in flight, and that the
// operator resumed before the sibling ended, goes on once the sibling ends.
func TestCommandParallelResumeDuringDrainGoesOn(t *testing.T) {
	fix := newTaskFixture(t, 1, 3, withParallel(2))
	hold0 := fix.gate(opSudoCheck, "dep-0/0")
	exec1 := fix.gate(opRunExec, "dep-0/1")
	fix.queue()

	hold0.next()
	exec1.next()

	// The save of node 0's staged phase can't be confirmed, so node 0
	// stops the task while node 1 is in flight.
	fix.vault.mu.Lock()
	fix.vault.failPhase = CommandPhaseStaged
	fix.vault.mu.Unlock()

	hold0.let("dep-0/0")
	fix.waitFor("the task to pause", func(task UpgradeTask) bool { return task.Paused })

	fix.vault.mu.Lock()
	fix.vault.failPhase = ""
	fix.vault.mu.Unlock()

	fix.resume()
	exec1.let("dep-0/1")

	done := fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })

	wantEverySettled(t, done.Instances[0])

	waitUntil(t, "the processor to let the task go", func() bool { return !fix.owned() })

	// Node 0 is driven again after its stage save was lost, so it passes
	// the sudo check twice, but no node is sent the command twice.
	for index := range 3 {
		if n := strings.Count(strings.Join(fix.opsOf("dep-0", index), " "), opRunExec); n != 1 {
			t.Errorf("node %d was sent exec %d times, want once", index, n)
		}
	}

	fix.assertNoCommandText()
}

// Processes that outlived KILL on the last instance's node, with the
// operator resuming before the sibling ends, finish the task and leave no
// task running without a processor.
func TestCommandParallelResumeDuringDrainAfterSurvivedFinishesTheTask(t *testing.T) {
	fix := newTaskFixture(t, 1, 3, withParallel(2))
	fix.cleanups["dep-0/1"] = "cleanup started=yes exit=- killed=survived"
	gate := fix.gate(opRunExec, "dep-0/0", "dep-0/1")
	fix.queue()

	gate.next()
	gate.next()
	gate.let("dep-0/1")

	fix.waitFor("the task to pause", func(task UpgradeTask) bool {
		return task.Paused && task.Instances[0].Command.VMs[1].Phase == CommandPhaseSettled
	})

	fix.resume()
	gate.let("dep-0/0")

	done := fix.waitFor("the task to finish", func(task UpgradeTask) bool {
		return task.Status == TaskStatusFailed || (task.Status == TaskStatusRunning && !task.Paused && task.Instances[0].Status == InstanceStatusFailed && !fix.owned())
	})

	if done.Status != TaskStatusFailed {
		t.Errorf("the task is %s paused=%v owned=%v, want failed", done.Status, done.Paused, fix.owned())
	}

	fix.assertNoCommandText()
}

// A worker that panics pauses the task, keeps the processor, and leaves the
// node in its last confirmed phase, so a resume settles a staged or sent
// node through cleanup only and drives a pending one.
func TestCommandWorkerPanicPausesTheTask(t *testing.T) {
	cases := []struct {
		name     string
		options  []taskOption
		op       string
		phase    CommandPhase
		settles  bool
		wantExec int
	}{
		{"sequential before staged", nil, opSudoCheck, CommandPhasePending, false, 1},
		{"sequential staged", nil, opRunPrepare, CommandPhaseStaged, true, 0},
		{"sequential sent", nil, opRunExec, CommandPhaseSent, true, 1},
		{"parallel before staged", []taskOption{withParallel(2)}, opSudoCheck, CommandPhasePending, false, 1},
		{"parallel staged", []taskOption{withParallel(2)}, opRunPrepare, CommandPhaseStaged, true, 0},
		{"parallel sent", []taskOption{withParallel(2)}, opRunExec, CommandPhaseSent, true, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fix := newTaskFixture(t, 1, 3, tc.options...)
			if tc.settles {
				fix.cleanups["dep-0/1"] = cleanupFailedExit
			}

			var fired atomic.Bool

			previous := fix.onOp
			fix.onOp = func(deployment string, index int, op string) {
				if previous != nil {
					previous(deployment, index, op)
				}

				if index == 1 && op == tc.op && fired.CompareAndSwap(false, true) {
					panic("boom " + runCommandText)
				}
			}

			fix.queue()

			paused := fix.waitFor("the task to pause", func(task UpgradeTask) bool { return task.Paused && task.Status == TaskStatusPaused })

			if paused.Instances[0].Error == "" {
				t.Error("the instance names no error for the panic")
			}

			if got := paused.Instances[0].Command.VMs[1].Phase; got != tc.phase {
				t.Errorf("the node is %s after the panic, want %s", got, tc.phase)
			}

			if !fix.owned() {
				t.Error("no processor holds the paused task")
			}

			fix.resume()

			done := fix.waitFor("the task to finish", func(task UpgradeTask) bool {
				return task.Status == TaskStatusCompleted || task.Status == TaskStatusFailed
			})

			node := done.Instances[0].Command.VMs[1]
			if node.Phase != CommandPhaseSettled {
				t.Errorf("the node is %s after the resume, want settled", node.Phase)
			}

			ops := fix.opsOf("dep-0", 1)
			if n := strings.Count(strings.Join(ops, " "), opRunExec); n != tc.wantExec {
				t.Errorf("the node was sent exec %d times (%v), want %d", n, ops, tc.wantExec)
			}

			if !tc.settles && done.Status != TaskStatusCompleted {
				t.Errorf("the task is %s, want completed, since the node ran after the resume", done.Status)
			}

			fix.assertNoCommandText()
		})
	}
}

// A failure that lands while the operator has paused the task is processed
// at once, so the record shows the failed instance and the skipped nodes
// during the pause, and the resume does not stop on that instance again.
func TestCommandParallelHaltDuringAPauseIsProcessedBeforeTheResume(t *testing.T) {
	fix := newTaskFixture(t, 2, 4, withParallel(2))
	fix.exits["dep-0/0"] = 2
	gate := fix.gate(opRunExec, "dep-0/0", "dep-0/1")
	fix.queue()

	gate.next()
	gate.next()

	err := fix.manager.PauseTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}

	gate.let("dep-0/1")

	fix.waitFor("node 1 to settle", func(task UpgradeTask) bool {
		return task.Instances[0].Command.VMs[1].Phase == CommandPhaseSettled
	})

	gate.let("dep-0/0")

	paused := fix.waitFor("the halted instance to fail during the pause", func(task UpgradeTask) bool {
		return task.Instances[0].Status == InstanceStatusFailed
	})

	want := []CommandPhase{CommandPhaseSettled, CommandPhaseSettled, CommandPhaseSkipped, CommandPhaseSkipped}
	if got := phases(paused.Instances[0]); !slices.Equal(got, want) {
		t.Errorf("phases = %v, want %v", got, want)
	}

	if !paused.Paused || fix.sentTo("dep-1") {
		t.Errorf("the task paused=%v and the second instance was sent to: %v", paused.Paused, fix.sentTo("dep-1"))
	}

	fix.resume()

	done := fix.waitFor("the second instance to run", func(task UpgradeTask) bool { return task.Instances[1].Status == InstanceStatusSuccess })

	if done.Instances[0].Status != InstanceStatusFailed {
		t.Errorf("the first instance is %s, want failed", done.Instances[0].Status)
	}

	fix.assertNoCommandText()
}

// Leaving the loop on a cancel still waits for the nodes in flight, so no
// session closes under a node and the task stays owned until they end.
func TestCommandParallelCancelWaitsForTheNodesInFlight(t *testing.T) {
	fix := newTaskFixture(t, 1, 3, withParallel(2))
	gate := fix.gate(opRunExec, "dep-0/0", "dep-0/1")

	var closedAtCleanup atomic.Int32

	closedAtCleanup.Store(-1)

	var ownedAtCleanup atomic.Bool

	previous := fix.onOp
	fix.onOp = func(deployment string, index int, op string) {
		if previous != nil {
			previous(deployment, index, op)
		}

		if index == 0 && op == opRunCleanup {
			closedAtCleanup.Store(fix.closed.Load())
			ownedAtCleanup.Store(fix.owned())
		}
	}

	fix.queue()

	gate.next()
	gate.next()

	fix.manager.mu.Lock()
	fix.task.Status = TaskStatusCancelled
	fix.manager.mu.Unlock()

	// Node 1's slot lets the feeder reach the cancel, which it logs.
	gate.let("dep-0/1")

	waitUntil(t, "the loop to see the cancel", func() bool { return strings.Contains(fix.log.joined(), "was cancelled") })

	gate.let("dep-0/0")

	waitUntil(t, "node 0 to clean up", func() bool { return closedAtCleanup.Load() >= 0 })

	if got := closedAtCleanup.Load(); got != 0 {
		t.Errorf("%d sessions were closed while node 0 was in flight", got)
	}

	if !ownedAtCleanup.Load() {
		t.Error("the task was let go while node 0 was in flight")
	}

	fix.assertNoCommandText()
}

// A slot taken after a worker reported a halt is refused, so no node starts
// once the deployment must stop.
func TestCommandPoolRefusesASlotAfterAHaltingWorkerReports(t *testing.T) {
	for range 2000 {
		pool := newCommandPool(1)

		if !pool.acquire() {
			t.Fatal("the first slot was refused")
		}

		pool.start(func() (bool, bool) { return false, true }, func(any) {})

		if pool.acquire() {
			t.Fatal("a slot was given after the worker reported a halt")
		}

		pool.wait()
	}
}

// A resume that lands between the pause a worker made and the processor's
// read of it, with no node moved, still carries the task on to its end.
func TestCommandResumeBeforeTheProcessorReadsThePauseGoesOn(t *testing.T) {
	fix := newTaskFixture(t, 1, 2)

	fix.vault.mu.Lock()
	fix.vault.failPhase = CommandPhaseStaged
	fix.vault.mu.Unlock()

	var resumed atomic.Bool

	fix.log.hook = func(line string) {
		if !strings.Contains(line, "could not confirm") || !resumed.CompareAndSwap(false, true) {
			return
		}

		fix.vault.mu.Lock()
		fix.vault.failPhase = ""
		fix.vault.mu.Unlock()

		fix.resume()
	}

	fix.queue()

	done := fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })

	wantEverySettled(t, done.Instances[0])
	waitUntil(t, "the processor to let the task go", func() bool { return !fix.owned() })

	fix.assertNoCommandText()
}

// A deployment halted in the same pass as a stop is still failed when the
// resume ends it, even though the pass after the resume halts nothing.
func TestCommandHaltBeforeAStopFailsTheInstanceAfterTheResume(t *testing.T) {
	fix := newTaskFixture(t, 1, 4, withParallel(2))
	inst := &fix.task.Instances[0]
	sudo := fix.gate(opSudoCheck, "dep-0/0", "dep-0/2")
	exec := fix.gate(opRunExec, "dep-0/1")

	rival := make(chan error, 1)

	go func() {
		sessions := fix.manager.newCommandSessions(inst.Command.Deployment)
		defer sessions.close()

		_, err := fix.manager.runCommandNode(fix.manager.ctx, sessions, fix.task, inst, 2)
		rival <- err
	}()

	sudo.wantNext("dep-0/2")

	fix.queue()

	sudo.wantNext("dep-0/0")
	exec.wantNext("dep-0/1")

	// Node 1 ends, and the loop reaches node 2, which the rival holds, so
	// the deployment halts while node 0 is still in flight.
	exec.let("dep-0/1")
	fix.waitForStop()

	// Node 0 then stops the task, because its stage save is not confirmed.
	fix.vault.mu.Lock()
	fix.vault.failPhase = CommandPhaseStaged
	fix.vault.mu.Unlock()

	sudo.let("dep-0/0")
	fix.waitFor("the task to pause", func(task UpgradeTask) bool { return task.Paused })

	fix.vault.mu.Lock()
	fix.vault.failPhase = ""
	fix.vault.mu.Unlock()

	sudo.let("dep-0/2")

	err := <-rival
	if err != nil {
		t.Errorf("the rival returned %v", err)
	}

	fix.resume()

	done := fix.waitFor("the instance to end", func(task UpgradeTask) bool { return task.Instances[0].Status.settled() })

	if got := done.Instances[0].Status; got != InstanceStatusFailed {
		t.Errorf("the instance is %s, want failed", got)
	}

	halted := "Blacksmith couldn't run a node of this deployment, so the rest of the deployment was skipped. " +
		"The node rows show why, and the Continue remaining instances button moves on to the instances still waiting."
	if got := done.Instances[0].Error; got != halted {
		t.Errorf("the instance says %q, want %q", got, halted)
	}

	fix.assertNoCommandText()
}

// Root can be turned off after the task is queued and before its first
// node starts, which is the window refusedBeforeStart covers.
func TestCommandTaskRefusedBeforeAnyNodeSaysWhatToDo(t *testing.T) {
	fix := newTaskFixture(t, 2, 1, withRoot())
	fix.setRoot(false)

	if !fix.manager.refusedBeforeStart(context.Background(), fix.task, &fix.task.Instances[0]) {
		t.Fatal("the refused task did not pause")
	}

	paused := fix.snapshot()

	want := "Running this command is no longer allowed, so the task paused before any node started. " +
		"403 root_disabled: Running commands as root is switched off on this broker, so choose vcap. " +
		"An administrator can turn it on with commands.allow_root in the broker configuration." + commandRefusedNextStep
	if got := paused.Instances[0].Error; got != want {
		t.Errorf("the first instance says %q, want %q", got, want)
	}

	fix.assertNoCommandText()
}

// expectUnlockedAfterPanic runs region, which must panic, and fails the
// test unless the manager's lock is free afterwards, so the worker's panic
// handler can still take it.
func expectUnlockedAfterPanic(t *testing.T, manager *Manager, region func()) {
	t.Helper()

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the region did not panic")
			}
		}()

		region()
	}()

	waitUntil(t, "taking the manager's lock after the panic", func() bool {
		if !manager.mu.TryLock() {
			return false
		}

		manager.mu.Unlock()

		return true
	})
}

// A panic while connect records the matched VM leaves the lock free.
func TestCommandConnectPanicLeavesTheLockFree(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	run := &commandRun{
		m: fix.manager, sessions: fix.sessions, task: fix.task, inst: fix.inst,
		vm: len(fix.inst.Command.VMs), identity: fix.inst.Command.VMs[0].Reviewed, indices: []int{0},
	}

	expectUnlockedAfterPanic(t, fix.manager, func() {
		_ = run.connect(context.Background(), run.identity)
	})
}

// A panic in the change advance makes to a node leaves the lock free.
func TestCommandAdvancePanicLeavesTheLockFree(t *testing.T) {
	t.Parallel()

	fix := newRunFixture(t)
	run := &commandRun{m: fix.manager, task: fix.task, inst: fix.inst}

	expectUnlockedAfterPanic(t, fix.manager, func() {
		_ = run.advance(context.Background(), func(*CommandVM) { panic("the change failed") })
	})
}
