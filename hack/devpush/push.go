package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"blacksmith/internal/bosh/sshconn"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"golang.org/x/crypto/ssh"
)

const (
	// setupBound bounds the wait for the setup task, as the broker does.
	setupBound = 3 * time.Minute
	// settleBound bounds everything after a setup that failed or was cut
	// short, which is the wait for the setup task to end and the cleanup
	// that follows.
	settleBound = 60 * time.Second
	// lostSetupGrace is how long to wait, after a setup whose answer was
	// lost, for a request still inside the director to become a task.
	lostSetupGrace = 10 * time.Second
	// pollInterval is how often the tool reads a task.
	pollInterval = 2 * time.Second
	// dialTimeout bounds the TCP connect and the SSH handshake with the VM.
	dialTimeout = 30 * time.Second
	// tempRemoveBound bounds the removal of the temporary file after a
	// failed or interrupted copy.
	tempRemoveBound = 15 * time.Second

	// The task states the tool reads. The director lists a task that has not
	// finished under any of the first three.
	runningTaskStates = "processing,cancelling,queued"
	// taskVerboseAll is the verbosity at which the director lists SSH tasks.
	taskVerboseAll = 2
	// setupDescriptionPrefix starts the description of every SSH setup task.
	setupDescriptionPrefix = "ssh: setup"
	// taskOutputResult is the task output that holds an SSH setup's hosts.
	taskOutputResult = "result"

	// remoteBinary is where the broker's binary lives on the VM.
	remoteBinary = "/var/vcap/packages/blacksmith/bin/blacksmith"
	// monitRestart restarts the blacksmith process.
	monitRestart = "sudo /var/vcap/bosh/bin/monit restart blacksmith"
)

var (
	// errSetupRefused means the director refused the SSH setup, so it
	// created no user.
	errSetupRefused = errors.New("the director refused the SSH setup")
	// errSetupLost means the answer to the SSH setup never arrived.
	errSetupLost = errors.New("the answer to the SSH setup was lost")
	// errSetupUnusable means the SSH setup ended without a host to push to.
	errSetupUnusable = errors.New("the SSH setup did not give a host to push to")
	// errSetupNotEnded means the setup task was still running when the
	// settle bound passed, so no cleanup could be sent.
	errSetupNotEnded = errors.New("the SSH setup task had not ended, so no cleanup was sent")
	// errCleanupFailed means the throwaway user may remain on the VM.
	errCleanupFailed = errors.New("the SSH cleanup did not finish")
	// errStepFailed means a step of the copy failed.
	errStepFailed = errors.New("a push step failed")
)

// setupState is a setup the tool has sent. The tool keeps it so the cleanup
// knows whether the setup task has ended.
type setupState struct {
	user string
	// taskID is the setup task, or 0 when the answer to the setup was lost.
	taskID int
	// ended is set once the setup task is known to have ended.
	ended bool
}

// pusher pushes one binary to one instance.
type pusher struct {
	client     boshapi.Client
	deployment string
	target     boshapi.SSHTarget
	binary     string
	out        io.Writer

	// The bounds below are fields so tests can shorten them.
	sshPort        string
	pollInterval   time.Duration
	setupBound     time.Duration
	settleBound    time.Duration
	lostSetupGrace time.Duration
	dialTimeout    time.Duration
}

func newPusher(client boshapi.Client, deployment string, target boshapi.SSHTarget, binary string, out io.Writer) *pusher {
	return &pusher{
		client:         client,
		deployment:     deployment,
		target:         target,
		binary:         binary,
		out:            out,
		sshPort:        sshconn.Port,
		pollInterval:   pollInterval,
		setupBound:     setupBound,
		settleBound:    settleBound,
		lostSetupGrace: lostSetupGrace,
		dialTimeout:    dialTimeout,
	}
}

