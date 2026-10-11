package bosh_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/bosh/sshconn"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"golang.org/x/crypto/ssh"
)

// Values the SSH tests share.
const (
	sshDeployment = "redis-1"
	sshInstance   = "redis"
	sshIndex      = 0
	sshTargetName = "redis/0"
	// sshCallLimit bounds an SSH call in these tests. A call that passes
	// returns long before it.
	sshCallLimit = 10 * time.Second
)

// anchoredUserRegex is the cleanup pattern for one throwaway user.
var anchoredUserRegex = regexp.MustCompile(`^\^bosh_[0-9a-f]{15}\$$`)

// newSSHAdapter starts an SSH director that answers with answer, and an
// adapter against it that polls tasks every testPollInterval and records
// its log lines.
func newSSHAdapter(t *testing.T, answer func(boshtest.SSHCall) boshtest.SSHAnswer) (*boshtest.SSHDirector, bosh.Director, *recordingLogger) {
	t.Helper()

	server := boshtest.NewSSH(t, answer, nil)
	logger := &recordingLogger{}

	director := newReadAdapter(t, server.Director, func(config *bosh.Config) { config.Logger = logger })
	bosh.SetTaskPollInterval(director, testPollInterval)

	return server, director, logger
}

// openSSHSession sets up SSH on the test instance and returns the session.
func openSSHSession(ctx context.Context, director bosh.Director) (*sshconn.Session, error) {
	info, err := director.SSHSession(ctx, sshDeployment, sshInstance, sshIndex, nil)
	if err != nil {
		return nil, err //nolint:wrapcheck // the test asserts on the adapter's own error
	}

	session, ok := info.(*sshconn.Session)
	if !ok {
		return nil, fmt.Errorf("SSHSession returned %T, want *sshconn.Session", info) //nolint:err113 // a test failure message
	}

	return session, nil
}

// mustOpenSSHSession is openSSHSession for a setup that must succeed.
func mustOpenSSHSession(t *testing.T, director bosh.Director) *sshconn.Session {
	t.Helper()

	var session *sshconn.Session

	err := callWithin(t, sshCallLimit, func() error {
		var err error

		session, err = openSSHSession(t.Context(), director)

		return err
	})
	if err != nil {
		t.Fatalf("SSHSession: %v", err)
	}

	return session
}

// onlyCall returns the one SSH request for command, and fails the test when
// there was not exactly one.
func onlyCall(t *testing.T, server *boshtest.SSHDirector, command string) boshtest.SSHCall {
	t.Helper()

	calls := server.CallsOf(command)
	if len(calls) != 1 {
		t.Fatalf("expected exactly one %s request, got %d: %+v", command, len(calls), calls)
	}

	return calls[0]
}

// sentKey returns the public key a setup request sent.
func sentKey(call boshtest.SSHCall) (ssh.PublicKey, error) {
	params, _ := call.Fields["params"].(map[string]interface{})
	text, _ := params["public_key"].(string)

	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(text)) //nolint:dogsled // Only the key matters; the comment, options, and rest do not.
	if err != nil {
		return nil, fmt.Errorf("the setup's public key %q does not parse: %w", text, err)
	}

	return key, nil
}

// setupKey is sentKey for a test goroutine.
func setupKey(t *testing.T, call boshtest.SSHCall) ssh.PublicKey {
	t.Helper()

	key, err := sentKey(call)
	if err != nil {
		t.Fatal(err)
	}

	return key
}

