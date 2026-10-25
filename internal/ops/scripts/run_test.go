package scripts_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"blacksmith/internal/ops/scripts"
)

const (
	runTask = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	runOps  = "var/vcap/data/runcmd"

	// vcapGID is the group the fake vcap home gets when the tests run
	// as root, which can hand a directory to any group.
	vcapGID = 1000

	// dispatch is the last line of run.sh, which the tests that call
	// one function of the script leave out.
	dispatch = "_rc_main \"$@\"; exit\n"

	// darwinShim stands in for the GNU tools that macOS lacks, and for
	// root, which the tests cannot be on a developer's Mac. stat -c
	// becomes BSD stat -f, and an owner is always reported as root.
	// realpath loses its -e. Everything else the script runs is the
	// real tool. It is spliced in after lib.sh, and only on macOS.
	darwinShim = `
stat() {
	if [ "$1" = -c ]; then
		local fmt=$2
		shift 2
		[ "${1:-}" = -- ] && shift
		fmt=${fmt//%u/0}
		fmt=${fmt//%a/%Lp}
		command stat -f "$fmt" "$@"
		return
	fi
	command stat "$@"
}
realpath() {
	[ "${1:-}" = -e ] && shift
	[ "${1:-}" = -- ] && shift
	command realpath "$@"
}
`
)

// requireRun skips a test on a host that cannot run run.sh. Linux needs
// root, because the script checks that its directories belong to root,
// and macOS runs the script with darwinShim in place of the GNU tools.
func requireRun(t *testing.T) {
	t.Helper()

	switch runtime.GOOS {
	case "linux":
		if os.Geteuid() != 0 {
			t.Skip("run.sh checks for root ownership, so this test must run as root")
		}
	case "darwin":
	default:
		t.Skip("run.sh needs a Unix with bash")
	}
}

// bashPath is the shell the tests run the script with. Linux has the
// /bin/bash that the broker names, and macOS has only an old one there.
func bashPath(t *testing.T) string {
	t.Helper()

	if runtime.GOOS == "linux" {
		return "/bin/bash"
	}

	path, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash on this host")
	}

	return path
}

// runBody returns run.sh without its last line.
func runBody(t *testing.T) []byte {
	t.Helper()

	body, err := os.ReadFile("run.sh")
	if err != nil {
		t.Fatal(err)
	}

	rest, found := bytes.CutSuffix(body, []byte(dispatch))
	if !found {
		t.Fatal("run.sh does not end with the dispatch line")
	}

	return rest
}

// stdinFor returns the stdin for one run of the script. It is what
// RunInput builds, except on macOS, where darwinShim sits between lib.sh
// and run.sh.
func stdinFor(t *testing.T, command []byte) io.Reader {
	t.Helper()

	if runtime.GOOS != "darwin" {
		stdin, err := scripts.RunInput(command)
		if err != nil {
			t.Fatal(err)
		}

		return stdin
	}

	body, err := os.ReadFile("run.sh")
	if err != nil {
		t.Fatal(err)
	}

	var text bytes.Buffer

	text.Write(scripts.Library())
	text.WriteString(darwinShim)
	text.Write(body)
	text.Write(command)

	return &text
}

// runTree is a fake root with the directories run.sh looks for.
type runTree struct {
	root string
	gid  int

	// env is added to the environment of every run of the script.
	env []string

	// setpgid starts the script as the leader of its own process group,
	// which is how the broker's SSH session can leave it.
	setpgid bool
}

func newRunTree(t *testing.T) *runTree {
	t.Helper()
	requireRun(t)

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{"var/vcap/data", "home/vcap", "usr/bin", "root"} {
		err = os.MkdirAll(filepath.Join(root, dir), 0o755) //nolint:gosec // a test tree
		if err != nil {
			t.Fatal(err)
		}
	}

	tree := &runTree{root: root, gid: os.Getgid()}

	if os.Geteuid() == 0 {
		tree.gid = vcapGID

		err = os.Chown(filepath.Join(root, "home/vcap"), vcapGID, vcapGID)
		if err != nil {
			t.Fatal(err)
		}
	}

	return tree
}

func (r *runTree) path(parts ...string) string {
	return filepath.Join(append([]string{r.root}, parts...)...)
}

// run runs the script as the broker does, with args after the dashes and
// stdin as given. It returns the exit code and what was printed.
func (r *runTree) run(t *testing.T, stdin io.Reader, args ...string) (int, string, string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	argv := append([]string{"-s", "--"}, args...)

	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, bashPath(t), argv...) //nolint:gosec // the script under test
	cmd.Env = append([]string{"CERTPATCH_ROOT=" + r.root, "HOME=" + r.root}, r.env...)
	cmd.Stdin = stdin
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// A command that outlives the script, such as a sleep, would
	// otherwise hold the output pipes open past the deadline.
	cmd.WaitDelay = 2 * time.Second

	if r.setpgid {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}

	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("%v blocked until its deadline", args)
	}

	if err == nil {
		return 0, stdout.String(), stderr.String()
	}

	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("%v: %v", args, err)
	}

	return exit.ExitCode(), stdout.String(), stderr.String()
}

// prepare runs the prepare operation with command on stdin.
func (r *runTree) prepare(t *testing.T, task, user string, command []byte) (int, string) {
	t.Helper()

	code, out, stderr := r.run(t, stdinFor(t, command), "prepare", task, user)
	if code != 0 {
		t.Logf("prepare exited %d; stderr: %s", code, stderr)
	}

	return code, out
}

// cleanup runs the cleanup operation.
func (r *runTree) cleanup(t *testing.T, task string) (int, string) {
	t.Helper()

	code, out, stderr := r.run(t, stdinFor(t, nil), "cleanup", task)
	if code != 0 {
		t.Logf("cleanup exited %d; stderr: %s", code, stderr)
	}

	return code, out
}

// snippet runs shell text after the whole script but its dispatch line, so
// a test can call one function of the script. CERTPATCH_ROOT is the
// tree's root unless noRoot.
func (r *runTree) snippet(t *testing.T, noRoot bool, text string) (int, string) {
	t.Helper()

	var script bytes.Buffer

	script.Write(scripts.Library())

	if runtime.GOOS == "darwin" {
		script.WriteString(darwinShim)
	}

	script.Write(runBody(t))
	script.WriteString(text)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, bashPath(t), "-s", "--") //nolint:gosec // the script under test
	cmd.Env = []string{"HOME=" + r.root}

	if !noRoot {
		cmd.Env = append(cmd.Env, "CERTPATCH_ROOT="+r.root)
	}

	cmd.Stdin = &script
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err == nil {
		return 0, stdout.String()
	}

	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatal(err)
	}

	t.Logf("snippet exited %d; stderr: %s", exit.ExitCode(), stderr.String())

	return exit.ExitCode(), stdout.String()
}

// canaryCommand would leave a mark if the unread stdin ever ran.
var canaryCommand = []byte(`touch "$CERTPATCH_ROOT/ran"` + "\n")

// requireNoCanary fails the test when canaryCommand ran.
func (r *runTree) requireNoCanary(t *testing.T) {
	t.Helper()

	_, err := os.Lstat(r.path("ran"))
	if err == nil {
		t.Error("the command on stdin ran")
	}
}

// cmdFile is the path of a task's staged command.
func (r *runTree) cmdFile(task string) string {
	return r.path(runOps, task, "cmd.sh")
}

