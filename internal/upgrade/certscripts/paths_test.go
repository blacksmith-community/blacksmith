package certscripts_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/upgrade/certscripts"
)

const (
	vcapData     = "/var/vcap/data"
	valkeyJobs   = vcapData + "/jobs/valkey"
	pinnedJobDir = valkeyJobs + "/abc"
	pinnedTarget = pinnedJobDir + "/config/server.crt"
	otherTaskID  = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	workBase     = "/var/vcap/data/certpatch/"
	refusedExit  = 13
	vcapUID      = 1000
)

// pathCase is one validator input and whether the validator accepts it.
type pathCase struct {
	path string
	ok   bool
}

func logicalCases() []pathCase {
	return []pathCase{
		{"/var/vcap/jobs/valkey/config/tls/server.crt", true},
		{"/var/vcap/jobs/valkey-1.2_x/config/server.key", true},
		{"/var/vcap/jobs/../etc/shadow", false},
		{"/var/vcap/jobs/valkey/../../x", false},
		{"/var/vcap/jobs/valkey/./x", false},
		{"/var/vcap/jobs/valkey/x/..", false},
		{"/var/vcap/jobs/valkey/x/.", false},
		{"/var/vcap/jobs/./x", false},
		{"/var/vcap/jobs/valkey//x", false},
		{"/var/vcap/jobs/valkey/x/", false},
		{"/var/vcap/jobs/valkey", false},
		{"/var/vcap/jobs/valkey/", false},
		{"/etc/passwd", false},
		{"/var/vcap/jobs/v alkey/x", false},
		{"/var/vcap/jobs/valkey/x;rm", false},
		{"/var/vcap/jobs/valkey/x\n", false},
		{"/var/vcap/jobs/valkey/$(id)", false},
		{"/var/vcap/jobs/valkey/x'y", false},
		{"/var/vcap/data/jobs/valkey/abc/x", false},
		{"var/vcap/jobs/valkey/x", false},
		{"//var/vcap/jobs/valkey/x", false},
		{"", false},
		{"/var/vcap/jobs/valkey/" + strings.Repeat("a", 1100), false},
	}
}

func TestValidLogicalJobPath(t *testing.T) {
	t.Parallel()

	for _, tc := range logicalCases() {
		err := certscripts.ValidLogicalJobPath(tc.path)
		checkVerdict(t, "ValidLogicalJobPath", tc.path, tc.ok, err)
	}
}

// pinnedCase is one ValidPinnedTarget input.
type pinnedCase struct {
	path   string
	jobDir string
	ok     bool
}

func pinnedCases() []pinnedCase {
	return []pinnedCase{
		{pinnedTarget, pinnedJobDir, true},
		{pinnedJobDir + "/server.crt", pinnedJobDir, true},
		{"/var/vcap/data/jobs/valkey/def/config/server.crt", pinnedJobDir, false},
		{"/var/vcap/data/jobs/valkey/abcd/config/server.crt", pinnedJobDir, false},
		{"/var/vcap/data/jobs/valkey/abc/../x", pinnedJobDir, false},
		{"/var/vcap/data/jobs/valkey/abc/./x", pinnedJobDir, false},
		{"/var/vcap/data/jobs/valkey/abc//x", pinnedJobDir, false},
		{"/var/vcap/data/jobs/valkey/abc/x/", pinnedJobDir, false},
		{"/var/vcap/jobs/valkey/x", pinnedJobDir, false},
		{pinnedJobDir, pinnedJobDir, false},
		{pinnedJobDir + "/", pinnedJobDir, false},
		{"/var/vcap/data/jobs/valkey/abc/x;y", pinnedJobDir, false},
		{"/var/vcap/data/jobs/valkey/abc/x y", pinnedJobDir, false},
		{"/etc/shadow", pinnedJobDir, false},
		{pinnedTarget, "", false},
		{pinnedTarget, valkeyJobs, false},
		{pinnedTarget, "/var/vcap/data/jobs/valkey/abc/", false},
		{"/var/vcap/data/jobs/valkey/abc/config/x", "/var/vcap/data/jobs/valkey/abc/config", false},
		{"/var/vcap/data/jobs/../abc/x", "/var/vcap/data/jobs/../abc", false},
		{"/var/vcap/data/jobs/valkey/../x", "/var/vcap/data/jobs/valkey/..", false},
		{"/var/vcap/jobs/valkey/abc/x", "/var/vcap/jobs/valkey/abc", false},
		{"/etc/ssl/x", "/etc/ssl", false},
	}
}

