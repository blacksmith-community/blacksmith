package yamlguard

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const graftPrefix = "github.com/fivetwenty-io/graft/"

// allowance says which banned import paths one file may use, and why.
type allowance struct {
	reason  string
	imports map[string]bool
}

// allowed maps each file that may import a YAML library, by its slash
// separated path from the module root, to the import paths it may use.
// Any other banned import from that file still fails.
var allowed = map[string]allowance{
	"internal/parity/scalars.go": {
		reason: "the parity harness needs yaml.v3 scalar styles, which graft does not export",
		imports: map[string]bool{
			"go.yaml.in/yaml/v2": true,
			"gopkg.in/yaml.v3":   true,
		},
	},
}

// allowedRequires are the direct requirements that stay for the allowed file.
var allowedRequires = map[string]bool{
	"go.yaml.in/yaml/v2": true,
	"gopkg.in/yaml.v3":   true,
}

func bannedImport(path string) bool {
	if strings.HasPrefix(path, graftPrefix) {
		return false
	}

	if strings.Contains(strings.ToLower(path), "yaml") {
		return true
	}

	for _, prefix := range []string{"github.com/goccy/", "github.com/geofffranks/", "github.com/smallfish/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}

	return false
}

func moduleRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	for {
		_, err = os.Stat(filepath.Join(dir, "go.mod"))
		if err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found above the test directory")
		}

		dir = parent
	}
}

// offendingImports returns "file: import" lines for every banned import
// under root that the allowlist does not permit for its file, a line for
// each Go file that does not parse, and a line per allowlisted file that
// is gone. A file that does not parse is reported and the walk goes on.
func offendingImports(root string, allowlist map[string]allowance) ([]string, error) {
	var problems []string

	seen := map[string]bool{}

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			switch entry.Name() {
			case "vendor", "testdata", ".git":
				return filepath.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		rel = filepath.ToSlash(rel)
		seen[rel] = true

		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			problems = append(problems, rel+" does not parse")

			return nil
		}

		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}

			if !bannedImport(importPath) || allowlist[rel].imports[importPath] {
				continue
			}

			problems = append(problems, rel+" imports "+importPath)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	for rel, allowed := range allowlist {
		if !seen[rel] {
			problems = append(problems, "allowlisted file "+rel+" no longer exists ("+allowed.reason+")")
		}
	}

	sort.Strings(problems)

	return problems, nil
}

// directRequires returns the module paths in go.mod text that carry no
// "// indirect" comment, from require blocks and single-line requires.
func directRequires(goMod string) []string {
	var modules []string

	inBlock := false

	for _, line := range strings.Split(goMod, "\n") {
		code, comment, _ := strings.Cut(line, "//")
		code = strings.TrimSpace(code)
		indirect := strings.HasPrefix(strings.TrimSpace(comment), "indirect")

		switch {
		case inBlock && code == ")":
			inBlock = false
		case inBlock:
			if fields := strings.Fields(code); len(fields) > 0 && !indirect {
				modules = append(modules, fields[0])
			}
		case strings.HasPrefix(code, "require") && strings.HasSuffix(code, "("):
			inBlock = true
		case strings.HasPrefix(code, "require "):
			if fields := strings.Fields(code); len(fields) > 1 && !indirect {
				modules = append(modules, fields[1])
			}
		}
	}

	return modules
}

func offendingRequires(goMod string) []string {
	var problems []string

	for _, module := range directRequires(goMod) {
		if bannedImport(module) && !allowedRequires[module] {
			problems = append(problems, "go.mod requires "+module+" directly")
		}
	}

	return problems
}

func TestOnlyGraftTouchesYAML(t *testing.T) {
	t.Parallel()

	problems, err := offendingImports(moduleRoot(t), allowed)
	if err != nil {
		t.Fatal(err)
	}

	if len(problems) > 0 {
		t.Errorf("YAML library imports are only allowed from graft; internal/parity/scalars.go alone may import go.yaml.in/yaml/v2 and gopkg.in/yaml.v3\n  %s",
			strings.Join(problems, "\n  "))
	}
}

