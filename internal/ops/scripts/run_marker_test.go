package scripts_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// runMarker is the entry exec puts into the environment of the command of
// runTask.
const runMarker = "BLACKSMITH_RUN_TASK=" + runTask

// leaderlessSession starts a session whose leader starts one sleeper and
// exits, which leaves the sleeper in a session with no leader. The leader
// runs with env, and the sleeper starts through prefix, such as "env -i".
// It returns the session's ID, the leader's start time, and the sleeper's
// pid.
func leaderlessSession(t *testing.T, tree *runTree, env []string, prefix string) (int, string, int) {
	t.Helper()

	setsid, err := exec.LookPath("setsid")
	if err != nil {
		t.Skip("no setsid on this host")
	}

	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep on this host")
	}

	pidFile := tree.path("sleeper-pid")
	statFile := tree.path("leader-stat")
	script := fmt.Sprintf(`cat /proc/$$/stat > "$2"; %s %q 60 & echo $! > "$1"; exit 0`, prefix, sleep)

	// Not a group leader, so setsid does not fork and the leader is this
	// process's own child, which Wait reaps.
	leader := exec.Command(setsid, "--wait", bashPath(t), "-c", script, "_", pidFile, statFile) //nolint:gosec // a test helper
	leader.Env = env
	leader.WaitDelay = 2 * time.Second

	err = leader.Start()
	if err != nil {
		t.Fatal(err)
	}

	sid := leader.Process.Pid

	t.Cleanup(func() {
		_ = syscall.Kill(-sid, syscall.SIGKILL)

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

	sleeper, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(sleeper)))
	if err != nil {
		t.Fatal(err)
	}

	if !sessionLive(t, sid) || len(procFields(sid)) != 0 {
		t.Fatal("the session has no member left, or its leader is still in /proc")
	}

	return sid, fields[19], pid
}

// A session whose leader is gone may be a stranger's that took the
// recorded number after the command's session emptied, as a daemon that
// forks, calls setsid, forks, and exits makes. So cleanup sends KILL only
// to a member whose environment holds the task's marker, in a task
// directory whose marker file says the command was given one. Any other
// live member gets nothing, and the answer holds the node: unknown on a
// first cleanup, and survived on a recheck of recorded survivors. A
// session whose members all prove to be the command's is ended, and
// reported yes.
func TestRunCleanupProvesEachMemberOfALeaderlessSession(t *testing.T) {
	t.Parallel()
	needDoneRecords(t)

	path := "PATH=" + os.Getenv("PATH")

	cases := []struct {
		name      string
		env       []string
		prefix    string
		noMarker  bool
		survivors bool
		want      string
	}{
		{name: "a stranger with no marker", env: []string{path}, want: "unknown"},
		{name: "a stranger with another task's marker", env: []string{path, "BLACKSMITH_RUN_TASK=1a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"}, want: "unknown"},
		{name: "a child with the marker", env: []string{path, runMarker}, want: "yes"},
		{name: "a child that cleared its environment", env: []string{path, runMarker}, prefix: "env -i", want: "unknown"},
		{name: "an old directory without the marker file", env: []string{path, runMarker}, noMarker: true, want: "unknown"},
		{name: "a recheck of a stranger", env: []string{path}, survivors: true, want: "survived"},
		{name: "a recheck of a child with the marker", env: []string{path, runMarker}, survivors: true, want: "yes"},
		{name: "a recheck of a child that cleared its environment", env: []string{path, runMarker}, prefix: "env -i", survivors: true, want: "survived"},
		{name: "a recheck in an old directory", env: []string{path, runMarker}, noMarker: true, survivors: true, want: "survived"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)
			tree.linkKill(t)
			tree.env = []string{"RC_KILL_TICKS=3"}

			sid, starttime, sleeper := leaderlessSession(t, tree, tc.env, tc.prefix)

			files := map[string]string{
				"started":   "",
				"pgid":      strconv.Itoa(sid) + "\n",
				"starttime": starttime + "\n",
			}

			if !tc.noMarker {
				files["marker"] = ""
			}

			if tc.survivors {
				files["survivors"] = strconv.Itoa(sleeper) + " " + startTime(t, sleeper) + "\n"
			}

			tree.makeTask(t, runTask, files)

			code, out := tree.cleanup(t, runTask)
			if code != 0 || out != "cleanup started=yes exit=- killed="+tc.want+"\n" {
				t.Errorf("exit = %d, output = %q, want killed=%s", code, out, tc.want)
			}

			if strings.Contains(out, "BLACKSMITH_RUN_TASK") {
				t.Errorf("the output %q names the marker", out)
			}

			_, dirErr := os.Stat(tree.path(runOps, runTask))

			if tc.want == "yes" {
				if sessionLive(t, sid) {
					t.Error("a member that holds the marker outlived cleanup")
				}

				if !errors.Is(dirErr, os.ErrNotExist) {
					t.Errorf("the task directory stayed: %v", dirErr)
				}

				return
			}

			if !sessionLive(t, sid) || len(procFields(sleeper)) == 0 || procFields(sleeper)[0] == "Z" {
				t.Error("cleanup signalled a member it could not prove was the command's")
			}

			if dirErr != nil {
				t.Errorf("the task directory went while an unproven member lives: %v", dirErr)
			}
		})
	}
}

