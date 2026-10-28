package scripts_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"blacksmith/internal/ops/scripts"
)

// holdTaskLock takes the exclusive flock that exec and cleanup share on
// the task directory, and returns the function that releases it.
func (r *runTree) holdTaskLock(t *testing.T) func() {
	t.Helper()

	dir, err := os.Open(r.path(runOps, runTask))
	if err != nil {
		t.Fatal(err)
	}

	err = syscall.Flock(int(dir.Fd()), syscall.LOCK_EX)
	if err != nil {
		t.Fatal(err)
	}

	released := false
	release := func() {
		if !released {
			released = true
			_ = dir.Close()
		}
	}

	t.Cleanup(release)

	return release
}

// scriptRun is a run of the script in the background.
type scriptRun struct {
	cmd    *exec.Cmd
	stdout bytes.Buffer
	done   chan struct{}
	code   int
}

// startOp starts the script in the background as the leader of its own
// process group, as run does in the foreground, and ends the group when
// the test ends.
func (r *runTree) startOp(t *testing.T, stdin io.Reader, args ...string) *scriptRun {
	t.Helper()

	run := &scriptRun{done: make(chan struct{})}

	run.cmd = exec.Command(bashPath(t), append([]string{"-s", "--"}, args...)...) //nolint:gosec // the script under test
	run.cmd.Env = append([]string{"CERTPATCH_ROOT=" + r.root, "HOME=" + r.root}, r.env...)
	run.cmd.Stdin = stdin
	run.cmd.Stdout = &run.stdout
	run.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	run.cmd.WaitDelay = 2 * time.Second

	err := run.cmd.Start()
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		defer close(run.done)

		err := run.cmd.Wait()

		var exit *exec.ExitError
		if errors.As(err, &exit) {
			run.code = exit.ExitCode()
		} else if err != nil {
			run.code = -1
		}
	}()

	t.Cleanup(func() {
		_ = syscall.Kill(-run.cmd.Process.Pid, syscall.SIGKILL)
		<-run.done
	})

	return run
}

// wait waits for the run to end and returns its exit code and what it
// printed.
func (s *scriptRun) wait(t *testing.T) (int, string) {
	t.Helper()

	select {
	case <-s.done:
	case <-time.After(20 * time.Second):
		t.Fatalf("%v did not end", s.cmd.Args[2:])
	}

	return s.code, s.stdout.String()
}

// lockWaiter waits until a flock that parent started is blocked on a lock,
// as /proc/locks lists it, and returns the pid of that flock.
func lockWaiter(t *testing.T, parent int) int {
	t.Helper()

	var pid int

	if !waitFor(10*time.Second, func() bool {
		pid = 0

		entries, err := os.ReadDir("/proc")
		if err != nil {
			return false
		}

		for _, entry := range entries {
			n, convErr := strconv.Atoi(entry.Name())
			if convErr != nil {
				continue
			}

			fields := procFields(n)
			comm, _ := os.ReadFile("/proc/" + entry.Name() + "/comm") //nolint:gosec // /proc

			if len(fields) > 1 && fields[1] == strconv.Itoa(parent) && strings.TrimSpace(string(comm)) == "flock" {
				pid = n
			}
		}

		if pid == 0 {
			return false
		}

		locks, err := os.ReadFile("/proc/locks")
		if err != nil {
			return false
		}

		for line := range strings.SplitSeq(string(locks), "\n") {
			f := strings.Fields(line)
			if len(f) > 5 && f[1] == "->" && f[2] == "FLOCK" && f[5] == strconv.Itoa(pid) {
				return true
			}
		}

		return false
	}) {
		t.Fatal("no flock waited on the task lock")
	}

	return pid
}

// stopProcess stops pid and waits until it shows as stopped, so a lock
// released after it returns can't be taken by pid.
func stopProcess(t *testing.T, pid int) {
	t.Helper()

	err := syscall.Kill(pid, syscall.SIGSTOP)
	if err != nil {
		t.Fatal(err)
	}

	if !waitFor(5*time.Second, func() bool {
		fields := procFields(pid)

		return len(fields) > 0 && fields[0] == "T"
	}) {
		t.Fatal("the waiter did not stop")
	}
}

