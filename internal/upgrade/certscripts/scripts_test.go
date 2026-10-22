package certscripts_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/certpatch"
	"blacksmith/internal/upgrade/certscripts"
)

const testTaskID = "0f1e2d3c-4b5a-4968-8776-655443322110"

// libScript is the name of the shared library among the scripts.
const libScript = "lib"

// requireLinux skips a test that runs the helper scripts, because macOS
// ships bash 3.2, BSD stat, and no sha256sum.
func requireLinux(t *testing.T) {
	t.Helper()

	if runtime.GOOS != "linux" {
		t.Skip("the helper scripts need Linux tools; run this test in the golang container")
	}

	_, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
}

// libHash runs the bash norm_hash on path.
func libHash(t *testing.T, path string) string {
	t.Helper()

	out, err := runNormHash(t.Context(), path)
	if err != nil {
		t.Fatalf("norm_hash on %s: %v", filepath.Base(path), err)
	}

	return strings.TrimSpace(string(out))
}

// runNormHash runs the bash norm_hash on path from lib.sh.
func runNormHash(ctx context.Context, path string) ([]byte, error) {
	//nolint:gosec // fixed program text; the path is a test fixture
	out, err := exec.CommandContext(ctx, "bash", "-c", `source lib.sh; norm_hash "$1"`, "_", path).Output()
	if err != nil {
		return out, fmt.Errorf("norm_hash: %w", err)
	}

	return out, nil
}

// throwawayKey writes a freshly generated EC key in PEM form to dir, with
// CRLF endings when crlf is set, and returns its path.
func throwawayKey(t *testing.T, dir string, crlf bool) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	text := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	name := "key.pem"

	if crlf {
		text = []byte(strings.ReplaceAll(string(text), "\n", "\r\n"))
		name = "key-crlf.pem"
	}

	path := filepath.Join(dir, name)

	err = os.WriteFile(path, text, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	return path
}

func TestNormalisedHashParityWithBash(t *testing.T) {
	t.Parallel()
	requireLinux(t)

	entries, err := os.ReadDir("testdata/normalise")
	if err != nil {
		t.Fatal(err)
	}

	var paths []string

	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		paths = append(paths, filepath.Join("testdata/normalise", entry.Name()))
	}

	if len(paths) < 5 {
		t.Fatalf("found %d fixtures, want at least 5", len(paths))
	}

	keys := t.TempDir()
	paths = append(paths, throwawayKey(t, keys, false), throwawayKey(t, keys, true))

	for _, path := range paths {
		content, err := os.ReadFile(path) //nolint:gosec // a test fixture
		if err != nil {
			t.Fatal(err)
		}

		want := certpatch.NormalisedHash(content)
		if got := libHash(t, path); got != want {
			t.Errorf("%s: bash norm_hash = %q, Go NormalisedHash = %q", filepath.Base(path), got, want)
		}
	}
}

