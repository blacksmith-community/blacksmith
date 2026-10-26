package scripts_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"blacksmith/internal/ops/scripts"
)

// plantKill puts a kill under the fake root that logs its arguments and
// signals nothing, and returns the log's path.
func (r *runTree) plantKill(t *testing.T) string {
	t.Helper()

	log := r.path("kill-log")
	plant := r.path("usr/bin/kill")

	// A link to the real kill must go, or the write would go through it.
	err := os.Remove(plant)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}

	err = os.WriteFile(plant, []byte(fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\n", log)), 0o755) //nolint:gosec // a test tool
	if err != nil {
		t.Fatal(err)
	}

	return log
}

// linkKill puts the real kill under the fake root.
func (r *runTree) linkKill(t *testing.T) {
	t.Helper()

	path, err := exec.LookPath("kill")
	if err != nil {
		t.Skip("no kill binary on this host")
	}

	err = os.Symlink(path, r.path("usr/bin/kill"))
	if err != nil {
		t.Fatal(err)
	}
}

// procFields returns the fields of a /proc stat file after the command
// name, which start with the state.
func procFields(pid int) []string {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat") //nolint:gosec // /proc
	if err != nil {
		return nil
	}

	idx := bytes.LastIndex(raw, []byte(") "))
	if idx < 0 {
		return nil
	}

	return strings.Fields(string(raw[idx+2:]))
}

// startTime is field 22 of a process's stat file, which run.sh records.
func startTime(t *testing.T, pid int) string {
	t.Helper()

	fields := procFields(pid)
	if len(fields) < 20 {
		t.Fatalf("no stat for %d", pid)
	}

	return fields[19]
}

// anyProcess reports whether a process that is not a zombie has the
// value at field index idx, where 2 is the group and 3 the session. A
// group that nothing reaps keeps its zombies, as it does when the tests
// are PID 1 in a container.
func anyProcess(t *testing.T, idx, id int) bool {
	t.Helper()

	stats, err := filepath.Glob("/proc/[0-9]*/stat")
	if err != nil {
		t.Fatal(err)
	}

	for _, stat := range stats {
		pid, convErr := strconv.Atoi(strings.Split(stat, "/")[2])
		if convErr != nil {
			continue
		}

		fields := procFields(pid)
		if len(fields) > idx && fields[0] != "Z" && fields[idx] == strconv.Itoa(id) {
			return true
		}
	}

	return false
}

// groupLive reports whether any process other than a zombie is in the
// process group.
func groupLive(t *testing.T, pgid int) bool {
	t.Helper()

	return anyProcess(t, 2, pgid)
}

// sessionLive reports whether any process other than a zombie is in the
// session.
func sessionLive(t *testing.T, sid int) bool {
	t.Helper()

	return anyProcess(t, 3, sid)
}

// waitFor polls until cond holds or the time runs out.
func waitFor(limit time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(limit)

	for time.Now().Before(deadline) {
		if cond() {
			return true
		}

		time.Sleep(50 * time.Millisecond)
	}

	return cond()
}

// runningCommand is an exec in progress.
type runningCommand struct {
	pgid      int
	sleepPID  int
	starttime string
}

// startExec stages command and starts exec for it in the background as
// its own process group. It waits for started and pgid, and ends the
// wrapper and the command's group when the test ends, however it ends.
// The command must write its background sleeper's pid to the file named
// pidFile.
func (r *runTree) startExec(t *testing.T, user, command, pidFile string) runningCommand {
	t.Helper()

	var wrapper *exec.Cmd

	if user == "vcap" {
		uid, gid := r.vcapIDs()
		r.fakeVcapTools(t, vcapEntry(uid, gid, "/home/vcap"))
	} else {
		r.linkRunTools(t)
	}

	code, _ := r.prepare(t, runTask, user, []byte(command))
	if code != 0 {
		t.Fatalf("prepare exited %d", code)
	}

	wrapper = exec.Command(bashPath(t), "-s", "--", "exec", runTask, user, "300", digestOf([]byte(command))) //nolint:gosec // the script under test
	wrapper.Env = append([]string{"CERTPATCH_ROOT=" + r.root, "HOME=" + r.root}, r.env...)
	wrapper.Stdin = stdinFor(t, []byte("trailing bytes\n"))
	wrapper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	wrapper.WaitDelay = 2 * time.Second

	err := wrapper.Start()
	if err != nil {
		t.Fatal(err)
	}

	var running runningCommand

	t.Cleanup(func() {
		_ = syscall.Kill(-wrapper.Process.Pid, syscall.SIGKILL)

		if running.pgid > 1 {
			_ = syscall.Kill(-running.pgid, syscall.SIGKILL)
		}

		if running.sleepPID > 1 {
			_ = syscall.Kill(running.sleepPID, syscall.SIGKILL)
		}

		_ = wrapper.Wait()
	})

	if !waitFor(10*time.Second, func() bool {
		raw, readErr := os.ReadFile(r.path(runOps, runTask, "pgid"))
		pgid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))

		if readErr != nil || convErr != nil {
			return false
		}

		// The group is known as soon as it is written, so the cleanup
		// above ends it even when the sleeper never reports.
		running.pgid = pgid

		sleeper, sleepErr := os.ReadFile(pidFile)
		pid, pidErr := strconv.Atoi(strings.TrimSpace(string(sleeper)))

		if sleepErr != nil || pidErr != nil {
			return false
		}

		running.sleepPID = pid

		return true
	}) {
		t.Fatal("the command did not start")
	}

	stamp, err := os.ReadFile(r.path(runOps, runTask, "starttime"))
	if err != nil {
		t.Fatal(err)
	}

	running.starttime = strings.TrimSpace(string(stamp))

	_, err = os.Stat(r.path(runOps, runTask, "started"))
	if err != nil {
		t.Fatalf("no started marker: %v", err)
	}

	return running
}

