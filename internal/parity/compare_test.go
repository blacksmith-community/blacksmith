package parity_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/parity"
)

func TestCompareYAMLMatches(t *testing.T) {
	t.Parallel()

	cases := map[string][2]string{
		"identical":  {"a: 1\nb: [x, y]\n", "a: 1\nb: [x, y]\n"},
		"re-indent":  {"a:\n  b: 1\n  c:\n    - x\n", "a:\n    b: 1\n    c:\n        - x\n"},
		"key order":  {"a: 1\nb: 2\nc: {d: 3, e: 4}\n", "c: {e: 4, d: 3}\nb: 2\na: 1\n"},
		"alias":      {"a: &x [1, 2]\nb: *x\n", "a: [1, 2]\nb: [1, 2]\n"},
		"quote kind": {"a: \"x\"\n", "a: 'x'\n"},
	}

	for name, pair := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			parity.CompareYAML(t, []byte(pair[0]), []byte(pair[1]))
		})
	}
}

func TestYAMLDifferencesReportsPaths(t *testing.T) {
	t.Parallel()

	// via is the check that must report the difference, and path must appear
	// in one of its lines.
	cases := map[string]struct{ want, got, via, path string }{
		"int to float":      {"a:\n  n: 3\n", "a:\n  n: 3.0\n", "tree", "$.a.n"},
		"date lost quote":   {"d: \"2026-01-02\"\n", "d: 2026-01-02\n", "tree", "$.d"},
		"yes lost quote":    {"v: \"yes\"\n", "v: yes\n", "decode", "$.v"},
		"null versus text":  {"v: ~\n", "v: \"null\"\n", "tree", "$.v"},
		"missing key":       {"a: 1\nb: 2\n", "a: 1\n", "tree", "$.b"},
		"extra key":         {"a: 1\n", "a: 1\nb: 2\n", "tree", "$.b"},
		"sequence order":    {"l: [a, b]\n", "l: [b, a]\n", "tree", "$.l[0]"},
		"dotted key":        {"a:\n  b: 1\n", "\"a.b\": 1\n", "tree", `$["a.b"]`},
		"changed value":     {"a: x\n", "a: y\n", "tree", "$.a"},
		"sequence length":   {"l: [a]\n", "l: [a, b]\n", "tree", "$.l"},
		"dotted in nesting": {"a:\n  b.c: 1\n", "a:\n  b:\n    c: 1\n", "tree", `$.a["b.c"]`},
		// The trees match, since pairs match by key and not by position, but
		// a key written before << loses to the merge and one after it wins.
		"merge order": {
			"b: &x {k: 1}\nc: {k: 2, <<: *x}\n",
			"b: &x {k: 1}\nc: {<<: *x, k: 2}\n",
			"decode", "$.c.k",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			differences := parity.YAMLDifferences([]byte(tc.want), []byte(tc.got))
			if len(differences) == 0 {
				t.Fatalf("no difference reported")
			}

			named := false

			for _, difference := range differences {
				if strings.Contains(difference, tc.path) {
					named = true
				}

				if tc.via == "decode" && strings.HasPrefix(difference, "tree") {
					t.Errorf("the tree check reported %q, but only the decode check should", difference)
				}
			}

			if !named {
				t.Errorf("no difference names %s: %v", tc.path, differences)
			}

			if !strings.HasPrefix(differences[0], tc.via) && tc.via == "decode" {
				t.Errorf("want the %s check to report first, got %v", tc.via, differences)
			}
		})
	}
}

func TestYAMLDifferencesPinsMergeKeyAgainstExpandedForm(t *testing.T) {
	t.Parallel()

	differences := parity.YAMLDifferences(
		[]byte("b: &x {k: 1}\nc:\n  <<: *x\n"),
		[]byte("b: {k: 1}\nc: {k: 1}\n"),
	)
	if len(differences) == 0 {
		t.Fatal("a merge key and its expanded form are reported as different today")
	}
}

