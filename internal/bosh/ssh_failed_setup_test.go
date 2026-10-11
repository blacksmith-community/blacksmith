package bosh_test

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/bosh/sshconn"
)

// Bounds the failed setup tests use in place of the adapter's own.
const (
	testLostSetupGrace = 20 * time.Millisecond
	testSetupSettle    = 5 * time.Second
	// settleWatch is how long a test watches for a cleanup that must not
	// arrive yet.
	settleWatch = 300 * time.Millisecond
)

// sshCleanupOwner is the part of the adapter that takes a cleanup store.
type sshCleanupOwner interface {
	SetSSHCleanupStore(store bosh.SSHCleanupStore)
}

// sshSweeper is the part of the adapter that sweeps its cleanup records.
type sshSweeper interface {
	SweepSSHCleanups(ctx context.Context) error
}

// sweepAdapter runs one sweep on the adapter, and fails the test when the
// sweep does not return in time or returns an error.
func sweepAdapter(t *testing.T, director bosh.Director) {
	t.Helper()

	sweeper, ok := director.(sshSweeper)
	if !ok {
		t.Fatalf("the adapter (%T) does not sweep its cleanup records", director)
	}

	err := callWithin(t, sshCallLimit, func() error { return sweeper.SweepSSHCleanups(t.Context()) })
	if err != nil {
		t.Fatalf("SweepSSHCleanups: %v", err)
	}
}

// newFailedSetupAdapter starts an SSH director whose setups answer with
// setup and whose cleanups end done, and an adapter against it that keeps
// its cleanup records in an in-process Vault and settles a failed setup
// within the test bounds.
func newFailedSetupAdapter(t *testing.T, setup func(boshtest.SSHCall) boshtest.SSHAnswer) (*boshtest.SSHDirector, bosh.Director, *cleanupStore, *recordingLogger) {
	t.Helper()

	server, director, logger := newSSHAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		if call.Command == boshtest.SSHSetup && setup != nil {
			return setup(call)
		}

		return boshtest.SSHAnswer{}
	})

	owner, ok := director.(sshCleanupOwner)
	if !ok {
		t.Fatalf("the adapter (%T) takes no cleanup store", director)
	}

	store := newCleanupStore(t)
	owner.SetSSHCleanupStore(store)
	bosh.SetSSHSetupSettle(director, testLostSetupGrace, testSetupSettle)

	return server, director, store, logger
}

// failSetup opens a session that must fail, and returns its error.
func failSetup(t *testing.T, ctx context.Context, director bosh.Director) error {
	t.Helper()

	err := callWithin(t, sshCallLimit, func() error {
		_, err := openSSHSession(ctx, director)

		return err
	})
	if err == nil {
		t.Fatal("SSHSession succeeded, want the setup to fail")
	}

	return err
}

// awaitCleanup waits for the one cleanup of the setup's user, and fails
// the test when it names another user or arrived before the setup task
// ended.
func awaitCleanup(t *testing.T, server *boshtest.SSHDirector, setup boshtest.SSHCall) {
	t.Helper()

	waitUntil(t, "the failed setup's cleanup arrives", func() bool { return len(server.CallsOf(boshtest.SSHCleanup)) > 0 })

	cleanup := onlyCall(t, server, boshtest.SSHCleanup)

	if cleanup.User() != sshconn.UserRegex(setup.User()) {
		t.Errorf("the cleanup names %q, want %q", cleanup.User(), sshconn.UserRegex(setup.User()))
	}

	task, found := server.Task(setup.TaskID)
	if !found {
		t.Fatalf("the director has no setup task %d", setup.TaskID)
	}

	if task.EndedAt.IsZero() || cleanup.At.Before(task.EndedAt) {
		t.Errorf("the cleanup arrived at %v, before setup task %d ended (state %s, ended at %v)",
			cleanup.At, task.ID, task.State, task.EndedAt)
	}
}

// checkNamesTheUser fails the test when the error does not name the user
// and the instance, and say the user is removed once the setup has ended.
func checkNamesTheUser(t *testing.T, err error, user string) {
	t.Helper()

	message := err.Error()

	for _, want := range []string{"SSH user " + user + " may exist on " + sshTargetName, "once the setup task has ended", "log names the BOSH task"} {
		if !strings.Contains(message, want) {
			t.Errorf("the error does not say %q: %s", want, message)
		}
	}
}