// makeTask builds a task directory by hand, as prepare and exec leave it.
func (r *runTree) makeTask(t *testing.T, task string, files map[string]string) {
	t.Helper()

	base := r.path(runOps)

	for _, dir := range []struct {
		path string
		mode os.FileMode
	}{{base, 0o711}, {filepath.Join(base, task), 0o710}} {
		err := os.MkdirAll(dir.path, 0o755) //nolint:gosec // a test tree
		if err != nil {
			t.Fatal(err)
		}

		err = os.Chmod(dir.path, dir.mode)
		if err != nil {
			t.Fatal(err)
		}
	}

	for name, content := range files {
		err := os.WriteFile(filepath.Join(base, task, name), []byte(content), 0o600)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func gidOf(t *testing.T, info os.FileInfo) int {
	t.Helper()

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("no Stat_t on this host")
	}

	return int(stat.Gid)
}

func randomBytes(t *testing.T, size int) []byte {
	t.Helper()

	out := make([]byte, size)

	_, err := rand.Read(out)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func TestRunInputIsScriptThenCommandExactly(t *testing.T) {
	t.Parallel()

	lib := scripts.Library()

	body, err := os.ReadFile("run.sh")
	if err != nil {
		t.Fatal(err)
	}

	script, err := scripts.RunScript()
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(script, slices.Concat(lib, body)) {
		t.Error("RunScript is not lib.sh followed by run.sh")
	}

	for _, command := range [][]byte{nil, []byte("\nexit 3"), []byte("echo $HOME\n"), randomBytes(t, 4096)} {
		stdin, err := scripts.RunInput(command)
		if err != nil {
			t.Fatal(err)
		}

		got, err := io.ReadAll(stdin)
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(got, slices.Concat(script, command)) {
			t.Errorf("RunInput(%d bytes) adds or drops a byte", len(command))
		}
	}
}

func TestRunScriptEndsWithDispatchLine(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("run.sh")
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.HasSuffix(body, []byte("\n"+dispatch)) {
		t.Errorf("run.sh does not end with %q", dispatch)
	}

	if bytes.Count(body, []byte(dispatch)) != 1 {
		t.Error("run.sh holds the dispatch line more than once")
	}
}

func TestRunValidators(t *testing.T) {
	t.Parallel()

	hash := strings.Repeat("ab", 32)

	cases := []struct {
		name  string
		check func(string) error
		ok    []string
		bad   []string
	}{
		{"user", scripts.ValidRunUser, []string{"root", "vcap"}, []string{"", "Root", "vcap ", "bosh_admin", "root\n", "0"}},
		{
			"seconds", scripts.ValidRunSeconds,
			[]string{"10", "600", "3600"},
			[]string{"", "9", "3601", "-10", "+10", "1e3", "010", "10.0", " 10", "10 ", "99999999999999999999", "٣٠"},
		},
		{
			"digest", scripts.ValidRunDigest,
			[]string{hash, strings.Repeat("0", 64)},
			[]string{"", strings.ToUpper(hash), hash + "a", hash[1:], strings.Repeat("g", 64), hash + "\n"},
		},
	}

	for _, tc := range cases {
		for _, in := range tc.ok {
			if err := tc.check(in); err != nil {
				t.Errorf("%s %q: %v", tc.name, in, err)
			}
		}

		for _, in := range tc.bad {
			err := tc.check(in)
			if !errors.Is(err, scripts.ErrRefused) {
				t.Errorf("%s %q: err = %v, want ErrRefused", tc.name, in, err)
			}
		}
	}
}

func TestRunOperationsBuildInvocations(t *testing.T) {
	t.Parallel()

	const program = "sudo -n /bin/bash -s --"

	hash := strings.Repeat("cd", 32)

	got, argv, err := scripts.OpRunPrepare(runTask, "vcap")
	if err != nil || got != program || !slices.Equal(argv, []string{"prepare", runTask, "vcap"}) {
		t.Errorf("prepare = %q %q %v", got, argv, err)
	}

	got, argv, err = scripts.OpRunExec(runTask, "root", "600", hash)
	if err != nil || got != program || !slices.Equal(argv, []string{"exec", runTask, "root", "600", hash}) {
		t.Errorf("exec = %q %q %v", got, argv, err)
	}

	got, argv, err = scripts.OpRunCleanup(runTask)
	if err != nil || got != program || !slices.Equal(argv, []string{"cleanup", runTask}) {
		t.Errorf("cleanup = %q %q %v", got, argv, err)
	}

	_, _, err = scripts.OpRunPrepare("not-a-task", "vcap")
	if !errors.Is(err, scripts.ErrRefused) {
		t.Errorf("a bad task: %v", err)
	}

	_, _, err = scripts.OpRunPrepare(runTask, "nobody")
	if !errors.Is(err, scripts.ErrRefused) {
		t.Errorf("a bad user: %v", err)
	}

	_, _, err = scripts.OpRunExec(runTask, "vcap", "9", hash)
	if !errors.Is(err, scripts.ErrRefused) {
		t.Errorf("a bad timeout: %v", err)
	}

	_, _, err = scripts.OpRunExec(runTask, "vcap", "60", "abc")
	if !errors.Is(err, scripts.ErrRefused) {
		t.Errorf("a bad digest: %v", err)
	}

	_, _, err = scripts.OpRunCleanup("../x")
	if !errors.Is(err, scripts.ErrRefused) {
		t.Errorf("cleanup of a bad task: %v", err)
	}
}

func TestRunRecordsParse(t *testing.T) {
	t.Parallel()

	hash := strings.Repeat("ef", 32)
	out := fmt.Sprintf("staged bytes=12 sha256=%s\ncleanup started=yes exit=7 killed=no\ncleanup started=no exit=- killed=no\n", hash)

	records, err := scripts.ParseRecords([]byte(out), scripts.RunKinds())
	if err != nil {
		t.Fatal(err)
	}

	if len(records) != 3 || records[0].Kind != scripts.RecordStaged || records[1].Fields["exit"] != "7" ||
		records[2].Fields["started"] != "no" {
		t.Errorf("records = %+v", records)
	}

	_, err = scripts.ParseRecords([]byte("staged bytes=1 extra=2\n"), scripts.RunKinds())
	if err == nil {
		t.Error("a staged record with an unknown key parsed")
	}
}

func TestPrepareStagesTheCommandByteForByte(t *testing.T) {
	t.Parallel()

	cases := map[string][]byte{
		"a leading newline":    []byte("\n\necho hi\n"),
		"no trailing newline":  []byte("echo hi"),
		"exit on first line":   []byte("exit\necho never\n"),
		"shell metacharacters": []byte("$(id) `id` ; | & > < ' \" \\ ${X:?} *? $'\\n' !! \n"),
		"carriage returns":     []byte("echo a\r\necho b\r\n"),
		"a here document":      []byte("cat <<'EOF'\n_rc_main\nEOF\n"),
		"64 KiB random bytes":  randomBytes(t, 64*1024),
		"an empty command":     {},
	}

	for name, command := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)

			code, out := tree.prepare(t, runTask, "vcap", command)
			if code != 0 {
				t.Fatalf("exit = %d", code)
			}

			got, err := os.ReadFile(tree.cmdFile(runTask))
			if err != nil {
				t.Fatal(err)
			}

			if !bytes.Equal(got, command) {
				t.Errorf("cmd.sh holds %d bytes that differ from the %d sent", len(got), len(command))
			}

			records, err := scripts.ParseRecords([]byte(out), scripts.RunKinds())
			if err != nil || len(records) != 1 || records[0].Kind != scripts.RecordStaged {
				t.Fatalf("output %q: %v", out, err)
			}

			sum := sha256.Sum256(command)

			if records[0].Fields["sha256"] != hex.EncodeToString(sum[:]) {
				t.Errorf("sha256 = %s, want %x", records[0].Fields["sha256"], sum)
			}

			if records[0].Fields["bytes"] != strconv.Itoa(len(command)) {
				t.Errorf("bytes = %s, want %d", records[0].Fields["bytes"], len(command))
			}
		})
	}
}

func TestPrepareSetsModesAndGroup(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)

	if code, _ := tree.prepare(t, runTask, "root", []byte("true\n")); code != 0 {
		t.Fatalf("exit = %d", code)
	}

	for _, want := range []struct {
		path string
		mode os.FileMode
		dir  bool
	}{
		{tree.path(runOps), 0o711, true},
		{tree.path(runOps, runTask), 0o710, true},
		{tree.cmdFile(runTask), 0o640, false},
	} {
		info, err := os.Stat(want.path)
		if err != nil {
			t.Fatal(err)
		}

		if info.Mode().Perm() != want.mode || info.IsDir() != want.dir {
			t.Errorf("%s = %v, want %v", want.path, info.Mode(), want.mode)
		}

		if want.path != tree.path(runOps) && gidOf(t, info) != tree.gid {
			t.Errorf("%s group = %d, want %d", want.path, gidOf(t, info), tree.gid)
		}
	}

	entries, err := os.ReadDir(tree.path(runOps, runTask))
	if err != nil || len(entries) != 1 {
		t.Errorf("the task directory holds %v, %v; want only cmd.sh", entries, err)
	}
}