func sleeperCommand(pidFile string) string {
	return fmt.Sprintf("sleep 60 &\necho $! > %q\nwait\n", pidFile)
}

func TestRunCleanupKillsARunningCommand(t *testing.T) {
	t.Parallel()

	for _, user := range []string{"root", "vcap"} {
		t.Run(user, func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)
			tree.linkKill(t)

			pidFile := tree.path("sleeper-pid")
			running := tree.startExec(t, user, sleeperCommand(pidFile), pidFile)

			ps, err := exec.LookPath("ps")
			if err != nil {
				t.Skip("no ps on this host")
			}

			got, err := exec.Command(ps, "-o", "pgid=", "-p", strconv.Itoa(running.sleepPID)).Output() //nolint:gosec // a test lookup
			if err != nil {
				t.Fatal(err)
			}

			if strings.TrimSpace(string(got)) != strconv.Itoa(running.pgid) {
				t.Errorf("ps reports group %q for the sleeper, the pgid file holds %d", got, running.pgid)
			}

			code, out := tree.cleanup(t, runTask)
			if code != 0 || out != "cleanup started=yes exit=- killed=yes\n" {
				t.Errorf("exit = %d, output = %q", code, out)
			}

			if groupLive(t, running.pgid) {
				t.Error("the command's group is still alive")
			}

			_, err = os.Lstat(tree.path(runOps, runTask))
			if err == nil {
				t.Error("the task directory is still there")
			}
		})
	}
}

func TestRunCleanupWaitsTheWindowBeforeKill(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	tree.linkKill(t)
	tree.env = []string{"RC_KILL_TICKS=8"}

	pidFile := tree.path("sleeper-pid")
	running := tree.startExec(t, "root", "trap '' TERM\n"+sleeperCommand(pidFile), pidFile)

	start := time.Now()
	code, out := tree.cleanup(t, runTask)

	if code != 0 || out != "cleanup started=yes exit=- killed=yes\n" {
		t.Errorf("exit = %d, output = %q", code, out)
	}

	if took := time.Since(start); took < 700*time.Millisecond {
		t.Errorf("cleanup took %v, so it did not wait out the window for a group that ignored TERM", took)
	}

	if groupLive(t, running.pgid) {
		t.Error("KILL did not end the group")
	}
}

