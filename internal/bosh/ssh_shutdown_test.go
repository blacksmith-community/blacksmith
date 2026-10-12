package bosh_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/bosh/sshconn"
)

const (
	// shutdownTestBudget stands in for the shutdown's SSH budget.
	shutdownTestBudget = 500 * time.Millisecond
	// shutdownTestSlack is how far past its budget the SSH shutdown may
	// return.
	shutdownTestSlack = time.Second
	// heldCleanup is how long the director holds each cleanup request in
	// the budget test, far past the budget.
	heldCleanup = 10 * time.Second
)

// sshShutdowner is the director's SSH shutdown, which the adapter and the
// pool both have.
type sshShutdowner interface {
	ShutdownSSH(ctx context.Context)
}

// shutdownSSH runs the director's SSH shutdown under shutdownTestBudget,
// and returns how long it took.
func shutdownSSH(t *testing.T, director bosh.Director) time.Duration {
	t.Helper()

	return shutdownSSHWithin(t, director, shutdownTestBudget)
}

// shutdownSSHWithin runs the director's SSH shutdown under budget, fails
// the test when it does not return within shutdownTestSlack of it, and
// returns how long it took.
func shutdownSSHWithin(t *testing.T, director bosh.Director, budget time.Duration) time.Duration {
	t.Helper()

	shutdowner, ok := director.(sshShutdowner)
	if !ok {
		t.Fatalf("the director (%T) has no SSH shutdown", director)
	}

	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()

	began := time.Now()

	err := callWithin(t, budget+shutdownTestSlack, func() error {
		shutdowner.ShutdownSSH(ctx)

		return nil
	})
	if err != nil {
		t.Fatalf("the SSH shutdown did not return within %v of its %v budget: %v", shutdownTestSlack, budget, err)
	}

	return time.Since(began)
}

// newShutdownAdapter is newFailedSetupAdapter whose cleanups answer with
// cleanup, when it is set.
func newShutdownAdapter(t *testing.T, cleanup func(boshtest.SSHCall) boshtest.SSHAnswer) (*boshtest.SSHDirector, bosh.Director, *cleanupStore) {
	t.Helper()

	server, director, _ := newSSHAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		if call.Command == boshtest.SSHCleanup && cleanup != nil {
			return cleanup(call)
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

	return server, director, store
}

// The SSH shutdown sends the cleanup of every open session at once and
// does not wait for the director, so three cleanups the director holds
// far past the budget still all arrive, and the shutdown returns at its
// budget. Each held request's context ends with the budget, which shows
// the sends run under the shutdown's budget and not a detached context.
// The records stay, because no cleanup was seen to end, and the next sweep
// settles them. The pool hands the shutdown to its director.
func TestShutdownSendsEverySSHCleanupWithinItsBudget(t *testing.T) {
	var arrived atomic.Int32

	release := make(chan struct{})
	gaveUp := make(chan time.Time, 3)

	server, director, store := newShutdownAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer {
		arrived.Add(1)

		return boshtest.SSHAnswer{Hold: func(ctx context.Context) {
			select {
			case <-ctx.Done():
				gaveUp <- time.Now()
			case <-release:
			case <-time.After(heldCleanup):
			}
		}}
	})
	t.Cleanup(func() { close(release) })

	sessions := []*sshconn.Session{mustOpenSSHSession(t, director), mustOpenSSHSession(t, director), mustOpenSSHSession(t, director)}

	pool := bosh.NewPooledDirector(director, 4, time.Second, &recordingLogger{})

	began := time.Now()

	elapsed := shutdownSSH(t, pool)
	if elapsed > shutdownTestBudget+shutdownTestSlack {
		t.Errorf("the SSH shutdown took %v, want at most %v", elapsed, shutdownTestBudget+shutdownTestSlack)
	}

	if got := int(arrived.Load()); got != len(sessions) {
		t.Errorf("%d cleanups reached the director within the budget, want all %d sent at once", got, len(sessions))
	}

	for _, session := range sessions {
		if _, kept := store.find(t, session.User); !kept {
			t.Errorf("the record of SSH user %s was cleared, want it kept for the sweep", session.User)
		}
	}

	deadline := time.After(time.Until(began.Add(shutdownTestBudget + shutdownTestSlack)))

	for range sessions {
		select {
		case at := <-gaveUp:
			if waited := at.Sub(began); waited > shutdownTestBudget+shutdownTestSlack {
				t.Errorf("a held cleanup's request ended %v after the shutdown began, want within %v", waited, shutdownTestBudget+shutdownTestSlack)
			}
		case <-deadline:
			t.Fatalf("a held cleanup's request was still open %v after the shutdown began, want every send ended with the budget",
				shutdownTestBudget+shutdownTestSlack)
		}
	}

	if setups := len(server.CallsOf(boshtest.SSHSetup)); setups != len(sessions) {
		t.Errorf("%d setups, want %d", setups, len(sessions))
	}
}