func TestValidPinnedTarget(t *testing.T) {
	t.Parallel()

	for _, tc := range pinnedCases() {
		err := certscripts.ValidPinnedTarget(tc.path, tc.jobDir)
		checkVerdict(t, "ValidPinnedTarget", tc.path+" in "+tc.jobDir, tc.ok, err)
	}
}

// workCase is one ValidWorkPath input.
type workCase struct {
	path string
	task string
	ok   bool
}

func workCases() []workCase {
	own := workBase + testTaskID

	return []workCase{
		{own + "/s/0", testTaskID, true},
		{own + "/b/3", testTaskID, true},
		{own + "/s/12", testTaskID, true},
		{workBase + otherTaskID + "/s/0", testTaskID, false},
		{own + "/s/x", testTaskID, false},
		{own + "/s/-1", testTaskID, false},
		{own + "/s/01", testTaskID, false},
		{own + "/s/", testTaskID, false},
		{own + "/s", testTaskID, false},
		{own + "/c/0", testTaskID, false},
		{own + "/s/0/extra", testTaskID, false},
		{own + "/s/../b/0", testTaskID, false},
		{own + "/../" + otherTaskID + "/s/0", testTaskID, false},
		{own + "//s/0", testTaskID, false},
		{own + "/s/0\n", testTaskID, false},
		{workBase + "not-a-uuid/s/0", "not-a-uuid", false},
		{workBase + "../s/0", "..", false},
		{workBase + strings.ToUpper(testTaskID) + "/s/0", strings.ToUpper(testTaskID), false},
		{"/tmp/" + testTaskID + "/s/0", testTaskID, false},
		{"/var/vcap/data/jobs/valkey/abc/s/0", testTaskID, false},
	}
}

func TestValidWorkPath(t *testing.T) {
	t.Parallel()

	for _, tc := range workCases() {
		err := certscripts.ValidWorkPath(tc.path, tc.task)
		checkVerdict(t, "ValidWorkPath", tc.path+" for "+tc.task, tc.ok, err)
	}
}

func taskCases() []pathCase {
	return []pathCase{
		{testTaskID, true},
		{otherTaskID, true},
		{"00000000-0000-0000-0000-000000000000", true},
		{strings.ToUpper(testTaskID), false},
		{"{" + testTaskID + "}", false},
		{strings.ReplaceAll(testTaskID, "-", ""), false},
		{"urn:uuid:" + testTaskID, false},
		{testTaskID + "\n", false},
		{testTaskID[:35], false},
		{testTaskID + "0", false},
		{"0f1e2d3c-4b5a-4968-8776-65544332211g", false},
		{"../" + testTaskID[3:], false},
		{"", false},
	}
}

func TestValidTaskID(t *testing.T) {
	t.Parallel()

	for _, tc := range taskCases() {
		err := certscripts.ValidTaskID(tc.path)
		checkVerdict(t, "ValidTaskID", tc.path, tc.ok, err)
	}
}

// checkVerdict compares a validator's answer with the expected one. A
// refusal must wrap ErrPathRefused and must not repeat the input.
func checkVerdict(t *testing.T, name, input string, ok bool, err error) {
	t.Helper()

	if ok {
		if err != nil {
			t.Errorf("%s(%q) refused a valid input: %v", name, input, err)
		}

		return
	}

	if err == nil {
		t.Errorf("%s(%q) accepted an input it must refuse", name, input)

		return
	}

	if !errors.Is(err, certscripts.ErrPathRefused) {
		t.Errorf("%s(%q) error %v is not ErrPathRefused", name, input, err)
	}

	if input != "" && strings.Contains(err.Error(), input) {
		t.Errorf("%s(%q) error repeats its input: %v", name, input, err)
	}
}

