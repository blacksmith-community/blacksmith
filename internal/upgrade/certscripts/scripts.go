// Package certscripts holds the helper scripts that patch certificate
// files on a VM, and the Go side of their contract: how a script is
// assembled and invoked, how content is staged, and how a script's output
// is read back.
package certscripts

import (
	"embed"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"blacksmith/internal/bosh/ssh"
)

// scripts holds lib.sh and every helper script beside it. The glob picks
// up each script that is added to this directory, with no edit here.
//
//go:embed *.sh
var scripts embed.FS

// libName is the shared library. It is joined in front of every script
// and is never run on its own.
const libName = "lib"

// The exit codes every script uses, which lib.sh lists as well.
const (
	ExitOK      = 0
	ExitUsage   = 2
	ExitDrift   = 10
	ExitBackup  = 11
	ExitRestart = 12
	ExitRefused = 13
)

// invokeProgram runs the script that arrives on stdin as root. The
// operation and its arguments follow as quoted words.
const invokeProgram = "sudo -n /bin/bash -s --"

// stageProgram writes stdin to a file that must not exist yet. set -C
// makes the redirection fail when the name exists, even as a symlink, so
// staging never truncates or follows a file, and the umask keeps the new
// file readable by root alone. The path arrives as $1, so it is never
// read as shell syntax.
const stageProgram = `sudo -n /bin/sh -c 'set -C && umask 077 && cat > "$1"' sh`

// scriptName is the form of a script name, which is a file name in this
// directory without its .sh suffix.
var scriptName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ErrUnknownScript is the error Script returns for a name that has no
// embedded script.
var ErrUnknownScript = errors.New("certscripts: no embedded script")

// ErrMalformedOutput is the error ParseLines returns for output that does
// not follow the key=value contract.
var ErrMalformedOutput = errors.New("certscripts: malformed script output")

// Script returns the text sent on stdin to run the named script, which is
// lib.sh followed by <name>.sh. A name with no embedded script, and lib
// itself, return an error that names the script and an empty string. A
// caller treats that error as a programming fault and fails its step.
func Script(name string) (string, error) {
	if name == libName || !scriptName.MatchString(name) {
		return "", fmt.Errorf("%w named %q", ErrUnknownScript, name)
	}

	body, err := scripts.ReadFile(name + ".sh")
	if err != nil {
		return "", fmt.Errorf("%w named %q", ErrUnknownScript, name)
	}

	lib, err := scripts.ReadFile(libName + ".sh")
	if err != nil {
		return "", fmt.Errorf("certscripts: the shared library is missing: %w", err)
	}

	return string(lib) + string(body), nil
}

// Invocation returns the program and the arguments that run a script sent on
// stdin as root, for ssh.Runner.Run. The arguments are op and then args,
// and Run quotes each one, so the command line is
// `sudo -n /bin/bash -s -- <op> <args>`. Only fixed operation names,
// validated paths, hashes, and task IDs may be passed, never content.
func Invocation(op string, args ...string) (string, []string) {
	argv := make([]string, 0, len(args)+1)
	argv = append(argv, op)
	argv = append(argv, args...)

	return invokeProgram, argv
}

// StageCommand returns the command line that writes stdin to path, which
// is `sudo -n /bin/sh -c 'set -C && umask 077 && cat > "$1"' sh <path>`
// with the path quoted. The path must be one that ValidWorkPath accepted.
// The command fails, and writes nothing, when the path already exists.
func StageCommand(path string) string {
	return ssh.Command(stageProgram, []string{path})
}

// outputKey is the form of a key in a script's output.
var outputKey = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ParseLines reads a script's output, which is one result per line as
// key=value pairs separated by single spaces. Each line becomes one map.
// Empty lines are skipped. A key outside allowed, a key that repeats on
// a line, a field without an equals sign, and any stray whitespace fail
// the whole parse. The error names the line and field by number and
// never repeats the output, which could hold anything a script misprinted.
func ParseLines(out []byte, allowed map[string]bool) ([]map[string]string, error) {
	lines := strings.Split(string(out), "\n")
	results := make([]map[string]string, 0, len(lines))

	for index, line := range lines {
		if line == "" {
			continue
		}

		result, err := parseLine(line, allowed)
		if err != nil {
			return nil, fmt.Errorf("%w: line %d: %w", ErrMalformedOutput, index+1, err)
		}

		results = append(results, result)
	}

	return results, nil
}

// The ways a line of output can break the contract. None of them repeats
// the output.
var (
	errControl  = errors.New("the line holds a control character")
	errNoPair   = errors.New("a field is not a key=value pair")
	errBadKey   = errors.New("a field has a malformed key")
	errUnknown  = errors.New("a field has a key that is not expected")
	errRepeated = errors.New("a field repeats a key")
)

// parseLine reads one line of key=value fields.
func parseLine(line string, allowed map[string]bool) (map[string]string, error) {
	if strings.ContainsAny(line, "\t\r\v\f\x00") {
		return nil, errControl
	}

	result := make(map[string]string)

	for index, field := range strings.Split(line, " ") {
		key, value, ok := strings.Cut(field, "=")

		var err error

		switch {
		case !ok:
			err = errNoPair
		case !outputKey.MatchString(key):
			err = errBadKey
		case !allowed[key]:
			err = errUnknown
		default:
			if _, seen := result[key]; seen {
				err = errRepeated
			}
		}

		if err != nil {
			return nil, fmt.Errorf("field %d: %w", index+1, err)
		}

		result[key] = value
	}

	return result, nil
}