func TestNormHashRefusesNUL(t *testing.T) {
	t.Parallel()
	requireLinux(t)

	path := filepath.Join(t.TempDir(), "nul.txt")

	err := os.WriteFile(path, []byte("a\x00b\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	out, err := runNormHash(t.Context(), path)
	if err == nil {
		t.Fatalf("norm_hash accepted a file with a NUL byte and printed %q", out)
	}

	if len(out) != 0 {
		t.Errorf("norm_hash printed %q for a refused file, want nothing", out)
	}
}

// stageProgramText is the fixed staging program. It refuses a name that
// exists as anything at all, and a parent directory that is not root's
// with mode 0700, before it writes.
const stageProgramText = `sudo -n /bin/sh -c 'set -C && umask 077 && ` +
	`[ ! -e "$1" ] && [ ! -L "$1" ] && [ ! -L "${1%/*}" ] && ` +
	`[ "$(stat -c %u:%a -- "${1%/*}")" = 0:700 ] && cat > "$1"' sh`

func TestStageCommandExact(t *testing.T) {
	t.Parallel()

	path := "/var/vcap/data/certpatch/" + testTaskID + "/s/0"
	want := stageProgramText + ` '` + path + `'`

	got, err := certscripts.StageCommand(path, testTaskID)
	if err != nil {
		t.Fatalf("StageCommand: %v", err)
	}

	if got != want {
		t.Errorf("StageCommand = %q\nwant         %q", got, want)
	}
}

func TestStageCommandRefusesOtherPaths(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		"/var/vcap/data/certpatch/" + otherTaskID + "/s/0",
		"/var/vcap/data/certpatch/" + testTaskID + "/s/../b/0",
		pinnedTarget,
		"/etc/shadow",
		"",
	} {
		got, err := certscripts.StageCommand(path, testTaskID)
		if !errors.Is(err, certscripts.ErrPathRefused) {
			t.Errorf("StageCommand(%q) error = %v, want ErrPathRefused", path, err)
		}

		if got != "" {
			t.Errorf("StageCommand(%q) returned %q with its error", path, got)
		}
	}
}

func TestCommandsPassTheRunnerCheck(t *testing.T) {
	t.Parallel()

	runner := ssh.NewRunner(nil, nil)

	stage, err := certscripts.StageCommand("/var/vcap/data/certpatch/"+testTaskID+"/b/12", testTaskID)
	if err != nil {
		t.Fatal(err)
	}

	program, argv := certscripts.Invocation("patch", pinnedTarget, testTaskID, strings.Repeat("0123456789abcdef", 4))

	for name, run := range map[string]func() error{
		"staging": func() error {
			_, err := runner.Run(t.Context(), stage, nil, nil, 0)

			return err //nolint:wrapcheck // compared below
		},
		"invocation": func() error {
			_, err := runner.Run(t.Context(), program, argv, nil, 0)

			return err //nolint:wrapcheck // compared below
		},
	} {
		// A runner with no connection checks the command line first, so
		// ErrNoConnection means the command passed that check.
		err := run()
		if !errors.Is(err, ssh.ErrNoConnection) {
			t.Errorf("%s command: Run error = %v, want ErrNoConnection", name, err)
		}
	}
}

// stagedText is what the staging tests try to write.
const stagedText = "staged\n"

// runStage runs the staging command on path, without its sudo prefix,
// because the container already runs as root and has no sudo. It reports
// whether the command succeeded. A command that blocks, as a write to a
// FIFO would, fails the test when its context ends.
func runStage(t *testing.T, path string) bool {
	t.Helper()

	// Build the command for a real work path, then point it at path,
	// so the test runs the program text StageCommand produces.
	form := workBase + testTaskID + "/s/0"

	command, err := certscripts.StageCommand(form, testTaskID)
	if err != nil {
		t.Fatal(err)
	}

	program, found := strings.CutSuffix(command, " "+ssh.Quote(form))
	if !found {
		t.Fatal("the staging command does not end with its quoted path")
	}

	program, found = strings.CutPrefix(program, "sudo -n ")
	if !found {
		t.Fatal("the staging command does not start with sudo -n")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", program+" "+ssh.Quote(path)) //nolint:gosec // the command under test
	cmd.Stdin = strings.NewReader(stagedText)

	err = cmd.Run()

	if ctx.Err() != nil {
		t.Fatalf("staging %s blocked until its deadline", filepath.Base(path))
	}

	return err == nil
}

// writeFile writes a fixture file and fails the test when it cannot.
func writeFile(t *testing.T, path, content string) {
	t.Helper()

	err := os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatal(err)
	}
}

// symlink makes a fixture symlink and fails the test when it cannot.
func symlink(t *testing.T, target, link string) {
	t.Helper()

	err := os.Symlink(target, link)
	if err != nil {
		t.Fatal(err)
	}
}

// privateDir makes a directory with mode 0700 under dir.
func privateDir(t *testing.T, dir, name string) string {
	t.Helper()

	path := filepath.Join(dir, name)

	err := os.Mkdir(path, 0o700)
	if err != nil {
		t.Fatal(err)
	}

	return path
}

func TestStageCommandWritesNewFile(t *testing.T) {
	t.Parallel()
	requireRoot(t)

	dir := privateDir(t, t.TempDir(), "s")

	fresh := filepath.Join(dir, "0")
	if !runStage(t, fresh) {
		t.Fatal("staging a new name failed")
	}

	assertContent(t, fresh, stagedText)

	info, err := os.Stat(fresh)
	if err != nil {
		t.Fatal(err)
	}

	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("staged file mode = %o, want 600", mode)
	}
}