func TestPrepareRefusals(t *testing.T) {
	t.Parallel()

	t.Run("a task ID that valid_task rejects", func(t *testing.T) {
		t.Parallel()

		tree := newRunTree(t)

		for _, task := range []string{"", "../x", "0A1B2C3D-4E5F-4A6B-8C7D-9E0F1A2B3C4D", runTask + "x", "a b"} {
			code, _ := tree.prepare(t, task, "vcap", canaryCommand)
			if code != scripts.ExitUsage {
				t.Errorf("task %q exited %d, want %d", task, code, scripts.ExitUsage)
			}
		}

		tree.requireNoCanary(t)

		_, err := os.Lstat(tree.path(runOps))
		if err == nil {
			t.Error("a refused task left the runcmd directory behind")
		}
	})

	t.Run("a user other than root and vcap", func(t *testing.T) {
		t.Parallel()

		tree := newRunTree(t)

		code, _ := tree.prepare(t, runTask, "nobody", canaryCommand)
		if code != scripts.ExitUsage {
			t.Errorf("exit = %d, want %d", code, scripts.ExitUsage)
		}

		tree.requireNoCanary(t)
	})

	t.Run("a wrong argument count", func(t *testing.T) {
		t.Parallel()

		tree := newRunTree(t)

		code, _, _ := tree.run(t, stdinFor(t, nil), "prepare", runTask)
		if code != scripts.ExitUsage {
			t.Errorf("exit = %d, want %d", code, scripts.ExitUsage)
		}
	})

	t.Run("an unknown operation", func(t *testing.T) {
		t.Parallel()

		tree := newRunTree(t)

		code, _, _ := tree.run(t, stdinFor(t, nil), "exec", runTask)
		if code != scripts.ExitUsage {
			t.Errorf("exit = %d, want %d", code, scripts.ExitUsage)
		}
	})

	t.Run("an existing directory", func(t *testing.T) {
		t.Parallel()

		tree := newRunTree(t)
		tree.makeTask(t, runTask, map[string]string{"cmd.sh": "first\n"})

		code, out := tree.prepare(t, runTask, "vcap", canaryCommand)
		if code != scripts.ExitRefused {
			t.Errorf("exit = %d, want %d", code, scripts.ExitRefused)
		}

		if out != "" {
			t.Errorf("a refusal printed %q", out)
		}

		tree.requireNoCanary(t)

		got, err := os.ReadFile(tree.cmdFile(runTask))
		if err != nil || string(got) != "first\n" {
			t.Errorf("the existing cmd.sh is now %q, %v", got, err)
		}

		entries, _ := os.ReadDir(tree.path(runOps))
		if len(entries) != 1 {
			t.Errorf("runcmd holds %d entries, want 1", len(entries))
		}
	})

	t.Run("a symlink at the task directory", func(t *testing.T) {
		t.Parallel()

		tree := newRunTree(t)
		tree.makeTask(t, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", nil)

		err := os.Symlink(tree.path("home"), tree.path(runOps, runTask))
		if err != nil {
			t.Fatal(err)
		}

		code, _ := tree.prepare(t, runTask, "vcap", []byte("true\n"))
		if code != scripts.ExitRefused {
			t.Errorf("exit = %d, want %d", code, scripts.ExitRefused)
		}

		entries, _ := os.ReadDir(tree.path("home"))
		if len(entries) != 1 {
			t.Errorf("home holds %d entries after a symlinked task directory", len(entries))
		}
	})

	t.Run("a runcmd that is a symlink", func(t *testing.T) {
		t.Parallel()

		tree := newRunTree(t)

		err := os.Symlink(tree.path("home"), tree.path(runOps))
		if err != nil {
			t.Fatal(err)
		}

		code, _ := tree.prepare(t, runTask, "vcap", []byte("true\n"))
		if code != scripts.ExitRefused {
			t.Errorf("exit = %d, want %d", code, scripts.ExitRefused)
		}

		entries, _ := os.ReadDir(tree.path("home"))
		if len(entries) != 1 {
			t.Errorf("home holds %d entries after a symlinked runcmd", len(entries))
		}
	})

	t.Run("a runcmd with the wrong mode", func(t *testing.T) {
		t.Parallel()

		tree := newRunTree(t)
		tree.makeTask(t, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", nil)

		err := os.Chmod(tree.path(runOps), 0o755)
		if err != nil {
			t.Fatal(err)
		}

		code, _ := tree.prepare(t, runTask, "vcap", []byte("true\n"))
		if code != scripts.ExitRefused {
			t.Errorf("exit = %d, want %d", code, scripts.ExitRefused)
		}
	})

	t.Run("a data directory others can write", func(t *testing.T) {
		t.Parallel()

		tree := newRunTree(t)

		err := os.Chmod(tree.path("var/vcap/data"), 0o777) //nolint:gosec // the case under test
		if err != nil {
			t.Fatal(err)
		}

		code, out := tree.prepare(t, runTask, "vcap", canaryCommand)
		if code != scripts.ExitRefused || out != "" {
			t.Errorf("exit = %d, output %q; want %d and none", code, out, scripts.ExitRefused)
		}

		tree.requireNoCanary(t)

		_, err = os.Lstat(tree.path(runOps))
		if err == nil {
			t.Error("a refused chain left runcmd behind")
		}
	})

	t.Run("a vcap home owned by group 0", func(t *testing.T) {
		t.Parallel()

		tree := newRunTree(t)
		if os.Geteuid() != 0 {
			t.Skip("only root can give the fake home to group 0")
		}

		err := os.Chown(tree.path("home/vcap"), 0, 0)
		if err != nil {
			t.Fatal(err)
		}

		code, _ := tree.prepare(t, runTask, "vcap", []byte("true\n"))
		if code != scripts.ExitRefused {
			t.Errorf("exit = %d, want %d", code, scripts.ExitRefused)
		}

		_, err = os.Lstat(tree.path(runOps, runTask))
		if err == nil {
			t.Error("a refusal left the task directory behind")
		}
	})
}

func TestPrepareRemovesWhatItMadeWhenItStopsEarly(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)

	// chmod fails after prepare has made the task directory, so the EXIT
	// trap must remove it.
	code, _ := tree.snippet(t, false, "chmod() { return 1; }\nprepare "+runTask+" vcap < /dev/null\n")
	if code == 0 {
		t.Error("prepare succeeded with a failing chmod")
	}

	_, err := os.Lstat(tree.path(runOps, runTask))
	if err == nil {
		t.Error("the task directory is still there")
	}

	// A home that is gone stops prepare before it makes anything.
	err = os.Remove(tree.path("home/vcap"))
	if err != nil {
		t.Fatal(err)
	}

	got, _ := tree.prepare(t, runTask, "vcap", []byte("true\n"))
	if got != scripts.ExitRefused {
		t.Errorf("exit = %d, want %d", got, scripts.ExitRefused)
	}

	_, err = os.Lstat(tree.path(runOps, runTask))
	if err == nil {
		t.Error("a refusal left the task directory behind")
	}
}

func TestCleanupReportsWhatTheFilesSay(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"exec never ran", map[string]string{"cmd.sh": "true\n"}, "cleanup started=no exit=- killed=no\n"},
		{"it ran and exited 7", map[string]string{"cmd.sh": "x", "started": "", "exit": "7\n"}, "cleanup started=yes exit=7 killed=no\n"},
		{"it started with no exit", map[string]string{"started": "", "pgid": "123\n"}, "cleanup started=yes exit=- killed=no\n"},
		{"an exit file with junk", map[string]string{"started": "", "exit": "7; id\n"}, "cleanup started=yes exit=- killed=no\n"},
		{"an exit file that is empty", map[string]string{"started": "", "exit": ""}, "cleanup started=yes exit=- killed=no\n"},
		{"an exit of 255", map[string]string{"started": "", "exit": "255\n"}, "cleanup started=yes exit=255 killed=no\n"},
		{"an exit of 256", map[string]string{"started": "", "exit": "256\n"}, "cleanup started=yes exit=- killed=no\n"},
		{"an exit of four digits", map[string]string{"started": "", "exit": "1000\n"}, "cleanup started=yes exit=- killed=no\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)
			tree.makeTask(t, runTask, tc.files)

			code, out := tree.cleanup(t, runTask)
			if code != 0 {
				t.Fatalf("exit = %d", code)
			}

			if out != tc.want {
				t.Errorf("output = %q, want %q", out, tc.want)
			}

			_, err := os.Lstat(tree.path(runOps, runTask))
			if err == nil {
				t.Error("the task directory is still there")
			}

			_, err = scripts.ParseRecords([]byte(out), scripts.RunKinds())
			if err != nil {
				t.Errorf("the output does not parse: %v", err)
			}
		})
	}
}

