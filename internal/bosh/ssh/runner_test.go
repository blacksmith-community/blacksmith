package ssh_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	boshssh "blacksmith/internal/bosh/ssh"
	"blacksmith/pkg/logger"
	"golang.org/x/crypto/ssh"
)

// execHandle runs one exec request on the test VM. It reads stdin from
// channel, writes stdout to channel and stderr to channel.Stderr(), and
// returns the exit status. stop closes when the test VM shuts down, so a
// handle that never ends on its own can return.
type execHandle func(command string, channel ssh.Channel, stop <-chan struct{}) uint32

// runnerVM is an in-process SSH server on a loopback port that stands in
// for a VM's sshd. It runs every exec request through its handle and
// counts the session channels and exec requests it sees.
type runnerVM struct {
	listener net.Listener
	hostKey  ssh.Signer
	handle   execHandle
	// onExec, when set, runs as each exec request arrives, before the
	// request is answered.
	onExec func()
	config *ssh.ServerConfig
	stop   chan struct{}
	wg     sync.WaitGroup

	channels atomic.Int32
	execs    atomic.Int32

	mu    sync.Mutex
	conns []net.Conn
}

// startRunnerVM starts a test VM that runs every exec request through
// handle. onExec, when given, runs as each exec request arrives, before
// it is answered.
func startRunnerVM(t *testing.T, handle execHandle, onExec ...func()) *runnerVM {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate the host key: %v", err)
	}

	hostKey, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("make the host signer: %v", err)
	}

	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(hostKey)

	server := &runnerVM{listener: listener, hostKey: hostKey, handle: handle, config: config, stop: make(chan struct{})}
	if len(onExec) > 0 {
		server.onExec = onExec[0]
	}

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

				server.serve(conn)
			}()
		}
	}()

	t.Cleanup(server.close)

	return server
}

func (s *runnerVM) close() {
	close(s.stop)
	_ = s.listener.Close()

	s.mu.Lock()
	for _, conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()

	s.wg.Wait()
}

func (s *runnerVM) serve(conn net.Conn) {
	serverConn, channels, requests, err := ssh.NewServerConn(conn, s.config)
	if err != nil {
		return
	}

	defer func() { _ = serverConn.Close() }()

	go ssh.DiscardRequests(requests)

	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "session only")

			continue
		}

		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			return
		}

		s.channels.Add(1)
		s.wg.Add(1)

		go func() {
			defer s.wg.Done()

			s.serveChannel(channel, channelRequests)
		}()
	}
}

func (s *runnerVM) serveChannel(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer func() { _ = channel.Close() }()

	for request := range requests {
		if request.Type != "exec" || len(request.Payload) < 4 {
			_ = request.Reply(false, nil)

			continue
		}

		s.execs.Add(1)

		if s.onExec != nil {
			s.onExec()
		}

		_ = request.Reply(true, nil)

		// Keep answering the channel's other requests while the command
		// runs, as sshd does.
		go func() {
			for request := range requests {
				_ = request.Reply(false, nil)
			}
		}()

		code := s.handle(string(request.Payload[4:]), channel, s.stop)

		status := make([]byte, 4)
		binary.BigEndian.PutUint32(status, code)
		_, _ = channel.SendRequest("exit-status", false, status)

		return
	}
}

// dial connects to the test VM as vcap, pinning its host key.
func (s *runnerVM) dial(t *testing.T) *ssh.Client {
	t.Helper()

	return s.dialVia(t, s.listener.Addr().String())
}

// dialVia connects to the test VM through addr, such as a proxy's, as
// vcap, pinning the VM's host key.
func (s *runnerVM) dialVia(t *testing.T, addr string) *ssh.Client {
	t.Helper()

	config := &ssh.ClientConfig{
		User:            "vcap",
		HostKeyCallback: ssh.FixedHostKey(s.hostKey.PublicKey()),
		Timeout:         5 * time.Second,
	}

	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		t.Fatalf("dial the test VM: %v", err)
	}

	t.Cleanup(func() { _ = client.Close() })

	return client
}