// push sets up a throwaway SSH user, copies the binary with it, restarts
// blacksmith, and removes the user. The cleanup runs however the push ends,
// once the setup task has ended.
func (p *pusher) push(ctx context.Context) error {
	credentials, err := sshconn.NewCredentials()
	if err != nil {
		return fmt.Errorf("make the throwaway SSH login: %w", err)
	}

	state, err := p.sendSetup(ctx, credentials)
	if state == nil {
		return err
	}

	var cleanupErr error

	func() {
		// The cleanup is deferred so it also runs while a panic unwinds. The
		// panic goes on once the cleanup has run.
		defer func() {
			recovered := recover()

			cleanupErr = p.finish(ctx, state)
			if recovered != nil {
				p.reportCleanupAfterPanic(state, cleanupErr)

				panic(recovered)
			}
		}()

		if err == nil {
			err = p.useSetup(ctx, credentials, state)
		}
	}()

	if cleanupErr != nil {
		return errors.Join(err, cleanupErr)
	}

	p.logf("Removed SSH user %s from %s of deployment %s", state.user, sshconn.TargetName(p.target), p.deployment)

	return err
}

// reportCleanupAfterPanic says how the cleanup that ran during a panic
// ended, because the panic leaves no error to carry it.
func (p *pusher) reportCleanupAfterPanic(state *setupState, cleanupErr error) {
	if cleanupErr != nil {
		p.logf("The push panicked, and the cleanup of SSH user %s did not finish: %v", state.user, cleanupErr)

		return
	}

	p.logf("The push panicked, and SSH user %s was removed from %s of deployment %s", state.user, sshconn.TargetName(p.target), p.deployment)
}

// useSetup waits for the setup, then copies the binary to the host it
// returned and restarts blacksmith.
func (p *pusher) useSetup(ctx context.Context, credentials *sshconn.Credentials, state *setupState) error {
	hosts, err := p.awaitSetup(ctx, state)
	if err != nil {
		return err
	}

	return p.transfer(ctx, credentials, hosts[0])
}

// sendSetup asks the director to create the user. It returns a nil state
// when the director refused, because then no user exists. It returns the
// state together with an error when the answer was lost, because the user
// may exist.
func (p *pusher) sendSetup(ctx context.Context, credentials *sshconn.Credentials) (*setupState, error) {
	instance := sshconn.TargetName(p.target)

	p.logf("Setting up SSH user %s on %s of deployment %s", credentials.User, instance, p.deployment)

	task, err := p.client.SSHClient().Setup(ctx, p.deployment,
		sshconn.SetupRequest(p.deployment, p.target, credentials.User, credentials.PublicKey))

	switch {
	case err == nil && task != nil:
		p.logf("BOSH task %d sets up SSH user %s", task.ID, credentials.User)

		return &setupState{user: credentials.User, taskID: task.ID}, nil
	case err == nil:
		return &setupState{user: credentials.User}, fmt.Errorf("%w: the director accepted the setup of SSH user %s on %s of deployment %s "+
			"but gave no task", errSetupLost, credentials.User, instance, p.deployment)
	case boshapi.IsOutcomeUnknown(err):
		return &setupState{user: credentials.User}, fmt.Errorf("%w: the request for SSH user %s on %s of deployment %s was sent, "+
			"and no answer came back, so the user may exist: %w", errSetupLost, credentials.User, instance, p.deployment, err)
	default:
		return nil, p.explainRefusal(err, instance)
	}
}

// explainRefusal says what a refused setup means and what to check.
func (p *pusher) explainRefusal(err error, instance string) error {
	var apiErr *boshapi.APIError

	switch {
	case errors.As(err, &apiErr) && apiErr.Code == boshapi.ErrorCodeDeploymentNotFound:
		return fmt.Errorf("%w: the director has no deployment %s (%w). Check the -d value against bosh deployments",
			errSetupRefused, p.deployment, err)
	case boshapi.IsForbidden(err):
		return fmt.Errorf("%w: the director refused the setup on %s of deployment %s (%w). "+
			"Give the BOSH_CLIENT client the bosh.admin authority, or a team admin authority that covers the deployment",
			errSetupRefused, instance, p.deployment, err)
	default:
		return fmt.Errorf("%w: the setup on %s of deployment %s failed (%w). "+
			"Check BOSH_ENVIRONMENT, BOSH_CA_CERT, and the client's credentials, and that the director is reachable",
			errSetupRefused, instance, p.deployment, err)
	}
}

