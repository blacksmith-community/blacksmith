package certscripts_test

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"blacksmith/internal/certpatch"
	"blacksmith/internal/upgrade/certscripts"
)

// The fixture contents and the work paths of testTaskID that the patch
// tests use. oldText is what newPinnedTree writes to the target.
const (
	oldText     = "old\n"
	newText     = "new\n"
	thirdText   = "third\n"
	patchTask   = workBase + testTaskID
	stagePath   = patchTask + "/s/0"
	backupPath  = patchTask + "/b/0"
	targetDir   = pinnedJobDir + "/config"
	monitPath   = "/var/vcap/bosh/bin/monit"
	opCheck     = "check"
	driftExit   = 10
	backupExit  = 11
	restartExit = 12
)

// textHash is the normalised hash of a fixture's content.
func textHash(text string) string {
	return certpatch.NormalisedHash([]byte(text))
}

// newPatchTree builds a tree whose pinned target holds oldText, is owned
// by vcap with mode 0640, and whose task directory prepare has made.
func newPatchTree(t *testing.T) *vmTree {
	t.Helper()

	tree := newPinnedTree(t, pinnedTarget, pinnedJobDir)
	tree.giveToVCAP(t, pinnedTarget)
	tree.chmod(t, pinnedTarget, 0o640)

	if _, code := tree.patch(t, "prepare", testTaskID); code != 0 {
		t.Fatalf("prepare exited %d, want 0", code)
	}

	return tree
}

// patch runs one operation of patch.sh in the tree.
func (v *vmTree) patch(t *testing.T, operation string, args ...string) (string, int) {
	t.Helper()

	return runScript(t, v.root, nil, "patch", operation, args...)
}

// stage writes the staged file of testTaskID directly, as the staging
// command would.
func (v *vmTree) stage(t *testing.T, content string) {
	t.Helper()
	v.write(t, stagePath, content)
}

func (v *vmTree) backup(t *testing.T, old string) (string, int) {
	t.Helper()

	return v.patch(t, "backup", testTaskID, pinnedJobDir, pinnedTarget, textHash(old), backupPath)
}

func (v *vmTree) replaceTarget(t *testing.T) (string, int) {
	t.Helper()

	return v.patch(t, "replace", testTaskID, pinnedJobDir, pinnedTarget, stagePath, textHash(oldText), textHash(newText))
}

func (v *vmTree) restore(t *testing.T) (string, int) {
	t.Helper()

	return v.patch(t, "restore", testTaskID, pinnedJobDir, pinnedTarget, backupPath, textHash(oldText), textHash(newText))
}

func (v *vmTree) check(t *testing.T) (string, int) {
	t.Helper()

	return v.patch(t, opCheck, testTaskID, pinnedJobDir, pinnedTarget, textHash(oldText), textHash(newText), backupPath)
}

// exists reports whether anything, a dangling symlink included, is at
// path in the tree.
func (v *vmTree) exists(t *testing.T, path string) bool {
	t.Helper()

	_, err := os.Lstat(v.at(path))
	if err == nil {
		return true
	}

	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}

	return false
}

// assertOwnerMode checks a file's owner, group, and permission bits.
func (v *vmTree) assertOwnerMode(t *testing.T, path string, uid uint32, mode os.FileMode) {
	t.Helper()

	info, err := os.Lstat(v.at(path))
	if err != nil {
		t.Fatal(err)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("no stat for the file")
	}

	if stat.Uid != uid || stat.Gid != uid {
		t.Errorf("%s is owned by %d:%d, want %d:%d", path, stat.Uid, stat.Gid, uid, uid)
	}

	if info.Mode().Perm() != mode {
		t.Errorf("%s has mode %04o, want %04o", path, info.Mode().Perm(), mode)
	}
}

// assertOnlyTarget checks that the target's directory holds the target
// and nothing else, so no temporary file was left behind.
func (v *vmTree) assertOnlyTarget(t *testing.T) {
	t.Helper()

	entries, err := os.ReadDir(v.at(targetDir))
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 1 || entries[0].Name() != "server.crt" {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}

		t.Errorf("the target directory holds %v, want only server.crt", names)
	}
}

