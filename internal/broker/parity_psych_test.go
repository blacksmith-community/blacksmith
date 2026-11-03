package broker_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestYamlHelpersParityPsych reads the spruce-era and graft recordings of the
// files WriteYamlFile writes, and of the text yamlGsub marshals, as the BOSH
// director does. The exemptions are the accepted differences, and the exempt
// counts pin how many of each there are, so a new difference of an accepted
// kind still fails the test.
//
// The one document graft refuses (duplicate keys) has no file in the graft
// recording, so the spruce-era file for it is not in the comparison. It stays
// in write-yaml-file.txt. The explicit !!float tag over integer text now reads
// as a float, so the graft recording has a file for it, and the spruce-era
// recording holds it only in write-yaml-file.txt, so the comparison skips it.
func TestYamlHelpersParityPsych(t *testing.T) {
	t.Parallel()

	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("ruby is not installed")
	}

	script, err := filepath.Abs(filepath.Join("..", "..", "hack", "parity", "psych-compare.rb"))
	if err != nil {
		t.Fatal(err)
	}

	scalars := "write-yaml-file/odd_yaml11-scalars.yml"
	versions := "write-yaml-file/odd_release-versions.yml"

	args := []string{
		script,
		"--allow", "symbol-quoting", "--allow", "y-strings",
		"--allow-diff", scalars, "$.properties.numbers[0]",
		"--allow-diff", scalars, "$.properties.octal-ish[0]",
		"--allow-diff", scalars, "$.properties.octal-ish[1]",
		"--allow-diff", versions, "$.releases[6].version",
		"--allow-diff", versions, "$.releases[1].sha1",
		"--allow-diff", versions, "$.releases[0].sha1",
		"--skip-missing-want", "write-yaml-file/odd_float-tag-over-int.yml=the spruce-era recording holds it only in write-yaml-file.txt",
		filepath.Join("testdata", "parity"), filepath.Join("testdata", "parity-graft"),
	}

	out, err := exec.Command(ruby, args...).CombinedOutput() //nolint:gosec // a test runs its own script
	if err != nil {
		t.Fatalf("psych-compare found a difference: %s\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")

	want := "psych-compare: 0 difference(s), 24 exempt (symbol-quoting 15, allow-diff 6, y-strings 2, skip-missing-want 1)"
	if last := lines[len(lines)-1]; last != want {
		t.Errorf("last line %q, want %q", last, want)
	}
}
