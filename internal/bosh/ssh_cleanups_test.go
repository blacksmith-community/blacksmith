package bosh_test

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/bosh/sshconn"
	"blacksmith/internal/vault"
	"blacksmith/pkg/testutil"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// Values the cleanup record tests share.
const (
	leftoverUser       = "bosh_0123456789abcde"
	leftoverTargetName = "redis/2"
	leftoverContextID  = "ctx-setup-before-the-restart"
	// laterUser is a second leftover user on the same deployment, whose
	// record is newer than leftoverUser's.
	laterUser = "bosh_fedcba987654321"
	// deploymentGone is the director's description for code 70000.
	deploymentGone = "Deployment 'redis-1' doesn't exist"
	// agentTimedOut is the director's result for an SSH task whose agent
	// on redis/0 did not answer.
	agentTimedOut = "Timed out sending 'ssh' to instance: 'redis/0'"
)

// userInError is the form of a throwaway user named in an error.
var userInError = regexp.MustCompile(`bosh_[0-9a-f]{15}`)

// cleanupStore is the broker's Vault store against an in-process Vault,
// with a way to make its writes fail and a way to hold them.
type cleanupStore struct {
	*vault.Vault

	mu         sync.Mutex
	recordErr  error
	recordHold func(context.Context, sshconn.PendingCleanup)
}

func newCleanupStore(t *testing.T) *cleanupStore {
	t.Helper()

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("starting the in-process Vault: %v", err)
	}

	return &cleanupStore{Vault: vault.New(server.Addr, server.RootToken, true)}
}

// RecordSSHCleanup runs the hold holdRecords set, then fails with the
// error failRecords set, and otherwise writes the record to Vault.
func (s *cleanupStore) RecordSSHCleanup(ctx context.Context, cleanup sshconn.PendingCleanup) error {
	s.mu.Lock()
	err, hold := s.recordErr, s.recordHold
	s.mu.Unlock()

	if hold != nil {
		hold(ctx, cleanup)
	}

	if err != nil {
		return err
	}

	return s.Vault.RecordSSHCleanup(ctx, cleanup) //nolint:wrapcheck // the store is the Vault's own
}

func (s *cleanupStore) failRecords(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.recordErr = err
}

// holdRecords makes each record write run hold first, with the write's
// context and the record.
func (s *cleanupStore) holdRecords(hold func(context.Context, sshconn.PendingCleanup)) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.recordHold = hold
}

// records lists every record, and fails the test when the listing fails.
func (s *cleanupStore) records(t *testing.T) []sshconn.PendingCleanup {
	t.Helper()

	records, err := s.ListSSHCleanups(context.Background())
	if err != nil {
		t.Fatalf("ListSSHCleanups: %v", err)
	}

	return records
}

// find returns the record for user.
func (s *cleanupStore) find(t *testing.T, user string) (sshconn.PendingCleanup, bool) {
	t.Helper()

	for _, record := range s.records(t) {
		if record.User == user {
			return record, true
		}
	}

	return sshconn.PendingCleanup{}, false
}

// leave writes the record a broker that stopped before its cleanup would
// have left for leftoverUser on redis/2.
func (s *cleanupStore) leave(t *testing.T) {
	t.Helper()

	s.leaveUser(t, leftoverUser, 2, time.Now().Add(-time.Hour))
}

// leaveUser writes the record a broker that stopped before its cleanup
// would have left for user on redis/<index>, made at createdAt.
func (s *cleanupStore) leaveUser(t *testing.T, user string, index int, createdAt time.Time) {
	t.Helper()

	err := s.Vault.RecordSSHCleanup(context.Background(), sshconn.PendingCleanup{
		User:           user,
		Deployment:     sshDeployment,
		Target:         boshapi.SSHTarget{Job: sshInstance, Indices: []int{index}},
		SetupContextID: leftoverContextID,
		CreatedAt:      createdAt.UTC(),
	})
	if err != nil {
		t.Fatalf("leaving the record of %s: %v", user, err)
	}
}