func TestRunCleanupReportsAGroupThatSurvivesKill(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	tree.linkKill(t)
	tree.env = []string{"RC_KILL_TICKS=3"}

	pidFile := tree.path("sleeper-pid")
	running := tree.startExec(t, "root", sleeperCommand(pidFile), pidFile)

	// The planted kill logs and signals nothing, so the group stays.
	log := tree.plantKill(t)

	code, out := tree.cleanup(t, runTask)
	if code != 0 || out != "cleanup started=yes exit=- killed=survived\n" {
		t.Errorf("exit = %d, output = %q", code, out)
	}

	if !groupLive(t, running.pgid) {
		t.Error("the group ended although nothing signalled it")
	}

	logged, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}

	want := fmt.Sprintf("-TERM -- -%[1]d\n-KILL -- -%[1]d\n", running.pgid)
	if string(logged) != want {
		t.Errorf("signals sent = %q, want %q", logged, want)
	}

	_, err = scripts.ParseCleanup([]byte(out))
	if err != nil {
		t.Errorf("the record does not parse: %v", err)
	}
}

func TestRunCleanupExitedGroupReportsKilledNo(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	tree.linkRunTools(t)
	log := tree.plantKill(t)

	done := exec.Command("true")
	done.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	err := done.Run()
	if err != nil {
		t.Fatal(err)
	}

	tree.makeTask(t, runTask, map[string]string{"started": "", "pgid": strconv.Itoa(done.Process.Pid) + "\n", "starttime": "5\n"})

	code, out := tree.cleanup(t, runTask)
	if code != 0 || out != "cleanup started=yes exit=- killed=no\n" {
		t.Errorf("exit = %d, output = %q", code, out)
	}

	_, err = os.Stat(log)
	if err == nil {
		t.Error("a signal was sent for a group that had already exited")
	}
}

func TestRunCleanupNeverSignalsOnABadPgid(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("kill_group reads /proc")
	}

	// Every case has a well formed starttime that the stubbed leader
	// matches, and the group stubbed live, so only the pgid refuses.
	for name, pgid := range map[string]string{
		"missing": "", "empty": "\n", "zero": "0\n", "one": "1\n", "minus one": "-1\n",
		"leading 0": "01\n", "two numbers": "12 34\n", "junk": "2; id\n",
		"too long": "99999999999\n", "wrap": "4294967295\n", "spaces": " 123\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			files := map[string]string{"started": "", "starttime": "777\n"}
			if name != "missing" {
				files["pgid"] = pgid
			}

			out, sent := killGroup(t, files, nil, liveStubs)
			if out != "no" || sent {
				t.Errorf("output = %q, signal sent = %v", out, sent)
			}
		})
	}
}

func TestRunCleanupSendsNoKillWhenTheLeaderChangesAfterTerm(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("kill_group reads /proc")
	}

	// The leader matches for the first checks, which are before TERM, and
	// has another start time from the fifth, which is after it.
	stubs := `_rc_group_state() { printf live; }
_n=0
_rc_stat() { _n=$((_n + 1)); local t=777; [ "$_n" -gt 4 ] && t=888; printf -v "$2" 'S 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 %s 0' "$t"; }
RC_KILL_TICKS=2
`

	tree := newRunTree(t)
	log := tree.plantKill(t)
	tree.makeTask(t, runTask, map[string]string{"started": "", "pgid": "4242\n", "starttime": "777\n"})

	dir := tree.path(runOps, runTask)

	code, out := tree.snippet(t, false, "CERTPATCH_ROOT="+tree.root+"\n"+stubs+"printf '%s\\n' \"$(_rc_kill_group \""+dir+"\")\"\n")
	if code != 0 {
		t.Fatalf("snippet exited %d", code)
	}

	logged, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}

	if string(logged) != "-TERM -- -4242\n" || strings.TrimSpace(out) != "yes" {
		t.Errorf("signals sent = %q, output = %q; want TERM only and yes", logged, out)
	}
}