func TestCleanupGuards(t *testing.T) {
	t.Parallel()

	t.Run("a bad task ID", func(t *testing.T) {
		t.Parallel()

		tree := newRunTree(t)

		code, _ := tree.cleanup(t, "../..")
		if code != scripts.ExitUsage {
			t.Errorf("exit = %d, want %d", code, scripts.ExitUsage)
		}
	})

	t.Run("no directory", func(t *testing.T) {
		t.Parallel()

		tree := newRunTree(t)

		code, out := tree.cleanup(t, runTask)
		if code != 0 || out != "cleanup started=- exit=- killed=no\n" {
			t.Errorf("exit = %d, output = %q; want the unknown record", code, out)
		}

		result, err := scripts.ParseCleanup([]byte(out))
		if err != nil || result.Started != "-" {
			t.Errorf("ParseCleanup = %+v, %v", result, err)
		}
	})

	t.Run("a symlink is never followed or removed", func(t *testing.T) {
		t.Parallel()

		tree := newRunTree(t)
		tree.makeTask(t, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", nil)

		victim := tree.path("home/vcap")

		err := os.WriteFile(filepath.Join(victim, "keep"), []byte("x"), 0o600)
		if err != nil {
			t.Fatal(err)
		}

		err = os.Symlink(victim, tree.path(runOps, runTask))
		if err != nil {
			t.Fatal(err)
		}

		code, _ := tree.cleanup(t, runTask)
		if code != scripts.ExitRefused {
			t.Errorf("exit = %d, want %d", code, scripts.ExitRefused)
		}

		_, err = os.Stat(filepath.Join(victim, "keep"))
		if err != nil {
			t.Errorf("cleanup reached through the symlink: %v", err)
		}
	})
}

func TestWriteRefusesAnExistingName(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)

	target := tree.path("home/vcap/target")

	err := os.WriteFile(target, []byte("old"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	link := tree.path("var/vcap/data/cmd.sh")

	err = os.Symlink(target, link)
	if err != nil {
		t.Fatal(err)
	}

	code, _ := tree.snippet(t, false, `echo "new" | _rc_write "`+link+`"`+"\n")
	if code == 0 {
		t.Error("_rc_write wrote through a symlink")
	}

	got, err := os.ReadFile(target)
	if err != nil || string(got) != "old" {
		t.Errorf("the symlink target is now %q, %v", got, err)
	}

	plain := tree.path("var/vcap/data/plain")

	err = os.WriteFile(plain, []byte("old"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	code, _ = tree.snippet(t, false, `echo "new" | _rc_write "`+plain+`"`+"\n")
	if code == 0 {
		t.Error("_rc_write replaced an existing file")
	}

	fresh := tree.path("var/vcap/data/fresh")

	code, _ = tree.snippet(t, false, `echo "new" | _rc_write "`+fresh+`"`+"\n")
	if code != 0 {
		t.Errorf("_rc_write refused a new name: exit %d", code)
	}
}

func TestCheckDirRefusesSymlinksAndMismatches(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	tree.makeTask(t, runTask, nil)

	dir := tree.path(runOps, runTask)
	link := tree.path("var/vcap/data/link")

	err := os.Symlink(dir, link)
	if err != nil {
		t.Fatal(err)
	}

	gid := strconv.Itoa(gidOf(t, mustStat(t, dir)))

	for _, tc := range []struct {
		name string
		call string
		ok   bool
	}{
		{"the real directory", `_rc_check_dir "` + dir + `" 0 ` + gid + ` 710`, true},
		{"any group", `_rc_check_dir "` + dir + `" 0 - 710`, true},
		{"a symlink to it", `_rc_check_dir "` + link + `" 0 ` + gid + ` 710`, false},
		{"the wrong mode", `_rc_check_dir "` + dir + `" 0 ` + gid + ` 700`, false},
		{"the wrong owner", `_rc_check_dir "` + dir + `" 1 ` + gid + ` 710`, false},
		{"the wrong group", `_rc_check_dir "` + dir + `" 0 99999 710`, false},
		{"a file", `_rc_check_dir "` + tree.path("var/vcap") + `/nothing" 0 - 710`, false},
	} {
		code, _ := tree.snippet(t, false, tc.call+"\n")
		if (code == 0) != tc.ok {
			t.Errorf("%s: exit %d, want ok=%v", tc.name, code, tc.ok)
		}
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	return info
}

func TestRCToolResolvesUnderTheFakeRoot(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)

	for _, name := range []string{"getent", "setpriv", "setsid", "timeout"} {
		code, out := tree.snippet(t, false, `printf '%s' "$(_rc_tool `+name+`)"`+"\n")
		if code != 0 || out != tree.root+"/usr/bin/"+name {
			t.Errorf("with the root, %s = %q (exit %d), want %s/usr/bin/%s", name, out, code, tree.root, name)
		}

		code, out = tree.snippet(t, true, `printf '%s' "$(_rc_tool `+name+`)"`+"\n")
		if code != 0 || out != name {
			t.Errorf("without the root, %s = %q (exit %d), want the bare name", name, out, code)
		}
	}
}

func TestRCVcapGIDComesFromTheFakeHome(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)

	code, out := tree.snippet(t, false, "_rc_vcap_gid\n")
	if code != 0 || out != strconv.Itoa(tree.gid) {
		t.Errorf("_rc_vcap_gid = %q (exit %d), want %d", out, code, tree.gid)
	}
}

func TestWriteRefusesSpecialFiles(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)

	fifo := tree.path("home/vcap/fifo")

	err := syscall.Mkfifo(fifo, 0o600)
	if err != nil {
		t.Skipf("no FIFOs here: %v", err)
	}

	for name, target := range map[string]string{
		"a symlink to a FIFO":   fifo,
		"a dangling symlink":    tree.path("home/vcap/missing"),
		"a symlink to the root": tree.root,
	} {
		link := tree.path("var/vcap/data", strings.ReplaceAll(name, " ", "-"))

		err := os.Symlink(target, link)
		if err != nil {
			t.Fatal(err)
		}

		// Without the refusal, the open of the FIFO blocks until the
		// 20 second deadline of the run.
		code, _ := tree.snippet(t, false, `echo "new" | _rc_write "`+link+`"`+"\n")
		if code == 0 {
			t.Errorf("%s: _rc_write succeeded", name)
		}

		_, err = os.Lstat(target)
		if name == "a dangling symlink" && err == nil {
			t.Errorf("%s: the target was created", name)
		}
	}

	info := mustStat(t, fifo)
	if info.Mode().Perm() != 0o600 || info.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("the FIFO is now %v", info.Mode())
	}
}

func TestPrepareChecksTheFileBeforeChangingIt(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)

	victim := tree.path("home/vcap/victim")

	err := os.WriteFile(victim, []byte("keep"), 0o644) //nolint:gosec // a test file
	if err != nil {
		t.Fatal(err)
	}

	// _rc_write is replaced by one that plants a symlink and reports
	// success, as a write through a link would. prepare must refuse it
	// before chgrp and chmod touch the victim.
	text := "_rc_write() { ln -s " + victim + ` "$1"; }` + "\nprepare " + runTask + " vcap < /dev/null\n"

	code, _ := tree.snippet(t, false, text)
	if code != scripts.ExitRefused {
		t.Errorf("exit = %d, want %d", code, scripts.ExitRefused)
	}

	if mustStat(t, victim).Mode().Perm() != 0o644 {
		t.Error("prepare changed the mode of a file behind a symlink")
	}
}

