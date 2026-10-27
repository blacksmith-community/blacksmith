package upgrade

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/ops/scripts"
)

// The outcomes of a node's command, as the node's Status holds them.
const (
	// CommandStatusSucceeded means the command exited with status 0.
	CommandStatusSucceeded = "succeeded"
	// CommandStatusFailed means the command, the wrapper, or the
	// connection failed, and Reason says which.
	CommandStatusFailed = "failed"
	// CommandStatusTimedOut means the command ran past its timeout.
	CommandStatusTimedOut = "timed_out"
	// CommandStatusRefused means the wrapper refused to run the command,
	// so nothing ran.
	CommandStatusRefused = "refused"
)

// The operations of run.sh.
const (
	opRunPrepare = "prepare"
	opRunExec    = "exec"
	opRunCleanup = "cleanup"
)

// The exit codes of exec that the wrapper and timeout(1) give meaning to.
const (
	exitWrapperLaunch = 125
	exitTimedOut      = 124
	exitKilled        = 137
)

// commandErrorMax bounds the error text a node's record keeps.
const commandErrorMax = 200

// identity is the VM the node's output is kept under and the route reads
// from, which is the matched one when the node has it and the reviewed one
// otherwise.
func (v *CommandVM) identity() CommandVMIdentity {
	if v.Matched.Group != "" {
		return v.Matched
	}

	return v.Reviewed
}

// errNoCommandRunner is what a command node fails with when the broker
// cannot open SSH sessions.
var errNoCommandRunner = errors.New("blacksmith has no SSH runner for commands")

// newCommandSessions returns the sessions one deployment's command nodes
// share, one for each instance group.
func (m *Manager) newCommandSessions(deployment string) *patchSessions {
	return &patchSessions{open: m.certDeps().CommandRunner, deployment: deployment, log: m.logger, missing: errNoCommandRunner, byGroup: make(map[string]PatchSession)}
}

// commandOutcome is how one node ended.
type commandOutcome struct {
	status string
	reason string
	err    error
	exit   *int
	result *ssh.Result
	killed bool
	path   string
	ended  time.Time
	pause  bool
	// strand means cleanup could not finish on a node of a cancelled task,
	// so the node is left as it is and the instance stays held.
	strand bool
	// skip means the node never started, because the task was cancelled
	// first, so the node is skipped with the reason.
	skip bool
}

// reasonCancelledBeforeStart is the reason a node that had not reached
// the VM when the task was cancelled gives.
const reasonCancelledBeforeStart = "The task was cancelled, so the command was not sent."

// commandRun is one node's command being run.
type commandRun struct {
	m        *Manager
	sessions *patchSessions
	task     *UpgradeTask
	inst     *InstanceUpgrade
	vm       int

	deployment string
	spec       CommandTaskSpec
	identity   CommandVMIdentity
	indices    []int

	sess PatchSession
	host int

	// script is what exec and cleanup take on stdin, and prepareInput is
	// the script followed by the command, as scripts.RunInput builds it.
	// Both are built before the first save.
	script       []byte
	prepareInput []byte

	// cancelled is set once the node's context ended because the task was
	// cancelled.
	cancelled bool
}

// errCommandNodeNotPending is what runCommandNode returns for a node that
// has already been staged, sent, settled, or skipped, or that another worker
// has claimed. Such a node is never driven again, because a node left in
// phase sent may have run its command.
var errCommandNodeNotPending = errors.New("the command can only start on a pending node, so nothing was sent")

// errCommandCancelled is the cause a node's context ends with when the task
// is cancelled, which tells a cancel from a deadline or a stop of the
// manager.
var errCommandCancelled = errors.New("the task was cancelled")

// errCommandVMChanged is what runCommandNode returns when the director no
// longer lists the VM the operator reviewed. The node is already failed
// and nothing was sent to it, and the caller stops the deployment.
var errCommandVMChanged = errors.New("the VM changed since the review")

// wasCancelled reports whether the context ended because the task was
// cancelled.
func wasCancelled(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), errCommandCancelled)
}

// commandNodeContext derives the context one node runs on from the task's.
// The context ends when the task is cancelled, which a close of the cancel
// channel says, so every node in flight is cancelled together. A value sent
// on the channel is a pause or a resume, and the watcher reads the task to
// tell a cancel from those. The function it returns stops the node's
// context and waits for the watcher to end, so no goroutine outlives its
// node.
func (m *Manager) commandNodeContext(ctx context.Context, task *UpgradeTask, cancelCh <-chan struct{}) (context.Context, func()) {
	nodeCtx, cancel := context.WithCancelCause(ctx)
	watcherDone := make(chan struct{})

	go m.watchNodeCancel(nodeCtx, cancel, task, cancelCh, watcherDone)

	return nodeCtx, func() {
		cancel(nil)
		<-watcherDone
	}
}

// watchNodeCancel ends the node's context with errCommandCancelled when
// the task is cancelled, and returns when the node's context ends. It
// closes done on the way out. It is the goroutine's own entry frame, so a
// dump of the goroutines names it.
func (m *Manager) watchNodeCancel(nodeCtx context.Context, cancel context.CancelCauseFunc, task *UpgradeTask, cancelCh <-chan struct{}, done chan<- struct{}) {
	defer close(done)

	for {
		select {
		case _, open := <-cancelCh:
			if !open || m.taskCancelled(task) {
				cancel(errCommandCancelled)

				return
			}
		case <-nodeCtx.Done():
			return
		}
	}
}

// runCommandNode runs the task's command on one node of the instance and
// settles the node. The node is saved in phase staged before prepare is
// sent and in phase sent before exec is sent, each as a confirmed save,
// so a crash never sends a command twice. It returns true when the task
// stopped, which happens when a save can't be confirmed or when processes
// of the command outlive KILL. Every other outcome, including a failed
// command, is recorded on the node and left to the caller. Before anything
// reaches the node, the deployment is listed again and the node is bound
// to the VM the review showed. A node whose VM changed is failed, nothing
// is sent, and runCommandNode returns errCommandVMChanged, so the caller
// stops the deployment. A listing that fails fails the node like any other
// failure and returns no error, and the caller treats it as a failed node.
// The listing is never kept between calls. The command text
// never reaches a log line or an error. A node that is not pending, or that
// another worker has claimed, is refused with an error that names why, and
// nothing is sent. The claim is atomic, so workers that race for a node
// never both drive it.
func (m *Manager) runCommandNode(ctx context.Context, sessions *patchSessions, task *UpgradeTask, inst *InstanceUpgrade, vm int) (bool, error) {
	err := m.claimCommandNode(inst, vm)
	if err != nil {
		return false, err
	}

	defer m.releaseCommandNode(inst, vm)

	return m.driveCommandNode(ctx, sessions, task, inst, vm)
}

// claimCommandNode takes a pending node for the caller. The check and the
// claim happen under one hold of the write lock, so of any number of
// workers that reach the same node, exactly one gets it and the others are
// refused with an error that names why.
func (m *Manager) claimCommandNode(inst *InstanceUpgrade, vm int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	node := &inst.Command.VMs[vm]

	switch {
	case node.Phase != CommandPhasePending:
		return fmt.Errorf("%w: the node is in phase %s", errCommandNodeNotPending, node.Phase)
	case node.claimed:
		return fmt.Errorf("%w: another worker is driving the node", errCommandNodeNotPending)
	}

	node.claimed = true

	return nil
}

// releaseCommandNode gives a claim back when the worker is done with the
// node. A node that moved on is refused a second driver by its phase, and a
// node that is pending again, which a save that could not be confirmed
// leaves it, can be claimed by the next run.
func (m *Manager) releaseCommandNode(inst *InstanceUpgrade, vm int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	inst.Command.VMs[vm].claimed = false
}

// commandNodePhase returns the phase the node is in now.
func (m *Manager) commandNodePhase(inst *InstanceUpgrade, vm int) CommandPhase {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return inst.Command.VMs[vm].Phase
}

