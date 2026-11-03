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

	cases := map[string]struct {
		want, got string
		exit      int
	}{
		"sexagesimal":      {"a: \"1:20\"\n", "a: 1:20\n", 1},
		"plain bool key":   {"\"yes\": 1\n", "yes: 1\n", 1},
		"load failures":    {"a: [\n", "b: [\n", 1},
		"same nan":         {"a: .nan\n", "a: .nan\n", 0},
		"identical":        {"a: 1:20\n", "a: 1:20\n", 0},
		"one load failure": {"a: 1\n", "a: [\n", 1},
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

			out, err := exec.Command(ruby, script, want, got).CombinedOutput() //nolint:gosec // a test runs its own script
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