// awaitSetup waits for the setup task, reads its result, and returns the
// host to push to. It marks the state ended once the task has ended.
func (p *pusher) awaitSetup(ctx context.Context, state *setupState) ([]sshconn.Host, error) {
	_, err := p.client.Tasks().PollUntilComplete(ctx, state.taskID, &boshapi.PollOptions{Interval: p.pollInterval, Timeout: p.setupBound})

	var failed *boshapi.TaskError

	switch {
	case err == nil:
		state.ended = true
	case errors.As(err, &failed):
		state.ended = true

		return nil, fmt.Errorf("%w: BOSH task %d, the SSH setup for user %s on %s of deployment %s, did not finish: %w",
			errSetupUnusable, state.taskID, state.user, sshconn.TargetName(p.target), p.deployment, err)
	default:
		return nil, fmt.Errorf("%w: stopped waiting for BOSH task %d, the SSH setup for user %s on %s of deployment %s, "+
			"after at most %s: %w", errSetupUnusable, state.taskID, state.user, sshconn.TargetName(p.target), p.deployment, p.setupBound, err)
	}

	output, err := p.client.Tasks().GetOutput(ctx, state.taskID, &boshapi.TaskOutputOptions{Type: taskOutputResult})
	if err != nil {
		return nil, fmt.Errorf("%w: reading the result of BOSH task %d for SSH user %s failed: %w",
			errSetupUnusable, state.taskID, state.user, err)
	}

	results, err := boshapi.ParseSSHResults(output)
	if err != nil {
		return nil, fmt.Errorf("%w: BOSH task %d for SSH user %s: %w", errSetupUnusable, state.taskID, state.user, err)
	}

	hosts, err := sshconn.ReadHosts(results, state.user)
	if err != nil {
		return nil, fmt.Errorf("%w: BOSH task %d on deployment %s: %w", errSetupUnusable, state.taskID, p.deployment, err)
	}

	if len(hosts) != 1 {
		return nil, fmt.Errorf("%w: BOSH task %d on deployment %s returned %d hosts for %s, but the push needs exactly one",
			errSetupUnusable, state.taskID, p.deployment, len(hosts), sshconn.TargetName(p.target))
	}

	return hosts, nil
}

// transfer connects to the host with the host key the director reported,
// copies the binary, and restarts blacksmith.
func (p *pusher) transfer(ctx context.Context, credentials *sshconn.Credentials, host sshconn.Host) error {
	config, err := sshconn.HostConfig(p.deployment, host, credentials.Signer)
	if err != nil {
		return fmt.Errorf("connect to %s of deployment %s: %w", host.Instance(), p.deployment, err)
	}

	config.Timeout = p.dialTimeout
	addr := net.JoinHostPort(host.Address, p.sshPort)

	p.logf("Connecting to %s (%s of deployment %s) as %s", addr, host.Instance(), p.deployment, host.Username)

	client, err := sshconn.DialDirect(ctx, addr, config)
	if err != nil {
		return fmt.Errorf("connect to %s (%s of deployment %s): %w. Check that the instance is reachable from this host on port %s",
			addr, host.Instance(), p.deployment, sshconn.ExplainHostKeyRefusal(err, p.deployment, host, addr), p.sshPort)
	}

	defer func() { _ = client.Close() }()

	guest := &remote{client: client, host: addr, instance: host.Instance(), deployment: p.deployment}
	temp := "/tmp/blacksmith." + host.Username

	err = p.copyAndInstall(ctx, guest, temp)
	if err != nil {
		p.removeTemp(ctx, guest, temp)

		return err
	}

	p.logf("Restarting blacksmith on %s", guest.host)

	output, err := guest.step(ctx, "restart", monitRestart, nil)
	if err != nil {
		return err
	}

	p.logf("Restarted blacksmith on %s: %s", guest.host, strings.TrimSpace(output))

	return nil
}

