package bosh

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"blacksmith/internal/bosh/sshconn"
)

const (
	// lostSetupGrace is how long the adapter waits, after a setup whose
	// answer was lost, for a request still inside the director to become
	// a task before it looks for running setups.
	lostSetupGrace = 10 * time.Second
	// sshSetupSettleTimeout bounds the wait for a failed setup's task to
	// end before its cleanup is sent.
	sshSetupSettleTimeout = 60 * time.Second
	// sshSettlePollInterval is how often the settle wait reads the setup
	// task when the adapter has no poll interval of its own.
	sshSettlePollInterval = 2 * time.Second
)

// afterFailedSSHSetup handles a setup that was sent and then failed. The
// director may have created the user, or may still create it, because it
// runs a started SSH job to its end even after a cancel. So the user's
// cleanup is sent once the setup task has ended, in the background, and
// the caller gets its error at once. taskID is the setup task, or 0 when
// the answer was lost. The error names the user, and the user stays in the
// live set until the cleanup ends, so the sweep leaves it alone.
func (d *DirectorAdapter) afterFailedSSHSetup(ctx context.Context, operation string, pending sshconn.PendingCleanup, taskID int, err error) error {
	d.explainContext(ctx, operation, &err)

	instance := sshconn.TargetName(pending.Target)
	nameSSHUser(err, pending.User, instance)
	d.markSSHUser(pending.User, sshUserSettling)

	if taskID > 0 {
		d.log.Infof("SSH setup task %d for user %s on %s of deployment %s failed, so Blacksmith removes the user once the task has ended",
			taskID, pending.User, instance, pending.Deployment)
	} else {
		d.log.Infof("The answer to the SSH setup for user %s on %s of deployment %s was lost, so Blacksmith removes the user once no SSH setup is running there",
			pending.User, instance, pending.Deployment)
	}

	go d.settleAndCleanUpSSH(context.WithoutCancel(ctx), pending, taskID)

	return err
}

// settleAndCleanUpSSH waits for a failed setup to end and then sends its
// cleanup. When the setup has not ended within the settle bound, it sends
// no cleanup, because a cleanup that ran first would leave the user the
// setup creates later. The user's record then stays for the sweep, which
// also waits for running setups.
func (d *DirectorAdapter) settleAndCleanUpSSH(ctx context.Context, pending sshconn.PendingCleanup, taskID int) {
	grace, settle := d.sshSettleBounds()
	instance := sshconn.TargetName(pending.Target)

	var settled bool

	if taskID > 0 {
		settled = d.settleSSHSetupTask(ctx, taskID, settle)
	} else {
		settled = d.settleLostSSHSetup(ctx, pending.Deployment, grace, settle)
	}

	if !settled {
		d.releaseSSHUser(pending.User)

		if taskID > 0 {
			d.warnf("SSH setup task %d for user %s on %s of deployment %s had not ended after %s, so Blacksmith sent no cleanup. "+
				"The user's record in Vault stays, and the next SSH cleanup sweep removes the user",
				taskID, pending.User, instance, pending.Deployment, settle)
		} else {
			d.warnf("An SSH setup was still running on deployment %s %s after the answer to the setup for user %s on %s was lost, "+
				"so Blacksmith sent no cleanup. The user's record in Vault stays, and the next SSH cleanup sweep removes the user",
				pending.Deployment, grace+settle, pending.User, instance)
		}

		return
	}

	// cleanUpSSH logs every way the removal can fail, and keeps the record
	// for the sweep when it does.
	_ = d.cleanUpSSH(ctx, pending.Deployment, pending.Target, pending.User, pending.SetupContextID)
}

// settleSSHSetupTask reads the setup task until it reaches a terminal
// state, and reports whether it did within settle. A failed read is
// logged and read again.
func (d *DirectorAdapter) settleSSHSetupTask(ctx context.Context, taskID int, settle time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, settle)
	defer cancel()

	for {
		task, err := d.api.Tasks().Get(ctx, taskID)

		switch {
		case err != nil:
			d.log.Debugf("Failed to read SSH setup task %d while waiting for it to end: %v", taskID, err)
		case task != nil && isTerminalTaskState(task.State):
			d.log.Debugf("SSH setup task %d ended in state %s", taskID, task.State)

			return true
		}

		if !d.sleepSettle(ctx) {
			return false
		}
	}
}

// settleLostSSHSetup waits grace for a setup whose answer was lost to
// become a task, and then lists the deployment's running tasks until no
// SSH setup remains. It reports whether none remained within settle. It
// never looks the setup up by its context ID, because the director does
// not store that ID on an SSH task.
func (d *DirectorAdapter) settleLostSSHSetup(ctx context.Context, deployment string, grace, settle time.Duration) bool {
	timer := time.NewTimer(grace)

	select {
	case <-ctx.Done():
		timer.Stop()

		return false
	case <-timer.C:
	}

	ctx, cancel := context.WithTimeout(ctx, settle)
	defer cancel()

	for {
		setup, err := d.runningSSHSetup(ctx, deployment)
		if err == nil && setup == nil {
			return true
		}

		if setup != nil {
			d.log.Debugf("SSH setup task %d is still %s on deployment %s", setup.ID, setup.State, deployment)
		}

		if !d.sleepSettle(ctx) {
			return false
		}
	}
}

// sleepSettle waits one settle poll, and reports false when ctx ended
// first.
func (d *DirectorAdapter) sleepSettle(ctx context.Context) bool {
	interval := d.taskPollInterval
	if interval <= 0 {
		interval = sshSettlePollInterval
	}

	timer := time.NewTimer(interval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// sshSettleBounds returns the adapter's lost-answer grace and settle
// bound, or the defaults where it has none.
func (d *DirectorAdapter) sshSettleBounds() (time.Duration, time.Duration) {
	grace, settle := d.lostSetupGrace, d.setupSettleTimeout

	if grace <= 0 {
		grace = lostSetupGrace
	}

	if settle <= 0 {
		settle = sshSetupSettleTimeout
	}

	return grace, settle
}

// nameSSHUser names the user and the instance in a failed setup's error,
// so the error says which user may exist. The first of these that the
// error holds gets them: an OutcomeUnknownError, a TaskFailedError for a
// setup task that ended in any state but done, and a DirectorCallError.
// Only the first is named, so the sentence never appears twice. Any
// other error is left as it is.
func nameSSHUser(err error, user, instance string) {
	var unknown *OutcomeUnknownError
	if errors.As(err, &unknown) {
		unknown.SSHUser, unknown.SSHInstance = user, instance

		return
	}

	var failed *TaskFailedError
	if errors.As(err, &failed) {
		failed.SSHUser, failed.SSHInstance = user, instance

		return
	}

	var callErr *DirectorCallError
	if errors.As(err, &callErr) {
		callErr.SSHUser, callErr.SSHInstance = user, instance
	}
}

// withSSHUser adds to an error message the sentence that names the SSH
// user a failed setup may have left, when there is one.
func withSSHUser(message, user, instance string) string {
	if user == "" {
		return message
	}

	sentence := fmt.Sprintf("SSH user %s may exist on %s. Blacksmith removes the user once the setup task has ended, "+
		"and the broker's log names the BOSH task that removes it", user, instance)

	if strings.HasSuffix(message, ".") {
		return message + " " + sentence + "."
	}

	return message + ". " + sentence + "."
}
