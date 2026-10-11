package bosh

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"blacksmith/internal/bosh/sshconn"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// sshCleanupRecordTimeout bounds each write or clear of a cleanup record
// that runs on a context of its own.
const sshCleanupRecordTimeout = 10 * time.Second

// sshSetupDescriptionPrefix starts the description the director gives
// every SSH setup task, "ssh: setup:" followed by the target.
const sshSetupDescriptionPrefix = "ssh: setup"

// sshNoVMText starts the director's error for an SSH task whose target
// matches no instance with a VM.
const sshNoVMText = "No instance with a VM in deployment"

var (
	// ErrSSHCleanupNotRecorded means a setup was not sent, because its user
	// could not be recorded for removal first.
	ErrSSHCleanupNotRecorded = errors.New("blacksmith could not record the SSH user for removal")
	// ErrSSHSweepIncomplete means a sweep left records it could not clear.
	ErrSSHSweepIncomplete = errors.New("the SSH cleanup sweep left records it could not clear")
	// ErrSSHCleanupsUnsupported means the director a pool wraps keeps no
	// SSH cleanup records.
	ErrSSHCleanupsUnsupported = errors.New("the director keeps no SSH cleanup records")
)

// SSHCleanupStore keeps a record of every throwaway SSH user that may
// still be on a VM, so a cleanup that a restart, a lost answer, or a failed
// task missed is sent again. The broker's Vault implements it.
type SSHCleanupStore interface {
	// RecordSSHCleanup writes the record for cleanup.User, replacing any
	// record the user already has.
	RecordSSHCleanup(ctx context.Context, cleanup sshconn.PendingCleanup) error
	// ClearSSHCleanup removes the record for user. A record that is
	// already gone is not an error.
	ClearSSHCleanup(ctx context.Context, user string) error
	// ListSSHCleanups returns every record, oldest first. It may return
	// the records it could use together with an error that names the ones
	// it could not.
	ListSSHCleanups(ctx context.Context) ([]sshconn.PendingCleanup, error)
}

// sshCleanupOwner is a director that keeps SSH cleanup records and sweeps
// them, as the adapter does.
type sshCleanupOwner interface {
	SetSSHCleanupStore(store SSHCleanupStore)
	SweepSSHCleanups(ctx context.Context) error
}

// sshUsers holds the adapter's cleanup store and its live set, the users
// whose setup or cleanup this process is running. The sweep leaves a live
// user to the call that owns it.
type sshUsers struct {
	mu    sync.Mutex
	store SSHCleanupStore
	live  map[string]struct{}
}

// SetSSHCleanupStore hands the adapter the store its SSH setups record
// their users in. With no store, a setup keeps no record, and a cleanup
// that fails is not retried.
func (d *DirectorAdapter) SetSSHCleanupStore(store SSHCleanupStore) {
	d.ssh.mu.Lock()
	defer d.ssh.mu.Unlock()

	d.ssh.store = store
}

// SweepSSHCleanups sends a cleanup for every recorded user that this
// process is not handling, one at a time, and clears each record whose
// cleanup settled it. It skips a live user, and every user on a deployment
// where an SSH setup is queued or running, so a cleanup never runs ahead
// of a setup. A cleanup that ends done, the director's answer that the
// deployment is gone, and its answer that the target has no VM each clear
// the record. Any other failure counts the attempt in the record, keeps
// it, and logs a warning.
//
// The sweep claims each user in the live set first, under the lock every
// setup takes to register its user, so no call in this process can start
// on the user while the sweep holds it. Only then does it ask the director
// for the deployment's running setups, once for each record and right
// before that record's cleanup. Each earlier cleanup can take up to
// sshCleanupTimeout, so an answer kept from earlier in the pass could miss
// a setup that the director started since, such as one whose answer was
// lost and whose settle gave up.
//
// Each broker owns its own Vault mount, so every record in the store is
// this broker's, and the live set is the only ownership check the sweep
// makes. Two brokers sharing one mount would clean up each other's users
// while their sessions were open.
func (d *DirectorAdapter) SweepSSHCleanups(ctx context.Context) error {
	store := d.sshCleanupStore()
	if store == nil {
		d.log.Debugf("No SSH cleanup store is set, so there are no SSH cleanups to sweep")

		return nil
	}

	records, listErr := store.ListSSHCleanups(ctx)
	if listErr != nil {
		d.log.Errorf("The SSH cleanup sweep could not read every record: %v", listErr)

		if len(records) == 0 {
			return fmt.Errorf("the SSH cleanup sweep could not read its records: %w", listErr)
		}
	}

	if len(records) == 0 {
		d.log.Debugf("The SSH cleanup sweep found no records")

		return nil
	}

	d.log.Infof("The SSH cleanup sweep found %d record(s)", len(records))

	failed := 0

	for _, record := range records {
		if ctx.Err() != nil {
			return fmt.Errorf("the SSH cleanup sweep stopped before it handled every record: %w", context.Cause(ctx))
		}

		if !d.claimSSHUser(record.User) {
			d.log.Debugf("The SSH cleanup sweep skips SSH user %s, whose setup or cleanup is running", record.User)

			continue
		}

		if !d.sweepSSHCleanup(ctx, store, record) {
			failed++
		}
	}

	if listErr != nil {
		return fmt.Errorf("%w: %d of %d record(s) failed: %w", ErrSSHSweepIncomplete, failed, len(records), listErr)
	}

	if failed > 0 {
		return fmt.Errorf("%w: %d of %d record(s) failed", ErrSSHSweepIncomplete, failed, len(records))
	}

	return nil
}

