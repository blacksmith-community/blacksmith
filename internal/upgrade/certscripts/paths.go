package certscripts

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrPathRefused is the error every validator wraps when it refuses its
// input. The message never repeats the input.
var ErrPathRefused = errors.New("certscripts: path refused")

// maxPathLength bounds every path, so no validator passes a path that
// the VM's tools would truncate or refuse in some other way.
const maxPathLength = 1024

// The path patterns. certlib.sh holds the same patterns for the checks the
// scripts run on the VM. The character classes leave out every shell
// metacharacter and whitespace, and the segment check below refuses
// empty, ".", and ".." segments, which the classes alone allow.
var (
	logicalPath = regexp.MustCompile(`^/var/vcap/jobs/[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+$`)
	pinnedPath  = regexp.MustCompile(`^/var/vcap/data/jobs/[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+$`)
	jobDirPath  = regexp.MustCompile(`^/var/vcap/data/jobs/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	taskID      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	workTail    = regexp.MustCompile(`^[sb]/(0|[1-9][0-9]{0,8})$`)
)

// workRoot holds one private directory per task, with staged files in
// its s directory and backups in its b directory.
const workRoot = "/var/vcap/data/certpatch/"

// refused wraps ErrPathRefused with a fixed reason.
func refused(reason string) error {
	return fmt.Errorf("%w: %s", ErrPathRefused, reason)
}

// cleanSegments reports whether every segment of the absolute path p is
// a real name, so none is empty, ".", or "..". Appending a slash puts a
// slash on both sides of every segment, the last one included.
func cleanSegments(p string) bool {
	s := p + "/"

	return !strings.Contains(s, "//") && !strings.Contains(s, "/./") && !strings.Contains(s, "/../")
}

// shaped reports whether p has the length, pattern, and segments a path
// of some kind needs.
func shaped(p string, pattern *regexp.Regexp) bool {
	return len(p) <= maxPathLength && pattern.MatchString(p) && cleanSegments(p)
}

// ValidLogicalJobPath accepts a path as a job sees it, under
// /var/vcap/jobs/<job>/. Such a path appears only in discovery output
// and in the UI, because /var/vcap/jobs/<job> is a symlink that BOSH
// repoints, and nothing ever writes to one.
func ValidLogicalJobPath(path string) error {
	if !shaped(path, logicalPath) {
		return refused("not a path under a job's directory in /var/vcap/jobs")
	}

	return nil
}

// ValidPinnedTarget accepts a certificate or key file under jobDir, the
// job's resolved directory that discovery recorded. jobDir must be
// /var/vcap/data/jobs/<job>/<version>, which is where BOSH installs a
// job and where /var/vcap/jobs/<job> points. Every write to a target file
// uses a path this accepts, and the script on the VM checks again that
// the job symlink still points at jobDir and that nothing on the way to
// the file is a symlink or owned by anyone but root.
func ValidPinnedTarget(path, jobDir string) error {
	if !shaped(jobDir, jobDirPath) {
		return refused("the job directory is not a resolved job directory in /var/vcap/data/jobs")
	}

	if !shaped(path, pinnedPath) {
		return refused("not a path under /var/vcap/data/jobs")
	}

	if !strings.HasPrefix(path, jobDir+"/") {
		return refused("the target is outside the job's resolved directory")
	}

	return nil
}

// ValidWorkPath accepts only /var/vcap/data/certpatch/<taskID>/s/<n> for
// a staged file and /var/vcap/data/certpatch/<taskID>/b/<n> for a backup,
// where n is a decimal file index with no leading zero.
func ValidWorkPath(path, taskID string) error {
	err := ValidTaskID(taskID)
	if err != nil {
		return err
	}

	tail, ok := strings.CutPrefix(path, workRoot+taskID+"/")
	if !ok || len(path) > maxPathLength || !workTail.MatchString(tail) {
		return refused("not a staging or backup path of this task")
	}

	return nil
}

// ValidTaskID accepts only a lowercase canonical UUID, which is the form
// CreateTask mints. A task ID names a directory on the VM, so nothing
// else may pass.
func ValidTaskID(id string) error {
	if !taskID.MatchString(id) {
		return refused("the task ID is not a lowercase UUID")
	}

	return nil
}