func TestPrepareClearsSetgidFromTheParent(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("macOS does not pass the setgid bit to new directories")
	}

	tree := newRunTree(t)

	err := os.Chmod(tree.path("var/vcap/data"), 0o755|os.ModeSetgid)
	if err != nil {
		t.Fatal(err)
	}

	code, _ := tree.prepare(t, runTask, "vcap", []byte("true\n"))
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}

	for _, path := range []string{tree.path(runOps), tree.path(runOps, runTask)} {
		if mustStat(t, path).Mode()&os.ModeSetgid != 0 {
			t.Errorf("%s kept the setgid bit", path)
		}
	}
}

func TestParseCleanupChecksValues(t *testing.T) {
	t.Parallel()

	for _, ok := range []string{
		"cleanup started=yes exit=7 killed=no\n",
		"cleanup started=no exit=- killed=no\n",
		"cleanup started=- exit=- killed=no\n",
		"cleanup started=yes exit=255 killed=yes\n",
	} {
		_, err := scripts.ParseCleanup([]byte(ok))
		if err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}

	for _, bad := range []string{
		"cleanup started=maybe exit=7 killed=no\n",
		"cleanup started=yes exit=256 killed=no\n",
		"cleanup started=yes exit=07 killed=no\n",
		"cleanup started=yes exit=7 killed=-\n",
		"cleanup started=yes exit=7 killed=no\ncleanup started=no exit=- killed=no\n",
		"staged bytes=1 sha256=" + strings.Repeat("a", 64) + "\n",
		"",
	} {
		_, err := scripts.ParseCleanup([]byte(bad))
		if err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

// linkRunTools puts the real setsid and timeout under the fake root, where
// the script looks for them, and skips on a host without them.
func (r *runTree) linkRunTools(t *testing.T) {
	t.Helper()

	if runtime.GOOS != "linux" {
		t.Skip("setsid and timeout need Linux")
	}

	for _, tool := range []string{"setsid", "timeout"} {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("no %s on this host", tool)
		}

		err = os.Symlink(path, r.path("usr/bin", tool))
		if err != nil {
			t.Fatal(err)
		}
	}
}

func digestOf(command []byte) string {
	sum := sha256.Sum256(command)

	return hex.EncodeToString(sum[:])
}

// execRoot runs exec as root for a task.
func (r *runTree) execRoot(t *testing.T, seconds, digest string) (int, string, string) {
	t.Helper()

	// The bytes after the script stay unread, as the command's would on
	// an SSH channel that is still open, and must never reach the command.
	return r.run(t, stdinFor(t, []byte("trailing bytes\n")), "exec", runTask, "root", seconds, digest)
}

// stageAndExec stages command with prepare and runs it as root.
func (r *runTree) stageAndExec(t *testing.T, command string) (int, string, string) {
	t.Helper()
	r.linkRunTools(t)

	code, _ := r.prepare(t, runTask, "root", []byte(command))
	if code != 0 {
		t.Fatalf("prepare exited %d", code)
	}

	return r.execRoot(t, "30", digestOf([]byte(command)))
}

func (r *runTree) taskFile(t *testing.T, name string) string {
	t.Helper()

	got, err := os.ReadFile(r.path(runOps, runTask, name))
	if err != nil {
		t.Fatal(err)
	}

	return string(got)
}

func TestRunExecRootPassesOutputAndExitCode(t *testing.T) {
	t.Parallel()

	for _, want := range []int{0, 1, 13, 200} {
		t.Run(strconv.Itoa(want), func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)

			code, out, errOut := tree.stageAndExec(t, fmt.Sprintf("echo out\necho err >&2\nexit %d\n", want))
			if code != want {
				t.Fatalf("exit = %d, want %d; stderr %q", code, want, errOut)
			}

			if out != "out\n" || errOut != "err\n" {
				t.Errorf("stdout %q, stderr %q", out, errOut)
			}

			if got := tree.taskFile(t, "started"); got != "" {
				t.Errorf("started holds %q", got)
			}

			if got := tree.taskFile(t, "exit"); got != strconv.Itoa(want)+"\n" {
				t.Errorf("exit file holds %q", got)
			}

			pgid, err := strconv.Atoi(strings.TrimSpace(tree.taskFile(t, "pgid")))
			if err != nil || pgid <= 1 {
				t.Errorf("pgid file holds %q, %v", tree.taskFile(t, "pgid"), err)
			}
		})
	}
}

func TestRunExecRootEnvironmentAndSetup(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	tree.env = []string{"RUN_CANARY=leaked"}

	code, out, errOut := tree.stageAndExec(t, `printf 'canary=%s\n' "${RUN_CANARY-unset}"
for v in HOME USER LOGNAME SHELL PATH LANG LC_ALL TMPDIR; do printf '%s=%s\n' "$v" "${!v}"; done
compgen -e | grep -vxE 'PWD|SHLVL|_' | sort | tr '\n' ' '; echo
umask
pwd -P
`)
	if code != 0 {
		t.Fatalf("exit = %d; stderr %q", code, errOut)
	}

	want := strings.Join([]string{
		"canary=unset",
		"HOME=/root",
		"USER=root",
		"LOGNAME=root",
		"SHELL=/bin/bash",
		"PATH=/var/vcap/bosh/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"TMPDIR=/var/vcap/data/tmp",
		"HOME LANG LC_ALL LOGNAME PATH SHELL TMPDIR USER ",
		"0022",
		tree.path("root"),
	}, "\n") + "\n"
	if out != want {
		t.Errorf("the command saw\n%s\nwant\n%s", out, want)
	}
}

func TestRunExecRootStdinIsEndOfFile(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)

	code, out, errOut := tree.stageAndExec(t, "cat\n")
	if code != 0 || out != "" {
		t.Errorf("exit %d, stdout %q, stderr %q; want 0 and nothing", code, out, errOut)
	}
}

func TestRunExecRootTimeoutExits124(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	tree.linkRunTools(t)

	command := []byte("sleep 30\n")

	code, _ := tree.prepare(t, runTask, "root", command)
	if code != 0 {
		t.Fatalf("prepare exited %d", code)
	}

	start := time.Now()

	code, _, _ = tree.execRoot(t, "1", digestOf(command))
	if code != 124 {
		t.Errorf("exit = %d, want 124", code)
	}

	if time.Since(start) > 15*time.Second {
		t.Errorf("the deadline took %v", time.Since(start))
	}

	if got := tree.taskFile(t, "exit"); got != "124\n" {
		t.Errorf("exit file holds %q", got)
	}
}