// driveCommandNode runs a pending node. The order is the listing, the SSH
// setup, the match of the session's host to the node, the sudo check, and
// then the saves and the operations, so nothing reaches the VM before the
// node is bound to it.
func (m *Manager) driveCommandNode(ctx context.Context, sessions *patchSessions, task *UpgradeTask, inst *InstanceUpgrade, vm int) (bool, error) {
	run := m.newCommandRun(sessions, task, inst, vm)

	err := run.authorize(ctx)
	if err != nil {
		return run.settle(ctx, commandOutcome{status: CommandStatusFailed, reason: "Running this command is no longer allowed, so nothing was sent.", err: err}), nil
	}

	if run.cancelledNow(ctx) {
		return run.settle(ctx, commandOutcome{skip: true, reason: reasonCancelledBeforeStart}), nil
	}

	err = run.validate()
	if err != nil {
		return run.settle(ctx, commandOutcome{status: CommandStatusFailed, reason: "The task's settings are not valid for the wrapper, so nothing was sent.", err: err}), nil
	}

	err = run.buildInputs()
	if err != nil {
		return run.settle(ctx, commandOutcome{status: CommandStatusFailed, reason: "Blacksmith could not build the script for the node, so nothing was sent.", err: err}), nil
	}

	live, outcome, bindErr := run.relist(ctx)
	if outcome != nil {
		return run.settle(ctx, *outcome), bindErr
	}

	err = run.connect(ctx, live)
	if err != nil {
		return run.settle(ctx, commandOutcome{status: CommandStatusFailed, reason: "Blacksmith could not open an SSH session on the node, so nothing was sent.", err: err}), nil
	}

	outcome = run.checkSudo(ctx)
	if outcome != nil {
		return run.settle(ctx, *outcome), nil
	}

	outcome, stop := run.stage(ctx)
	if outcome != nil {
		return run.settle(ctx, *outcome), nil
	}

	if stop {
		return true, nil
	}

	return run.execute(ctx), nil
}

func (m *Manager) newCommandRun(sessions *patchSessions, task *UpgradeTask, inst *InstanceUpgrade, vm int) *commandRun {
	m.mu.RLock()
	defer m.mu.RUnlock()

	run := &commandRun{m: m, sessions: sessions, task: task, inst: inst, vm: vm, deployment: inst.Command.Deployment}

	if task.Command != nil {
		run.spec = *task.Command
	}

	node := &inst.Command.VMs[vm]
	run.identity = node.identity()

	for i := range inst.Command.VMs {
		other := &inst.Command.VMs[i]
		if other.Phase == CommandPhaseSkipped {
			continue
		}

		if other.identity().Group == run.identity.Group {
			run.indices = append(run.indices, other.identity().Index)
		}
	}

	return run
}

// cancelledNow reports whether the task was cancelled, whether or not the
// cancel has reached the node's context yet.
func (r *commandRun) cancelledNow(ctx context.Context) bool {
	return wasCancelled(ctx) || r.m.taskCancelled(r.task)
}

// authorize asks the authorizer whether the stored task may still run.
func (r *commandRun) authorize(ctx context.Context) error {
	return r.m.authorizeCommand(ctx, r.spec) //nolint:wrapcheck // the authorizer answers with the API error
}

// validate checks every value the three operations take, before anything is
// sent.
func (r *commandRun) validate() error {
	user, seconds, digest := r.spec.RunAs, strconv.Itoa(r.spec.TimeoutSeconds), r.spec.CommandDigest

	_, _, err := scripts.OpRunPrepare(r.task.ID, user)
	if err != nil {
		return fmt.Errorf("preparing: %w", err)
	}

	_, _, err = scripts.OpRunExec(r.task.ID, user, seconds, digest)
	if err != nil {
		return fmt.Errorf("executing: %w", err)
	}

	_, _, err = scripts.OpRunCleanup(r.task.ID)
	if err != nil {
		return fmt.Errorf("cleaning up: %w", err)
	}

	return nil
}

// buildInputs builds every stdin the three operations take.
func (r *commandRun) buildInputs() error {
	build := scripts.RunScript
	if r.m.commandScript != nil {
		build = r.m.commandScript
	}

	script, err := build()
	if err != nil {
		return fmt.Errorf("building the script: %w", err)
	}

	r.script = script
	r.prepareInput = append(bytes.Clone(script), r.spec.Command...)

	return nil
}

// relist lists the deployment's VMs and finds the node's reviewed tuple
// among them. It returns the VM as the director lists it now. When the
// node is not there, or is there with another CID or ID, the outcome is
// the failure to settle and the error is errCommandVMChanged. When the
// listing fails, the outcome is the failure and the error is nil. The
// reviewed tuple is never trusted in place of a listing.
func (r *commandRun) relist(ctx context.Context) (CommandVMIdentity, *commandOutcome, error) {
	vms, err := directorCall(ctx, func(c context.Context) ([]bosh.VM, error) {
		return r.m.director.GetDeploymentVMs(c, r.deployment)
	})
	if err != nil {
		return CommandVMIdentity{}, &commandOutcome{status: CommandStatusFailed, reason: "The director did not list the deployment's VMs, so nothing was sent.", err: err}, nil
	}

	reviewed := r.identity

	for _, vm := range vms {
		if vm.Job == reviewed.Group && vm.Index == reviewed.Index && vm.ID == reviewed.ID && vm.CID == reviewed.CID {
			return CommandVMIdentity{
				Group: vm.Job, Index: vm.Index, ID: vm.ID, CID: vm.CID,
				AZ: vm.AZ, IPs: slices.Clone(vm.IPs), State: vm.State, JobState: vm.JobState,
			}, nil, nil
		}
	}

	reason := fmt.Sprintf("VM %s/%d changed since the review, so nothing ran on it", reviewed.Group, reviewed.Index)

	return CommandVMIdentity{}, &commandOutcome{status: CommandStatusFailed, reason: reason}, errCommandVMChanged
}

// connect opens the group's session, finds the one host that carries the
// node's identity, and records the matched tuple on the node. The host is
// matched by group and by an IndexOrID equal to the node's instance ID or
// index, never by position, and a session with no host or with more than
// one for the node is refused.
func (r *commandRun) connect(ctx context.Context, live CommandVMIdentity) error {
	sess, err := r.sessions.group(ctx, r.identity.Group, r.indices)
	if err != nil {
		return err
	}

	host, err := r.matchHost(sess)
	if err != nil {
		return err
	}

	r.m.mu.Lock()
	r.inst.Command.VMs[r.vm].Matched = live.clone()
	r.m.mu.Unlock()

	r.sess, r.host, r.identity = sess, host, live

	return nil
}

// matchHost finds the node's host in the session.
func (r *commandRun) matchHost(sess PatchSession) (int, error) {
	found := -1

	for index, host := range sess.VMs() {
		named := host.IndexOrID == strconv.Itoa(r.identity.Index) || (r.identity.ID != "" && host.IndexOrID == r.identity.ID)
		if host.Job != r.identity.Group || !named {
			continue
		}

		if found >= 0 {
			return 0, fmt.Errorf("the SSH session has more than one host for %s-%d", r.identity.Group, r.identity.Index)
		}

		found = index
	}

	if found < 0 {
		return 0, fmt.Errorf("the SSH session has no host for %s-%d", r.identity.Group, r.identity.Index)
	}

	return found, nil
}

// checkSudo runs the sudo check, which a VM without passwordless sudo
// fails before any script is sent.
func (r *commandRun) checkSudo(ctx context.Context) *commandOutcome {
	sudoCtx, cancel := context.WithTimeout(ctx, patchCommandTimeout)
	defer cancel()

	result, err := r.sess.Run(sudoCtx, r.host, opSudoCheck, nil, nil)

	switch {
	case err != nil:
		return &commandOutcome{status: CommandStatusFailed, reason: "The sudo check did not finish on the node, so nothing was sent.", err: err}
	case result.ExitCode != 0:
		return &commandOutcome{status: CommandStatusFailed, reason: "The sudo check failed because the node has no passwordless sudo, so nothing was sent."}
	}

	return nil
}

// stage saves the node as staged, sends prepare, and checks what the node
// says it staged against the task.
func (r *commandRun) stage(ctx context.Context) (*commandOutcome, bool) {
	// The cancel can land while the node is bound to its VM, and nothing
	// of the command should reach the VM after it.
	if r.cancelledNow(ctx) {
		return &commandOutcome{skip: true, reason: reasonCancelledBeforeStart}, false
	}

	staged := r.m.certNow()

	err := r.advance(ctx, func(node *CommandVM) {
		node.Phase = CommandPhaseStaged
		node.StagedAt = &staged
	})
	if err != nil {
		r.m.pauseUnconfirmed(r.task, r.inst, "stage the command on the node")

		return nil, true
	}

	prepCtx, cancel := context.WithTimeout(ctx, patchCommandTimeout)
	defer cancel()

	result, err := r.sess.Run(prepCtx, r.host, opRunPrepare, []string{r.task.ID, r.spec.RunAs}, bytes.NewReader(r.prepareInput))

	switch {
	case err != nil:
		return r.cleanedUp(ctx, "The prepare step did not finish on the node, so the command did not run.", err), false
	case result.ExitCode != 0:
		return &commandOutcome{status: CommandStatusFailed, reason: "The prepare step failed on the node with status " + strconv.Itoa(result.ExitCode) + ", so the command did not run."}, false
	}

	err = r.checkStaged(result.Stdout)
	if err != nil {
		return r.cleanedUp(ctx, "The node staged something other than the command Blacksmith sent, so it did not run.", err), false
	}

	return nil, false
}

