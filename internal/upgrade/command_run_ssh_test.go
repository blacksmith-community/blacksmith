package upgrade

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh/sshconn"

	gossh "golang.org/x/crypto/ssh"
)

// The secrets each stream and the stdin of the fake VM's commands hold.
const (
	secretStdout = "s3cr3t-out"
	secretStderr = "s3cr3t-err"
	secretStdin  = "s3cr3t-in"
)

// sshVM is an in-process SSH server on a loopback port that stands in for a
// VM's sshd. Every exec request runs through handle, which gets the
// command's stdin, a writer for each stream, and a channel that closes
// when the VM shuts down, and returns the exit status.
type sshVM struct {
	listener net.Listener
	hostKey  gossh.Signer
	config   *gossh.ServerConfig
	handle   func(channel gossh.Channel, stop <-chan struct{}) uint32
	stop     chan struct{}
	wg       sync.WaitGroup

	mu    sync.Mutex
	conns []net.Conn
}

func startSSHVM(t *testing.T, handle func(channel gossh.Channel, stop <-chan struct{}) uint32) *sshVM {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate the host key: %v", err)
	}

	hostKey, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("make the host signer: %v", err)
	}

	config := &gossh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(hostKey)

	vm := &sshVM{listener: listener, hostKey: hostKey, config: config, handle: handle, stop: make(chan struct{})}

	vm.wg.Add(1)

	go func() {
		defer vm.wg.Done()

		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			vm.mu.Lock()
			vm.conns = append(vm.conns, conn)
			vm.mu.Unlock()

			vm.wg.Add(1)

			go func() {
				defer vm.wg.Done()

				vm.serve(conn)
			}()
		}
	}()

	t.Cleanup(vm.close)

	return vm
}

func (v *sshVM) close() {
	close(v.stop)
	_ = v.listener.Close()

	v.mu.Lock()
	for _, conn := range v.conns {
		_ = conn.Close()
	}
	v.mu.Unlock()

	v.wg.Wait()
}

func (v *sshVM) serve(conn net.Conn) {
	serverConn, channels, requests, err := gossh.NewServerConn(conn, v.config)
	if err != nil {
		return
	}

	defer func() { _ = serverConn.Close() }()

	go gossh.DiscardRequests(requests)

	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(gossh.UnknownChannelType, "session only")

			continue
		}

		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			return
		}

		v.wg.Add(1)

		go func() {
			defer v.wg.Done()

			v.serveChannel(channel, channelRequests)
		}()
	}
}

func (v *sshVM) serveChannel(channel gossh.Channel, requests <-chan *gossh.Request) {
	defer func() { _ = channel.Close() }()

	for request := range requests {
		if request.Type != "exec" {
			_ = request.Reply(false, nil)

			continue
		}

		_ = request.Reply(true, nil)

		go func() {
			for other := range requests {
				_ = other.Reply(false, nil)
			}
		}()

		code := v.handle(channel, v.stop)

		status := make([]byte, 4)
		binary.BigEndian.PutUint32(status, code)
		_, _ = channel.SendRequest("exit-status", false, status)

		return
	}
}

// sshDirector hands the patch session a single host on the fake VM.
type sshDirector struct {
	vm *sshVM
	t  *testing.T
}

func (d *sshDirector) SSHGroupSession(_ context.Context, deployment, group string, _ []int) (*sshconn.Session, error) {
	return &sshconn.Session{
		Deployment: deployment,
		Hosts:      []sshconn.Host{{Job: group, IndexOrID: runVMID, Address: d.vm.listener.Addr().String()}},
	}, nil
}

func (d *sshDirector) DialSSHHost(ctx context.Context, _ *sshconn.Session, _ sshconn.Host) (*gossh.Client, error) {
	config := &gossh.ClientConfig{
		User:            "vcap",
		HostKeyCallback: gossh.FixedHostKey(d.vm.hostKey.PublicKey()),
		Timeout:         5 * time.Second,
	}

	dialer := net.Dialer{}

	conn, err := dialer.DialContext(ctx, "tcp", d.vm.listener.Addr().String())
	if err != nil {
		return nil, errors.New("dialing the fake VM")
	}

	clientConn, channels, requests, err := gossh.NewClientConn(conn, d.vm.listener.Addr().String(), config)
	if err != nil {
		_ = conn.Close()

		return nil, errors.New("handshake with the fake VM")
	}

	client := gossh.NewClient(clientConn, channels, requests)
	d.t.Cleanup(func() { _ = client.Close() })

	return client, nil
}