func TestRunCleanupLeavesAGroupOutsideTheCommandsSession(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("process groups are read from /proc")
	}

	tree := newRunTree(t)
	tree.linkKill(t)

	// A group of the test's session, which is not one setsid made.
	stranger := exec.Command("sleep", "60")
	stranger.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stranger.WaitDelay = 2 * time.Second

	err := stranger.Start()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = syscall.Kill(-stranger.Process.Pid, syscall.SIGKILL)
		_ = stranger.Wait()
	})

	tree.makeTask(t, runTask, map[string]string{
		"started":   "",
		"pgid":      strconv.Itoa(stranger.Process.Pid) + "\n",
		"starttime": startTime(t, stranger.Process.Pid) + "\n",
	})

	code, out := tree.cleanup(t, runTask)
	if code != 0 || out != "cleanup started=yes exit=- killed=no\n" {
		t.Errorf("exit = %d, output = %q", code, out)
	}

	time.Sleep(300 * time.Millisecond)

	if !groupLive(t, stranger.Process.Pid) {
		t.Error("cleanup signalled a group that is not the command's session")
	}
}

func TestRunCleanupLeavesItsOwnGroupAndSession(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("process groups are read from /proc")
	}

	tree := newRunTree(t)
	tree.newSession = true

	code, out := tree.snippet(t, false, `read -r line < /proc/$BASHPID/stat
read -r _ _ pg sid _ <<< "${line##*) }"
echo "$(_rc_group_state "$pg") $(_rc_group_state "$sid")"
`)
	if code != 0 || out != "self self\n" {
		t.Errorf("exit = %d, output = %q", code, out)
	}
}

func TestRunCleanupLeavesACommandThatHasExited(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	log := tree.plantKill(t)
	tree.makeTask(t, runTask, map[string]string{"started": "", "exit": "3\n", "pgid": "2\n"})

	code, out := tree.cleanup(t, runTask)
	if code != 0 || out != "cleanup started=yes exit=3 killed=no\n" {
		t.Errorf("exit = %d, output = %q", code, out)
	}

	_, err := os.Stat(log)
	if err == nil {
		t.Error("a signal was sent although the command had recorded its exit")
	}
}

// killGroup runs _rc_kill_group on a task directory built from files,
// after setup has changed it, with the planted kill. stub text runs
// before the call and may replace functions of the script. It returns
// what the function printed and whether the planted kill ran.
func killGroup(t *testing.T, files map[string]string, setup func(dir string), stub string) (string, bool) {
	t.Helper()

	tree := newRunTree(t)
	log := tree.plantKill(t)
	tree.makeTask(t, runTask, files)

	dir := tree.path(runOps, runTask)

	if setup != nil {
		setup(dir)
	}

	code, out := tree.snippet(t, false, stub+"\nprintf '%s\\n' \"$(_rc_kill_group \""+dir+"\")\"\n")
	if code != 0 {
		t.Fatalf("snippet exited %d", code)
	}

	_, err := os.Stat(log)

	return strings.TrimSpace(out), err == nil
}

// liveStubs make every group look live and every leader match the
// starttime 777, so only the checks in front of them can refuse.
const liveStubs = `_rc_group_state() { printf live; }
_rc_stat() { printf -v "$2" 'S 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 777 0'; }
`