// checkStaged requires exactly one staged record that agrees with the task.
func (r *commandRun) checkStaged(out []byte) error {
	records, err := scripts.ParseRecords(out, scripts.RunKinds())
	if err != nil {
		return fmt.Errorf("reading the staged record: %w", err)
	}

	if len(records) != 1 || records[0].Kind != scripts.RecordStaged {
		return fmt.Errorf("%w: prepare did not print exactly one staged record", scripts.ErrMalformedOutput)
	}

	fields := records[0].Fields

	if fields["bytes"] != strconv.Itoa(len(r.spec.Command)) {
		return fmt.Errorf("%w: the staged size differs from the command's", scripts.ErrRefused)
	}

	if fields["sha256"] != r.spec.CommandDigest {
		return fmt.Errorf("%w: the staged digest differs from the task's", scripts.ErrRefused)
	}

	return nil
}

// cleanedUp removes what a failed prepare may have left and returns the
// failure to settle. A cleanup that fails adds to the error.
func (r *commandRun) cleanedUp(ctx context.Context, reason string, cause error) *commandOutcome {
	_, _, err := r.cleanup(ctx)
	if err != nil {
		// A cancelled task never leaves the command's text on a node it
		// could not clean, so the node keeps its phase and the instance
		// stays held until a resume cleans it.
		if wasCancelled(ctx) {
			return &commandOutcome{strand: true}
		}

		cause = errors.Join(cause, err)
	}

	return &commandOutcome{status: CommandStatusFailed, reason: reason, err: cause}
}

// execute saves the node as sent, sends exec, cleans up, stores the
// output, and settles the node.
func (r *commandRun) execute(ctx context.Context) bool {
	// The switch can flip during the prepare round trip and its save, so
	// this is the last gate before anything runs. A refusal removes what
	// prepare left on the node.
	err := r.authorize(ctx)
	if wasCancelled(ctx) {
		return r.settle(ctx, *r.cleanedUp(ctx, "The task was cancelled before the command was sent, so it did not run.", errCommandCancelled))
	}

	if err != nil {
		return r.settle(ctx, *r.cleanedUp(ctx, "Running this command is no longer allowed, so it did not run.", err))
	}

	started := r.m.certNow()

	err = r.advance(ctx, func(node *CommandVM) {
		node.Phase = CommandPhaseSent
		node.StartedAt = &started
	})
	if err != nil {
		r.m.pauseUnconfirmed(r.task, r.inst, "send the command to the node")

		return true
	}

	timeout := time.Duration(r.spec.TimeoutSeconds) * time.Second

	execCtx, cancel := context.WithTimeout(ctx, timeout+patchCleanupTimeout)
	args := []string{r.task.ID, r.spec.RunAs, strconv.Itoa(r.spec.TimeoutSeconds), r.spec.CommandDigest}
	execStart := r.m.certNow()
	result, execErr := r.sess.Run(execCtx, r.host, opRunExec, args, bytes.NewReader(r.script))
	deadlineHit := errors.Is(execCtx.Err(), context.DeadlineExceeded)

	cancel()

	ended := r.m.certNow()
	r.cancelled = wasCancelled(ctx) && !deadlineHit
	record, survived, cleanErr := r.cleanup(ctx)

	// Cleanup is how a cancelled node is known to have ended. When it
	// can't tell, the command may still be running, so the node stays sent,
	// the instance stays held, and a resume sends cleanup only. What the
	// node wrote before the cancel is kept on the node meanwhile.
	if r.cancelled && cleanErr != nil && !survived {
		path, _ := r.storeOutput(ctx, result)

		return r.settle(ctx, commandOutcome{strand: true, result: &result, path: path})
	}

	outcome := r.classify(result, execErr, deadlineHit, record, cleanErr, ended.Sub(execStart) >= timeout)
	outcome.ended = ended
	outcome.result = &result
	outcome.killed = record.Killed == "yes"
	outcome.pause = survived

	path, storeErr := r.storeOutput(ctx, result)
	if storeErr != nil {
		outcome.status = CommandStatusFailed
		outcome.reason = "Blacksmith could not store the node's output. " + outcome.reason
		outcome.err = errors.Join(outcome.err, storeErr)
	}

	outcome.path = path

	return r.settle(ctx, outcome)
}

// storeOutput keeps what the node printed, on a context that outlives the
// node's own.
func (r *commandRun) storeOutput(ctx context.Context, result ssh.Result) (string, error) {
	storeCtx, storeCancel := storeContext(ctx)
	defer storeCancel()

	return r.m.storeCommandOutput(storeCtx, r.task.ID, r.inst.InstanceID, r.identity, result) //nolint:wrapcheck // the caller records the store's own error
}

// classify turns what the node answered into an outcome.
func (r *commandRun) classify(result ssh.Result, execErr error, deadlineHit bool, record scripts.Cleanup, cleanErr error, pastTimeout bool) commandOutcome {
	if record.Killed == "survived" {
		return commandOutcome{
			status: CommandStatusFailed,
			reason: "Processes of the command survived KILL on the node, so the task is paused and nothing was sent again.",
		}
	}

	if cleanErr != nil {
		return commandOutcome{status: CommandStatusFailed, reason: "The cleanup step did not report cleanly on the node.", err: cleanErr}
	}

	if execErr != nil && r.cancelled {
		return r.classifyCancelled(execErr, record)
	}

	if execErr != nil {
		if deadlineHit || errors.Is(execErr, context.DeadlineExceeded) {
			return commandOutcome{status: CommandStatusTimedOut, reason: "The node did not finish within the timeout and the grace period.", err: execErr}
		}

		return commandOutcome{status: CommandStatusFailed, reason: "The connection to the node ended before the command's result was known.", err: execErr}
	}

	code := result.ExitCode
	exit := &code
	started := record.Started == "yes"

	switch {
	case code == 0:
		return commandOutcome{status: CommandStatusSucceeded, exit: exit}
	case code == scripts.ExitRefused && !started:
		return commandOutcome{status: CommandStatusRefused, reason: "The wrapper refused to run the command on the node, so nothing ran."}
	case code == exitWrapperLaunch && started:
		return commandOutcome{status: CommandStatusFailed, reason: "The wrapper failed before the command started."}
	case !started:
		return commandOutcome{status: CommandStatusFailed, reason: "The command did not start on the node, and the wrapper exited with status " + strconv.Itoa(code) + "."}
	case code == exitTimedOut, code == exitKilled && pastTimeout:
		return commandOutcome{status: CommandStatusTimedOut, reason: "The command ran past its timeout of " + strconv.Itoa(r.spec.TimeoutSeconds) + " seconds.", exit: exit}
	}

	return commandOutcome{status: CommandStatusFailed, reason: "The command exited with status " + strconv.Itoa(code) + ".", exit: exit}
}

// classifyCancelled turns what cleanup found on a node whose exec ended with
// the task's cancel into an outcome. No exit status reached Blacksmith, so
// the record is the only witness. A command that had exited already counts
// as the exit the record holds, and one cleanup had to end is failed and
// killed.
func (r *commandRun) classifyCancelled(execErr error, record scripts.Cleanup) commandOutcome {
	if record.Started == "yes" && record.Killed != "yes" {
		code, err := strconv.Atoi(record.Exit)
		if err == nil && code == 0 {
			return commandOutcome{status: CommandStatusSucceeded, exit: &code}
		}

		if err == nil {
			return commandOutcome{status: CommandStatusFailed, reason: "The task was cancelled after the command exited with status " + strconv.Itoa(code) + ".", exit: &code}
		}
	}

	switch {
	case record.Killed == "yes":
		return commandOutcome{status: CommandStatusFailed, reason: "The task was cancelled, so cleanup ended the command's process group on the node.", err: execErr}
	case record.Started == "no":
		return commandOutcome{status: CommandStatusFailed, reason: "The task was cancelled before the command started on the node.", err: execErr}
	}

	return commandOutcome{status: CommandStatusFailed, reason: "The task was cancelled, and what the command did on the node is not known.", err: execErr}
}