// The setup posts the director's own body, with the throwaway user and its
// ed25519 key, on the deployment's SSH route alone. It carries the
// caller's context ID, or a fresh one when the caller brought none.
func TestSSHSetupSendsTheDirectorsBody(t *testing.T) {
	cases := map[string]struct {
		ctx       func(context.Context) context.Context
		contextID func(string) bool
	}{
		"fresh context ID": {
			ctx:       func(ctx context.Context) context.Context { return ctx },
			contextID: func(id string) bool { return id != "" && id != callerContextID },
		},
		"caller's context ID": {
			ctx:       func(ctx context.Context) context.Context { return boshapi.WithContextID(ctx, callerContextID) },
			contextID: func(id string) bool { return id == callerContextID },
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BLACKSMITH_TEST_MODE", "true")

			server, director, _ := newSSHAdapter(t, nil)

			err := callWithin(t, sshCallLimit, func() error {
				_, err := openSSHSession(testCase.ctx(t.Context()), director)

				return err
			})
			if err != nil {
				t.Fatalf("SSHSession: %v", err)
			}

			setup := onlyCall(t, server, boshtest.SSHSetup)

			if setup.Deployment != sshDeployment {
				t.Errorf("setup posted to deployment %q, want %q", setup.Deployment, sshDeployment)
			}

			user := setup.User()
			if !agentUser.MatchString(user) {
				t.Fatalf("setup user %q does not match %s", user, agentUser)
			}

			key := setupKey(t, setup)
			if key.Type() != ssh.KeyAlgoED25519 {
				t.Errorf("setup key type %q, want %q", key.Type(), ssh.KeyAlgoED25519)
			}

			params, _ := setup.Fields["params"].(map[string]interface{})
			publicKey, _ := params["public_key"].(string)
			want := `{"command":"setup","deployment_name":"redis-1","target":{"job":"redis","indexes":[0],"ids":[]},` +
				`"params":{"user":"` + user + `","public_key":"` + publicKey + `"}}`

			if strings.TrimSpace(setup.Body) != want {
				t.Errorf("setup body\n%s\nwant\n%s", setup.Body, want)
			}

			if !testCase.contextID(setup.ContextID) {
				t.Errorf("setup carried context ID %q", setup.ContextID)
			}

			for _, request := range server.APIRequests() {
				if request.Method == http.MethodGet && strings.HasPrefix(request.Path, "/deployments/") {
					t.Errorf("the setup read %s, which bosh-cli's FindDeployment sends", request.Path)
				}
			}
		})
	}
}

// agentUser is the user name form the BOSH agent's cleanup removes.
var agentUser = regexp.MustCompile(`^bosh_[0-9a-f]{15}$`)

// The cleanup targets the setup's instance alone, removes only the setup's
// user, carries a context ID of its own, and is sent once however often
// it is called.
func TestSSHCleanupSendsTheAnchoredUserRegex(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, director, _ := newSSHAdapter(t, nil)
	session := mustOpenSSHSession(t, director)

	for range 2 {
		err := callWithin(t, sshCallLimit, func() error { return session.Cleanup(t.Context()) })
		if err != nil {
			t.Fatalf("Cleanup: %v", err)
		}
	}

	setup := onlyCall(t, server, boshtest.SSHSetup)
	cleanup := onlyCall(t, server, boshtest.SSHCleanup)

	pattern := cleanup.User()
	if !anchoredUserRegex.MatchString(pattern) {
		t.Fatalf("cleanup pattern %q does not match %s", pattern, anchoredUserRegex)
	}

	if pattern != "^"+setup.User()+"$" {
		t.Errorf("cleanup pattern %q does not anchor the setup's user %q", pattern, setup.User())
	}

	want := `{"command":"cleanup","deployment_name":"redis-1","target":{"job":"redis","indexes":[0],"ids":[]},` +
		`"params":{"user_regex":"` + pattern + `"}}`
	if strings.TrimSpace(cleanup.Body) != want {
		t.Errorf("cleanup body\n%s\nwant\n%s", cleanup.Body, want)
	}

	if cleanup.Deployment != sshDeployment {
		t.Errorf("cleanup posted to deployment %q, want %q", cleanup.Deployment, sshDeployment)
	}

	if cleanup.ContextID == "" || cleanup.ContextID == setup.ContextID {
		t.Errorf("cleanup context ID %q, want a fresh one that differs from the setup's %q", cleanup.ContextID, setup.ContextID)
	}
}

// A setup whose result names no instance, or names one where the agent
// failed, is refused.
func TestSSHSetupRefusesAnEmptyOrFailedResult(t *testing.T) {
	cases := map[string]struct {
		output string
		want   error
	}{
		"empty array": {output: "[]", want: sshconn.ErrNoHosts},
		"error status": {
			output: `[{"status":"error","ip":"","host_public_key":"","job":"redis","index":0,"id":"abc"}]`,
			want:   sshconn.ErrSetupFailed,
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BLACKSMITH_TEST_MODE", "true")

			_, director, _ := newSSHAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
				if call.Command == boshtest.SSHSetup {
					return boshtest.SSHAnswer{Output: testCase.output}
				}

				return boshtest.SSHAnswer{}
			})

			err := callWithin(t, sshCallLimit, func() error {
				_, err := openSSHSession(t.Context(), director)

				return err
			})
			if !errors.Is(err, testCase.want) {
				t.Fatalf("SSHSession returned %v, want an error that wraps %v", err, testCase.want)
			}
		})
	}
}

