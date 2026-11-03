package blacksmith_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/parity"
)

// TestGetConfigMarshaledParityPsych reads the spruce-era and graft
// recordings of the marshaled config as the BOSH director does. The two
// have to read identically, so nothing is exempt.
func TestGetConfigMarshaledParityPsych(t *testing.T) {
	t.Parallel()

	ruby := parity.Ruby(t)

	script, err := filepath.Abs(filepath.Join("..", "..", "..", "hack", "parity", "psych-compare.rb"))
	if err != nil {
		t.Fatal(err)
	}

	base := "testdata"
	out, err := exec.Command(ruby, script, filepath.Join(base, "parity"), filepath.Join(base, "parity-graft")).CombinedOutput() //nolint:gosec // a test runs its own script
	if err != nil {
		t.Fatalf("psych-compare found a difference: %s\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if last, want := lines[len(lines)-1], "psych-compare: 0 difference(s), 0 exempt"; last != want {
		t.Errorf("last line %q, want %q", last, want)
	}
}
