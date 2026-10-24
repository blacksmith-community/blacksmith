package certscripts_test

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"slices"
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
	workRoot    = "/var/vcap/data/certpatch"
	stagePath   = patchTask + "/s/0"
	backupPath  = patchTask + "/b/0"
	targetDir   = pinnedJobDir + "/config"
	monitPath   = "/var/vcap/bosh/bin/monit"
	opCheck     = "check"
	opStage     = "stage"
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

// newFreshTree builds the tree newPatchTree does without running
// prepare, so /var/vcap/data/certpatch does not exist, as on a VM that
// no patch task has touched.
func newFreshTree(t *testing.T) *vmTree {
	t.Helper()

	tree := newPinnedTree(t, pinnedTarget, pinnedJobDir)
	tree.giveToVCAP(t, pinnedTarget)
	tree.chmod(t, pinnedTarget, 0o640)

	return tree
}

// checkRaw runs check with the raw hashes of oldText and newText, as the
// broker sends it for a record with raw hashes.
func (v *vmTree) checkRaw(t *testing.T) (string, int) {
	t.Helper()

	return v.patch(t, opCheck, testTaskID, pinnedJobDir, pinnedTarget, textHash(oldText), textHash(newText), backupPath,
		certpatch.RawHash([]byte(oldText)), certpatch.RawHash([]byte(newText)))
}

// TestCheckOnFreshVM runs a first patch on a VM that no task has
// touched, in the order the broker sends it. check comes before prepare
// has made the work directory, so it must read the target, report no
// backup, and create nothing. prepare, staging, the backup, and the
// replace then run as they do on any VM, and check afterwards sees the
// new content and the backup.
func TestCheckOnFreshVM(t *testing.T) {
	t.Parallel()

	tree := newFreshTree(t)

	for name, check := range map[string]func(*testing.T) (string, int){"normalised": tree.check, "raw": tree.checkRaw} {
		out, code := check(t)
		if code != 0 {
			t.Fatalf("%s check before prepare exited %d, want 0", name, code)
		}

		wantOutput(t, out, "ok path="+pinnedTarget+" state=old backup=none\n")
	}

	if tree.exists(t, workBase) {
		t.Fatal("check created the work directory")
	}

	if _, code := tree.patch(t, "prepare", testTaskID); code != 0 {
		t.Fatalf("prepare exited %d, want 0", code)
	}

	if code := runStage(t, tree.root, stagePath, newText); code != 0 {
		t.Fatalf("staging exited %d, want 0", code)
	}

	if _, code := tree.patch(t, "stagecheck", testTaskID, stagePath, textHash(newText)); code != 0 {
		t.Fatalf("stagecheck exited %d, want 0", code)
	}

	out, code := tree.checkRaw(t)
	if code != 0 {
		t.Fatalf("check after prepare exited %d, want 0", code)
	}

	wantOutput(t, out, "ok path="+pinnedTarget+" state=old backup=none\n")

	if _, code := tree.backup(t, oldText); code != 0 {
		t.Fatalf("backup exited %d, want 0", code)
	}

	if _, code := tree.replaceTarget(t); code != 0 {
		t.Fatalf("replace exited %d, want 0", code)
	}

	tree.assert(t, pinnedTarget, newText)

	out, _ = tree.checkRaw(t)
	wantOutput(t, out, "ok path="+pinnedTarget+" state=new backup=old\n")
}

// privateDirs makes each directory with mode 0700, as prepare would.
func (v *vmTree) privateDirs(t *testing.T, dirs ...string) {
	t.Helper()

	for _, dir := range dirs {
		v.mkdir(t, dir)
		v.chmod(t, dir, 0o700)
	}
}

// plantedDir makes a private directory with a private b directory
// outside the work directory, and returns its full path.
func (v *vmTree) plantedDir(t *testing.T) string {
	t.Helper()
	v.privateDirs(t, "/etc/planted", "/etc/planted/b")

	return v.at("/etc/planted")
}