// SSHSession hands back a typed session that carries the host, the user,
// the target, the key the setup sent, and a cleanup.
func TestSSHSessionReturnsTheTypedSession(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, director, _ := newSSHAdapter(t, nil)
	session := mustOpenSSHSession(t, director)

	setup := onlyCall(t, server, boshtest.SSHSetup)
	user := setup.User()

	if session.Deployment != sshDeployment || session.User != user {
		t.Errorf("session for deployment %q and user %q, want %q and %q", session.Deployment, session.User, sshDeployment, user)
	}

	if session.Target.Job != sshInstance || len(session.Target.Indices) != 1 || session.Target.Indices[0] != sshIndex || len(session.Target.IDs) != 0 {
		t.Errorf("session target %+v, want %s", session.Target, sshTargetName)
	}

	if len(session.Hosts) != 1 || session.Hosts[0].Address != "127.0.0.1" || session.Hosts[0].Username != user {
		t.Errorf("session hosts %+v, want one host 127.0.0.1 for %s", session.Hosts, user)
	}

	if session.Signer == nil || !bytes.Equal(session.Signer.PublicKey().Marshal(), setupKey(t, setup).Marshal()) {
		t.Error("the session's signer does not hold the key the setup sent")
	}

	if session.Cleanup == nil {
		t.Fatal("the session carries no cleanup")
	}

	err := session.Cleanup(t.Context())
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	onlyCall(t, server, boshtest.SSHCleanup)
}

// SSHCommand sets up the user through boshapi, logs in with the key the
// setup sent, runs the command, and removes the user once.
func TestSSHCommandRunsThroughBoshapi(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, director, _ := newSSHAdapter(t, nil)

	vmServer := startExecServer(t, func(user string, key ssh.PublicKey) bool {
		setups := server.CallsOf(boshtest.SSHSetup)
		if len(setups) != 1 || user != setups[0].User() {
			return false
		}

		sent, err := sentKey(setups[0])

		return err == nil && bytes.Equal(key.Marshal(), sent.Marshal())
	}, server.HostKey)
	bosh.SetSSHPort(director, vmServer.port)

	var output string

	err := callWithin(t, sshCallLimit, func() error {
		var err error

		output, err = director.SSHCommand(t.Context(), sshDeployment, sshInstance, sshIndex, "echo", []string{"hello world"}, nil)

		return err //nolint:wrapcheck // the test asserts on the adapter's own error
	})
	if err != nil {
		t.Fatalf("SSHCommand: %v", err)
	}

	if output != "ran: echo 'hello world'\n" {
		t.Errorf("output %q, want the command's output", output)
	}

	setup := onlyCall(t, server, boshtest.SSHSetup)
	cleanup := onlyCall(t, server, boshtest.SSHCleanup)

	if cleanup.User() != "^"+setup.User()+"$" {
		t.Errorf("cleanup pattern %q does not anchor the setup's user %q", cleanup.User(), setup.User())
	}

	if logins := vmServer.logins(); len(logins) != 1 || logins[0] != setup.User() {
		t.Errorf("the VM saw logins %v, want one as %s", logins, setup.User())
	}
}

// The cleanup waits for its task, and a task that ends in any state but
// done is logged at Error with the task, the user, the deployment, the
// target, and both context IDs.
func TestSSHCleanupWaitsForItsTaskAndLogsAFailure(t *testing.T) {
	t.Run("waits for its task", func(t *testing.T) {
		t.Setenv("BLACKSMITH_TEST_MODE", "true")
		testSSHCleanupWaitsForItsTask(t)
	})

	t.Run("logs a failed task", func(t *testing.T) {
		t.Setenv("BLACKSMITH_TEST_MODE", "true")
		testSSHCleanupLogsAFailedTask(t)
	})
}

