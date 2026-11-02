package certinventory_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"blacksmith/internal/certinventory"
	"blacksmith/internal/parity"
)

// factsView turns what the walk found into values the dump can read. The
// literals hold parsed certificates, so each shows only the fields that
// identify it.
func factsView(facts *certinventory.ManifestFacts) map[string]interface{} {
	literals := make([]interface{}, 0, len(facts.Literals))

	for _, literal := range facts.Literals {
		literals = append(literals, map[string]interface{}{
			"path":      literal.Path,
			"group":     literal.Group,
			"subject":   literal.Cert.Subject.CommonName,
			"not_after": literal.Cert.NotAfter.UTC().Format("2006-01-02T15:04:05Z"),
			"is_ca":     literal.Cert.IsCA,
		})
	}

	jobs := make([]interface{}, 0, len(facts.Jobs))

	for _, job := range facts.Jobs {
		jobs = append(jobs, map[string]interface{}{
			"group":      job.Group,
			"job":        job.Job,
			"properties": job.Properties,
			"cert_refs":  job.CertRefs,
		})
	}

	return map[string]interface{}{
		"name":           facts.Name,
		"literals":       literals,
		"signing_cas":    facts.SigningCAs,
		"cert_variables": facts.CertVariables,
		"groups":         facts.Groups,
		"jobs":           jobs,
	}
}

func walkGolden(t *testing.T, name, manifest string) {
	t.Helper()

	golden := filepath.Join("testdata", "parity", name+".dump")

	facts, err := certinventory.WalkManifest(manifest)
	if err != nil {
		parity.Golden(t, golden, "ERROR: "+err.Error()+"\n")

		return
	}

	parity.Golden(t, golden, parity.Dump(factsView(facts)))
}

func TestWalkManifestParityOverRenderedManifests(t *testing.T) {
	t.Parallel()

	for _, rendered := range parity.RenderedManifests(t) {
		t.Run(rendered.Name, func(t *testing.T) {
			t.Parallel()

			walkGolden(t, "rendered_"+rendered.Name, rendered.Body)
		})
	}
}

func TestWalkManifestParityOverHandwrittenManifests(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob(filepath.Join("testdata", "manifests", "*.yml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("list the hand-written manifests: %v (%d found)", err, len(paths))
	}

	sort.Strings(paths)

	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".yml")

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			body, err := os.ReadFile(path) //nolint:gosec // a fixture file
			if err != nil {
				t.Fatalf("read %s: %s", path, err)
			}

			walkGolden(t, "handwritten_"+name, string(body))
		})
	}
}

func TestWalkManifestParityOverInvalidInput(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"empty":            "",
		"not-a-mapping":    "- one\n- two\n",
		"unterminated":     "name: [unterminated\n",
		"duplicate-keys":   "name: first\nname: second\n",
		"tab-indent":       "name: x\ninstance_groups:\n\t- name: web\n",
		"undefined-alias":  "name: *missing\n",
		"scalar-top-level": "just a string\n",
	}

	for name, manifest := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			walkGolden(t, "invalid_"+name, manifest)
		})
	}
}