// cleanup sends cleanup on a context of its own, so a node whose context
// ended still has its directory removed, and reads the record. The bool is
// true when processes outlived KILL.
func (r *commandRun) cleanup(ctx context.Context) (scripts.Cleanup, bool, error) {
	cleanCtx, cancel := context.WithTimeout(context.Background(), patchCommandTimeout)
	defer cancel()

	result, err := r.sess.Run(cleanCtx, r.host, opRunCleanup, []string{r.task.ID}, bytes.NewReader(r.script))
	if err != nil {
		return scripts.Cleanup{}, false, fmt.Errorf("cleanup did not finish: %w", err)
	}

	// run.sh prints the record before it exits non-zero when it can't
	// remove the directory, so the record is read whatever the status is.
	record, parseErr := scripts.ParseCleanup(result.Stdout)
	if parseErr != nil {
		record = scripts.Cleanup{}
		err = fmt.Errorf("cleanup: %w", parseErr)
	}

	if result.ExitCode != 0 {
		err = errors.Join(err, fmt.Errorf("cleanup exited with status %d", result.ExitCode))
	}

	return record, record.Killed == "survived", err
}

// advance changes the node and saves the task with a confirmed save, and
// then queues the audit record of the move. When the save can't be
// confirmed the node goes back to what it was, so memory agrees with the
// last confirmed record, and nothing is audited.
func (r *commandRun) advance(ctx context.Context, change func(*CommandVM)) error {
	r.m.mu.Lock()
	before := r.inst.Command.clone().VMs[r.vm]
	change(&r.inst.Command.VMs[r.vm])
	r.m.mu.Unlock()

	err := r.m.persistCertConfirmed(ctx, r.task)

	r.m.mu.Lock()
	defer r.m.mu.Unlock()

	if err != nil {
		r.inst.Command.VMs[r.vm] = before

		return err
	}

	// The move is in the confirmed record, so its audit record is queued
	// now, and by the one worker that made it.
	r.m.auditCommandNodeLocked(r.task, r.inst, r.vm)

	return nil
}

// settle records how the node ended. It returns true when the task
// stopped, because the outcome asks for it or because the save of the
// settled node could not be confirmed.
func (r *commandRun) settle(ctx context.Context, outcome commandOutcome) bool {
	if outcome.strand {
		return r.strand(ctx, outcome)
	}

	if outcome.skip {
		err := r.advance(ctx, func(node *CommandVM) {
			node.Phase = CommandPhaseSkipped
			node.Reason = outcome.reason
		})
		if err != nil {
			r.m.pauseUnconfirmed(r.task, r.inst, "record that the node was skipped")

			return true
		}

		return false
	}

	ended := outcome.ended
	if ended.IsZero() {
		ended = r.m.certNow()
	}

	err := r.advance(ctx, func(node *CommandVM) {
		node.Phase = CommandPhaseSettled
		node.Status = outcome.status
		node.Reason = outcome.reason
		node.Error = ""
		node.ExitCode = outcome.exit
		node.EndedAt = &ended
		node.Killed = outcome.killed
		node.Survived = outcome.pause

		if outcome.err != nil {
			node.Error = commandErrorText(outcome.err)
		}

		// An outcome with no result, as a resume's cleanup gives, keeps
		// the output an earlier run stored.
		if outcome.result != nil {
			recordOutput(node, outcome)
		}

		if outcome.pause {
			// A finished or cancelled task stays as it is, as
			// pauseUnconfirmed leaves it.
			switch r.task.Status {
			case TaskStatusPending, TaskStatusRunning, TaskStatusPaused:
				r.task.Status = TaskStatusPaused
				r.task.Paused = true
			case TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled:
			}

			r.inst.Error = outcome.reason
		}
	})
	if err != nil {
		r.m.pauseUnconfirmed(r.task, r.inst, "record how the command ended on the node")

		return true
	}

	return outcome.pause
}

// recordOutput records on the node what it printed and where that is kept.
func recordOutput(node *CommandVM, outcome commandOutcome) {
	res := outcome.result

	node.OutputPath = outcome.path
	node.StdoutKept = len(res.Stdout)
	node.StdoutReceived = int(res.StdoutReceived)
	node.StderrKept = len(res.Stderr)
	node.StderrReceived = int(res.StderrReceived)
	node.StdoutTruncated = res.StdoutTruncated
	node.StderrTruncated = res.StderrTruncated
}

// strand leaves the node in the phase it is in, because cleanup could not
// remove what the node holds, and keeps the instance held. The task stays
// cancelled and pauses, so a resume is what cleans the node. The output the
// node wrote is recorded on it, so the resume does not lose it. It returns
// true, because the task stopped.
func (r *commandRun) strand(ctx context.Context, outcome commandOutcome) bool {
	if outcome.result != nil {
		r.m.mu.Lock()
		recordOutput(&r.inst.Command.VMs[r.vm], outcome)
		r.m.mu.Unlock()
	}

	r.m.holdAndSave(ctx, r.task, r.inst)

	return true
}

// holdAndSave holds the cancelled task for a resume, as holdCancelledCommand
// does, and saves the hold, so a restart finds the task waiting.
func (m *Manager) holdAndSave(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade) {
	m.holdCancelledCommand(task, inst)

	err := m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.logger.Error("Command task %s is cancelled and holds instance %s, but the hold could not be confirmed in Vault", task.ID, inst.InstanceID)

		// The hold stands in memory. The text says that Vault may not
		// have it, so the operator knows a restart can lose it.
		m.mu.Lock()
		if commandInstanceHeldLocked(inst) {
			inst.Error += " The hold is not confirmed in Vault, so a restart may not find the task waiting."
		}
		m.mu.Unlock()
	}
}

// commandErrorText makes an error fit a node's record, as one short line
// of printable text.
func commandErrorText(err error) string {
	var refusal *apierr.Error
	if errors.As(err, &refusal) {
		err = fmt.Errorf("%d %s: %s", refusal.Status, refusal.Code, refusal.Message)
	}

	text := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}

		return r
	}, strings.ToValidUTF8(err.Error(), "?"))

	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) > commandErrorMax {
		runes = runes[:commandErrorMax]
	}

	return string(runes)
}

// storeContext lets the output of a node whose context ended still reach
// Vault.
func storeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), persistTaskTimeout)
}

// authorizeCommand asks the authorizer whether a stored command task may
// go on. The authorizer is read under the manager's lock, so a gate never
// races Configure.
func (m *Manager) authorizeCommand(ctx context.Context, spec CommandTaskSpec) error {
	m.mu.RLock()
	authorizer := m.deps.Authorizer
	m.mu.RUnlock()

	if authorizer == nil {
		authorizer = NewConfigAuthorizer(nil)
	}

	return authorizer.AuthorizeStored(ctx, spec) //nolint:wrapcheck // the authorizer answers with the API error
}

// runsCommandLocked reports whether the manager runs this task as a
// command task. Only a configured manager does, and only while the
// authorizer still allows the stored task, so a task whose switches were
// turned off while it waited is let go the way an unrunnable certificate
// task is. The caller holds mu, which the authorizer is read under without
// taking it again, so an authorizer must never block or take a manager
// lock.
func (m *Manager) runsCommandLocked(task *UpgradeTask) bool {
	if task.KindOrDefault() != TaskKindCommand || task.Command == nil || !m.configured {
		return false
	}

	return m.commandRefusalLocked(task) == nil
}

// commandRefusalLocked asks the authorizer about the stored task and
// returns its refusal. The caller holds mu, as runsCommandLocked describes.
func (m *Manager) commandRefusalLocked(task *UpgradeTask) error {
	authorizer := m.deps.Authorizer
	if authorizer == nil {
		authorizer = NewConfigAuthorizer(nil)
	}

	return authorizer.AuthorizeStored(m.ctx, *task.Command) //nolint:wrapcheck // the authorizer answers with the API error
}

// commandRefusedMessage starts what a command task shows on its instance
// when the authorizer refuses it, whether at a restart or at the queue.
const commandRefusedMessage = "Running this command is no longer allowed, so the task paused. "

// pauseRefusedCommandLocked pauses a loaded or queued command task the authorizer
// refuses, and says why on its first instance that has not ended. It
// reports whether it paused the task. A manager that is not configured
// asks nothing, because it can't tell yet. The task keeps its holds, since
// a paused command task holds every instance that has not settled. The
// caller holds mu.
func (m *Manager) pauseRefusedCommandLocked(task *UpgradeTask) bool {
	if task.KindOrDefault() != TaskKindCommand || task.Command == nil || !m.configured {
		return false
	}

	err := m.commandRefusalLocked(task)
	if err == nil {
		return false
	}

	task.Status = TaskStatusPaused
	task.Paused = true

	for i := range task.Instances {
		inst := &task.Instances[i]
		if inst.Status.settled() {
			continue
		}

		inst.Error = commandRefusedMessage + commandErrorText(err)

		break
	}

	m.logger.Info("Command task %s paused, because the authorizer refuses it: %v", task.ID, err)

	return true
}

