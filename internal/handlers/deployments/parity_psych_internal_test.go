package deployments

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/parity"
)

// TestUpdateManifestParityPsych reads the spruce-era and graft recordings of
// the manifest an update sends, as the BOSH director does. Only three
// differences are accepted. A colon-led string is quoted, so it is no
// longer a Symbol. A whole float is written as an integer. And 1e-06 is
// written as 1.0e-06, which Psych reads as a Float where it read the old
// text as a String. The exempt counts pin each, so a new difference of an
// accepted kind still fails the test.
func TestUpdateManifestParityPsych(t *testing.T) {
	t.Parallel()

	ruby := parity.Ruby(t)

	script, err := filepath.Abs(filepath.Join("..", "..", "..", "hack", "parity", "psych-compare.rb"))
	if err != nil {
		t.Fatal(err)
	}

	base := filepath.Join("testdata", "parity")
	args := []string{
		script,
		"--allow", "symbol-quoting",
		"--allow-diff", "numbers.yml", "$.properties.safe-max",
		"--allow-diff", "numbers.yml", "$.properties.small",
		filepath.Join(base, "update-manifest"), filepath.Join(base, "update-manifest-graft"),
	}

	out, err := exec.Command(ruby, args...).CombinedOutput() //nolint:gosec // a test runs its own script
	if err != nil {
		t.Fatalf("psych-compare found a difference: %s\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if last, want := lines[len(lines)-1], "psych-compare: 0 difference(s), 3 exempt (symbol-quoting 1, allow-diff 2)"; last != want {
		t.Errorf("last line %q, want %q", last, want)
	}
}
