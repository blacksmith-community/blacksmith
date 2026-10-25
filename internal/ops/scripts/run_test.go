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
}

func newRunTree(t *testing.T) *runTree {
	t.Helper()
	requireRun(t)

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{"var/vcap/data", "home/vcap", "usr/bin"} {
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
	cmd.Env = []string{"CERTPATCH_ROOT=" + r.root, "HOME=" + r.root}
	cmd.Stdin = stdin
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

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
