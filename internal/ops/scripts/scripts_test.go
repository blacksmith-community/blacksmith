package scripts_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"blacksmith/internal/ops/scripts"
)

// libScript is the base library's name, without its .sh suffix.
const libScript = "lib"

// readLib reads lib.sh from this directory.
func readLib(t *testing.T) string {
	t.Helper()

	body, err := os.ReadFile(libScript + ".sh") //nolint:gosec // a file in this package
	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}

func TestInvocation(t *testing.T) {
	t.Parallel()

	target := "/var/vcap/data/jobs/valkey/abc/config/server.crt"

	program, argv := scripts.Invocation("patch", target, "task1")
	if program != "sudo -n /bin/bash -s --" {
		t.Errorf("program = %q", program)
	}

	want := []string{"patch", target, "task1"}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %q, want %q", argv, want)
	}
}

func TestParseLines(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{"path": true, "hash": true, "state": true}
	out := []byte("path=/var/vcap/data/jobs/valkey/abc/x hash=abc123\n\nstate=ok\n")

	got, err := scripts.ParseLines(out, allowed)
	if err != nil {
		t.Fatalf("ParseLines: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2: %v", len(got), got)
	}

	if got[0]["path"] != "/var/vcap/data/jobs/valkey/abc/x" || got[0]["hash"] != "abc123" || got[1]["state"] != "ok" {
		t.Errorf("parsed %v", got)
	}

	empty, err := scripts.ParseLines(nil, allowed)
	if err != nil || len(empty) != 0 {
		t.Errorf("empty output parsed as %v, %v", empty, err)
	}
}

func TestParseLinesRejectsUnknownKeys(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{"path": true, "hash": true}
	keyText := "MIIBszCCAVmgAwIBAgIUQ"

	bad := map[string]string{
		"unknown key":          "path=/x other=1\n",
		"unknown key on later": "path=/x\nsecret=" + keyText + "\n",
		"no equals sign":       "path=/x " + keyText + "\n",
		"empty key":            "=" + keyText + "\n",
		"repeated key":         "path=/x path=/y\n",
		"double space":         "path=/x  hash=1\n",
		"leading space":        " path=/x\n",
		"trailing space":       "path=/x \n",
		"tab inside":           "path=/x\thash=1\n",
		"carriage return":      "path=/x\r\n",
		"pem marker line":      "-----BEGIN PRIVATE KEY-----\n",
		"uppercase key":        "PATH=/x\n",
		"escape sequence":      "path=/x\x1b[2J\n",
		"start of heading":     "path=/x\x01\n",
		"backspace":            "path=/x\x08y\n",
		"bell in a key":        "pa\x07th=/x\n",
		"unit separator":       "path=/x\x1f\n",
		"delete":               "path=/x\x7f\n",
		"C1 next line":         "path=/x\u0085y\n",
		"C1 control sequence":  "path=/x\u009b2J\n",
		"C1 first":             "path=/x\u0080\n",
		"C1 last":              "path=/x\u009f\n",
		"invalid byte":         "path=/x\xff\n",
		"truncated sequence":   "path=/x\xc3\n",
		"overlong slash":       "path=\xc0\xaf\n",
		"surrogate half":       "path=/x\xed\xa0\x80\n",
	}

	for name, out := range bad {
		got, err := scripts.ParseLines([]byte(out), allowed)
		if err == nil {
			t.Errorf("%s: ParseLines accepted %q as %v", name, out, got)

			continue
		}

		if !errors.Is(err, scripts.ErrMalformedOutput) {
			t.Errorf("%s: error %v is not ErrMalformedOutput", name, err)
		}

		if got != nil {
			t.Errorf("%s: ParseLines returned %v with its error", name, got)
		}

		if strings.Contains(err.Error(), keyText) || strings.Contains(err.Error(), "PRIVATE") {
			t.Errorf("%s: the error repeats script output: %v", name, err)
		}
	}
}

