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
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/certpatch"
	"blacksmith/internal/ops/scripts"
	"blacksmith/internal/upgrade/certscripts"
)

const testTaskID = "0f1e2d3c-4b5a-4968-8776-655443322110"

// certLibScript is the name of the certificate-only library among the
// scripts. It is joined in front of every script and is never run on its
// own.
const certLibScript = "certlib"

// sourceLibs is the shell text that loads both libraries in the order
// Script joins them. The base library lives in ops/scripts, so a test in
// this directory reaches it by a relative path.
const sourceLibs = "source ../../ops/scripts/lib.sh; source certlib.sh\n"

// libText returns what Script puts in front of every script, which is the
// base library followed by certlib.sh.
func libText(t *testing.T) string {
	t.Helper()

	return string(scripts.Library()) + readSource(t, certLibScript)
}

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
	out, err := exec.CommandContext(ctx, "bash", "-c", sourceLibs+`norm_hash "$1"`, "_", path).Output()
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

// TestNormalisedHashParityWithBash shows that bash and Go agree on the
// normalised hash and the raw hash of every fixture.
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

		//nolint:gosec // fixed program text; the path is a test fixture
		raw, err := exec.CommandContext(t.Context(), "bash", "-c", sourceLibs+`raw_hash "$1"`, "_", path).Output()
		if err != nil {
			t.Fatalf("raw_hash on %s: %v", filepath.Base(path), err)
		}

		if got, want := strings.TrimSpace(string(raw)), certpatch.RawHash(content); got != want {
			t.Errorf("%s: bash raw_hash = %q, Go RawHash = %q", filepath.Base(path), got, want)
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

// TestStageCommandExact shows that staging runs patch.sh's stage
// operation through the same program as every other operation, so the
// script's own checks run before it writes.
func TestStageCommandExact(t *testing.T) {
	t.Parallel()

	path := "/var/vcap/data/certpatch/" + testTaskID + "/s/0"
	want := "sudo -n /bin/bash -s -- 'stage' '" + testTaskID + "' '" + path + "'"

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
		"/var/vcap/data/certpatch/" + testTaskID + "/b/0",
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

	stage, err := certscripts.StageCommand("/var/vcap/data/certpatch/"+testTaskID+"/s/12", testTaskID)
	if err != nil {
		t.Fatal(err)
	}

	program, argv := scripts.Invocation("patch", pinnedTarget, testTaskID, strings.Repeat("0123456789abcdef", 4))

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

// runStage runs the command StageCommand builds for path in the tree at
// root, without its sudo prefix, because the container already runs as
// root and has no sudo. stdin is what StageInput builds for content. It
// returns the exit code. A command that blocks, as a write to a FIFO
// would, fails the test when its context ends.
func runStage(t *testing.T, root, path, content string) int {
	t.Helper()

	command, err := certscripts.StageCommand(path, testTaskID)
	if err != nil {
		t.Fatal(err)
	}

	program, found := strings.CutPrefix(command, "sudo -n ")
	if !found {
		t.Fatal("the staging command does not start with sudo -n")
	}

	stdin, err := certscripts.StageInput(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var stderr strings.Builder

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", program) //nolint:gosec // the command under test
	cmd.Env = []string{"CERTPATCH_ROOT=" + root}
	cmd.Stdin = stdin
	cmd.Stderr = &stderr

	err = cmd.Run()

	if ctx.Err() != nil {
		t.Fatalf("staging %s blocked until its deadline", path)
	}

	if err == nil {
		return 0
	}

	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("staging %s: %v", path, err)
	}

	t.Logf("staging %s exited %d; stderr: %s", path, exit.ExitCode(), stderr.String())

	return exit.ExitCode()
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

func TestStageCommandWritesNewFile(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)

	if code := runStage(t, tree.root, stagePath, stagedText); code != 0 {
		t.Fatalf("staging a new name exited %d, want 0", code)
	}

	tree.assert(t, stagePath, stagedText)
	tree.assertOwnerMode(t, stagePath, 0, 0o600)

	// The content is data, never script, even when it reads as shell.
	planted := tree.at("/planted")
	content := "staged\ntouch " + planted + "\nexit 3\n"
	next := patchTask + "/s/1"

	if code := runStage(t, tree.root, next, content); code != 0 {
		t.Fatalf("staging shell text exited %d, want 0", code)
	}

	tree.assert(t, next, content)

	if tree.exists(t, "/planted") {
		t.Error("the staged content ran as a command")
	}

	assertOnlyStaged(t, tree, "0", "1")
}

// assertOnlyStaged checks that the task's s directory holds the named
// files and nothing else, so no temporary file was left behind.
func assertOnlyStaged(t *testing.T, tree *vmTree, names ...string) {
	t.Helper()

	entries, err := os.ReadDir(tree.at(patchTask + "/s"))
	if err != nil {
		t.Fatal(err)
	}

	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}

	if strings.Join(got, " ") != strings.Join(names, " ") {
		t.Errorf("the s directory holds %v, want %v", got, names)
	}
}

func TestStageCommandRefusesExisting(t *testing.T) {
	t.Parallel()

	tree := newPatchTree(t)
	tree.mkdir(t, "/etc")
	tree.write(t, "/etc/target", "target\n")

	existing := patchTask + "/s/1"
	tree.write(t, existing, "other\n")

	link := patchTask + "/s/2"
	symlink(t, tree.at("/etc/target"), tree.at(link))

	dangling := patchTask + "/s/3"
	symlink(t, tree.at("/etc/absent"), tree.at(dangling))

	// set -C would refuse only an existing regular file, so a FIFO would
	// hand the staged bytes to whoever reads it, and a symlink to
	// /dev/null would swallow them while staging reported success.
	fifo := patchTask + "/s/4"

	err := syscall.Mkfifo(tree.at(fifo), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	devNull := patchTask + "/s/5"
	symlink(t, "/dev/null", tree.at(devNull))

	sub := patchTask + "/s/6"
	tree.mkdir(t, sub)

	for name, path := range map[string]string{
		"an existing file":    existing,
		"a symlink":           link,
		"a dangling symlink":  dangling,
		"a FIFO":              fifo,
		"a link to /dev/null": devNull,
		"a directory":         sub,
	} {
		if code := runStage(t, tree.root, path, stagedText); code != refusedExit {
			t.Errorf("staging onto %s exited %d, want 13", name, code)
		}
	}

	tree.assert(t, existing, "other\n")
	tree.assert(t, "/etc/target", "target\n")

	if tree.exists(t, "/etc/absent") {
		t.Error("staging through a dangling symlink created its target")
	}
}

func TestStageCommandRefusesUnprivateDirectory(t *testing.T) {
	t.Parallel()

	t.Run("an open s directory", func(t *testing.T) {
		t.Parallel()

		tree := newPatchTree(t)
		tree.chmod(t, patchTask+"/s", 0o755)

		if code := runStage(t, tree.root, stagePath, stagedText); code != refusedExit {
			t.Errorf("staging into a 0755 directory exited %d, want 13", code)
		}

		if tree.exists(t, stagePath) {
			t.Error("the refused staging wrote the file")
		}
	})

	t.Run("an s directory vcap owns", func(t *testing.T) {
		t.Parallel()

		tree := newPatchTree(t)
		tree.giveToVCAP(t, patchTask+"/s")

		if code := runStage(t, tree.root, stagePath, stagedText); code != refusedExit {
			t.Errorf("staging into a directory owned by vcap exited %d, want 13", code)
		}

		if tree.exists(t, stagePath) {
			t.Error("the refused staging wrote the file")
		}
	})

	t.Run("a symlinked s directory", func(t *testing.T) {
		t.Parallel()

		tree := newPatchTree(t)
		tree.mkdir(t, "/etc/inside")
		tree.chmod(t, "/etc/inside", 0o700)
		tree.replace(t, patchTask+"/s")
		symlink(t, tree.at("/etc/inside"), tree.at(patchTask+"/s"))

		if code := runStage(t, tree.root, stagePath, stagedText); code != refusedExit {
			t.Errorf("staging through a symlinked directory exited %d, want 13", code)
		}

		if tree.exists(t, "/etc/inside/0") {
			t.Error("the refused staging wrote through the symlink")
		}
	})
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

	program, argv := scripts.Invocation("patch", target, testTaskID)
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

// scriptNames lists the runnable scripts in this directory without their
// .sh suffix, which leaves out certlib. The embed pattern is *.sh in the
// same directory, so these and certlib are the embedded files.
func scriptNames(t *testing.T) []string {
	t.Helper()

	paths, err := filepath.Glob("*.sh")
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(paths))
	for _, path := range paths {
		name := strings.TrimSuffix(path, ".sh")
		if name != certLibScript {
			names = append(names, name)
		}
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

	// Script returns both libraries in front of every other script, so
	// checking each joined text covers every embedded file, and the
	// libraries are checked from their sources in case no script exists
	// yet.
	texts := map[string]string{
		"lib":         string(scripts.Library()),
		certLibScript: readSource(t, certLibScript),
	}

	for _, name := range names {
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

	for _, name := range []string{"nope", "lib", "certlib", "", "../lib", "lib.sh"} {
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

	for _, lib := range []string{string(scripts.Library()), readSource(t, certLibScript)} {
		if !strings.HasSuffix(lib, "\n") {
			t.Error("a library does not end with a newline, so it would run into the next script")
		}
	}

	lib := libText(t)

	for _, name := range scriptNames(t) {
		body, err := certscripts.Script(name)
		if err != nil {
			t.Errorf("Script(%s): %v", name, err)

			continue
		}

		if body != lib+readSource(t, name) {
			t.Errorf("Script(%s) is not lib.sh, certlib.sh, then the script", name)
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

	for _, line := range strings.Split(string(scripts.Library()), "\n") {
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

	for _, line := range strings.Split(string(scripts.Library()), "\n") {
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

	if runtime.GOOS != "linux" {
		return
	}

	// Sourcing lib.sh shows the values the scripts really see.
	out, err := exec.CommandContext(t.Context(), "bash", "-c",
		sourceLibs+`echo "$EXIT_USAGE $EXIT_DRIFT $EXIT_BACKUP $EXIT_RESTART $EXIT_REFUSED $EXIT_INVALID"`).Output()
	if err != nil {
		t.Fatalf("sourcing lib.sh: %v", err)
	}

	sourced := fmt.Sprintf("%d %d %d %d %d %d\n", scripts.ExitUsage, scripts.ExitDrift,
		scripts.ExitBackup, scripts.ExitRestart, scripts.ExitRefused, scripts.ExitInvalid)
	if string(out) != sourced {
		t.Errorf("sourced exit codes = %q, want %q", out, sourced)
	}
}