func TestStageCommandRefusesExisting(t *testing.T) {
	t.Parallel()
	requireRoot(t)

	top := t.TempDir()
	dir := privateDir(t, top, "s")

	existing := filepath.Join(dir, "existing")
	writeFile(t, existing, "other\n")

	if runStage(t, existing) {
		t.Error("staging over an existing file succeeded")
	}

	assertContent(t, existing, "other\n")

	target := filepath.Join(top, "target")
	writeFile(t, target, "target\n")

	link := filepath.Join(dir, "link")
	symlink(t, target, link)

	if runStage(t, link) {
		t.Error("staging through a symlink succeeded")
	}

	assertContent(t, target, "target\n")

	dangling := filepath.Join(dir, "dangling")
	symlink(t, filepath.Join(top, "absent"), dangling)

	if runStage(t, dangling) {
		t.Error("staging through a dangling symlink succeeded")
	}

	_, err := os.Lstat(filepath.Join(top, "absent"))
	if err == nil {
		t.Error("staging through a dangling symlink created its target")
	}

	// set -C refuses only an existing regular file, so these two are
	// refused by the explicit check that runs before the write. Without
	// it, a FIFO would hand the staged bytes to whoever reads it, and the
	// symlink would send them to /dev/null while staging reported success.
	fifo := filepath.Join(dir, "fifo")

	err = syscall.Mkfifo(fifo, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	if runStage(t, fifo) {
		t.Error("staging onto a FIFO succeeded")
	}

	devNull := filepath.Join(dir, "null")
	symlink(t, "/dev/null", devNull)

	if runStage(t, devNull) {
		t.Error("staging through a symlink to /dev/null succeeded")
	}

	sub := privateDir(t, dir, "sub")
	if runStage(t, sub) {
		t.Error("staging onto a directory succeeded")
	}
}

func TestStageCommandRefusesUnprivateDirectory(t *testing.T) {
	t.Parallel()
	requireRoot(t)

	top := t.TempDir()

	open := privateDir(t, top, "open")

	err := os.Chmod(open, 0o755) //nolint:gosec // the directory under test must be too open
	if err != nil {
		t.Fatal(err)
	}

	if runStage(t, filepath.Join(open, "0")) {
		t.Error("staging into a 0755 directory succeeded")
	}

	owned := privateDir(t, top, "owned")

	err = os.Chown(owned, vcapUID, vcapUID)
	if err != nil {
		t.Fatal(err)
	}

	if runStage(t, filepath.Join(owned, "0")) {
		t.Error("staging into a directory owned by vcap succeeded")
	}

	inside := privateDir(t, top, "inside")
	linked := filepath.Join(top, "linked")
	symlink(t, inside, linked)

	if runStage(t, filepath.Join(linked, "0")) {
		t.Error("staging through a symlinked directory succeeded")
	}

	for _, path := range []string{filepath.Join(open, "0"), filepath.Join(owned, "0"), filepath.Join(inside, "0")} {
		_, err := os.Lstat(path)
		if err == nil {
			t.Errorf("the refused staging created %s", path)
		}
	}
}

func assertContent(t *testing.T, path, want string) {
	t.Helper()

	got, err := os.ReadFile(path) //nolint:gosec // a test fixture
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != want {
		t.Errorf("%s holds %q, want %q", filepath.Base(path), got, want)
	}
}

func TestInvocation(t *testing.T) {
	t.Parallel()

	target := "/var/vcap/data/jobs/valkey/abc/config/server.crt"

	program, argv := certscripts.Invocation("patch", target, testTaskID)
	if program != "sudo -n /bin/bash -s --" {
		t.Errorf("program = %q", program)
	}

	want := []string{"patch", target, testTaskID}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv = %q, want %q", argv, want)
	}
}