// The SSH shutdown returns once every cleanup is sent, without waiting
// for the cleanup tasks. The director answers each cleanup at once and
// leaves its task processing, so the shutdown returns well inside its
// budget, never reads a cleanup task, and leaves every record for the
// sweep.
func TestShutdownReturnsOnceEveryCleanupIsSent(t *testing.T) {
	const budget = 4 * time.Second

	server, director, store := newShutdownAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer {
		return boshtest.SSHAnswer{State: boshtest.TaskProcessing}
	})

	sessions := []*sshconn.Session{mustOpenSSHSession(t, director), mustOpenSSHSession(t, director), mustOpenSSHSession(t, director)}

	elapsed := shutdownSSHWithin(t, director, budget)
	if elapsed > budget/2 {
		t.Errorf("the SSH shutdown took %v with every cleanup answered at once, want it back well inside its %v budget", elapsed, budget)
	}

	cleanups := server.CallsOf(boshtest.SSHCleanup)
	if len(cleanups) != len(sessions) {
		t.Fatalf("%d cleanups sent, want %d", len(cleanups), len(sessions))
	}

	for _, cleanup := range cleanups {
		if reads := server.Reads(cleanup.TaskID); reads != 0 {
			t.Errorf("the shutdown read cleanup task %d %d time(s), want it never waited for", cleanup.TaskID, reads)
		}
	}

	for _, session := range sessions {
		if _, kept := store.find(t, session.User); !kept {
			t.Errorf("the record of SSH user %s was cleared, want it kept for the sweep", session.User)
		}
	}
}

// A setup whose task is still running when the shutdown begins is left to
// its record, and the shutdown sends it no cleanup. When that setup ends
// done after the shutdown began, it opens no session. It sends the user's
// cleanup without waiting for the task, keeps the record for the sweep,
// and fails with an error that wraps ErrBrokerShuttingDown.
func TestASetupThatEndsAfterShutdownBeganOpensNoSession(t *testing.T) {
	// The setup and the cleanup both start processing. The setup ends when
	// the test says, and the cleanup never does.
	server, director, store := newLateSetupAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer {
		return boshtest.SSHAnswer{State: boshtest.TaskProcessing}
	})

	type opened struct {
		session *sshconn.Session
		err     error
	}

	result := make(chan opened, 1)

	go func() {
		session, err := openSSHSession(t.Context(), director)
		result <- opened{session, err}
	}()

	waitUntil(t, "the setup reaches the director", func() bool { return len(server.CallsOf(boshtest.SSHSetup)) == 1 })

	setup := onlyCall(t, server, boshtest.SSHSetup)

	shutdownSSH(t, director)

	if cleanups := server.CallsOf(boshtest.SSHCleanup); len(cleanups) != 0 {
		t.Fatalf("the shutdown sent %d cleanups for a setup still in flight, want none", len(cleanups))
	}

	if _, kept := store.find(t, setup.User()); !kept {
		t.Fatalf("the record of SSH user %s is gone, want it kept", setup.User())
	}

	server.SetState(setup.TaskID, boshtest.TaskDone)

	var got opened

	select {
	case got = <-result:
	case <-time.After(sshCallLimit):
		t.Fatal("the setup that was in flight at shutdown never ended")
	}

	if got.session != nil {
		t.Errorf("the setup that ended after the shutdown began opened a session for %s, want none", got.session.User)
	}

	assertLateSetupRefused(t, server, store, setup.User(), got.err)
}