// printSecrets is a command that reads its stdin, prints a secret on each
// stream, and exits with code.
func printSecrets(code uint32) func(channel gossh.Channel, stop <-chan struct{}) uint32 {
	return func(channel gossh.Channel, _ <-chan struct{}) uint32 {
		_, _ = io.Copy(io.Discard, channel)
		_, _ = io.WriteString(channel, secretStdout)
		_, _ = io.WriteString(channel.Stderr(), secretStderr)

		return code
	}
}

// blockAfterSecrets prints the secrets and then runs until the VM stops.
func blockAfterSecrets(channel gossh.Channel, stop <-chan struct{}) uint32 {
	_, _ = io.WriteString(channel, secretStdout)
	_, _ = io.WriteString(channel.Stderr(), secretStderr)

	<-stop

	return 0
}

// openSecretSession opens a session through open and runs exec on it with a
// secret on stdin, and returns what the runner logged along with the
// result.
func runWithSecrets(t *testing.T, build func(*sshDirector, *captureLog) func(context.Context, string, string, []int) (PatchSession, error), handle func(gossh.Channel, <-chan struct{}) uint32, timeout time.Duration) (string, string, error) {
	t.Helper()

	log := &captureLog{}
	director := &sshDirector{vm: startSSHVM(t, handle), t: t}

	sess, err := build(director, log)(t.Context(), "dep", "redis", []int{0})
	if err != nil {
		t.Fatalf("opening the session: %v", err)
	}

	t.Cleanup(func() { _ = sess.Close() })

	ctx := t.Context()

	if timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	result, runErr := sess.Run(ctx, 0, "exec", []string{runTaskID, "vcap", "30", strings.Repeat("a", 64)}, strings.NewReader(secretStdin))

	return log.joined(), string(result.Stdout) + string(result.Stderr), runErr
}

func assertNoSecrets(t *testing.T, logs string) {
	t.Helper()

	for _, secret := range []string{secretStdout, secretStderr, secretStdin} {
		if strings.Contains(logs, secret) {
			t.Errorf("the logs hold a secret (%d bytes of log)", len(logs))
		}
	}
}

// A command runner built by NewCommandRunner never logs a stream, whether
// the command succeeds, fails, or is cut off, and a patch runner does, so
// the check can tell the two apart.
func TestNewCommandRunnerLogsNoOutput(t *testing.T) {
	t.Parallel()

	command := func(director *sshDirector, log *captureLog) func(context.Context, string, string, []int) (PatchSession, error) {
		return NewCommandRunner(director, log)
	}

	cases := map[string]struct {
		handle  func(gossh.Channel, <-chan struct{}) uint32
		timeout time.Duration
	}{
		"success": {handle: printSecrets(0)},
		"failure": {handle: printSecrets(3)},
		"cancel":  {handle: blockAfterSecrets, timeout: 300 * time.Millisecond},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			logs, _, _ := runWithSecrets(t, command, tc.handle, tc.timeout)

			assertNoSecrets(t, logs)
		})
	}
}

// The control for the test above. A patch runner does log the streams.
func TestNewPatchRunnerLogsOutput(t *testing.T) {
	t.Parallel()

	patch := func(director *sshDirector, log *captureLog) func(context.Context, string, string, []int) (PatchSession, error) {
		return NewPatchRunner(director, log)
	}

	logs, output, err := runWithSecrets(t, patch, printSecrets(3), 0)
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}

	if !strings.Contains(output, secretStdout) || !strings.Contains(output, secretStderr) {
		t.Fatal("the fake VM's output did not come back")
	}

	if !strings.Contains(logs, secretStdout) && !strings.Contains(logs, secretStderr) {
		t.Skip("the patch runner no longer logs streams, so the control proves nothing")
	}
}

// The command runner's results still hold the whole output.
func TestNewCommandRunnerReturnsTheOutput(t *testing.T) {
	t.Parallel()

	command := func(director *sshDirector, log *captureLog) func(context.Context, string, string, []int) (PatchSession, error) {
		return NewCommandRunner(director, log)
	}

	_, output, err := runWithSecrets(t, command, printSecrets(3), 0)
	if err != nil {
		t.Fatalf("the run failed: %v", err)
	}

	if !strings.Contains(output, secretStdout) || !strings.Contains(output, secretStderr) {
		t.Fatal("the output did not come back")
	}
}

// A nil director gives no runner, as it does for patch mode.
func TestNewCommandRunnerNeedsADirector(t *testing.T) {
	t.Parallel()

	if NewCommandRunner(nil, &captureLog{}) != nil {
		t.Fatal("a nil director gave a runner")
	}
}