// TestCheckBeforePrepareRefusesPlantedDirs shows that check before
// prepare still holds every directory that exists to what valid_work
// requires. One that is open, another user's, or a symlink, or a
// /var/vcap/data others can write, makes check refuse with nothing
// printed and nothing created.
func TestCheckBeforePrepareRefusesPlantedDirs(t *testing.T) {
	t.Parallel()

	plants := map[string]func(t *testing.T, tree *vmTree){
		"an open /var/vcap/data": func(t *testing.T, tree *vmTree) {
			t.Helper()
			tree.chmod(t, vcapData, 0o777)
		},
		"a certpatch directory with mode 0755": func(t *testing.T, tree *vmTree) {
			t.Helper()
			tree.privateDirs(t, workRoot)
			tree.chmod(t, workRoot, 0o755)
		},
		"a certpatch directory owned by vcap": func(t *testing.T, tree *vmTree) {
			t.Helper()
			tree.privateDirs(t, workRoot)
			tree.giveToVCAP(t, workRoot)
		},
		"a certpatch symlink": func(t *testing.T, tree *vmTree) {
			t.Helper()
			symlink(t, tree.plantedDir(t), tree.at(workRoot))
		},
		"a dangling certpatch symlink": func(t *testing.T, tree *vmTree) {
			t.Helper()
			symlink(t, tree.at("/etc/nowhere"), tree.at(workRoot))
		},
		"an open task directory": func(t *testing.T, tree *vmTree) {
			t.Helper()
			tree.privateDirs(t, workRoot, patchTask)
			tree.chmod(t, patchTask, 0o755)
		},
		"a task directory symlink": func(t *testing.T, tree *vmTree) {
			t.Helper()
			tree.privateDirs(t, workRoot)
			symlink(t, tree.plantedDir(t), tree.at(patchTask))
		},
		"an open backup directory": func(t *testing.T, tree *vmTree) {
			t.Helper()
			tree.privateDirs(t, workRoot, patchTask, patchTask+"/b")
			tree.chmod(t, patchTask+"/b", 0o755)
		},
		"a backup directory owned by vcap": func(t *testing.T, tree *vmTree) {
			t.Helper()
			tree.privateDirs(t, workRoot, patchTask, patchTask+"/b")
			tree.giveToVCAP(t, patchTask+"/b")
		},
		"a backup that is a symlink": func(t *testing.T, tree *vmTree) {
			t.Helper()
			tree.privateDirs(t, workRoot, patchTask, patchTask+"/b")
			symlink(t, tree.at(pinnedTarget), tree.at(backupPath))
		},
	}

	for name, plant := range plants {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := newFreshTree(t)
			plant(t, tree)

			if out, code := tree.checkRaw(t); code != refusedExit || out != "" {
				t.Errorf("check exited %d with %q, want 13 and no output", code, out)
			}

			if tree.exists(t, patchTask+"/s") {
				t.Error("the refused check created the staging directory")
			}
		})
	}
}

// TestCheckBeforePrepareAcceptsPartialDirs shows that a part of the work
// directory that is not there yet means no backup can be there, so
// check reports none, and it creates none of the parts that are
// missing.
func TestCheckBeforePrepareAcceptsPartialDirs(t *testing.T) {
	t.Parallel()

	layouts := map[string][]string{
		"no certpatch directory":               nil,
		"only the certpatch directory":         {workRoot},
		"a task directory with no b directory": {workRoot, patchTask},
		"an empty b directory":                 {workRoot, patchTask, patchTask + "/b"},
	}

	for name, dirs := range layouts {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := newFreshTree(t)
			tree.privateDirs(t, dirs...)

			out, code := tree.checkRaw(t)
			if code != 0 {
				t.Fatalf("check exited %d, want 0", code)
			}

			wantOutput(t, out, "ok path="+pinnedTarget+" state=old backup=none\n")

			for _, dir := range []string{workRoot, patchTask, patchTask + "/b"} {
				if !slices.Contains(dirs, dir) && tree.exists(t, dir) {
					t.Errorf("check created %s", dir)
				}
			}
		})
	}
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

