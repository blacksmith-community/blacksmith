package upgrade

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/bosh/sshconn"
	"blacksmith/internal/config"
)

const taskFixtureID = runTaskID

// nodeCall is one operation the fake sessions saw, in the order the
// fixture's sessions saw them.
type nodeCall struct {
	deployment string
	index      int
	op         string
}

// taskDirector lists the VMs of every deployment of the fixture, counts the
// listings, and counts the deploys the manager sends.
type taskDirector struct {
	*waitDirector

	fix     *taskFixture
	deploys atomic.Int32

	mu       sync.Mutex
	listings int
}

func (d *taskDirector) GetDeploymentVMs(_ context.Context, deployment string) ([]bosh.VM, error) {
	d.mu.Lock()
	d.listings++
	d.mu.Unlock()

	d.fix.mu.Lock()
	defer d.fix.mu.Unlock()

	var vms []bosh.VM

	for _, listed := range d.fix.listed[deployment] {
		vms = append(vms, vmOf(listed))
	}

	return vms, nil
}

func (d *taskDirector) UpdateDeployment(context.Context, string, string) (*bosh.Task, error) {
	d.deploys.Add(1)

	return nil, errors.New("a command task must never deploy")
}

func (d *taskDirector) listed() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.listings
}

// taskSession answers the operations of one deployment's nodes.
type taskSession struct {
	fix        *taskFixture
	deployment string
	hosts      []sshconn.Host
	indices    []int
}

func (s *taskSession) VMs() []sshconn.Host { return slices.Clone(s.hosts) }

func (s *taskSession) Close() error { return nil }

func (s *taskSession) Run(_ context.Context, host int, op string, _ []string, stdin io.Reader) (ssh.Result, error) {
	if stdin != nil {
		_, err := io.Copy(io.Discard, stdin)
		if err != nil {
			return ssh.Result{}, fmt.Errorf("reading stdin: %w", err)
		}
	}

	return s.fix.answer(s.deployment, s.indices[host], op)
}

// taskFixture is a manager with a command task of several instances, each
// with several nodes, and fake sessions that record every operation.
type taskFixture struct {
	t        *testing.T
	manager  *Manager
	director *taskDirector
	log      *captureLog
	vault    *runVault
	task     *UpgradeTask

	mu     sync.Mutex
	calls  []nodeCall
	listed map[string][]CommandVMIdentity
	flight int
	peak   int
	held   int

	// cfg is what the authorizer reads.
	cfg config.CommandsConfig

	// exits maps "deployment/index" to the exit status of exec.
	exits map[string]int
	// onOp runs before a node's operation is answered.
	onOp func(deployment string, index int, op string)
}

type taskOption func(*taskFixture, *UpgradeTask)

// withRoot makes the task run as root, and turns root runs on.
func withRoot() taskOption {
	return func(f *taskFixture, task *UpgradeTask) {
		task.Command.RunAs = "root"
		f.setRoot(true)
	}
}

func (f *taskFixture) setRoot(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.cfg.Root = &on
}