// libCheck runs one lib.sh function with args and returns its exit code.
func libCheck(t *testing.T, root string, function string, args ...string) int {
	t.Helper()

	return runLib(t, root, "", function+` "$@"`, args...)
}

// runLib runs body after lib.sh with args as $1 onward, stdin as its
// input, and CERTPATCH_ROOT set to root, and returns the exit code.
func runLib(t *testing.T, root, stdin, body string, args ...string) int {
	t.Helper()

	argv := append([]string{"-c", "source lib.sh\n" + body, "_"}, args...)

	cmd := exec.CommandContext(t.Context(), "bash", argv...) //nolint:gosec // fixed test program text
	cmd.Env = []string{"CERTPATCH_ROOT=" + root}
	cmd.Stdin = strings.NewReader(stdin)

	err := cmd.Run()
	if err == nil {
		return 0
	}

	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("running lib.sh: %v", err)
	}

	return exit.ExitCode()
}

// shapeExit is the exit code a refused or accepted input should give in
// the shell when the filesystem plays no part.
func shapeExit(ok bool) int {
	if ok {
		return 0
	}

	return refusedExit
}

func TestValidatorShapeParityWithBash(t *testing.T) {
	t.Parallel()
	requireLinux(t)

	root := t.TempDir()

	for _, tc := range logicalCases() {
		if got := libCheck(t, root, "valid_logical", tc.path); got != shapeExit(tc.ok) {
			t.Errorf("valid_logical %q exited %d, want %d", tc.path, got, shapeExit(tc.ok))
		}
	}

	for _, tc := range taskCases() {
		if got := libCheck(t, root, "valid_task", tc.path); got != shapeExit(tc.ok) {
			t.Errorf("valid_task %q exited %d, want %d", tc.path, got, shapeExit(tc.ok))
		}
	}

	// The shell adds checks of the VM's files to these two, so an input
	// the Go side refuses must be refused there too, while an accepted
	// one depends on the files and is tested below.
	for _, tc := range pinnedCases() {
		if !tc.ok && libCheck(t, root, "valid_pinned", tc.path, tc.jobDir) != refusedExit {
			t.Errorf("valid_pinned %q %q did not refuse", tc.path, tc.jobDir)
		}
	}

	for _, tc := range workCases() {
		if !tc.ok && libCheck(t, root, "valid_work", tc.path, tc.task) != refusedExit {
			t.Errorf("valid_work %q %q did not refuse", tc.path, tc.task)
		}
	}

	for _, args := range [][]string{{}, {pinnedTarget}, {pinnedTarget, pinnedJobDir, "x"}} {
		if libCheck(t, root, "valid_pinned", args...) != refusedExit {
			t.Errorf("valid_pinned with %d arguments did not refuse", len(args))
		}
	}
}

// vmTree is a fake VM filesystem under a temporary root, laid out the way
// BOSH lays out a job.
type vmTree struct {
	root string
}

func newVMTree(t *testing.T) *vmTree {
	t.Helper()
	requireRoot(t)

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	tree := &vmTree{root: root}
	tree.mkdir(t, "/var/vcap/jobs")
	tree.mkdir(t, pinnedJobDir+"/config")
	tree.mkdir(t, "/var/vcap/data/jobs/valkey/def/config")
	tree.mkdir(t, "/etc")
	tree.write(t, pinnedTarget, "old\n")
	tree.write(t, "/var/vcap/data/jobs/valkey/def/config/server.crt", "def\n")
	tree.write(t, "/etc/shadow-fixture", "shadow\n")
	symlink(t, tree.at(pinnedJobDir), tree.at("/var/vcap/jobs/valkey"))

	return tree
}

// requireRoot skips a test that sets ownership, which only root can do.
func requireRoot(t *testing.T) {
	t.Helper()
	requireLinux(t)

	if os.Geteuid() != 0 {
		t.Skip("this test sets file ownership, so it must run as root")
	}
}

func (v *vmTree) at(path string) string {
	return v.root + path
}

func (v *vmTree) mkdir(t *testing.T, path string) {
	t.Helper()

	err := os.MkdirAll(v.at(path), 0o755) //nolint:gosec // a test tree
	if err != nil {
		t.Fatal(err)
	}
}