// awaitCleared waits until the user's record is gone from Vault.
func awaitCleared(t *testing.T, store *cleanupStore, user string) {
	t.Helper()

	waitUntil(t, "the record of "+user+" is cleared", func() bool {
		_, kept := store.find(t, user)

		return !kept
	})
}

// failedSetupPath is one way a setup that was sent can fail.
type failedSetupPath struct {
	name string
	// answer answers the setup. cancel ends the call's context.
	answer func(cancel context.CancelFunc) boshtest.SSHAnswer
	// afterReturn runs once the call has returned, with the setup.
	afterReturn func(server *boshtest.SSHDirector, setup boshtest.SSHCall)
}

func failedSetupPaths() []failedSetupPath {
	failedHost := `[{"status":"error","ip":"","host_public_key":"","job":"redis","index":0,"id":"abc"}]`

	return []failedSetupPath{
		{name: "the task ends in error", answer: func(context.CancelFunc) boshtest.SSHAnswer {
			return boshtest.SSHAnswer{State: boshtest.TaskError, Result: agentTimedOut}
		}},
		{name: "the task is cancelled", answer: func(context.CancelFunc) boshtest.SSHAnswer {
			return boshtest.SSHAnswer{State: boshtest.TaskCancelled}
		}},
		{name: "the task times out", answer: func(context.CancelFunc) boshtest.SSHAnswer {
			return boshtest.SSHAnswer{State: boshtest.TaskTimeout}
		}},
		{
			name: "a read of the task fails while it runs",
			answer: func(context.CancelFunc) boshtest.SSHAnswer {
				return boshtest.SSHAnswer{State: boshtest.TaskProcessing, FailReads: 1}
			},
			afterReturn: func(server *boshtest.SSHDirector, setup boshtest.SSHCall) {
				server.SetState(setup.TaskID, boshtest.TaskDone)
			},
		},
		// The context ends at the adapter's first read of the queued task,
		// not at a deadline, because a deadline that starts before the
		// setup is sent can pass before the setup reaches the director.
		{name: "the context ends while the task is queued", answer: func(cancel context.CancelFunc) boshtest.SSHAnswer {
			return boshtest.SSHAnswer{State: boshtest.TaskQueued, OnRead: cancel}
		}},
		{name: "the context ends after the task is done but before its result is read", answer: func(cancel context.CancelFunc) boshtest.SSHAnswer {
			return boshtest.SSHAnswer{HoldOutput: func(read context.Context) {
				cancel()
				<-read.Done()
			}}
		}},
		{name: "the result cannot be read", answer: func(context.CancelFunc) boshtest.SSHAnswer {
			return boshtest.SSHAnswer{OutputStatus: http.StatusNotFound}
		}},
		{name: "the result does not parse", answer: func(context.CancelFunc) boshtest.SSHAnswer {
			return boshtest.SSHAnswer{Output: "not json"}
		}},
		{name: "the result names no host", answer: func(context.CancelFunc) boshtest.SSHAnswer {
			return boshtest.SSHAnswer{Output: "[]"}
		}},
		{name: "the result reports a failed host", answer: func(context.CancelFunc) boshtest.SSHAnswer {
			return boshtest.SSHAnswer{Output: failedHost}
		}},
		{name: "the answer to the setup is lost", answer: func(context.CancelFunc) boshtest.SSHAnswer {
			return boshtest.SSHAnswer{Drop: true}
		}},
	}
}

// Once a setup has been sent, every failure removes its user with one
// cleanup, sent after the setup task ended, and the record is cleared once
// that cleanup ends done.
func TestSSHSetupCleansUpOnEveryFailurePath(t *testing.T) {
	for _, path := range failedSetupPaths() {
		t.Run(path.name, func(t *testing.T) {
			t.Setenv("BLACKSMITH_TEST_MODE", "true")

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			server, director, store, _ := newFailedSetupAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer { return path.answer(cancel) })

			_ = failSetup(t, ctx, director)

			setup := onlyCall(t, server, boshtest.SSHSetup)

			if path.afterReturn != nil {
				path.afterReturn(server, setup)
			}

			awaitCleanup(t, server, setup)
			awaitCleared(t, store, setup.User())
		})
	}
}

