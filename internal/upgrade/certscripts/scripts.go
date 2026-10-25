// Package certscripts holds the helper scripts that patch certificate
// files on a VM, and the Go side of their contract: how a script is
// assembled and invoked, how content is staged, and how a script's output
// is read back. The parts every script shares live in ops/scripts, and the
// names here are aliases of them.
package certscripts

import (
	"embed"
	"fmt"
	"io"
	"strings"

	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/ops/scripts"
)

// certFS holds lib.sh and every helper script beside it. The glob picks
// up each script that is added to this directory, with no edit here.
//
//go:embed *.sh
var certFS embed.FS

// libName is the certificate package's library. It is joined in front of
// every script and is never run on its own.
const libName = "lib"

// patchScript is the script that changes files on a VM, staging included.
const patchScript = "patch"

// The exit codes every script uses, which lib.sh lists as well.
const (
	ExitOK      = scripts.ExitOK
	ExitUnknown = scripts.ExitUnknown
	ExitUsage   = scripts.ExitUsage
	ExitDrift   = scripts.ExitDrift
	ExitBackup  = scripts.ExitBackup
	ExitRestart = scripts.ExitRestart
	ExitRefused = scripts.ExitRefused
	ExitInvalid = scripts.ExitInvalid
)

// opStage is patch.sh's operation that writes the content on stdin to a
// staging path.
const opStage = "stage"

// ErrUnknownScript is the error Script returns for a name that has no
// embedded script.
var ErrUnknownScript = scripts.ErrUnknownScript

// ErrMalformedOutput is the error ParseLines returns for output that does
// not follow the key=value contract.
var ErrMalformedOutput = scripts.ErrMalformedOutput

// Record is one line of output that starts with a word naming its kind.
type Record = scripts.Record

// Script returns the text sent on stdin to run the named script, which is
// lib.sh followed by <name>.sh. A name with no embedded script, and lib
// itself, return an error that names the script and an empty string. A
// caller treats that error as a programming fault and fails its step.
func Script(name string) (string, error) {
	lib, err := certFS.ReadFile(libName + ".sh")
	if err != nil {
		return "", fmt.Errorf("certscripts: the shared library is missing: %w", err)
	}

	text, err := scripts.Script(certFS, [][]byte{lib}, name)
	if err != nil {
		return "", err
	}

	return string(text), nil
}

// Invocation returns the program and the arguments that run a script sent on
// stdin as root, for ssh.Runner.Run. See scripts.Invocation.
func Invocation(op string, args ...string) (string, []string) {
	return scripts.Invocation(op, args...)
}

// StageCommand returns the command line that writes the content on
// stdin to path, a staging path of the task. It runs patch.sh's stage
// operation, which takes the input StageInput builds. It refuses, with an
// error that wraps ErrPathRefused, any path that ValidWorkPath refuses and
// any backup path. On the VM the stage operation runs valid_work on the
// task's directories itself, so it writes nothing unless they are the
// private ones prepare made, and it refuses a name that already exists
// as anything.
func StageCommand(path, taskID string) (string, error) {
	err := ValidWorkPath(path, taskID)
	if err != nil {
		return "", err
	}

	if !strings.HasPrefix(path, workRoot+taskID+"/s/") {
		return "", refused("not a staging path of this task")
	}

	program, argv := Invocation(opStage, taskID, path)

	return ssh.Command(program, argv), nil
}

// StageInput returns the stdin for the command StageCommand builds,
// which is lib.sh and patch.sh followed by content. patch.sh starts its
// operation from its last line, so bash has read the whole script before
// the stage operation copies the rest of stdin, and content is never read
// as shell.
func StageInput(content io.Reader) (io.Reader, error) {
	text, err := Script(patchScript)
	if err != nil {
		return nil, err
	}

	return io.MultiReader(strings.NewReader(text), content), nil
}

// ParseLines reads a script's output, one result per line. See
// scripts.ParseLines.
func ParseLines(out []byte, allowed map[string]bool) ([]map[string]string, error) {
	return scripts.ParseLines(out, allowed)
}

// ParseRecords reads a script's output whose lines each start with a word
// that names the kind of line. See scripts.ParseRecords.
func ParseRecords(out []byte, kinds map[string]map[string]bool) ([]Record, error) {
	return scripts.ParseRecords(out, kinds)
}