func (v *vmTree) write(t *testing.T, path, content string) {
	t.Helper()
	writeFile(t, v.at(path), content)
}

// giveToVCAP hands path to the vcap user, as a compromised job could.
func (v *vmTree) giveToVCAP(t *testing.T, path string) {
	t.Helper()

	err := os.Lchown(v.at(path), vcapUID, vcapUID)
	if err != nil {
		t.Fatal(err)
	}
}

func (v *vmTree) chmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()

	err := os.Chmod(v.at(path), mode)
	if err != nil {
		t.Fatal(err)
	}
}

func (v *vmTree) replace(t *testing.T, path string) {
	t.Helper()

	err := os.RemoveAll(v.at(path))
	if err != nil {
		t.Fatal(err)
	}
}

func (v *vmTree) assert(t *testing.T, path, want string) {
	t.Helper()
	assertContent(t, v.at(path), want)
}

// The guarded writes stand in for a script step. Each runs the validator
// first and writes stdin to the path only when it passes, so a refusal
// shows as exit 13 with the file untouched.
const (
	guardedPinnedWrite = `valid_pinned "$1" "$2" || exit; cat > "$CERTPATCH_ROOT$1"`
	guardedWorkWrite   = `valid_work "$1" "$2" || exit; set -C; cat > "$CERTPATCH_ROOT$1"`
)

// writePinned runs the guarded write of path in pinnedJobDir.
func (v *vmTree) writePinned(t *testing.T, path string) int {
	t.Helper()

	return runLib(t, v.root, "new\n", guardedPinnedWrite, path, pinnedJobDir)
}

// writeWork runs the guarded write of the work path for testTaskID.
func (v *vmTree) writeWork(t *testing.T, path string) int {
	t.Helper()

	return runLib(t, v.root, "new\n", guardedWorkWrite, path, testTaskID)
}

func (v *vmTree) makeWorkDir(t *testing.T, task string) int {
	t.Helper()

	return libCheck(t, v.root, "make_work_dir", task)
}

func TestOnVMAcceptsPinnedTarget(t *testing.T) {
	t.Parallel()

	tree := newVMTree(t)

	if got := tree.writePinned(t, pinnedTarget); got != 0 {
		t.Fatalf("a valid pinned write exited %d, want 0", got)
	}

	tree.assert(t, pinnedTarget, "new\n")
}

func TestOnVMRefusesSymlinkedTarget(t *testing.T) {
	t.Parallel()

	tree := newVMTree(t)
	tree.replace(t, pinnedTarget)
	symlink(t, tree.at("/etc/shadow-fixture"), tree.at(pinnedTarget))

	if got := tree.writePinned(t, pinnedTarget); got != refusedExit {
		t.Errorf("a write through a symlinked target exited %d, want 13", got)
	}

	tree.assert(t, "/etc/shadow-fixture", "shadow\n")

	// A symlinked directory between the job directory and the file is
	// refused the same way.
	tree = newVMTree(t)
	tree.mkdir(t, "/etc/config")
	tree.write(t, "/etc/config/server.crt", "shadow\n")
	tree.replace(t, pinnedJobDir+"/config")
	symlink(t, tree.at("/etc/config"), tree.at(pinnedJobDir+"/config"))

	if got := tree.writePinned(t, pinnedTarget); got != refusedExit {
		t.Errorf("a write through a symlinked directory exited %d, want 13", got)
	}

	tree.assert(t, "/etc/config/server.crt", "shadow\n")

	// A target that is not a regular file is refused.
	tree = newVMTree(t)
	tree.replace(t, pinnedTarget)
	tree.mkdir(t, pinnedTarget)

	if got := tree.writePinned(t, pinnedTarget); got != refusedExit {
		t.Errorf("a write to a directory exited %d, want 13", got)
	}

	// A target that is missing is refused.
	tree = newVMTree(t)
	tree.replace(t, pinnedTarget)

	if got := tree.writePinned(t, pinnedTarget); got != refusedExit {
		t.Errorf("a write to a missing target exited %d, want 13", got)
	}

	_, err := os.Lstat(tree.at(pinnedTarget))
	if err == nil {
		t.Error("the refused write created the target")
	}
}

