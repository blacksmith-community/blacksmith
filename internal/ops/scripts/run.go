package scripts

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
)

// runScript is the script that stages, runs, and cleans up after one
// operator command on a VM.
const runScript = "run"

// The operations of run.sh.
const (
	opRunPrepare = "prepare"
	opRunExec    = "exec"
	opRunCleanup = "cleanup"
)

// The kinds of line run.sh prints. staged answers prepare, and cleanup
// answers cleanup.
const (
	RecordStaged  = "staged"
	RecordCleanup = "cleanup"
)

// RunKinds returns the kinds map for ParseRecords on the output of run.sh,
// which holds each kind of line and the keys it may carry.
func RunKinds() map[string]map[string]bool {
	return map[string]map[string]bool{
		RecordStaged:  {"bytes": true, "sha256": true},
		RecordCleanup: {"started": true, "exit": true, "killed": true},
	}
}

// The bounds of the per-node timeout in seconds.
const (
	minRunSeconds = 10
	maxRunSeconds = 3600
)

// runSeconds is one to four digits with no leading zero, because the
// shell reads a leading zero as octal.
var runSeconds = regexp.MustCompile(`^[1-9][0-9]{1,3}$`)

// The values a cleanup record may carry. started is - when the task
// directory was already gone, so nothing is known about the command.
var (
	cleanupYesNo = regexp.MustCompile(`^(yes|no)$`)
	cleanupExit  = regexp.MustCompile(`^(-|[0-9]|[1-9][0-9]|1[0-9]{2}|2[0-4][0-9]|25[0-5])$`)
	cleanupSeen  = regexp.MustCompile(`^(yes|no|-)$`)
)

// Cleanup is what the cleanup operation reports. Started is "yes", "no",
// or "-" when the task directory was already gone, which leaves the
// outcome unknown. Exit is the recorded exit code, or "-" when there is
// none. Killed is "yes" or "no".
type Cleanup struct {
	Started string
	Exit    string
	Killed  string
}

// ParseCleanup reads the output of cleanup, which must be exactly one
// cleanup record with a valid value in each field.
func ParseCleanup(out []byte) (Cleanup, error) {
	records, err := ParseRecords(out, RunKinds())
	if err != nil {
		return Cleanup{}, err
	}

	if len(records) != 1 || records[0].Kind != RecordCleanup {
		return Cleanup{}, fmt.Errorf("%w: cleanup did not print exactly one cleanup record", ErrMalformedOutput)
	}

	fields := records[0].Fields
	result := Cleanup{Started: fields["started"], Exit: fields["exit"], Killed: fields["killed"]}

	if !cleanupSeen.MatchString(result.Started) || !cleanupExit.MatchString(result.Exit) ||
		!cleanupYesNo.MatchString(result.Killed) {
		return Cleanup{}, fmt.Errorf("%w: a cleanup field holds a value outside its set", ErrMalformedOutput)
	}

	return result, nil
}

// RunScript returns the text sent on stdin for the operations that carry
// no command, which is lib.sh followed by run.sh.
func RunScript() ([]byte, error) {
	return Script(base, [][]byte{Library()}, runScript)
}

// RunInput returns the stdin for the prepare operation, which is the
// script followed by exactly the bytes of command. run.sh starts its
// operation from its last line, so bash has read the whole script before
// prepare copies the rest of stdin, and command is never read as shell.
func RunInput(command []byte) (io.Reader, error) {
	text, err := RunScript()
	if err != nil {
		return nil, err
	}

	return io.MultiReader(bytes.NewReader(text), bytes.NewReader(command)), nil
}

// ValidRunUser accepts only the two accounts a command may run as.
func ValidRunUser(user string) error {
	if user != "root" && user != "vcap" {
		return fmt.Errorf("%w: the user is neither root nor vcap", ErrRefused)
	}

	return nil
}

// ValidRunSeconds accepts only digits that make a whole number of seconds
// from 10 to 3600.
func ValidRunSeconds(seconds string) error {
	if runSeconds.MatchString(seconds) {
		value, err := strconv.Atoi(seconds)
		if err == nil && value >= minRunSeconds && value <= maxRunSeconds {
			return nil
		}
	}

	return fmt.Errorf("%w: the timeout is not between %d and %d seconds",
		ErrRefused, minRunSeconds, maxRunSeconds)
}

// ValidRunDigest accepts only a lowercase hex SHA-256.
func ValidRunDigest(digest string) error {
	if !hashValue.MatchString(digest) {
		return fmt.Errorf("%w: the digest is not a lowercase hex SHA-256", ErrRefused)
	}

	return nil
}

// OpRunPrepare returns the program and arguments that run prepare for a
// task, with the input RunInput builds.
func OpRunPrepare(task, user string) (string, []string, error) {
	err := ValidTaskID(task)
	if err != nil {
		return "", nil, err
	}

	err = ValidRunUser(user)
	if err != nil {
		return "", nil, err
	}

	program, argv := Invocation(opRunPrepare, task, user)

	return program, argv, nil
}

// OpRunExec returns the program and arguments that run exec for a task,
// with the input RunScript builds. digest is the SHA-256 that prepare
// reported for the staged command.
func OpRunExec(task, user, seconds, digest string) (string, []string, error) {
	err := ValidTaskID(task)
	if err != nil {
		return "", nil, err
	}

	err = ValidRunUser(user)
	if err != nil {
		return "", nil, err
	}

	err = ValidRunSeconds(seconds)
	if err != nil {
		return "", nil, err
	}

	err = ValidRunDigest(digest)
	if err != nil {
		return "", nil, err
	}

	program, argv := Invocation(opRunExec, task, user, seconds, digest)

	return program, argv, nil
}

// OpRunCleanup returns the program and arguments that run cleanup for a
// task, with the input RunScript builds.
func OpRunCleanup(task string) (string, []string, error) {
	err := ValidTaskID(task)
	if err != nil {
		return "", nil, err
	}

	program, argv := Invocation(opRunCleanup, task)

	return program, argv, nil
}
