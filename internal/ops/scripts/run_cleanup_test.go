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

	// The slot is held while the wrapper starts and no longer, because the
	// test goes on to run cleanup while the command lives on.
	freeSlot := takeRunSlot(t)
	defer freeSlot()

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

// survivorTree is a task whose cleanup found processes that outlived
// KILL, with the planted kill that let them live still in place.
func survivorTree(t *testing.T) (*runTree, runningCommand, string) {
	t.Helper()

	tree := newRunTree(t)
	tree.linkKill(t)
	tree.env = []string{"RC_KILL_TICKS=3"}

	pidFile := tree.path("sleeper-pid")
	running := tree.startExec(t, "root", sleeperCommand(pidFile), pidFile)
	log := tree.plantKill(t)

	code, out := tree.cleanup(t, runTask)
	if code != 0 || out != "cleanup started=yes exit=- killed=survived\n" {
		t.Fatalf("exit = %d, output = %q", code, out)
	}

	return tree, running, log
}

// relinkKill takes the planted kill away and puts the real one back.
func (r *runTree) relinkKill(t *testing.T) {
	t.Helper()

	err := os.Remove(r.path("usr/bin/kill"))
	if err != nil {
		t.Fatal(err)
	}

	r.linkKill(t)
}

// A cleanup that reports survivors keeps the task directory and a record
// of each live member of the session with its start time, so a later
// cleanup can tell whether the same processes still live. The command's
// text goes.
func TestRunCleanupKeepsARecordOfTheSurvivors(t *testing.T) {
	t.Parallel()

	tree, running, _ := survivorTree(t)

	if _, err := os.Stat(tree.path(runOps, runTask, "cmd.sh")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("cmd.sh stayed beside the survivors: %v", err)
	}

	record := tree.taskFile(t, "survivors")

	for _, pid := range []int{running.pgid, running.sleepPID} {
		line := fmt.Sprintf("%d %s\n", pid, startTime(t, pid))
		if !strings.Contains(record, line) {
			t.Errorf("the survivors record %q lacks %q", record, line)
		}
	}
}

// A later cleanup sends KILL to the recorded survivors again, and while
// they live it reports them again and keeps the record.
func TestRunCleanupReportsTheSurvivorsAgainWhileTheyLive(t *testing.T) {
	t.Parallel()

	tree, running, log := survivorTree(t)

	err := os.Truncate(log, 0)
	if err != nil {
		t.Fatal(err)
	}

	code, out := tree.cleanup(t, runTask)
	if code != 0 || out != "cleanup started=yes exit=- killed=survived\n" {
		t.Errorf("exit = %d, output = %q", code, out)
	}

	logged, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}

	for _, pid := range []int{running.pgid, running.sleepPID} {
		if !strings.Contains(string(logged), " "+strconv.Itoa(pid)) || !strings.HasPrefix(string(logged), "-KILL -- ") {
			t.Errorf("the second cleanup sent %q, want KILL to %d", logged, pid)
		}
	}

	if _, err := os.Stat(tree.path(runOps, runTask, "survivors")); err != nil {
		t.Errorf("the survivors record went while they live: %v", err)
	}
}

// A later cleanup that ends the survivors reports them killed and removes
// the task directory.
func TestRunCleanupEndsTheSurvivorsAndRemovesTheRecord(t *testing.T) {
	t.Parallel()

	tree, running, _ := survivorTree(t)
	tree.relinkKill(t)

	code, out := tree.cleanup(t, runTask)
	if code != 0 || out != "cleanup started=yes exit=- killed=yes\n" {
		t.Errorf("exit = %d, output = %q", code, out)
	}

	if sessionLive(t, running.pgid) {
		t.Error("the survivors still live")
	}

	if _, err := os.Stat(tree.path(runOps, runTask)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the task directory stayed: %v", err)
	}
}