// While the recorded leader lives with its start time, the session is the
// command's, as _rc_leader_ok explains, so cleanup ends the whole group,
// a member without the marker included.
func TestRunCleanupEndsTheGroupOfALiveLeaderWithoutTheMarker(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	tree.linkKill(t)

	pidFile := tree.path("sleeper-pid")
	running := tree.startExec(t, "root", fmt.Sprintf("env -i sleep 60 &\necho $! > %q\nwait\n", pidFile), pidFile)

	code, out := tree.cleanup(t, runTask)
	if code != 0 || out != "cleanup started=yes exit=- killed=yes\n" {
		t.Errorf("exit = %d, output = %q", code, out)
	}

	if sessionLive(t, running.pgid) {
		t.Error("a member of a session with a live leader outlived cleanup")
	}
}

// exec gives the command the task's marker and leaves the marker file
// beside the started marker, so a later cleanup can prove the command's
// processes once their leader has gone.
func TestRunExecGivesTheCommandTheTaskMarker(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)

	code, out, errOut := tree.stageAndExec(t, `printf '%s\n' "${BLACKSMITH_RUN_TASK-unset}"`+"\n")
	if code != 0 || out != runTask+"\n" {
		t.Errorf("exit = %d, stdout %q, stderr %q; want the task ID", code, out, errOut)
	}

	if got := tree.taskFile(t, "marker"); got != "" {
		t.Errorf("the marker file holds %q, want nothing", got)
	}
}

// A cleanup sends a leaderless session no signal while none of its members
// is proven, so it reports unknown without waiting out the TERM and KILL
// ticks for members it would never signal.
func TestRunCleanupWaitsForNoMemberItCannotSignal(t *testing.T) {
	t.Parallel()
	needDoneRecords(t)

	tree := newRunTree(t)
	tree.linkKill(t)
	tree.env = []string{"RC_KILL_TICKS=100"}

	sid, starttime, _ := leaderlessSession(t, tree, []string{"PATH=" + os.Getenv("PATH")}, "")

	tree.makeTask(t, runTask, map[string]string{
		"started":   "",
		"marker":    "",
		"pgid":      strconv.Itoa(sid) + "\n",
		"starttime": starttime + "\n",
	})

	began := time.Now()

	code, out := tree.cleanup(t, runTask)
	if code != 0 || out != "cleanup started=yes exit=- killed=unknown\n" {
		t.Errorf("exit = %d, output = %q, want killed=unknown", code, out)
	}

	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("cleanup took %v over a session it could not signal", took)
	}
}

// A recheck whose recorded leader pid another process holds, with another
// start time, has no live leader behind it, so a recorded survivor gets
// KILL only once its marker proves it is the command's.
func TestRunCleanupRecheckProvesASurvivorBehindAReusedLeaderPid(t *testing.T) {
	t.Parallel()
	needDoneRecords(t)

	tree := newRunTree(t)
	tree.linkKill(t)
	tree.env = []string{"RC_KILL_TICKS=3"}

	stranger := exec.Command("sleep", "60")
	stranger.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	err := stranger.Start()
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = stranger.Process.Kill(); _ = stranger.Wait() }()

	pid := stranger.Process.Pid

	tree.makeTask(t, runTask, map[string]string{
		"started":   "",
		"marker":    "",
		"pgid":      strconv.Itoa(pid) + "\n",
		"starttime": "1\n",
		"survivors": strconv.Itoa(pid) + " " + startTime(t, pid) + "\n",
	})

	code, out := tree.cleanup(t, runTask)
	if code != 0 || out != "cleanup started=yes exit=- killed=survived\n" {
		t.Errorf("exit = %d, output = %q, want killed=survived", code, out)
	}

	if f := procFields(pid); len(f) == 0 || f[0] == "Z" {
		t.Error("the recheck killed a survivor it could not prove was the command's")
	}
}

// _rc_proven reads a member's stat again after its environment, so a
// member that became a zombie in between is left out rather than counted
// unproven, and a pid that a new process took in between is never proven,
// whatever that process's environment holds.
func TestRunProvenReadsTheMemberAgain(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("_rc_proven reads /proc")
	}

	cases := []struct {
		name   string
		marker bool
		second string
		want   string
	}{
		{name: "a proven member", marker: true, second: "S", want: "0 yes"},
		{name: "an unproven member", second: "S", want: "1 no"},
		{name: "an unproven member that became a zombie", second: "Z", want: "0 no"},
		{name: "a proven pid that a new process took", marker: true, second: "new", want: "0 no"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)
			tree.makeTask(t, runTask, map[string]string{"started": "", "marker": ""})

			env := "env -i"
			if tc.marker {
				env += " " + runMarker
			}

			// The sleeper's environment is read once env has exec'd sleep.
			// The stub shows the sleeper in session 4242 with start time 5
			// on the first read and as the case says on the second, and
			// every other process as gone.
			script := fmt.Sprintf(`%s sleep 60 &
m=$!
c=
until [ "$c" = sleep ]; do
	sleep 0.01
	{ IFS= read -r -d '' c < "/proc/$m/cmdline"; } 2> /dev/null
done
_n=0
_rc_stat() {
	[ "$1" = "/proc/$m/stat" ] || return 1
	local state=S start=5
	if ((_n++ > 0)); then
		case %q in Z) state=Z ;; new) start=6 ;; esac
	fi
	printf -v "$2" '%%s 0 0 4242 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 %%s 0' "$state" "$start"
}
proven=()
_rc_proven %q 4242
rc=$?
got=no
[ "${proven[*]}" != "$m" ] || got=yes
kill -KILL "$m"
printf '%%s %%s\n' "$rc" "$got"
`, env, tc.second, tree.path(runOps, runTask))

			code, out := tree.snippet(t, false, script)
			if code != 0 || strings.TrimSpace(out) != tc.want {
				t.Errorf("exit = %d, output = %q, want %q", code, out, tc.want)
			}
		})
	}
}
