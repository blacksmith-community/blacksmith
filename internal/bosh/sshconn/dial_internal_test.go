package sshconn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// dialSlack is how long past the context deadline a dial may take to return.
const dialSlack = time.Second

// testSSHServer is an in-process listener on a random loopback port. Every
// accepted connection is handed to handle, and Close stops the listener,
// closes every connection, and waits for all goroutines to finish.
type testSSHServer struct {
	listener net.Listener
	wg       sync.WaitGroup

	mu    sync.Mutex
	conns []net.Conn
}

func startTestSSHServer(t *testing.T, handle func(s *testSSHServer, conn net.Conn)) *testSSHServer {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := &testSSHServer{listener: listener}

	server.wg.Add(1)

	go func() {
		defer server.wg.Done()

		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			server.mu.Lock()
			server.conns = append(server.conns, conn)
			server.mu.Unlock()

			server.wg.Add(1)

			go func() {
				defer server.wg.Done()

				handle(server, conn)
			}()
		}
	}()

	t.Cleanup(server.Close)

	return server
}

func (s *testSSHServer) Addr() string { return s.listener.Addr().String() }

func (s *testSSHServer) Close() {
	_ = s.listener.Close()

	s.mu.Lock()
	for _, conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()

	s.wg.Wait()
}

// testHostSigner is the host key every in-process server here presents,
// so every test client can pin it.
//
//nolint:gochecknoglobals // one host key, made once, that every test server presents
var testHostSigner = sync.OnceValues(func() (ssh.Signer, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err //nolint:wrapcheck // a test fixture
	}

	return ssh.NewSignerFromKey(key) //nolint:wrapcheck // a test fixture
})

func hostSigner(t *testing.T) ssh.Signer {
	t.Helper()

	signer, err := testHostSigner()
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}

	return signer
}

func testHostConfig(t *testing.T) *ssh.ServerConfig {
	t.Helper()

	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(hostSigner(t))

	return config
}

// testClientConfig logs in as vcap and pins the test servers' host key.
func testClientConfig(t *testing.T) *ssh.ClientConfig {
	t.Helper()

	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("client signer: %v", err)
	}

	config := PinnedConfig("vcap", signer, hostSigner(t).PublicKey())
	config.Timeout = 5 * time.Second

	return config
}

// silentHandler accepts the TCP connection and never writes a byte.
func silentHandler(s *testSSHServer, conn net.Conn) {
	_, _ = io.Copy(io.Discard, conn)
}

// execHandler is an SSH server that answers every exec request with output.
func execHandler(config *ssh.ServerConfig, output string) func(*testSSHServer, net.Conn) {
	return func(_ *testSSHServer, conn net.Conn) {
		serverConn, chans, reqs, err := ssh.NewServerConn(conn, config)
		if err != nil {
			return
		}
		defer serverConn.Close()

		go ssh.DiscardRequests(reqs)

		for newChannel := range chans {
			if newChannel.ChannelType() != "session" {
				_ = newChannel.Reject(ssh.UnknownChannelType, "session only")

				continue
			}

			channel, requests, err := newChannel.Accept()
			if err != nil {
				return
			}

			go func() {
				defer channel.Close()

				for req := range requests {
					if req.Type != "exec" {
						_ = req.Reply(false, nil)

						continue
					}

					_ = req.Reply(true, nil)
					_, _ = io.WriteString(channel, output)

					status := make([]byte, 4)
					binary.BigEndian.PutUint32(status, 0)
					_, _ = channel.SendRequest("exit-status", false, status)

					return
				}
			}()
		}
	}
}

// dialDirectClient is DialDirect returning a Client, and a nil Client
// when the dial fails, so a caller's nil check holds.
//
//nolint:ireturn // The dial tests share one helper typed by Client.
func dialDirectClient(ctx context.Context, addr string, config *ssh.ClientConfig) (Client, error) {
	client, err := DialDirect(ctx, addr, config)
	if err != nil {
		return nil, err
	}

	return client, nil
}