// A setup the director has started runs to its end even after a cancel,
// so the cleanup waits until the cancelled setup task has ended.
func TestSSHCleanupWaitsForACancelledSetupToEnd(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, director, store, _ := newFailedSetupAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer {
		return boshtest.SSHAnswer{State: boshtest.TaskProcessing}
	})

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_ = failSetup(t, ctx, director)

	setup := onlyCall(t, server, boshtest.SSHSetup)

	waitUntil(t, "the setup task is cancelled", func() bool {
		task, _ := server.Task(setup.TaskID)

		return task.State == boshtest.TaskCancelling
	})

	time.Sleep(settleWatch)

	if calls := server.CallsOf(boshtest.SSHCleanup); len(calls) != 0 {
		t.Fatalf("a cleanup arrived while the cancelled setup task was still running: %+v", calls)
	}

	server.SetState(setup.TaskID, boshtest.TaskCancelled)

	awaitCleanup(t, server, setup)
	awaitCleared(t, store, setup.User())
}

// A setup whose answer was lost is never looked up by its context ID. Its
// cleanup waits until no SSH setup is running on the deployment, and the
// caller's error names the context ID and the user.
func TestSSHCleanupAfterALostAnswerWaitsForTheSetupToLeave(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, director, store, _ := newFailedSetupAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer {
		return boshtest.SSHAnswer{Drop: true, State: boshtest.TaskProcessing}
	})

	err := failSetup(t, t.Context(), director)
	setup := onlyCall(t, server, boshtest.SSHSetup)

	var unknown *bosh.OutcomeUnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("SSHSession = %v, want an OutcomeUnknownError", err)
	}

	if !unknown.NoLookup || unknown.ContextID != setup.ContextID || unknown.SSHUser != setup.User() {
		t.Errorf("OutcomeUnknownError has NoLookup %v, context ID %q, and user %q, want true, %q, and %q",
			unknown.NoLookup, unknown.ContextID, unknown.SSHUser, setup.ContextID, setup.User())
	}

	checkNamesTheUser(t, err, setup.User())

	time.Sleep(settleWatch)

	if calls := server.CallsOf(boshtest.SSHCleanup); len(calls) != 0 {
		t.Fatalf("a cleanup arrived while the setup task was still running: %+v", calls)
	}

	server.SetState(setup.TaskID, boshtest.TaskDone)

	awaitCleanup(t, server, setup)
	awaitCleared(t, store, setup.User())

	for _, query := range server.Listings() {
		if query.Get("context_id") != "" {
			t.Errorf("the adapter looked the lost setup up by its context ID: %v", query)
		}
	}
}

// A setup task that never ends within the settle bound gets no cleanup.
// Its record stays for the sweep, and the log says so.
func TestSSHSetupThatNeverSettlesIsLeftToItsRecord(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, director, store, logger := newFailedSetupAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer {
		return boshtest.SSHAnswer{State: boshtest.TaskProcessing}
	})
	bosh.SetSSHSetupSettle(director, testLostSetupGrace, 100*time.Millisecond)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_ = failSetup(t, ctx, director)

	setup := onlyCall(t, server, boshtest.SSHSetup)
	taskText := "task " + strconv.Itoa(setup.TaskID)

	waitUntil(t, "the adapter gives up on the setup task", func() bool {
		return logger.count("WARN", setup.User(), taskText, "record") > 0
	})

	time.Sleep(settleWatch)

	if calls := server.CallsOf(boshtest.SSHCleanup); len(calls) != 0 {
		t.Fatalf("a cleanup arrived for a setup task that never ended: %+v", calls)
	}

	if _, kept := store.find(t, setup.User()); !kept {
		t.Fatal("the record of a user whose setup never ended was cleared")
	}

	if count := logger.count("WARN", setup.User(), taskText, "record"); count != 1 {
		t.Errorf("%d Warning lines name the user, the setup task, and the record, want 1: %v", count, logger.lines)
	}
}

// A shutdown spares a read-only task, but a setup task creates a user, so
// its wait still cancels it.
func TestSSHSetupWaitCancelsEvenWhenTheBrokerIsShuttingDown(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, director, store, _ := newFailedSetupAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer {
		return boshtest.SSHAnswer{State: boshtest.TaskQueued}
	})

	ctx, cancel := context.WithTimeoutCause(t.Context(), 100*time.Millisecond, bosh.ErrBrokerShuttingDown)
	defer cancel()

	err := failSetup(t, ctx, director)

	setup := onlyCall(t, server, boshtest.SSHSetup)

	var callErr *bosh.DirectorCallError
	if !errors.As(err, &callErr) || callErr.SSHUser != setup.User() || callErr.SSHInstance != sshTargetName {
		t.Errorf("SSHSession = %v, want a DirectorCallError that names user %s on %s", err, setup.User(), sshTargetName)
	}

	checkNamesTheUser(t, err, setup.User())

	cancels := server.Cancels()
	if len(cancels) != 1 || cancels[0].TaskID != setup.TaskID {
		t.Fatalf("cancels %+v, want one for setup task %d", cancels, setup.TaskID)
	}

	awaitCleanup(t, server, setup)
	awaitCleared(t, store, setup.User())
}

