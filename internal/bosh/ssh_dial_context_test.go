package bosh_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/sshconn"
	"golang.org/x/crypto/ssh"
)

// loopbackHost is where the silent server listens.
const loopbackHost = "127.0.0.1"

// errDialReachedASilentServer marks a dial that finished against a server
// that never speaks, which cannot happen.
var errDialReachedASilentServer = errors.New("the dial finished against a server that never speaks")

// silentSSHServer accepts TCP connections on loopback and never writes a
// byte, so an SSH handshake with it waits until something ends it.
type silentSSHServer struct {
	port     string
	accepted atomic.Int32
}

func startSilentSSHServer(t *testing.T) *silentSSHServer {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", net.JoinHostPort(loopbackHost, "0"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split %s: %v", listener.Addr(), err)
	}

	server := &silentSSHServer{port: port}

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

			server.accepted.Add(1)

			connsMu.Lock()

			conns = append(conns, conn)

			connsMu.Unlock()

			running.Go(func() { _, _ = io.Copy(io.Discard, conn) })
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

	return server
}

// The adapter's own dial ends with its context. A VM, or a gateway, that
// accepts the connection and never speaks lets the dial go at the
// caller's deadline rather than holding SSHCommand.
func TestSSHClientDialEndsWithItsContext(t *testing.T) {
	const deadline = 300 * time.Millisecond

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}

	cases := map[string]sshconn.Gateway{
		"direct":            {},
		"through a gateway": {User: "jumpbox", Host: loopbackHost},
	}

	for name, gateway := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BLACKSMITH_TEST_MODE", "true")

			_, director, _ := newSSHAdapter(t, nil)
			silent := startSilentSSHServer(t)
			bosh.SetSSHPort(director, silent.port)

			session := &sshconn.Session{
				Deployment: sshDeployment,
				User:       "bosh_0123456789abcde",
				Hosts:      []sshconn.Host{{Address: loopbackHost, Username: "bosh_0123456789abcde", HostKey: signer.PublicKey()}},
				Signer:     signer,
				Gateway:    gateway,
			}

			ctx, cancel := context.WithTimeout(t.Context(), deadline)
			defer cancel()

			began := time.Now()

			err := callWithin(t, deadline+time.Second, func() error {
				client, err := bosh.DialSSHSession(ctx, director, session)
				if err != nil {
					return err //nolint:wrapcheck // the test asserts on the adapter's own error
				}

				_ = client.Close()

				return errDialReachedASilentServer
			})
			if err == nil || errors.Is(err, errDialReachedASilentServer) {
				t.Fatalf("the dial returned %v, want a failure at the deadline", err)
			}

			if elapsed := time.Since(began); elapsed < deadline {
				t.Errorf("the dial gave up after %v, before its %v deadline: %v", elapsed, deadline, err)
			}

			if got := silent.accepted.Load(); got != 1 {
				t.Errorf("the silent server accepted %d connections, want 1", got)
			}
		})
	}
}