func newTaskFixture(t *testing.T, instances, nodes int, options ...taskOption) *taskFixture {
	t.Helper()

	var seq atomic.Int64

	fix := &taskFixture{t: t, log: &captureLog{}, listed: map[string][]CommandVMIdentity{}, exits: map[string]int{}}
	fix.vault = &runVault{confirmVault: newConfirmVault(), seq: &seq, taskID: taskFixtureID}

	command := runCommandText

	task := &UpgradeTask{
		ID:     taskFixtureID,
		Name:   "run uptime",
		Kind:   TaskKindCommand,
		Status: TaskStatusPending,
		Command: &CommandTaskSpec{
			Command:        command,
			CommandDigest:  commandDigest(command),
			CommandLength:  len(command),
			RunAs:          "vcap",
			TimeoutSeconds: runTimeoutSecs,
		},
		TotalCount: instances,
		CreatedAt:  time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC),
	}

	for i := 0; i < instances; i++ {
		deployment := "dep-" + strconv.Itoa(i)
		record := &InstanceCommand{Deployment: deployment}

		for j := 0; j < nodes; j++ {
			identity := CommandVMIdentity{Group: "redis", Index: j, ID: fmt.Sprintf("vm-%d-%d", i, j), CID: fmt.Sprintf("cid-%d-%d", i, j), AZ: "z1"}
			record.VMs = append(record.VMs, CommandVM{Reviewed: identity, Phase: CommandPhasePending})
			fix.listed[deployment] = append(fix.listed[deployment], identity)
		}

		task.Instances = append(task.Instances, InstanceUpgrade{
			InstanceID:     "inst-" + strconv.Itoa(i),
			InstanceName:   "redis",
			DeploymentName: deployment,
			Status:         InstanceStatusPending,
			Command:        record,
		})
	}

	fix.task = task

	for _, option := range options {
		option(fix, task)
	}

	fix.director = &taskDirector{waitDirector: newWaitDirector(), fix: fix}

	manager := NewManager(fix.log, fix.director, fix.vault)

	deps := Deps{
		Now: func() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) },
		CommandRunner: func(_ context.Context, deployment, _ string, indices []int) (PatchSession, error) {
			sess := &taskSession{fix: fix, deployment: deployment, indices: slices.Clone(indices)}

			for _, index := range indices {
				sess.hosts = append(sess.hosts, sshconn.Host{Job: "redis", IndexOrID: strconv.Itoa(index), Address: "10.0.0.10"})
			}

			return sess, nil
		},
		Authorizer: NewConfigAuthorizer(func() config.CommandsConfig {
			fix.mu.Lock()
			defer fix.mu.Unlock()

			return fix.cfg
		}),
	}

	err := manager.Configure(deps)
	if err != nil {
		t.Fatalf("configuring the manager: %v", err)
	}

	t.Cleanup(manager.Stop)
	manager.Start()
	waitForLoad(t, manager)

	fix.manager = manager

	err = manager.reserveCertificateTask(task)
	if err != nil {
		t.Fatalf("reserving the task: %v", err)
	}

	return fix
}

// setSlots limits how many tasks run at once.
func (f *taskFixture) setSlots(n int) {
	f.t.Helper()

	f.manager.settingsLock.Lock()
	f.manager.settings.MaxBatchJobs = n
	f.manager.settingsLock.Unlock()
	f.manager.updateJobSemaphore()
}

// answer plays the VM for one operation.
func (f *taskFixture) answer(deployment string, index int, op string) (ssh.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, nodeCall{deployment: deployment, index: index, op: op})
	hook := f.onOp
	exit := f.exits[fmt.Sprintf("%s/%d", deployment, index)]
	f.mu.Unlock()

	if hook != nil {
		hook(deployment, index, op)
	}

	switch op {
	case opSudoCheck:
		return ssh.Result{}, nil
	case opRunPrepare:
		digest := commandDigest(runCommandText)

		return ssh.Result{Stdout: []byte("staged bytes=" + strconv.Itoa(len(runCommandText)) + " sha256=" + digest + "\n")}, nil
	case opRunExec:
		f.enter()
		defer f.leave()

		return ssh.Result{Stdout: []byte("out"), ExitCode: exit}, nil
	case opRunCleanup:
		return ssh.Result{Stdout: []byte(runCleanupDone + "\n")}, nil
	}

	f.t.Errorf("unexpected op %s", op)

	return ssh.Result{}, nil
}

// enter records a node running, and the number of slots the manager hands
// out at that moment.
func (f *taskFixture) enter() {
	slots := 0

	f.manager.settingsLock.RLock()
	if f.manager.jobSemaphore != nil {
		slots = len(f.manager.jobSemaphore)
	}
	f.manager.settingsLock.RUnlock()

	f.mu.Lock()
	defer f.mu.Unlock()

	f.flight++
	f.peak = max(f.peak, f.flight)
	f.held = max(f.held, slots)
}