// sweepSSHCleanup handles one record whose user the sweep has claimed, and
// releases the user when it returns. It first asks the director whether an
// SSH setup is queued or running on the record's deployment, and leaves
// the record for the next sweep when one is. Otherwise it sends the
// cleanup and settles the record. It reports false when the listing or
// the cleanup failed.
func (d *DirectorAdapter) sweepSSHCleanup(ctx context.Context, store SSHCleanupStore, record sshconn.PendingCleanup) bool {
	defer d.releaseSSHUser(record.User)

	instance := sshconn.TargetName(record.Target)

	setup, err := d.runningSSHSetup(ctx, record.Deployment)
	if err != nil {
		d.log.Errorf("The SSH cleanup sweep could not list the running tasks of deployment %s, so it leaves SSH user %s for the next sweep: %v",
			record.Deployment, record.User, err)

		return false
	}

	if setup != nil {
		d.log.Infof("The SSH cleanup sweep leaves SSH user %s on %s of deployment %s for the next sweep, because SSH setup task %d is %s there",
			record.User, instance, record.Deployment, setup.ID, setup.State)

		return true
	}

	return d.retrySSHCleanup(ctx, store, record)
}

// retrySSHCleanup sends the cleanup for one claimed record and settles the
// record. It reports whether the record was settled.
func (d *DirectorAdapter) retrySSHCleanup(ctx context.Context, store SSHCleanupStore, record sshconn.PendingCleanup) bool {
	instance := sshconn.TargetName(record.Target)

	d.log.Infof("The SSH cleanup sweep removes SSH user %s from %s of deployment %s (setup context ID %s, %d earlier attempt(s))",
		record.User, instance, record.Deployment, record.SetupContextID, record.Attempts)

	err := d.sendSSHCleanup(ctx, record.Deployment, record.Target, record.User, record.SetupContextID, d.log.Debugf)
	if sshCleanupSettled(err) {
		if err != nil {
			d.log.Infof("The SSH cleanup sweep clears the record of SSH user %s on %s of deployment %s, because the user cannot remain: %v",
				record.User, instance, record.Deployment, err)
		}

		d.clearSSHCleanup(ctx, store, record.User)

		return true
	}

	record.Attempts++
	record.LastError = err.Error()
	record.LastAttemptAt = time.Now().UTC()

	d.warnf("The SSH cleanup of SSH user %s on %s of deployment %s failed on attempt %d, so the record stays for the next sweep: %v",
		record.User, instance, record.Deployment, record.Attempts, err)

	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sshCleanupRecordTimeout)
	defer cancel()

	writeErr := store.RecordSSHCleanup(recordCtx, record)
	if writeErr != nil {
		d.log.Errorf("Failed to count attempt %d in the record of SSH user %s on %s of deployment %s: %v",
			record.Attempts, record.User, instance, record.Deployment, writeErr)
	}

	return false
}

// runningSSHSetup returns the newest SSH setup task that is queued or
// running on deployment, or nil when there is none. It lists at verbose 2,
// because the director's default verbosity leaves SSH tasks out.
func (d *DirectorAdapter) runningSSHSetup(ctx context.Context, deployment string) (*Task, error) {
	tasks, err := d.listTasks(ctx, "list the running tasks of deployment "+deployment,
		&boshapi.TaskListOptions{Deployment: deployment, State: runningTaskStates, Verbose: taskVerboseAll})
	if err != nil {
		return nil, err
	}

	for _, task := range tasks {
		if isRunningTaskState(task.State) && strings.HasPrefix(task.Description, sshSetupDescriptionPrefix) {
			return &task, nil
		}
	}

	return nil, nil //nolint:nilnil // no running setup is an answer, not an error
}