func wantOutput(t *testing.T, got, want string) {
	t.Helper()

	if got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestScriptPatch(t *testing.T) {
	t.Parallel()

	body, err := certscripts.Script("patch")
	if err != nil {
		t.Fatalf("Script(patch): %v", err)
	}

	if body != readSource(t, libScript)+readSource(t, "patch") {
		t.Error("Script(patch) is not lib.sh followed by patch.sh")
	}
}

func TestPrepareMakesPrivateDirs(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)

	for _, dir := range []string{strings.TrimSuffix(workBase, "/"), patchTask, patchTask + "/s", patchTask + "/b"} {
		tree.assertOwnerMode(t, dir, 0, 0o700)
	}

	// A second attempt of the same task accepts its own directory.
	out, code := tree.patch(t, "prepare", testTaskID)
	if code != 0 {
		t.Fatalf("a second prepare exited %d, want 0", code)
	}

	wantOutput(t, out, "ok path="+patchTask+"\n")
}

func TestPrepareRefusesForeignTaskDir(t *testing.T) {
	t.Parallel()

	tree := newPinnedTree(t, pinnedTarget, pinnedJobDir)
	tree.mkdir(t, workBase)
	tree.chmod(t, workBase, 0o700)
	tree.mkdir(t, patchTask)
	tree.chmod(t, patchTask, 0o700)
	tree.giveToVCAP(t, patchTask)

	if _, code := tree.patch(t, "prepare", testTaskID); code != refusedExit {
		t.Fatalf("prepare in a foreign task directory exited %d, want 13", code)
	}

	if tree.exists(t, patchTask+"/s") || tree.exists(t, patchTask+"/b") {
		t.Error("prepare made a directory inside a foreign task directory")
	}
}

// TestStagingNeedsPrepare shows that staging works only after prepare has
// made the task's directories, and that staging itself refuses every
// tree that prepare refuses, so no caller can stage before those checks.
func TestStagingNeedsPrepare(t *testing.T) {
	t.Parallel()

	t.Run("a fresh tree", func(t *testing.T) {
		t.Parallel()

		tree := newPinnedTree(t, pinnedTarget, pinnedJobDir)

		if code := runStage(t, tree.root, stagePath, stagedText); code != refusedExit {
			t.Fatalf("staging before prepare exited %d, want 13", code)
		}

		if tree.exists(t, workBase) {
			t.Error("staging before prepare created the work directory")
		}

		if _, code := tree.patch(t, "prepare", testTaskID); code != 0 {
			t.Fatalf("prepare exited %d, want 0", code)
		}

		if code := runStage(t, tree.root, stagePath, stagedText); code != 0 {
			t.Fatalf("staging after prepare exited %d, want 0", code)
		}

		tree.assert(t, stagePath, stagedText)
	})

	t.Run("an insecure tree", func(t *testing.T) {
		t.Parallel()

		tree := newPinnedTree(t, pinnedTarget, pinnedJobDir)
		tree.chmod(t, vcapData, 0o777)

		if _, code := tree.patch(t, "prepare", testTaskID); code != refusedExit {
			t.Fatalf("prepare in an insecure tree exited %d, want 13", code)
		}

		if tree.exists(t, workBase) {
			t.Error("prepare in an insecure tree created the work directory")
		}

		if code := runStage(t, tree.root, stagePath, stagedText); code != refusedExit {
			t.Errorf("staging in an insecure tree exited %d, want 13", code)
		}
	})
}

// TestStagingRefusesPlantedTree shows that a planted task directory with
// a private s directory, inside a work directory others can write or as
// a task directory others can read, which a check of the s directory
// alone would pass, makes both prepare and staging refuse.
func TestStagingRefusesPlantedTree(t *testing.T) {
	t.Parallel()

	for name, open := range map[string]string{
		"a planted work directory": workBase,
		"an open task directory":   patchTask,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := newPinnedTree(t, pinnedTarget, pinnedJobDir)
			tree.mkdir(t, patchTask+"/s")
			tree.mkdir(t, patchTask+"/b")
			tree.chmod(t, patchTask+"/s", 0o700)
			tree.chmod(t, patchTask+"/b", 0o700)
			tree.chmod(t, patchTask, 0o700)
			tree.chmod(t, workBase, 0o700)

			mode := os.FileMode(0o755)
			if open == workBase {
				mode = 0o777
			}

			tree.chmod(t, open, mode)

			if _, code := tree.patch(t, "prepare", testTaskID); code != refusedExit {
				t.Fatalf("prepare exited %d, want 13", code)
			}

			if code := runStage(t, tree.root, stagePath, stagedText); code != refusedExit {
				t.Errorf("staging exited %d, want 13", code)
			}

			if tree.exists(t, stagePath) {
				t.Error("the refused staging wrote the file")
			}
		})
	}
}

