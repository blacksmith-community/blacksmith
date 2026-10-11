package bosh

import (
	"context"
	"fmt"
	"sync"
	"time"

	"blacksmith/internal/bosh/sshconn"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// sshShutdowner is a director that sends its SSH cleanups when the broker
// shuts down, as the adapter does.
type sshShutdowner interface {
	ShutdownSSH(ctx context.Context)
}

// sshSessionCleanup is the one cleanup of an open session's user. The
// session's Cleanup and ShutdownSSH share it. Whichever claims it first
// sends the cleanup, and every caller gets that one cleanup's error, so
// the user gets one cleanup however the session ends.
type sshSessionCleanup struct {
	deployment     string
	target         boshapi.SSHTarget
	user           string
	setupContextID string

	// claimed is guarded by the adapter's sshUsers mutex.
	claimed bool
	// done closes once err is set.
	done chan struct{}
	err  error
}

// finish records the outcome of the cleanup and releases every caller
// waiting for it.
func (c *sshSessionCleanup) finish(err error) {
	c.err = err
	close(c.done)
}

// lateSSHCleanupTimeout bounds the send of the cleanup of a user whose
// setup ended done after ShutdownSSH closed the gate. It matches the
// budget shutdown gives its own sends, and it does not cover the cleanup
// task, which nothing waits for.
const lateSSHCleanupTimeout = 5 * time.Second

// openSSHSessionCleanup moves the session's user to in session, registers
// the session's cleanup for ShutdownSSH, and returns the session's Cleanup.
// Cleanup removes the user once however often it is called, and returns
// that one cleanup's error to every caller. When ShutdownSSH has already
// sent the cleanup, Cleanup sends none and returns the error of the one
// ShutdownSSH sent.
//
// When ShutdownSSH closed the gate while setup task taskID ran, no session
// opens. The user's cleanup is sent at once without waiting for its task,
// and the error wraps ErrBrokerShuttingDown.
func (d *DirectorAdapter) openSSHSessionCleanup(ctx context.Context, session *sshconn.Session, taskID int, setupContextID string) (func(context.Context) error, error) {
	cleanup := &sshSessionCleanup{
		deployment:     session.Deployment,
		target:         session.Target,
		user:           session.User,
		setupContextID: setupContextID,
		done:           make(chan struct{}),
	}

	if !d.registerSSHSession(cleanup) {
		return nil, d.refuseLateSSHSession(ctx, cleanup, taskID)
	}

	return func(ctx context.Context) error {
		if d.claimSessionCleanup(cleanup) {
			cleanup.finish(d.cleanUpSSH(ctx, cleanup.deployment, cleanup.target, cleanup.user, cleanup.setupContextID))
		}

		<-cleanup.done

		return cleanup.err
	}, nil
}

// registerSSHSession moves the cleanup's user to in session and registers
// the cleanup for ShutdownSSH, and reports true. Once ShutdownSSH has
// closed the gate, it registers nothing, claims the cleanup for the
// caller, moves the user to cleaning up, and reports false. The check and
// the registration share the lock ShutdownSSH closes the gate under, so
// every session's cleanup is either sent by ShutdownSSH or by the caller.
func (d *DirectorAdapter) registerSSHSession(cleanup *sshSessionCleanup) bool {
	d.ssh.mu.Lock()
	defer d.ssh.mu.Unlock()

	if d.ssh.live == nil {
		d.ssh.live = make(map[string]sshUserState)
	}

	if d.ssh.closed {
		cleanup.claimed = true
		d.ssh.live[cleanup.user] = sshUserCleaningUp

		return false
	}

	if d.ssh.sessions == nil {
		d.ssh.sessions = make(map[string]*sshSessionCleanup)
	}

	d.ssh.live[cleanup.user] = sshUserInSession
	d.ssh.sessions[cleanup.user] = cleanup

	return true
}

// refuseLateSSHSession handles a setup that ended done after ShutdownSSH
// closed the gate. It sends the user's cleanup within
// lateSSHCleanupTimeout, which tests shorten, on a context detached from
// the caller's, and does not wait for the cleanup task, so the user's
// record stays for the next sweep. It returns the error that wraps
// ErrBrokerShuttingDown, and the send's own error when the cleanup was not
// sent.
func (d *DirectorAdapter) refuseLateSSHSession(ctx context.Context, cleanup *sshSessionCleanup, taskID int) error {
	instance := sshconn.TargetName(cleanup.target)

	d.log.Infof("SSH setup task %d set up SSH user %s on %s of deployment %s after the SSH shutdown began, "+
		"so Blacksmith opens no session and sends the user's cleanup", taskID, cleanup.user, instance, cleanup.deployment)

	bound := d.lateCleanupTimeout
	if bound <= 0 {
		bound = lateSSHCleanupTimeout
	}

	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bound)
	defer cancel()

	err := d.sendSSHCleanupAtShutdown(sendCtx, cleanup)
	cleanup.finish(err)

	if err != nil {
		return fmt.Errorf("%w: SSH setup task %d set up SSH user %s on %s of deployment %s after the shutdown began, so Blacksmith opened no session. "+
			"The user's record in Vault stays for the next SSH cleanup sweep: %w",
			ErrBrokerShuttingDown, taskID, cleanup.user, instance, cleanup.deployment, err)
	}

	return fmt.Errorf("%w: SSH setup task %d set up SSH user %s on %s of deployment %s after the shutdown began, "+
		"so Blacksmith opened no session and sent the user's cleanup",
		ErrBrokerShuttingDown, taskID, cleanup.user, instance, cleanup.deployment)
}

