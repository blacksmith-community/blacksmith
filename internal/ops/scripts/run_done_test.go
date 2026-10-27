package scripts_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// donePath is where cleanup leaves the done record of a task.
func (r *runTree) donePath(task string) string {
	return r.path(runOps, task+".done")
}

// A cleanup that shows the command ended leaves a done record with its
// line in place of the task directory, and a repeat cleanup for the task
// prints that line again instead of reporting the directory gone, so an
// answer lost on the wire, or a broker that could not save it, can ask
// again and get the same evidence.
func TestRunCleanupReplaysTheDoneRecord(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	tree.makeTask(t, runTask, map[string]string{"cmd.sh": "x", "started": "", "exit": "7\n"})

	want := "cleanup started=yes exit=7 killed=no\n"

	for round := 1; round <= 2; round++ {
		code, out := tree.cleanup(t, runTask)
		if code != 0 || out != want {
			t.Errorf("cleanup %d exited %d with %q, want %q", round, code, out, want)
		}
	}

	if _, err := os.Lstat(tree.path(runOps, runTask)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the task directory stayed: %v", err)
	}

	info, err := os.Lstat(tree.donePath(runTask))
	if err != nil {
		t.Fatal(err)
	}

	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Errorf("the done record is %v, want a regular file with mode 0600", info.Mode())
	}

	if got, _ := os.ReadFile(tree.donePath(runTask)); string(got) != want { //nolint:gosec // the test tree
		t.Errorf("the done record holds %q, want %q", got, want)
	}

	if _, err := os.Lstat(tree.donePath(runTask) + ".new"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the record's temporary name stayed: %v", err)
	}
}

// A temporary record that an earlier failed write left behind does not
// stop the write of the record: cleanup clears it first, writes the
// record, and leaves no temporary file.
func TestRunCleanupWritesTheRecordOverALeftoverTemporaryFile(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	tree.makeTask(t, runTask, map[string]string{"started": "", "exit": "5\n"})

	err := os.WriteFile(tree.donePath(runTask)+".new", []byte("cleanup st"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	want := "cleanup started=yes exit=5 killed=no\n"

	code, out := tree.cleanup(t, runTask)
	if code != 0 || out != want {
		t.Fatalf("exit = %d, output = %q, want %q", code, out, want)
	}

	if got, _ := os.ReadFile(tree.donePath(runTask)); string(got) != want { //nolint:gosec // the test tree
		t.Errorf("the done record holds %q, want %q", got, want)
	}

	if _, err := os.Lstat(tree.donePath(runTask) + ".new"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the temporary file stayed: %v", err)
	}

	if _, err := os.Lstat(tree.path(runOps, runTask)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the task directory stayed: %v", err)
	}
}

// A done record whose task directory is still there, as a cleanup that
// died between the rename and the removal leaves it, is replayed, and the
// directory goes.
func TestRunCleanupReplayRemovesALeftoverDirectory(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	tree.makeTask(t, runTask, map[string]string{"started": ""})

	line := "cleanup started=yes exit=0 killed=no\n"

	err := os.WriteFile(tree.donePath(runTask), []byte(line), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	code, out := tree.cleanup(t, runTask)
	if code != 0 || out != line {
		t.Errorf("exit = %d, output = %q, want %q", code, out, line)
	}

	if _, err := os.Lstat(tree.path(runOps, runTask)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the task directory stayed: %v", err)
	}
}

// Only a done record that a cleanup which showed the command ended could
// have written is replayed: a root-owned regular file of mode 0600 that
// holds one whole line with killed=yes or no. Anything else proves nothing,
// so a cleanup with no task directory reports the directory gone.
func TestRunCleanupReplaysOnlyAWholeDoneRecord(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		text string
		mode os.FileMode
		link bool
	}{
		"unterminated":    {text: "cleanup started=yes exit=0 killed=no", mode: 0o600},
		"two lines":       {text: "cleanup started=yes exit=0 killed=no\ncleanup started=yes exit=0 killed=no\n", mode: 0o600},
		"killed=unknown":  {text: "cleanup started=yes exit=- killed=unknown\n", mode: 0o600},
		"killed=survived": {text: "cleanup started=yes exit=- killed=survived\n", mode: 0o600},
		"started=-":       {text: "cleanup started=- exit=- killed=no\n", mode: 0o600},
		"junk":            {text: "cleanup started=yes exit=0 killed=no; id\n", mode: 0o600},
		"empty":           {text: "", mode: 0o600},
		"mode 0644":       {text: "cleanup started=yes exit=0 killed=no\n", mode: 0o644},
		"a symlink":       {text: "cleanup started=yes exit=0 killed=no\n", mode: 0o600, link: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)
			tree.makeTask(t, "other", nil)

			err := os.RemoveAll(tree.path(runOps, "other"))
			if err != nil {
				t.Fatal(err)
			}

			target := tree.donePath(runTask)
			if tc.link {
				target = tree.path("elsewhere")
			}

			err = os.WriteFile(target, []byte(tc.text), tc.mode)
			if err == nil {
				err = os.Chmod(target, tc.mode)
			}

			if err != nil {
				t.Fatal(err)
			}

			if tc.link {
				err = os.Symlink(target, tree.donePath(runTask))
				if err != nil {
					t.Fatal(err)
				}
			}

			code, out := tree.cleanup(t, runTask)
			if code != 0 || out != "cleanup started=- exit=- killed=no\n" {
				t.Errorf("exit = %d, output = %q, want the directory reported gone", code, out)
			}
		})
	}
}

