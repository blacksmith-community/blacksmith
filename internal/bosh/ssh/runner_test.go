package ssh_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
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
	// rejectText, when set, makes the VM refuse every session channel
	// with this text as the reason.
	rejectText atomic.Pointer[string]

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

		if reason := s.rejectText.Load(); reason != nil {
			_ = newChannel.Reject(ssh.Prohibited, *reason)

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

// Each stream reports its own truncation and the number of bytes it was
// offered, whether they were kept or dropped.
func TestRunnerReportsTruncationPerStream(t *testing.T) {
	t.Parallel()

	const limit = 100

	cases := []struct {
		name                         string
		stdoutLen, stderrLen         int
		wantStdoutCut, wantStderrCut bool
	}{
		{name: "only stderr over the limit", stdoutLen: 10, stderrLen: 250, wantStderrCut: true},
		{name: "only stdout over the limit", stdoutLen: 250, stderrLen: 10, wantStdoutCut: true},
		{name: "both exactly at the limit", stdoutLen: limit, stderrLen: limit},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fakeVM := startRunnerVM(t, func(_ string, channel ssh.Channel, _ <-chan struct{}) uint32 {
				_, _ = channel.Write(bytes.Repeat([]byte("o"), tc.stdoutLen))
				_, _ = channel.Stderr().Write(bytes.Repeat([]byte("e"), tc.stderrLen))

				return 0
			})

			runner := boshssh.NewRunner(fakeVM.dial(t), &runnerLog{})

			result, err := runner.Run(t.Context(), catProgram, nil, nil, limit)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			if result.StdoutTruncated != tc.wantStdoutCut || result.StderrTruncated != tc.wantStderrCut {
				t.Errorf("StdoutTruncated %v, StderrTruncated %v; want %v, %v",
					result.StdoutTruncated, result.StderrTruncated, tc.wantStdoutCut, tc.wantStderrCut)
			}

			if want := tc.wantStdoutCut || tc.wantStderrCut; result.Truncated != want {
				t.Errorf("Truncated %v, want %v", result.Truncated, want)
			}

			if result.StdoutReceived != int64(tc.stdoutLen) || result.StderrReceived != int64(tc.stderrLen) {
				t.Errorf("received %d stdout bytes and %d stderr bytes; want %d and %d",
					result.StdoutReceived, result.StderrReceived, tc.stdoutLen, tc.stderrLen)
			}
		})
	}
}

// A run the context ends still returns what the command printed first.
func TestRunnerReturnsPartialOutputOnDeadline(t *testing.T) {
	t.Parallel()

	fakeVM := startRunnerVM(t, func(_ string, channel ssh.Channel, stop <-chan struct{}) uint32 {
		_, _ = io.WriteString(channel, "first line\n")

		<-stop

		return 0
	})

	runner := boshssh.NewRunner(fakeVM.dial(t), &runnerLog{})

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	result, err := runner.Run(ctx, "sleep", []string{"forever"}, nil, 0)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run returned %v, want context.DeadlineExceeded", err)
	}

	if string(result.Stdout) != "first line\n" || result.StdoutReceived != int64(len("first line\n")) {
		t.Errorf("Run returned stdout %q, received %d; want the first line with its length", result.Stdout, result.StdoutReceived)
	}
}

// A connection that drops after output, with no exit status, returns the
// partial output with ErrNoExitStatus.
func TestRunnerReturnsPartialOutputWithoutExitStatus(t *testing.T) {
	t.Parallel()

	fakeVM := startRunnerVM(t, func(_ string, channel ssh.Channel, _ <-chan struct{}) uint32 {
		_, _ = io.WriteString(channel, "some output")
		_, _ = io.WriteString(channel.Stderr(), "some warning")
		_ = channel.Close()

		return 0
	})

	runner := boshssh.NewRunner(fakeVM.dial(t), &runnerLog{})

	result, err := runner.Run(t.Context(), catProgram, nil, nil, 0)
	if !errors.Is(err, boshssh.ErrNoExitStatus) {
		t.Fatalf("Run returned %v, want ErrNoExitStatus", err)
	}

	if string(result.Stdout) != "some output" || string(result.Stderr) != "some warning" {
		t.Errorf("Run returned stdout %q, stderr %q; want the partial output", result.Stdout, result.Stderr)
	}
}