func TestOnVMRefusesChangedJobSymlink(t *testing.T) {
	t.Parallel()

	tree := newVMTree(t)
	tree.replace(t, "/var/vcap/jobs/valkey")
	symlink(t, tree.at("/var/vcap/data/jobs/valkey/def"), tree.at("/var/vcap/jobs/valkey"))

	if got := tree.writePinned(t, pinnedTarget); got != refusedExit {
		t.Errorf("a write after the job symlink changed exited %d, want 13", got)
	}

	tree.assert(t, pinnedTarget, "old\n")
	tree.assert(t, "/var/vcap/data/jobs/valkey/def/config/server.crt", "def\n")

	// A job link that is a real directory, or missing, is refused too.
	tree = newVMTree(t)
	tree.replace(t, "/var/vcap/jobs/valkey")
	tree.mkdir(t, "/var/vcap/jobs/valkey")

	if got := tree.writePinned(t, pinnedTarget); got != refusedExit {
		t.Errorf("a write with no job symlink exited %d, want 13", got)
	}

	tree.assert(t, pinnedTarget, "old\n")
}

func TestOnVMRefusesNonRootAncestor(t *testing.T) {
	t.Parallel()

	for _, dir := range []string{pinnedJobDir + "/config", pinnedJobDir} {
		tree := newVMTree(t)
		tree.giveToVCAP(t, dir)

		if got := tree.writePinned(t, pinnedTarget); got != refusedExit {
			t.Errorf("with %s owned by vcap the write exited %d, want 13", dir, got)
		}

		tree.assert(t, pinnedTarget, "old\n")
	}
}

func TestOnVMMakesPrivateWorkDir(t *testing.T) {
	t.Parallel()

	tree := newVMTree(t)

	if got := tree.makeWorkDir(t, testTaskID); got != 0 {
		t.Fatalf("make_work_dir exited %d, want 0", got)
	}

	for _, dir := range []string{"", testTaskID, testTaskID + "/s", testTaskID + "/b"} {
		info, err := os.Lstat(tree.at(workBase + dir))
		if err != nil {
			t.Fatal(err)
		}

		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Errorf("%s is %v, want a 0700 directory", workBase+dir, info.Mode())
		}
	}

	if got := tree.makeWorkDir(t, testTaskID); got != 0 {
		t.Errorf("make_work_dir on its own directory exited %d, want 0", got)
	}

	staged := workBase + testTaskID + "/s/0"
	if got := tree.writeWork(t, staged); got != 0 {
		t.Fatalf("a valid work write exited %d, want 0", got)
	}

	tree.assert(t, staged, "new\n")

	if got := tree.makeWorkDir(t, "NOT-A-TASK"); got != refusedExit {
		t.Errorf("make_work_dir with a bad task exited %d, want 13", got)
	}
}

func TestOnVMRefusesPlantedWorkDir(t *testing.T) {
	t.Parallel()

	taskDir := workBase + testTaskID
	staged := taskDir + "/s/0"

	plants := map[string]func(t *testing.T, tree *vmTree){
		"task directory owned by vcap": func(t *testing.T, tree *vmTree) {
			t.Helper()
			tree.giveToVCAP(t, taskDir)
		},
		"task directory with mode 0755": func(t *testing.T, tree *vmTree) {
			t.Helper()
			tree.chmod(t, taskDir, 0o755)
		},
		"task directory as a symlink": func(t *testing.T, tree *vmTree) {
			t.Helper()
			tree.mkdir(t, "/etc/planted/s")
			tree.chmod(t, "/etc/planted", 0o700)
			tree.chmod(t, "/etc/planted/s", 0o700)
			tree.replace(t, taskDir)
			symlink(t, tree.at("/etc/planted"), tree.at(taskDir))
		},
		"certpatch directory owned by vcap": func(t *testing.T, tree *vmTree) {
			t.Helper()
			tree.giveToVCAP(t, workBase)
		},
		"certpatch directory with mode 0755": func(t *testing.T, tree *vmTree) {
			t.Helper()
			tree.chmod(t, workBase, 0o755)
		},
		"staging directory owned by vcap": func(t *testing.T, tree *vmTree) {
			t.Helper()
			tree.giveToVCAP(t, taskDir+"/s")
		},
	}

	for name, plant := range plants {
		tree := newVMTree(t)
		if got := tree.makeWorkDir(t, testTaskID); got != 0 {
			t.Fatalf("%s: make_work_dir on a clean tree exited %d", name, got)
		}

		plant(t, tree)

		if got := tree.makeWorkDir(t, testTaskID); got != refusedExit {
			t.Errorf("%s: make_work_dir exited %d, want 13", name, got)
		}

		if got := tree.writeWork(t, staged); got != refusedExit {
			t.Errorf("%s: the staging write exited %d, want 13", name, got)
		}

		_, err := os.Lstat(tree.at("/etc/planted/s/0"))
		if err == nil {
			t.Errorf("%s: the refused write landed in the planted directory", name)
		}

		_, err = os.Lstat(tree.at(staged))
		if err == nil && name != "task directory as a symlink" {
			t.Errorf("%s: the refused write created the staging file", name)
		}
	}
}

