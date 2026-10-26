package api_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/api"
	"blacksmith/internal/bosh/sshconn"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/upgrade"
	"blacksmith/pkg/logger"
	pkgservices "blacksmith/pkg/services"

	gossh "golang.org/x/crypto/ssh"
)

const (
	wiringStdout = "wiring-secret-out"
	wiringStderr = "wiring-secret-err"
	wiringStdin  = "wiring-secret-in"
)

// wiringLog keeps every line the runner writes.
type wiringLog struct {
	logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func (l *wiringLog) add(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *wiringLog) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return strings.Join(l.lines, "\n")
}

func (l *wiringLog) Named(string) logger.Logger { return l }

func (l *wiringLog) Debugf(format string, args ...interface{}) { l.add(format, args...) }
func (l *wiringLog) Infof(format string, args ...interface{})  { l.add(format, args...) }
func (l *wiringLog) Warnf(format string, args ...interface{})  { l.add(format, args...) }
func (l *wiringLog) Errorf(format string, args ...interface{}) { l.add(format, args...) }
func (l *wiringLog) Debug(msg string, args ...interface{})     { l.add(msg, args...) }
func (l *wiringLog) Info(msg string, args ...interface{})      { l.add(msg, args...) }
func (l *wiringLog) Warn(msg string, args ...interface{})      { l.add(msg, args...) }
func (l *wiringLog) Error(msg string, args ...interface{})     { l.add(msg, args...) }

// wiringDirector hands the SSH runners one host on a loopback server that
// prints a secret on each stream.
type wiringDirector struct {
	interfaces.Director

	addr    string
	hostKey gossh.Signer
}

func (d *wiringDirector) SSHGroupSession(_ context.Context, deployment, group string, _ []int) (*sshconn.Session, error) {
	return &sshconn.Session{
		Deployment: deployment,
		Hosts:      []sshconn.Host{{Job: group, IndexOrID: "vm-1", Address: d.addr}},
	}, nil
}

func (d *wiringDirector) DialSSHHost(ctx context.Context, _ *sshconn.Session, _ sshconn.Host) (*gossh.Client, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", d.addr)
	if err != nil {
		return nil, fmt.Errorf("dialing the fake VM: %w", err)
	}

	config := &gossh.ClientConfig{User: "vcap", HostKeyCallback: gossh.FixedHostKey(d.hostKey.PublicKey()), Timeout: 5 * time.Second}

	clientConn, channels, requests, err := gossh.NewClientConn(conn, d.addr, config)
	if err != nil {
		_ = conn.Close()

		return nil, fmt.Errorf("handshake with the fake VM: %w", err)
	}

	return gossh.NewClient(clientConn, channels, requests), nil
}

// startWiringVM serves exec requests by reading stdin and printing a
// secret on stdout and stderr, then exiting 3.
func startWiringVM(t *testing.T) *wiringDirector {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("host key: %v", err)
	}

	hostKey, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}

	config := &gossh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(hostKey)

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go serveWiringConn(conn, config)
		}
	}()

	return &wiringDirector{addr: listener.Addr().String(), hostKey: hostKey}
}

func serveWiringConn(conn net.Conn, config *gossh.ServerConfig) {
	defer func() { _ = conn.Close() }()

	serverConn, channels, requests, err := gossh.NewServerConn(conn, config)
	if err != nil {
		return
	}

	go gossh.DiscardRequests(requests)

	for newChannel := range channels {
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			return
		}

		go func() {
			defer func() { _ = channel.Close() }()

			for request := range channelRequests {
				if request.Type != "exec" {
					_ = request.Reply(false, nil)

					continue
				}

				_ = request.Reply(true, nil)

				_, _ = io.Copy(io.Discard, channel)
				_, _ = io.WriteString(channel, wiringStdout)
				_, _ = io.WriteString(channel.Stderr(), wiringStderr)
				_, _ = channel.SendRequest("exit-status", false, []byte{0, 0, 0, 3})

				return
			}
		}()
	}

	_ = serverConn.Close()
}

// The runner the production constructor gives a command task keeps what a
// command prints out of the broker log, and the patch runner beside it,
// which is the control, does not.
func TestAPICommandRunnerKeepsOutputOutOfTheLog(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		runner func(*upgrade.Manager) func(context.Context, string, string, []int) (upgrade.PatchSession, error)
		logged bool
	}{
		{name: "command runner", runner: func(m *upgrade.Manager) func(context.Context, string, string, []int) (upgrade.PatchSession, error) {
			return m.Dependencies().CommandRunner
		}},
		{name: "patch runner", runner: func(m *upgrade.Manager) func(context.Context, string, string, []int) (upgrade.PatchSession, error) {
			return m.Dependencies().PatchRunner
		}, logged: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			log := &wiringLog{}
			director := startWiringVM(t)

			apiHandler := api.NewInternalAPI(api.Dependencies{
				Config:             &mockConfig{},
				Logger:             log,
				Vault:              &memoryVault{saved: map[string][]byte{}},
				Broker:             &mockBroker{},
				Director:           &startupDirector{},
				BatchDirector:      director,
				CFManager:          &mockCFManager{},
				ServicesManager:    &pkgservices.Manager{},
				SecurityMiddleware: &pkgservices.SecurityMiddleware{},
			})
			t.Cleanup(apiHandler.UpgradeHandler().Stop)

			open := tc.runner(apiHandler.UpgradeHandler().GetManager())
			if open == nil {
				t.Fatal("the manager has no runner")
			}

			sess, err := open(t.Context(), "dep", "redis", []int{0})
			if err != nil {
				t.Fatalf("opening the session: %v", err)
			}

			t.Cleanup(func() { _ = sess.Close() })

			result, _ := sess.Run(t.Context(), 0, "exec", []string{"task-1", "vcap", "30", strings.Repeat("a", 64)}, strings.NewReader(wiringStdin))
			if !strings.Contains(string(result.Stdout), wiringStdout) {
				t.Fatalf("the fake VM's output did not come back (%d bytes)", len(result.Stdout))
			}

			logs := log.joined()
			held := strings.Contains(logs, wiringStdout) || strings.Contains(logs, wiringStderr)

			if held != tc.logged {
				t.Errorf("log holds the output = %v, want %v (%d bytes of log)", held, tc.logged, len(logs))
			}

			if strings.Contains(logs, wiringStdin) {
				t.Error("the log holds the stdin secret")
			}
		})
	}
}