// Survivors that ended on their own are reported killed, and nothing is
// signalled, not even a process that now has a recorded pid with another
// start time.
func TestRunCleanupTrustsOnlyTheRecordedStartTimes(t *testing.T) {
	t.Parallel()

	tree, running, log := survivorTree(t)

	for _, pid := range []int{running.sleepPID, running.pgid} {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}

	if !waitFor(10*time.Second, func() bool { return !sessionLive(t, running.pgid) }) {
		t.Fatal("the survivors did not end")
	}

	stranger := os.Getpid()
	record := fmt.Sprintf("%d %s\n%d 1\n", running.sleepPID, running.starttime, stranger)

	err := os.WriteFile(tree.path(runOps, runTask, "survivors"), []byte(record), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = os.Truncate(log, 0)
	if err != nil {
		t.Fatal(err)
	}

	// The wrapper records the exit of the leader the test ended.
	code, out := tree.cleanup(t, runTask)
	if code != 0 || !strings.HasPrefix(out, "cleanup started=yes exit=") || !strings.HasSuffix(out, " killed=yes\n") {
		t.Errorf("exit = %d, output = %q", code, out)
	}

	if logged, _ := os.ReadFile(log); len(logged) != 0 {
		t.Errorf("cleanup signalled %q, want nothing", logged)
	}

	if _, err := os.Stat(tree.path(runOps, runTask)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the task directory stayed: %v", err)
	}
}

// A survivors record that can't be read proves nothing, so the processes
// are reported as surviving and the record stays.
func TestRunCleanupKeepsAnUnreadableSurvivorsRecord(t *testing.T) {
	t.Parallel()

	tree, _, _ := survivorTree(t)

	err := os.WriteFile(tree.path(runOps, runTask, "survivors"), []byte("not a record\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	code, out := tree.cleanup(t, runTask)
	if code != 0 || out != "cleanup started=yes exit=- killed=survived\n" {
		t.Errorf("exit = %d, output = %q", code, out)
	}

	if _, err := os.Stat(tree.path(runOps, runTask, "survivors")); err != nil {
		t.Errorf("the survivors record went: %v", err)
	}
}

// A survivors record that is empty, or whose last line has no newline, may
// be the start of a record whose write failed, so it proves nothing. The
// processes are reported as surviving, nothing is signalled, and the record
// stays.
func TestRunCleanupDistrustsAnIncompleteSurvivorsRecord(t *testing.T) {
	t.Parallel()

	for name, record := range map[string]func(runningCommand) string{
		"empty": func(runningCommand) string { return "" },
		"unterminated": func(running runningCommand) string {
			return fmt.Sprintf("%d %s", running.sleepPID, startTime(t, running.sleepPID))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree, running, log := survivorTree(t)

			err := os.WriteFile(tree.path(runOps, runTask, "survivors"), []byte(record(running)), 0o600)
			if err != nil {
				t.Fatal(err)
			}

			err = os.Truncate(log, 0)
			if err != nil {
				t.Fatal(err)
			}

			code, out := tree.cleanup(t, runTask)
			if code != 0 || out != "cleanup started=yes exit=- killed=survived\n" {
				t.Errorf("exit = %d, output = %q", code, out)
			}

			if logged, _ := os.ReadFile(log); len(logged) != 0 {
				t.Errorf("cleanup signalled %q, want nothing", logged)
			}

			if _, err := os.Stat(tree.path(runOps, runTask, "survivors")); err != nil {
				t.Errorf("the survivors record went: %v", err)
			}
		})
	}
}

// A survivors record whose write or rename fails still reports the
// processes as surviving and keeps the task directory, and no part of the
// record ever stands under its own name. Every later cleanup reports them
// again and signals nothing, because no whole record names them, even once
// a working kill is back.
func TestRunCleanupKeepsTheTaskWhenTheSurvivorsRecordFails(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("cleanup reads /proc")
	}

	for name, stub := range map[string]string{
		// The first member's line lands and the write then fails, as on a
		// disk that fills part way through.
		"write":  `_rc_write() { head -n 1 > "$1"; return 1; }` + "\n",
		"rename": `mv() { return 1; }` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)
			tree.linkKill(t)
			tree.env = []string{"RC_KILL_TICKS=3"}

			pidFile := tree.path("sleeper-pid")
			running := tree.startExec(t, "root", sleeperCommand(pidFile), pidFile)
			log := tree.plantKill(t)

			code, out := tree.snippet(t, false, "RC_KILL_TICKS=3\n"+stub+"cleanup "+runTask+"\n")
			if code != 0 || out != "cleanup started=yes exit=- killed=survived\n" {
				t.Fatalf("exit = %d, output = %q", code, out)
			}

			if _, err := os.Stat(tree.path(runOps, runTask, "survivors")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a survivors record whose write failed stands under its name: %v", err)
			}

			tree.relinkKill(t)

			code, out = tree.cleanup(t, runTask)
			if code != 0 || out != "cleanup started=yes exit=- killed=survived\n" {
				t.Errorf("the later cleanup exited %d with %q, want the survivors reported again", code, out)
			}

			if _, err := os.Stat(tree.path(runOps, runTask)); err != nil {
				t.Errorf("the task directory went: %v", err)
			}

			if !sessionLive(t, running.pgid) {
				t.Error("the later cleanup ended processes that no whole record names")
			}

			if logged, _ := os.ReadFile(log); !strings.Contains(string(logged), "-KILL") {
				t.Errorf("the first cleanup sent %q, want TERM and KILL", logged)
			}
		})
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