func TestStagecheck(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)
	tree.stage(t, "new\r\n")

	out, code := tree.patch(t, "stagecheck", testTaskID, stagePath, textHash(newText))
	if code != 0 {
		t.Fatalf("stagecheck of the right content exited %d, want 0", code)
	}

	wantOutput(t, out, "ok path="+stagePath+"\n")

	out, code = tree.patch(t, "stagecheck", testTaskID, stagePath, textHash(thirdText))
	if code != driftExit {
		t.Fatalf("stagecheck of the wrong content exited %d, want 10", code)
	}

	wantOutput(t, out, "drift path="+stagePath+"\n")

	missing := patchTask + "/s/1"
	if _, code := tree.patch(t, "stagecheck", testTaskID, missing, textHash(newText)); code != driftExit {
		t.Errorf("stagecheck of a missing file exited %d, want 10", code)
	}

	if _, code := tree.patch(t, "stagecheck", testTaskID, backupPath, textHash(newText)); code != refusedExit {
		t.Errorf("stagecheck of a backup path exited %d, want 13", code)
	}
}

func TestCheckReportsState(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)

	out, code := tree.check(t)
	if code != 0 {
		t.Fatalf("check exited %d, want 0", code)
	}

	wantOutput(t, out, "ok path="+pinnedTarget+" state=old backup=none\n")

	if _, code := tree.backup(t, oldText); code != 0 {
		t.Fatalf("backup exited %d, want 0", code)
	}

	out, _ = tree.check(t)
	wantOutput(t, out, "ok path="+pinnedTarget+" state=old backup=old\n")

	tree.write(t, pinnedTarget, newText)

	out, _ = tree.check(t)
	wantOutput(t, out, "ok path="+pinnedTarget+" state=new backup=old\n")

	tree.write(t, pinnedTarget, thirdText)

	out, code = tree.check(t)
	if code != driftExit {
		t.Errorf("check of a third hash exited %d, want 10", code)
	}

	wantOutput(t, out, "drift path="+pinnedTarget+"\n")

	tree.write(t, pinnedTarget, oldText)
	tree.write(t, backupPath, thirdText)

	out, code = tree.check(t)
	if code != backupExit {
		t.Errorf("check with a wrong backup exited %d, want 11", code)
	}

	wantOutput(t, out, "drift path="+backupPath+"\n")
}

func TestBackupIsImmutable(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)

	out, code := tree.backup(t, oldText)
	if code != 0 {
		t.Fatalf("backup exited %d, want 0", code)
	}

	wantOutput(t, out, "ok path="+pinnedTarget+" backup="+backupPath+" action=created\n")
	tree.assert(t, backupPath, oldText)
	tree.assertOwnerMode(t, backupPath, 0, 0o600)

	// The same backup again reports that it exists.
	out, code = tree.backup(t, oldText)
	if code != 0 {
		t.Fatalf("a repeated backup exited %d, want 0", code)
	}

	wantOutput(t, out, "ok path="+pinnedTarget+" backup="+backupPath+" action=exists\n")

	// A changed target backed up to the same path finds a backup with
	// another hash, and the first backup stays.
	tree.write(t, pinnedTarget, thirdText)

	if _, code := tree.backup(t, thirdText); code != backupExit {
		t.Fatalf("a second backup of a changed target exited %d, want 11", code)
	}

	tree.assert(t, backupPath, oldText)

	entries, err := os.ReadDir(tree.at(patchTask + "/b"))
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 1 {
		t.Errorf("the backup directory holds %d entries, want 1", len(entries))
	}
}

func TestBackupRefusesDrift(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)
	tree.write(t, pinnedTarget, thirdText)

	out, code := tree.backup(t, oldText)
	if code != driftExit {
		t.Fatalf("a backup of a drifted target exited %d, want 10", code)
	}

	wantOutput(t, out, "drift path="+pinnedTarget+"\n")

	entries, err := os.ReadDir(tree.at(patchTask + "/b"))
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 0 {
		t.Errorf("a refused backup left %d entries", len(entries))
	}
}

func TestReplaceKeepsOwnerAndMode(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)
	tree.stage(t, newText)

	out, code := tree.replaceTarget(t)
	if code != 0 {
		t.Fatalf("replace exited %d, want 0", code)
	}

	wantOutput(t, out, "ok path="+pinnedTarget+" state=new action=replaced\n")
	tree.assert(t, pinnedTarget, newText)
	tree.assertOwnerMode(t, pinnedTarget, vcapUID, 0o640)
	tree.assertOnlyTarget(t)
}

