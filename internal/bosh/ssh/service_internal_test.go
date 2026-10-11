package ssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blacksmith/internal/bosh/sshconn"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"golang.org/x/crypto/ssh"
)

const (
	testVMUser    = "bosh_0123456789abcde"
	testVMAddress = "127.0.0.1"
)

var (
	errTestKeyRefused     = errors.New("test server refuses every key")
	errTestCleanupRefused = errors.New("director refused the cleanup")
)

// testVMServer is an in-process SSH server on a random loopback port. It
// stands in for a VM's sshd. Close stops the listener, closes every
// connection, and waits for every goroutine it started.
type testVMServer struct {
	listener net.Listener
	wg       sync.WaitGroup

	mu    sync.Mutex
	conns []net.Conn
}

func startTestVMServer(t *testing.T, handle func(conn net.Conn)) *testVMServer {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	server := &testVMServer{listener: listener}

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

				handle(conn)
			}()
		}
	}()

	t.Cleanup(server.Close)

	return server
}

func (s *testVMServer) Close() {
	_ = s.listener.Close()

	s.mu.Lock()
	for _, conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()

	s.wg.Wait()
}

func (s *testVMServer) port(t *testing.T) string {
	t.Helper()

	_, port, err := net.SplitHostPort(s.listener.Addr().String())
	if err != nil {
		t.Fatalf("split %s: %v", s.listener.Addr(), err)
	}

	return port
}

// testKey is an ed25519 key, as the director adapter hands the terminal
// in its session.
type testKey struct {
	signer ssh.Signer
}

func newTestKey(t *testing.T) testKey {
	t.Helper()

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}

	return testKey{signer: signer}
}

// sharedVMHostKey is the host key the test VMs present unless a test
// gives them others, and the one cleanupDirector reports.
//
//nolint:gochecknoglobals // one host key, made once, that the test VMs present
var sharedVMHostKey = sync.OnceValues(func() (ssh.Signer, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err //nolint:wrapcheck // a test fixture
	}

	return ssh.NewSignerFromKey(private) //nolint:wrapcheck // a test fixture
})

func vmHostKey(t *testing.T) ssh.Signer {
	t.Helper()

	signer, err := sharedVMHostKey()
	if err != nil {
		t.Fatalf("generate the VM's host key: %v", err)
	}

	return signer
}

// vmServerConfig accepts only the user key it is given, or refuses every
// key when accept is nil. It presents the host keys it is given, or the
// shared one that cleanupDirector reports.
func vmServerConfig(t *testing.T, accept ssh.PublicKey, hostKeys ...ssh.Signer) *ssh.ServerConfig {
	t.Helper()

	if len(hostKeys) == 0 {
		hostKeys = []ssh.Signer{vmHostKey(t)}
	}

	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if accept != nil && bytes.Equal(key.Marshal(), accept.Marshal()) {
				return &ssh.Permissions{}, nil
			}

			return nil, errTestKeyRefused
		},
	}

	for _, hostKey := range hostKeys {
		config.AddHostKey(hostKey)
	}

	return config
}

// shellHandler serves a VM that grants a PTY and a shell on every session
// channel and keeps the channel open until the client closes it.
func shellHandler(config *ssh.ServerConfig) func(net.Conn) {
	return func(conn net.Conn) {
		serverConn, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			return
		}

		defer func() { _ = serverConn.Close() }()

		go ssh.DiscardRequests(requests)

		for newChannel := range channels {
			if newChannel.ChannelType() != "session" {
				_ = newChannel.Reject(ssh.UnknownChannelType, "only session channels")

				continue
			}

			channel, channelRequests, err := newChannel.Accept()
			if err != nil {
				return
			}

			go func() {
				for request := range channelRequests {
					switch request.Type {
					case "pty-req", "shell", "window-change", "env":
						_ = request.Reply(true, nil)
					default:
						_ = request.Reply(false, nil)
					}
				}
			}()

			go func() {
				_, _ = io.Copy(io.Discard, channel)
				_ = channel.Close()
			}()
		}
	}
}

// silentHandler accepts the TCP connection and never speaks, so a client
// handshake on it waits until something ends it.
func silentHandler(conn net.Conn) {
	_, _ = io.Copy(io.Discard, conn)
}