// runCommand runs command in a new session on client and returns its
// combined output.
func runCommand(client Client, command string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("new session: %w", err)
	}

	defer func() { _ = session.Close() }()

	output, err := session.CombinedOutput(command)
	if err != nil {
		return string(output), fmt.Errorf("run %q: %w", command, err)
	}

	return string(output), nil
}

// requireDialEndsWithContext runs dial and fails unless it returns an error
// within dialSlack of the context deadline.
func requireDialEndsWithContext(t *testing.T, server *testSSHServer, timeout time.Duration, dial func(ctx context.Context) (Client, error)) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	deadline, _ := ctx.Deadline()

	type result struct {
		client   Client
		err      error
		returned time.Time
	}

	results := make(chan result, 1)

	go func() {
		client, err := dial(ctx)
		results <- result{client, err, time.Now()}
	}()

	select {
	case res := <-results:
		if res.client != nil {
			_ = res.client.Close()
		}

		if res.err == nil {
			t.Fatal("dial succeeded against a server that never answers")
		}

		// Compare against the deadline, not ctx.Err(): the connection deadline
		// and the context timer fire together, and the error can arrive
		// before the context records that it ended.
		if res.returned.Before(deadline) {
			t.Fatalf("dial failed %s before the context deadline: %v", deadline.Sub(res.returned), res.err)
		}
	case <-time.After(timeout + dialSlack):
		// Release the blocked dial so its goroutine does not outlive the test.
		server.Close()

		res := <-results
		if res.client != nil {
			_ = res.client.Close()
		}

		t.Fatalf("dial still blocked %s after the %s context deadline", dialSlack, timeout)
	}
}

func TestSSHDialEndsWithTheContext(t *testing.T) {
	t.Parallel()

	server := startTestSSHServer(t, silentHandler)
	config := testClientConfig(t)

	requireDialEndsWithContext(t, server, 300*time.Millisecond, func(ctx context.Context) (Client, error) {
		return dialDirectClient(ctx, server.Addr(), config)
	})
}

// scriptedConn is a net.Conn for driving handshakeSSH directly. When readErr
// is set the connection fails every read and swallows every write, as a peer
// that resets the connection would. onClose runs inside the first Close.
type scriptedConn struct {
	net.Conn

	readErr error
	onClose func()
	closes  atomic.Int32
}

func newScriptedConn(t *testing.T) *scriptedConn {
	t.Helper()

	local, remote := net.Pipe()

	t.Cleanup(func() {
		_ = local.Close()
		_ = remote.Close()
	})

	// Nothing is ever sent to the connection, so a read waits for a close.
	return &scriptedConn{Conn: local}
}

func (c *scriptedConn) Read(p []byte) (int, error) {
	if c.readErr != nil {
		return 0, c.readErr
	}

	return c.Conn.Read(p)
}

func (c *scriptedConn) Write(p []byte) (int, error) {
	if c.readErr != nil {
		return len(p), nil
	}

	return c.Conn.Write(p)
}

func (c *scriptedConn) Close() error {
	c.closes.Add(1)

	if c.onClose != nil {
		c.onClose()
	}

	return c.Conn.Close()
}

