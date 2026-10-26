package upgrade

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

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

// newCommandSessions returns the sessions one deployment's command nodes
// share, one for each instance group.
func (m *Manager) newCommandSessions(deployment string) *patchSessions {
	return &patchSessions{open: m.certDeps().CommandRunner, deployment: deployment, log: m.logger, byGroup: make(map[string]PatchSession)}
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
}

// commandRun is one node's command being run.
type commandRun struct {
	m        *Manager
	sessions *patchSessions
	task     *UpgradeTask
	inst     *InstanceUpgrade
	vm       int

	spec     CommandTaskSpec
	identity CommandVMIdentity
	indices  []int

	sess PatchSession
	host int

	// script is what exec and cleanup take on stdin, and prepareInput is
	// the script followed by the command, as scripts.RunInput builds it.
	// Both are built before the first save.
	script       []byte
	prepareInput []byte
}

// errCommandNodeNotPending is what runCommandNode returns for a node that
// has already been staged, sent, settled, or skipped. Such a node is never
// driven again, because a node left in phase sent may have run its command.
var errCommandNodeNotPending = errors.New("the command can only start on a pending node, so nothing was sent")

// runCommandNode runs the task's command on one node of the instance and
// settles the node. The node is saved in phase staged before prepare is
// sent and in phase sent before exec is sent, each as a confirmed save,
// so a crash never sends a command twice. It returns true when the task
// stopped, which happens when a save can't be confirmed or when processes
// of the command outlive KILL. Every other outcome, including a failed
// command, is recorded on the node and left to the caller. The command text
// never reaches a log line or an error. A node that is not pending is
// refused with an error that names its phase, and nothing is sent.
func (m *Manager) runCommandNode(ctx context.Context, sessions *patchSessions, task *UpgradeTask, inst *InstanceUpgrade, vm int) (bool, error) {
	phase := m.commandNodePhase(inst, vm)
	if phase != CommandPhasePending {
		return false, fmt.Errorf("%w: the node is in phase %s", errCommandNodeNotPending, phase)
	}

	return m.driveCommandNode(ctx, sessions, task, inst, vm), nil
}

// commandNodePhase returns the phase the node is in now.
func (m *Manager) commandNodePhase(inst *InstanceUpgrade, vm int) CommandPhase {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return inst.Command.VMs[vm].Phase
}

// driveCommandNode runs a pending node.
func (m *Manager) driveCommandNode(ctx context.Context, sessions *patchSessions, task *UpgradeTask, inst *InstanceUpgrade, vm int) bool {
	run := m.newCommandRun(sessions, task, inst, vm)

	err := run.validate()
	if err != nil {
		return run.settle(ctx, commandOutcome{status: CommandStatusFailed, reason: "The task's settings are not valid for the wrapper, so nothing was sent.", err: err})
	}

	err = run.buildInputs()
	if err != nil {
		return run.settle(ctx, commandOutcome{status: CommandStatusFailed, reason: "Blacksmith could not build the script for the node, so nothing was sent.", err: err})
	}

	err = run.connect(ctx)
	if err != nil {
		return run.settle(ctx, commandOutcome{status: CommandStatusFailed, reason: "Blacksmith could not open an SSH session on the node, so nothing was sent.", err: err})
	}

	outcome := run.checkSudo(ctx)
	if outcome != nil {
		return run.settle(ctx, *outcome)
	}

	outcome, stop := run.stage(ctx)
	if outcome != nil {
		return run.settle(ctx, *outcome)
	}

	if stop {
		return true
	}

	return run.execute(ctx)
}

func (m *Manager) newCommandRun(sessions *patchSessions, task *UpgradeTask, inst *InstanceUpgrade, vm int) *commandRun {
	m.mu.RLock()
	defer m.mu.RUnlock()

	run := &commandRun{m: m, sessions: sessions, task: task, inst: inst, vm: vm}

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

// connect opens the group's session and finds the node's host in it.
func (r *commandRun) connect(ctx context.Context) error {
	sess, err := r.sessions.group(ctx, r.identity.Group, r.indices)
	if err != nil {
		return err
	}

	ids := []string{strconv.Itoa(r.identity.Index)}
	if r.identity.ID != "" {
		ids = []string{r.identity.ID, ids[0]}
	}

	for _, id := range ids {
		host, err := hostIndex(sess, r.identity.Group, id)
		if err == nil {
			r.sess, r.host = sess, host

			return nil
		}
	}

	return fmt.Errorf("the SSH session has no host for %s-%d", r.identity.Group, r.identity.Index)
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
	err := r.advance(ctx, func(node *CommandVM) { node.Phase = CommandPhaseStaged })
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
		cause = errors.Join(cause, err)
	}

	return &commandOutcome{status: CommandStatusFailed, reason: reason, err: cause}
}

// execute saves the node as sent, sends exec, cleans up, stores the
// output, and settles the node.
func (r *commandRun) execute(ctx context.Context) bool {
	started := r.m.certNow()

	err := r.advance(ctx, func(node *CommandVM) {
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
	record, survived, cleanErr := r.cleanup(ctx)

	outcome := r.classify(result, execErr, deadlineHit, record, cleanErr, ended.Sub(execStart) >= timeout)
	outcome.ended = ended
	outcome.result = &result
	outcome.killed = record.Killed == "yes"
	outcome.pause = survived

	storeCtx, storeCancel := storeContext(ctx)
	path, storeErr := r.m.storeCommandOutput(storeCtx, r.task.ID, r.inst.InstanceID, r.identity, result)

	storeCancel()

	if storeErr != nil {
		outcome.status = CommandStatusFailed
		outcome.reason = "Blacksmith could not store the node's output. " + outcome.reason
		outcome.err = errors.Join(outcome.err, storeErr)
	}

	outcome.path = path

	return r.settle(ctx, outcome)
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

// cleanup sends cleanup on a context of its own, so a node whose context
// ended still has its directory removed, and reads the record. The bool is
// true when processes outlived KILL.
func (r *commandRun) cleanup(ctx context.Context) (scripts.Cleanup, bool, error) {
	cleanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), patchCommandTimeout)
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

// advance changes the node and saves the task with a confirmed save. When
// the save can't be confirmed the node goes back to what it was, so memory
// agrees with the last confirmed record.
func (r *commandRun) advance(ctx context.Context, change func(*CommandVM)) error {
	r.m.mu.Lock()
	before := r.inst.Command.clone().VMs[r.vm]
	change(&r.inst.Command.VMs[r.vm])
	r.m.mu.Unlock()

	err := r.m.persistCertConfirmed(ctx, r.task)
	if err != nil {
		r.m.mu.Lock()
		r.inst.Command.VMs[r.vm] = before
		r.m.mu.Unlock()
	}

	return err
}

// settle records how the node ended. It returns true when the task
// stopped, because the outcome asks for it or because the save of the
// settled node could not be confirmed.
func (r *commandRun) settle(ctx context.Context, outcome commandOutcome) bool {
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
		node.OutputPath = outcome.path

		if outcome.err != nil {
			node.Error = commandErrorText(outcome.err)
		}

		if res := outcome.result; res != nil {
			node.StdoutKept = len(res.Stdout)
			node.StdoutReceived = int(res.StdoutReceived)
			node.StderrKept = len(res.Stderr)
			node.StderrReceived = int(res.StderrReceived)
			node.StdoutTruncated = res.StdoutTruncated
			node.StderrTruncated = res.StderrTruncated
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

// commandErrorText makes an error fit a node's record, as one short line
// of printable text.
func commandErrorText(err error) string {
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