// prepareLoadedCommandLocked settles in memory what a restart left of a
// loaded command task, and reports whether it changed the task, which the
// caller then saves. Nothing here sends anything. A node left staged or
// sent stays as it is, and settles through cleanup alone when the task
// resumes, since its command may have run. A halt that was never saved is
// found again here. An instance that holds a node that failed, survived,
// or is waiting to be settled has its pending nodes skipped, so none of
// them is ever driven, and a node whose processes survived keeps an
// unfinished task paused, as does an authorizer that refuses the task. A
// task that sent nothing loads paused, as commandNeverStartedLocked
// describes. A cancelled task ends each running instance that has no node
// left to clean, and keeps the instances that have one held. The caller
// holds mu.
func (m *Manager) prepareLoadedCommandLocked(task *UpgradeTask) bool {
	changed := false

	switch task.Status {
	case TaskStatusCompleted, TaskStatusFailed:
		return false
	case TaskStatusCancelled:
		for i := range task.Instances {
			inst := &task.Instances[i]
			if inst.Command != nil && m.endCancelledCommandInstanceLocked(task, inst) {
				changed = true
			}
		}

		return changed
	case TaskStatusPending, TaskStatusRunning, TaskStatusPaused:
	}

	survived := false

	for i := range task.Instances {
		inst := &task.Instances[i]
		if inst.Command == nil {
			continue
		}

		for vm, node := range inst.Command.VMs {
			if commandNodeHeld(node) {
				m.noteStrandCauseLocked(task, inst, vm, strandedByRestart)
			}
		}

		if commandInstanceHaltedLocked(inst) && skipPendingNodesLocked(inst, "Blacksmith restarted after a node of the deployment did not succeed, so the command was not sent.") {
			changed = true
		}

		survived = survived || slices.ContainsFunc(inst.Command.VMs, func(vm CommandVM) bool { return vm.Survived })
	}

	if survived && !task.Paused {
		task.Status = TaskStatusPaused
		task.Paused = true
		changed = true
	}

	// A task that sent nothing may have had its creation reported as
	// failed, so it never runs on its own, as a certificate task of the
	// kind never does.
	if commandNeverStartedLocked(task) {
		task.Status = TaskStatusPaused
		task.Paused = true

		for i := range task.Instances {
			task.Instances[i].Error = commandNeverStartedMessage
		}

		m.logger.Info("Command task %s sent nothing before Blacksmith restarted, so it loads paused until an operator resumes it", task.ID)

		changed = true
	}

	// A queued or running task the authorizer refuses after the restart is
	// not queued. It is paused, with the refusal on the task, so a resume
	// after the switch is turned back on runs what is left of it.
	if !task.Paused && m.pauseRefusedCommandLocked(task) {
		changed = true
	}

	return changed
}

// commandNeverStartedMessage is what a loaded command task that sent
// nothing shows, since its creation may have been reported as failed.
const commandNeverStartedMessage = "Blacksmith restarted before this task ran anything, and it can't tell whether " +
	"the task's creation was reported as failed. Check that the command is still wanted, then resume it or cancel the task."

// commandNeverStartedLocked reports whether a loaded command task sent
// nothing yet: it is confirmed and unfinished, every instance is pending,
// and every node is pending or was skipped at create because the preview
// did not target it. The caller holds mu.
func commandNeverStartedLocked(task *UpgradeTask) bool {
	if task.KindOrDefault() != TaskKindCommand || !task.CreationConfirmed || task.Paused {
		return false
	}

	if task.Status != TaskStatusPending && task.Status != TaskStatusRunning {
		return false
	}

	for i := range task.Instances {
		inst := &task.Instances[i]
		if inst.Status != InstanceStatusPending || inst.Command == nil {
			return false
		}

		if slices.ContainsFunc(inst.Command.VMs, func(vm CommandVM) bool { return vm.Phase != CommandPhasePending && vm.Phase != CommandPhaseSkipped }) {
			return false
		}
	}

	return true
}

// clearCommandLoadMessagesLocked clears the never-started message from the
// instances still pending, and the refusal from any instance that has not
// ended, once an operator resumes the task, since the resume answers both.
// A later failure then shows its own reason. The caller holds mu.
func clearCommandLoadMessagesLocked(task *UpgradeTask) {
	for i := range task.Instances {
		inst := &task.Instances[i]
		if inst.Status == InstanceStatusPending && inst.Error == commandNeverStartedMessage {
			inst.Error = ""
		}

		if !inst.Status.settled() && strings.HasPrefix(inst.Error, commandRefusedMessage) {
			inst.Error = ""
		}
	}
}

// commandInstanceHaltedLocked reports whether a node of the instance
// stopped the deployment, which it did when it failed or its processes
// survived, or when its outcome is not known because a restart found it
// staged or sent. The caller holds mu.
func commandInstanceHaltedLocked(inst *InstanceUpgrade) bool {
	return slices.ContainsFunc(inst.Command.VMs, func(vm CommandVM) bool {
		return commandNodeHeld(vm) || vm.Survived || (vm.Phase == CommandPhaseSettled && vm.Status != CommandStatusSucceeded)
	})
}

// commandResumeGate runs the gate and the authorizer for the resume of a
// command task, and leaves any other task to ResumeTask. The gate comes
// first, as the check of the second credential did before it moved here.
// A cancelled task that waits for a resume only has its nodes cleaned, which
// sends no command, so it needs neither, as the cancel needed neither.
// Cancelled is final, so a task found waiting here never turns into one
// that runs.
func (m *Manager) commandResumeGate(ctx context.Context, taskID string, gate func(context.Context) error) error {
	m.mu.RLock()
	task, exists := m.tasks[taskID]
	cleansOnly := exists && task.KindOrDefault() == TaskKindCommand && waitsCancelledLocked(task)
	needsGate := exists && task.KindOrDefault() == TaskKindCommand && !cleansOnly
	m.mu.RUnlock()

	if !needsGate {
		return nil
	}

	if gate != nil {
		err := gate(ctx)
		if err != nil {
			return err
		}
	}

	return m.commandResumeAllowed(ctx, taskID)
}

// commandResumeAllowed refuses the resume of a command task the authorizer
// no longer allows, and leaves it paused. Any other task is left to
// ResumeTask.
func (m *Manager) commandResumeAllowed(ctx context.Context, taskID string) error {
	m.mu.RLock()
	task, exists := m.tasks[taskID]

	var spec *CommandTaskSpec
	if exists && task.KindOrDefault() == TaskKindCommand {
		spec = task.Command.clone()
	}
	m.mu.RUnlock()

	if spec == nil {
		return nil
	}

	err := m.authorizeCommand(ctx, *spec)
	if err != nil {
		m.logger.Info("Command task %s stays paused, because the authorizer refuses it: %v", taskID, err)
	}

	return err
}

// processCommandInstance runs the command on the nodes of one instance and
// reports whether the task goes on to its next instance. The nodes start in
// order, and a bounded pool of workers runs them: one at a time unless the
// task asked for parallel, and otherwise at most max in flight at once. A
// node that ends in anything but success stops the deployment. No new node
// starts, the nodes already in flight run to their own end and are
// recorded, the rest are skipped, the instance fails, and the task pauses
// when an instance is still pending, so the operator decides whether the
// rest get the command. The pool drains before the instance ends, so the
// next instance never starts while a node of this one is in flight. A
// failed instance is settled, so a resume never runs it again. A node left
// staged or sent by an earlier run is settled through cleanup only, and is
// never driven again.
func (m *Manager) processCommandInstance(task *UpgradeTask, inst *InstanceUpgrade, cancelCh <-chan struct{}) bool {
	goOn := m.runCommandInstance(task, inst, cancelCh)
	if !goOn && m.taskCancelled(task) {
		persistCtx := context.WithoutCancel(m.ctx)

		m.endCancelledCommandInstance(persistCtx, task, inst)
		m.skipPendingInstances(persistCtx, task)
	}

	return goOn
}

