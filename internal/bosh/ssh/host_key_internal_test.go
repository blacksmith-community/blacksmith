package ssh

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/bosh/sshconn"
	"golang.org/x/crypto/ssh"
)

// nobleHostKeys are the RSA, ECDSA, and Ed25519 host keys a noble VM's
// sshd holds. RSA key generation is slow, so they are made once.
//
//nolint:gochecknoglobals // RSA key generation is slow, so the keys are made once
var nobleHostKeys = sync.OnceValues(func() (map[string]ssh.Signer, error) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err //nolint:wrapcheck // a test fixture
	}

	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err //nolint:wrapcheck // a test fixture
	}

	signers := map[string]ssh.Signer{}

	for name, key := range map[string]interface{}{"rsa": rsaKey, "ecdsa": ecdsaKey} {
		signer, err := ssh.NewSignerFromKey(key)
		if err != nil {
			return nil, err //nolint:wrapcheck // a test fixture
		}

		signers[name] = signer
	}

	ed25519Signer, err := sharedVMHostKey()
	if err != nil {
		return nil, err
	}

	signers["ed25519"] = ed25519Signer

	return signers, nil
})

func noblesHostKeys(t *testing.T) map[string]ssh.Signer {
	t.Helper()

	signers, err := nobleHostKeys()
	if err != nil {
		t.Fatalf("generate the VM's host keys: %v", err)
	}

	return signers
}

// The terminal connects to a VM that holds RSA, ECDSA, and Ed25519 host
// keys, whichever of them the director reports.
func TestTerminalAcceptsTheDirectorsHostKey(t *testing.T) {
	t.Parallel()

	keys := noblesHostKeys(t)
	user := newTestKey(t)
	server := startTestVMServer(t, shellHandler(vmServerConfig(t, user.signer.PublicKey(), keys["rsa"], keys["ecdsa"], keys["ed25519"])))

	for name, reported := range keys {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			director := &cleanupDirector{signer: user.signer, hostKey: reported.PublicKey()}
			service := newTestService(t, director, server.port(t))

			session, err := service.CreateSession(t.Context(), terminalRequest())
			if err != nil {
				t.Fatalf("create session: %v", err)
			}

			err = session.Start()
			if err != nil {
				t.Fatalf("Start against a VM that holds the director's %s host key: %v", name, err)
			}

			err = session.Close()
			if err != nil {
				t.Fatalf("Close: %v", err)
			}

			if got := director.cleanups.Load(); got != 1 {
				t.Errorf("%d cleanups after Close, want 1", got)
			}
		})
	}
}

// The terminal refuses a VM whose host key is not the one the director
// reported, as a VM recreated at the same address presents, and its user
// is still removed when the session closes.
func TestTerminalSessionRefusesAMismatchedHostKey(t *testing.T) {
	t.Parallel()

	user := newTestKey(t)
	recreated := newTestKey(t).signer
	server := startTestVMServer(t, shellHandler(vmServerConfig(t, user.signer.PublicKey(), recreated)))
	director := &cleanupDirector{signer: user.signer}
	service := newTestService(t, director, server.port(t))

	session, err := service.CreateSession(t.Context(), terminalRequest())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	err = session.Start()
	if !errors.Is(err, sshconn.ErrHostKeyMismatch) {
		t.Fatalf("Start returned %v, want a host key mismatch", err)
	}

	for _, part := range []string{
		terminalRequest().Deployment, "redis/0",
		ssh.FingerprintSHA256(vmHostKey(t).PublicKey()),
		ssh.FingerprintSHA256(recreated.PublicKey()),
	} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("the error does not say %q: %v", part, err)
		}
	}

	_ = session.Close()

	if got := director.cleanups.Load(); got != 1 {
		t.Errorf("%d cleanups after Close, want 1", got)
	}
}

// The director reports a VM's RSA host key. The terminal refuses a VM that
// offers no RSA key as a mismatch, naming the deployment, the instance,
// and the reported key's fingerprint, and its user is still removed when
// the session closes.
func TestTerminalSessionRefusesAVMThatOffersNoKeyOfTheReportedType(t *testing.T) {
	t.Parallel()

	keys := noblesHostKeys(t)
	user := newTestKey(t)
	server := startTestVMServer(t, shellHandler(vmServerConfig(t, user.signer.PublicKey(), keys["ecdsa"], keys["ed25519"])))
	director := &cleanupDirector{signer: user.signer, hostKey: keys["rsa"].PublicKey()}
	service := newTestService(t, director, server.port(t))

	session, err := service.CreateSession(t.Context(), terminalRequest())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	err = session.Start()
	if !errors.Is(err, sshconn.ErrHostKeyMismatch) {
		t.Fatalf("Start returned %v, want a host key mismatch", err)
	}

	for _, part := range []string{
		terminalRequest().Deployment, "redis/0",
		ssh.FingerprintSHA256(keys["rsa"].PublicKey()),
		"recreated",
	} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("the error does not say %q: %v", part, err)
		}
	}

	_ = session.Close()

	if got := director.cleanups.Load(); got != 1 {
		t.Errorf("%d cleanups after Close, want 1", got)
	}
}
