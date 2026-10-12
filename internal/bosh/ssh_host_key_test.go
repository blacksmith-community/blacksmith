package bosh_test

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/bosh/sshconn"
	"golang.org/x/crypto/ssh"
)

// nobleHostKeys are the RSA, ECDSA, and Ed25519 host keys a noble VM's sshd
// holds. RSA key generation is slow, so they are made once.
//
//nolint:gochecknoglobals // RSA key generation is slow, so the keys are made once
var nobleHostKeys = sync.OnceValues(func() ([]ssh.Signer, error) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err //nolint:wrapcheck // a test fixture
	}

	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err //nolint:wrapcheck // a test fixture
	}

	_, ed25519Key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err //nolint:wrapcheck // a test fixture
	}

	signers := make([]ssh.Signer, 0, 3)

	for _, key := range []interface{}{rsaKey, ecdsaKey, ed25519Key} {
		signer, err := ssh.NewSignerFromKey(key)
		if err != nil {
			return nil, err //nolint:wrapcheck // a test fixture
		}

		signers = append(signers, signer)
	}

	return signers, nil
})

// vmHostKeys returns the RSA, ECDSA, and Ed25519 host keys, in that order.
func vmHostKeys(t *testing.T) []ssh.Signer {
	t.Helper()

	signers, err := nobleHostKeys()
	if err != nil {
		t.Fatalf("generate the VM's host keys: %v", err)
	}

	return signers
}

func acceptEveryLogin(string, ssh.PublicKey) bool { return true }

// newEd25519Signer makes a fresh ed25519 key, which stands in for the
// host key of a VM recreated at the same address.
func newEd25519Signer(t *testing.T) ssh.Signer {
	t.Helper()

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}

	return signer
}

// reportHostKey answers each setup with one success entry that reports
// hostKey, in the director's authorized-keys form, or the text as given.
func reportHostKey(hostKey string) func(boshtest.SSHCall) boshtest.SSHAnswer {
	return func(call boshtest.SSHCall) boshtest.SSHAnswer {
		if call.Command != boshtest.SSHSetup {
			return boshtest.SSHAnswer{}
		}

		return boshtest.SSHAnswer{Output: boshtest.SSHSetupOutput(boshtest.SSHSuccess(hostKey))}
	}
}

// SSHCommand connects to a VM that holds RSA, ECDSA, and Ed25519 host
// keys, whichever of them the director reports. The terminal's half of
// this guarantee is in the terminal's own tests.
func TestSSHAcceptsTheDirectorsHostKey(t *testing.T) {
	keys := vmHostKeys(t)

	for name, reported := range map[string]ssh.Signer{"rsa": keys[0], "ecdsa": keys[1], "ed25519": keys[2]} {
		t.Run(name, func(t *testing.T) {
			server, director, _ := newSSHAdapter(t, reportHostKey(boshtest.AuthorizedKey(reported.PublicKey())))
			vmServer := startExecServer(t, acceptEveryLogin, keys...)
			bosh.SetSSHPort(director, vmServer.port)

			var output string

			err := callWithin(t, sshCallLimit, func() error {
				var err error

				output, err = director.SSHCommand(t.Context(), sshDeployment, sshInstance, sshIndex, "uptime", nil, nil)

				return err //nolint:wrapcheck // the test asserts on the adapter's own error
			})
			if err != nil {
				t.Fatalf("SSHCommand against a VM that holds the director's %s host key: %v", name, err)
			}

			if output != "ran: uptime\n" {
				t.Errorf("output %q, want the command's output", output)
			}

			onlyCall(t, server, boshtest.SSHCleanup)
		})
	}
}

