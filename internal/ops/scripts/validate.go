package scripts

import (
	"errors"
	"fmt"
	"regexp"
)

// SudoCheckCommand is the command a driver runs on a VM before any script.
// Every privileged step runs with sudo -n, so a VM whose BOSH SSH user
// needs a password fails this at once instead of hanging a later step.
const SudoCheckCommand = "sudo -n true"

// ErrRefused is the error every validator here wraps when it refuses its
// input. The message never repeats the input.
var ErrRefused = errors.New("scripts: input refused")

// The patterns lib.sh holds as CP_TASK_RE and CP_HASH_RE.
var (
	taskID    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	hashValue = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ValidTaskID accepts only a lowercase canonical UUID, which is the form
// CreateTask mints. A task ID names a directory on the VM, so nothing
// else may pass.
func ValidTaskID(id string) error {
	if !taskID.MatchString(id) {
		return fmt.Errorf("%w: the task ID is not a lowercase UUID", ErrRefused)
	}

	return nil
}

// ValidHash accepts only a lowercase hex SHA-256, the form norm_hash prints.
func ValidHash(hash string) error {
	if !hashValue.MatchString(hash) {
		return fmt.Errorf("%w: the hash is not a lowercase hex SHA-256", ErrRefused)
	}

	return nil
}
