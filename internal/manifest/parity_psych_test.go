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