// sshCleanupSettled reports whether a cleanup's outcome means the user can
// no longer be on the VM. That is a cleanup task that ended done, the
// director's answer that the deployment is gone, and a cleanup that failed
// because the target has no VM, whose users lived on a root disk that a
// recreate replaced.
func sshCleanupSettled(err error) bool {
	if err == nil {
		return true
	}

	var apiErr *boshapi.APIError
	if errors.As(err, &apiErr) && apiErr.Code == boshapi.ErrorCodeDeploymentNotFound {
		return true
	}

	var failed *TaskFailedError

	return errors.As(err, &failed) && strings.HasPrefix(failed.Result, sshNoVMText)
}

// recordSSHCleanup claims the user and writes its record before a setup
// is sent, within sshCleanupRecordTimeout of ctx. With no store set, it
// only claims the user. A failed write
// releases the user and returns an error wrapping
// ErrSSHCleanupNotRecorded, and the setup must not be sent.
func (d *DirectorAdapter) recordSSHCleanup(ctx context.Context, cleanup sshconn.PendingCleanup) error {
	d.claimSSHUser(cleanup.User)

	store := d.sshCleanupStore()
	if store == nil {
		d.log.Debugf("No SSH cleanup store is set, so SSH user %s gets no record and a missed cleanup is not retried", cleanup.User)

		return nil
	}

	recordCtx, cancel := context.WithTimeout(ctx, sshCleanupRecordTimeout)
	defer cancel()

	err := store.RecordSSHCleanup(recordCtx, cleanup)
	if err != nil {
		d.releaseSSHUser(cleanup.User)

		d.log.Errorf("Failed to record SSH user %s on %s of deployment %s for removal, so no setup was sent: %v",
			cleanup.User, sshconn.TargetName(cleanup.Target), cleanup.Deployment, err)

		return fmt.Errorf("%w: Blacksmith did not set up SSH user %s on %s of deployment %s, because it could not record the user for removal: %w",
			ErrSSHCleanupNotRecorded, cleanup.User, sshconn.TargetName(cleanup.Target), cleanup.Deployment, err)
	}

	return nil
}

// clearSSHCleanup removes the record for user on a context of its own, so
// a caller whose context has ended still clears it. A failed clear is
// logged, and the next sweep sends a cleanup that finds no user and clears
// the record then.
func (d *DirectorAdapter) clearSSHCleanup(ctx context.Context, store SSHCleanupStore, user string) {
	if store == nil {
		return
	}

	clearCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sshCleanupRecordTimeout)
	defer cancel()

	err := store.ClearSSHCleanup(clearCtx, user)
	if err != nil {
		d.log.Errorf("Failed to clear the record of SSH user %s, so the next sweep sends its cleanup again: %v", user, err)
	}
}

// dropSSHCleanup clears the record of a user the director never created,
// and releases the user.
func (d *DirectorAdapter) dropSSHCleanup(ctx context.Context, user string) {
	defer d.releaseSSHUser(user)

	d.clearSSHCleanup(ctx, d.sshCleanupStore(), user)
}

// sshCleanupStore returns the store set, or nil.
func (d *DirectorAdapter) sshCleanupStore() SSHCleanupStore { //nolint:ireturn // the store is the interface the caller set
	d.ssh.mu.Lock()
	defer d.ssh.mu.Unlock()

	return d.ssh.store
}

// claimSSHUser adds user to the live set, and reports false when it was
// already there.
func (d *DirectorAdapter) claimSSHUser(user string) bool {
	d.ssh.mu.Lock()
	defer d.ssh.mu.Unlock()

	if d.ssh.live == nil {
		d.ssh.live = make(map[string]struct{})
	}

	if _, live := d.ssh.live[user]; live {
		return false
	}

	d.ssh.live[user] = struct{}{}

	return true
}

// releaseSSHUser removes user from the live set.
func (d *DirectorAdapter) releaseSSHUser(user string) {
	d.ssh.mu.Lock()
	defer d.ssh.mu.Unlock()

	delete(d.ssh.live, user)
}

// warnf logs at Warning when the logger has that level, and at Error when
// it does not.
func (d *DirectorAdapter) warnf(format string, args ...interface{}) {
	warner, ok := d.log.(interface {
		Warnf(format string, args ...interface{})
	})
	if ok {
		warner.Warnf(format, args...)

		return
	}

	d.log.Errorf(format, args...)
}
