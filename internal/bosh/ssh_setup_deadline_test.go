package bosh_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"golang.org/x/crypto/ssh"
)

// testSetupTimeout stands in for the adapter's bound on the setup wait.
const testSetupTimeout = 100 * time.Millisecond

// A setup task that stays queued under a caller context that never ends,
// as the async command's does, is cancelled at the setup bound, and its
// user is removed once the task is cancelled.
func TestSSHSetupWaitEndsAtItsDeadline(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, director, store, _ := newFailedSetupAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer {
		return boshtest.SSHAnswer{State: boshtest.TaskQueued}
	})
	bosh.SetSSHSetupTimeout(director, testSetupTimeout)

	err := failSetup(t, context.WithoutCancel(t.Context()), director)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("SSHSession = %v, want the setup bound's deadline", err)
	}

	setup := onlyCall(t, server, boshtest.SSHSetup)

	cancels := server.Cancels()
	if len(cancels) != 1 || cancels[0].TaskID != setup.TaskID || cancels[0].State != boshtest.TaskQueued {
		t.Fatalf("cancels %+v, want one for queued setup task %d", cancels, setup.TaskID)
	}

	awaitCleanup(t, server, setup)
	awaitCleared(t, store, setup.User())
}

// The bound covers only the wait for the setup task. A session outlives
// it, and a command whose login takes longer than the bound still runs.
func TestSSHSetupDeadlineDoesNotBoundTheSession(t *testing.T) {
	const bound = 50 * time.Millisecond

	t.Run("a session", func(t *testing.T) {
		t.Setenv("BLACKSMITH_TEST_MODE", "true")

		server, director, _ := newSSHAdapter(t, nil)
		bosh.SetSSHSetupTimeout(director, bound)

		session := mustOpenSSHSession(t, director)

		time.Sleep(4 * bound)

		if calls, cancels := server.CallsOf(boshtest.SSHCleanup), server.Cancels(); len(calls) != 0 || len(cancels) != 0 {
			t.Fatalf("after the setup bound passed, the adapter sent %d cleanups and %d cancels, want none", len(calls), len(cancels))
		}

		err := callWithin(t, sshCallLimit, func() error { return session.Cleanup(t.Context()) })
		if err != nil {
			t.Fatalf("the session's cleanup after the setup bound: %v", err)
		}

		onlyCall(t, server, boshtest.SSHCleanup)
	})

	t.Run("a command", func(t *testing.T) {
		t.Setenv("BLACKSMITH_TEST_MODE", "true")

		server, director, _ := newSSHAdapter(t, nil)
		bosh.SetSSHSetupTimeout(director, bound)

		vmServer := startExecServer(t, func(user string, _ ssh.PublicKey) bool {
			time.Sleep(4 * bound)

			setups := server.CallsOf(boshtest.SSHSetup)

			return len(setups) == 1 && user == setups[0].User()
		}, server.HostKey)
		bosh.SetSSHPort(director, vmServer.port)

		var output string

		err := callWithin(t, sshCallLimit, func() error {
			var err error

			output, err = director.SSHCommand(t.Context(), sshDeployment, sshInstance, sshIndex, "true", nil, nil)

			return err //nolint:wrapcheck // the test asserts on the adapter's own error
		})
		if err != nil {
			t.Fatalf("SSHCommand whose login outlasts the setup bound: %v", err)
		}

		if output != "ran: true\n" {
			t.Errorf("output %q, want the command's output", output)
		}
	})
}