// newSweepAdapter starts an SSH director that answers with answer, and a
// pooled adapter against it that keeps its cleanup records in an
// in-process Vault.
func newSweepAdapter(t *testing.T, answer func(boshtest.SSHCall) boshtest.SSHAnswer) (*boshtest.SSHDirector, *bosh.PooledDirector, *cleanupStore, *recordingLogger) {
	t.Helper()

	server, director, logger := newSSHAdapter(t, answer)

	pool := bosh.NewPooledDirector(director, 4, time.Second, logger)
	t.Cleanup(pool.Stop)

	store := newCleanupStore(t)
	pool.SetSSHCleanupStore(store)

	return server, pool, store, logger
}

// sweep runs one sweep and fails the test when it does not return in time.
func sweep(t *testing.T, pool *bosh.PooledDirector) error {
	t.Helper()

	return callWithin(t, sshCallLimit, func() error { return pool.SweepSSHCleanups(t.Context()) })
}

// The record is in Vault before the director hears of the setup, so a
// restart at any point after the setup is sent still finds the user.
func TestSSHSetupRecordsTheCleanupBeforeSending(t *testing.T) {
	var (
		guard  sync.Mutex
		stored *sshconn.PendingCleanup
		store  *cleanupStore
	)

	server, pool, store, _ := newSweepAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		if call.Command != boshtest.SSHSetup {
			return boshtest.SSHAnswer{}
		}

		records, err := store.ListSSHCleanups(context.Background())
		if err != nil {
			t.Errorf("the setup handler could not list the records: %v", err)
		}

		guard.Lock()
		defer guard.Unlock()

		for _, record := range records {
			if record.User == call.User() {
				stored = &record
			}
		}

		return boshtest.SSHAnswer{}
	})

	mustOpenSSHSession(t, pool)

	setup := onlyCall(t, server, boshtest.SSHSetup)

	guard.Lock()
	defer guard.Unlock()

	if stored == nil {
		t.Fatalf("the record for %s was not in Vault when the setup arrived", setup.User())
	}

	if stored.Deployment != sshDeployment || sshconn.TargetName(stored.Target) != sshTargetName ||
		stored.SetupContextID != setup.ContextID || stored.CreatedAt.IsZero() || stored.Attempts != 0 {
		t.Errorf("record %+v, want deployment %s, target %s, the setup's context ID %s, a creation time, and no attempts",
			*stored, sshDeployment, sshTargetName, setup.ContextID)
	}
}

// A setup whose record cannot be written is never sent, and the error
// names the user and says why nothing was set up.
func TestSSHSetupIsRefusedWhenTheRecordFails(t *testing.T) {
	sealed := errors.New("vault is sealed") //nolint:err113 // a test's own failure

	server, pool, store, _ := newSweepAdapter(t, nil)
	store.failRecords(sealed)

	err := callWithin(t, sshCallLimit, func() error {
		_, err := openSSHSession(t.Context(), pool)

		return err
	})

	if !errors.Is(err, bosh.ErrSSHCleanupNotRecorded) || !errors.Is(err, sealed) {
		t.Fatalf("SSHSession = %v, want ErrSSHCleanupNotRecorded wrapping the store's error", err)
	}

	if !userInError.MatchString(err.Error()) || !strings.Contains(err.Error(), "could not record") {
		t.Errorf("the error does not name the user and say it could not be recorded: %v", err)
	}

	if calls := server.Calls(); len(calls) != 0 {
		t.Errorf("%d SSH requests were sent, want none: %+v", len(calls), calls)
	}
}

// A cleanup task that ends done clears the record, and one that ends in
// error keeps it for the sweep.
func TestSSHCleanupClearsTheRecordOnlyWhenItsTaskIsDone(t *testing.T) {
	tests := []struct {
		name  string
		state string
		kept  bool
	}{
		{"done clears it", boshtest.TaskDone, false},
		{"error keeps it", boshtest.TaskError, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, pool, store, _ := newSweepAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
				if call.Command == boshtest.SSHCleanup {
					return boshtest.SSHAnswer{State: test.state, Result: "the agent on redis/0 did not answer"}
				}

				return boshtest.SSHAnswer{}
			})
			session := mustOpenSSHSession(t, pool)

			_ = callWithin(t, sshCallLimit, func() error { return session.Cleanup(t.Context()) })

			_, kept := store.find(t, session.User)
			if kept != test.kept {
				t.Fatalf("after a cleanup that ended %s the record was kept: %v, want %v", test.state, kept, test.kept)
			}
		})
	}
}