// killFromFile kills the pid that a test command wrote to path, if any,
// when the test ends.
func killFromFile(t *testing.T, path string) {
	t.Helper()

	t.Cleanup(func() {
		raw, err := os.ReadFile(path) //nolint:gosec // the test tree
		if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && convErr == nil && pid > 1 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
}

// fileExists reports whether anything stands at path.
func fileExists(path string) bool {
	_, err := os.Lstat(path)

	return err == nil
}

// Exec and cleanup take the same lock on the task directory, so whichever
// takes it first decides the answer. The test holds the lock while both
// wait on it, stops one of them, and releases it. When exec goes first,
// cleanup finds started and checks the command's session. When cleanup
// goes first, it reports started=no, and exec then refuses, so no command
// runs that cleanup said never started.
func TestRunExecAndCleanupTakeTheTaskLockInEitherOrder(t *testing.T) {
	t.Parallel()
	needDoneRecords(t)

	for _, execFirst := range []bool{true, false} {
		name := "cleanup first"
		if execFirst {
			name = "exec first"
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)
			tree.linkRunTools(t)
			tree.linkKill(t)
			tree.env = []string{"RC_KILL_TICKS=3"}

			pidFile := tree.path("sleeper-pid")
			killFromFile(t, pidFile)

			command := sleeperCommand(pidFile)

			code, _ := tree.prepare(t, runTask, "root", []byte(command))
			if code != 0 {
				t.Fatalf("prepare exited %d", code)
			}

			release := tree.holdTaskLock(t)

			execRun := tree.startOp(t, stdinFor(t, []byte("trailing bytes\n")), "exec", runTask, "root", "300", digestOf([]byte(command)))
			execWaiter := lockWaiter(t, execRun.cmd.Process.Pid)

			cleanRun := tree.startOp(t, stdinFor(t, nil), "cleanup", runTask)
			cleanWaiter := lockWaiter(t, cleanRun.cmd.Process.Pid)

			loser := execWaiter
			if execFirst {
				loser = cleanWaiter
			}

			stopProcess(t, loser)
			release()

			if execFirst {
				if !waitFor(10*time.Second, func() bool {
					return fileExists(pidFile) && fileExists(tree.path(runOps, runTask, "pgid"))
				}) {
					t.Fatal("exec did not start the command after it took the lock")
				}
			} else {
				cleanRun.wait(t)
			}

			err := syscall.Kill(loser, syscall.SIGCONT)
			if err != nil {
				t.Fatal(err)
			}

			cleanCode, cleanOut := cleanRun.wait(t)
			execCode, _ := execRun.wait(t)

			if execFirst {
				if cleanCode != 0 || cleanOut != "cleanup started=yes exit=- killed=yes\n" {
					t.Errorf("cleanup exited %d with %q, want started=yes with the session ended", cleanCode, cleanOut)
				}

				raw, _ := os.ReadFile(pidFile) //nolint:gosec // the test tree
				pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))

				if fields := procFields(pid); convErr != nil || len(fields) > 0 && fields[0] != "Z" {
					t.Error("the command outlived the cleanup that found it started")
				}

				return
			}

			if cleanCode != 0 || cleanOut != "cleanup started=no exit=- killed=no\n" {
				t.Errorf("cleanup exited %d with %q, want started=no", cleanCode, cleanOut)
			}

			if execCode != scripts.ExitRefused {
				t.Errorf("exec exited %d after cleanup, want %d", execCode, scripts.ExitRefused)
			}

			if fileExists(pidFile) {
				t.Error("the command ran after cleanup reported it never started")
			}
		})
	}
}

// Exec makes its last checks while it holds the task lock. A cleanup that
// fenced the task, wrote its done record, removed cmd.sh, or removed or
// replaced the directory while exec waited for the lock makes exec refuse
// without writing started, and nothing runs.
func TestRunExecChecksTheTaskUnderTheLock(t *testing.T) {
	t.Parallel()
	needDoneRecords(t)

	cases := map[string]func(t *testing.T, tree *runTree){
		"a fence": func(t *testing.T, tree *runTree) {
			t.Helper()
			writeTestFile(t, tree.path(runOps, runTask, "fence"), "", 0o600)
		},
		"a done record": func(t *testing.T, tree *runTree) {
			t.Helper()
			writeTestFile(t, tree.donePath(runTask), "cleanup started=no exit=- killed=no\n", 0o600)
		},
		"cmd.sh gone": func(t *testing.T, tree *runTree) {
			t.Helper()

			err := os.Remove(tree.cmdFile(runTask))
			if err != nil {
				t.Fatal(err)
			}
		},
		"the directory removed": func(t *testing.T, tree *runTree) {
			t.Helper()

			err := os.RemoveAll(tree.path(runOps, runTask))
			if err != nil {
				t.Fatal(err)
			}
		},
		"the directory replaced": func(t *testing.T, tree *runTree) {
			t.Helper()

			dir := tree.path(runOps, runTask)
			text, err := os.ReadFile(tree.cmdFile(runTask))
			if err != nil {
				t.Fatal(err)
			}

			err = os.Rename(dir, dir+".old")
			if err != nil {
				t.Fatal(err)
			}

			err = os.Mkdir(dir, 0o700)
			if err != nil {
				t.Fatal(err)
			}

			for _, step := range []error{os.Chown(dir, 0, tree.gid), os.Chmod(dir, 0o710)} {
				if step != nil {
					t.Fatal(step)
				}
			}

			writeTestFile(t, tree.cmdFile(runTask), string(text), 0o640)

			err = os.Chown(tree.cmdFile(runTask), 0, tree.gid)
			if err != nil {
				t.Fatal(err)
			}
		},
	}

	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)
			tree.linkRunTools(t)

			command := []byte(fmt.Sprintf("echo ran > %q\n", tree.path("ran")))

			code, _ := tree.prepare(t, runTask, "root", command)
			if code != 0 {
				t.Fatalf("prepare exited %d", code)
			}

			release := tree.holdTaskLock(t)

			execRun := tree.startOp(t, stdinFor(t, []byte("trailing bytes\n")), "exec", runTask, "root", "30", digestOf(command))
			lockWaiter(t, execRun.cmd.Process.Pid)

			change(t, tree)
			release()

			code, _ = execRun.wait(t)
			if code != scripts.ExitRefused {
				t.Errorf("exec exited %d, want %d", code, scripts.ExitRefused)
			}

			if fileExists(tree.path("ran")) {
				t.Error("the command ran")
			}

			if fileExists(tree.path(runOps, runTask, "started")) {
				t.Error("exec wrote started")
			}
		})
	}
}