func TestRunCleanupLeavesASessionWhoseLeaderHasExited(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("sessions are read from /proc")
	}

	tree := newRunTree(t)
	log := tree.plantKill(t)

	setsid, err := exec.LookPath("setsid")
	if err != nil {
		t.Skip("no setsid on this host")
	}

	pidFile := tree.path("sleeper-pid")
	statFile := tree.path("leader-stat")
	script := `echo $$ > "$1.leader"; cat /proc/$$/stat > "$2"; sleep 60 & echo $! > "$1"; exit 0`

	// Not a group leader, so setsid does not fork and the leader is this
	// process's own child, which Wait reaps.
	leader := exec.Command(setsid, "--wait", bashPath(t), "-c", script, "_", pidFile, statFile) //nolint:gosec // a test helper
	leader.WaitDelay = 2 * time.Second

	err = leader.Start()
	if err != nil {
		t.Fatal(err)
	}

	pgid := leader.Process.Pid

	t.Cleanup(func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)

		raw, readErr := os.ReadFile(pidFile)
		if pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw))); readErr == nil && convErr == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	err = leader.Wait()
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(statFile)
	if err != nil {
		t.Fatal(err)
	}

	fields := strings.Fields(string(raw[bytes.LastIndex(raw, []byte(") "))+2:]))
	if len(fields) < 20 {
		t.Fatalf("the leader's stat has %d fields", len(fields))
	}

	if !sessionLive(t, pgid) {
		t.Fatal("no member of the session remains after the leader exited")
	}

	if len(procFields(pgid)) != 0 {
		t.Fatal("the leader is still in /proc")
	}

	tree.makeTask(t, runTask, map[string]string{
		"started":   "",
		"pgid":      strconv.Itoa(pgid) + "\n",
		"starttime": fields[19] + "\n",
	})

	code, out := tree.cleanup(t, runTask)
	if code != 0 || out != "cleanup started=yes exit=- killed=no\n" {
		t.Errorf("exit = %d, output = %q", code, out)
	}

	_, err = os.Stat(log)
	if err == nil {
		t.Error("a signal was sent to a session whose leader is gone")
	}

	if !sessionLive(t, pgid) {
		t.Error("the member of the session was ended")
	}
}