func testSSHCleanupWaitsForItsTask(t *testing.T) {
	t.Helper()

	server, director, _ := newSSHAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		if call.Command == boshtest.SSHCleanup {
			return boshtest.SSHAnswer{State: boshtest.TaskProcessing}
		}

		return boshtest.SSHAnswer{}
	})
	session := mustOpenSSHSession(t, director)

	done := make(chan error, 1)

	go func() { done <- session.Cleanup(t.Context()) }()

	waitUntil(t, "the cleanup task is read", func() bool {
		calls := server.CallsOf(boshtest.SSHCleanup)

		return len(calls) == 1 && server.Reads(calls[0].TaskID) > 0
	})

	select {
	case err := <-done:
		t.Fatalf("the cleanup returned %v while its task was still processing", err)
	case <-time.After(5 * testPollInterval):
	}

	server.SetState(onlyCall(t, server, boshtest.SSHCleanup).TaskID, boshtest.TaskDone)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Cleanup: %v", err)
		}
	case <-time.After(sshCallLimit):
		t.Fatal("the cleanup did not return once its task ended")
	}
}

func testSSHCleanupLogsAFailedTask(t *testing.T) {
	t.Helper()

	server, director, logger := newSSHAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		if call.Command == boshtest.SSHCleanup {
			return boshtest.SSHAnswer{State: boshtest.TaskError, Result: "the agent on redis/0 did not answer"}
		}

		return boshtest.SSHAnswer{}
	})
	session := mustOpenSSHSession(t, director)

	err := callWithin(t, sshCallLimit, func() error { return session.Cleanup(t.Context()) })

	var failed *bosh.TaskFailedError
	if !errors.As(err, &failed) || failed.State != boshtest.TaskError {
		t.Fatalf("Cleanup returned %v, want a TaskFailedError in state error", err)
	}

	setup := onlyCall(t, server, boshtest.SSHSetup)
	cleanup := onlyCall(t, server, boshtest.SSHCleanup)

	if failed.TaskID != cleanup.TaskID {
		t.Errorf("the error names task %d, want the cleanup's task %d", failed.TaskID, cleanup.TaskID)
	}

	if logger.count("ERROR", "task "+strconv.Itoa(cleanup.TaskID), setup.User(), sshDeployment, sshTargetName,
		setup.ContextID, cleanup.ContextID) != 1 {
		t.Errorf("no single Error line names the task, the user, the deployment, the target, and both context IDs: %v", logger.lines)
	}
}

// Only the director's deployment-not-found answer, code 70000, means the
// deployment is gone. A 404 for a route the director does not serve is
// not that answer.
func TestSSHSetupKeepsTheDirectorsNotFoundAnswer(t *testing.T) {
	t.Run("code 70000", func(t *testing.T) {
		t.Setenv("BLACKSMITH_TEST_MODE", "true")

		_, director, _ := newSSHAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer {
			return boshtest.SSHAnswer{Status: http.StatusNotFound, Code: boshtest.CodeDeploymentNotFound, Description: "Deployment 'redis-1' doesn't exist"}
		})

		err := callWithin(t, sshCallLimit, func() error {
			_, err := openSSHSession(t.Context(), director)

			return err
		})
		if !errors.Is(err, bosh.ErrDeploymentNotFound) {
			t.Fatalf("SSHSession returned %v, want ErrDeploymentNotFound", err)
		}

		var apiErr *boshapi.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != boshtest.CodeDeploymentNotFound {
			t.Errorf("the director's answer is not reachable in %v", err)
		}
	})

	t.Run("route 404", func(t *testing.T) {
		t.Setenv("BLACKSMITH_TEST_MODE", "true")

		server := boshtest.New(t, nil)
		director := newTaskAdapter(t, server)

		err := callWithin(t, sshCallLimit, func() error {
			_, err := openSSHSession(t.Context(), director)

			return err
		})
		if err == nil || errors.Is(err, bosh.ErrDeploymentNotFound) {
			t.Fatalf("SSHSession returned %v for a route 404, want an error that is not ErrDeploymentNotFound", err)
		}

		if !boshapi.IsRouteNotFound(err) {
			t.Errorf("the route 404 is not reachable in %v", err)
		}
	})
}