// claimSessionCleanup claims the session's cleanup for the caller, which
// must then send it and finish it, and moves the user to cleaning up. It
// reports false when the cleanup was claimed already.
func (d *DirectorAdapter) claimSessionCleanup(cleanup *sshSessionCleanup) bool {
	d.ssh.mu.Lock()
	defer d.ssh.mu.Unlock()

	return d.claimSessionCleanupLocked(cleanup)
}

// claimSessionCleanupLocked is claimSessionCleanup for a caller that holds
// the sshUsers mutex.
func (d *DirectorAdapter) claimSessionCleanupLocked(cleanup *sshSessionCleanup) bool {
	if cleanup.claimed {
		return false
	}

	cleanup.claimed = true

	delete(d.ssh.sessions, cleanup.user)

	if d.ssh.live == nil {
		d.ssh.live = make(map[string]sshUserState)
	}

	d.ssh.live[cleanup.user] = sshUserCleaningUp

	return true
}

// claimSSHSetup claims a new setup's user as setting up, unless
// ShutdownSSH has closed the gate, in which case it claims nothing and
// returns an error wrapping ErrBrokerShuttingDown. The check and the claim
// share the lock ShutdownSSH closes the gate under, so a setup is either
// refused or live when ShutdownSSH looks.
func (d *DirectorAdapter) claimSSHSetup(cleanup sshconn.PendingCleanup) error {
	d.ssh.mu.Lock()
	defer d.ssh.mu.Unlock()

	if d.ssh.closed {
		return fmt.Errorf("%w: Blacksmith did not set up SSH user %s on %s of deployment %s, so nothing was sent",
			ErrBrokerShuttingDown, cleanup.User, sshconn.TargetName(cleanup.Target), cleanup.Deployment)
	}

	if d.ssh.live == nil {
		d.ssh.live = make(map[string]sshUserState)
	}

	// NewCredentials makes a fresh user for every setup, so no other call
	// can hold it.
	d.ssh.live[cleanup.User] = sshUserSettingUp

	return nil
}

// checkSSHSetupGate runs once the pending user's record is written, just
// before its setup is sent. While the gate is open it returns nil. Once
// ShutdownSSH has closed the gate, the setup must not be sent, so it
// clears the record, which names a user no setup created, releases the
// user, and returns an error that wraps ErrBrokerShuttingDown. A record
// whose clear fails stays, and the next sweep sends a cleanup that finds
// no user and clears it then. A setup that passes this check can still
// meet a gate closed before its request reaches the director, and it is
// then a setup in flight that ShutdownSSH leaves to its record.
func (d *DirectorAdapter) checkSSHSetupGate(ctx context.Context, pending sshconn.PendingCleanup) error {
	d.ssh.mu.Lock()
	closed := d.ssh.closed
	d.ssh.mu.Unlock()

	if !closed {
		return nil
	}

	instance := sshconn.TargetName(pending.Target)

	d.log.Infof("The SSH shutdown began while SSH user %s on %s of deployment %s was being recorded, so Blacksmith sends no setup and clears the record",
		pending.User, instance, pending.Deployment)

	d.dropSSHCleanup(ctx, pending.User)

	return fmt.Errorf("%w: Blacksmith did not set up SSH user %s on %s of deployment %s, because the shutdown began while the user was being recorded, so nothing was sent",
		ErrBrokerShuttingDown, pending.User, instance, pending.Deployment)
}