// copyAndInstall streams the binary into temp and installs it from there.
// The install command removes temp once it has copied the file.
func (p *pusher) copyAndInstall(ctx context.Context, guest *remote, temp string) error {
	file, err := os.Open(p.binary)
	if err != nil {
		return fmt.Errorf("%w: open %s: %w", errStepFailed, p.binary, err)
	}

	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("%w: stat %s: %w", errStepFailed, p.binary, err)
	}

	p.logf("Uploading %s (%d bytes) to %s on %s", p.binary, info.Size(), temp, guest.host)

	_, err = guest.step(ctx, "upload", "umask 077 && cat > "+temp, file)
	if err != nil {
		return err
	}

	p.logf("Installing %s on %s", remoteBinary, guest.host)

	install := "sudo install -o vcap -g vcap -m 0755 " + temp + " " + remoteBinary + " && rm -f " + temp

	_, err = guest.step(ctx, "install", install, nil)

	return err
}

// removeTemp removes the temporary file after a failed copy. It runs on a
// context of its own, so an interrupt does not leave the file behind. A
// removal that fails is reported and does not replace the push's error.
func (p *pusher) removeTemp(ctx context.Context, guest *remote, temp string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tempRemoveBound)
	defer cancel()

	stop := context.AfterFunc(ctx, func() { _ = guest.client.Close() })
	defer stop()

	_, err := guest.step(ctx, "remove the temporary file", "rm -f "+temp, nil)
	if err != nil {
		p.logf("Could not remove %s: %v", temp, err)

		return
	}

	p.logf("Removed %s from %s", temp, guest.host)
}

// finish ends the setup's user. It waits for the setup task to end first,
// cancelling it when it is still running, because a cleanup that ran ahead
// of a setup the director still completes would leave the user behind. All
// of it runs under one deadline on a context of its own, so an interrupted
// push still cleans up.
func (p *pusher) finish(ctx context.Context, state *setupState) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.settleBound)
	defer cancel()

	instance := sshconn.TargetName(p.target)

	if !state.ended {
		if !p.settle(ctx, state) {
			return fmt.Errorf("%w: the SSH setup for user %s on %s of deployment %s was still running after %s. "+
				"The user's private key was only in this tool's memory, so nobody can log in as the user. "+
				"Remove user %s later from %s of deployment %s with a cleanup request for ^%s$",
				errSetupNotEnded, state.user, instance, p.deployment, p.settleBound, state.user, instance, p.deployment, state.user)
		}
	}

	return p.cleanUp(ctx, state)
}

// settle waits until the setup has ended, and reports whether it did.
func (p *pusher) settle(ctx context.Context, state *setupState) bool {
	if state.taskID == 0 {
		return p.awaitNoSetup(ctx)
	}

	p.cancelSetup(ctx, state.taskID)

	return p.awaitEnd(ctx, state.taskID)
}

// cancelSetup asks the director to cancel the setup task. A task that has
// ended or is already cancelling is not a failure.
func (p *pusher) cancelSetup(ctx context.Context, taskID int) {
	p.logf("Cancelling BOSH task %d, the SSH setup, and waiting for it to end", taskID)

	err := p.client.Tasks().Cancel(ctx, taskID)

	var stateErr *boshapi.TaskStateError
	if err != nil && !errors.As(err, &stateErr) {
		p.logf("Could not cancel BOSH task %d, so the tool waits for it to end by itself: %v", taskID, err)
	}
}

// awaitEnd reads the task until it reaches a final state, and reports
// whether it did before ctx ended. A read that fails is read again.
func (p *pusher) awaitEnd(ctx context.Context, taskID int) bool {
	for {
		task, err := p.client.Tasks().Get(ctx, taskID)
		if err == nil && task != nil && finalState(task.State) {
			return true
		}

		if !p.sleep(ctx, p.pollInterval) {
			return false
		}
	}
}

// awaitNoSetup waits for a setup whose answer was lost to become a task,
// and then lists the deployment's running tasks until none is an SSH setup.
// It reports whether none remained before ctx ended.
func (p *pusher) awaitNoSetup(ctx context.Context) bool {
	p.logf("The answer to the SSH setup was lost, so the tool waits %s and then for every SSH setup on deployment %s to end",
		p.lostSetupGrace, p.deployment)

	if !p.sleep(ctx, p.lostSetupGrace) {
		return false
	}

	for {
		tasks, err := p.client.Tasks().List(ctx, &boshapi.TaskListOptions{
			Deployment: p.deployment, State: runningTaskStates, Verbose: taskVerboseAll,
		})
		if err == nil && !hasRunningSetup(tasks) {
			return true
		}

		if !p.sleep(ctx, p.pollInterval) {
			return false
		}
	}
}