// runCommandInstance is the body of processCommandInstance.
func (m *Manager) runCommandInstance(task *UpgradeTask, inst *InstanceUpgrade, cancelCh <-chan struct{}) bool {
	ctx := m.ctx
	persistCtx := context.WithoutCancel(ctx)

	goOn, _ := m.waitWhilePaused(persistCtx, task, cancelCh)
	if !goOn {
		return false
	}

	if m.refusedBeforeStart(ctx, task, inst) {
		return false
	}

	m.mu.Lock()
	settled := inst.Status.settled()

	if !settled && inst.Status == InstanceStatusPending {
		started := m.certNowLocked()
		inst.Status = InstanceStatusRunning
		inst.StartedAt = &started

		// A refusal recorded before the task started is over once the
		// instance runs, so a later failure shows its own error.
		inst.Error = ""
	}
	m.mu.Unlock()

	if settled {
		return true
	}

	sessions := m.newCommandSessions(inst.Command.Deployment)
	defer sessions.close()

	// A task a worker stopped is paused, and it is waited for here rather
	// than handed back, so a resume that lands while the other workers are
	// still in flight finds a processor that goes on with the instance. The
	// halt is kept across passes, so a deployment halted before a stop is
	// still failed when it ends.
	halted := false

	for {
		// A node an earlier run left staged or sent is settled before any
		// pending node is driven. It settles as failed, which halts the
		// deployment like any failed node, so the pass that found one
		// drives nothing more. A node that failed in an earlier pass halts
		// it too.
		stopped, halt, survived := m.settleStrandedNodes(ctx, sessions, task, inst)
		stranded := stopped || halt

		if !stranded {
			pool := newCommandPool(m.commandWorkers(task))
			if !m.feedCommandNodes(ctx, persistCtx, pool, sessions, task, inst, cancelCh) {
				return false
			}

			stopped, halt, survived = pool.outcome()
		}

		// A cancel that the nodes in flight have answered ends the instance
		// as cancelled, whatever those nodes recorded.
		if !stopped && m.taskCancelled(task) {
			return false
		}

		halted = halted || halt
		if halted {
			m.skipRemainingNodes(inst)
		}

		// A task that stopped is paused already and its instance stays as
		// it is, unless the node that stopped it is also the one that halted
		// the deployment, which only processes that outlived KILL do. The
		// instance is ended first, and then the task stays paused rather
		// than finishing, whatever instance it is in.
		if !stopped || survived {
			ended := m.endCommandInstance(persistCtx, task, inst, halted)

			return ended && (!survived || !m.commandTaskPaused(task))
		}

		// A stranded node that can't be settled yet leaves the task for its
		// next resume, which settles the node again from the start.
		if stranded || m.commandTaskOver(task) || ctx.Err() != nil {
			return false
		}

		goOn, _ := m.waitWhilePaused(persistCtx, task, cancelCh)
		if !goOn {
			return false
		}
	}
}

// feedCommandNodes starts the pending nodes of the instance in order on the
// pool and waits for the workers to end. A slot is taken before the checks,
// so a pause, a stop, or a failure that happens while the pool is full is
// seen before the next node starts. A pause that arrives while workers are
// in flight lets them end first, so a failure among them is processed
// before the pause is honoured. The pool is drained on every way out, so no
// worker is left running on a session that closes. It reports whether the
// instance goes on to be ended.
func (m *Manager) feedCommandNodes(ctx, persistCtx context.Context, pool *commandPool, sessions *patchSessions, task *UpgradeTask, inst *InstanceUpgrade, cancelCh <-chan struct{}) bool {
	defer pool.wait()

	for vm := range inst.Command.VMs {
		if !pool.acquire() {
			break
		}

		if m.commandTaskPaused(task) {
			pool.release()
			pool.wait()

			if pool.stopping() {
				break
			}

			if !pool.acquire() {
				break
			}
		}

		goOn, _ := m.waitWhilePaused(persistCtx, task, cancelCh)
		if !goOn {
			pool.release()

			return false
		}

		if ctx.Err() != nil {
			pool.release()

			m.logger.Info("Manager stopping during command task %s, so instance %s stays as it is", task.ID, inst.InstanceID)

			return false
		}

		if pool.stopping() {
			pool.release()

			break
		}

		if !m.commandNodeWaits(inst, vm) {
			pool.release()

			continue
		}

		pool.start(func() (bool, bool) {
			nodeCtx, stop := m.commandNodeContext(ctx, task, cancelCh)
			defer stop()

			return m.driveNodeInLoop(nodeCtx, sessions, task, inst, vm)
		}, func(recovered any) {
			m.pauseWorkerPanic(task, inst, vm, recovered)
		})
	}

	pool.wait()

	return true
}

// commandTaskPaused reports whether the task is paused.
func (m *Manager) commandTaskPaused(task *UpgradeTask) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return task.Paused
}

// commandTaskOver reports whether the task has ended or been cancelled.
func (m *Manager) commandTaskOver(task *UpgradeTask) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	switch task.Status {
	case TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled:
		return true
	default:
		return false
	}
}

// pauseWorkerPanic pauses the task after a worker panicked, because what
// happened on the node is not known. Only the type of the panic is logged,
// since its value may carry what the node was running. The node keeps its
// last confirmed phase, so a resume settles a staged or sent node through
// cleanup and drives a pending one.
func (m *Manager) pauseWorkerPanic(task *UpgradeTask, inst *InstanceUpgrade, vm int, recovered any) {
	message := "Blacksmith stopped a node of the deployment because of an internal error, so the task paused. Resume once the node has been looked at."

	m.mu.Lock()

	if task.Status == TaskStatusPending || task.Status == TaskStatusRunning || task.Status == TaskStatusPaused {
		task.Status = TaskStatusPaused
		task.Paused = true
	}

	inst.Error = message

	if commandNodeHeld(inst.Command.VMs[vm]) {
		m.noteStrandCauseLocked(task, inst, vm, strandedByPanic)
	}

	m.mu.Unlock()

	m.logger.Error("Command task %s paused, because node %d of instance %s panicked (%T)", task.ID, vm, inst.InstanceID, recovered)
}

// commandWorkers returns how many nodes of one deployment may run at once,
// which is one unless the task asked for parallel.
func (m *Manager) commandWorkers(task *UpgradeTask) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if task.Command == nil || !task.Command.Parallel || task.Command.MaxInFlight < 1 {
		return 1
	}

	return task.Command.MaxInFlight
}

// commandPool runs the nodes of one deployment on at most a fixed number of
// workers, and keeps what the workers report. A worker reports and gives its
// slot back in one step, so the feeder that takes the slot next sees the
// report.
type commandPool struct {
	group sync.WaitGroup

	mu   sync.Mutex
	cond *sync.Cond

	workers, active int
	// stoppedTask is set when a worker could not run its node and the task
	// stopped, and halted when the deployment must stop. survived is set
	// when the same node did both, which processes that outlived KILL do.
	stoppedTask, halted, survived bool
}

func newCommandPool(workers int) *commandPool {
	p := &commandPool{workers: workers}
	p.cond = sync.NewCond(&p.mu)

	return p
}

// acquire waits for a free slot and reports whether it got one. It gets
// none once a worker has stopped the task or halted the deployment.
func (p *commandPool) acquire() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	for p.active >= p.workers && !p.stoppedTask && !p.halted {
		p.cond.Wait()
	}

	if p.stoppedTask || p.halted {
		return false
	}

	p.active++

	return true
}

// release gives back a slot that no worker took.
func (p *commandPool) release() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.active--
	p.cond.Broadcast()
}

// stopping reports whether no further node may start.
func (p *commandPool) stopping() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.stoppedTask || p.halted
}

// start runs the node on a worker that holds the slot the caller acquired.
// A node that panics stops the task without ending its instance, because
// what happened on the node is not known, and onPanic is told what the
// panic carried.
func (p *commandPool) start(drive func() (bool, bool), onPanic func(recovered any)) {
	p.group.Add(1)

	go func() {
		defer p.group.Done()

		stopped, halt := true, false

		defer func() {
			if recovered := recover(); recovered != nil {
				onPanic(recovered)
			}

			p.finish(stopped, halt)
		}()

		stopped, halt = drive()
	}()
}

// finish keeps what one worker found and gives its slot back.
func (p *commandPool) finish(stopped, halt bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.stoppedTask = p.stoppedTask || stopped
	p.halted = p.halted || halt
	p.survived = p.survived || (stopped && halt)
	p.active--
	p.cond.Broadcast()
}

// wait blocks until every worker has returned.
func (p *commandPool) wait() { p.group.Wait() }

// outcome returns what the workers found.
func (p *commandPool) outcome() (stopped, halted, survived bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.stoppedTask, p.halted, p.survived
}

// driveNodeInLoop runs one pending node and reports whether the task
// stopped and whether the deployment must stop. The deployment stops when
// the node settled in anything but success, and also when the node could
// not be run for a reason other than a VM that changed, which no node may
// pass over without a word. A node whose processes outlived KILL has both
// stopped the task and settled failed, so the deployment stops and the
// caller ends the instance before it honours the stop.
func (m *Manager) driveNodeInLoop(ctx context.Context, sessions *patchSessions, task *UpgradeTask, inst *InstanceUpgrade, vm int) (bool, bool) {
	stopped, err := m.runCommandNode(ctx, sessions, task, inst, vm)
	if err != nil && !errors.Is(err, errCommandVMChanged) {
		m.logger.Error("Command task %s did not run node %d of instance %s, so the deployment stops: %v", task.ID, vm, inst.InstanceID, err)

		return stopped, true
	}

	return stopped, m.commandNodeFailed(inst, vm)
}

