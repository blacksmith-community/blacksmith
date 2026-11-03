package parity_test

import (
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

	cases := map[string]struct{ want, got, path string }{
		"int to float":      {"a:\n  n: 3\n", "a:\n  n: 3.0\n", "$.a.n"},
		"date lost quote":   {"d: \"2026-01-02\"\n", "d: 2026-01-02\n", "$.d"},
		"yes lost quote":    {"v: \"yes\"\n", "v: yes\n", "$.v"},
		"null versus text":  {"v: ~\n", "v: \"null\"\n", "$.v"},
		"missing key":       {"a: 1\nb: 2\n", "a: 1\n", "$.b"},
		"extra key":         {"a: 1\n", "a: 1\nb: 2\n", "$.b"},
		"sequence order":    {"l: [a, b]\n", "l: [b, a]\n", "$.l["},
		"dotted key":        {"a:\n  b: 1\n", "\"a.b\": 1\n", "$"},
		"changed value":     {"a: x\n", "a: y\n", "$.a"},
		"sequence length":   {"l: [a]\n", "l: [a, b]\n", "$.l"},
		"merged value":      {"b: &x {k: 1}\nc:\n  <<: *x\n", "b: &x {k: 1}\nc:\n  <<: *x\n  k: 2\n", "$.c"},
		"dotted in nesting": {"a:\n  b.c: 1\n", "a:\n  b:\n    c: 1\n", "$.a"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			differences := parity.YAMLDifferences([]byte(tc.want), []byte(tc.got))
			if len(differences) == 0 {
				t.Fatalf("no difference reported")
			}

			for _, difference := range differences {
				if !strings.Contains(difference, tc.path) {
					t.Errorf("difference %q does not name %s", difference, tc.path)
				}
			}
		})
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