// hasRunningSetup reports whether tasks holds an SSH setup that has not
// ended.
func hasRunningSetup(tasks []boshapi.Task) bool {
	for _, task := range tasks {
		if !finalState(task.State) && strings.HasPrefix(task.Description, setupDescriptionPrefix) {
			return true
		}
	}

	return false
}

// finalState reports whether a task in state has ended.
func finalState(state string) bool {
	switch state {
	case "done", "error", "cancelled", "timeout":
		return true
	default:
		return false
	}
}

// sleep waits for d, and reports false when ctx ended first.
func (p *pusher) sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// cleanUp asks the director to remove the user, with the target the setup
// used, and waits for the cleanup task.
func (p *pusher) cleanUp(ctx context.Context, state *setupState) error {
	instance := sshconn.TargetName(p.target)

	p.logf("Removing SSH user %s from %s of deployment %s", state.user, instance, p.deployment)

	task, err := p.client.SSHClient().Cleanup(ctx, p.deployment, sshconn.CleanupRequest(p.deployment, p.target, state.user))
	if err != nil {
		return fmt.Errorf("%w: the cleanup request for SSH user %s on %s of deployment %s failed (%w), so the user may remain. "+
			"The user's private key was only in this tool's memory, so nobody can log in as the user",
			errCleanupFailed, state.user, instance, p.deployment, err)
	}

	if task == nil {
		return fmt.Errorf("%w: the director accepted the cleanup request for SSH user %s on %s of deployment %s but gave no task, so the user may remain. "+
			"The user's private key was only in this tool's memory, so nobody can log in as the user",
			errCleanupFailed, state.user, instance, p.deployment)
	}

	_, err = p.client.Tasks().PollUntilComplete(ctx, task.ID, &boshapi.PollOptions{Interval: p.pollInterval})
	if err != nil {
		return fmt.Errorf("%w: BOSH task %d, which removes SSH user %s from %s of deployment %s, did not finish (%w), so the user may remain. "+
			"The user's private key was only in this tool's memory, so nobody can log in as the user",
			errCleanupFailed, task.ID, state.user, instance, p.deployment, err)
	}

	return nil
}

// logf reports one step.
func (p *pusher) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(p.out, "devpush: "+format+"\n", args...)
}

// remote runs commands on the VM over one SSH connection.
type remote struct {
	client     *ssh.Client
	host       string
	instance   string
	deployment string
}

// step runs command in a session of its own, with stdin as its input, and
// returns what it wrote to stdout. A failure names the host, the step, and
// the command's stderr. When ctx ends first, the session is closed.
func (r *remote) step(ctx context.Context, name, command string, stdin io.Reader) (string, error) {
	session, err := r.client.NewSession()
	if err != nil {
		return "", fmt.Errorf("%w: the %s step could not open a session on %s (%s of deployment %s): %w",
			errStepFailed, name, r.host, r.instance, r.deployment, err)
	}

	defer func() { _ = session.Close() }()

	var stdout, stderr bytes.Buffer

	session.Stdin, session.Stdout, session.Stderr = stdin, &stdout, &stderr

	stop := context.AfterFunc(ctx, func() { _ = session.Close() })

	err = session.Run(command)

	stop()

	if err != nil {
		reason := ""
		if ctx.Err() != nil {
			reason = fmt.Sprintf(" (stopped: %v)", context.Cause(ctx))
		}

		return stdout.String(), fmt.Errorf("%w: the %s step failed on %s (%s of deployment %s)%s: %w. Remote command: %s. Remote stderr: %q",
			errStepFailed, name, r.host, r.instance, r.deployment, reason, err, command, strings.TrimSpace(stderr.String()))
	}

	return stdout.String(), nil
}