// cleanupDirector stands in for the director adapter. Every SSHSession
// answer is a *sshconn.Session whose cleanup counts its calls. Its host
// reports hostKey, or the shared VM host key when hostKey is nil.
type cleanupDirector struct {
	signer     ssh.Signer
	hostKey    ssh.PublicKey
	cleanupErr error
	setups     atomic.Int32
	cleanups   atomic.Int32
}

func (d *cleanupDirector) SSHCommand(context.Context, string, string, int, string, []string, map[string]interface{}) (string, error) {
	return "", nil
}

func (d *cleanupDirector) SSHSession(_ context.Context, deployment, instance string, index int, _ map[string]interface{}) (interface{}, error) {
	d.setups.Add(1)

	hostKey := d.hostKey
	if hostKey == nil {
		signer, err := sharedVMHostKey()
		if err != nil {
			return nil, err //nolint:wrapcheck // a test fixture
		}

		hostKey = signer.PublicKey()
	}

	return &sshconn.Session{
		Deployment: deployment,
		Target:     boshapi.SSHTarget{Job: instance, Indices: []int{index}},
		User:       testVMUser,
		Hosts: []sshconn.Host{{
			Address: testVMAddress, Username: testVMUser, HostKey: hostKey, Job: instance, IndexOrID: strconv.Itoa(index),
		}},
		Signer: d.signer,
		Cleanup: func(context.Context) error {
			d.cleanups.Add(1)

			return d.cleanupErr
		},
	}, nil
}

func newTestService(t *testing.T, director DirectorInterface, port string) *ServiceImpl {
	t.Helper()

	service := NewSSHService(director, Config{
		MaxConcurrent:      10,
		SessionInitTimeout: 5 * time.Second,
		KnownHostsFile:     filepath.Join(t.TempDir(), "known_hosts"),
	}, nil)
	service.port = port

	return service
}

func terminalRequest() *SSHRequest {
	return &SSHRequest{Deployment: "redis-1", Instance: "redis", Index: 0}
}