// Any cleanup removes done records, and the part-written ones a failed
// write leaves, once they are older than seven days. It never touches a
// younger record or a task directory, however old, because a directory
// that cleanup kept may still hold a command it could not check.
func TestRunCleanupPrunesOnlyOldDoneRecords(t *testing.T) {
	t.Parallel()

	tree := newRunTree(t)
	tree.makeTask(t, "kept-task", map[string]string{"started": ""})
	tree.makeTask(t, runTask, map[string]string{"started": "", "exit": "0\n"})

	old := time.Now().Add(-8 * 24 * time.Hour)
	young := time.Now().Add(-6 * 24 * time.Hour)

	files := map[string]time.Time{
		tree.donePath("old-task"):              old,
		tree.donePath("old-task-new") + ".new": old,
		tree.donePath("young-task"):            young,
	}

	for path, when := range files {
		err := os.WriteFile(path, []byte("cleanup started=yes exit=0 killed=no\n"), 0o600)
		if err != nil {
			t.Fatal(err)
		}

		err = os.Chtimes(path, when, when)
		if err != nil {
			t.Fatal(err)
		}
	}

	// A directory with a record's name is not a record, so it stays too.
	err := os.Mkdir(tree.donePath("dir-task"), 0o700)
	if err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{tree.path(runOps, "kept-task"), tree.path(runOps, "kept-task", "started"), tree.donePath("dir-task")} {
		err := os.Chtimes(dir, old, old)
		if err != nil {
			t.Fatal(err)
		}
	}

	code, out := tree.cleanup(t, runTask)
	if code != 0 || out != "cleanup started=yes exit=0 killed=no\n" {
		t.Fatalf("exit = %d, output = %q", code, out)
	}

	for path, when := range files {
		_, err := os.Lstat(path)
		if gone := errors.Is(err, os.ErrNotExist); gone != when.Equal(old) {
			t.Errorf("%s gone = %v, want %v", filepath.Base(path), gone, when.Equal(old))
		}
	}

	if _, err := os.Stat(tree.path(runOps, "kept-task", "started")); err != nil {
		t.Errorf("pruning removed a task directory: %v", err)
	}

	if _, err := os.Stat(tree.donePath("dir-task")); err != nil {
		t.Errorf("pruning removed a directory with a record's name: %v", err)
	}

	if _, err := os.Stat(tree.donePath(runTask)); err != nil {
		t.Errorf("the record this cleanup wrote went: %v", err)
	}
}

// The done record is written under a temporary name and renamed, so no
// part of a line ever stands under the record's name. When the write or
// the rename fails, cleanup still prints its line, leaves no record and
// no temporary file, and keeps the task directory as it was, so the next
// cleanup works the answer out again and writes the record then.
func TestRunCleanupKeepsTheTaskWhenTheDoneRecordFails(t *testing.T) {
	t.Parallel()

	for name, stub := range map[string]string{
		"write":  `_rc_write() { head -c 10 > "$1"; return 1; }` + "\n",
		"rename": `mv() { return 1; }` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := newRunTree(t)
			tree.makeTask(t, runTask, map[string]string{"cmd.sh": "x", "started": "", "exit": "3\n"})

			// A temporary file that an earlier failed write left behind
			// does not stop the next write.
			err := os.WriteFile(tree.donePath(runTask)+".new", []byte("cleanup st"), 0o600)
			if err != nil {
				t.Fatal(err)
			}

			want := "cleanup started=yes exit=3 killed=no\n"

			code, out := tree.snippet(t, false, stub+"cleanup "+runTask+"\n")
			if code != 0 || out != want {
				t.Fatalf("exit = %d, output = %q, want %q", code, out, want)
			}

			for _, path := range []string{tree.donePath(runTask), tree.donePath(runTask) + ".new"} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("%s stands after a failed write: %v", filepath.Base(path), err)
				}
			}

			for _, name := range []string{"cmd.sh", "started", "exit"} {
				if _, err := os.Stat(tree.path(runOps, runTask, name)); err != nil {
					t.Errorf("the task directory lost %s: %v", name, err)
				}
			}

			code, out = tree.cleanup(t, runTask)
			if code != 0 || out != want {
				t.Errorf("the next cleanup exited %d with %q, want %q", code, out, want)
			}

			if got, _ := os.ReadFile(tree.donePath(runTask)); string(got) != want { //nolint:gosec // the test tree
				t.Errorf("the next cleanup wrote %q, want %q", got, want)
			}
		})
	}
}
