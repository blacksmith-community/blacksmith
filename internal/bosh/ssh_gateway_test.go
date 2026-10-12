package bosh_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	boshssh "blacksmith/internal/bosh/ssh"
	"blacksmith/internal/bosh/sshconn"
)

const (
	// anyHostKey is a well-formed host key, for setups that are refused
	// before any connection, so the key is never checked against a VM.
	anyHostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDtqJ7zOtqQtYqOo0CpvDXNlMhV3HeJDpjrASKGLWdop root@vm"
	// gatewayUser is the user a director's default_ssh_options name for
	// its gateway.
	gatewayUser = "jumpbox"
	// gatewayCallLimit bounds a call against a director that names a
	// gateway, which the refusal makes return at once.
	gatewayCallLimit = 3 * time.Second
)

// errTerminalOpenedThroughAGateway marks a terminal session that opened on
// a setup that names a gateway.
var errTerminalOpenedThroughAGateway = errors.New("the terminal opened a session on a setup that names a gateway")

// gatewayOutput is a setup result with one success entry for the test
// instance that names the gateway user@host.
func gatewayOutput(user, host string) string {
	entry := boshtest.SSHSuccess(anyHostKey)
	entry.GatewayUser = user
	entry.GatewayHost = host

	return boshtest.SSHSetupOutput(entry)
}

// gatewayAnswer answers each setup with gatewayOutput.
func gatewayAnswer(user, host string) func(boshtest.SSHCall) boshtest.SSHAnswer {
	return func(call boshtest.SSHCall) boshtest.SSHAnswer {
		if call.Command != boshtest.SSHSetup {
			return boshtest.SSHAnswer{}
		}

		return boshtest.SSHAnswer{Output: gatewayOutput(user, host)}
	}
}

// checkGatewayRefusal fails the test unless err is the refusal of the
// gateway jumpbox@127.0.0.1 and says why.
func checkGatewayRefusal(t *testing.T, err error) {
	t.Helper()

	if !errors.Is(err, sshconn.ErrGatewayUnsupported) {
		t.Fatalf("got %v, want an error that wraps ErrGatewayUnsupported", err)
	}

	for _, part := range []string{
		gatewayUser + "@" + loopbackHost, "default_ssh_options",
		"does not support SSH through a gateway", "refuse the throwaway key",
	} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("the error does not say %q: %v", part, err)
		}
	}
}

// A director whose default_ssh_options name a gateway gets its setup
// refused, through SSHCommand and through a terminal session alike. The
// gateway would refuse the throwaway key, so nothing connects to it, and
// the user the setup created is removed once.
func TestSSHRefusesAResultThatNamesAGateway(t *testing.T) {
	t.Run("SSHCommand", func(t *testing.T) {
		server, director, store, _ := newFailedSetupAdapter(t, gatewayAnswer(gatewayUser, loopbackHost))
		gateway := startSilentSSHServer(t)
		bosh.SetSSHPort(director, gateway.port)

		ctx, cancel := context.WithTimeout(t.Context(), gatewayCallLimit)
		defer cancel()

		err := callWithin(t, sshCallLimit, func() error {
			output, err := director.SSHCommand(ctx, sshDeployment, sshInstance, sshIndex, "uptime", nil, nil)
			if err == nil {
				return errSSHCommandRanOnTheWrongVM(output)
			}

			return err //nolint:wrapcheck // the test asserts on the adapter's own error
		})
		checkGatewayRefusal(t, err)

		setup := onlyCall(t, server, boshtest.SSHSetup)
		awaitCleanup(t, server, setup)
		awaitCleared(t, store, setup.User())

		if got := gateway.accepted.Load(); got != 0 {
			t.Errorf("the gateway accepted %d connections, want none", got)
		}
	})

	t.Run("terminal session", func(t *testing.T) {
		server, director, store, _ := newFailedSetupAdapter(t, gatewayAnswer(gatewayUser, loopbackHost))
		gateway := startSilentSSHServer(t)
		bosh.SetSSHPort(director, gateway.port)

		service := boshssh.NewSSHService(director, boshssh.Config{MaxConcurrent: 1, SessionInitTimeout: gatewayCallLimit}, nil)

		ctx, cancel := context.WithTimeout(t.Context(), gatewayCallLimit)
		defer cancel()

		err := callWithin(t, sshCallLimit, func() error {
			session, err := service.CreateSession(ctx, &boshssh.SSHRequest{Deployment: sshDeployment, Instance: sshInstance, Index: sshIndex})
			if err == nil {
				_ = session.Close()

				return errTerminalOpenedThroughAGateway
			}

			return err //nolint:wrapcheck // the test asserts on the service's own error
		})
		checkGatewayRefusal(t, err)

		setup := onlyCall(t, server, boshtest.SSHSetup)
		awaitCleanup(t, server, setup)
		awaitCleared(t, store, setup.User())

		if got := gateway.accepted.Load(); got != 0 {
			t.Errorf("the gateway accepted %d connections, want none", got)
		}
	})
}

// A result that names a gateway user but no gateway host names no
// gateway, so SSHCommand connects to the VM directly.
func TestSSHIgnoresAGatewayUserWithoutAHost(t *testing.T) {
	var hostKey string

	server, director, _ := newSSHAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		if call.Command != boshtest.SSHSetup {
			return boshtest.SSHAnswer{}
		}

		entry := boshtest.SSHSuccess(hostKey)
		entry.GatewayUser = gatewayUser

		return boshtest.SSHAnswer{Output: boshtest.SSHSetupOutput(entry)}
	})
	hostKey = boshtest.AuthorizedKey(server.HostKey.PublicKey())

	vmServer := startExecServer(t, acceptEveryLogin, server.HostKey)
	bosh.SetSSHPort(director, vmServer.port)

	var output string

	err := callWithin(t, sshCallLimit, func() error {
		var err error

		output, err = director.SSHCommand(t.Context(), sshDeployment, sshInstance, sshIndex, "uptime", nil, nil)

		return err //nolint:wrapcheck // the test asserts on the adapter's own error
	})
	if err != nil {
		t.Fatalf("SSHCommand with a gateway user and no gateway host: %v", err)
	}

	if output != "ran: uptime\n" {
		t.Errorf("output %q, want the command's output", output)
	}

	onlyCall(t, server, boshtest.SSHCleanup)
}