// Cleanup reads started only once it holds the task lock, so an exec that
// held the lock while cleanup waited, and wrote started under it, is seen
// as started, and its session is checked.
func TestRunCleanupReadsStartedUnderTheLock(t *testing.T) {
	t.Parallel()
	needDoneRecords(t)

	tree := newRunTree(t)

	code, _ := tree.prepare(t, runTask, "root", []byte("exit 0\n"))
	if code != 0 {
		t.Fatalf("prepare exited %d", code)
	}

	release := tree.holdTaskLock(t)

	cleanRun := tree.startOp(t, stdinFor(t, nil), "cleanup", runTask)
	lockWaiter(t, cleanRun.cmd.Process.Pid)

	for name, text := range withGoneGroup(t, map[string]string{"marker": "", "started": ""}) {
		writeTestFile(t, tree.path(runOps, runTask, name), text, 0o600)
	}

	release()

	code, out := cleanRun.wait(t)
	if code != 0 || out != "cleanup started=yes exit=- killed=no\n" {
		t.Errorf("cleanup exited %d with %q, want started=yes with the session checked", code, out)
	}
}

// A cleanup that finds no started fences the task before it reports
// started=no, so an exec that comes after it refuses, whether the cleanup
// removed the directory or kept it because its done record failed.
func TestRunExecRefusesATaskThatCleanupFenced(t *testing.T) {
	t.Parallel()
	needDoneRecords(t)

	for name, stub := range map[string]string{
		"the directory removed":  "",
		"the done record failed": `mv() { return 1; }` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)
			tree.linkRunTools(t)

			command := []byte(fmt.Sprintf("echo ran > %q\n", tree.path("ran")))

			code, _ := tree.prepare(t, runTask, "root", command)
			if code != 0 {
				t.Fatalf("prepare exited %d", code)
			}

			code, out := tree.snippet(t, false, stub+"cleanup "+runTask+"\n")
			if code != 0 || out != "cleanup started=no exit=- killed=no\n" {
				t.Fatalf("cleanup exited %d with %q, want started=no", code, out)
			}

			if stub != "" && fileExists(tree.cmdFile(runTask)) && !fileExists(tree.path(runOps, runTask, "fence")) {
				t.Fatal("the kept directory is neither fenced nor without cmd.sh")
			}

			code, _, _ = tree.execRoot(t, "30", digestOf(command))
			if code != scripts.ExitRefused {
				t.Errorf("exec exited %d, want %d", code, scripts.ExitRefused)
			}

			if fileExists(tree.path("ran")) {
				t.Error("the command ran after cleanup reported it never started")
			}
		})
	}
}