func TestReplaceIsIdempotent(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)
	tree.write(t, pinnedTarget, newText)
	tree.stage(t, newText)

	before, err := os.Stat(tree.at(pinnedTarget))
	if err != nil {
		t.Fatal(err)
	}

	out, code := tree.replaceTarget(t)
	if code != 0 {
		t.Fatalf("replace of a target already new exited %d, want 0", code)
	}

	wantOutput(t, out, "ok path="+pinnedTarget+" state=new action=unchanged\n")

	after, err := os.Stat(tree.at(pinnedTarget))
	if err != nil {
		t.Fatal(err)
	}

	if !os.SameFile(before, after) {
		t.Error("replace swapped a target that was already new")
	}

	tree.assertOnlyTarget(t)
}

func TestReplaceRefusesDrift(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)
	tree.write(t, pinnedTarget, thirdText)
	tree.stage(t, newText)

	out, code := tree.replaceTarget(t)
	if code != driftExit {
		t.Fatalf("replace of a drifted target exited %d, want 10", code)
	}

	wantOutput(t, out, "drift path="+pinnedTarget+"\n")
	tree.assert(t, pinnedTarget, thirdText)
	tree.assertOnlyTarget(t)

	// A staged file with the wrong content is drift too, and the target
	// keeps its old content.
	tree.write(t, pinnedTarget, oldText)
	tree.replace(t, stagePath)
	tree.stage(t, thirdText)

	out, code = tree.replaceTarget(t)
	if code != driftExit {
		t.Fatalf("replace from a wrong staged file exited %d, want 10", code)
	}

	wantOutput(t, out, "drift path="+stagePath+"\n")
	tree.assert(t, pinnedTarget, oldText)
	tree.assertOnlyTarget(t)
}

func TestRestoreFromBackup(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)
	tree.stage(t, newText)

	if _, code := tree.backup(t, oldText); code != 0 {
		t.Fatalf("backup exited %d, want 0", code)
	}

	if _, code := tree.replaceTarget(t); code != 0 {
		t.Fatalf("replace exited %d, want 0", code)
	}

	out, code := tree.restore(t)
	if code != 0 {
		t.Fatalf("restore exited %d, want 0", code)
	}

	wantOutput(t, out, "ok path="+pinnedTarget+" state=old action=restored\n")
	tree.assert(t, pinnedTarget, oldText)
	tree.assertOwnerMode(t, pinnedTarget, vcapUID, 0o640)
	tree.assert(t, backupPath, oldText)
	tree.assertOnlyTarget(t)
}

func TestRestoreIsIdempotent(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)

	if _, code := tree.backup(t, oldText); code != 0 {
		t.Fatalf("backup exited %d, want 0", code)
	}

	out, code := tree.restore(t)
	if code != 0 {
		t.Fatalf("restore of a target already old exited %d, want 0", code)
	}

	wantOutput(t, out, "ok path="+pinnedTarget+" state=old action=unchanged\n")
	tree.assert(t, pinnedTarget, oldText)
	tree.assert(t, backupPath, oldText)

	// A third hash is not this task's to undo.
	tree.write(t, pinnedTarget, thirdText)

	out, code = tree.restore(t)
	if code != driftExit {
		t.Fatalf("restore of a third hash exited %d, want 10", code)
	}

	wantOutput(t, out, "unrecovered path="+pinnedTarget+"\n")
	tree.assert(t, pinnedTarget, thirdText)
}

func TestRestoreRefusesBadBackup(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)
	tree.write(t, pinnedTarget, newText)

	out, code := tree.restore(t)
	if code != backupExit {
		t.Fatalf("restore with no backup exited %d, want 11", code)
	}

	wantOutput(t, out, "unrecovered path="+pinnedTarget+"\n")

	tree.write(t, backupPath, thirdText)

	if _, code := tree.restore(t); code != backupExit {
		t.Fatalf("restore from a wrong backup exited %d, want 11", code)
	}

	tree.assert(t, pinnedTarget, newText)
	tree.assertOnlyTarget(t)
}

func TestCleanupRemovesTaskDir(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)
	tree.stage(t, newText)

	if _, code := tree.backup(t, oldText); code != 0 {
		t.Fatalf("backup exited %d, want 0", code)
	}

	out, code := tree.patch(t, "cleanup", testTaskID)
	if code != 0 {
		t.Fatalf("cleanup exited %d, want 0", code)
	}

	wantOutput(t, out, "ok path="+patchTask+"\n")

	if tree.exists(t, patchTask) {
		t.Error("cleanup left the task directory")
	}

	if !tree.exists(t, workBase) {
		t.Error("cleanup removed the shared work directory")
	}

	tree.assert(t, pinnedTarget, oldText)

	// A task directory that is already gone is cleaned up.
	if _, code := tree.patch(t, "cleanup", testTaskID); code != 0 {
		t.Errorf("a repeated cleanup exited %d, want 0", code)
	}
}