func TestHandshakeSSHClosesTheConnectionOnce(t *testing.T) {
	t.Parallel()

	t.Run("when the handshake fails", func(t *testing.T) {
		t.Parallel()

		conn := newScriptedConn(t)
		conn.readErr = errors.New("connection reset by peer")

		client, err := handshakeSSH(context.Background(), conn, "198.51.100.1:22", testClientConfig(t))
		if err == nil || client != nil {
			t.Fatalf("handshakeSSH = %v, %v, want an error", client, err)
		}

		if got := conn.closes.Load(); got != 1 {
			t.Fatalf("connection closed %d times, want 1", got)
		}
	})

	t.Run("when the context ends during the handshake", func(t *testing.T) {
		t.Parallel()

		conn := newScriptedConn(t)

		ctx, cancel := context.WithCancelCause(context.Background())
		cause := errors.New("shutting down")

		time.AfterFunc(50*time.Millisecond, func() { cancel(cause) })

		_, err := handshakeSSH(ctx, conn, "198.51.100.1:22", testClientConfig(t))
		if !errors.Is(err, cause) {
			t.Fatalf("handshakeSSH error = %v, want the context's cause", err)
		}

		if got := conn.closes.Load(); got != 1 {
			t.Fatalf("connection closed %d times, want 1", got)
		}
	})

	t.Run("when the handshake succeeds the client closes it once", func(t *testing.T) {
		t.Parallel()

		server := startTestSSHServer(t, execHandler(testHostConfig(t), ""))

		raw, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", server.Addr())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}

		conn := &scriptedConn{Conn: raw}

		client, err := handshakeSSH(context.Background(), conn, server.Addr(), testClientConfig(t))
		if err != nil {
			t.Fatalf("handshakeSSH: %v", err)
		}

		if got := conn.closes.Load(); got != 0 {
			t.Fatalf("connection closed %d times during a successful handshake, want 0", got)
		}

		_ = client.Close()
		_ = client.Close()

		if got := conn.closes.Load(); got != 1 {
			t.Fatalf("connection closed %d times after the client closed, want 1", got)
		}
	})
}

func TestHandshakeSSHReportsItsOwnErrorWhenTheContextEndsAfterwards(t *testing.T) {
	t.Parallel()

	handshakeErr := errors.New("connection reset by peer")
	conn := newScriptedConn(t)
	conn.readErr = handshakeErr

	ctx, cancel := context.WithCancelCause(context.Background())
	// The SSH library closes the connection once the handshake has failed.
	// Ending the context at that point makes it end before handshakeSSH looks
	// at the result, although the context never closed the connection.
	conn.onClose = func() { cancel(errors.New("context ended after the failure")) }

	_, err := handshakeSSH(ctx, conn, "198.51.100.1:22", testClientConfig(t))
	if !errors.Is(err, handshakeErr) {
		t.Fatalf("handshakeSSH error = %v, want the handshake's own error", err)
	}

	if context.Cause(ctx) == nil {
		t.Fatal("test setup: the context did not end")
	}
}

func TestSSHDialIsBoundedByTheTimeoutWithoutAContextDeadline(t *testing.T) {
	t.Parallel()

	const timeout = 300 * time.Millisecond

	server := startTestSSHServer(t, silentHandler)
	config := testClientConfig(t)
	config.Timeout = timeout

	type result struct {
		client  Client
		err     error
		elapsed time.Duration
	}

	results := make(chan result, 1)
	start := time.Now()

	go func() {
		client, err := dialDirectClient(context.Background(), server.Addr(), config)
		results <- result{client, err, time.Since(start)}
	}()

	var res result

	select {
	case res = <-results:
	case <-time.After(5 * timeout):
		// Release the blocked dial so its goroutine does not outlive the test.
		server.Close()

		<-results

		t.Fatalf("dial still blocked after %s, want it bounded by the %s timeout", 5*timeout, timeout)
	}

	if res.client != nil {
		_ = res.client.Close()
	}

	if res.err == nil {
		t.Fatal("dial succeeded against a server that never answers")
	}

	if res.elapsed < timeout {
		t.Fatalf("dial failed after %s, before the %s timeout: %v", res.elapsed, timeout, res.err)
	}
}

func TestSSHSessionOutlivesTheHandshakeTimeout(t *testing.T) {
	t.Parallel()

	const (
		timeout = 200 * time.Millisecond
		output  = "still connected\n"
	)

	server := startTestSSHServer(t, execHandler(testHostConfig(t), output))
	config := testClientConfig(t)
	config.Timeout = timeout

	client, err := DialDirect(context.Background(), server.Addr(), config)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	defer func() { _ = client.Close() }()

	// Run the command after the handshake deadline would have passed.
	time.Sleep(2 * timeout)

	got, err := runCommand(client, "echo hello")
	if err != nil {
		t.Fatalf("command after the handshake timeout: %v", err)
	}

	if got != output {
		t.Fatalf("command output = %q, want %q", got, output)
	}
}