// TestCleanupRemovesTempFiles shows that cleanup removes the temporary
// files an interrupted replace or restore of this task left beside each
// target it names, and nothing else. A temporary file's name carries the
// task ID, so another task's file stays, and a symlink with the name of
// one is neither followed nor removed.
func TestCleanupRemovesTempFiles(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)
	tree.mkdir(t, "/etc")
	tree.write(t, "/etc/keep", "keep\n")

	ours := targetDir + "/.certpatch." + testTaskID + ".Ab3dE6gH9j"
	theirs := targetDir + "/.certpatch." + otherTaskID + ".Ab3dE6gH9j"
	link := targetDir + "/.certpatch." + testTaskID + ".link"

	tree.write(t, ours, newText)
	tree.write(t, theirs, newText)
	symlink(t, tree.at("/etc/keep"), tree.at(link))

	out, code := tree.patch(t, "cleanup", testTaskID, pinnedJobDir, pinnedTarget)
	if code != 0 {
		t.Fatalf("cleanup exited %d, want 0", code)
	}

	wantOutput(t, out, "ok path="+patchTask+"\n")

	if tree.exists(t, ours) {
		t.Error("cleanup left this task's temporary file")
	}

	if !tree.exists(t, theirs) {
		t.Error("cleanup removed another task's temporary file")
	}

	if !tree.exists(t, link) {
		t.Error("cleanup removed a symlink")
	}

	tree.assert(t, "/etc/keep", "keep\n")
	tree.assert(t, pinnedTarget, oldText)

	if tree.exists(t, patchTask) {
		t.Error("cleanup left the task directory")
	}

	// A target that fails its checks stops cleanup before it removes
	// anything.
	tree.write(t, ours, newText)

	if _, code := tree.patch(t, "cleanup", testTaskID, pinnedJobDir, "/etc/keep"); code != refusedExit {
		t.Errorf("cleanup beside a refused target exited %d, want 13", code)
	}

	if !tree.exists(t, ours) {
		t.Error("a refused cleanup removed a temporary file")
	}

	if _, code := tree.patch(t, "cleanup", testTaskID, pinnedJobDir); code != usageExit {
		t.Errorf("cleanup with a job directory and no path exited %d, want 2", code)
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
		{"stage with no path", []string{opStage, testTaskID}, usageExit},
		{"stage to a backup path", []string{opStage, testTaskID, backupPath}, refusedExit},
		{"stage to another task", []string{opStage, testTaskID, workBase + otherTaskID + "/s/0"}, refusedExit},
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

// padded is a patch tree whose target holds the old certificate as a job
// template renders it, with a blank line before the PEM and two after it,
// and whose staged file holds the bytes the broker builds to replace it:
// the target's lead and tail around the new PEM. The system's openssl is
// in the tree, so the checks that run it work.
type padded struct {
	*vmTree

	old, fresh pair
	// original is the target's bytes as discovery read them, and written
	// the bytes that replace them.
	original, written string
}

func newPadded(t *testing.T) *padded {
	t.Helper()

	tree := &padded{vmTree: newPatchTree(t), old: newPair(t, "old"), fresh: newPair(t, "new")}
	tree.original = "\n" + tree.old.cert + "\n\n"
	tree.written = "\n" + strings.TrimSpace(tree.fresh.cert) + "\n\n\n"

	tree.write(t, pinnedTarget, tree.original)
	tree.stage(t, tree.written)
	tree.withOpenSSL(t)

	return tree
}

// withOpenSSL links the system's openssl into the tree, where the scripts
// look for the stemcell's.
func (v *vmTree) withOpenSSL(t *testing.T) {
	t.Helper()

	openssl, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl is not installed")
	}

	v.mkdir(t, "/usr/bin")
	symlink(t, openssl, v.at("/usr/bin/openssl"))
}