func TestCleanupRefusesOtherPaths(t *testing.T) {
	t.Parallel()

	for _, task := range []string{"..", "*", "", strings.ToUpper(testTaskID), testTaskID + "/s"} {
		tree := newPatchTree(t)

		if _, code := tree.patch(t, "cleanup", task); code != refusedExit {
			t.Errorf("cleanup of task %q exited %d, want 13", task, code)
		}

		if !tree.exists(t, patchTask+"/s") {
			t.Errorf("cleanup of task %q removed the task directory", task)
		}
	}

	t.Run("a symlinked task directory", func(t *testing.T) {
		t.Parallel()

		tree := newPatchTree(t)
		tree.mkdir(t, "/etc/planted")
		tree.write(t, "/etc/planted/keep", "keep\n")
		tree.replace(t, patchTask)
		symlink(t, tree.at("/etc/planted"), tree.at(patchTask))

		if _, code := tree.patch(t, "cleanup", testTaskID); code != refusedExit {
			t.Errorf("cleanup of a symlinked task directory exited %d, want 13", code)
		}

		tree.assert(t, "/etc/planted/keep", "keep\n")
	})

	t.Run("a task directory vcap owns", func(t *testing.T) {
		t.Parallel()

		tree := newPatchTree(t)
		tree.giveToVCAP(t, patchTask)

		if _, code := tree.patch(t, "cleanup", testTaskID); code != refusedExit {
			t.Errorf("cleanup of a vcap task directory exited %d, want 13", code)
		}

		if !tree.exists(t, patchTask+"/s") {
			t.Error("a refused cleanup removed the task directory")
		}
	})
}

// fakeMonit is a monit that logs each restart and prints a summary and a
// status in the monit 5.2.5 forms, with the valkey process in the given
// state. valkey starts with pid 100 and valkey-exporter with pid 200.
// When monit.renew exists, a restart gives the process a new pid, and
// without it a restart leaves the old process in place.
const fakeMonit = `#!/bin/sh
dir=${0%/*}

pid() {
	n=$2
	if grep -qx "$1" "$dir/monit.renewed" 2> /dev/null; then
		n=$((n + 1))
	fi
	echo "$n"
}

case "$1" in
restart)
	echo "restart $2" >> "$dir/monit.log"
	if [ -f "$dir/monit.renew" ]; then
		echo "$2" >> "$dir/monit.renewed"
	fi
	;;
summary)
	echo "The Monit daemon 5.2.5 uptime: 3h 2m "
	printf '\n'
	echo "Process 'valkey'                    STATE"
	echo "Process 'valkey-exporter'           running"
	echo "System 'system_localhost'           running"
	;;
status)
	echo "The Monit daemon 5.2.5 uptime: 3h 2m "
	printf '\n'
	echo "Process 'valkey'"
	echo "  status                            STATE"
	echo "  monitoring status                 monitored"
	echo "  pid                               $(pid valkey 100)"
	echo "  parent pid                        1"
	echo "  uptime                            3h 2m "
	printf '\n'
	echo "Process 'valkey-exporter'"
	echo "  status                            running"
	echo "  monitoring status                 monitored"
	echo "  pid                               $(pid valkey-exporter 200)"
	echo "  parent pid                        1"
	echo "  uptime                            3h 2m "
	printf '\n'
	echo "System 'system_localhost'"
	echo "  status                            running"
	;;
*)
	exit 1
	;;
esac
`

// installMonit puts a fake monit where BOSH installs the real one. renew
// says whether a restart starts a new process.
func (v *vmTree) installMonit(t *testing.T, state string, renew bool) {
	t.Helper()

	v.mkdir(t, "/var/vcap/bosh/bin")
	v.writeMode(t, monitPath, strings.ReplaceAll(fakeMonit, "STATE", state), 0o755)

	if renew {
		v.write(t, "/var/vcap/bosh/bin/monit.renew", "")
	}
}

