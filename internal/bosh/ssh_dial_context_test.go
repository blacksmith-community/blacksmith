package bosh_test

import (
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

	// connected is closed when the first connection has been accepted.
	connected chan struct{}
	first     sync.Once
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

	server := &silentSSHServer{port: port, connected: make(chan struct{})}

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
			server.first.Do(func() { close(server.connected) })

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

// The adapter's own dial ends with its context. A VM that accepts the
// connection and never speaks lets the dial go at the caller's deadline
// rather than holding SSHCommand.
func TestSSHClientDialEndsWithItsContext(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}

	_, director, _ := newSSHAdapter(t, nil)
	silent := startSilentSSHServer(t)
	bosh.SetSSHPort(director, silent.port)

	session := &sshconn.Session{
		Deployment: sshDeployment,
		User:       "bosh_0123456789abcde",
		Hosts:      []sshconn.Host{{Address: loopbackHost, Username: "bosh_0123456789abcde", HostKey: signer.PublicKey()}},
		Signer:     signer,
	}

	// The deadline passes once the silent server has accepted the
	// connection, never before, so a slow host cannot spend it on the
	// connect and leave the dial with nothing to wait on.
	expiring, expire := newExpiringContext(t.Context())
	defer expire()

	ctx := datedContext{expiringContext: expiring, deadline: time.Now().Add(callBackstop)}

	done := make(chan error, 1)

	go func() {
		client, err := bosh.DialSSHSession(ctx, director, session)
		if err != nil {
			done <- err //nolint:wrapcheck // the test asserts on the adapter's own error

			return
		}

		_ = client.Close()

		done <- errDialReachedASilentServer
	}()

	select {
	case <-silent.connected:
	case err = <-done:
		t.Fatalf("the dial returned %v before the silent server had a connection", err)
	case <-time.After(callBackstop):
		t.Fatalf("the silent server had no connection %s after the dial started", callBackstop)
	}

	expire()

	select {
	case err = <-done:
	case <-time.After(callBackstop):
		t.Fatalf("the dial had not returned %s after its context expired, so it does not end with its context", callBackstop)
	}

	if err == nil || errors.Is(err, errDialReachedASilentServer) {
		t.Fatalf("the dial returned %v, want a failure at the deadline", err)
	}

	if got := silent.accepted.Load(); got != 1 {
		t.Errorf("the silent server accepted %d connections, want 1", got)
	}
}