func TestParseRecords(t *testing.T) {
	t.Parallel()

	kinds := map[string]map[string]bool{"match": {"job": true, "path": true}}

	got, err := scripts.ParseRecords([]byte("match job=valkey path=/x\n\nmatch job=redis\n"), kinds)
	if err != nil {
		t.Fatalf("ParseRecords: %v", err)
	}

	if len(got) != 2 || got[0].Kind != "match" || got[0].Fields["job"] != "valkey" || got[0].Line != 1 || got[1].Line != 3 {
		t.Errorf("parsed %v", got)
	}

	for _, out := range []string{"other job=x\n", "match\n", "match nope=1\n"} {
		_, err := scripts.ParseRecords([]byte(out), kinds)
		if !errors.Is(err, scripts.ErrMalformedOutput) {
			t.Errorf("ParseRecords(%q) error %v is not ErrMalformedOutput", out, err)
		}
	}
}

func TestLibraryIsTheEmbeddedLib(t *testing.T) {
	t.Parallel()

	if got := string(scripts.Library()); got != readLib(t) {
		t.Error("Library() is not the bytes of lib.sh")
	}
}

// AllocsPerRun counts the allocations of every goroutine, so a parallel
// test would add its own. The one allocation is the copy, so lib.sh is
// not read again.
//
//nolint:paralleltest // see above
func TestLibraryAllocatesOnlyItsCopy(t *testing.T) {
	if allocs := testing.AllocsPerRun(10, func() { _ = scripts.Library() }); allocs != 1 {
		t.Errorf("Library() allocates %v times a call, want 1", allocs)
	}
}

// A caller that changes the bytes Library returned leaves the next call's
// bytes as lib.sh. It does not run in parallel, so a Library that shared
// its bytes could not race the tests that read them, and the change is
// undone before it ends.
//
//nolint:paralleltest // see above
func TestLibraryReturnsACopy(t *testing.T) {
	first := scripts.Library()
	first[0] ^= 0xff

	second := string(scripts.Library())

	first[0] ^= 0xff

	if second != readLib(t) {
		t.Error("a change to the bytes Library returned reached the next call")
	}
}

func TestScriptJoinsLibsThenBody(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{"x.sh": {Data: []byte("echo x\n")}}
	libA := []byte("# a\n")
	libB := []byte("# b\n")

	got, err := scripts.Script(fsys, [][]byte{libA, libB}, "x")
	if err != nil {
		t.Fatalf("Script: %v", err)
	}

	if want := "# a\n# b\necho x\n"; !bytes.Equal(got, []byte(want)) {
		t.Errorf("Script = %q, want %q", got, want)
	}

	alone, err := scripts.Script(fsys, nil, "x")
	if err != nil || string(alone) != "echo x\n" {
		t.Errorf("Script with no libraries = %q, %v", alone, err)
	}
}

func TestScriptUnknownName(t *testing.T) {
	t.Parallel()

	fsys := fstest.MapFS{
		"lib.sh": {Data: []byte("# lib\n")},
		"x.sh":   {Data: []byte("echo x\n")},
	}

	for _, name := range []string{"nope", "lib", "", "../lib", "lib.sh", "X"} {
		body, err := scripts.Script(fsys, [][]byte{scripts.Library()}, name)
		if err == nil {
			t.Errorf("Script(%q) returned no error", name)

			continue
		}

		if !errors.Is(err, scripts.ErrUnknownScript) {
			t.Errorf("Script(%q) error %v is not ErrUnknownScript", name, err)
		}

		if body != nil {
			t.Errorf("Script(%q) returned text with its error", name)
		}

		if !strings.Contains(err.Error(), fmt.Sprintf("%q", name)) {
			t.Errorf("Script(%q) error %q does not name the script", name, err)
		}
	}
}