// newLateSetupAdapter starts an SSH director that answers with answer,
// and an adapter against it that keeps its cleanup records in an
// in-process Vault.
func newLateSetupAdapter(t *testing.T, answer func(boshtest.SSHCall) boshtest.SSHAnswer) (*boshtest.SSHDirector, bosh.Director, *cleanupStore) {
	t.Helper()

	server, director, _ := newSSHAdapter(t, answer)

	owner, ok := director.(sshCleanupOwner)
	if !ok {
		t.Fatalf("the adapter (%T) takes no cleanup store", director)
	}

	store := newCleanupStore(t)
	owner.SetSSHCleanupStore(store)

	return server, director, store
}

// runLateSetup opens a session in the background with open, runs the SSH
// shutdown once the setup reaches the director, then ends the setup task
// done. It returns the setup's user and what open returned, and fails the
// test when open has not returned within limit of the task ending.
func runLateSetup(t *testing.T, server *boshtest.SSHDirector, director bosh.Director, limit time.Duration,
	open func() (*sshconn.Session, error),
) (string, openedSession) {
	t.Helper()

	result := make(chan openedSession, 1)

	go func() {
		session, err := open()
		result <- openedSession{session: session, err: err}
	}()

	waitUntil(t, "the setup reaches the director", func() bool { return len(server.CallsOf(boshtest.SSHSetup)) == 1 })

	setup := onlyCall(t, server, boshtest.SSHSetup)

	shutdownSSH(t, director)

	server.SetState(setup.TaskID, boshtest.TaskDone)

	select {
	case got := <-result:
		return setup.User(), got
	case <-time.After(limit):
		t.Fatalf("the setup that ended after the shutdown began had not returned %v after its task ended", limit)

		return "", openedSession{}
	}
}

// assertLateSetupRefused asserts that the setup of user, which ended done
// after the shutdown began, failed with an error that wraps
// ErrBrokerShuttingDown and names the user, that it sent the user's one
// cleanup without reading its task, and that the user's record stays.
func assertLateSetupRefused(t *testing.T, server *boshtest.SSHDirector, store *cleanupStore, user string, err error) {
	t.Helper()

	if !errors.Is(err, bosh.ErrBrokerShuttingDown) {
		t.Fatalf("the setup that ended after the shutdown began returned %v, want an error that wraps ErrBrokerShuttingDown", err)
	}

	if !strings.Contains(err.Error(), user) {
		t.Errorf("the error does not name SSH user %s: %v", user, err)
	}

	cleanup := onlyCall(t, server, boshtest.SSHCleanup)
	if cleanup.User() != sshconn.UserRegex(user) {
		t.Errorf("the cleanup names %q, want %q", cleanup.User(), sshconn.UserRegex(user))
	}

	if reads := server.Reads(cleanup.TaskID); reads != 0 {
		t.Errorf("cleanup task %d was read %d time(s), want it never waited for", cleanup.TaskID, reads)
	}

	if _, kept := store.find(t, user); !kept {
		t.Errorf("the record of SSH user %s is gone, want it kept for the sweep", user)
	}
}

// Once the shutdown begins, a setup sends nothing, writes no record, and
// says the broker is shutting down.
func TestSSHSetupIsRefusedOnceShutdownBegins(t *testing.T) {
	server, director, store := newShutdownAdapter(t, nil)

	shutdownSSH(t, director)

	//nolint:paralleltest // the subtests share the closed adapter
	for name, call := range map[string]func() error{
		"SSHSession": func() error {
			_, err := openSSHSession(t.Context(), director)

			return err
		},
		"SSHCommand": func() error {
			_, err := director.SSHCommand(t.Context(), sshDeployment, sshInstance, sshIndex, "uptime", nil, nil)

			return err //nolint:wrapcheck // the test asserts on the adapter's own error
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := callWithin(t, sshCallLimit, call)
			if !errors.Is(err, bosh.ErrBrokerShuttingDown) {
				t.Fatalf("%s after the shutdown began returned %v, want an error that wraps ErrBrokerShuttingDown", name, err)
			}
		})
	}

	if calls := server.Calls(); len(calls) != 0 {
		t.Errorf("the director got %d SSH requests after the shutdown began, want none: %+v", len(calls), calls)
	}

	if records := store.records(t); len(records) != 0 {
		t.Errorf("the store holds %d records, want none: %+v", len(records), records)
	}
}