// The caller gets the setup's error at once, while the cleanup is still
// waiting on the director.
func TestSSHSetupFailureReturnsBeforeTheCleanup(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	release := make(chan struct{})
	released := false

	defer func() {
		if !released {
			close(release)
		}
	}()

	server, director, logger := newSSHAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		if call.Command == boshtest.SSHCleanup {
			<-release

			return boshtest.SSHAnswer{}
		}

		return boshtest.SSHAnswer{State: boshtest.TaskError, Result: agentTimedOut}
	})
	bosh.SetSSHSetupSettle(director, testLostSetupGrace, testSetupSettle)

	err := callWithin(t, 2*time.Second, func() error {
		_, err := openSSHSession(t.Context(), director)

		return err
	})
	if !errors.Is(err, bosh.ErrTaskFailed) {
		t.Fatalf("SSHSession = %v, want the setup task's failure", err)
	}

	if calls := server.CallsOf(boshtest.SSHCleanup); len(calls) != 0 {
		t.Fatalf("the cleanup finished before the caller got its error: %+v", calls)
	}

	close(release)

	released = true

	setup := onlyCall(t, server, boshtest.SSHSetup)
	awaitCleanup(t, server, setup)

	waitUntil(t, "the cleanup is logged", func() bool {
		return logger.count("INFO", setup.User(), "removed") > 0
	})
}

// A failed setup whose task outlasted the settle bound leaves the live
// set, so once its task ends the next sweep in the same process removes
// the user and clears the record.
func TestSSHSweepRemovesAUserWhoseSetupNeverSettled(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, director, store, logger := newFailedSetupAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer {
		return boshtest.SSHAnswer{State: boshtest.TaskProcessing}
	})
	bosh.SetSSHSetupSettle(director, testLostSetupGrace, 100*time.Millisecond)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_ = failSetup(t, ctx, director)

	setup := onlyCall(t, server, boshtest.SSHSetup)

	waitUntil(t, "the adapter gives up on the setup task", func() bool {
		return logger.count("WARN", setup.User(), "task "+strconv.Itoa(setup.TaskID), "record") > 0
	})

	server.SetState(setup.TaskID, boshtest.TaskCancelled)

	sweepAdapter(t, director)

	awaitCleanup(t, server, setup)

	if _, kept := store.find(t, setup.User()); kept {
		t.Error("the record is still in Vault after the sweep's cleanup ended done")
	}
}

// A setup task that ends in error is the commonest failure, and its error
// names the user and the instance as every other failed setup's does.
func TestSSHSetupTaskFailureNamesTheUser(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, director, store, _ := newFailedSetupAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer {
		return boshtest.SSHAnswer{State: boshtest.TaskError, Result: agentTimedOut}
	})

	err := failSetup(t, t.Context(), director)

	setup := onlyCall(t, server, boshtest.SSHSetup)

	var failed *bosh.TaskFailedError
	if !errors.As(err, &failed) || failed.SSHUser != setup.User() || failed.SSHInstance != sshTargetName {
		t.Errorf("SSHSession = %v, want a TaskFailedError that names user %s on %s", err, setup.User(), sshTargetName)
	}

	checkNamesTheUser(t, err, setup.User())

	awaitCleanup(t, server, setup)
	awaitCleared(t, store, setup.User())
}

// The sweep leaves a user this process holds to the call that holds it,
// and its log says what that call is doing, so the state a user is in is
// known for as long as it is live.
func TestSSHSweepSaysWhyItLeavesAHeldUser(t *testing.T) {
	t.Run("a setup that is still running", func(t *testing.T) {
		t.Setenv("BLACKSMITH_TEST_MODE", "true")
		checkSweepSkipsASettingUpUser(t)
	})

	t.Run("an open session", func(t *testing.T) {
		t.Setenv("BLACKSMITH_TEST_MODE", "true")
		checkSweepSkipsASessionUser(t)
	})

	t.Run("a failed setup whose task has not ended", func(t *testing.T) {
		t.Setenv("BLACKSMITH_TEST_MODE", "true")
		checkSweepSkipsASettlingUser(t)
	})

	t.Run("a cleanup that is still running", func(t *testing.T) {
		t.Setenv("BLACKSMITH_TEST_MODE", "true")
		checkSweepSkipsACleaningUpUser(t)
	})
}