// refusedBeforeStart asks the authorizer about a task no node of which has
// started, which is the case for a root task that waited for its slot while
// root runs were turned off. A refusal pauses the task with the refusal on
// its first instance, fails no instance, and leaves every instance pending,
// so a resume after the switch is turned back on runs the whole task. The
// task's slot is released when the processing returns. A task that has
// started is not asked here, because the gate of each node refuses its next
// node instead. It reports whether the task paused.
func (m *Manager) refusedBeforeStart(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade) bool {
	m.mu.RLock()

	started := slices.ContainsFunc(task.Instances, func(other InstanceUpgrade) bool { return other.Status != InstanceStatusPending })
	spec := task.Command.clone()

	m.mu.RUnlock()

	if started || spec == nil {
		return false
	}

	err := m.authorizeCommand(ctx, *spec)
	if err == nil {
		return false
	}

	m.mu.Lock()

	if task.Status == TaskStatusRunning {
		task.Status = TaskStatusPaused
		task.Paused = true
	}

	inst.Error = "Running this command is no longer allowed, so the task paused before any node started. " + commandErrorText(err)
	m.releaseUnheldLocked(task)
	m.mu.Unlock()

	m.logger.Info("Command task %s paused before it started, because the authorizer refuses it: %v", task.ID, err)

	err = m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.pauseUnconfirmed(task, inst, "record the refusal")
	}

	return true
}

// commandNodeWaits reports whether the node is still pending, and skips it
// first when its VM has no CID, which is no failure.
func (m *Manager) commandNodeWaits(inst *InstanceUpgrade, vm int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	node := &inst.Command.VMs[vm]
	if node.Phase != CommandPhasePending {
		return false
	}

	if node.Reviewed.CID == "" {
		node.Phase = CommandPhaseSkipped
		node.Reason = "The VM has no CID, so there is nothing to run on."

		return false
	}

	return true
}

// commandNodeFailed reports whether the node settled in anything but
// success.
func (m *Manager) commandNodeFailed(inst *InstanceUpgrade, vm int) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	node := &inst.Command.VMs[vm]

	return node.Phase == CommandPhaseSettled && node.Status != CommandStatusSucceeded
}

// skipRemainingNodes marks every pending node of the instance skipped,
// except one a worker has claimed.
func (m *Manager) skipRemainingNodes(inst *InstanceUpgrade) {
	m.mu.Lock()
	defer m.mu.Unlock()

	skipPendingNodesLocked(inst, "An earlier node of the deployment did not succeed, so the command was not sent.")
}

// skipPendingNodesLocked marks every pending node of the instance skipped
// with the reason, except one a worker has claimed, and reports whether it
// marked any. The caller holds mu.
func skipPendingNodesLocked(inst *InstanceUpgrade, reason string) bool {
	skipped := false

	for i := range inst.Command.VMs {
		node := &inst.Command.VMs[i]
		if node.Phase != CommandPhasePending || node.claimed {
			continue
		}

		node.Phase = CommandPhaseSkipped
		node.Reason = reason
		skipped = true
	}

	return skipped
}

// endCommandInstance settles the instance from its nodes, counts it, and
// saves it. The instance fails when any node ended in anything but success,
// and a failure pauses the task when an instance is still pending. A save
// that can't be confirmed pauses the task instead. A halted deployment is
// one the loop stopped, which fails the instance even when no node recorded
// a failure. It reports whether the task goes on to its next instance.
func (m *Manager) endCommandInstance(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade, halted bool) bool {
	m.mu.Lock()

	failed := false

	for i := range inst.Command.VMs {
		node := &inst.Command.VMs[i]
		if node.Phase == CommandPhaseSettled && node.Status != CommandStatusSucceeded {
			failed = true

			if inst.Error == "" {
				inst.Error = node.Reason
			}
		}
	}

	// A deployment halted by a node that could not be run fails even when
	// no node recorded a failure.
	if halted && !failed {
		failed = true
		inst.Error = "Blacksmith could not run a node of the deployment, so the deployment stopped."
	}

	ended := m.certNowLocked()
	inst.CompletedAt = &ended

	if failed {
		inst.Status = InstanceStatusFailed
		task.FailedCount++
	} else {
		inst.Status = InstanceStatusSuccess
		inst.Error = ""
		task.CompletedCount++
	}

	pause := failed && task.Status == TaskStatusRunning && !task.Paused && anyPendingLocked(task)
	if pause {
		task.Paused = true
		task.Status = TaskStatusPaused
	}

	m.releaseUnheldLocked(task)
	m.mu.Unlock()

	if pause {
		m.logger.Info("Instance %s of command task %s did not succeed, so the task pauses before its remaining instances", inst.InstanceID, task.ID)
	}

	err := m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.pauseUnconfirmed(task, inst, "record how the instance ended")

		return false
	}

	return true
}

// settleStrandedNodes settles each node an earlier run left staged or sent.
// A resume never drives such a node again, because the command may have run
// on it, so cleanup reports what it left and the node is recorded as an
// outcome Blacksmith did not see. It reports whether the task stopped,
// which it does when a node can't be reached or the save can't be
// confirmed, and the node then stays as it was for the next resume, so
// the pass ends there. It also reports whether the deployment halted,
// which a node that settled as a failure does, in this pass or an earlier
// one. A node whose processes outlived KILL settles and stops the task,
// and the pass goes on to clean the other stranded nodes, so survived is
// set only when the task stopped and every stranded node settled. A task
// that stopped on a node still staged or sent is never survived, and its
// instance must stay open for the next resume.
func (m *Manager) settleStrandedNodes(ctx context.Context, sessions *patchSessions, task *UpgradeTask, inst *InstanceUpgrade) (stopped, halted, survived bool) {
	halted = m.commandInstanceFailed(inst)

	for vm := range inst.Command.VMs {
		phase := m.commandNodePhase(inst, vm)
		if phase != CommandPhaseStaged && phase != CommandPhaseSent {
			continue
		}

		stop := m.settleStrandedNode(ctx, sessions, task, inst, vm)
		failed := m.commandNodeFailed(inst, vm)
		halted = halted || failed

		if stop && !failed {
			return true, halted, false
		}

		stopped = stopped || stop
	}

	return stopped, halted, stopped
}

// commandInstanceFailed reports whether a node of the instance settled in
// anything but success.
func (m *Manager) commandInstanceFailed(inst *InstanceUpgrade) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return slices.ContainsFunc(inst.Command.VMs, func(node CommandVM) bool {
		return node.Phase == CommandPhaseSettled && node.Status != CommandStatusSucceeded
	})
}

// settleStrandedNode runs cleanup on one stranded node and records it.
func (m *Manager) settleStrandedNode(ctx context.Context, sessions *patchSessions, task *UpgradeTask, inst *InstanceUpgrade, vm int) bool {
	run := m.newCommandRun(sessions, task, inst, vm)

	err := m.reachStranded(ctx, run)
	if err != nil {
		m.pauseStranded(task, inst, "Blacksmith could not reach the node to clean up after an earlier run, so it is still waiting for a resume.", err)

		return true
	}

	record, _, err := run.cleanup(ctx)
	if err != nil {
		m.pauseStranded(task, inst, "The cleanup of a node an earlier run left behind did not report cleanly, so it is still waiting for a resume.", err)

		return true
	}

	why := m.strandCause(task, inst, vm)

	outcome := commandOutcome{
		status: CommandStatusFailed,
		reason: "Blacksmith lost track of this node's command " + why + ", so its outcome is not known. Cleanup found started=" + record.Started + " killed=" + record.Killed + ".",
		pause:  record.Killed == "survived",
	}

	if code, convErr := strconv.Atoi(record.Exit); convErr == nil {
		outcome.exit = &code
	}

	stopped := run.settle(ctx, outcome)
	if m.commandNodeFailed(inst, vm) {
		m.clearStrandCause(task, inst, vm)
	}

	return stopped
}

// Why a node was stranded, as the reason of the node says it.
const (
	strandedByPause   = "when it paused"
	strandedByCancel  = "when the task was cancelled"
	strandedByRestart = "when Blacksmith restarted"
	strandedByPanic   = "when a worker for the node hit an internal error"
)

// strandKey names one node of a task for strandCauses.
func strandKey(task *UpgradeTask, inst *InstanceUpgrade, vm int) string {
	return task.ID + "/" + inst.InstanceID + "/" + strconv.Itoa(vm)
}

// noteStrandCauseLocked records why a node is stranded, when a pause is not
// the reason. The caller holds mu.
func (m *Manager) noteStrandCauseLocked(task *UpgradeTask, inst *InstanceUpgrade, vm int, why string) {
	m.strandCauses[strandKey(task, inst, vm)] = why
}

