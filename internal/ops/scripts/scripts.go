// Package scripts holds what every helper script run on a BOSH VM shares,
// which is the base library lib.sh and the Go side of the contract: how a
// script is assembled and invoked, and how its output is read back.
// Packages with scripts of their own pass their file system and their
// libraries to Script.
package scripts

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"unicode/utf8"
)

// base holds lib.sh. The glob picks up each script that is added to this
// directory, with no edit here.
//
//go:embed *.sh
var base embed.FS

// libName is the base library. It is never run on its own.
const libName = "lib"

// The exit codes every script uses, which lib.sh lists as well. ExitUnknown
// is any other failure, after which the broker treats the VM's state as
// unknown and runs check before it goes on.
const (
	ExitOK      = 0
	ExitUnknown = 1
	ExitUsage   = 2
	ExitDrift   = 10
	ExitBackup  = 11
	ExitRestart = 12
	ExitRefused = 13
	// ExitInvalid is a new file that openssl cannot read as a
	// certificate or a key, found before it replaced anything.
	ExitInvalid = 14
)

// invokeProgram runs the script that arrives on stdin as root. The
// operation and its arguments follow as quoted words.
const invokeProgram = "sudo -n /bin/bash -s --"

// scriptName is the form of a script name, which is a file name in this
// directory without its .sh suffix.
var scriptName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ErrUnknownScript is the error Script returns for a name that has no
// embedded script.
var ErrUnknownScript = errors.New("scripts: no embedded script")

// ErrMalformedOutput is the error ParseLines returns for output that does
// not follow the key=value contract.
var ErrMalformedOutput = errors.New("scripts: malformed script output")

// Library returns the bytes of the base library lib.sh.
func Library() []byte {
	body, err := base.ReadFile(libName + ".sh")
	if err != nil {
		// The embed pattern matches lib.sh, so a build without it fails.
		panic(fmt.Sprintf("scripts: the base library is missing: %v", err))
	}

	return body
}

// Script returns the text sent on stdin to run the named script, which is
// each library in libs, in order, followed by <name>.sh read from fsys. A
// name with no script in fsys, and lib itself, return an error that names
// the script and nil. A caller treats that error as a programming fault
// and fails its step.
func Script(fsys fs.FS, libs [][]byte, name string) ([]byte, error) {
	if name == libName || !scriptName.MatchString(name) {
		return nil, fmt.Errorf("%w named %q", ErrUnknownScript, name)
	}

	body, err := fs.ReadFile(fsys, name+".sh")
	if err != nil {
		return nil, fmt.Errorf("%w named %q", ErrUnknownScript, name)
	}

	size := len(body)
	for _, lib := range libs {
		size += len(lib)
	}

	text := make([]byte, 0, size)
	for _, lib := range libs {
		text = append(text, lib...)
	}

	return append(text, body...), nil
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

// outputKey is the form of a key in a script's output.
var outputKey = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ParseLines reads a script's output, which is one result per line as
// key=value pairs separated by single spaces. Each line becomes one map.
// Empty lines are skipped. A key outside allowed, a key that repeats on
// a line, a field without an equals sign, and any stray whitespace fail
// the whole parse, and so does a line that is not valid UTF-8 or holds
// a control character. The error names the line and field by number and
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

// Record is one line of output that starts with a word naming its kind,
// such as `match job=valkey path=...`. Line is the line's number in the
// output, counting from 1.
type Record struct {
	Kind   string
	Fields map[string]string
	Line   int
}

// ParseRecords reads a script's output whose lines each start with a word
// that names the kind of line, followed by key=value fields as ParseLines
// reads them. kinds maps each kind of line to the keys it may hold. Empty
// lines are skipped. A kind outside kinds, a kind with no fields, and
// every fault ParseLines refuses fail the whole parse, with an error that
// names the line by number and never repeats the output.
func ParseRecords(out []byte, kinds map[string]map[string]bool) ([]Record, error) {
	lines := strings.Split(string(out), "\n")
	records := make([]Record, 0, len(lines))

	for index, line := range lines {
		if line == "" {
			continue
		}

		kind, rest, _ := strings.Cut(line, " ")

		allowed, known := kinds[kind]
		if !known {
			return nil, fmt.Errorf("%w: line %d: %w", ErrMalformedOutput, index+1, errKind)
		}

		fields, err := parseLine(rest, allowed)
		if err != nil {
			return nil, fmt.Errorf("%w: line %d: %w", ErrMalformedOutput, index+1, err)
		}

		records = append(records, Record{Kind: kind, Fields: fields, Line: index + 1})
	}

	return records, nil
}

// The ways a line of output can break the contract. None of them repeats
// the output.
var (
	errKind     = errors.New("the line does not start with a known kind")
	errEncoding = errors.New("the line is not valid UTF-8")
	errControl  = errors.New("the line holds a control character")
	errNoPair   = errors.New("a field is not a key=value pair")
	errBadKey   = errors.New("a field has a malformed key")
	errUnknown  = errors.New("a field has a key that is not expected")
	errRepeated = errors.New("a field repeats a key")
)

// isControl reports whether r is a C0 control, DEL, or a C1 control.
// A value printed into the journal, the UI, or a log must not carry any
// of them, because a terminal or a viewer would act on them. Tab counts
// as a control here too, because only a single space separates fields.
func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// parseLine reads one line of key=value fields. The line must be valid
// UTF-8 with no control character.
func parseLine(line string, allowed map[string]bool) (map[string]string, error) {
	if !utf8.ValidString(line) {
		return nil, errEncoding
	}

	if strings.IndexFunc(line, isControl) >= 0 {
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