// A record a previous run left is swept with one cleanup for its own user
// and target, and cleared once that cleanup ends done.
func TestSSHSweepRetriesALeftoverRecord(t *testing.T) {
	server, pool, store, _ := newSweepAdapter(t, nil)
	store.leave(t)

	err := sweep(t, pool)
	if err != nil {
		t.Fatalf("SweepSSHCleanups: %v", err)
	}

	cleanup := onlyCall(t, server, boshtest.SSHCleanup)

	if cleanup.User() != sshconn.UserRegex(leftoverUser) || cleanup.Deployment != sshDeployment {
		t.Errorf("the sweep sent %q on %s, want %q on %s", cleanup.User(), cleanup.Deployment, sshconn.UserRegex(leftoverUser), sshDeployment)
	}

	target, _ := cleanup.Fields["target"].(map[string]interface{})
	if indexes, _ := target["indexes"].([]interface{}); target["job"] != sshInstance || len(indexes) != 1 || indexes[0] != float64(2) {
		t.Errorf("the sweep's cleanup targets %v, want %s", target, leftoverTargetName)
	}

	if cleanup.ContextID == "" || cleanup.ContextID == leftoverContextID {
		t.Errorf("the sweep's cleanup carries context ID %q, want a fresh one", cleanup.ContextID)
	}

	if _, kept := store.find(t, leftoverUser); kept {
		t.Error("the record is still in Vault after its cleanup ended done")
	}
}

// A user whose setup or cleanup this broker is still running is left to
// that call.
func TestSSHSweepSkipsALiveUser(t *testing.T) {
	server, pool, store, _ := newSweepAdapter(t, nil)
	session := mustOpenSSHSession(t, pool)

	err := sweep(t, pool)
	if err != nil {
		t.Fatalf("SweepSSHCleanups: %v", err)
	}

	if calls := server.CallsOf(boshtest.SSHCleanup); len(calls) != 0 {
		t.Fatalf("the sweep sent %d cleanups for a live user, want none", len(calls))
	}

	if _, kept := store.find(t, session.User); !kept {
		t.Fatal("the sweep cleared the record of a live user")
	}
}

// While any SSH setup is queued or running on a deployment, the sweep
// sends no cleanup there, so a cleanup never runs ahead of a setup. The
// next pass after the setup ends sends it.
func TestSSHSweepWaitsWhileASetupRuns(t *testing.T) {
	server, pool, store, logger := newSweepAdapter(t, nil)
	store.leave(t)

	setupTask := server.AddTask(sshDeployment, boshtest.SSHSetup, boshtest.TaskProcessing)

	err := sweep(t, pool)
	if err != nil {
		t.Fatalf("SweepSSHCleanups: %v", err)
	}

	if calls := server.CallsOf(boshtest.SSHCleanup); len(calls) != 0 {
		t.Fatalf("the sweep sent %d cleanups while a setup ran, want none", len(calls))
	}

	if _, kept := store.find(t, leftoverUser); !kept {
		t.Fatal("the sweep cleared the record while a setup ran")
	}

	if logger.count("INFO", leftoverUser, sshDeployment, "task "+strconv.Itoa(setupTask)) != 1 {
		t.Errorf("no single Info line names the user, the deployment, and the running setup task: %v", logger.lines)
	}

	server.SetState(setupTask, boshtest.TaskDone)

	err = sweep(t, pool)
	if err != nil {
		t.Fatalf("SweepSSHCleanups after the setup ended: %v", err)
	}

	onlyCall(t, server, boshtest.SSHCleanup)

	if _, kept := store.find(t, leftoverUser); kept {
		t.Error("the record is still in Vault after the setup ended and the cleanup ended done")
	}
}