func TestOnVMRefusesPlantedStagingFile(t *testing.T) {
	t.Parallel()

	staged := workBase + testTaskID + "/s/0"

	tree := newVMTree(t)
	if got := tree.makeWorkDir(t, testTaskID); got != 0 {
		t.Fatalf("make_work_dir on a clean tree exited %d", got)
	}

	symlink(t, tree.at("/etc/shadow-fixture"), tree.at(staged))

	if got := tree.writeWork(t, staged); got != refusedExit {
		t.Errorf("a planted staging symlink gave exit %d, want 13", got)
	}

	tree.assert(t, "/etc/shadow-fixture", "shadow\n")

	// A data directory that is not owned by root stops make_work_dir
	// before it creates anything.
	tree = newVMTree(t)
	tree.giveToVCAP(t, vcapData)

	if got := tree.makeWorkDir(t, testTaskID); got != refusedExit {
		t.Errorf("with /var/vcap/data owned by vcap make_work_dir exited %d, want 13", got)
	}

	_, err := os.Lstat(tree.at(workBase))
	if err == nil {
		t.Error("make_work_dir created the certpatch directory under a vcap-owned parent")
	}
}

func TestOnVMRefusesTraversal(t *testing.T) {
	t.Parallel()

	tree := newVMTree(t)

	traversal := pinnedJobDir + "/../def/config/server.crt"
	if got := tree.writePinned(t, traversal); got != refusedExit {
		t.Errorf("a pinned write through .. exited %d, want 13", got)
	}

	tree.assert(t, "/var/vcap/data/jobs/valkey/def/config/server.crt", "def\n")

	escape := pinnedJobDir + "/../../../../../etc/shadow-fixture"
	if got := tree.writePinned(t, escape); got != refusedExit {
		t.Errorf("a pinned write out of the tree exited %d, want 13", got)
	}

	tree.assert(t, "/etc/shadow-fixture", "shadow\n")

	if got := tree.makeWorkDir(t, testTaskID); got != 0 {
		t.Fatalf("make_work_dir exited %d", got)
	}

	workEscape := workBase + testTaskID + "/s/../../../../../etc/shadow-fixture"
	if got := tree.writeWork(t, workEscape); got != refusedExit {
		t.Errorf("a work write through .. exited %d, want 13", got)
	}

	tree.assert(t, "/etc/shadow-fixture", "shadow\n")

	if got := libCheck(t, tree.root, "valid_logical", "/var/vcap/jobs/valkey/../../etc/shadow"); got != refusedExit {
		t.Errorf("valid_logical with .. exited %d, want 13", got)
	}

	if got := tree.makeWorkDir(t, "../../etc"); got != refusedExit {
		t.Errorf("make_work_dir with a .. task exited %d, want 13", got)
	}
}