func TestParseLines(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{"path": true, "hash": true, "state": true}
	out := []byte("path=/var/vcap/data/jobs/valkey/abc/x hash=abc123\n\nstate=ok\n")

	got, err := certscripts.ParseLines(out, allowed)
	if err != nil {
		t.Fatalf("ParseLines: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2: %v", len(got), got)
	}

	if got[0]["path"] != "/var/vcap/data/jobs/valkey/abc/x" || got[0]["hash"] != "abc123" || got[1]["state"] != "ok" {
		t.Errorf("parsed %v", got)
	}

	empty, err := certscripts.ParseLines(nil, allowed)
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
	}

	for name, out := range bad {
		got, err := certscripts.ParseLines([]byte(out), allowed)
		if err == nil {
			t.Errorf("%s: ParseLines accepted %q as %v", name, out, got)

			continue
		}

		if !errors.Is(err, certscripts.ErrMalformedOutput) {
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

// scriptNames lists the scripts in this directory without their .sh
// suffix. The embed pattern is *.sh in the same directory, so this is the
// set of embedded files.
func scriptNames(t *testing.T) []string {
	t.Helper()

	paths, err := filepath.Glob("*.sh")
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(paths))
	for _, path := range paths {
		names = append(names, strings.TrimSuffix(path, ".sh"))
	}

	return names
}

// readSource reads a script file from this directory.
func readSource(t *testing.T, name string) string {
	t.Helper()

	body, err := os.ReadFile(name + ".sh") //nolint:gosec // a file in this package
	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}

func TestScriptsContainNoTee(t *testing.T) {
	t.Parallel()

	tee := regexp.MustCompile(`(^|[^A-Za-z0-9_-])tee([^A-Za-z0-9_-]|$)`)
	if !tee.MatchString("cat x | tee y") || tee.MatchString("guarantee") {
		t.Fatal("the tee pattern is wrong")
	}

	names := scriptNames(t)
	if len(names) == 0 {
		t.Fatal("no scripts were found")
	}

	// Script returns lib.sh in front of every other script, so checking
	// each joined text covers every embedded file, and lib.sh is checked
	// from its source in case it is the only one so far.
	texts := map[string]string{libScript: readSource(t, libScript)}

	for _, name := range names {
		if name == libScript {
			continue
		}

		text, err := certscripts.Script(name)
		if err != nil {
			t.Fatalf("Script(%s): %v", name, err)
		}

		texts[name] = text
	}

	for name, text := range texts {
		for index, line := range strings.Split(text, "\n") {
			if tee.MatchString(line) {
				t.Errorf("%s: line %d uses tee", name, index+1)
			}
		}
	}
}

func TestScriptUnknownName(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"nope", "lib", "", "../lib", "lib.sh"} {
		body, err := certscripts.Script(name)
		if err == nil {
			t.Errorf("Script(%q) returned no error", name)

			continue
		}

		if !errors.Is(err, certscripts.ErrUnknownScript) {
			t.Errorf("Script(%q) error %v is not ErrUnknownScript", name, err)
		}

		if body != "" {
			t.Errorf("Script(%q) returned text with its error", name)
		}

		if !strings.Contains(err.Error(), fmt.Sprintf("%q", name)) {
			t.Errorf("Script(%q) error %q does not name the script", name, err)
		}
	}
}

func TestScriptJoinsLib(t *testing.T) {
	t.Parallel()

	lib := readSource(t, libScript)
	if !strings.HasSuffix(lib, "\n") {
		t.Error("lib.sh does not end with a newline, so it would run into the next script")
	}

	for _, name := range scriptNames(t) {
		if name == libScript {
			continue
		}

		body, err := certscripts.Script(name)
		if err != nil {
			t.Errorf("Script(%s): %v", name, err)

			continue
		}

		if body != lib+readSource(t, name) {
			t.Errorf("Script(%s) is not lib.sh followed by the script", name)
		}
	}
}