// strandCause says why a stranded node was lost. A cancel is what the
// operator last asked for, so it comes first, then a restart or a worker
// panic, and otherwise the node was stranded by a pause.
func (m *Manager) strandCause(task *UpgradeTask, inst *InstanceUpgrade, vm int) string {
	if m.taskCancelled(task) {
		return strandedByCancel
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if why, found := m.strandCauses[strandKey(task, inst, vm)]; found {
		return why
	}

	return strandedByPause
}

// clearStrandCause forgets the cause of a node that settled.
func (m *Manager) clearStrandCause(task *UpgradeTask, inst *InstanceUpgrade, vm int) {
	m.mu.Lock()
	delete(m.strandCauses, strandKey(task, inst, vm))
	m.mu.Unlock()
}

// reachStranded builds the cleanup input and opens the session on the
// node, after the deployment is listed again and the node's VM is found.
// A VM that changed or is gone is a reason not to reach the node, like any
// other.
func (m *Manager) reachStranded(ctx context.Context, run *commandRun) error {
	err := run.buildInputs()
	if err != nil {
		return err
	}

	live, outcome, err := run.relist(ctx)
	if outcome != nil {
		return errors.Join(err, outcome.err, errors.New(outcome.reason))
	}

	return run.connect(ctx, live)
}

// pauseStranded pauses the task because a stranded node can't be settled
// yet, and says why on the instance.
func (m *Manager) pauseStranded(task *UpgradeTask, inst *InstanceUpgrade, message string, cause error) {
	m.mu.Lock()

	if task.Status == TaskStatusPending || task.Status == TaskStatusRunning || task.Status == TaskStatusPaused {
		task.Status = TaskStatusPaused
		task.Paused = true
	}

	inst.Error = message
	m.mu.Unlock()

	m.logger.Error("Command task %s paused: %s %s", task.ID, message, commandErrorText(cause))

	// A cancelled task stays cancelled and waits for a resume, held.
	m.holdCancelledCommand(task, inst)
}

// commandNodeHeld reports whether the node holds something of ours on its
// VM. After prepare the command text sits in cmd.sh on the VM even though
// nothing has run, so a staged node holds as a sent one does.
func commandNodeHeld(vm CommandVM) bool {
	return vm.Phase == CommandPhaseStaged || vm.Phase == CommandPhaseSent
}

// commandInstanceHeldLocked reports whether any node of the instance holds
// something of ours on its VM. The caller holds mu.
func commandInstanceHeldLocked(inst *InstanceUpgrade) bool {
	return inst.Command != nil && slices.ContainsFunc(inst.Command.VMs, commandNodeHeld)
}

// commandHeldLocked reports whether any instance of the task has a node
// that holds something of ours on its VM. The caller holds mu.
func commandHeldLocked(task *UpgradeTask) bool {
	for i := range task.Instances {
		if commandInstanceHeldLocked(&task.Instances[i]) {
			return true
		}
	}

	return false
}

// holdCancelledCommand makes a cancelled task wait for a resume because a
// node of the instance could not be cleaned, and says so on the instance.
// The task stays cancelled, so the reservation of the instance stays too.
// It changes nothing for a task that is not cancelled or an instance with
// no node left to clean.
func (m *Manager) holdCancelledCommand(task *UpgradeTask, inst *InstanceUpgrade) {
	m.mu.Lock()
	defer m.mu.Unlock()

	holdCancelledCommandLocked(task, inst)
}

// holdCancelledCommandLocked is holdCancelledCommand for a caller that holds
// mu.
func holdCancelledCommandLocked(task *UpgradeTask, inst *InstanceUpgrade) {
	if task.Status != TaskStatusCancelled || inst.Command == nil {
		return
	}

	for i := range inst.Command.VMs {
		node := &inst.Command.VMs[i]
		if !commandNodeHeld(*node) {
			continue
		}

		identity := node.identity()
		task.Paused = true
		inst.Error = fmt.Sprintf("The task was cancelled, but cleanup did not finish, so instance %s is held pending cleanup on %s/%d. Resume the task once the node can be reached.", inst.InstanceID, identity.Group, identity.Index)

		return
	}
}

// rewordHeldCommandsLocked gives the instances of a command task that
// paused and was then cancelled the text of a hold, in place of the text of
// the pause. A task that is not paused is left to its processor. The caller
// holds mu.
func rewordHeldCommandsLocked(task *UpgradeTask) {
	if task.KindOrDefault() != TaskKindCommand || !task.Paused {
		return
	}

	for i := range task.Instances {
		holdCancelledCommandLocked(task, &task.Instances[i])
	}
}

// endCancelledCommandInstance ends an instance of a cancelled task once no
// node of it holds anything on a VM. Its pending nodes are skipped, the
// instance is cancelled and counted, and its reservation is released. An
// instance with a node still to clean stays as it is, held, for the resume.
func (m *Manager) endCancelledCommandInstance(ctx context.Context, task *UpgradeTask, inst *InstanceUpgrade) {
	m.mu.Lock()
	ended := m.endCancelledCommandInstanceLocked(task, inst)
	m.mu.Unlock()

	if !ended {
		return
	}

	err := m.persistCertConfirmed(ctx, task)
	if err != nil {
		m.logger.Error("Command task %s ended instance %s as cancelled, but the record could not be confirmed in Vault", task.ID, inst.InstanceID)
	}
}

// endCancelledCommandInstanceLocked is endCancelledCommandInstance up to
// its save, and reports whether it ended the instance. The caller holds mu.
func (m *Manager) endCancelledCommandInstanceLocked(task *UpgradeTask, inst *InstanceUpgrade) bool {
	if task.Status != TaskStatusCancelled || inst.Status != InstanceStatusRunning || inst.Command == nil || commandInstanceHeldLocked(inst) {
		return false
	}

	skipPendingNodesLocked(inst, reasonCancelledBeforeStart)

	ended := m.certNowLocked()
	inst.Status = InstanceStatusCancelled
	inst.CompletedAt = &ended
	inst.Error = ""
	task.CancelledCount++

	m.releaseUnheldLocked(task)

	return true
}

// resumeCancelledCommandLocked starts the cleanup of each running instance
// of a cancelled command task that has a node holding something on its VM,
// when nothing processes or settles the task and it does not wait for a
// resume. Each instance counts as a settle of the task, so nothing else ends
// it meanwhile, and the function it returns starts the cleanups, which the
// caller runs once it lets go of mu. The caller holds mu.
func (m *Manager) resumeCancelledCommandLocked(task *UpgradeTask) func() {
	if task.Status != TaskStatusCancelled || task.Paused || !m.configured || m.director == nil ||
		m.owned[task.ID] || m.settling[task.ID] > 0 || m.ctx.Err() != nil {
		return func() {}
	}

	var clean []*InstanceUpgrade

	for i := range task.Instances {
		inst := &task.Instances[i]
		if inst.Status == InstanceStatusRunning && commandInstanceHeldLocked(inst) {
			clean = append(clean, inst)
		}
	}

	if len(clean) == 0 {
		return func() {}
	}

	m.settling[task.ID] += len(clean)
	m.wg.Add(len(clean))

	return func() {
		for _, inst := range clean {
			go m.cleanCancelledCommand(task, inst)
		}
	}
}

// cleanCancelledCommand settles the nodes of one instance of a cancelled
// task through cleanup only, as a resume settles a stranded node, and never
// sends the command again. It ends the settle that
// resumeCancelledCommandLocked counted. A node it can't clean leaves the
// task waiting for a resume with the instance held.
func (m *Manager) cleanCancelledCommand(task *UpgradeTask, inst *InstanceUpgrade) {
	defer m.wg.Done()
	defer m.endSettling(loadedDeploy{task: task, instance: inst})

	defer func() {
		if r := recover(); r != nil {
			m.logger.Error("PANIC while command task %s cleaned up instance %s (%T)", task.ID, inst.InstanceID, r)
			m.holdAndSave(context.WithoutCancel(m.ctx), task, inst)
		}
	}()

	m.logger.Info("Command task %s is cancelled, so it cleans up the nodes of instance %s that still hold its command", task.ID, inst.InstanceID)

	sessions := m.newCommandSessions(inst.Command.Deployment)
	defer sessions.close()

	persistCtx := context.WithoutCancel(m.ctx)

	// A settle that stopped the task leaves a node held only when it could
	// not clean it. Processes that outlived KILL settle the node, so
	// nothing is left to wait for, and the instance ends as it does when
	// every node is clean.
	stopped, _, _ := m.settleStrandedNodes(m.ctx, sessions, task, inst)
	if stopped {
		m.holdAndSave(persistCtx, task, inst)
	}

	m.endCancelledCommandInstance(persistCtx, task, inst)
}