// Key and certificate content travels only on stdin, so an argument or
// program that carries it is refused before any session is opened.
func TestRunnerRefusesContentInArgs(t *testing.T) {
	t.Parallel()

	fakeVM := startRunnerVM(t, func(string, ssh.Channel, <-chan struct{}) uint32 { return 0 })
	log := &runnerLog{}
	runner := boshssh.NewRunner(fakeVM.dial(t), log)
	rsaKey := rsaKeyBase64(t)

	cases := map[string]struct {
		program string
		args    []string
	}{
		"a one-line base64 RSA key": {catProgram, []string{"-in", rsaKey}},
		"a long letters-and-digits path segment": {
			catProgram, []string{"/var/vcap/data/" + strings.Repeat("Ab3", 23) + "x/server.crt"},
		},
		"a colon-hex dump":       {catProgram, []string{strings.TrimSpace(colonHex(16, false, 16))}},
		"five dashes":            {catProgram, []string{"-----"}},
		"text that is not UTF-8": {catProgram, []string{"a\xffb"}},
		"a certificate header":   {catProgram, []string{"-----BEGIN CERTIFICATE-----"}},
		"a key word":             {catProgram, []string{"x", "RSA PRIVATE"}},
		"a newline":              {catProgram, []string{"a\nb"}},
		"a NUL":                  {catProgram, []string{"a\x00b"}},
		"a newline in program":   {"cat\nrm", nil},
		"a bare key body line":   {catProgram, []string{"-in", testKeyBody}},
		"a key body as program":  {testKeyBody, nil},
		"an escape character":    {catProgram, []string{"a\x1bb"}},
	}

	for name, testCase := range cases {
		_, err := runner.Run(t.Context(), testCase.program, testCase.args, nil, 0)
		if !errors.Is(err, boshssh.ErrContentInArgs) {
			t.Errorf("%s: Run returned %v, want boshssh.ErrContentInArgs", name, err)
		}

		if err != nil && (strings.Contains(err.Error(), "BEGIN") || strings.Contains(err.Error(), "PRIVATE") ||
			strings.Contains(err.Error(), testKeyBody) || strings.Contains(err.Error(), rsaKey[:40])) {
			t.Errorf("%s: the error %q repeats the argument", name, err)
		}
	}

	if channels, execs := fakeVM.channels.Load(), fakeVM.execs.Load(); channels != 0 || execs != 0 {
		t.Errorf("the VM saw %d sessions and %d exec requests, want none", channels, execs)
	}

	for _, line := range log.all() {
		if strings.Contains(line, "BEGIN") || strings.Contains(line, "PRIVATE") || strings.Contains(line, testKeyBody) ||
			strings.Contains(line, rsaKey[:40]) {
			t.Errorf("the runner logged %q", line)
		}
	}
}

// rsaKeyBase64 generates a throwaway 2048-bit RSA key and returns its
// PKCS#1 DER form in standard base64 on one line.
func rsaKeyBase64(t *testing.T) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate the test key: %v", err)
	}

	return base64.StdEncoding.EncodeToString(x509.MarshalPKCS1PrivateKey(key))
}

// The reason a VM's sshd gives for refusing a session is free text, so
// it reaches neither the error nor a raw log line.
func TestRunnerKeepsSSHDTextOutOfErrors(t *testing.T) {
	t.Parallel()

	fakeVM := startRunnerVM(t, func(string, ssh.Channel, <-chan struct{}) uint32 { return 0 })
	reason := "no sessions today\nINFO forged " + testKeyBody
	fakeVM.rejectText.Store(&reason)

	log := &runnerLog{}
	runner := boshssh.NewRunner(fakeVM.dial(t), log)

	_, err := runner.Run(t.Context(), catProgram, nil, nil, 0)
	if !errors.Is(err, boshssh.ErrSessionNotOpened) {
		t.Fatalf("Run returned %v, want ErrSessionNotOpened", err)
	}

	if got, want := err.Error(), boshssh.ErrSessionNotOpened.Error(); got != want {
		t.Errorf("Run returned the error %q, want %q", got, want)
	}

	for _, line := range log.all() {
		if strings.ContainsAny(line, "\n\r") || strings.Contains(line, testKeyBody) {
			t.Errorf("the runner logged %q", line)
		}
	}
}