func (f *taskFixture) leave() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.flight--
}

// queue hands the task to the dispatcher the way a create does.
func (f *taskFixture) queue() {
	f.manager.mu.Lock()
	f.manager.owned[f.task.ID] = true
	f.manager.mu.Unlock()

	f.manager.enqueue(f.task)
}

func (f *taskFixture) recorded() []nodeCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.calls)
}

// opsOf returns the operations sent to the deployment's node, in order.
func (f *taskFixture) opsOf(deployment string, index int) []string {
	var ops []string

	for _, call := range f.recorded() {
		if call.deployment == deployment && call.index == index {
			ops = append(ops, call.op)
		}
	}

	return ops
}

func (f *taskFixture) sentTo(deployment string) bool {
	return slices.ContainsFunc(f.recorded(), func(call nodeCall) bool { return call.deployment == deployment })
}

func (f *taskFixture) snapshot() UpgradeTask {
	f.manager.mu.RLock()
	defer f.manager.mu.RUnlock()

	copied := *f.task
	copied.Instances = nil

	for i := range f.task.Instances {
		inst := f.task.Instances[i]
		inst.Command = inst.Command.clone()
		copied.Instances = append(copied.Instances, inst)
	}

	return copied
}

func (f *taskFixture) waitFor(what string, done func(UpgradeTask) bool) UpgradeTask {
	f.t.Helper()

	var last UpgradeTask

	waitUntil(f.t, what, func() bool {
		last = f.snapshot()

		return done(last)
	})

	return last
}

func (f *taskFixture) assertNoCommandText() {
	f.t.Helper()

	(&runFixture{t: f.t, log: f.log, vault: f.vault}).assertNoCommandText()
}

func phases(inst InstanceUpgrade) []CommandPhase {
	var out []CommandPhase

	for _, vm := range inst.Command.VMs {
		out = append(out, vm.Phase)
	}

	return out
}

func TestCommandTaskRunsEveryNodeOneAtATime(t *testing.T) {
	fix := newTaskFixture(t, 2, 2)
	fix.setSlots(1)
	fix.queue()

	done := fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })

	cycle := []string{opSudoCheck, opRunPrepare, opRunExec, opRunCleanup}

	var want []nodeCall

	for _, deployment := range []string{"dep-0", "dep-1"} {
		for index := 0; index < 2; index++ {
			for _, op := range cycle {
				want = append(want, nodeCall{deployment: deployment, index: index, op: op})
			}
		}
	}

	if got := fix.recorded(); !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}

	if fix.peak != 1 {
		t.Errorf("%d nodes ran at once, want 1", fix.peak)
	}

	if fix.held != 1 {
		t.Errorf("the task held %d slots while it ran, want 1", fix.held)
	}

	if done.CompletedCount != 2 || done.FailedCount != 0 {
		t.Errorf("counts = %d completed, %d failed, want 2 and 0", done.CompletedCount, done.FailedCount)
	}

	for _, inst := range done.Instances {
		if inst.Status != InstanceStatusSuccess {
			t.Errorf("instance %s is %s, want success", inst.InstanceID, inst.Status)
		}

		for _, vm := range inst.Command.VMs {
			if vm.Phase != CommandPhaseSettled || vm.Status != CommandStatusSucceeded {
				t.Errorf("node %s/%d is %s/%s, want settled/succeeded", inst.InstanceID, vm.Reviewed.Index, vm.Phase, vm.Status)
			}
		}
	}

	if n := fix.director.deploys.Load(); n != 0 {
		t.Errorf("the director was sent %d deploys for a command task", n)
	}

	fix.assertNoCommandText()
}