func TestOnVMRefusesWritableDirectory(t *testing.T) {
	t.Parallel()

	// Each directory on the way to the target, from the stand-in for /
	// down to the target's parent, made writable by a non-root user.
	writable := map[string]os.FileMode{
		"":                       0o777,
		"/var":                   0o757,
		"/var/vcap":              0o775,
		vcapData:                 0o777,
		"/var/vcap/data/jobs":    0o777,
		valkeyJobs:               0o775,
		pinnedJobDir:             0o777,
		pinnedJobDir + "/config": 0o775,
	}

	for dir, mode := range writable {
		tree := newVMTree(t)
		tree.chmod(t, dir, mode)

		if got := tree.writePinned(t, pinnedTarget); got != refusedExit {
			t.Errorf("with %q at mode %o the pinned write exited %d, want 13", dir, mode, got)
		}

		tree.assert(t, pinnedTarget, "old\n")
	}

	staged := workBase + testTaskID + "/s/0"

	for _, dir := range []string{"", "/var", "/var/vcap", vcapData} {
		tree := newVMTree(t)
		if got := tree.makeWorkDir(t, testTaskID); got != 0 {
			t.Fatalf("make_work_dir on a clean tree exited %d", got)
		}

		tree.chmod(t, dir, 0o777)

		if got := tree.makeWorkDir(t, testTaskID); got != refusedExit {
			t.Errorf("with %q at mode 777 make_work_dir exited %d, want 13", dir, got)
		}

		if got := tree.writeWork(t, staged); got != refusedExit {
			t.Errorf("with %q at mode 777 the staging write exited %d, want 13", dir, got)
		}

		_, err := os.Lstat(tree.at(staged))
		if err == nil {
			t.Errorf("with %q at mode 777 the refused write created the staging file", dir)
		}
	}

	tree := newVMTree(t)
	tree.chmod(t, vcapData, 0o777)

	if got := tree.makeWorkDir(t, testTaskID); got != refusedExit {
		t.Errorf("with a 0777 /var/vcap/data make_work_dir exited %d, want 13", got)
	}

	_, err := os.Lstat(tree.at(workBase))
	if err == nil {
		t.Error("make_work_dir created the certpatch directory under a writable parent")
	}
}

func TestOnVMAcceptsStockLayout(t *testing.T) {
	t.Parallel()

	dirs := []string{
		vcapData, "/var/vcap/data/jobs", valkeyJobs,
		pinnedJobDir, pinnedJobDir + "/config",
	}

	for _, mode := range []os.FileMode{0o755, 0o750} {
		tree := newVMTree(t)
		for _, dir := range dirs {
			tree.chmod(t, dir, mode)
		}

		if got := tree.writePinned(t, pinnedTarget); got != 0 {
			t.Errorf("with directories at mode %o the pinned write exited %d, want 0", mode, got)
		}

		tree.assert(t, pinnedTarget, "new\n")

		if got := tree.makeWorkDir(t, testTaskID); got != 0 {
			t.Errorf("with directories at mode %o make_work_dir exited %d, want 0", mode, got)
		}

		if got := tree.writeWork(t, workBase+testTaskID+"/b/0"); got != 0 {
			t.Errorf("with directories at mode %o the backup write exited %d, want 0", mode, got)
		}
	}
}

func TestOnVMRefusesHardLinkedTarget(t *testing.T) {
	t.Parallel()

	tree := newVMTree(t)
	tree.replace(t, pinnedTarget)

	err := os.Link(tree.at("/etc/shadow-fixture"), tree.at(pinnedTarget))
	if err != nil {
		t.Fatal(err)
	}

	if got := tree.writePinned(t, pinnedTarget); got != refusedExit {
		t.Errorf("a write to a hard-linked target exited %d, want 13", got)
	}

	tree.assert(t, "/etc/shadow-fixture", "shadow\n")

	// A second name for the target inside the job's own tree counts too.
	tree = newVMTree(t)

	err = os.Link(tree.at(pinnedTarget), tree.at(pinnedJobDir+"/server.crt.copy"))
	if err != nil {
		t.Fatal(err)
	}

	if got := tree.writePinned(t, pinnedTarget); got != refusedExit {
		t.Errorf("a write to a target with two names exited %d, want 13", got)
	}

	tree.assert(t, pinnedTarget, "old\n")
}