// ShutdownSSH runs when the broker shuts down. It first closes the gate.
// From then on a new setup claims no user and sends nothing. A setup that
// claimed its user earlier checks the gate again after its record is
// written, just before it is sent, so it sends nothing either and clears
// its record. A setup that passed that second check before the gate closed
// may still reach the director, and its user is left to its record like
// any other setup in flight. No setup still in flight opens a session, and
// no sweep under way claims another user. It then sends the cleanup of
// every open session, all at once, and returns when every cleanup has
// been sent or ctx has ended, whichever is first. It does not wait for
// the cleanup tasks, and it keeps every record, because no cleanup was
// seen to end. The next sweep sends each cleanup again, which finds no
// user once the first has run, and clears the record. A session whose
// cleanup this sent sends no second one when it closes.
//
// A user whose setup is running, whose failed setup is settling, or whose
// cleanup is already running is left alone. A setup still in flight, and
// the settle goroutine of a failed one, can outlast any shutdown budget,
// because a setup task runs until the director ends it and the settle
// waits up to a minute for that. A setup in flight that ends done later
// opens no session, and sends its user's cleanup itself without waiting
// for the task. Each such user has had its record since before its setup
// was sent, so the next sweep removes it, and the sweep waits for any
// setup still running on the deployment first.
func (d *DirectorAdapter) ShutdownSSH(ctx context.Context) {
	sending, left := d.closeSSH()

	for user, state := range left {
		d.log.Infof("The SSH shutdown leaves SSH user %s, %s, to its record in Vault", user, state)
	}

	if len(sending) == 0 {
		d.log.Infof("The SSH shutdown found no open session to send a cleanup for")

		return
	}

	d.log.Infof("The SSH shutdown sends the cleanup of %d open session(s) and does not wait for the cleanup tasks", len(sending))

	var sent sync.WaitGroup

	for _, cleanup := range sending {
		sent.Go(func() { cleanup.finish(d.sendSSHCleanupAtShutdown(ctx, cleanup)) })
	}

	allSent := make(chan struct{})

	go func() {
		sent.Wait()
		close(allSent)
	}()

	select {
	case <-allSent:
	case <-ctx.Done():
		d.warnf("The SSH shutdown stopped before every cleanup was sent: %v. Each user's record in Vault stays, and the next SSH cleanup sweep removes the user",
			context.Cause(ctx))
	}
}

// closeSSH closes the setup gate, claims the cleanup of every open
// session, and returns those cleanups together with every other live user
// and its state.
func (d *DirectorAdapter) closeSSH() ([]*sshSessionCleanup, map[string]sshUserState) {
	d.ssh.mu.Lock()
	defer d.ssh.mu.Unlock()

	d.ssh.closed = true

	sending := make([]*sshSessionCleanup, 0, len(d.ssh.sessions))

	for _, cleanup := range d.ssh.sessions {
		if d.claimSessionCleanupLocked(cleanup) {
			sending = append(sending, cleanup)
		}
	}

	claimed := make(map[string]bool, len(sending))
	for _, cleanup := range sending {
		claimed[cleanup.user] = true
	}

	left := make(map[string]sshUserState)

	for user, state := range d.ssh.live {
		if !claimed[user] {
			left[user] = state
		}
	}

	return sending, left
}

// sendSSHCleanupAtShutdown sends one session's cleanup within ctx without
// waiting for its task, and releases the user. The user's record stays
// either way, for the next sweep.
func (d *DirectorAdapter) sendSSHCleanupAtShutdown(ctx context.Context, cleanup *sshSessionCleanup) error {
	defer d.releaseSSHUser(cleanup.user)

	instance := sshconn.TargetName(cleanup.target)

	task, contextID, err := d.requestSSHCleanup(ctx, cleanup.deployment, cleanup.target, cleanup.user, cleanup.setupContextID)
	if err != nil {
		d.log.Errorf("The SSH shutdown could not send the cleanup of SSH user %s on %s of deployment %s (setup context ID %s). "+
			"The user's record in Vault stays, and the next SSH cleanup sweep removes the user: %v",
			cleanup.user, instance, cleanup.deployment, cleanup.setupContextID, err)

		return fmt.Errorf("the cleanup of SSH user %s on %s of deployment %s was not sent at shutdown: %w",
			cleanup.user, instance, cleanup.deployment, err)
	}

	d.log.Infof("BOSH task %d removes SSH user %s from %s of deployment %s (context ID %s). The broker does not wait for it, so the user's record in Vault stays for the next sweep",
		task.ID, cleanup.user, instance, cleanup.deployment, contextID)

	return nil
}