func TestRunCleanupValidatorAloneRefusesLowPgids(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("kill_group reads /proc")
	}

	for _, pgid := range []string{"0", "1", "00", "01", "-1", "2; id", "4294967295"} {
		t.Run(pgid, func(t *testing.T) {
			t.Parallel()

			out, sent := killGroup(t, map[string]string{"started": "", "pgid": pgid + "\n", "starttime": "777\n"}, nil, liveStubs)
			if out != "no" || sent {
				t.Errorf("output = %q, signal sent = %v", out, sent)
			}
		})
	}

	// The control is a good pgid under the same stubs.
	out, sent := killGroup(t, map[string]string{"started": "", "pgid": "4242\n", "starttime": "777\n"}, nil, liveStubs+"_n=0\n_rc_alive() { ((_n++ < 1)); }\n")
	if out != "yes" || !sent {
		t.Errorf("control: output = %q, signal sent = %v", out, sent)
	}
}

func TestRunCleanupGroupStateRefusesLowPgids(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("group state reads /proc")
	}

	tree := newRunTree(t)

	code, out := tree.snippet(t, false, `echo "$(_rc_group_state 0) $(_rc_group_state 1) $(_rc_group_state 01) $(_rc_group_state -1)"`+"\n")
	if code != 0 || out != "bad bad bad bad\n" {
		t.Errorf("exit = %d, output = %q", code, out)
	}
}

func TestRunCleanupPgidFileMustBeARegularFile(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("kill_group reads /proc")
	}

	files := map[string]string{"started": "", "pgid": "4242\n", "starttime": "777\n"}

	for name, setup := range map[string]func(dir string){
		"a symlink": func(dir string) {
			target := filepath.Join(dir, "target")
			_ = os.WriteFile(target, []byte("4242\n"), 0o600)
			_ = os.Remove(filepath.Join(dir, "pgid"))
			_ = os.Symlink(target, filepath.Join(dir, "pgid"))
		},
		"a directory": func(dir string) {
			_ = os.Remove(filepath.Join(dir, "pgid"))
			_ = os.Mkdir(filepath.Join(dir, "pgid"), 0o700)
		},
		"a starttime symlink": func(dir string) {
			target := filepath.Join(dir, "target")
			_ = os.WriteFile(target, []byte("777\n"), 0o600)
			_ = os.Remove(filepath.Join(dir, "starttime"))
			_ = os.Symlink(target, filepath.Join(dir, "starttime"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			out, sent := killGroup(t, files, setup, liveStubs)
			if out != "no" || sent {
				t.Errorf("output = %q, signal sent = %v", out, sent)
			}
		})
	}
}

func TestRunCleanupNeverSignalsItsOwnGroupThroughTheKillPath(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("kill_group reads /proc")
	}

	// The files name cleanup's own group and its true start time, so
	// only the refusal of its own group stands in the way.
	stub := `_rc_stat "/proc/$BASHPID/stat" own
read -r _ _ mypg _ <<< "$own"
read -ra f <<< "$own"
printf '%s\n' "$mypg" > "$_dir/pgid"
printf '%s\n' "${f[19]}" > "$_dir/starttime"
_rc_stat() { printf -v "$2" '%s' "$own"; }
`

	tree := newRunTree(t)
	tree.newSession = true
	log := tree.plantKill(t)
	tree.makeTask(t, runTask, map[string]string{"started": "", "pgid": "x", "starttime": "1"})

	dir := tree.path(runOps, runTask)
	text := "_dir=" + dir + "\n" + stub + "printf '%s\\n' \"$(_rc_kill_group \"$_dir\")\"\n"

	code, out := tree.snippet(t, false, text)
	if code != 0 || out != "no\n" {
		t.Errorf("exit = %d, output = %q", code, out)
	}

	_, err := os.Stat(log)
	if err == nil {
		t.Error("a signal was sent to cleanup's own group")
	}
}