// checkSweepSkipsASettingUpUser sweeps while a setup task runs.
func checkSweepSkipsASettingUpUser(t *testing.T) {
	t.Helper()

	server, director, _, logger := newFailedSetupAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer {
		return boshtest.SSHAnswer{State: boshtest.TaskProcessing}
	})

	opened := make(chan openedSession, 1)

	go func() {
		session, err := openSSHSession(t.Context(), director)
		opened <- openedSession{session: session, err: err}
	}()

	waitUntil(t, "the setup arrives", func() bool { return len(server.CallsOf(boshtest.SSHSetup)) == 1 })

	setup := onlyCall(t, server, boshtest.SSHSetup)

	sweepAdapter(t, director)

	server.SetState(setup.TaskID, boshtest.TaskDone)

	session := awaitSession(t, opened)
	_ = callWithin(t, sshCallLimit, func() error { return session.Cleanup(t.Context()) })

	checkSkipReason(t, logger, setup.User(), "whose SSH setup is running")
}

// checkSweepSkipsASessionUser sweeps while a session is open.
func checkSweepSkipsASessionUser(t *testing.T) {
	t.Helper()

	_, director, _, logger := newFailedSetupAdapter(t, nil)
	session := mustOpenSSHSession(t, director)

	sweepAdapter(t, director)

	checkSkipReason(t, logger, session.User, "whose session is open")
}

// checkSweepSkipsASettlingUser sweeps while a failed setup waits for its
// task to end.
func checkSweepSkipsASettlingUser(t *testing.T) {
	t.Helper()

	server, director, _, logger := newFailedSetupAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer {
		return boshtest.SSHAnswer{State: boshtest.TaskProcessing}
	})

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_ = failSetup(t, ctx, director)

	setup := onlyCall(t, server, boshtest.SSHSetup)

	sweepAdapter(t, director)

	server.SetState(setup.TaskID, boshtest.TaskCancelled)
	awaitCleanup(t, server, setup)

	checkSkipReason(t, logger, setup.User(), "whose failed setup task has not ended")
}

// checkSweepSkipsACleaningUpUser sweeps while a session's cleanup task
// is held.
func checkSweepSkipsACleaningUpUser(t *testing.T) {
	t.Helper()

	release := make(chan struct{})
	arrived := make(chan struct{}, 1)

	server, director, logger := newSSHAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		if call.Command == boshtest.SSHCleanup {
			arrived <- struct{}{}

			<-release
		}

		return boshtest.SSHAnswer{}
	})

	owner, ok := director.(sshCleanupOwner)
	if !ok {
		t.Fatalf("the adapter (%T) takes no cleanup store", director)
	}

	owner.SetSSHCleanupStore(newCleanupStore(t))

	session := mustOpenSSHSession(t, director)

	cleaned := make(chan error, 1)

	go func() { cleaned <- session.Cleanup(t.Context()) }()

	select {
	case <-arrived:
	case <-time.After(sshCallLimit):
		close(release)
		t.Fatal("the session's cleanup never arrived")
	}

	sweepAdapter(t, director)
	close(release)

	select {
	case <-cleaned:
	case <-time.After(sshCallLimit):
		t.Fatal("the session's cleanup did not return")
	}

	if calls := server.CallsOf(boshtest.SSHCleanup); len(calls) != 1 {
		t.Errorf("%d cleanups were sent, want only the session's own", len(calls))
	}

	checkSkipReason(t, logger, session.User, "whose cleanup is running")
}

// openedSession is what a session opened in the background returned.
type openedSession struct {
	session *sshconn.Session
	err     error
}

// awaitSession waits for a session that opens in the background.
func awaitSession(t *testing.T, opened <-chan openedSession) *sshconn.Session {
	t.Helper()

	select {
	case result := <-opened:
		if result.err != nil {
			t.Fatalf("SSHSession: %v", result.err)
		}

		return result.session
	case <-time.After(sshCallLimit):
		t.Fatal("SSHSession did not return once its setup task was done")
	}

	return nil
}

// checkSkipReason fails the test unless one Debug line says the sweep
// skipped the user, and why.
func checkSkipReason(t *testing.T, logger *recordingLogger, user, reason string) {
	t.Helper()

	if count := logger.count("DEBUG", "sweep skips SSH user "+user, reason); count != 1 {
		t.Errorf("%d Debug lines say the sweep skipped %s %s, want 1: %v", count, user, reason, logger.lines)
	}
}