// A director that refuses the broker's client for the deployment, with a
// 401 and code 600000, is asked once, and the error says the client lacks
// the permission to set up SSH there.
func TestSSHSetupNamesAMissingPermission(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, director, _ := newSSHAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer {
		return boshtest.SSHAnswer{
			Status:      http.StatusUnauthorized,
			Code:        boshapi.ErrorCodeUnauthorizedToAccessDeployment,
			Description: "Require one of the scopes: bosh.admin, bosh.00000000-0000-0000-0000-000000000000.admin, bosh.teams.redis.admin",
		}
	})

	err := callWithin(t, sshCallLimit, func() error {
		_, err := openSSHSession(t.Context(), director)

		return err
	})
	if !errors.Is(err, bosh.ErrSSHNotPermitted) {
		t.Fatalf("SSHSession returned %v, want ErrSSHNotPermitted", err)
	}

	for _, part := range []string{"permission", "set up SSH", sshDeployment, "bosh.admin"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q does not say %q", err, part)
		}
	}

	if calls := server.Calls(); len(calls) != 1 {
		t.Errorf("the director was asked %d times, want once", len(calls))
	}
}

// A target that is not one instance is refused before anything is sent,
// because an empty group selects every instance in the deployment.
func TestSSHRefusesATargetThatIsNotOneInstance(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server, director, _ := newSSHAdapter(t, nil)

	for name, target := range map[string]struct {
		instance string
		index    int
	}{
		"no group":       {"", 0},
		"negative index": {sshInstance, -1},
	} {
		_, err := director.SSHSession(t.Context(), sshDeployment, target.instance, target.index, nil)
		if !errors.Is(err, bosh.ErrSSHTargetInvalid) {
			t.Errorf("%s: SSHSession returned %v, want ErrSSHTargetInvalid", name, err)
		}

		_, err = director.SSHCommand(t.Context(), sshDeployment, target.instance, target.index, "true", nil, nil)
		if !errors.Is(err, bosh.ErrSSHTargetInvalid) {
			t.Errorf("%s: SSHCommand returned %v, want ErrSSHTargetInvalid", name, err)
		}
	}

	if calls := server.Calls(); len(calls) != 0 {
		t.Errorf("%d SSH requests were sent, want none", len(calls))
	}
}

// execServer is an in-process SSH server on a loopback port that stands
// in for a VM's sshd. It presents every host key it is given, accepts a
// login the accept function approves, answers each exec request with
// "ran: <command>", and records each login's user.
type execServer struct {
	port string

	mu    sync.Mutex
	users []string
}

func (s *execServer) logins() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.users...)
}

var errExecServerRefused = errors.New("the test VM refuses the login")

func startExecServer(t *testing.T, accept func(user string, key ssh.PublicKey) bool, hostKeys ...ssh.Signer) *execServer {
	t.Helper()

	if len(hostKeys) == 0 {
		t.Fatal("the test VM needs a host key")
	}

	server := &execServer{}

	// The callback records each user it accepts and grants no permission
	// the exec handler reads, so the stateful-callback hazard does not
	// apply to this test server.
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) { //nolint:gosec // see the comment above
			if !accept(meta.User(), key) {
				return nil, errExecServerRefused
			}

			server.mu.Lock()
			server.users = append(server.users, meta.User())
			server.mu.Unlock()

			return &ssh.Permissions{}, nil
		},
	}
	for _, hostKey := range hostKeys {
		config.AddHostKey(hostKey)
	}

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	var waitGroup sync.WaitGroup

	t.Cleanup(func() {
		_ = listener.Close()

		waitGroup.Wait()
	})

	_, server.port, _ = net.SplitHostPort(listener.Addr().String())

	waitGroup.Add(1)

	go func() {
		defer waitGroup.Done()

		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			waitGroup.Add(1)

			go func() {
				defer waitGroup.Done()

				serveExec(conn, config)
			}()
		}
	}()

	return server
}

func serveExec(conn net.Conn, config *ssh.ServerConfig) {
	defer func() { _ = conn.Close() }()

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
			defer func() { _ = channel.Close() }()

			for request := range channelRequests {
				if request.Type != "exec" || len(request.Payload) < 4 {
					_ = request.Reply(false, nil)

					continue
				}

				command := string(request.Payload[4:])
				_ = request.Reply(true, nil)

				_, _ = fmt.Fprintf(channel, "ran: %s\n", command)

				status := make([]byte, 4)
				binary.BigEndian.PutUint32(status, 0)

				_, _ = channel.SendRequest("exit-status", false, status)

				return
			}
		}()
	}
}