func TestRunExecRootRefusals(t *testing.T) {
	t.Parallel()

	// The canary path is part of the command text, because env -i
	// removes every variable that could name it.

	for _, tc := range []struct {
		name   string
		break_ func(t *testing.T, tree *runTree)
		hash   func(command []byte) string
	}{
		{"a digest that does not match", func(*testing.T, *runTree) {}, func([]byte) string { return digestOf([]byte("other\n")) }},
		{"a file changed after prepare", func(t *testing.T, tree *runTree) {
			t.Helper()

			err := os.WriteFile(tree.cmdFile(runTask), []byte("changed\n"), 0o640)
			if err != nil {
				t.Fatal(err)
			}
		}, digestOf},
		{"a group-writable file", func(t *testing.T, tree *runTree) {
			t.Helper()

			err := os.Chmod(tree.cmdFile(runTask), 0o660)
			if err != nil {
				t.Fatal(err)
			}
		}, digestOf},
		{"a symlinked file", func(t *testing.T, tree *runTree) {
			t.Helper()

			err := os.Rename(tree.cmdFile(runTask), tree.path("real.sh"))
			if err != nil {
				t.Fatal(err)
			}

			err = os.Symlink(tree.path("real.sh"), tree.cmdFile(runTask))
			if err != nil {
				t.Fatal(err)
			}
		}, digestOf},
		{"a missing file", func(t *testing.T, tree *runTree) {
			t.Helper()

			err := os.Remove(tree.cmdFile(runTask))
			if err != nil {
				t.Fatal(err)
			}
		}, digestOf},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)
			tree.linkRunTools(t)

			command := canaryExec(tree)

			code, _ := tree.prepare(t, runTask, "root", command)
			if code != 0 {
				t.Fatalf("prepare exited %d", code)
			}

			tc.break_(t, tree)

			code, out, errOut := tree.execRoot(t, "30", tc.hash(command))
			if code != scripts.ExitRefused {
				t.Errorf("exit = %d, want %d", code, scripts.ExitRefused)
			}

			if out != "" || strings.Contains(errOut, "touch") || strings.Contains(errOut, tc.hash(command)) {
				t.Errorf("a refusal printed %q and %q", out, errOut)
			}

			tree.requireExecCanary(t, false)

			for _, name := range []string{"started", "pgid", "exit"} {
				_, err := os.Lstat(tree.path(runOps, runTask, name))
				if err == nil {
					t.Errorf("a refusal left %s behind", name)
				}
			}
		})
	}
}

func TestRunExecRootCanaryControl(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	tree.linkRunTools(t)

	command := canaryExec(tree)

	code, _ := tree.prepare(t, runTask, "root", command)
	if code != 0 {
		t.Fatalf("prepare exited %d", code)
	}

	code, _, errOut := tree.execRoot(t, "30", digestOf(command))
	if code != 0 {
		t.Fatalf("exit = %d; stderr %q", code, errOut)
	}

	tree.requireExecCanary(t, true)
}

func TestRunExecRootUsageErrors(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	good := strings.Repeat("a", 64)

	for _, args := range [][]string{
		{"exec", runTask, "root", "30"},
		{"exec", "nope", "root", "30", good},
		{"exec", runTask, "nobody", "30", good},
		{"exec", runTask, "root", "0", good},
		{"exec", runTask, "root", "x", good},
		{"exec", runTask, "root", "30", "ABC"},
	} {
		code, _, _ := tree.run(t, stdinFor(t, nil), args...)
		if code != scripts.ExitUsage {
			t.Errorf("%v exited %d, want %d", args, code, scripts.ExitUsage)
		}
	}
}

// canaryExec is a command that leaves a mark at an absolute path.
func canaryExec(r *runTree) []byte {
	return []byte(fmt.Sprintf("touch %q\n", r.path("ran")))
}

// requireExecCanary fails unless the mark of canaryExec exists exactly
// when want says it should.
func (r *runTree) requireExecCanary(t *testing.T, want bool) {
	t.Helper()

	_, err := os.Lstat(r.path("ran"))
	if (err == nil) != want {
		t.Errorf("the canary exists = %v, want %v", err == nil, want)
	}
}

func TestRunExecRootCommandLeadsItsOwnSessionAndGroup(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)

	code, _, errOut := tree.stageAndExec(t, `read -r _ _ _ _ pgrp sid _ < /proc/$$/stat
echo "$pgrp $sid" >&2
`)
	if code != 0 {
		t.Fatalf("exit = %d; stderr %q", code, errOut)
	}

	pgid := strings.TrimSpace(tree.taskFile(t, "pgid"))

	if errOut != pgid+" "+pgid+"\n" {
		t.Errorf("the command reported group and session %q, the pgid file holds %q", errOut, pgid)
	}

	if pgid == strconv.Itoa(syscall.Getpgrp()) {
		t.Errorf("the command stayed in the test's process group %s", pgid)
	}

	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Fatal(err)
	}

	// Fields after the parenthesised name are state, ppid, pgrp, session.
	_, rest, _ := bytes.Cut(stat, []byte(") "))
	fields := strings.Fields(string(rest))

	if len(fields) < 4 || pgid == fields[3] {
		t.Errorf("the command stayed in the test's session, fields %v", fields)
	}
}

func TestRunExecRootWaitsWhenTheWrapperLeadsItsGroup(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	tree.setpgid = true

	code, _, errOut := tree.stageAndExec(t, "sleep 1\nexit 7\n")
	if code != 7 {
		t.Fatalf("exit = %d, want 7; stderr %q", code, errOut)
	}

	if got := tree.taskFile(t, "exit"); got != "7\n" {
		t.Errorf("exit file holds %q", got)
	}

	pgid, err := strconv.Atoi(strings.TrimSpace(tree.taskFile(t, "pgid")))
	if err != nil || pgid <= 1 {
		t.Errorf("pgid file holds %q, %v", tree.taskFile(t, "pgid"), err)
	}
}

func TestRunExecRootLaunchFailureExits125(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	tree.linkRunTools(t)

	command := canaryExec(tree)

	code, _ := tree.prepare(t, runTask, "root", command)
	if code != 0 {
		t.Fatalf("prepare exited %d", code)
	}

	err := os.WriteFile(tree.path(runOps, runTask, "pgid"), nil, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	code, out, errOut := tree.execRoot(t, "30", digestOf(command))
	if code != 125 || out != "" || errOut != "run.sh: the command was not started\n" {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}

	tree.requireExecCanary(t, false)

	if got := tree.taskFile(t, "exit"); got != "125\n" {
		t.Errorf("exit file holds %q", got)
	}
}

func TestRunExecRootKeepsTheCommandsCodeWhenTheExitFileFails(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	tree.linkRunTools(t)

	command := []byte("exit 5\n")

	code, _ := tree.prepare(t, runTask, "root", command)
	if code != 0 {
		t.Fatalf("prepare exited %d", code)
	}

	err := os.WriteFile(tree.path(runOps, runTask, "exit"), []byte("old\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	code, _, _ = tree.execRoot(t, "30", digestOf(command))
	if code != 5 {
		t.Errorf("exit = %d, want 5", code)
	}
}

func TestRunExecRootKilledAfterTheDeadlineKeepsStderrClean(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)

	command := "echo err >&2\ntrap '' TERM\nsleep 30\n"
	tree.linkRunTools(t)

	code, _ := tree.prepare(t, runTask, "root", []byte(command))
	if code != 0 {
		t.Fatalf("prepare exited %d", code)
	}

	code, _, errOut := tree.execRoot(t, "1", digestOf([]byte(command)))
	if code != 137 || errOut != "err\n" {
		t.Errorf("exit %d, stderr %q; want 137 and only the command's own line", code, errOut)
	}
}

// TestRunExecRootLaunchesThroughSetsidWait plants a setsid that records its
// first argument. Under the real wrapper setsid is never a group leader,
// so losing --wait would not change the exit status the other tests see,
// and the argument is the only place the loss shows.
func TestRunExecRootLaunchesThroughSetsidWait(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	tree.linkRunTools(t)

	real, err := exec.LookPath("setsid")
	if err != nil {
		t.Skip("no setsid on this host")
	}

	plant := tree.path("usr/bin/setsid")

	err = os.Remove(plant)
	if err != nil {
		t.Fatal(err)
	}

	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$1\" > %q\nexec %q \"$@\"\n", tree.path("setsid-first"), real)

	err = os.WriteFile(plant, []byte(script), 0o755) //nolint:gosec // a test tool
	if err != nil {
		t.Fatal(err)
	}

	command := []byte("exit 3\n")

	code, _ := tree.prepare(t, runTask, "root", command)
	if code != 0 {
		t.Fatalf("prepare exited %d", code)
	}

	code, _, errOut := tree.execRoot(t, "30", digestOf(command))
	if code != 3 {
		t.Fatalf("exit = %d, want 3; stderr %q", code, errOut)
	}

	got, err := os.ReadFile(tree.path("setsid-first"))
	if err != nil || string(got) != "--wait\n" {
		t.Errorf("setsid was given %q first, %v; want --wait", got, err)
	}
}

// vcapIDs are the uid and gid that the fake getent reports for vcap. As
// root the fake home is handed to them, as newRunTree does for the group.
func (r *runTree) vcapIDs() (int, int) {
	if os.Geteuid() == 0 {
		return vcapGID, vcapGID
	}

	return os.Getuid(), r.gid
}

// fakeGetent plants a getent that prints entry for the vcap account, or
// nothing and exit 2 when entry is empty, as the real one does for a name
// it does not know.
func (r *runTree) fakeGetent(t *testing.T, entry string) {
	t.Helper()

	script := "#!/bin/sh\nexit 2\n"
	if entry != "" {
		script = fmt.Sprintf("#!/bin/sh\n[ \"$1 $2\" = 'passwd vcap' ] || exit 2\nprintf '%%s\\n' %q\n", entry)
	}

	// The plant is removed first, so a link to the real getent is
	// replaced and never written through.
	plant := r.path("usr/bin/getent")

	err := os.Remove(plant)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}

	err = os.WriteFile(plant, []byte(script), 0o755) //nolint:gosec // a test tool
	if err != nil {
		t.Fatal(err)
	}
}

