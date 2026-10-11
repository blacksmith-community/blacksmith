package sshconn_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh/sshconn"
	"golang.org/x/crypto/ssh"
)

// pinTestTimeout bounds each connection in the pinning tests.
const pinTestTimeout = 5 * time.Second

// loopbackHost is the address every test VM listens on.
const loopbackHost = "127.0.0.1"

// rsaHostKeyBits is the size of the RSA host key, which is what a
// stemcell's sshd generates.
const rsaHostKeyBits = 2048

// vmHostKeys are the three host keys a noble VM's sshd holds. RSA key
// generation is slow, so the keys are made once for the package.
type vmHostKeys struct {
	rsa, ecdsa, ed25519 ssh.Signer
}

//nolint:gochecknoglobals // RSA key generation is slow, so the keys are made once
var sharedVMHostKeys = sync.OnceValues(func() (vmHostKeys, error) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, rsaHostKeyBits)
	if err != nil {
		return vmHostKeys{}, err //nolint:wrapcheck // a test fixture
	}

	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return vmHostKeys{}, err //nolint:wrapcheck // a test fixture
	}

	_, ed25519Key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return vmHostKeys{}, err //nolint:wrapcheck // a test fixture
	}

	var keys vmHostKeys

	keys.rsa, err = ssh.NewSignerFromKey(rsaKey)
	if err != nil {
		return vmHostKeys{}, err //nolint:wrapcheck // a test fixture
	}

	keys.ecdsa, err = ssh.NewSignerFromKey(ecdsaKey)
	if err != nil {
		return vmHostKeys{}, err //nolint:wrapcheck // a test fixture
	}

	keys.ed25519, err = ssh.NewSignerFromKey(ed25519Key)
	if err != nil {
		return vmHostKeys{}, err //nolint:wrapcheck // a test fixture
	}

	return keys, nil
})

func hostKeys(t *testing.T) vmHostKeys {
	t.Helper()

	keys, err := sharedVMHostKeys()
	if err != nil {
		t.Fatalf("generate the VM's host keys: %v", err)
	}

	return keys
}

func newUserSigner(t *testing.T) ssh.Signer {
	t.Helper()

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("user signer: %v", err)
	}

	return signer
}

// startMultiKeyVM serves SSH on a loopback port with every host key it is
// given, as a VM's sshd does, and accepts any login. It returns the
// server's address.
func startMultiKeyVM(t *testing.T, hostSigners ...ssh.Signer) string {
	t.Helper()

	config := &ssh.ServerConfig{NoClientAuth: true}
	for _, signer := range hostSigners {
		config.AddHostKey(signer)
	}

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	var (
		running sync.WaitGroup
		connsMu sync.Mutex
		conns   []net.Conn
	)

	running.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			connsMu.Lock()

			conns = append(conns, conn)

			connsMu.Unlock()

			running.Go(func() {
				serverConn, channels, requests, err := ssh.NewServerConn(conn, config)
				if err != nil {
					return
				}

				defer func() { _ = serverConn.Close() }()

				go ssh.DiscardRequests(requests)

				for channel := range channels {
					_ = channel.Reject(ssh.Prohibited, "the pinning tests open no channels")
				}
			})
		}
	})

	t.Cleanup(func() {
		_ = listener.Close()

		connsMu.Lock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		connsMu.Unlock()

		running.Wait()
	})

	return listener.Addr().String()
}

func dialPinned(t *testing.T, addr string, config *ssh.ClientConfig) error {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), pinTestTimeout)
	defer cancel()

	client, err := sshconn.DialDirect(ctx, addr, config)
	if err != nil {
		return err //nolint:wrapcheck // the test asserts on the dial's own error
	}

	_ = client.Close()

	return nil
}

// A VM's sshd holds RSA, ECDSA, and Ed25519 host keys, and the director
// reports one of them. The pinned configuration asks the VM for that key,
// whichever it is, so the connection succeeds. The control shows that a
// client pinned to the RSA key without narrowing its algorithms is
// steered to another key and refuses the VM.
func TestSSHPinsAnRSAKeyOnAServerThatAlsoOffersECDSAAndEd25519(t *testing.T) {
	t.Parallel()

	keys := hostKeys(t)
	addr := startMultiKeyVM(t, keys.rsa, keys.ecdsa, keys.ed25519)
	user := newUserSigner(t)

	for name, pinned := range map[string]ssh.Signer{
		"rsa":     keys.rsa,
		"ecdsa":   keys.ecdsa,
		"ed25519": keys.ed25519,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			config := sshconn.PinnedConfig(hostsTestUser, user, pinned.PublicKey())
			config.Timeout = pinTestTimeout

			err := dialPinned(t, addr, config)
			if err != nil {
				t.Fatalf("a client pinned to the VM's %s host key was refused: %v", name, err)
			}
		})
	}

	t.Run("control: the default algorithms reach another key", func(t *testing.T) {
		t.Parallel()

		config := sshconn.PinnedConfig(hostsTestUser, user, keys.rsa.PublicKey())
		config.HostKeyAlgorithms = nil
		config.Timeout = pinTestTimeout

		err := dialPinned(t, addr, config)
		if !errors.Is(err, sshconn.ErrHostKeyMismatch) {
			t.Fatalf("a client pinned to the RSA key with the default algorithms got %v, want a host key mismatch, "+
				"which would show that the server steers it to another key", err)
		}
	})
}

