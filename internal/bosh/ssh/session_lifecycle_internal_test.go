package ssh

import (
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// lifecycleWait bounds each wait in these tests. A session that behaves
// gets there long before it.
const lifecycleWait = 5 * time.Second

// newKeyedDirector returns a director whose sessions log in with key.
func newKeyedDirector(key testKey) *cleanupDirector {
	return &cleanupDirector{signer: key.signer}
}

// recordingLogger keeps every line logged at Error.
type recordingLogger struct {
	mu     sync.Mutex
	errors []string
}

func (l *recordingLogger) Infof(string, ...interface{})  {}
func (l *recordingLogger) Debugf(string, ...interface{}) {}

func (l *recordingLogger) Errorf(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.errors = append(l.errors, fmt.Sprintf(format, args...))
}

func (l *recordingLogger) errorLines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.errors...)
}

// grantingHandler serves a VM that opens every session channel and
// grants only the channel requests it names.
func grantingHandler(config *ssh.ServerConfig, granted ...string) func(net.Conn) {
	return func(conn net.Conn) {
		serverConn, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			return
		}

		defer func() { _ = serverConn.Close() }()

		go ssh.DiscardRequests(requests)

		for newChannel := range channels {
			channel, channelRequests, err := newChannel.Accept()
			if err != nil {
				return
			}

			go func() {
				for request := range channelRequests {
					_ = request.Reply(slices.Contains(granted, request.Type), nil)
				}
			}()

			go func() {
				_, _ = io.Copy(io.Discard, channel)
				_ = channel.Close()
			}()
		}
	}
}

// A Start that fails after it connects closes what it opened itself, so
// the session's Close has nothing left to close and logs no false error
// beside the real one.
func TestCloseAfterAFailedStartLogsNoCloseErrors(t *testing.T) {
	t.Parallel()

	key := newTestKey(t)
	server := startTestVMServer(t, grantingHandler(vmServerConfig(t, key.signer.PublicKey()), "shell"))
	director := newKeyedDirector(key)
	service := newTestService(t, director, server.port(t))
	logger := &recordingLogger{}
	service.logger = logger

	session, err := service.CreateSession(t.Context(), terminalRequest())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	err = session.Start()
	if err == nil {
		t.Fatal("Start succeeded against a VM that refuses the PTY")
	}

	before := len(logger.errorLines())

	err = session.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, line := range logger.errorLines()[before:] {
		if strings.Contains(line, "Failed to close") {
			t.Errorf("Close after a failed Start logged %q", line)
		}
	}

	if got := director.cleanups.Load(); got != 1 {
		t.Errorf("%d cleanups after Close, want 1", got)
	}
}

// A cleanup that panics still leaves the session out of the pool and
// closed, so it never holds a session slot until the broker restarts.
func TestSessionLeavesThePoolWhenItsCleanupPanics(t *testing.T) {
	t.Parallel()

	service := newTestService(t, newKeyedDirector(newTestKey(t)), sshPort)

	session, err := service.CreateSession(t.Context(), terminalRequest())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	impl, ok := session.(*SessionImpl)
	if !ok {
		t.Fatalf("CreateSession returned %T, want *SessionImpl", session)
	}

	impl.cleanupFunc = func() error { panic("cleanup panicked") }

	recovered := make(chan interface{}, 1)

	func() {
		defer func() { recovered <- recover() }()

		_ = session.Close()
	}()

	if <-recovered == nil {
		t.Fatal("Close did not pass on the cleanup's panic")
	}

	if pooled := service.poolIDs(); len(pooled) != 0 {
		t.Errorf("the pool holds %v after a close whose cleanup panicked", pooled)
	}

	if status := session.Status(); status != SessionStatusClosed {
		t.Errorf("status %s after a close whose cleanup panicked, want %s", status, SessionStatusClosed)
	}
}

// gatedHandler reports each connection on accepted, holds it before the
// handshake until release is closed, serves it with handle, and reports
// on served once handle returns, which is when the client has closed it.
func gatedHandler(accepted, served chan<- struct{}, release <-chan struct{}, handle func(net.Conn)) func(net.Conn) {
	return func(conn net.Conn) {
		accepted <- struct{}{}

		<-release

		handle(conn)

		served <- struct{}{}
	}
}