func rawHash(text string) string { return certpatch.RawHash([]byte(text)) }

func (p *padded) oldRaw() string { return rawHash(p.original) }

func (p *padded) newRaw() string { return rawHash(p.written) }

func (p *padded) check(t *testing.T) (string, int) {
	t.Helper()

	return p.patch(t, opCheck, testTaskID, pinnedJobDir, pinnedTarget, p.old.certHash(), p.fresh.certHash(), backupPath,
		p.oldRaw(), p.newRaw())
}

func (p *padded) stagecheck(t *testing.T, newHash, newRaw, role string) (string, int) {
	t.Helper()

	return p.patch(t, "stagecheck", testTaskID, stagePath, newHash, newRaw, role)
}

func (p *padded) backup(t *testing.T) (string, int) {
	t.Helper()

	return p.patch(t, "backup", testTaskID, pinnedJobDir, pinnedTarget, p.old.certHash(), backupPath, p.oldRaw())
}

func (p *padded) swap(t *testing.T, newHash, newRaw string) (string, int) {
	t.Helper()

	return p.patch(t, "replace", testTaskID, pinnedJobDir, pinnedTarget, stagePath, p.old.certHash(), newHash,
		p.oldRaw(), newRaw, "certificate")
}

func (p *padded) restore(t *testing.T) (string, int) {
	t.Helper()

	return p.patch(t, "restore", testTaskID, pinnedJobDir, pinnedTarget, backupPath, p.old.certHash(), p.fresh.certHash(),
		p.oldRaw(), p.newRaw())
}

// mustRun fails the test unless the operation exited 0.
func mustRun(t *testing.T, name string, run func(*testing.T) (string, int)) string {
	t.Helper()

	out, code := run(t)
	if code != 0 {
		t.Fatalf("%s exited %d, want 0", name, code)
	}

	return out
}

// TestReplaceKeepsSurroundingWhitespace shows that a replacement writes
// exactly the bytes the broker staged, the target's own blank lines and
// trailing whitespace around the new PEM, which BOSH renders from the
// same template on the next deploy, and that a rollback puts the
// original bytes back.
func TestReplaceKeepsSurroundingWhitespace(t *testing.T) {
	t.Parallel()

	tree := newPadded(t)

	mustRun(t, "stagecheck", func(t *testing.T) (string, int) {
		t.Helper()

		return tree.stagecheck(t, tree.fresh.certHash(), tree.newRaw(), "certificate")
	})
	mustRun(t, "backup", tree.backup)
	tree.assert(t, backupPath, tree.original)

	out := mustRun(t, "replace", func(t *testing.T) (string, int) {
		t.Helper()

		return tree.swap(t, tree.fresh.certHash(), tree.newRaw())
	})
	wantOutput(t, out, "ok path="+pinnedTarget+" state=new action=replaced\n")
	tree.assert(t, pinnedTarget, tree.written)
	tree.assertOwnerMode(t, pinnedTarget, vcapUID, 0o640)
	tree.assertOnlyTarget(t)

	if got := libHash(t, tree.at(pinnedTarget)); got != tree.fresh.certHash() {
		t.Errorf("norm_hash of the written target = %s, want the new hash %s", got, tree.fresh.certHash())
	}

	out, _ = tree.check(t)
	wantOutput(t, out, "ok path="+pinnedTarget+" state=new backup=old\n")

	out = mustRun(t, "restore", tree.restore)
	wantOutput(t, out, "ok path="+pinnedTarget+" state=old action=restored\n")
	tree.assert(t, pinnedTarget, tree.original)
	tree.assertOnlyTarget(t)
}

