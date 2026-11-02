package parity

import (
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// update rewrites the goldens a test compares against instead of failing
// when they differ.
var update = flag.Bool("update", false, "rewrite the parity goldens")

// Updating reports whether the test run rewrites goldens.
func Updating() bool { return *update }

const (
	goldenDirPermissions  = 0o755
	goldenFilePermissions = 0o644
)

// Golden compares got with the golden file at path, which is relative to the
// calling package, or rewrites the file when the test runs with -update. A
// missing golden fails the test and says how to create it.
func Golden(t *testing.T, path string, got string) {
	t.Helper()

	if *update {
		err := os.MkdirAll(filepath.Dir(path), goldenDirPermissions)
		if err != nil {
			t.Fatalf("create golden directory: %s", err)
		}

		err = os.WriteFile(path, []byte(got), goldenFilePermissions)
		if err != nil {
			t.Fatalf("write golden %s: %s", path, err)
		}

		return
	}

	want, err := os.ReadFile(path) //nolint:gosec // a test reads its own golden
	if err != nil {
		t.Fatalf("read golden %s: %s (run the test with -update to create it)", path, err)
	}

	if string(want) != got {
		t.Errorf("golden %s differs (run the test with -update to accept the change)\n--- want\n%s\n--- got\n%s", path, want, got)
	}
}

// Fixture is one rendered forge release with one set of properties.
type Fixture struct {
	Forge   string
	Variant string
	// Dir holds services/ and jobs/, and is the base path for file
	// operators.
	Dir string
}

// Name returns forge/variant.
func (f Fixture) Name() string { return f.Forge + "/" + f.Variant }

// ServicesDir returns the directory the broker reads as one service.
func (f Fixture) ServicesDir() string { return filepath.Join(f.Dir, "services") }

// FixtureRoot returns the directory holding the rendered forge trees.
func FixtureRoot() string {
	_, file, _, _ := runtime.Caller(0)

	return filepath.Join(filepath.Dir(file), "testdata", "forges")
}

// Fixtures lists every rendered forge tree, sorted by forge and variant.
func Fixtures(t *testing.T) []Fixture {
	t.Helper()

	root := FixtureRoot()

	forges, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read fixtures: %s", err)
	}

	var fixtures []Fixture

	for _, forge := range forges {
		variants, err := os.ReadDir(filepath.Join(root, forge.Name()))
		if err != nil {
			t.Fatalf("read fixtures for %s: %s", forge.Name(), err)
		}

		for _, variant := range variants {
			fixtures = append(fixtures, Fixture{
				Forge:   forge.Name(),
				Variant: variant.Name(),
				Dir:     filepath.Join(root, forge.Name(), variant.Name()),
			})
		}
	}

	sort.Slice(fixtures, func(i, j int) bool { return fixtures[i].Name() < fixtures[j].Name() })

	return fixtures
}

func globAll(dir string, patterns ...string) ([]string, error) {
	var found []string

	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return nil, err //nolint:wrapcheck // a pattern error names itself
		}

		found = append(found, matches...)
	}

	sort.Strings(found)

	return found, nil
}

// Relative replaces the fixture root in text with a placeholder, so a
// golden does not depend on where the repository is checked out.
func Relative(text string) string {
	return strings.ReplaceAll(text, FixtureRoot(), "<fixtures>")
}