// The pinned configuration offers only the algorithms the pinned key
// signs with. An RSA key gets the two SHA-2 algorithms and never the SHA-1
// ssh-rsa one.
func TestPinnedConfigOffersOnlyThePinnedKeysAlgorithms(t *testing.T) {
	t.Parallel()

	keys := hostKeys(t)
	user := newUserSigner(t)

	for name, testCase := range map[string]struct {
		key  ssh.PublicKey
		want []string
	}{
		"rsa":     {keys.rsa.PublicKey(), []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}},
		"ecdsa":   {keys.ecdsa.PublicKey(), []string{ssh.KeyAlgoECDSA256}},
		"ed25519": {keys.ed25519.PublicKey(), []string{ssh.KeyAlgoED25519}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			config := sshconn.PinnedConfig(hostsTestUser, user, testCase.key)

			if !slices.Equal(config.HostKeyAlgorithms, testCase.want) {
				t.Errorf("HostKeyAlgorithms %v, want %v", config.HostKeyAlgorithms, testCase.want)
			}

			if config.User != hostsTestUser {
				t.Errorf("User %q, want %q", config.User, hostsTestUser)
			}

			if config.HostKeyCallback == nil {
				t.Fatal("the pinned configuration has no host key callback")
			}

			err := config.HostKeyCallback("10.0.0.5:22", nil, testCase.key)
			if err != nil {
				t.Errorf("the callback refused the pinned key: %v", err)
			}
		})
	}
}

// A VM that presents a key other than the director's, as a VM recreated
// at the same address does, is refused. The error names the deployment,
// the instance, the host, and both fingerprints, and says what the
// mismatch may mean.
func TestHostConfigRefusesAKeyThatDoesNotMatchTheDirector(t *testing.T) {
	t.Parallel()

	keys := hostKeys(t)
	recreated := newUserSigner(t)
	addr := startMultiKeyVM(t, recreated)

	host := sshconn.Host{
		Address:   loopbackHost,
		Username:  hostsTestUser,
		HostKey:   keys.ed25519.PublicKey(),
		Job:       hostsTestJob,
		IndexOrID: firstHostID,
	}

	config, err := sshconn.HostConfig(hostsTestDeployment, host, newUserSigner(t))
	if err != nil {
		t.Fatalf("HostConfig: %v", err)
	}

	config.Timeout = pinTestTimeout

	err = dialPinned(t, addr, config)

	var mismatch *sshconn.HostKeyMismatchError
	if !errors.As(err, &mismatch) || !errors.Is(err, sshconn.ErrHostKeyMismatch) {
		t.Fatalf("got %v, want a HostKeyMismatchError", err)
	}

	for _, part := range []string{
		hostsTestDeployment, firstHostInstance, addr,
		ssh.FingerprintSHA256(keys.ed25519.PublicKey()),
		ssh.FingerprintSHA256(recreated.PublicKey()),
		"recreated", "intercepting",
	} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("the error does not say %q: %v", part, err)
		}
	}
}

// A host with no key to pin gets no configuration, because it could only
// be reached without checking whom it talks to.
func TestHostConfigRefusesAHostWithoutAKey(t *testing.T) {
	t.Parallel()

	_, err := sshconn.HostConfig(hostsTestDeployment, sshconn.Host{Address: loopbackHost, Username: hostsTestUser, Job: hostsTestJob, IndexOrID: firstHostID},
		newUserSigner(t))
	if !errors.Is(err, sshconn.ErrHostKeyUnusable) {
		t.Fatalf("got %v, want an error that wraps ErrHostKeyUnusable", err)
	}

	for _, part := range []string{hostsTestDeployment, firstHostInstance} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("the error does not say %q: %v", part, err)
		}
	}
}