// vcapEntry is a passwd line for vcap with the given numbers and home.
func vcapEntry(uid, gid int, home string) string {
	return fmt.Sprintf("vcap:x:%d:%d:vcap:%s:/bin/bash", uid, gid, home)
}

// fakeVcapTools plants the fakes that stand in for getent, setpriv, and
// timeout. The timeout and setpriv fakes log their arguments the way
// fakeCLI does, one per line, and setpriv also logs the directory it was
// started in. Both then run what follows, so the command still runs.
func (r *runTree) fakeVcapTools(t *testing.T, entry string) {
	t.Helper()
	r.linkRunTools(t)
	r.fakeGetent(t, entry)

	real, err := exec.LookPath("timeout")
	if err != nil {
		t.Skip("no timeout on this host")
	}

	for name, script := range map[string]string{
		"timeout": fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\nexec %q \"$@\"\n", r.path("timeout-args"), real),
		"setpriv": fmt.Sprintf("#!/bin/sh\npwd -P > %q\nwhile [ \"$1\" != -- ]; do shift; done\nshift\nexec \"$@\"\n", r.path("setpriv-pwd")),
	} {
		plant := r.path("usr/bin", name)

		err := os.Remove(plant)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}

		err = os.WriteFile(plant, []byte(script), 0o755) //nolint:gosec // a test tool
		if err != nil {
			t.Fatal(err)
		}
	}
}

// execVcap runs exec as vcap for the staged task.
func (r *runTree) execVcap(t *testing.T, seconds, digest string) (int, string, string) {
	t.Helper()

	return r.run(t, stdinFor(t, []byte("trailing bytes\n")), "exec", runTask, "vcap", seconds, digest)
}

// stageAndExecVcap stages command and runs it as vcap through the fakes.
func (r *runTree) stageAndExecVcap(t *testing.T, command string) (int, string, string) {
	t.Helper()

	uid, gid := r.vcapIDs()
	r.fakeVcapTools(t, vcapEntry(uid, gid, "/home/vcap"))

	code, _ := r.prepare(t, runTask, "vcap", []byte(command))
	if code != 0 {
		t.Fatalf("prepare exited %d", code)
	}

	return r.execVcap(t, "30", digestOf([]byte(command)))
}

func TestRunExecVcapLaunchLine(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	uid, gid := tree.vcapIDs()

	code, out, errOut := tree.stageAndExecVcap(t, "echo out\nexit 4\n")
	if code != 4 || out != "out\n" {
		t.Fatalf("exit %d, stdout %q, stderr %q; want 4 and out", code, out, errOut)
	}

	got, err := os.ReadFile(tree.path("timeout-args"))
	if err != nil {
		t.Fatal(err)
	}

	want := strings.Join([]string{
		"-k", "10", "30",
		tree.path("usr/bin/setpriv"),
		fmt.Sprintf("--reuid=%d", uid), fmt.Sprintf("--regid=%d", gid), "--clear-groups", "--no-new-privs", "--",
		"env", "-i", "HOME=/home/vcap", "USER=vcap", "LOGNAME=vcap", "SHELL=/bin/bash",
		"PATH=/var/vcap/bosh/bin:/usr/local/bin:/usr/bin:/bin",
		"LANG=C.UTF-8", "LC_ALL=C.UTF-8", "TMPDIR=/var/vcap/data/tmp",
		"/bin/bash", "--", tree.cmdFile(runTask),
	}, "\n") + "\n"
	if string(got) != want {
		t.Errorf("timeout was given\n%s\nwant\n%s", got, want)
	}

	pwd, err := os.ReadFile(tree.path("setpriv-pwd"))
	if err != nil || strings.TrimSpace(string(pwd)) != tree.path("home/vcap") {
		t.Errorf("setpriv started in %q, %v; want the vcap home", pwd, err)
	}

	if got := tree.taskFile(t, "exit"); got != "4\n" {
		t.Errorf("exit file holds %q", got)
	}
}