func TestCommandTaskNeverReachesTheDeployPath(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)

	fix.manager.mu.Lock()
	fix.manager.owned[fix.task.ID] = true
	fix.manager.mu.Unlock()

	checked := false

	goOn := fix.manager.processInstance(fix.task, &fix.task.Instances[0], make(chan struct{}, 1), &checked)
	if !goOn {
		t.Error("processInstance stopped a command instance that ran to its end")
	}

	if n := fix.director.deploys.Load(); n != 0 {
		t.Fatalf("processInstance sent %d deploys for a command task", n)
	}

	if ops := fix.opsOf("dep-0", 0); !slices.Equal(ops, []string{opSudoCheck, opRunPrepare, opRunExec, opRunCleanup}) {
		t.Errorf("the node saw %v, want the command's four operations", ops)
	}

	if status := fix.snapshot().Instances[0].Status; status != InstanceStatusSuccess {
		t.Errorf("the instance is %s, want success", status)
	}
}

func TestCommandTaskFailurePausesAndResumeSkipsTheFailedInstance(t *testing.T) {
	fix := newTaskFixture(t, 2, 2)
	fix.exits["dep-0/0"] = 3
	fix.queue()

	paused := fix.waitFor("the task to pause", func(task UpgradeTask) bool { return task.Status == TaskStatusPaused && task.Paused })

	first := paused.Instances[0]
	if first.Status != InstanceStatusFailed {
		t.Fatalf("the first instance is %s, want failed", first.Status)
	}

	if got := []CommandPhase{CommandPhaseSettled, CommandPhaseSkipped}; !slices.Equal(phases(first), got) {
		t.Errorf("phases = %v, want %v", phases(first), got)
	}

	node := first.Command.VMs[0]
	if node.Status != CommandStatusFailed || node.ExitCode == nil || *node.ExitCode != 3 {
		t.Errorf("the first node is %s with exit %v, want failed with exit 3", node.Status, node.ExitCode)
	}

	if paused.Instances[1].Status != InstanceStatusPending || fix.sentTo("dep-1") {
		t.Errorf("the second instance is %s and was sent to: %v", paused.Instances[1].Status, fix.sentTo("dep-1"))
	}

	if len(fix.opsOf("dep-0", 1)) != 0 {
		t.Errorf("the skipped node saw %v", fix.opsOf("dep-0", 1))
	}

	before := len(fix.recorded())

	err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	done := fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })

	for _, call := range fix.recorded()[before:] {
		if call.deployment != "dep-1" {
			t.Fatalf("the resume sent %s to %s, which is not the second instance", call.op, call.deployment)
		}
	}

	if done.Instances[1].Status != InstanceStatusSuccess || done.Instances[0].Status != InstanceStatusFailed {
		t.Errorf("instances are %s and %s, want failed and success", done.Instances[0].Status, done.Instances[1].Status)
	}

	fix.assertNoCommandText()
}

func TestCommandTaskSkipsAVMWithNoCIDWithoutFailing(t *testing.T) {
	fix := newTaskFixture(t, 1, 2)

	fix.task.Instances[0].Command.VMs[0].Reviewed.CID = ""
	fix.listed["dep-0"][0].CID = ""
	fix.queue()

	done := fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })

	inst := done.Instances[0]
	if got := []CommandPhase{CommandPhaseSkipped, CommandPhaseSettled}; !slices.Equal(phases(inst), got) {
		t.Errorf("phases = %v, want %v", phases(inst), got)
	}

	if inst.Status != InstanceStatusSuccess || done.FailedCount != 0 {
		t.Errorf("the instance is %s with %d failures, want success and none", inst.Status, done.FailedCount)
	}

	if len(fix.opsOf("dep-0", 0)) != 0 {
		t.Errorf("a VM with no CID saw %v", fix.opsOf("dep-0", 0))
	}
}