func TestNoDirectYAMLRequirements(t *testing.T) {
	t.Parallel()

	text, err := os.ReadFile(filepath.Join(moduleRoot(t), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}

	problems := offendingRequires(string(text))
	if len(problems) > 0 {
		t.Errorf("go.mod may require only graft for YAML, besides go.yaml.in/yaml/v2 and gopkg.in/yaml.v3 for internal/parity/scalars.go\n  %s",
			strings.Join(problems, "\n  "))
	}
}

func TestBannedImport(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"github.com/fivetwenty-io/graft/pkg/yaml":       false,
		"github.com/fivetwenty-io/graft/pkg/graft":      false,
		"github.com/fivetwenty-io/graft":                false,
		"github.com/fivetwenty-io/graftyaml":            true,
		"gopkg.in/yaml.v3":                              true,
		"gopkg.in/YAML.v3":                              true,
		"go.yaml.in/yaml/v2":                            true,
		"sigs.k8s.io/yaml":                              true,
		"github.com/goccy/go-yaml":                      true,
		"github.com/goccy/go-json":                      true,
		"github.com/geofffranks/spruce":                 true,
		"github.com/geofffranks/simpleyaml":             true,
		"github.com/smallfish/simpleyaml":               true,
		"github.com/smallfish/other":                    true,
		"encoding/json":                                 false,
		"github.com/cloudfoundry-community/go-cfclient": false,
	}

	for path, want := range cases {
		if got := bannedImport(path); got != want {
			t.Errorf("bannedImport(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestDirectRequires(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		goMod string
		want  []string
	}{
		"single-line require": {
			goMod: "module x\n\nrequire github.com/a/b v1\n",
			want:  []string{"github.com/a/b"},
		},
		"single-line indirect require": {
			goMod: "module x\n\nrequire github.com/a/b v1 // indirect\n",
		},
		"block require": {
			goMod: "module x\n\nrequire (\n\tgithub.com/a/b v1\n\tgithub.com/c/d v2\n)\n",
			want:  []string{"github.com/a/b", "github.com/c/d"},
		},
		"indirect with a space": {
			goMod: "module x\n\nrequire (\n\tgithub.com/a/b v1 // indirect\n\tgithub.com/c/d v2\n)\n",
			want:  []string{"github.com/c/d"},
		},
		"indirect without a space": {
			goMod: "module x\n\nrequire (\n\tgithub.com/a/b v1 //indirect\n)\n",
		},
		"indirect with trailing text": {
			goMod: "module x\n\nrequire (\n\tgithub.com/a/b v1 // indirect; needed by c\n)\n",
		},
		"a comment that is not indirect": {
			goMod: "module x\n\nrequire (\n\tgithub.com/a/b v1 // pinned for c\n)\n",
			want:  []string{"github.com/a/b"},
		},
		"a comment line in a block": {
			goMod: "module x\n\nrequire (\n\t// a note\n\tgithub.com/a/b v1\n)\n",
			want:  []string{"github.com/a/b"},
		},
		"a block after the single line": {
			goMod: "module x\n\nrequire github.com/a/b v1\n\nrequire (\n\tgithub.com/c/d v2\n)\n\nreplace github.com/e/f => ../f\n",
			want:  []string{"github.com/a/b", "github.com/c/d"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := directRequires(tc.goMod)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("directRequires = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOffendingImportsWalk(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	files := map[string]string{
		"go.mod":                     "module x\n",
		"good/good.go":               "package good\n\nimport (\n\t\"fmt\"\n\n\t\"github.com/fivetwenty-io/graft/pkg/yaml\"\n)\n",
		"bad/bad.go":                 "package bad\n\nimport y \"gopkg.in/yaml.v3\"\n",
		"vendor/v/v.go":              "package v\n\nimport \"github.com/goccy/go-yaml\"\n",
		"broken/broken.go":           "package broken\n\nimport (\n",
		"internal/parity/scalars.go": "package parity\n\nimport (\n\t\"go.yaml.in/yaml/v2\"\n\t\"gopkg.in/yaml.v3\"\n\t\"github.com/goccy/go-yaml\"\n)\n",
	}

	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))

		err := os.MkdirAll(filepath.Dir(path), 0o750)
		if err != nil {
			t.Fatal(err)
		}

		err = os.WriteFile(path, []byte(content), 0o600)
		if err != nil {
			t.Fatal(err)
		}
	}

	allowlist := map[string]allowance{
		"internal/parity/scalars.go": allowed["internal/parity/scalars.go"],
		"gone/gone.go":               {reason: "a file that was removed"},
	}

	problems, err := offendingImports(root, allowlist)
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"allowlisted file gone/gone.go no longer exists (a file that was removed)",
		"bad/bad.go imports gopkg.in/yaml.v3",
		"broken/broken.go does not parse",
		"internal/parity/scalars.go imports github.com/goccy/go-yaml",
	}

	if strings.Join(problems, "|") != strings.Join(want, "|") {
		t.Errorf("problems = %q, want %q", problems, want)
	}
}

func TestRequireParsing(t *testing.T) {
	t.Parallel()

	goMod := "module x\n\nrequire github.com/a/b v1 // indirect\nrequire github.com/goccy/go-yaml v1\n\nrequire (\n\tgithub.com/c/d v2\n\tsigs.k8s.io/yaml v1 // indirect\n\t// comment\n\tgopkg.in/yaml.v2 v2 //indirect\n\tgopkg.in/yaml.v3 v3\n)\n"

	got := strings.Join(offendingRequires(goMod), "|")
	if got != "go.mod requires github.com/goccy/go-yaml directly" {
		t.Errorf("unexpected problems %q", got)
	}
}
