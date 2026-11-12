package bosh_test

import (
	"context"
	"sync"
	"testing"

	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/bosh/sshconn"
)

// taskIDs records what a session's task observer hears.
type taskIDs struct {
	mu      sync.Mutex
	setup   []int
	cleanup []int
}

// observe returns ctx carrying an observer that records into ids.
func (ids *taskIDs) observe(ctx context.Context) context.Context {
	return sshconn.WithTaskObserver(ctx, sshconn.TaskObserver{
		Setup: func(id int) {
			ids.mu.Lock()
			ids.setup = append(ids.setup, id)
			ids.mu.Unlock()
		},
		Cleanup: func(id int) {
			ids.mu.Lock()
			ids.cleanup = append(ids.cleanup, id)
			ids.mu.Unlock()
		},
	})
}

// heard returns copies of what the observer heard so far.
func (ids *taskIDs) heard() ([]int, []int) {
	ids.mu.Lock()
	defer ids.mu.Unlock()

	return append([]int(nil), ids.setup...), append([]int(nil), ids.cleanup...)
}

// checkHeardOnly fails the test unless the observer heard exactly the one
// setup task and the one cleanup task given. A zero cleanup means none.
func checkHeardOnly(t *testing.T, ids *taskIDs, setup, cleanup int) {
	t.Helper()

	setups, cleanups := ids.heard()
	if len(setups) != 1 || setups[0] != setup {
		t.Errorf("the observer heard setup tasks %v, want [%d]", setups, setup)
	}

	switch {
	case cleanup == 0 && len(cleanups) != 0:
		t.Errorf("the observer heard cleanup tasks %v, want none", cleanups)
	case cleanup != 0 && (len(cleanups) != 1 || cleanups[0] != cleanup):
		t.Errorf("the observer heard cleanup tasks %v, want [%d]", cleanups, cleanup)
	}
}

// The observer hears the setup task while the task still runs, so a
// caller can save its ID before the wait for it ends. The session's
// cleanup, sent on a context that carries no observer, still reaches the
// observer the session was opened with.
func TestSSHSessionTellsItsObserverTheSetupBeforeTheWaitEnds(t *testing.T) {
	t.Parallel()

	ids := &taskIDs{}

	var server *boshtest.SSHDirector

	server, director, _ := newSSHAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		if call.Command != boshtest.SSHSetup {
			return boshtest.SSHAnswer{}
		}

		// The setup task runs until a read finds the observer has heard
		// it, so the session opens only once the setup was observed
		// while the adapter was still waiting on the task.
		return boshtest.SSHAnswer{State: boshtest.TaskProcessing, OnRead: func() {
			if setups, _ := ids.heard(); len(setups) == 1 {
				server.SetState(setups[0], boshtest.TaskDone)
			}
		}}
	})

	var session *sshconn.Session

	err := callWithin(t, sshCallLimit, func() error {
		var err error

		session, err = openSSHSession(ids.observe(t.Context()), director)

		return err
	})
	if err != nil {
		t.Fatalf("SSHSession: %v", err)
	}

	setup := onlyCall(t, server, boshtest.SSHSetup)
	checkHeardOnly(t, ids, setup.TaskID, 0)

	err = callWithin(t, sshCallLimit, func() error { return session.Cleanup(context.Background()) })
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	cleanup := onlyCall(t, server, boshtest.SSHCleanup)
	checkHeardOnly(t, ids, setup.TaskID, cleanup.TaskID)
}

// A setup that fails after the director accepted it is heard, and so is
// the cleanup sent in the background once the setup task has ended.
func TestSSHSetupThatFailsTellsItsObserverBothTasks(t *testing.T) {
	t.Parallel()

	ids := &taskIDs{}

	server, director, _, _ := newFailedSetupAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer {
		return boshtest.SSHAnswer{State: boshtest.TaskError, Result: agentTimedOut}
	})

	_ = failSetup(t, ids.observe(t.Context()), director)

	setup := onlyCall(t, server, boshtest.SSHSetup)
	awaitCleanup(t, server, setup)

	waitUntil(t, "the observer hears the cleanup", func() bool {
		_, cleanups := ids.heard()

		return len(cleanups) > 0
	})

	checkHeardOnly(t, ids, setup.TaskID, onlyCall(t, server, boshtest.SSHCleanup).TaskID)
}

// The cleanup the SSH shutdown sends for an open session reaches the
// observer the session was opened with, and a later close of the session
// sends and reports nothing more.
func TestSSHShutdownTellsTheSessionsObserverItsCleanup(t *testing.T) {
	t.Parallel()

	ids := &taskIDs{}

	server, director, _ := newShutdownAdapter(t, nil)

	var session *sshconn.Session

	err := callWithin(t, sshCallLimit, func() error {
		var err error

		session, err = openSSHSession(ids.observe(t.Context()), director)

		return err
	})
	if err != nil {
		t.Fatalf("SSHSession: %v", err)
	}

	shutdownSSH(t, director)

	waitUntil(t, "the observer hears the shutdown's cleanup", func() bool {
		_, cleanups := ids.heard()

		return len(cleanups) > 0
	})

	_ = callWithin(t, sshCallLimit, func() error { return session.Cleanup(context.Background()) })

	checkHeardOnly(t, ids, onlyCall(t, server, boshtest.SSHSetup).TaskID, onlyCall(t, server, boshtest.SSHCleanup).TaskID)
}