func TestDispatcherDropsACommandTaskTheAuthorizerRefuses(t *testing.T) {
	fix := newTaskFixture(t, 1, 1, withRoot())
	fix.setRoot(false)
	fix.queue()

	waitUntil(t, "the dispatcher to drop the task", func() bool {
		fix.manager.mu.RLock()
		defer fix.manager.mu.RUnlock()

		return !fix.manager.owned[fix.task.ID]
	})

	if calls := fix.recorded(); len(calls) != 0 {
		t.Errorf("a refused task sent %v", calls)
	}

	if status := fix.snapshot().Status; status != TaskStatusPending {
		t.Errorf("the dropped task is %s, want pending", status)
	}

	if !strings.Contains(fix.log.joined(), "Skipping command task") {
		t.Error("the drop was not logged")
	}
}

func TestDispatcherRunsACommandTaskTheAuthorizerPasses(t *testing.T) {
	fix := newTaskFixture(t, 1, 1, withRoot())
	fix.queue()

	fix.waitFor("the task to complete", func(task UpgradeTask) bool { return task.Status == TaskStatusCompleted })
}

func TestRootTaskWaitingForASlotIsRefusedWhenRootTurnsOff(t *testing.T) {
	fix := newTaskFixture(t, 2, 1, withRoot())
	fix.setSlots(1)

	fix.manager.settingsLock.RLock()
	semaphore := fix.manager.jobSemaphore
	fix.manager.settingsLock.RUnlock()

	semaphore <- struct{}{}

	fix.queue()

	waitUntil(t, "the task to wait for its slot", func() bool {
		fix.manager.taskCancelLock.RLock()
		defer fix.manager.taskCancelLock.RUnlock()

		_, waiting := fix.manager.taskCancelCh[fix.task.ID]

		return waiting
	})

	fix.setRoot(false)

	<-semaphore

	paused := fix.waitFor("the task to pause", func(task UpgradeTask) bool { return task.Status == TaskStatusPaused })

	node := paused.Instances[0].Command.VMs[0]
	if node.Status != CommandStatusFailed || !strings.Contains(node.Error, "403") || !strings.Contains(node.Error, apierr.CodeRootDisabled) {
		t.Errorf("the node is %s with error %q, want failed with 403 %s", node.Status, node.Error, apierr.CodeRootDisabled)
	}

	if calls := fix.recorded(); len(calls) != 0 {
		t.Errorf("a refused root task sent %v", calls)
	}

	fix.assertNoCommandText()
}

func TestNodeGateRefusesBeforePrepareWhenRootTurnsOffBetweenNodes(t *testing.T) {
	fix := newTaskFixture(t, 2, 2, withRoot())
	fix.onOp = func(deployment string, index int, op string) {
		if deployment == "dep-0" && index == 0 && op == opRunCleanup {
			fix.setRoot(false)
		}
	}

	fix.queue()

	paused := fix.waitFor("the task to pause", func(task UpgradeTask) bool { return task.Status == TaskStatusPaused })

	first := paused.Instances[0]
	if got := []CommandPhase{CommandPhaseSettled, CommandPhaseSettled}; !slices.Equal(phases(first), got) {
		t.Fatalf("phases = %v, want %v", phases(first), got)
	}

	if first.Command.VMs[0].Status != CommandStatusSucceeded {
		t.Errorf("the first node is %s, want succeeded", first.Command.VMs[0].Status)
	}

	second := first.Command.VMs[1]
	if second.Status != CommandStatusFailed || !strings.Contains(second.Error, "403") || !strings.Contains(second.Error, apierr.CodeRootDisabled) {
		t.Errorf("the second node is %s with error %q, want failed with 403 %s", second.Status, second.Error, apierr.CodeRootDisabled)
	}

	if ops := fix.opsOf("dep-0", 1); len(ops) != 0 {
		t.Errorf("the refused node saw %v, want nothing, not even prepare", ops)
	}

	if first.Status != InstanceStatusFailed || paused.Instances[1].Status != InstanceStatusPending {
		t.Errorf("instances are %s and %s, want failed and pending", first.Status, paused.Instances[1].Status)
	}

	fix.assertNoCommandText()
}