func TestRunExecVcapRefusals(t *testing.T) {
	t.Parallel()

	same := func(uid, gid int) string { return vcapEntry(uid, gid, "/home/vcap") }

	chown := func(path string, uid, gid int) func(*testing.T, *runTree) {
		return func(t *testing.T, tree *runTree) {
			t.Helper()

			err := os.Chown(tree.path(path), uid, gid)
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	// Every case is built so that the check it names is the only one that
	// refuses. Without that check the command would run.
	for _, tc := range []struct {
		name  string
		entry func(uid, gid int) string
		setup func(t *testing.T, tree *runTree)
	}{
		{"no vcap entry", func(int, int) string { return "" }, nil},
		{"a name that is not vcap", func(uid, gid int) string {
			return fmt.Sprintf("vcapx:x:%d:%d:vcap:/home/vcap:/bin/bash", uid, gid)
		}, nil},
		{"uid 0 on a home owned by root", func(_, gid int) string { return vcapEntry(0, gid, "/home/vcap") }, chown("home/vcap", 0, vcapGID)},
		{"gid 0 on a task and home of group 0", func(uid, _ int) string { return vcapEntry(uid, 0, "/home/vcap") }, func(t *testing.T, tree *runTree) {
			t.Helper()

			for _, path := range []string{"home/vcap", runOps + "/" + runTask, runOps + "/" + runTask + "/cmd.sh"} {
				chown(path, -1, 0)(t, tree)
			}
		}},
		{"a home owned by another uid", func(uid, gid int) string { return vcapEntry(uid+1, gid, "/home/vcap") }, nil},
		{"a home owned by another gid", same, chown("home/vcap", -1, vcapGID+1)},
		{"a missing home", func(uid, gid int) string { return vcapEntry(uid, gid, "/home/nobody") }, nil},
		{"a symlinked home", func(uid, gid int) string { return vcapEntry(uid, gid, "/home/link") }, func(t *testing.T, tree *runTree) {
			t.Helper()

			err := os.Symlink(tree.path("home/vcap"), tree.path("home/link"))
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"a home with a dot-dot segment", func(uid, gid int) string { return vcapEntry(uid, gid, "/home/../home/vcap") }, nil},
		{"a home with a space", func(uid, gid int) string { return vcapEntry(uid, gid, "/home/v cap") }, func(t *testing.T, tree *runTree) {
			t.Helper()

			err := os.Mkdir(tree.path("home/v cap"), 0o755) //nolint:gosec // a test tree
			if err != nil {
				t.Fatal(err)
			}

			chown("home/v cap", vcapGID, vcapGID)(t, tree)
		}},
		{"a home that is not absolute", func(uid, gid int) string { return vcapEntry(uid, gid, "home/vcap") }, nil},
		{"a parent of the home that anyone can write", same, func(t *testing.T, tree *runTree) {
			t.Helper()

			err := os.Chmod(tree.path("home"), 0o777) //nolint:gosec // the case under test
			if err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)
			uid, gid := tree.vcapIDs()
			command := canaryExec(tree)

			tree.fakeVcapTools(t, vcapEntry(uid, gid, "/home/vcap"))

			code, _ := tree.prepare(t, runTask, "vcap", command)
			if code != 0 {
				t.Fatalf("prepare exited %d", code)
			}

			tree.fakeGetent(t, tc.entry(uid, gid))

			if tc.setup != nil {
				tc.setup(t, tree)
			}

			code, out, errOut := tree.execVcap(t, "30", digestOf(command))
			if code != scripts.ExitRefused {
				t.Errorf("exit = %d, want %d", code, scripts.ExitRefused)
			}

			if out != "" || strings.Contains(errOut, "touch") || strings.Contains(errOut, digestOf(command)) {
				t.Errorf("a refusal printed %q and %q", out, errOut)
			}

			tree.requireExecCanary(t, false)

			for _, name := range []string{"started", "pgid", "exit"} {
				_, err := os.Lstat(tree.path(runOps, runTask, name))
				if err == nil {
					t.Errorf("a refusal left %s behind", name)
				}
			}
		})
	}
}

// TestRunExecVcapEntryChecks calls the entry parser alone, so each check
// shows up without the ownership checks that follow it in exec.
func TestRunExecVcapEntryChecks(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		entry string
		want  string
	}{
		{"a good entry", "vcap:x:1000:1001:vcap:/home/vcap:/bin/bash", "1000 1001 /home/vcap"},
		{"a name that is not vcap", "vcapx:x:1000:1001:v:/home/vcap:/bin/bash", ""},
		{"no entry", "", ""},
		{"uid 0", "vcap:x:0:1001:v:/home/vcap:/bin/bash", ""},
		{"gid 0", "vcap:x:1000:0:v:/home/vcap:/bin/bash", ""},
		{"uid 00", "vcap:x:00:1001:v:/home/vcap:/bin/bash", ""},
		{"gid 00", "vcap:x:1000:00:v:/home/vcap:/bin/bash", ""},
		{"uid with leading zeros", "vcap:x:01000:1001:v:/home/vcap:/bin/bash", ""},
		{"uid that is not a number", "vcap:x:abc:1001:v:/home/vcap:/bin/bash", ""},
		{"uid of minus one", "vcap:x:4294967295:1001:v:/home/vcap:/bin/bash", ""},
		{"uid just past 32 bits", "vcap:x:4294967296:1001:v:/home/vcap:/bin/bash", ""},
		{"gid just past 32 bits", "vcap:x:1000:4294967296:v:/home/vcap:/bin/bash", ""},
		{"uid of ten nines", "vcap:x:9999999999:1001:v:/home/vcap:/bin/bash", ""},
		{"gid of ten nines", "vcap:x:1000:9999999999:v:/home/vcap:/bin/bash", ""},
		{"gid of minus one", "vcap:x:1000:4294967295:v:/home/vcap:/bin/bash", ""},
		{"a home that is not absolute", "vcap:x:1000:1001:v:home/vcap:/bin/bash", ""},
		{"a home with a space", "vcap:x:1000:1001:v:/home/v cap:/bin/bash", ""},
		{"a home with dot-dot", "vcap:x:1000:1001:v:/home/../root:/bin/bash", ""},
		{"a home that ends in dot-dot", "vcap:x:1000:1001:v:/home/vcap/..:/bin/bash", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)
			tree.fakeGetent(t, tc.entry)

			code, out := tree.snippet(t, false, "_rc_vcap_entry\n")
			if tc.want == "" {
				if code == 0 || out != "" {
					t.Errorf("exit %d, printed %q; want a refusal", code, out)
				}

				return
			}

			if code != 0 || out != tc.want {
				t.Errorf("exit %d, printed %q; want %q", code, out, tc.want)
			}
		})
	}
}

func TestRunExecVcapCanaryControl(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	command := canaryExec(tree)

	code, _, errOut := tree.stageAndExecVcap(t, string(command))
	if code != 0 {
		t.Fatalf("exit = %d; stderr %q", code, errOut)
	}

	tree.requireExecCanary(t, true)
}

func TestRunExecRefusesOtherUsers(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)

	for _, user := range []string{"nobody", "VCAP", "vcap2", "root:vcap", ""} {
		code, _, _ := tree.run(t, stdinFor(t, nil), "exec", runTask, user, "30", strings.Repeat("a", 64))
		if code != scripts.ExitUsage {
			t.Errorf("user %q exited %d, want %d", user, code, scripts.ExitUsage)
		}
	}
}

func TestRunExecCommandHasNoDescriptor3(t *testing.T) {
	t.Parallel()

	const command = "if { : >&3; } 2> /dev/null; then echo open; else echo closed; fi\n"

	for _, user := range []string{"root", "vcap"} {
		t.Run(user, func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)

			var (
				code   int
				out    string
				errOut string
			)

			if user == "root" {
				code, out, errOut = tree.stageAndExec(t, command)
			} else {
				code, out, errOut = tree.stageAndExecVcap(t, command)
			}

			if code != 0 || out != "closed\n" || errOut != "" {
				t.Errorf("exit %d, stdout %q, stderr %q; want descriptor 3 closed", code, out, errOut)
			}
		})
	}
}

// TestRunExecVcapRealAccount runs a command as the real vcap account, so
// it needs Linux, root, and that account. It checks the identity the
// command has, that it can read cmd.sh through the 0711 and 0710
// directories, and that it can neither append to cmd.sh nor remove the
// task directory.
func TestRunExecVcapRealAccount(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("the real vcap test needs Linux and root")
	}

	account, err := user.Lookup("vcap")
	if err != nil {
		t.Skipf("no vcap account on this host: %v", err)
	}

	uid, _ := strconv.Atoi(account.Uid)
	gid, _ := strconv.Atoi(account.Gid)

	tree := newRunTree(t)
	tree.linkRunTools(t)

	// Only setpriv is linked. getent is a fake, and a fake must never be
	// written through a link to the real tool.
	path, err := exec.LookPath("setpriv")
	if err != nil {
		t.Skip("no setpriv on this host")
	}

	err = os.Symlink(path, tree.path("usr/bin/setpriv"))
	if err != nil {
		t.Fatal(err)
	}

	// The fake home stands in for the account's home, and the account must
	// be able to walk down to cmd.sh from the top of the temporary tree.
	home := tree.path("home/vcap")

	err = os.Chown(home, uid, gid)
	if err != nil {
		t.Fatal(err)
	}

	// The search bit is added to the test root and to the directories
	// above it that this test created, and to nothing outside them.
	stop, err := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(tree.root)))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(tree.root, stop+"/") || strings.Count(strings.TrimPrefix(tree.root, stop), "/") > 2 {
		t.Fatalf("the test root %s is not within two levels of %s", tree.root, stop)
	}

	for dir := tree.root; dir != stop; dir = filepath.Dir(dir) {
		info, statErr := os.Stat(dir)
		if statErr != nil {
			t.Fatal(statErr)
		}

		err = os.Chmod(dir, info.Mode().Perm()|0o011) //nolint:gosec // a test tree the account must pass through
		if err != nil {
			t.Fatal(err)
		}
	}

	// The account's home in the passwd database is not under the fake
	// root, so the fake getent reports the path as the script joins it.
	tree.fakeGetent(t, vcapEntry(uid, gid, "/home/vcap"))

	taskDir := tree.path(runOps, runTask)
	command := fmt.Sprintf("id -u\nid -g\nid -G\ngrep ^NoNewPrivs: /proc/self/status\ncat %[1]q\n(echo more >> %[1]q) 2> /dev/null || echo append-denied\nrm -rf %[2]q 2> /dev/null\n[ -e %[2]q ] && echo remove-denied\n", taskDir+"/cmd.sh", taskDir)

	code, _ := tree.prepare(t, runTask, "vcap", []byte(command))
	if code != 0 {
		t.Fatalf("prepare exited %d", code)
	}

	code, out, errOut := tree.execVcap(t, "30", digestOf([]byte(command)))
	if code != 0 {
		t.Fatalf("exit = %d; stderr %q", code, errOut)
	}

	want := fmt.Sprintf("%d\n%d\n%d\nNoNewPrivs:\t1\n%sappend-denied\nremove-denied\n", uid, gid, gid, command)
	if out != want {
		t.Errorf("the command printed\n%s\nwant\n%s", out, want)
	}
}