// A session whose cleanup the shutdown sent sends no second one when it
// closes, as the terminal's sessions do when the SSH service closes.
func TestSessionCloseAfterShutdownSendsNoSecondCleanup(t *testing.T) {
	server, director, _ := newShutdownAdapter(t, nil)

	session := mustOpenSSHSession(t, director)

	shutdownSSH(t, director)

	waitUntil(t, "the shutdown's cleanup arrives", func() bool { return len(server.CallsOf(boshtest.SSHCleanup)) == 1 })

	err := callWithin(t, sshCallLimit, func() error { return session.Cleanup(t.Context()) })
	if err != nil {
		t.Fatalf("Cleanup after the shutdown sent it: %v", err)
	}

	cleanup := onlyCall(t, server, boshtest.SSHCleanup)
	if cleanup.User() != sshconn.UserRegex(session.User) {
		t.Errorf("the cleanup names %q, want %q", cleanup.User(), sshconn.UserRegex(session.User))
	}
}

// firstSweptUser sorts ahead of every user a setup makes, so the sweep
// handles its record first.
const firstSweptUser = "bosh_000000000000000"

// A sweep pass already under way when the shutdown begins claims no user
// after that, so it sends no second cleanup for a user whose cleanup the
// shutdown just sent. The director holds the sweep on its first record
// while the shutdown runs.
func TestASweepUnderWayStopsOnceShutdownBegins(t *testing.T) {
	held := make(chan struct{})
	release := make(chan struct{})

	server, director, store := newShutdownAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		if call.User() == sshconn.UserRegex(firstSweptUser) {
			close(held)
			<-release
		}

		return boshtest.SSHAnswer{}
	})

	session := mustOpenSSHSession(t, director)
	store.leaveUser(t, firstSweptUser, 2, time.Now().Add(-time.Hour))

	sweeper, ok := director.(sshSweeper)
	if !ok {
		t.Fatalf("the adapter (%T) does not sweep its cleanup records", director)
	}

	swept := make(chan error, 1)

	go func() { swept <- sweeper.SweepSSHCleanups(t.Context()) }()

	select {
	case <-held:
	case <-time.After(sshCallLimit):
		close(release)
		t.Fatal("the sweep never sent the cleanup of its first record")
	}

	shutdownSSH(t, director)
	close(release)

	var err error

	select {
	case err = <-swept:
	case <-time.After(sshCallLimit):
		t.Fatal("the sweep never returned")
	}

	if !errors.Is(err, bosh.ErrBrokerShuttingDown) {
		t.Errorf("the sweep returned %v, want an error that wraps ErrBrokerShuttingDown", err)
	}

	var sessionCleanups int

	for _, cleanup := range server.CallsOf(boshtest.SSHCleanup) {
		if cleanup.User() == sshconn.UserRegex(session.User) {
			sessionCleanups++
		}
	}

	if sessionCleanups != 1 {
		t.Errorf("%d cleanups for SSH user %s, want only the shutdown's", sessionCleanups, session.User)
	}

	if _, kept := store.find(t, session.User); !kept {
		t.Errorf("the record of SSH user %s is gone, want it kept for the next sweep", session.User)
	}
}

// lateCleanupTestBound stands in for the bound on the send of a late
// setup's cleanup.
const lateCleanupTestBound = time.Second