// TestRawHashesCatchWhitespaceDrift shows that a target that gained only
// a blank line after discovery read it is drift, which a normalised hash
// alone would miss, so the patch never writes over bytes it did not see.
func TestRawHashesCatchWhitespaceDrift(t *testing.T) {
	t.Parallel()

	tree := newPadded(t)
	tree.original = tree.old.cert
	changed := "\n" + tree.old.cert
	tree.write(t, pinnedTarget, changed)

	if libHash(t, tree.at(pinnedTarget)) != tree.old.certHash() {
		t.Fatal("the changed target must keep the old normalised hash")
	}

	for name, run := range map[string]func(*testing.T) (string, int){
		opCheck:  tree.check,
		"backup": tree.backup,
		"replace": func(t *testing.T) (string, int) {
			t.Helper()

			return tree.swap(t, tree.fresh.certHash(), tree.newRaw())
		},
	} {
		out, code := run(t)
		if code != driftExit {
			t.Errorf("%s of a target that changed after discovery exited %d, want %d", name, code, driftExit)
		}

		wantOutput(t, out, "drift path="+pinnedTarget+"\n")
	}

	tree.assert(t, pinnedTarget, changed)
	tree.assertOnlyTarget(t)

	if tree.exists(t, backupPath) {
		t.Error("a target that drifted was backed up")
	}
}

// TestRawHashesNeedTheOldPEMToo shows that a raw hash alone never makes
// a target old. Discovery could read the raw bytes of a file that changed
// after it was matched, so the target must also hold the old PEM, or the
// patch would back up and replace a file that never held it.
func TestRawHashesNeedTheOldPEMToo(t *testing.T) {
	t.Parallel()

	tree := newPadded(t)
	other := newPair(t, "other")
	tree.original = "\n" + other.cert + "\n\n"
	tree.write(t, pinnedTarget, tree.original)

	for name, run := range map[string]func(*testing.T) (string, int){
		opCheck:  tree.check,
		"backup": tree.backup,
		"replace": func(t *testing.T) (string, int) {
			t.Helper()

			return tree.swap(t, tree.fresh.certHash(), tree.newRaw())
		},
	} {
		if _, code := run(t); code != driftExit {
			t.Errorf("%s of a target with the pinned bytes but another PEM exited %d, want %d", name, code, driftExit)
		}
	}

	tree.assert(t, pinnedTarget, tree.original)

	if tree.exists(t, backupPath) {
		t.Error("a target that does not hold the old PEM was backed up")
	}
}

// TestRawHashesRefuseStagedBytesThatDiffer shows that a staged file must
// hold exactly the bytes the broker recorded, not just the same PEM.
func TestRawHashesRefuseStagedBytesThatDiffer(t *testing.T) {
	t.Parallel()

	tree := newPadded(t)
	tree.replace(t, stagePath)
	tree.stage(t, tree.fresh.cert)

	out, code := tree.stagecheck(t, tree.fresh.certHash(), tree.newRaw(), "certificate")
	if code != driftExit {
		t.Errorf("stagecheck of other bytes exited %d, want %d", code, driftExit)
	}

	wantOutput(t, out, "drift path="+stagePath+"\n")

	out, code = tree.swap(t, tree.fresh.certHash(), tree.newRaw())
	if code != driftExit {
		t.Errorf("replace from other bytes exited %d, want %d", code, driftExit)
	}

	wantOutput(t, out, "drift path="+stagePath+"\n")
	tree.assert(t, pinnedTarget, tree.original)
	tree.assertOnlyTarget(t)
}