func TestNodeGateRefusesBeforeExecWhenRootTurnsOffAfterPrepare(t *testing.T) {
	fix := newTaskFixture(t, 2, 1, withRoot())
	fix.onOp = func(deployment string, index int, op string) {
		if deployment == "dep-0" && op == opRunPrepare {
			fix.setRoot(false)
		}
	}

	fix.queue()

	paused := fix.waitFor("the task to pause", func(task UpgradeTask) bool { return task.Status == TaskStatusPaused })

	node := paused.Instances[0].Command.VMs[0]
	if node.Phase != CommandPhaseSettled || node.Status != CommandStatusFailed {
		t.Fatalf("the node is %s/%s, want settled/failed", node.Phase, node.Status)
	}

	if !strings.Contains(node.Error, "403") || !strings.Contains(node.Error, apierr.CodeRootDisabled) {
		t.Errorf("the node's error is %q, want 403 %s", node.Error, apierr.CodeRootDisabled)
	}

	if node.StartedAt != nil {
		t.Error("the node records a start, though nothing was sent to run")
	}

	want := []string{opSudoCheck, opRunPrepare, opRunCleanup}
	if ops := fix.opsOf("dep-0", 0); !slices.Equal(ops, want) {
		t.Errorf("the node saw %v, want %v, so cmd.sh does not stay on the VM", ops, want)
	}

	fix.assertNoCommandText()
}

func TestResumeIsRefusedWhenTheAuthorizerRefusesTheStoredTask(t *testing.T) {
	fix := newTaskFixture(t, 2, 1, withRoot())
	fix.manager.mu.Lock()
	fix.task.Status = TaskStatusPaused
	fix.task.Paused = true
	fix.manager.mu.Unlock()
	fix.setRoot(false)

	err := fix.manager.ResumeTask(context.Background(), fix.task.ID)

	var refusal *apierr.Error
	if !errors.As(err, &refusal) || refusal.Status != http.StatusForbidden || refusal.Code != apierr.CodeRootDisabled {
		t.Fatalf("resume = %v, want 403 %s", err, apierr.CodeRootDisabled)
	}

	if snap := fix.snapshot(); snap.Status != TaskStatusPaused || !snap.Paused {
		t.Errorf("the refused resume left the task %s, paused=%v, want paused", snap.Status, snap.Paused)
	}
}

func TestResumeListsTheDeploymentAgainBeforeItsFirstNode(t *testing.T) {
	fix := newTaskFixture(t, 2, 1)
	fix.exits["dep-0/0"] = 1
	fix.queue()

	fix.waitFor("the task to pause", func(task UpgradeTask) bool { return task.Status == TaskStatusPaused })

	fix.mu.Lock()
	fix.listed["dep-1"][0].CID = "cid-replaced-during-the-pause"
	fix.mu.Unlock()

	listed := fix.director.listed()

	err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	done := fix.waitFor("the task to settle", func(task UpgradeTask) bool { return task.Instances[1].Status == InstanceStatusFailed })

	if fix.director.listed() <= listed {
		t.Error("the resumed task drove its node without listing the deployment again")
	}

	node := done.Instances[1].Command.VMs[0]
	if node.Status != CommandStatusFailed || !strings.Contains(node.Reason, "changed since the review") {
		t.Errorf("the node is %s with reason %q, want failed because the VM changed", node.Status, node.Reason)
	}

	if fix.sentTo("dep-1") {
		t.Error("a VM that changed during the pause was sent something")
	}
}