// Paths and lowercase hex hashes are not key content, so they still
// travel as arguments.
func TestRunnerAllowsPathsAndHashesInArgs(t *testing.T) {
	t.Parallel()

	fakeVM := startRunnerVM(t, func(command string, channel ssh.Channel, _ <-chan struct{}) uint32 {
		_, _ = io.WriteString(channel, command)

		return 0
	})
	runner := boshssh.NewRunner(fakeVM.dial(t), &runnerLog{})

	args := []string{
		"/var/vcap/jobs/nginx/config/tls/server.crt",
		"/var/vcap/data/jobs/valkey/abc/config/tls/server.crt",
		"/var/vcap/data/jobs/blacksmith/" + testHash[:40] + "/config/tls/server.crt",
		"/var/vcap/store/" + testHash + "/s/1",
		"/var/vcap/jobs/blacksmith/config/certs/director-ca.pem",
		testHash,
		"sha256=" + testHash,
	}

	result, err := runner.Run(t.Context(), "sha256sum", append(args, "a\tb"), nil, 0)
	if err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}

	for _, arg := range args {
		if !strings.Contains(string(result.Stdout), arg) {
			t.Errorf("the VM ran %q, which lacks the argument %q", result.Stdout, arg)
		}
	}
}

// catProgram is the program the runner tests send.
const catProgram = "cat"

// longBase64Run matches 40 or more base64 characters in a row.
var longBase64Run = regexp.MustCompile(`[A-Za-z0-9+/]{40,}`)

// A runner built with WithoutOutputLogging never logs what a command
// printed, whether the run ends with a status, a non-zero status, a
// cancelled context, or a dropped connection, and it still logs the exit
// status, the byte counts, and the truncation flags.
func TestRunnerWithoutOutputLoggingLogsNoOutput(t *testing.T) {
	t.Parallel()

	const (
		secretOut = "hunter2"
		secretErr = "s3cret"
	)

	cases := []struct {
		name       string
		exit       uint32
		drop       bool
		cancel     bool
		wantStatus string
		wantErr    error
	}{
		{name: "success", wantStatus: "exit status 0"},
		{name: "non-zero exit", exit: 3, wantStatus: "exit status 3"},
		{name: "cancelled context", cancel: true, wantStatus: "ended without an exit status", wantErr: context.DeadlineExceeded},
		{name: "dropped connection", drop: true, wantStatus: "ended without an exit status", wantErr: boshssh.ErrNoExitStatus},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fakeVM := startRunnerVM(t, func(_ string, channel ssh.Channel, stop <-chan struct{}) uint32 {
				_, _ = io.WriteString(channel, secretOut)
				_, _ = io.WriteString(channel.Stderr(), secretErr)

				if tc.cancel {
					<-stop
				}

				if tc.drop {
					_ = channel.Close()
				}

				return tc.exit
			})

			log := &runnerLog{}
			runner := boshssh.NewRunner(fakeVM.dial(t), log).WithoutOutputLogging()

			ctx := t.Context()

			if tc.cancel {
				var cancel context.CancelFunc

				ctx, cancel = context.WithTimeout(ctx, 300*time.Millisecond)
				defer cancel()
			}

			// A limit of 4 keeps four bytes of each stream, so the flags are true.
			result, err := runner.Run(ctx, catProgram, nil, nil, 4)
			if tc.wantErr == nil && err != nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("Run returned %v, want %v", err, tc.wantErr)
			}

			if result.StdoutReceived != int64(len(secretOut)) || !result.StdoutTruncated {
				t.Fatalf("Run received %d stdout bytes, truncated %v; the test does not test the counts",
					result.StdoutReceived, result.StdoutTruncated)
			}

			lines := log.all()

			for _, line := range lines {
				if strings.Contains(line, "hunter2") || strings.Contains(line, "s3cret") ||
					strings.Contains(line, "hunt") || strings.Contains(line, "s3cr") {
					t.Errorf("the runner logged output in %q", line)
				}
			}

			var summary string

			for _, line := range lines {
				if strings.Contains(line, "stdout 4 of 7 bytes") {
					summary = line
				}
			}

			if summary == "" {
				t.Fatalf("no log line holds the counts; the log is %q", lines)
			}

			for _, want := range []string{"stderr 4 of 6 bytes", "stdout truncated true", "stderr truncated true", tc.wantStatus} {
				if !strings.Contains(summary, want) {
					t.Errorf("the line %q lacks %q", summary, want)
				}
			}
		})
	}
}

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
