// Package certscripts holds the helper scripts that patch certificate
// files on a VM, and the Go side of what only they need: how a script is
// assembled from the certificate library, how content is staged, and how
// paths and task IDs are validated. The parts every script shares, which
// are the exit codes, the invocation, and the output parsers, live in
// ops/scripts and are used from there.
package certscripts

import (
	"embed"
	"fmt"
	"io"
	"strings"

	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/ops/scripts"
)

// certFS holds certlib.sh and every helper script beside it. The glob
// picks up each script that is added to this directory, with no edit here.
//
//go:embed *.sh
var certFS embed.FS

// libName is the certificate package's library, which holds what only the
// certificate scripts use. It is joined after the base library and in
// front of every script, and is never run on its own.
const libName = "certlib"

// patchScript is the script that changes files on a VM, staging included.
const patchScript = "patch"

// opStage is patch.sh's operation that writes the content on stdin to a
// staging path.
const opStage = "stage"

// ErrUnknownScript is the error Script returns for a name that has no
// embedded script.
var ErrUnknownScript = scripts.ErrUnknownScript

// Script returns the text sent on stdin to run the named script, which is
// the base lib.sh, then certlib.sh, then <name>.sh. A name with no
// embedded script, and lib and certlib themselves, return an error that
// names the script and an empty string. A caller treats that error as a
// programming fault and fails its step.
func Script(name string) (string, error) {
	if name == libName {
		return "", fmt.Errorf("%w named %q", ErrUnknownScript, name)
	}

	lib, err := certFS.ReadFile(libName + ".sh")
	if err != nil {
		return "", fmt.Errorf("certscripts: the certificate library is missing: %w", err)
	}

	text, err := scripts.Script(certFS, [][]byte{scripts.Library(), lib}, name)
	if err != nil {
		return "", err
	}

	return string(text), nil
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

	program, argv := scripts.Invocation(opStage, taskID, path)

	return ssh.Command(program, argv), nil
}

// StageInput returns the stdin for the command StageCommand builds,
// which is both libraries and patch.sh followed by content. patch.sh
// starts its operation from its last line, so bash has read the whole
// script before the stage operation copies the rest of stdin, and content
// is never read as shell.
func StageInput(content io.Reader) (io.Reader, error) {
	text, err := Script(patchScript)
	if err != nil {
		return nil, err
	}

	return io.MultiReader(strings.NewReader(text), content), nil
}