// The director reports a VM's RSA host key, so a VM that offers no RSA key
// at all, as one recreated onto a stemcell without one does, or as
// something intercepting the connection with another key does, ends the
// handshake in algorithm negotiation before the host key callback runs.
// That refusal is still a host key mismatch, and it names the deployment,
// the instance, the host, the reported key's fingerprint, and the key
// types the VM offered. A VM that holds an RSA key but offers it only with
// the SHA-1 algorithm is refused too, but not as a mismatch, because its
// key may well be the director's.
func TestHostConfigRefusesAVMThatOffersNoKeyOfTheReportedType(t *testing.T) {
	t.Parallel()

	keys := hostKeys(t)

	sha1Only, err := ssh.NewSignerWithAlgorithms(keys.rsa.(ssh.AlgorithmSigner), []string{ssh.KeyAlgoRSA}) //nolint:forcetypeassert // an RSA signer from NewSignerFromKey is an AlgorithmSigner
	if err != nil {
		t.Fatalf("restrict the RSA host key to SHA-1: %v", err)
	}

	host := sshconn.Host{
		Address:   loopbackHost,
		Username:  hostsTestUser,
		HostKey:   keys.rsa.PublicKey(),
		Job:       hostsTestJob,
		IndexOrID: firstHostID,
	}

	dial := func(t *testing.T, hostSigners ...ssh.Signer) (string, error) {
		t.Helper()

		return dialWithoutTheReportedType(t, host, hostSigners...)
	}

	t.Run("an RSA key pinned against a VM with no RSA key", func(t *testing.T) {
		t.Parallel()

		addr, err := dial(t, keys.ecdsa, keys.ed25519)

		var mismatch *sshconn.HostKeyMismatchError
		if !errors.As(err, &mismatch) || !errors.Is(err, sshconn.ErrHostKeyMismatch) {
			t.Fatalf("got %v, want a HostKeyMismatchError", err)
		}

		if !slices.Equal(mismatch.Offered, []string{ssh.KeyAlgoECDSA256, ssh.KeyAlgoED25519}) {
			t.Errorf("Offered %v, want the VM's ECDSA and Ed25519 algorithms", mismatch.Offered)
		}

		for _, part := range []string{
			hostsTestDeployment, firstHostInstance, addr,
			ssh.FingerprintSHA256(keys.rsa.PublicKey()), ssh.KeyAlgoRSA,
			ssh.KeyAlgoECDSA256, ssh.KeyAlgoED25519,
			"recreated", "intercepting",
		} {
			if !strings.Contains(err.Error(), part) {
				t.Errorf("the error does not say %q: %v", part, err)
			}
		}
	})

	t.Run("an RSA key pinned against a VM that offers it with SHA-1 only", func(t *testing.T) {
		t.Parallel()

		_, err := dial(t, sha1Only, keys.ecdsa)

		if errors.Is(err, sshconn.ErrHostKeyMismatch) {
			t.Fatalf("got %v, want the negotiation failure as it is, because the VM holds an RSA key", err)
		}
	})
}

// dialWithoutTheReportedType dials a VM that holds hostSigners with the
// configuration pinned to host's key, asserts that the handshake ended in
// x/crypto's host key negotiation, and returns the VM's address and the
// explained refusal.
func dialWithoutTheReportedType(t *testing.T, host sshconn.Host, hostSigners ...ssh.Signer) (string, error) {
	t.Helper()

	addr := startMultiKeyVM(t, hostSigners...)

	config, err := sshconn.HostConfig(hostsTestDeployment, host, newUserSigner(t))
	if err != nil {
		t.Fatalf("HostConfig: %v", err)
	}

	config.Timeout = pinTestTimeout

	dialErr := dialPinned(t, addr, config)

	var negotiation *ssh.AlgorithmNegotiationError
	if !errors.As(dialErr, &negotiation) {
		t.Fatalf("the dial returned %v, want x/crypto's host key negotiation failure", dialErr)
	}

	return addr, sshconn.ExplainHostKeyRefusal(dialErr, hostsTestDeployment, host, addr) //nolint:wrapcheck // the test asserts on the explained error itself
}

// A failure that is not a host key negotiation, and a host with no key,
// pass through as they are.
func TestExplainHostKeyRefusalLeavesOtherErrorsAlone(t *testing.T) {
	t.Parallel()

	keys := hostKeys(t)
	host := sshconn.Host{Address: loopbackHost, Username: hostsTestUser, HostKey: keys.rsa.PublicKey(), Job: hostsTestJob, IndexOrID: firstHostID}
	cipher := &ssh.AlgorithmNegotiationError{What: "client to server cipher"}
	hostKey := &ssh.AlgorithmNegotiationError{What: "host key", RequestedAlgorithms: []string{ssh.KeyAlgoED25519}}
	keyless := host
	keyless.HostKey = nil

	for name, testCase := range map[string]struct {
		err  error
		host sshconn.Host
	}{
		"no error":           {nil, host},
		"a refused connect":  {net.ErrClosed, host},
		"a cipher mismatch":  {cipher, host},
		"a host with no key": {hostKey, keyless},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := sshconn.ExplainHostKeyRefusal(testCase.err, hostsTestDeployment, testCase.host, net.JoinHostPort(loopbackHost, sshconn.Port))
			if !errors.Is(got, testCase.err) || errors.Is(got, sshconn.ErrHostKeyMismatch) || (testCase.err == nil) != (got == nil) {
				t.Errorf("got %v, want %v as it is", got, testCase.err)
			}
		})
	}
}