// TestRestoreRefusesTamperedBackup shows that a backup that changed by
// one blank line is refused before it is copied, and the target keeps the
// new content.
func TestRestoreRefusesTamperedBackup(t *testing.T) {
	t.Parallel()

	tree := newPadded(t)
	mustRun(t, "backup", tree.backup)
	mustRun(t, "replace", func(t *testing.T) (string, int) {
		t.Helper()

		return tree.swap(t, tree.fresh.certHash(), tree.newRaw())
	})

	tree.write(t, backupPath, "\n"+tree.original)

	out, code := tree.restore(t)
	if code != backupExit {
		t.Errorf("restore from a tampered backup exited %d, want %d", code, backupExit)
	}

	wantOutput(t, out, "unrecovered path="+pinnedTarget+"\n")
	tree.assert(t, pinnedTarget, tree.written)
	tree.assertOnlyTarget(t)

	out, code = tree.check(t)
	if code != backupExit {
		t.Errorf("check of a tampered backup exited %d, want %d", code, backupExit)
	}

	wantOutput(t, out, "drift path="+backupPath+"\n")

	if _, code := tree.backup(t); code != backupExit {
		t.Errorf("backup over a tampered backup exited %d, want %d", code, backupExit)
	}
}

// TestInvalidReplacementAbortsBeforeRename shows that a staged file that
// openssl cannot read in its role stops stagecheck, and stops replace
// before the rename, so the target keeps its bytes and no temporary file
// is left. Without the stemcell's openssl nothing is replaced either.
func TestInvalidReplacementAbortsBeforeRename(t *testing.T) {
	t.Parallel()

	const invalidExit = 14

	invalid := "\n-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n\n\n"

	t.Run("a certificate openssl cannot read", func(t *testing.T) {
		t.Parallel()

		tree := newPadded(t)
		tree.replace(t, stagePath)
		tree.stage(t, invalid)

		out, code := tree.stagecheck(t, textHash(invalid), rawHash(invalid), "certificate")
		if code != invalidExit {
			t.Errorf("stagecheck exited %d, want %d", code, invalidExit)
		}

		wantOutput(t, out, "drift path="+stagePath+"\n")

		out, code = tree.swap(t, textHash(invalid), rawHash(invalid))
		if code != invalidExit {
			t.Errorf("replace exited %d, want %d", code, invalidExit)
		}

		wantOutput(t, out, "drift path="+stagePath+"\n")
		tree.assert(t, pinnedTarget, tree.original)
		tree.assertOnlyTarget(t)
	})

	t.Run("a certificate checked as a key", func(t *testing.T) {
		t.Parallel()

		tree := newPadded(t)

		if _, code := tree.stagecheck(t, tree.fresh.certHash(), tree.newRaw(), "key"); code != invalidExit {
			t.Errorf("stagecheck of a certificate as a key exited %d, want %d", code, invalidExit)
		}

		tree.replace(t, stagePath)
		key := "\n" + strings.TrimSpace(tree.fresh.key) + "\n"
		tree.stage(t, key)

		if _, code := tree.stagecheck(t, tree.fresh.keyHash(), rawHash(key), "key"); code != 0 {
			t.Errorf("stagecheck of a key exited %d, want 0", code)
		}
	})
}

// TestReplacementNeedsOpenSSL shows that without the stemcell's openssl,
// or with a role it does not know, nothing is checked and nothing is
// replaced.
func TestReplacementNeedsOpenSSL(t *testing.T) {
	t.Parallel()

	t.Run("no openssl", func(t *testing.T) {
		t.Parallel()

		tree := newPadded(t)
		tree.replace(t, "/usr/bin/openssl")

		if _, code := tree.stagecheck(t, tree.fresh.certHash(), tree.newRaw(), "certificate"); code != 1 {
			t.Errorf("stagecheck without openssl exited %d, want 1", code)
		}

		if _, code := tree.swap(t, tree.fresh.certHash(), tree.newRaw()); code != 1 {
			t.Errorf("replace without openssl exited %d, want 1", code)
		}

		tree.assert(t, pinnedTarget, tree.original)
		tree.assertOnlyTarget(t)
	})

	t.Run("an unknown role", func(t *testing.T) {
		t.Parallel()

		tree := newPadded(t)

		if _, code := tree.stagecheck(t, tree.fresh.certHash(), tree.newRaw(), "ca"); code != usageExit {
			t.Errorf("stagecheck with an unknown role exited %d, want %d", code, usageExit)
		}

		if _, code := tree.patch(t, "replace", testTaskID, pinnedJobDir, pinnedTarget, stagePath, tree.old.certHash(),
			tree.fresh.certHash(), tree.oldRaw(), tree.newRaw(), "ca"); code != usageExit {
			t.Errorf("replace with an unknown role exited %d, want %d", code, usageExit)
		}

		tree.assert(t, pinnedTarget, tree.original)
	})
}

