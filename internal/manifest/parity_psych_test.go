package manifest_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMarshaledJobsParityPsych reads the spruce-era and graft recordings of
// the jobs map credentials templates read, as the BOSH director does. The
// two recordings differ only in key order and indentation, so the
// comparison needs no exemption, and the count pins that.
func TestMarshaledJobsParityPsych(t *testing.T) {
	t.Parallel()

	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("ruby is not installed")
	}

	script, err := filepath.Abs(filepath.Join("..", "..", "hack", "parity", "psych-compare.rb"))
	if err != nil {
		t.Fatal(err)
	}

	args := []string{script, filepath.Join("testdata", "parity"), filepath.Join("testdata", "parity-graft")}

	out, err := exec.Command(ruby, args...).CombinedOutput() //nolint:gosec // a test runs its own script
	if err != nil {
		t.Fatalf("psych-compare found a difference: %s\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")

	want := "psych-compare: 0 difference(s), 0 exempt"
	if last := lines[len(lines)-1]; last != want {
		t.Errorf("last line %q, want %q", last, want)
	}
}

// pemAllowDiffs returns the --allow-diff flags for the values the redis tls
// and dual-mode renders read from PEM files that begin with an empty line.
// Spruce dropped that newline and graft keeps it, so each such value differs
// by design. Every file named holds all three values.
func pemAllowDiffs(files []string) []string {
	var args []string

	for _, file := range files {
		for _, path := range []string{"$.meta.redis.tls.ca", "$.meta.redis.tls.ca_cert", "$.meta.redis.tls.ca_key"} {
			args = append(args, "--allow-diff", file, path)
		}
	}

	return args
}

// redisPEMRenders lists the redis tls and dual-mode recordings of each plan,
// each with the suffixes given, relative to the recording directory.
func redisPEMRenders(suffixes ...string) []string {
	var files []string

	for _, variant := range []string{"dual-mode", "tls"} {
		for _, plan := range []string{"redis-standalone", "redis-standalone-6", "redis-standalone-7"} {
			for _, suffix := range suffixes {
				files = append(files, "redis/"+variant+"/"+plan+suffix+".yml")
			}
		}
	}

	return files
}

// runPsychCompare runs hack/parity/psych-compare.rb with args and returns its
// last line, failing the test on any difference.
func runPsychCompare(t *testing.T, args ...string) string {
	t.Helper()

	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("ruby is not installed")
	}

	script, err := filepath.Abs(filepath.Join("..", "..", "hack", "parity", "psych-compare.rb"))
	if err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(ruby, append([]string{script}, args...)...).CombinedOutput() //nolint:gosec // a test runs its own script
	if err != nil {
		t.Fatalf("psych-compare found a difference: %s\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")

	return lines[len(lines)-1]
}

// TestGenManifestParityPsych reads the spruce-era and graft recordings of
// every plan render, as the BOSH director does. Symbol quoting and the PEM
// values in the redis tls and dual-mode renders are the accepted
// differences, and the exempt counts pin how many there are.
func TestGenManifestParityPsych(t *testing.T) {
	t.Parallel()

	args := []string{"--allow", "symbol-quoting"}
	args = append(args, pemAllowDiffs(redisPEMRenders(".none", ".strings", ".tuned", ".types"))...)
	args = append(args, filepath.Join("testdata", "manifests"), filepath.Join("testdata", "manifests-graft"))

	want := "psych-compare: 0 difference(s), 86 exempt (symbol-quoting 14, allow-diff 72)"
	if last := runPsychCompare(t, args...); last != want {
		t.Errorf("last line %q, want %q", last, want)
	}
}

// TestCredentialsManifestParityPsych reads the spruce-era and graft
// recordings of every credentials render, as the BOSH director does. Symbol
// quoting and the PEM values in the redis tls and dual-mode renders are the
// accepted differences, and the exempt counts pin how many there are.
func TestCredentialsManifestParityPsych(t *testing.T) {
	t.Parallel()

	args := []string{"--allow", "symbol-quoting"}
	args = append(args, pemAllowDiffs(redisPEMRenders(""))...)
	args = append(args, filepath.Join("testdata", "credentials"), filepath.Join("testdata", "credentials-graft"))

	want := "psych-compare: 0 difference(s), 37 exempt (symbol-quoting 19, allow-diff 18)"
	if last := runPsychCompare(t, args...); last != want {
		t.Errorf("last line %q, want %q", last, want)
	}
}
