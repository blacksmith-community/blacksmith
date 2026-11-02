package upgrade

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh/sshconn"
	"blacksmith/internal/ops/scripts"

	gossh "golang.org/x/crypto/ssh"
)

const bashInvoke = "sudo -n /bin/bash -s -- "

// bashVM is an SSH server whose exec requests run the real bash on the
// script the broker sends, under a test root, with sudo stripped.
type bashVM struct {
	listener net.Listener
	hostKey  gossh.Signer
	root     string

	mu    sync.Mutex
	execs []string
}

func startBashVM(t *testing.T, root string) *bashVM {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	_, private, _ := ed25519.GenerateKey(rand.Reader)
	hostKey, _ := gossh.NewSignerFromKey(private)
	config := &gossh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(hostKey)

	vm := &bashVM{listener: listener, hostKey: hostKey, root: root}

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			go func() {
				sc, chans, reqs, err := gossh.NewServerConn(conn, config)
				if err != nil {
					return
				}
				defer sc.Close()
				go gossh.DiscardRequests(reqs)

				for nc := range chans {
					ch, creqs, err := nc.Accept()
					if err != nil {
						return
					}

					go vm.serve(ch, creqs)
				}
			}()
		}
	}()

	t.Cleanup(func() { _ = listener.Close() })

	return vm
}