// TestResumeMidSwapOfPaddedTarget shows that a task that stopped during
// the swap of a padded target finds the target's state by its raw bytes,
// finishes the replacement, and can still roll back, whether it stopped
// before or after the rename.
func TestResumeMidSwapOfPaddedTarget(t *testing.T) {
	t.Parallel()

	leftover := targetDir + "/.certpatch." + testTaskID + ".Ab3dE6gH9j"
	replace := func(tree *padded) func(*testing.T) (string, int) {
		return func(t *testing.T) (string, int) {
			t.Helper()

			return tree.swap(t, tree.fresh.certHash(), tree.newRaw())
		}
	}

	t.Run("before the rename", func(t *testing.T) {
		t.Parallel()

		tree := newPadded(t)
		mustRun(t, "backup", tree.backup)

		// The swap wrote part of its temporary file and stopped.
		tree.write(t, leftover, "\n"+tree.fresh.cert[:20])

		out := mustRun(t, "check after the stop", tree.check)
		wantOutput(t, out, "ok path="+pinnedTarget+" state=old backup=old\n")
		tree.assert(t, pinnedTarget, tree.original)

		out = mustRun(t, "the resumed replace", replace(tree))
		wantOutput(t, out, "ok path="+pinnedTarget+" state=new action=replaced\n")
		tree.assert(t, pinnedTarget, tree.written)

		mustRun(t, "cleanup", func(t *testing.T) (string, int) {
			t.Helper()

			return tree.patch(t, "cleanup", testTaskID, pinnedJobDir, pinnedTarget)
		})

		if tree.exists(t, leftover) {
			t.Error("cleanup left the interrupted swap's temporary file")
		}
	})

	t.Run("after the rename", func(t *testing.T) {
		t.Parallel()

		tree := newPadded(t)
		mustRun(t, "backup", tree.backup)
		mustRun(t, "replace", replace(tree))

		// The journal never learned of the swap, so the task runs check
		// and replace again.
		out := mustRun(t, "check after the stop", tree.check)
		wantOutput(t, out, "ok path="+pinnedTarget+" state=new backup=old\n")

		out = mustRun(t, "the resumed replace", replace(tree))
		wantOutput(t, out, "ok path="+pinnedTarget+" state=new action=unchanged\n")
		tree.assert(t, pinnedTarget, tree.written)

		mustRun(t, "restore", tree.restore)
		tree.assert(t, pinnedTarget, tree.original)
	})
}

// TestResumeMidRestoreOfPaddedTarget shows that a rollback that stopped
// after its rename finds the original bytes back and runs on.
func TestResumeMidRestoreOfPaddedTarget(t *testing.T) {
	t.Parallel()

	t.Run("after the rename", func(t *testing.T) {
		t.Parallel()

		tree := newPadded(t)
		mustRun(t, "backup", tree.backup)
		mustRun(t, "replace", func(t *testing.T) (string, int) {
			t.Helper()

			return tree.swap(t, tree.fresh.certHash(), tree.newRaw())
		})
		mustRun(t, "restore", tree.restore)

		// The restore renamed the original back and stopped, so the
		// rollback runs it again.
		out := mustRun(t, "the resumed restore", tree.restore)
		wantOutput(t, out, "ok path="+pinnedTarget+" state=old action=unchanged\n")
		tree.assert(t, pinnedTarget, tree.original)

		out = mustRun(t, "check", tree.check)
		wantOutput(t, out, "ok path="+pinnedTarget+" state=old backup=old\n")
	})
}