func TestResumeSettlesAStrandedNodeThroughCleanupOnly(t *testing.T) {
	fix := newTaskFixture(t, 1, 2)

	started := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	node := &fix.task.Instances[0].Command.VMs[0]
	node.Phase = CommandPhaseSent
	node.StartedAt = &started

	fix.manager.mu.Lock()
	fix.task.Status = TaskStatusPaused
	fix.task.Paused = true
	fix.task.Instances[0].Status = InstanceStatusRunning
	fix.manager.mu.Unlock()

	err := fix.manager.ResumeTask(context.Background(), fix.task.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}

	done := fix.waitFor("the task to finish", func(task UpgradeTask) bool {
		return task.Status == TaskStatusCompleted || task.Status == TaskStatusFailed
	})

	if ops := fix.opsOf("dep-0", 0); !slices.Equal(ops, []string{opSudoCheck, opRunCleanup}) && !slices.Equal(ops, []string{opRunCleanup}) {
		t.Errorf("the stranded node saw %v, want cleanup only, with no prepare and no exec", ops)
	}

	if slices.Contains(fix.opsOf("dep-0", 0), opRunExec) || slices.Contains(fix.opsOf("dep-0", 0), opRunPrepare) {
		t.Errorf("the stranded node was driven again: %v", fix.opsOf("dep-0", 0))
	}

	inst := done.Instances[0]
	if inst.Command.VMs[0].Phase != CommandPhaseSettled || inst.Command.VMs[0].Status == CommandStatusSucceeded {
		t.Errorf("the stranded node is %s/%s, want settled and not a success", inst.Command.VMs[0].Phase, inst.Command.VMs[0].Status)
	}

	if ops := fix.opsOf("dep-0", 1); !slices.Equal(ops, []string{opSudoCheck, opRunPrepare, opRunExec, opRunCleanup}) {
		t.Errorf("the pending node saw %v, want the command's four operations", ops)
	}

	if inst.Status != InstanceStatusFailed {
		t.Errorf("the instance is %s, want failed, since one node's outcome is not a success", inst.Status)
	}

	fix.assertNoCommandText()
}

func TestStagedNodeStaysHeldAcrossAPause(t *testing.T) {
	fix := newTaskFixture(t, 1, 1)

	fix.manager.mu.Lock()
	fix.task.Instances[0].Command.VMs[0].Phase = CommandPhaseStaged
	fix.task.Instances[0].Status = InstanceStatusRunning
	fix.task.Status = TaskStatusPaused
	fix.task.Paused = true

	held := fix.manager.shouldHoldLocked(fix.task, &fix.task.Instances[0])
	_, reserved := fix.manager.reservations[fix.task.ID][fix.task.Instances[0].InstanceID]

	other := &UpgradeTask{ID: "other", Kind: TaskKindUpgrade, Status: TaskStatusPending, Instances: []InstanceUpgrade{pendingInstance("inst-0", "dep-0")}}
	err := fix.manager.reserveNewLocked(other)
	fix.manager.mu.Unlock()

	if !held || !reserved {
		t.Errorf("a paused task's staged node is held=%v reserved=%v, want both", held, reserved)
	}

	if err == nil || !strings.Contains(err.Error(), "command") {
		t.Errorf("another task reserved the instance of a paused staged node, or the refusal does not name the command: %v", err)
	}
}

func TestCommandTaskFailureInTheLastInstanceFinishesTheTaskFailed(t *testing.T) {
	fix := newTaskFixture(t, 1, 2)
	fix.exits["dep-0/0"] = 2
	fix.queue()

	done := fix.waitFor("the task to finish", func(task UpgradeTask) bool { return task.Status == TaskStatusFailed })

	if done.Paused || done.Instances[0].Status != InstanceStatusFailed {
		t.Errorf("paused=%v, instance %s, want a finished task with a failed instance", done.Paused, done.Instances[0].Status)
	}

	if got := []CommandPhase{CommandPhaseSettled, CommandPhaseSkipped}; !slices.Equal(phases(done.Instances[0]), got) {
		t.Errorf("phases = %v, want %v", phases(done.Instances[0]), got)
	}

	fix.assertNoCommandText()
}