// Without flock, or with a flock that fails, exec and cleanup can't
// exclude each other, so both refuse. exec writes no started and runs
// nothing, and cleanup prints no record, which the broker reads as a
// cleanup that did not report cleanly and holds the node, and keeps the
// directory.
func TestRunWithoutFlockRefusesExecAndCleanup(t *testing.T) {
	t.Parallel()
	needDoneRecords(t)

	for name, plant := range map[string]string{
		"no flock":           "",
		"a flock that fails": "#!/bin/sh\nexit 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)
			tree.linkRunTools(t)

			command := []byte(fmt.Sprintf("echo ran > %q\n", tree.path("ran")))

			code, _ := tree.prepare(t, runTask, "root", command)
			if code != 0 {
				t.Fatalf("prepare exited %d", code)
			}

			err := os.Remove(tree.path("usr/bin/flock"))
			if err != nil {
				t.Fatal(err)
			}

			if plant != "" {
				err = os.WriteFile(tree.path("usr/bin/flock"), []byte(plant), 0o755) //nolint:gosec // a test tool
				if err != nil {
					t.Fatal(err)
				}
			}

			code, _, _ = tree.execRoot(t, "30", digestOf(command))
			if code != scripts.ExitRefused {
				t.Errorf("exec exited %d, want %d", code, scripts.ExitRefused)
			}

			if fileExists(tree.path("ran")) || fileExists(tree.path(runOps, runTask, "started")) {
				t.Error("exec started the command without the lock")
			}

			code, out := tree.cleanup(t, runTask)
			if code != scripts.ExitRefused || out != "" {
				t.Errorf("cleanup exited %d with %q, want %d and no record", code, out, scripts.ExitRefused)
			}

			if !fileExists(tree.cmdFile(runTask)) || fileExists(tree.path(runOps, runTask, "fence")) {
				t.Error("cleanup changed the task directory without the lock")
			}
		})
	}
}

// A cleanup that finds a done record beside a task directory reads started
// under the task lock too, so an exec that wrote started while cleanup
// waited is checked from the directory and never replayed away.
func TestRunCleanupReplayReadsStartedUnderTheLock(t *testing.T) {
	t.Parallel()
	needDoneRecords(t)

	tree := newRunTree(t)
	tree.makeTask(t, runTask, map[string]string{"exit": "0\n"})
	writeTestFile(t, tree.donePath(runTask), "cleanup started=yes exit=0 killed=no\n", 0o600)

	release := tree.holdTaskLock(t)

	cleanRun := tree.startOp(t, stdinFor(t, nil), "cleanup", runTask)
	lockWaiter(t, cleanRun.cmd.Process.Pid)

	for name, text := range withGoneGroup(t, map[string]string{"marker": "", "started": ""}) {
		writeTestFile(t, tree.path(runOps, runTask, name), text, 0o600)
	}

	release()

	code, out := cleanRun.wait(t)
	if code != 0 || out != "cleanup started=yes exit=0 killed=no\n" {
		t.Errorf("cleanup exited %d with %q, want the directory's answer", code, out)
	}

	if _, err := os.Lstat(tree.path(runOps, runTask)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the checked directory stayed: %v", err)
	}
}

// _rc_fence removes cmd.sh when the fence can't be written, which also
// makes exec refuse, and fails only when neither is done. A cleanup whose
// fence fails refuses rather than report started=no.
func TestRunFenceFallsBackToRemovingTheCommand(t *testing.T) {
	t.Parallel()
	needDoneRecords(t)

	cases := []struct {
		name string
		stub string
		want string
	}{
		{name: "a fence that can't be written", stub: "_rc_write() { return 1; }\n", want: "0 gone"},
		{name: "a fence and a removal that both fail", stub: "_rc_write() { return 1; }\nrm() { return 0; }\n", want: "1 kept"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)
			tree.makeTask(t, runTask, map[string]string{"cmd.sh": "x"})

			dir := tree.path(runOps, runTask)
			script := fmt.Sprintf("%s_rc_fence %q\nrc=$?\nstate=kept\n[ -e %q ] || state=gone\nprintf '%%s %%s\\n' \"$rc\" \"$state\"\n", tc.stub, dir, dir+"/cmd.sh")

			code, out := tree.snippet(t, false, script)
			if code != 0 || strings.TrimSpace(out) != tc.want {
				t.Errorf("exit = %d, output = %q, want %q", code, out, tc.want)
			}
		})
	}

	t.Run("a cleanup whose fence fails", func(t *testing.T) {
		t.Parallel()

		tree := newRunTree(t)
		tree.makeTask(t, runTask, map[string]string{"cmd.sh": "x"})

		code, out := tree.snippet(t, false, "_rc_fence() { return 1; }\ncleanup "+runTask+"\n")
		if code != scripts.ExitRefused || out != "" {
			t.Errorf("exit = %d, output = %q, want %d and no record", code, out, scripts.ExitRefused)
		}
	})
}

// writeTestFile writes a file of the test tree with mode, whatever the
// umask.
func writeTestFile(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()

	err := os.WriteFile(path, []byte(text), mode)
	if err == nil {
		err = os.Chmod(path, mode)
	}

	if err != nil {
		t.Fatal(err)
	}
}