// A VM whose host key is not the one the director reported is refused
// before any login, and the user is still removed. The error names the
// deployment, the instance, the host, and both fingerprints.
func TestSSHRefusesAHostKeyThatDoesNotMatchTheDirector(t *testing.T) {
	server, director, _ := newSSHAdapter(t, nil)
	recreated := newEd25519Signer(t)
	vmServer := startExecServer(t, acceptEveryLogin, recreated)
	bosh.SetSSHPort(director, vmServer.port)

	err := callWithin(t, sshCallLimit, func() error {
		output, err := director.SSHCommand(t.Context(), sshDeployment, sshInstance, sshIndex, "uptime", nil, nil)
		if err == nil {
			return errSSHCommandRanOnTheWrongVM(output)
		}

		return err //nolint:wrapcheck // the test asserts on the adapter's own error
	})
	if !errors.Is(err, sshconn.ErrHostKeyMismatch) {
		t.Fatalf("SSHCommand returned %v, want a host key mismatch", err)
	}

	for _, part := range []string{
		sshDeployment,
		sshInstance + "/" + boshtest.DefaultSSHInstanceID,
		net.JoinHostPort("127.0.0.1", vmServer.port),
		ssh.FingerprintSHA256(server.HostKey.PublicKey()),
		ssh.FingerprintSHA256(recreated.PublicKey()),
	} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("the error does not say %q: %v", part, err)
		}
	}

	if logins := vmServer.logins(); len(logins) != 0 {
		t.Errorf("the VM saw logins %v, want none", logins)
	}

	onlyCall(t, server, boshtest.SSHCleanup)
}

// The director reports a VM's RSA host key. A VM at that address that
// offers no RSA key ends the handshake in algorithm negotiation, and it is
// refused as a mismatch before any login. The error names the deployment,
// the instance, the host, and the reported key's fingerprint, and the
// user is still removed.
func TestSSHRefusesAVMThatOffersNoKeyOfTheReportedType(t *testing.T) {
	keys := vmHostKeys(t)
	server, director, _ := newSSHAdapter(t, reportHostKey(boshtest.AuthorizedKey(keys[0].PublicKey())))
	vmServer := startExecServer(t, acceptEveryLogin, keys[1], keys[2])
	bosh.SetSSHPort(director, vmServer.port)

	err := callWithin(t, sshCallLimit, func() error {
		output, err := director.SSHCommand(t.Context(), sshDeployment, sshInstance, sshIndex, "uptime", nil, nil)
		if err == nil {
			return errSSHCommandRanOnTheWrongVM(output)
		}

		return err //nolint:wrapcheck // the test asserts on the adapter's own error
	})
	if !errors.Is(err, sshconn.ErrHostKeyMismatch) {
		t.Fatalf("SSHCommand returned %v, want a host key mismatch", err)
	}

	for _, part := range []string{
		sshDeployment,
		sshInstance + "/" + boshtest.DefaultSSHInstanceID,
		net.JoinHostPort("127.0.0.1", vmServer.port),
		ssh.FingerprintSHA256(keys[0].PublicKey()),
		"recreated",
	} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("the error does not say %q: %v", part, err)
		}
	}

	if logins := vmServer.logins(); len(logins) != 0 {
		t.Errorf("the VM saw logins %v, want none", logins)
	}

	onlyCall(t, server, boshtest.SSHCleanup)
}

// A setup whose result reports no host key, or one that does not parse,
// is refused, because there is nothing to check the VM against, and the
// user it created is removed once.
func TestSSHRefusesAMissingOrUnparseableHostKey(t *testing.T) {
	for name, testCase := range map[string]struct {
		hostKey string
		says    string
	}{
		"missing":     {hostKey: "", says: "no host key"},
		"unparseable": {hostKey: "ssh-rsa not-base64", says: "does not parse"},
	} {
		t.Run(name, func(t *testing.T) {
			server, director, store, _ := newFailedSetupAdapter(t, reportHostKey(testCase.hostKey))

			err := failSetup(t, t.Context(), director)
			if !errors.Is(err, sshconn.ErrHostKeyUnusable) {
				t.Fatalf("SSHSession returned %v, want an error that wraps ErrHostKeyUnusable", err)
			}

			setup := onlyCall(t, server, boshtest.SSHSetup)

			for _, part := range []string{testCase.says, setup.User(), sshInstance + "/" + boshtest.DefaultSSHInstanceID, sshDeployment} {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("the error does not say %q: %v", part, err)
				}
			}

			awaitCleanup(t, server, setup)
			awaitCleared(t, store, setup.User())
		})
	}
}

// errSSHCommandRanOnTheWrongVM marks a command that ran on a VM whose host
// key was not the director's.
func errSSHCommandRanOnTheWrongVM(output string) error {
	return &wrongVMError{output: output}
}

type wrongVMError struct{ output string }

func (e *wrongVMError) Error() string {
	return "SSHCommand ran on a VM whose host key is not the director's, with output " + e.output
}
