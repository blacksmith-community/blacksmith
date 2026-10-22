package ssh

import (
	"bytes"
	"errors"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// loopbackClient returns an SSH client connected over loopback TCP to a
// server that accepts every session and runs nothing on it.
func loopbackClient(t *testing.T) *ssh.Client {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	t.Cleanup(func() { _ = listener.Close() })

	hostKey := vmHostKey(t)

	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(hostKey)

	go func() {
		serverSide, err := listener.Accept()
		if err != nil {
			return
		}

		serverConn, channels, requests, err := ssh.NewServerConn(serverSide, config)
		if err != nil {
			return
		}

		defer func() { _ = serverConn.Close() }()

		go ssh.DiscardRequests(requests)

		for newChannel := range channels {
			_ = newChannel.Reject(ssh.Prohibited, "no sessions")
		}
	}()

	clientSide, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	conn, channels, requests, err := ssh.NewClientConn(clientSide, "loopback", &ssh.ClientConfig{
		User:            "vcap",
		HostKeyCallback: ssh.FixedHostKey(hostKey.PublicKey()),
	})
	if err != nil {
		t.Fatalf("connect over loopback: %v", err)
	}

	client := ssh.NewClient(conn, channels, requests)

	t.Cleanup(func() { _ = client.Close() })

	return client
}

// Once Close has run, opening a session always returns ErrRunnerClosed,
// even though the failed open and the closed runner are both ready.
func TestOpenSessionAfterCloseReturnsErrRunnerClosed(t *testing.T) {
	t.Parallel()

	runner := NewRunner(loopbackClient(t), nil)

	_ = runner.Close()

	for attempt := range 200 {
		session, err := runner.openSession(t.Context(), "cat")
		if !errors.Is(err, ErrRunnerClosed) || session != nil {
			t.Fatalf("attempt %d: openSession returned %v and %v, want ErrRunnerClosed", attempt, session, err)
		}
	}
}

// blockedReader blocks each read until release closes, then fills the
// buffer with 'k'. entered closes when the first read starts.
type blockedReader struct {
	entered chan struct{}
	release chan struct{}
}

func (r *blockedReader) Read(buf []byte) (int, error) {
	close(r.entered)
	<-r.release

	for i := range buf {
		buf[i] = 'k'
	}

	return len(buf), nil
}

// stop does not wait for a read of the caller's reader that is under way,
// and the bytes that read returns once stop has run never reach the
// session.
func TestStdinGateStopDoesNotWaitForARead(t *testing.T) {
	t.Parallel()

	reader := &blockedReader{entered: make(chan struct{}), release: make(chan struct{})}
	gate := &stdinGate{reader: reader}

	type readResult struct {
		n   int
		err error
	}

	buf := make([]byte, 16)
	read := make(chan readResult, 1)

	go func() {
		n, err := gate.Read(buf)
		read <- readResult{n: n, err: err}
	}()

	<-reader.entered

	stopped := make(chan struct{})

	go func() {
		gate.stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(time.Second):
		close(reader.release)
		t.Fatal("stop waited for a read that was under way")
	}

	close(reader.release)

	got := <-read
	if got.n != 0 || !errors.Is(got.err, errStdinStopped) {
		t.Errorf("the read under way returned %d and %v, want 0 and errStdinStopped", got.n, got.err)
	}

	if bytes.Contains(buf, []byte("k")) {
		t.Errorf("the read under way delivered %q after stop", buf)
	}
}