// The cleanup a setup sends when it ends done after the shutdown began
// runs on a context of its own, bounded by lateSSHCleanupTimeout. The
// director holds that cleanup, and the caller's context is cancelled
// while it is held. The held request outlives the caller and ends with
// the bound, and the setup returns within the bound, so neither a caller
// that goes away nor a director that never answers decides how long the
// cleanup is sent for.
func TestALateSetupSendsItsCleanupWithinItsOwnBound(t *testing.T) {
	callerCtx, cancelCaller := context.WithCancel(t.Context())
	defer cancelCaller()

	release := make(chan struct{})

	type held struct {
		arrived, cancelled, ended time.Time
	}

	cleanupHeld := make(chan held, 1)

	server, director, store := newLateSetupAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		if call.Command != boshtest.SSHCleanup {
			return boshtest.SSHAnswer{State: boshtest.TaskProcessing}
		}

		return boshtest.SSHAnswer{Hold: func(ctx context.Context) {
			got := held{arrived: time.Now()}

			cancelCaller()

			got.cancelled = time.Now()

			select {
			case <-ctx.Done():
				got.ended = time.Now()
			case <-release:
			case <-time.After(heldCleanup):
			}

			cleanupHeld <- got
		}}
	})
	// Registered after the director, so the hold ends before the director
	// closes.
	t.Cleanup(func() { close(release) })
	bosh.SetLateSSHCleanupTimeout(director, lateCleanupTestBound)

	// Past the bound and the slack, only a send with no bound is still
	// open, because the director holds the cleanup for heldCleanup.
	user, got := runLateSetup(t, server, director, lateCleanupTestBound+2*shutdownTestSlack, func() (*sshconn.Session, error) {
		return openSSHSession(callerCtx, director)
	})

	request := <-cleanupHeld

	if request.ended.IsZero() {
		t.Fatalf("the held cleanup's request never ended, want it ended by the %v bound", lateCleanupTestBound)
	}

	if outlived := request.ended.Sub(request.cancelled); outlived < lateCleanupTestBound/2 {
		t.Errorf("the held cleanup's request ended %v after the caller's context was cancelled, want it to outlive the caller",
			outlived)
	}

	if open := request.ended.Sub(request.arrived); open > lateCleanupTestBound+shutdownTestSlack {
		t.Errorf("the held cleanup's request stayed open %v, want it ended within %v", open, lateCleanupTestBound+shutdownTestSlack)
	}

	if got.session != nil {
		t.Errorf("the setup that ended after the shutdown began opened a session for %s, want none", got.session.User)
	}

	// The director records a request once its hold ends, which can be
	// just after the setup gave up on it.
	waitUntil(t, "the held cleanup is recorded", func() bool { return len(server.CallsOf(boshtest.SSHCleanup)) == 1 })

	assertLateSetupRefused(t, server, store, user, got.err)
}

// A setup that claimed its user before the shutdown began, and whose
// record was still being written when it began, is not sent. The store
// holds the write while the shutdown closes the gate. The director never
// gets the setup, the record is cleared, the user is released, and the
// setup fails with an error that wraps ErrBrokerShuttingDown.
func TestASetupRecordedAsShutdownBeginsIsNotSent(t *testing.T) {
	server, director, store, logger := newFailedSetupAdapter(t, nil)

	writing := make(chan string, 1)
	release := make(chan struct{})
	releaseWrite := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseWrite)

	store.holdRecords(func(ctx context.Context, cleanup sshconn.PendingCleanup) {
		writing <- cleanup.User

		select {
		case <-release:
		case <-ctx.Done():
		}
	})

	result := make(chan error, 1)

	go func() {
		_, err := openSSHSession(t.Context(), director)
		result <- err
	}()

	var user string

	select {
	case user = <-writing:
	case <-time.After(sshCallLimit):
		t.Fatal("the setup never wrote its record")
	}

	shutdownSSH(t, director)
	releaseWrite()

	var err error

	select {
	case err = <-result:
	case <-time.After(sshCallLimit):
		t.Fatal("the setup whose record was being written at shutdown never returned")
	}

	if !errors.Is(err, bosh.ErrBrokerShuttingDown) {
		t.Fatalf("the setup returned %v, want an error that wraps ErrBrokerShuttingDown", err)
	}

	if !strings.Contains(err.Error(), user) {
		t.Errorf("the error does not name SSH user %s: %v", user, err)
	}

	if calls := server.Calls(); len(calls) != 0 {
		t.Errorf("the director got %d SSH requests after the shutdown began, want none: %+v", len(calls), calls)
	}

	if _, kept := store.find(t, user); kept {
		t.Errorf("the record of SSH user %s stays, want it cleared because nothing was sent", user)
	}

	// A user still live is named again by a second shutdown.
	left := logger.count("INFO", "leaves SSH user "+user)

	shutdownSSH(t, director)

	if again := logger.count("INFO", "leaves SSH user "+user); again != left {
		t.Errorf("SSH user %s is still live after its setup was refused, want it released", user)
	}
}