func TestYAMLDifferencesTreatsSameNaNAsEqual(t *testing.T) {
	t.Parallel()

	if differences := parity.YAMLDifferences([]byte("a: .nan\n"), []byte("a: .nan\n")); len(differences) != 0 {
		t.Fatalf("want no difference, got %v", differences)
	}
}

type recorder struct {
	testing.TB

	errors []string
}

func (r *recorder) Helper() {}

func (r *recorder) Error(args ...any) { r.errors = append(r.errors, fmt.Sprint(args...)) }

func TestCompareYAMLFailsTheTestOnADifference(t *testing.T) {
	t.Parallel()

	fake := &recorder{TB: t}

	parity.CompareYAMLTB(fake, []byte("a: 1\n"), []byte("a: 2\n"))

	if len(fake.errors) == 0 || !strings.Contains(fake.errors[0], "$.a") {
		t.Fatalf("want an error naming $.a, got %v", fake.errors)
	}
}

func TestYAMLDifferencesEscapesDottedKeys(t *testing.T) {
	t.Parallel()

	differences := parity.YAMLDifferences([]byte("a.b: 1\n"), []byte("a.b: 2\n"))
	if len(differences) == 0 || !strings.Contains(differences[0], `$["a.b"]`) {
		t.Fatalf("want a bracketed path, got %v", differences)
	}
}

func TestYAMLDifferencesRejectsBadYAML(t *testing.T) {
	t.Parallel()

	if len(parity.YAMLDifferences([]byte("a: [\n"), []byte("a: 1\n"))) == 0 {
		t.Fatal("a document that does not parse must be a difference")
	}
}

// TestPsychCompareCatchesWhatTheTreeCheckCannot runs the Ruby backstop on
// the classes that the Go comparator leaves to it.
func TestPsychCompareCatchesWhatTheTreeCheckCannot(t *testing.T) {
	t.Parallel()

	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("ruby is not installed")
	}

	script, err := filepath.Abs("../../hack/parity/psych-compare.rb")
	if err != nil {
		t.Fatal(err)
	}

	allowSymbols := []string{"--allow", "symbol-quoting"}
	allowKeys := []string{"--allow", "stringified-keys"}
	allowY := []string{"--allow", "y-strings"}

	cases := map[string]struct {
		want, got string
		args      []string
		exit      int
	}{
		"sexagesimal":                 {"a: \"1:20\"\n", "a: 1:20\n", nil, 1},
		"plain bool key":              {"\"yes\": 1\n", "yes: 1\n", nil, 1},
		"load failures":               {"a: [\n", "b: [\n", nil, 1},
		"same nan":                    {"a: .nan\n", "a: .nan\n", nil, 0},
		"identical":                   {"a: 1:20\n", "a: 1:20\n", nil, 0},
		"one load failure":            {"a: 1\n", "a: [\n", nil, 1},
		"symbol quoting exempt":       {"a: :x\n", "a: \":x\"\n", allowSymbols, 0},
		"symbol quoting off":          {"a: :x\n", "a: \":x\"\n", nil, 1},
		"symbol to other text":        {"a: :x\n", "a: \":y\"\n", allowSymbols, 1},
		"symbol to text without sign": {"a: :x\n", "a: x\n", allowSymbols, 1},
		"string to symbol":            {"a: \":x\"\n", "a: :x\n", allowSymbols, 1},
		"integer key exempt":          {"1: one\n", "\"1\": one\n", allowKeys, 0},
		"integer key off":             {"1: one\n", "\"1\": one\n", nil, 1},
		"integer key other value":     {"1: one\n", "\"1\": two\n", allowKeys, 1},
		"integer key other name":      {"1: one\n", "\"2\": one\n", allowKeys, 1},
		"y exempt":                    {"a: true\n", "a: \"y\"\n", allowY, 0},
		"true for n":                  {"a: true\n", "a: \"n\"\n", allowY, 1},
		"false for y":                 {"a: false\n", "a: \"y\"\n", allowY, 1},
		"false for n":                 {"a: false\n", "a: \"n\"\n", allowY, 0},
		"float key not exempt":        {"1.5: one\n", "\"1.5\": one\n", allowKeys, 1},
		"bool key not exempt":         {"true: one\n", "\"true\": one\n", allowKeys, 1},
		"allow-diff covers its path":  {"a: 1\nb: 2\n", "a: x\nb: 2\n", []string{"--allow-diff", "want.yml", "$.a"}, 0},
		"allow-diff misses a path":    {"a: 1\nb: 2\n", "a: x\nb: y\n", []string{"--allow-diff", "want.yml", "$.a"}, 1},
		"allow-diff matches nothing":  {"a: 1\n", "a: 1\n", []string{"--allow-diff", "want.yml", "$.a"}, 1},
		"allow-diff other file":       {"a: 1\n", "a: x\n", []string{"--allow-diff", "other.yml", "$.a"}, 1},
		"allow-diff longer path":      {"ab: 1\n", "ab: x\n", []string{"--allow-diff", "want.yml", "$.a"}, 1},
		"skip both errors":            {"ERROR: a\n", "ERROR: b\n", []string{"--skip-error-file", "want.yml=error text"}, 0},
		"skip one error":              {"ERROR: a\n", "a: 1\n", []string{"--skip-error-file", "want.yml=error text"}, 1},
		"skip no error":               {"a: 1\n", "a: 2\n", []string{"--skip-error-file", "want.yml=error text"}, 1},
		"y off":                       {"a: true\n", "a: \"y\"\n", nil, 1},
		"y for other text":            {"a: true\n", "a: \"x\"\n", allowY, 1},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			want, got := filepath.Join(dir, "want.yml"), filepath.Join(dir, "got.yml")

			for path, text := range map[string]string{want: tc.want, got: tc.got} {
				if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			out, err := exec.Command(ruby, append(append([]string{script}, tc.args...), want, got)...).CombinedOutput() //nolint:gosec // a test runs its own script
			code := 0

			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				code = exitErr.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}

			if code != tc.exit {
				t.Errorf("exit %d, want %d:\n%s", code, tc.exit, out)
			}
		})
	}
}

