package certscripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"blacksmith/internal/ops/scripts"
	"blacksmith/internal/upgrade/certscripts"
)

// beforeSplit is where the library and the staged scripts from before the
// split are kept, so the tests have the original bytes without git.
const beforeSplit = "testdata"

// stagedScripts names each script Script stages.
func stagedScripts() []string {
	return []string{"gate", "patch", "discover", "verify"}
}

// readBeforeSplit reads a file under testdata.
func readBeforeSplit(t *testing.T, path ...string) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(append([]string{beforeSplit}, path...)...)) //nolint:gosec // a fixture in this package
	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}

// codeLines returns the sorted lines of text that are neither blank nor
// comments, which is the multiset a split must keep whole.
func codeLines(text string) []string {
	var lines []string

	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		lines = append(lines, line)
	}

	slices.Sort(lines)

	return lines
}

// TestSplitKeepsEveryLibraryLine shows that the base library and certlib
// together hold the same code lines as the library before the split, so
// the split moved lines and never edited, added, or dropped one.
func TestSplitKeepsEveryLibraryLine(t *testing.T) {
	t.Parallel()

	want := codeLines(readBeforeSplit(t, "lib-before-split.sh"))
	got := codeLines(libText(t))

	if !slices.Equal(got, want) {
		t.Errorf("the split libraries hold %d code lines, the original %d; first difference: %s",
			len(got), len(want), firstDifference(got, want))
	}
}

// TestSplitMovesOnlyCertificateCode shows that certlib.sh holds the
// certificate-only code and the base library holds none of it.
func TestSplitMovesOnlyCertificateCode(t *testing.T) {
	t.Parallel()

	base := string(scripts.Library())
	cert := readSource(t, certLibScript)

	moved := []string{
		"CP_LOGICAL_RE=", "CP_PINNED_RE=", "CP_JOBDIR_RE=",
		"valid_logical()", "valid_pinned()", "valid_work()", "valid_work_unmade()",
		"_cp_valid_work()", "make_work_dir()",
	}
	for _, name := range moved {
		if strings.Contains(base, name) {
			t.Errorf("the base library still holds %s", name)
		}

		if !strings.Contains(cert, name) {
			t.Errorf("certlib.sh does not hold %s", name)
		}
	}

	kept := []string{
		"CP_TASK_RE=", "CP_HASH_RE=", "norm_hash()", "raw_hash()", "_cp_shaped()", "_cp_root()",
		"_cp_secure_dir()", "_cp_secure_chain()", "_cp_private_dir()", "_cp_make_private()",
		"valid_task()", "valid_hash()", "set -u -o pipefail", "umask 077",
	}
	for _, name := range kept {
		if !strings.Contains(base, name) {
			t.Errorf("the base library lost %s", name)
		}

		if strings.Contains(cert, name) {
			t.Errorf("certlib.sh holds %s, which belongs to the base library", name)
		}
	}
}

// TestBaseLibraryStandsAlone shows that the base library passes bash -n
// and sources under set -u with no certificate function defined, so
// nothing in it reads a certificate pattern at load time.
func TestBaseLibraryStandsAlone(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "lib.sh")

	err := os.WriteFile(path, scripts.Library(), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	out, err := exec.CommandContext(t.Context(), "bash", "-n", path).CombinedOutput() //nolint:gosec // a file the test wrote
	if err != nil {
		t.Fatalf("bash -n on the base library: %v\n%s", err, out)
	}

	probe := `set -u; source "$1" || exit 90
for fn in valid_logical valid_pinned valid_work valid_work_unmade make_work_dir; do
	if declare -F "$fn" > /dev/null; then echo "$fn is defined"; exit 91; fi
done
for var in CP_LOGICAL_RE CP_PINNED_RE CP_JOBDIR_RE; do
	if [ -n "${!var+x}" ]; then echo "$var is set"; exit 92; fi
done
valid_task 00000000-0000-0000-0000-000000000000 || exit 93
`

	out, err = exec.CommandContext(t.Context(), "bash", "-c", probe, "_", path).CombinedOutput() //nolint:gosec // fixed test program text
	if err != nil {
		t.Fatalf("sourcing the base library alone: %v\n%s", err, out)
	}
}

// TestCertLibPassesSyntaxCheck runs bash -n on certlib.sh.
func TestCertLibPassesSyntaxCheck(t *testing.T) {
	t.Parallel()

	out, err := exec.CommandContext(t.Context(), "bash", "-n", certLibScript+".sh").CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n on certlib.sh: %v\n%s", err, out)
	}
}

// TestStagedScriptsKeepEveryLine shows that each script Script stages
// holds the same code lines as the script staged before the split, so the
// only change is the order in which the library lines arrive.
func TestStagedScriptsKeepEveryLine(t *testing.T) {
	t.Parallel()

	for _, name := range stagedScripts() {
		staged, err := certscripts.Script(name)
		if err != nil {
			t.Errorf("Script(%s): %v", name, err)

			continue
		}

		want := codeLines(readBeforeSplit(t, "staged-before-split", name+".sh"))
		got := codeLines(staged)

		if !slices.Equal(got, want) {
			t.Errorf("Script(%s) holds %d code lines, the original %d; first difference: %s",
				name, len(got), len(want), firstDifference(got, want))
		}
	}
}

// TestStagedScriptsKeepTheirOwnLinesInOrder shows that, once the libraries
// in front are taken off, each script Script stages is the script staged
// before the split line for line, comments, blank lines, and order
// included. Only the library in front of it changed.
func TestStagedScriptsKeepTheirOwnLinesInOrder(t *testing.T) {
	t.Parallel()

	libs := string(scripts.Library()) + readSource(t, certLibScript)
	before := readBeforeSplit(t, "lib-before-split.sh")

	for _, name := range stagedScripts() {
		staged, err := certscripts.Script(name)
		if err != nil {
			t.Errorf("Script(%s): %v", name, err)

			continue
		}

		original := readBeforeSplit(t, "staged-before-split", name+".sh")

		own, cut := strings.CutPrefix(staged, libs)
		if !cut {
			t.Errorf("Script(%s) does not start with the base library and certlib.sh", name)

			continue
		}

		ownBefore, cut := strings.CutPrefix(original, before)
		if !cut {
			t.Errorf("the script %s staged before the split does not start with the library of the time", name)

			continue
		}

		got := strings.Split(own, "\n")
		want := strings.Split(ownBefore, "\n")

		if !slices.Equal(got, want) {
			t.Errorf("Script(%s) holds %d lines of its own, the original %d; first difference: %s",
				name, len(got), len(want), firstDifference(got, want))
		}
	}
}

// firstDifference names the first line at which two line lists differ.
func firstDifference(got, want []string) string {
	for index := range max(len(got), len(want)) {
		var left, right string

		if index < len(got) {
			left = got[index]
		}

		if index < len(want) {
			right = want[index]
		}

		if left != right {
			return "got " + left + ", want " + right
		}
	}

	return "none"
}