// Every terminal session removes its user when it closes, whether it
// never started, failed to start, or connected.
func TestSessionCloseRunsTheDirectorCleanup(t *testing.T) {
	t.Parallel()

	key := newTestKey(t)

	cases := map[string]struct {
		start   bool
		handler func(net.Conn)
		wantErr bool
	}{
		"never started":         {start: false, handler: silentHandler},
		"start failed":          {start: true, handler: shellHandler(vmServerConfig(t, nil)), wantErr: true},
		"connected and started": {start: true, handler: shellHandler(vmServerConfig(t, key.signer.PublicKey()))},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			server := startTestVMServer(t, testCase.handler)
			director := &cleanupDirector{signer: key.signer}
			service := newTestService(t, director, server.port(t))

			session, err := service.CreateSession(t.Context(), terminalRequest())
			if err != nil {
				t.Fatalf("create session: %v", err)
			}

			if testCase.start {
				err = session.Start()
				if testCase.wantErr && err == nil {
					t.Fatal("Start succeeded against a server that refuses the key")
				}

				if !testCase.wantErr && err != nil {
					t.Fatalf("Start: %v", err)
				}
			}

			if got := director.cleanups.Load(); got != 0 {
				t.Fatalf("%d cleanups before Close, want 0", got)
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

// Closing a session twice removes its user once.
func TestSessionCloseRunsTheCleanupOnce(t *testing.T) {
	t.Parallel()

	director := &cleanupDirector{signer: newTestKey(t).signer}
	service := newTestService(t, director, sshPort)

	session, err := service.CreateSession(t.Context(), terminalRequest())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	for range 2 {
		_ = session.Close()
	}

	err = service.Close()
	if err != nil {
		t.Fatalf("close service: %v", err)
	}

	if got := director.cleanups.Load(); got != 1 {
		t.Errorf("%d cleanups after two closes and a service close, want 1", got)
	}
}

// Close reports a cleanup that failed, because that user stays on the VM.
func TestSessionCloseReportsAFailedCleanup(t *testing.T) {
	t.Parallel()

	director := &cleanupDirector{signer: newTestKey(t).signer, cleanupErr: errTestCleanupRefused}
	service := newTestService(t, director, sshPort)

	session, err := service.CreateSession(t.Context(), terminalRequest())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	err = session.Close()
	if !errors.Is(err, errTestCleanupRefused) {
		t.Errorf("Close returned %v, want the cleanup's error", err)
	}
}

// A session whose Start fails is closed by the executor, so its user is
// removed rather than left behind.
func TestExecutorClosesASessionWhoseStartFailed(t *testing.T) {
	t.Parallel()

	server := startTestVMServer(t, shellHandler(vmServerConfig(t, nil)))
	director := &cleanupDirector{signer: newTestKey(t).signer}
	service := newTestService(t, director, server.port(t))
	executor := NewSSHExecutor(service, Config{Timeout: 5 * time.Second}, nil)

	_, err := executor.ExecuteWithStream(t.Context(), &SSHRequest{
		Deployment: "redis-1", Instance: "redis", Index: 0, Command: "true",
	}, io.Discard)
	if err == nil {
		t.Fatal("ExecuteWithStream succeeded against a server that refuses the key")
	}

	if got := director.setups.Load(); got != 1 {
		t.Fatalf("%d setups, want 1", got)
	}

	if got := director.cleanups.Load(); got != 1 {
		t.Errorf("%d cleanups after a failed Start, want 1", got)
	}
}

func (s *ServiceImpl) poolIDs() []string {
	s.sessionMutex.RLock()
	defer s.sessionMutex.RUnlock()

	ids := make([]string, 0, len(s.sessionPool))
	for id := range s.sessionPool {
		ids = append(ids, id)
	}

	return ids
}

// A closed session leaves the pool, so the limit counts open sessions
// rather than every session since boot.
func TestElevenSessionsInSequenceAllOpen(t *testing.T) {
	t.Parallel()

	director := &cleanupDirector{signer: newTestKey(t).signer}
	service := newTestService(t, director, sshPort)

	for index := range 11 {
		request := terminalRequest()
		request.Index = index

		session, err := service.CreateSession(t.Context(), request)
		if err != nil {
			t.Fatalf("session %d of 11: %v", index+1, err)
		}

		err = session.Close()
		if err != nil {
			t.Fatalf("close session %d: %v", index+1, err)
		}
	}

	if ids := service.poolIDs(); len(ids) != 0 {
		t.Errorf("pool holds %v after every session closed, want it empty", ids)
	}

	if got := director.cleanups.Load(); got != 11 {
		t.Errorf("%d cleanups, want 11", got)
	}
}

// The service's close closes every open session, and each session's own
// removal from the pool must not deadlock with it.
func TestServiceCloseWithOpenSessionsReturns(t *testing.T) {
	t.Parallel()

	director := &cleanupDirector{signer: newTestKey(t).signer}
	service := newTestService(t, director, sshPort)

	for range 3 {
		_, err := service.CreateSession(t.Context(), terminalRequest())
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
	}

	closed := make(chan error, 1)

	go func() { closed <- service.Close() }()

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close service: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the service's close did not return within 2s")
	}

	if got := director.cleanups.Load(); got != 3 {
		t.Errorf("%d cleanups, want 3", got)
	}

	if ids := service.poolIDs(); len(ids) != 0 {
		t.Errorf("pool holds %v after the service closed, want it empty", ids)
	}
}

// Two sessions on one instance in the same second are two pool entries,
// so the service's close reaches both.
func TestTwoSessionsOnOneInstanceGetDistinctIDs(t *testing.T) {
	t.Parallel()

	director := &cleanupDirector{signer: newTestKey(t).signer}
	service := newTestService(t, director, sshPort)

	for range 2 {
		_, err := service.CreateSession(t.Context(), terminalRequest())
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
	}

	if ids := service.poolIDs(); len(ids) != 2 {
		t.Fatalf("pool holds %v after two sessions on one instance, want two entries", ids)
	}

	err := service.Close()
	if err != nil {
		t.Fatalf("close service: %v", err)
	}

	if got := director.cleanups.Load(); got != 2 {
		t.Errorf("%d cleanups after the service closed, want 2", got)
	}
}

// The terminal's dial ends at the session's init timeout, even against a
// VM that accepts the connection and never speaks.
func TestTerminalSessionDialEndsWithItsContext(t *testing.T) {
	t.Parallel()

	const (
		initTimeout = 300 * time.Millisecond
		slack       = time.Second
	)

	server := startTestVMServer(t, silentHandler)
	director := &cleanupDirector{signer: newTestKey(t).signer}
	service := newTestService(t, director, server.port(t))
	service.config.SessionInitTimeout = initTimeout

	session, err := service.CreateSession(t.Context(), terminalRequest())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	type result struct {
		err     error
		elapsed time.Duration
	}

	results := make(chan result, 1)
	started := time.Now()

	go func() {
		err := session.Start()
		results <- result{err, time.Since(started)}
	}()

	var res result

	select {
	case res = <-results:
	case <-time.After(initTimeout + slack):
		// Release the blocked dial so its goroutine does not outlive the test.
		server.Close()

		<-results

		t.Fatalf("Start still blocked %v after its %v init timeout", slack, initTimeout)
	}

	if res.err == nil {
		t.Fatal("Start succeeded against a VM that never speaks")
	}

	if res.elapsed < initTimeout {
		t.Errorf("Start failed after %v, before its %v init timeout: %v", res.elapsed, initTimeout, res.err)
	}

	_ = session.Close()

	if got := director.cleanups.Load(); got != 1 {
		t.Errorf("%d cleanups after Close, want 1", got)
	}
}

// fixedDirector answers every SSHSession with the same value.
type fixedDirector struct {
	info interface{}
}

func (d *fixedDirector) SSHCommand(context.Context, string, string, int, string, []string, map[string]interface{}) (string, error) {
	return "", nil
}

func (d *fixedDirector) SSHSession(context.Context, string, string, int, map[string]interface{}) (interface{}, error) {
	return d.info, nil
}

// The terminal refuses a director answer it cannot connect with. A setup
// that created a user still has that user removed.
func TestTerminalRefusesASessionItCannotUse(t *testing.T) {
	t.Parallel()

	signer := newTestKey(t).signer

	cases := map[string]struct {
		info     func(cleanups *atomic.Int32) interface{}
		want     error
		cleanups int32
	}{
		"not a session": {
			info: func(*atomic.Int32) interface{} { return map[string]interface{}{"private_key": "x"} },
			want: ErrUnexpectedSessionInfo,
		},
		"nil session": {
			info: func(*atomic.Int32) interface{} { return (*sshconn.Session)(nil) },
			want: ErrUnexpectedSessionInfo,
		},
		"no host": {
			info: func(cleanups *atomic.Int32) interface{} {
				return &sshconn.Session{User: testVMUser, Signer: signer, Cleanup: countCleanup(cleanups)}
			},
			want:     ErrUnusableSessionInfo,
			cleanups: 1,
		},
		"no key": {
			info: func(cleanups *atomic.Int32) interface{} {
				return &sshconn.Session{
					User:    testVMUser,
					Hosts:   []sshconn.Host{{Address: "127.0.0.1", Username: testVMUser, HostKey: signer.PublicKey()}},
					Cleanup: countCleanup(cleanups),
				}
			},
			want:     ErrUnusableSessionInfo,
			cleanups: 1,
		},
		"no host key": {
			info: func(cleanups *atomic.Int32) interface{} {
				return &sshconn.Session{
					User:    testVMUser,
					Hosts:   []sshconn.Host{{Address: "127.0.0.1", Username: testVMUser}},
					Signer:  signer,
					Cleanup: countCleanup(cleanups),
				}
			},
			want:     ErrUnusableSessionInfo,
			cleanups: 1,
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var cleanups atomic.Int32

			service := newTestService(t, &fixedDirector{info: testCase.info(&cleanups)}, sshPort)

			session, err := service.CreateSession(t.Context(), terminalRequest())
			if !errors.Is(err, testCase.want) {
				t.Fatalf("CreateSession returned %v and %v, want an error that wraps %v", session, err, testCase.want)
			}

			if got := cleanups.Load(); got != testCase.cleanups {
				t.Errorf("%d cleanups, want %d", got, testCase.cleanups)
			}

			if pooled := service.poolIDs(); len(pooled) != 0 {
				t.Errorf("the pool holds %v after a refused session", pooled)
			}
		})
	}
}

func countCleanup(cleanups *atomic.Int32) func(context.Context) error {
	return func(context.Context) error {
		cleanups.Add(1)

		return nil
	}
}