func TestScriptsContainNoTee(t *testing.T) {
	t.Parallel()

	tee := regexp.MustCompile(`(^|[^A-Za-z0-9_-])tee([^A-Za-z0-9_-]|$)`)
	if !tee.MatchString("cat x | tee y") || tee.MatchString("guarantee") {
		t.Fatal("the tee pattern is wrong")
	}

	for index, line := range strings.Split(readLib(t), "\n") {
		if tee.MatchString(line) {
			t.Errorf("lib.sh: line %d uses tee", index+1)
		}
	}
}

// TestExitTableMatchesGo shows that the table of exit codes in lib.sh's
// header lists exactly the codes that have Go constants.
func TestExitTableMatchesGo(t *testing.T) {
	t.Parallel()

	want := []int{
		scripts.ExitOK, scripts.ExitUnknown, scripts.ExitUsage, scripts.ExitDrift,
		scripts.ExitBackup, scripts.ExitRestart, scripts.ExitRefused, scripts.ExitInvalid,
	}

	row := regexp.MustCompile(`^#   ([0-9]+) +[a-z]`)

	var got []int

	for _, line := range strings.Split(readLib(t), "\n") {
		if !strings.HasPrefix(line, "#") {
			break
		}

		match := row.FindStringSubmatch(line)
		if match == nil {
			continue
		}

		code, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatal(err)
		}

		got = append(got, code)
	}

	if !slices.Equal(got, want) {
		t.Errorf("lib.sh's table lists the codes %v, want %v", got, want)
	}
}

func TestExitCodesMatchLib(t *testing.T) {
	t.Parallel()

	want := map[string]int{
		"EXIT_USAGE":   scripts.ExitUsage,
		"EXIT_DRIFT":   scripts.ExitDrift,
		"EXIT_BACKUP":  scripts.ExitBackup,
		"EXIT_RESTART": scripts.ExitRestart,
		"EXIT_REFUSED": scripts.ExitRefused,
		"EXIT_INVALID": scripts.ExitInvalid,
	}

	if scripts.ExitOK != 0 {
		t.Errorf("ExitOK = %d, want 0", scripts.ExitOK)
	}

	assignment := regexp.MustCompile(`\b(EXIT_[A-Z]+)=([0-9]+)\b`)
	got := make(map[string]int)

	for _, line := range strings.Split(readLib(t), "\n") {
		if !strings.HasPrefix(line, "readonly ") {
			continue
		}

		for _, match := range assignment.FindAllStringSubmatch(line, -1) {
			code, err := strconv.Atoi(match[2])
			if err != nil {
				t.Fatal(err)
			}

			if _, seen := got[match[1]]; seen {
				t.Errorf("lib.sh sets %s twice", match[1])
			}

			got[match[1]] = code
		}
	}

	for name, code := range want {
		if got[name] != code {
			t.Errorf("lib.sh sets %s=%d, but the Go constant is %d", name, got[name], code)
		}
	}

	for name := range got {
		if _, known := want[name]; !known {
			t.Errorf("lib.sh sets %s, which has no Go constant", name)
		}
	}

	// Sourcing lib.sh shows the values the scripts really see.
	t.Run("sourced", func(t *testing.T) {
		t.Parallel()

		if runtime.GOOS != "linux" {
			t.Skipf("sourcing lib.sh needs the Linux bash the VMs run, and this host is %s", runtime.GOOS)
		}

		out, err := exec.CommandContext(t.Context(), "bash", "-c",
			`source lib.sh; echo "$EXIT_USAGE $EXIT_DRIFT $EXIT_BACKUP $EXIT_RESTART $EXIT_REFUSED $EXIT_INVALID"`).Output()
		if err != nil {
			t.Fatalf("sourcing lib.sh: %v", err)
		}

		sourced := fmt.Sprintf("%d %d %d %d %d %d\n", scripts.ExitUsage, scripts.ExitDrift,
			scripts.ExitBackup, scripts.ExitRestart, scripts.ExitRefused, scripts.ExitInvalid)
		if string(out) != sourced {
			t.Errorf("sourced exit codes = %q, want %q", out, sourced)
		}
	})
}
