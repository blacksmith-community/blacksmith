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

// allowed maps each file that may import a YAML library, by its slash
// separated path from the module root, to the reason it may.
var allowed = map[string]string{
	"internal/parity/scalars.go": "the parity harness needs yaml.v3 scalar styles, which graft does not export",
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
// under root that is not allowed, plus a line per allowed file that is gone.
func offendingImports(t *testing.T, root string) []string {
	t.Helper()

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
			return err
		}

		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}

			if !bannedImport(importPath) {
				continue
			}

			if _, ok := allowed[rel]; ok {
				continue
			}

			problems = append(problems, rel+" imports "+importPath)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for rel, reason := range allowed {
		if !seen[rel] {
			problems = append(problems, "allowlisted file "+rel+" no longer exists ("+reason+")")
		}
	}

	sort.Strings(problems)

	return problems
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

	problems := offendingImports(t, moduleRoot(t))
	if len(problems) > 0 {
		t.Errorf("YAML library imports are only allowed from graft; the sole allowlisted file is internal/parity/scalars.go\n  %s",
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

func TestRequireParsing(t *testing.T) {
	t.Parallel()

	goMod := "module x\n\nrequire github.com/a/b v1 // indirect\nrequire github.com/goccy/go-yaml v1\n\nrequire (\n\tgithub.com/c/d v2\n\tsigs.k8s.io/yaml v1 // indirect\n\t// comment\n\tgopkg.in/yaml.v2 v2 //indirect\n\tgopkg.in/yaml.v3 v3\n)\n"

	got := strings.Join(offendingRequires(goMod), "|")
	if got != "go.mod requires github.com/goccy/go-yaml directly" {
		t.Errorf("unexpected problems %q", got)
	}
}