// runnerLog records every line a runner logs, with its level.
type runnerLog struct {
	logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func (l *runnerLog) Debugf(format string, args ...interface{}) { l.recordf("DEBUG", format, args...) }
func (l *runnerLog) Infof(format string, args ...interface{})  { l.recordf("INFO", format, args...) }
func (l *runnerLog) Warnf(format string, args ...interface{})  { l.recordf("WARN", format, args...) }
func (l *runnerLog) Warningf(format string, args ...interface{}) {
	l.recordf("WARN", format, args...)
}
func (l *runnerLog) Errorf(format string, args ...interface{}) { l.recordf("ERROR", format, args...) }
func (l *runnerLog) Fatalf(format string, args ...interface{}) { l.recordf("FATAL", format, args...) }
func (l *runnerLog) Debug(msg string, args ...interface{})     { l.recordf("DEBUG", msg, args...) }
func (l *runnerLog) Info(msg string, args ...interface{})      { l.recordf("INFO", msg, args...) }
func (l *runnerLog) Warn(msg string, args ...interface{})      { l.recordf("WARN", msg, args...) }
func (l *runnerLog) Warning(msg string, args ...interface{})   { l.recordf("WARN", msg, args...) }
func (l *runnerLog) Error(msg string, args ...interface{})     { l.recordf("ERROR", msg, args...) }
func (l *runnerLog) Fatal(msg string, args ...interface{})     { l.recordf("FATAL", msg, args...) }

//nolint:ireturn // the logger interface returns itself
func (l *runnerLog) WithField(string, interface{}) logger.Logger { return l }

//nolint:ireturn // the logger interface returns itself
func (l *runnerLog) WithFields(map[string]interface{}) logger.Logger { return l }

//nolint:ireturn // the logger interface returns itself
func (l *runnerLog) Named(string) logger.Logger { return l }

func (l *runnerLog) recordf(level, format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, level+" "+fmt.Sprintf(format, args...))
}

func (l *runnerLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.lines...)
}