// A close that lands while Start runs, as the service's close does at
// shutdown, wins. Start returns an error, the session stays closed, the
// cleanup runs once, and the connection that Start opened is closed
// rather than left open.
func TestCloseWhileStartRunsEndsTheStart(t *testing.T) {
	t.Parallel()

	key := newTestKey(t)
	accepted := make(chan struct{}, 1)
	served := make(chan struct{}, 1)
	release := make(chan struct{})
	server := startTestVMServer(t, gatedHandler(accepted, served, release, shellHandler(vmServerConfig(t, key.signer.PublicKey()))))

	var releaseOnce sync.Once

	releaseServer := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseServer)

	director := newKeyedDirector(key)
	service := newTestService(t, director, server.port(t))

	session, err := service.CreateSession(t.Context(), terminalRequest())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	started := make(chan error, 1)

	go func() { started <- session.Start() }()

	select {
	case <-accepted:
	case <-time.After(lifecycleWait):
		t.Fatal("Start never reached the VM")
	}

	err = service.Close()
	if err != nil {
		t.Fatalf("service close: %v", err)
	}

	releaseServer()

	select {
	case err := <-started:
		if err == nil {
			t.Fatal("Start succeeded after its session was closed")
		}
	case <-time.After(lifecycleWait):
		t.Fatal("Start had not returned after its session was closed")
	}

	if status := session.Status(); status != SessionStatusClosed {
		t.Errorf("status %s after Start lost the race with Close, want %s", status, SessionStatusClosed)
	}

	if got := director.cleanups.Load(); got != 1 {
		t.Errorf("%d cleanups, want 1", got)
	}

	select {
	case <-served:
	case <-time.After(lifecycleWait):
		t.Error("the connection that Start opened was left open")
	}
}

// handshakeOnlyHandler serves a VM that finishes the SSH handshake and
// then never answers a channel open, as a VM with a hung PAM session
// module does. It reports on served once the client closes the
// connection.
func handshakeOnlyHandler(config *ssh.ServerConfig, served chan<- struct{}) func(net.Conn) {
	return func(conn net.Conn) {
		defer func() { served <- struct{}{} }()

		serverConn, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			return
		}

		defer func() { _ = serverConn.Close() }()

		go ssh.DiscardRequests(requests)

		// Every channel open is taken and left unanswered.
		for range channels {
		}
	}
}

// The session's init timeout bounds all of Start, not only the dial. A
// VM that completes the handshake and then ignores the session request
// still has Start return at the timeout, with the connection closed, and
// the session's Close still removes the user once.
func TestStartEndsAtItsInitTimeoutAfterTheHandshake(t *testing.T) {
	t.Parallel()

	const (
		initTimeout = 300 * time.Millisecond
		slack       = time.Second
	)

	key := newTestKey(t)
	served := make(chan struct{}, 1)
	server := startTestVMServer(t, handshakeOnlyHandler(vmServerConfig(t, key.signer.PublicKey()), served))
	director := newKeyedDirector(key)
	service := newTestService(t, director, server.port(t))
	service.config.SessionInitTimeout = initTimeout

	session, err := service.CreateSession(t.Context(), terminalRequest())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	began := time.Now()
	started := make(chan error, 1)

	go func() { started <- session.Start() }()

	select {
	case err := <-started:
		if err == nil {
			t.Fatal("Start succeeded against a VM that never answers the session request")
		}

		if elapsed := time.Since(began); elapsed < initTimeout {
			t.Errorf("Start returned after %v, before its %v init timeout: %v", elapsed, initTimeout, err)
		}
	case <-time.After(initTimeout + slack):
		t.Fatalf("Start still blocked %v after its %v init timeout", slack, initTimeout)
	}

	select {
	case <-served:
	case <-time.After(lifecycleWait):
		t.Error("the connection to the VM was left open after Start gave up")
	}

	err = session.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := director.cleanups.Load(); got != 1 {
		t.Errorf("%d cleanups after Close, want 1", got)
	}
}