// The director's deployment-not-found answer clears the record, because
// the user went with the deployment.
func TestSSHSweepClearsARecordWhoseDeploymentIsGone(t *testing.T) {
	server, pool, store, _ := newSweepAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		return boshtest.SSHAnswer{Status: http.StatusNotFound, Code: boshapi.ErrorCodeDeploymentNotFound, Description: deploymentGone}
	})
	store.leave(t)

	err := sweep(t, pool)
	if err != nil {
		t.Fatalf("SweepSSHCleanups: %v", err)
	}

	onlyCall(t, server, boshtest.SSHCleanup)

	if _, kept := store.find(t, leftoverUser); kept {
		t.Error("the record is still in Vault after the director said the deployment is gone")
	}
}

// A cleanup that fails because the instance has no VM clears the record,
// because the VM's users lived on a root disk that a recreate replaced.
func TestSSHSweepClearsARecordWhoseVMIsGone(t *testing.T) {
	server, pool, store, _ := newSweepAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		return boshtest.SSHAnswer{
			State:  boshtest.TaskError,
			Result: "No instance with a VM in deployment 'redis-1' matched filter {\"job\"=>\"redis\", \"indexes\"=>[2]}",
		}
	})
	store.leave(t)

	err := sweep(t, pool)
	if err != nil {
		t.Fatalf("SweepSSHCleanups: %v", err)
	}

	onlyCall(t, server, boshtest.SSHCleanup)

	if _, kept := store.find(t, leftoverUser); kept {
		t.Error("the record is still in Vault after the director said the instance has no VM")
	}
}

// Any other failure keeps the record, counts the attempt, stores the
// error, and logs a warning that names the attempt.
func TestSSHSweepKeepsAFailedRecordAndCountsTheAttempt(t *testing.T) {
	const agentDown = "Timed out sending 'ssh' to instance: 'redis/2'"

	_, pool, store, logger := newSweepAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		return boshtest.SSHAnswer{State: boshtest.TaskError, Result: agentDown}
	})
	store.leave(t)

	for attempt := 1; attempt <= 2; attempt++ {
		before := time.Now()

		err := sweep(t, pool)
		if !errors.Is(err, bosh.ErrSSHSweepIncomplete) {
			t.Fatalf("sweep %d = %v, want ErrSSHSweepIncomplete", attempt, err)
		}

		if msg := err.Error(); strings.Contains(msg, "%!") || !strings.HasSuffix(msg, "1 of 1 record(s) failed") {
			t.Errorf("sweep %d error = %q, want it to end with the failed count and hold no format error", attempt, msg)
		}

		record, kept := store.find(t, leftoverUser)
		if !kept {
			t.Fatalf("sweep %d cleared a record whose cleanup failed", attempt)
		}

		if record.Attempts != attempt || !strings.Contains(record.LastError, agentDown) || record.LastAttemptAt.Before(before.Add(-time.Second)) {
			t.Errorf("after sweep %d the record holds %d attempts, error %q, and last attempt %v, want %d, the task's error, and a fresh time",
				attempt, record.Attempts, record.LastError, record.LastAttemptAt, attempt)
		}

		if record.SetupContextID != leftoverContextID || sshconn.TargetName(record.Target) != leftoverTargetName {
			t.Errorf("after sweep %d the record holds context ID %s and target %s, want them unchanged",
				attempt, record.SetupContextID, sshconn.TargetName(record.Target))
		}

		if logger.count("WARN", leftoverUser, leftoverTargetName, sshDeployment, "attempt "+strconv.Itoa(attempt)) != 1 {
			t.Errorf("no single Warning line names the user, the target, the deployment, and attempt %d: %v", attempt, logger.lines)
		}
	}
}

// A setup the director refused created no user, so its record is cleared
// at once rather than left for the sweep.
func TestSSHSetupTheDirectorRefusedLeavesNoRecord(t *testing.T) {
	tests := []struct {
		name   string
		answer boshtest.SSHAnswer
	}{
		{"the deployment is gone", boshtest.SSHAnswer{
			Status: http.StatusNotFound, Code: boshapi.ErrorCodeDeploymentNotFound, Description: deploymentGone,
		}},
		{"the broker's client is not permitted", boshtest.SSHAnswer{
			Status: http.StatusUnauthorized, Code: boshapi.ErrorCodeUnauthorizedToAccessDeployment, Description: "Require one of the scopes: bosh.admin",
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, pool, store, _ := newSweepAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer { return test.answer })

			err := callWithin(t, sshCallLimit, func() error {
				_, err := openSSHSession(t.Context(), pool)

				return err
			})
			if err == nil {
				t.Fatal("SSHSession succeeded, want the director's refusal")
			}

			setup := onlyCall(t, server, boshtest.SSHSetup)

			if _, kept := store.find(t, setup.User()); kept {
				t.Errorf("the record of %s is still in Vault after the director refused its setup", setup.User())
			}
		})
	}
}