// The command reads its stdin, and its stdout, stderr, and exit status
// come back apart.
func TestRunnerSendsStdinAndSplitsStreams(t *testing.T) {
	t.Parallel()

	var command atomic.Value

	fakeVM := startRunnerVM(t, func(cmd string, channel ssh.Channel, _ <-chan struct{}) uint32 {
		command.Store(cmd)

		_, _ = io.Copy(channel, channel)
		_, _ = io.WriteString(channel.Stderr(), "warn")

		return 3
	})

	runner := boshssh.NewRunner(fakeVM.dial(t), &runnerLog{})
	input := "state=new\nhash=" + testHash + "\n"

	result, err := runner.Run(t.Context(), "sudo -n /bin/bash -s --", []string{"stage", "it is"}, strings.NewReader(input), 0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if string(result.Stdout) != input || string(result.Stderr) != "warn" || result.ExitCode != 3 || result.Truncated {
		t.Errorf("Run returned stdout %q, stderr %q, exit %d, truncated %v; want the input, \"warn\", 3, false",
			result.Stdout, result.Stderr, result.ExitCode, result.Truncated)
	}

	if got, want := command.Load(), `sudo -n /bin/bash -s -- 'stage' 'it is'`; got != want {
		t.Errorf("the VM ran %q, want %q", got, want)
	}
}

// A stream longer than the limit is cut at the limit, the rest is read
// and dropped so the command can finish, and the result says it was cut.
func TestRunnerBoundsOutput(t *testing.T) {
	t.Parallel()

	fakeVM := startRunnerVM(t, func(_ string, channel ssh.Channel, _ <-chan struct{}) uint32 {
		_, _ = channel.Write(bytes.Repeat([]byte("x"), 2<<20))

		return 0
	})

	runner := boshssh.NewRunner(fakeVM.dial(t), &runnerLog{})

	result, err := runner.Run(t.Context(), catProgram, nil, nil, 1<<20)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(result.Stdout) != 1<<20 || !result.Truncated || result.ExitCode != 0 {
		t.Errorf("Run returned %d bytes of stdout, truncated %v, exit %d; want %d bytes, true, 0",
			len(result.Stdout), result.Truncated, result.ExitCode, 1<<20)
	}
}

// Key and certificate content travels only on stdin, so an argument or
// program that carries it is refused before any session is opened.
func TestRunnerRefusesContentInArgs(t *testing.T) {
	t.Parallel()

	fakeVM := startRunnerVM(t, func(string, ssh.Channel, <-chan struct{}) uint32 { return 0 })
	log := &runnerLog{}
	runner := boshssh.NewRunner(fakeVM.dial(t), log)

	cases := map[string]struct {
		program string
		args    []string
	}{
		"a certificate header": {catProgram, []string{"-----BEGIN CERTIFICATE-----"}},
		"a key word":           {catProgram, []string{"x", "RSA PRIVATE"}},
		"a newline":            {catProgram, []string{"a\nb"}},
		"a NUL":                {catProgram, []string{"a\x00b"}},
		"a newline in program": {"cat\nrm", nil},
	}

	for name, testCase := range cases {
		_, err := runner.Run(t.Context(), testCase.program, testCase.args, nil, 0)
		if !errors.Is(err, boshssh.ErrContentInArgs) {
			t.Errorf("%s: Run returned %v, want boshssh.ErrContentInArgs", name, err)
		}

		if err != nil && (strings.Contains(err.Error(), "BEGIN") || strings.Contains(err.Error(), "PRIVATE")) {
			t.Errorf("%s: the error %q repeats the argument", name, err)
		}
	}

	if channels, execs := fakeVM.channels.Load(), fakeVM.execs.Load(); channels != 0 || execs != 0 {
		t.Errorf("the VM saw %d sessions and %d exec requests, want none", channels, execs)
	}

	for _, line := range log.all() {
		if strings.Contains(line, "BEGIN") || strings.Contains(line, "PRIVATE") {
			t.Errorf("the runner logged %q", line)
		}
	}
}

// catProgram is the program the runner tests send.
const catProgram = "cat"

// longBase64Run matches 40 or more base64 characters in a row.
var longBase64Run = regexp.MustCompile(`[A-Za-z0-9+/]{40,}`)

// A key that a command prints never reaches a log line, even when the
// output limit cuts it so that neither marker survives in one stream.
func TestRunnerLogsNoKeyMaterial(t *testing.T) {
	t.Parallel()

	keyBlock := "-----BEGIN PRIVATE KEY-----\n" + testKeyBody + "\n" + testKeyBody + "\n-----END PRIVATE KEY-----\n"
	filler := strings.Repeat(".", 100)

	fakeVM := startRunnerVM(t, func(_ string, channel ssh.Channel, _ <-chan struct{}) uint32 {
		// stdout is cut inside the header. stderr starts inside the body
		// and is cut before the footer, so it holds no marker at all.
		_, _ = io.WriteString(channel, filler+keyBlock)
		_, _ = io.WriteString(channel.Stderr(), testKeyBody+"\n"+testKeyBody+"\n-----END PRIVATE KEY-----\n")

		return 1
	})

	log := &runnerLog{}
	runner := boshssh.NewRunner(fakeVM.dial(t), log)

	result, err := runner.Run(t.Context(), catProgram, []string{"/var/vcap/data/certpatch/x"}, nil, 108)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.ExitCode != 1 || !result.Truncated {
		t.Errorf("Run returned exit %d and truncated %v, want 1 and true", result.ExitCode, result.Truncated)
	}

	if !strings.HasSuffix(string(result.Stdout), "-----BEG") {
		t.Fatalf("stdout %q is not cut inside the header, so the test does not test the cut", result.Stdout)
	}

	if bytes.Contains(result.Stderr, []byte("-----")) || len(result.Stderr) != 108 {
		t.Fatalf("stderr %q is not cut inside the body, so the test does not test the cut", result.Stderr)
	}

	lines := log.all()
	if len(lines) == 0 {
		t.Fatal("the runner logged nothing, so the test does not test its log lines")
	}

	for _, line := range lines {
		if !strings.HasPrefix(line, "DEBUG ") {
			t.Errorf("the runner logged %q above debug level", line)
		}

		if strings.Contains(line, "PRIVATE") || strings.Contains(line, "BEGIN") || longBase64Run.MatchString(line) {
			t.Errorf("the runner logged key material: %q", line)
		}
	}
}

// A command that never ends is stopped at the context's deadline.
func TestRunnerDeadline(t *testing.T) {
	t.Parallel()

	fakeVM := startRunnerVM(t, func(_ string, _ ssh.Channel, stop <-chan struct{}) uint32 {
		<-stop

		return 0
	})

	runner := boshssh.NewRunner(fakeVM.dial(t), &runnerLog{})

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	started := time.Now()

	_, err := runner.Run(ctx, "sleep", []string{"forever"}, strings.NewReader("input"), 0)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Run returned %v, want context.DeadlineExceeded", err)
	}

	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("Run returned after %s, want within 1s", elapsed)
	}
}

// Close closes the shared connection, and a run after it fails.
func TestRunnerClose(t *testing.T) {
	t.Parallel()

	fakeVM := startRunnerVM(t, func(string, ssh.Channel, <-chan struct{}) uint32 { return 0 })
	runner := boshssh.NewRunner(fakeVM.dial(t), nil)

	_, err := runner.Run(t.Context(), "true", nil, nil, 0)
	if err != nil {
		t.Fatalf("Run before Close: %v", err)
	}

	err = runner.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	err = runner.Close()
	if err != nil {
		t.Errorf("a second Close returned %v, want nil", err)
	}

	_, err = runner.Run(t.Context(), "true", nil, nil, 0)
	if !errors.Is(err, boshssh.ErrRunnerClosed) {
		t.Errorf("Run after Close returned %v, want ErrRunnerClosed", err)
	}
}