func TestRunCleanupIgnoresAForgedCommName(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("process names are read from /proc")
	}

	tree := newRunTree(t)
	tree.linkKill(t)

	pidFile := tree.path("sleeper-pid")
	running := tree.startExec(t, "root", sleeperCommand(pidFile), pidFile)

	// A process outside the command whose name holds a newline and a
	// ")", so a line-by-line read of its stat file takes the group and
	// session from the name.
	// The name holds 15 bytes at most, which seven digits of pgid fit.
	forgery := fmt.Sprintf("x) S 1 %d\n", running.pgid)
	if len(forgery) > 15 {
		t.Skipf("the pgid %d is too long to forge in a 15 byte name", running.pgid)
	}

	forger := exec.Command("bash", "-c", `printf '%s' "$1" > /proc/$$/comm; sleep 60 & wait`, "_", forgery) //nolint:gosec // a test helper
	forger.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	forger.WaitDelay = 2 * time.Second

	err := forger.Start()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = syscall.Kill(-forger.Process.Pid, syscall.SIGKILL)
		_ = forger.Wait()
	})

	if !waitFor(5*time.Second, func() bool {
		raw, readErr := os.ReadFile("/proc/" + strconv.Itoa(forger.Process.Pid) + "/comm")

		return readErr == nil && strings.HasPrefix(string(raw), forgery)
	}) {
		t.Fatal("the forged name did not take")
	}

	code, out := tree.cleanup(t, runTask)
	if code != 0 || out != "cleanup started=yes exit=- killed=yes\n" {
		t.Errorf("exit = %d, output = %q", code, out)
	}

	if groupLive(t, running.pgid) {
		t.Error("the command's group is still alive")
	}
}

func TestRunCleanupRefusesAStarttimeThatDoesNotMatch(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		change func(t *testing.T, path, real string)
	}{
		{"a different one", func(t *testing.T, path, real string) {
			t.Helper()

			n, _ := strconv.Atoi(real)

			err := os.WriteFile(path, []byte(strconv.Itoa(n+1)+"\n"), 0o600)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"a malformed one", func(t *testing.T, path, _ string) {
			t.Helper()

			err := os.WriteFile(path, []byte("12 34\n"), 0o600)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"a missing one", func(t *testing.T, path, _ string) {
			t.Helper()

			err := os.Remove(path)
			if err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)

			pidFile := tree.path("sleeper-pid")
			running := tree.startExec(t, "root", sleeperCommand(pidFile), pidFile)

			// The planted kill logs and signals nothing.
			log := tree.plantKill(t)

			if running.starttime != startTime(t, running.pgid) {
				t.Errorf("starttime file holds %q, the leader's is %q", running.starttime, startTime(t, running.pgid))
			}

			tc.change(t, tree.path(runOps, runTask, "starttime"), running.starttime)

			code, out := tree.cleanup(t, runTask)
			if code != 0 || out != "cleanup started=yes exit=- killed=no\n" {
				t.Errorf("exit = %d, output = %q", code, out)
			}

			_, err := os.Stat(log)
			if err == nil {
				t.Error("a signal was sent although the start time did not match")
			}
		})
	}
}

func TestRunCleanupEndsAChildInAGroupOfItsOwn(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	tree.linkKill(t)

	// set -m puts the background job in a group of its own, within the
	// command's session. A child that calls setsid again leaves the
	// session and is out of reach of cleanup.
	pidFile := tree.path("sleeper-pid")
	running := tree.startExec(t, "root", "set -m\n"+sleeperCommand(pidFile), pidFile)

	fields := procFields(running.sleepPID)
	if len(fields) < 4 || fields[2] == strconv.Itoa(running.pgid) || fields[3] != strconv.Itoa(running.pgid) {
		t.Fatalf("the child is not in a group of its own within the session, fields %v", fields)
	}

	code, out := tree.cleanup(t, runTask)
	if code != 0 || out != "cleanup started=yes exit=- killed=yes\n" {
		t.Errorf("exit = %d, output = %q", code, out)
	}

	if sessionLive(t, running.pgid) {
		t.Error("a member of the command's session is still alive")
	}
}