func (v *bashVM) serve(ch gossh.Channel, reqs <-chan *gossh.Request) {
	defer ch.Close()

	for req := range reqs {
		if req.Type != "exec" || len(req.Payload) < 4 {
			_ = req.Reply(false, nil)

			continue
		}

		n := binary.BigEndian.Uint32(req.Payload)
		command := string(req.Payload[4 : 4+n])
		_ = req.Reply(true, nil)

		go func() {
			for other := range reqs {
				_ = other.Reply(false, nil)
			}
		}()

		v.mu.Lock()
		v.execs = append(v.execs, command)
		v.mu.Unlock()

		code := uint32(255)

		if rest, ok := strings.CutPrefix(command, bashInvoke); ok {
			cmd := exec.Command("/bin/bash", "-c", "exec /bin/bash -s -- "+rest)
			cmd.Env = []string{"CERTPATCH_ROOT=" + v.root, "HOME=" + v.root, "PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
			cmd.Stdin = ch
			cmd.Stdout = ch
			cmd.Stderr = ch.Stderr()
			cmd.WaitDelay = 2 * time.Second

			err := cmd.Run()

			var exitErr *exec.ExitError

			switch {
			case err == nil:
				code = 0
			case errors.As(err, &exitErr):
				code = uint32(exitErr.ExitCode())
			}
		}

		status := make([]byte, 4)
		binary.BigEndian.PutUint32(status, code)
		_, _ = ch.SendRequest("exit-status", false, status)

		return
	}
}

type bashDirector struct {
	vm *bashVM
	t  *testing.T
}

func (d *bashDirector) SSHGroupSession(_ context.Context, deployment, group string, _ []int) (*sshconn.Session, error) {
	return &sshconn.Session{Deployment: deployment, Hosts: []sshconn.Host{{Job: group, IndexOrID: runVMID, Address: d.vm.listener.Addr().String()}}}, nil
}

func (d *bashDirector) DialSSHHost(ctx context.Context, _ *sshconn.Session, _ sshconn.Host) (*gossh.Client, error) {
	config := &gossh.ClientConfig{User: "vcap", HostKeyCallback: gossh.FixedHostKey(d.vm.hostKey.PublicKey()), Timeout: 5 * time.Second}

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", d.vm.listener.Addr().String())
	if err != nil {
		return nil, err
	}

	cc, chans, reqs, err := gossh.NewClientConn(conn, d.vm.listener.Addr().String(), config)
	if err != nil {
		return nil, err
	}

	client := gossh.NewClient(cc, chans, reqs)
	d.t.Cleanup(func() { _ = client.Close() })

	return client, nil
}

func bashTree(t *testing.T) string {
	t.Helper()

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{"var/vcap/data", "home/vcap", "usr/bin", "root"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if os.Geteuid() == 0 {
		if err := os.Chown(filepath.Join(root, "home/vcap"), 1000, 1000); err != nil {
			t.Fatal(err)
		}
	}

	return root
}

// bashPrepare sends prepare through the production command runner over
// SSH, with the stdin the driver builds, and returns the exit code.
func bashPrepare(t *testing.T, root, task, user string, command []byte) (int, *bashVM) {
	t.Helper()

	vm := startBashVM(t, root)
	open := NewCommandRunner(&bashDirector{vm: vm, t: t}, &captureLog{})

	sess, err := open(t.Context(), "dep", "redis", []int{0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	script, err := scripts.RunScript()
	if err != nil {
		t.Fatal(err)
	}

	input := append(bytes.Clone(script), command...)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	result, err := sess.Run(ctx, 0, opRunPrepare, []string{task, user}, bytes.NewReader(input))
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	return result.ExitCode, vm
}

func bashCanary() []byte { return []byte(`touch "$CERTPATCH_ROOT/ran"` + "\n") }

// A refused prepare that travels the production path, the command runner
// over a real SSH exec of the real script under real bash, answers with
// the refusal status and never runs the command on stdin. A prepare that
// succeeds stages the command without running it. The server needs GNU
// tools, so the test skips off Linux. It also needs root, because the
// script refuses directories that root does not own, so as anyone else
// the control's prepare is refused and every refusal case passes for
// that reason rather than its own.
func TestPrepareRefusalThroughSSHNeverRunsTheCommand(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the exact RunInput needs GNU tools")
	}

	if os.Geteuid() != 0 {
		t.Skip("run.sh checks for root ownership, so this test must run as root")
	}

	t.Run("existing task directory", func(t *testing.T) {
		root := bashTree(t)
		dir := filepath.Join(root, "var/vcap/data/runcmd", runTaskID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(filepath.Join(root, "var/vcap/data/runcmd"), 0o711)
		_ = os.WriteFile(filepath.Join(dir, "cmd.sh"), []byte("first\n"), 0o600)

		code, vm := bashPrepare(t, root, runTaskID, "vcap", bashCanary())
		if code != scripts.ExitRefused {
			t.Errorf("exit %d, want %d", code, scripts.ExitRefused)
		}

		vm.mu.Lock()
		execs := slices.Clone(vm.execs)
		vm.mu.Unlock()

		if len(execs) != 1 || !strings.HasPrefix(execs[0], bashInvoke) {
			t.Errorf("the server saw %d execs, want one that starts with the sudo bash invocation", len(execs))
		}

		if _, err := os.Lstat(filepath.Join(root, "ran")); err == nil {
			t.Error("the command on stdin ran")
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "cmd.sh")); string(got) != "first\n" {
			t.Error("the refusal changed the existing cmd.sh")
		}
	})

	t.Run("a user other than root and vcap", func(t *testing.T) {
		root := bashTree(t)

		code, _ := bashPrepare(t, root, runTaskID, "nobody", bashCanary())
		if code != scripts.ExitUsage {
			t.Errorf("exit %d, want %d", code, scripts.ExitUsage)
		}
		if _, err := os.Lstat(filepath.Join(root, "ran")); err == nil {
			t.Error("the command on stdin ran")
		}
	})

	t.Run("world-writable data dir", func(t *testing.T) {
		root := bashTree(t)
		_ = os.Chmod(filepath.Join(root, "var/vcap/data"), 0o777)

		code, _ := bashPrepare(t, root, runTaskID, "vcap", bashCanary())
		if code != scripts.ExitRefused {
			t.Errorf("exit %d, want %d", code, scripts.ExitRefused)
		}
		if _, err := os.Lstat(filepath.Join(root, "ran")); err == nil {
			t.Error("the command on stdin ran")
		}
	})

	t.Run("control: a prepare that succeeds stages without running", func(t *testing.T) {
		root := bashTree(t)

		code, _ := bashPrepare(t, root, runTaskID, "vcap", bashCanary())
		staged, err := os.ReadFile(filepath.Join(root, "var/vcap/data/runcmd", runTaskID, "cmd.sh"))
		if code != 0 || err != nil || !bytes.Equal(staged, bashCanary()) {
			t.Errorf("exit %d, staged ok=%v", code, err == nil && bytes.Equal(staged, bashCanary()))
		}
		if _, err := os.Lstat(filepath.Join(root, "ran")); err == nil {
			t.Error("the staged command ran during prepare")
		}
	})
}
