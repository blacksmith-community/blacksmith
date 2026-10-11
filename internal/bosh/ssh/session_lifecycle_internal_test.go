package ssh

import (
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// newKeyedDirector returns a director whose sessions log in with key.
func newKeyedDirector(key testKey) *cleanupDirector {
	return &cleanupDirector{privateKey: key.pem}
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