// TestRenderForgesScanFlagsNonStringKeys shows that the fixture scan fails on
// a plain mapping key that YAML 1.1 reads as something other than a string,
// and passes on the committed fixtures.
func TestRenderForgesScanFlagsNonStringKeys(t *testing.T) {
	t.Parallel()

	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("ruby is not installed")
	}

	script, err := filepath.Abs("../../hack/parity/render-forges.rb")
	if err != nil {
		t.Fatal(err)
	}

	fixtures, err := filepath.Abs("testdata/forges")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	body := "1: a\nyes: b\n~: c\n1.5: d\n2026-01-02: e\n!!int 5: f\n? [x, y]\n: g\n" +
		"\"3\": quoted\n!!str 6: tagged string\nname: plain\n"

	err = os.WriteFile(filepath.Join(dir, "bad.yml"), []byte(body), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(ruby, script, "--scan", dir).CombinedOutput() //nolint:gosec // a test runs its own script
	if err == nil {
		t.Fatalf("the scan passed a directory with non-string keys:\n%s", out)
	}

	for _, key := range []string{".1", ".yes", ".~", ".1.5", ".2026-01-02", ".5", ".(complex key)"} {
		if !strings.Contains(string(out), "non-string key "+key+"\n") {
			t.Errorf("the scan did not flag %s:\n%s", key, out)
		}
	}

	for _, key := range []string{".3", ".6", ".name"} {
		if strings.Contains(string(out), "non-string key "+key+"\n") {
			t.Errorf("the scan flagged the string key %s:\n%s", key, out)
		}
	}

	out, err = exec.Command(ruby, script, "--scan", fixtures).CombinedOutput() //nolint:gosec // a test runs its own script
	if err != nil {
		t.Fatalf("the committed fixtures are not clean:\n%s", out)
	}
}