// A session whose cleanup task failed leaves the live set when that
// cleanup ends, so the next sweep in the same process sends the cleanup
// again and clears the record once it ends done.
func TestSSHSweepRetriesASessionCleanupThatFailed(t *testing.T) {
	var cleanups atomic.Int32

	server, pool, store, _ := newSweepAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		if call.Command == boshtest.SSHCleanup && cleanups.Add(1) == 1 {
			return boshtest.SSHAnswer{State: boshtest.TaskError, Result: agentTimedOut}
		}

		return boshtest.SSHAnswer{}
	})
	session := mustOpenSSHSession(t, pool)

	err := callWithin(t, sshCallLimit, func() error { return session.Cleanup(t.Context()) })
	if !errors.Is(err, bosh.ErrTaskFailed) {
		t.Fatalf("Cleanup = %v, want the cleanup task's failure", err)
	}

	if _, kept := store.find(t, session.User); !kept {
		t.Fatal("a cleanup that failed cleared the record")
	}

	err = sweep(t, pool)
	if err != nil {
		t.Fatalf("SweepSSHCleanups: %v", err)
	}

	calls := server.CallsOf(boshtest.SSHCleanup)
	if len(calls) != 2 || calls[1].User() != sshconn.UserRegex(session.User) {
		t.Fatalf("the sweep did not send the failed cleanup again: %d cleanups %+v", len(calls), calls)
	}

	if _, kept := store.find(t, session.User); kept {
		t.Error("the record is still in Vault after the sweep's cleanup ended done")
	}
}

// The sweep asks whether an SSH setup runs on a deployment right before
// each cleanup it sends there, not once per pass. A setup that starts
// while an earlier cleanup on the deployment is in flight holds back the
// cleanups that follow, and the next pass after it ends sends them.
func TestSSHSweepLooksForASetupBeforeEachCleanup(t *testing.T) {
	var (
		guard    sync.Mutex
		director *boshtest.SSHDirector
		setup    int
	)

	server, pool, store, _ := newSweepAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		guard.Lock()
		defer guard.Unlock()

		if call.Command == boshtest.SSHCleanup && setup == 0 {
			setup = director.AddTask(sshDeployment, boshtest.SSHSetup, boshtest.TaskProcessing)
		}

		return boshtest.SSHAnswer{}
	})

	guard.Lock()
	director = server
	guard.Unlock()

	store.leaveUser(t, leftoverUser, 2, time.Now().Add(-2*time.Hour))
	store.leaveUser(t, laterUser, 3, time.Now().Add(-time.Hour))

	err := sweep(t, pool)
	if err != nil {
		t.Fatalf("SweepSSHCleanups: %v", err)
	}

	calls := server.CallsOf(boshtest.SSHCleanup)
	if len(calls) != 1 || calls[0].User() != sshconn.UserRegex(leftoverUser) {
		t.Fatalf("the sweep sent %d cleanups %+v, want only the one for %s, sent before the setup started", len(calls), calls, leftoverUser)
	}

	if _, kept := store.find(t, laterUser); !kept {
		t.Fatalf("the sweep cleared the record of %s while a setup ran", laterUser)
	}

	guard.Lock()
	running := setup
	guard.Unlock()

	server.SetState(running, boshtest.TaskDone)

	err = sweep(t, pool)
	if err != nil {
		t.Fatalf("SweepSSHCleanups after the setup ended: %v", err)
	}

	calls = server.CallsOf(boshtest.SSHCleanup)
	if len(calls) != 2 || calls[1].User() != sshconn.UserRegex(laterUser) {
		t.Fatalf("after the setup ended the sweep sent %+v, want a second cleanup for %s", calls, laterUser)
	}

	if records := store.records(t); len(records) != 0 {
		t.Errorf("records %+v are still in Vault after both cleanups ended done", records)
	}
}