func TestRestartWaitsForRunning(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)
	tree.installMonit(t, "running", true)

	out, code := tree.patch(t, "restart", testTaskID, "valkey", "valkey-exporter")
	if code != 0 {
		t.Fatalf("restart exited %d, want 0", code)
	}

	wantOutput(t, out, "ok name=valkey\nok name=valkey-exporter\n")
	tree.assert(t, "/var/vcap/bosh/bin/monit.log", "restart valkey\nrestart valkey-exporter\n")
}

func TestRestartTimeout(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)
	tree.installMonit(t, "not monitored", true)

	started := time.Now()

	out, code := runScript(t, tree.root, []string{"CERTPATCH_RESTART_TIMEOUT=3"}, "patch", "restart", testTaskID, "valkey", "valkey-exporter")
	if code != restartExit {
		t.Fatalf("restart of a process that never runs exited %d, want 12", code)
	}

	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Errorf("restart took %s with a 3 s timeout", elapsed)
	}

	wantOutput(t, out, "drift name=valkey\n")
}

// TestRestartNeedsNewProcess shows that a restart monit accepted but never
// carried out fails, because each process still runs with the pid it had
// before.
func TestRestartNeedsNewProcess(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)
	tree.installMonit(t, "running", false)

	out, code := runScript(t, tree.root, []string{"CERTPATCH_RESTART_TIMEOUT=3"}, "patch", "restart", testTaskID, "valkey", "valkey-exporter")
	if code != restartExit {
		t.Fatalf("restart that kept the old processes exited %d, want 12", code)
	}

	wantOutput(t, out, "drift name=valkey\ndrift name=valkey-exporter\n")
}

func TestPatchRefusesBadArguments(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)
	tree.stage(t, newText)

	old, fresh := textHash(oldText), textHash(newText)

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"an unknown operation", []string{"remove", testTaskID}, usageExit},
		{"no operation", nil, usageExit},
		{"prepare with no task", []string{"prepare"}, usageExit},
		{"prepare with a bad task", []string{"prepare", "../x"}, refusedExit},
		{"stage with no path", []string{"stage", testTaskID}, usageExit},
		{"stage to a backup path", []string{"stage", testTaskID, backupPath}, refusedExit},
		{"stage to another task", []string{"stage", testTaskID, workBase + otherTaskID + "/s/0"}, refusedExit},
		{"check with too few arguments", []string{opCheck, testTaskID, pinnedJobDir, pinnedTarget, old, fresh}, usageExit},
		{"check with a bad hash", []string{opCheck, testTaskID, pinnedJobDir, pinnedTarget, "abc", fresh, backupPath}, usageExit},
		{"check with equal hashes", []string{opCheck, testTaskID, pinnedJobDir, pinnedTarget, old, old, backupPath}, usageExit},
		{"check with a staging path as backup", []string{opCheck, testTaskID, pinnedJobDir, pinnedTarget, old, fresh, stagePath}, refusedExit},
		{"check of another task's backup", []string{opCheck, testTaskID, pinnedJobDir, pinnedTarget, old, fresh, workBase + otherTaskID + "/b/0"}, refusedExit},
		{"backup outside the job", []string{"backup", testTaskID, pinnedJobDir, "/etc/passwd", old, backupPath}, refusedExit},
		{"backup to a staging path", []string{"backup", testTaskID, pinnedJobDir, pinnedTarget, old, stagePath}, refusedExit},
		{"replace from a backup path", []string{"replace", testTaskID, pinnedJobDir, pinnedTarget, backupPath, old, fresh}, refusedExit},
		{"replace with equal hashes", []string{"replace", testTaskID, pinnedJobDir, pinnedTarget, stagePath, fresh, fresh}, usageExit},
		{"restore from a staging path", []string{"restore", testTaskID, pinnedJobDir, pinnedTarget, stagePath, old, fresh}, refusedExit},
		{"restart with no process", []string{"restart", testTaskID}, usageExit},
		{"restart of a bad name", []string{"restart", testTaskID, "valkey;reboot"}, usageExit},
		{"cleanup with two tasks", []string{"cleanup", testTaskID, otherTaskID}, usageExit},
	}

	for _, test := range cases {
		var code int
		if len(test.args) == 0 {
			_, code = tree.patch(t, "")
		} else {
			_, code = tree.patch(t, test.args[0], test.args[1:]...)
		}

		if code != test.want {
			t.Errorf("%s exited %d, want %d", test.name, code, test.want)
		}
	}

	tree.assert(t, pinnedTarget, oldText)
	tree.assertOnlyTarget(t)

	if tree.exists(t, backupPath) {
		t.Error("a refused call wrote a backup")
	}
}
