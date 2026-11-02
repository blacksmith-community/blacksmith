package parity_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/parity"
)

func TestDumpWritesOneLinePerLeaf(t *testing.T) {
	t.Parallel()

	value := map[interface{}]interface{}{
		"name":  "redis",
		"count": 3,
		"ratio": 3.0,
		"on":    true,
		"none":  nil,
		"list":  []interface{}{"a", 1},
		"empty": map[string]interface{}{},
		5:       "numeric key",
		"nest":  map[string]interface{}{"inner": []interface{}{}},
	}

	want := "$.5(int)\tstring\t\"numeric key\"\n" +
		"$.count\tint\t3\n" +
		"$.empty\tmap[string]interface {}\t{}\n" +
		"$.list[0]\tstring\t\"a\"\n" +
		"$.list[1]\tint\t1\n" +
		"$.name\tstring\t\"redis\"\n" +
		"$.nest.inner\t[]interface {}\t[]\n" +
		"$.none\tinterface {}\tnull\n" +
		"$.on\tbool\ttrue\n" +
		"$.ratio\tfloat64\t3\n"

	if got := parity.Dump(value); got != want {
		t.Errorf("dump differs\n--- want\n%s\n--- got\n%s", want, got)
	}
}

func TestDumpTellsIntegralFloatFromInteger(t *testing.T) {
	t.Parallel()

	if parity.Dump(3) == parity.Dump(3.0) {
		t.Error("an int and an integral float dump the same")
	}
}

func TestDumpReadsStructsAndPointers(t *testing.T) {
	t.Parallel()

	type inner struct{ Value string }

	type outer struct {
		Name    string
		Inner   *inner
		Missing *inner
		hidden  string
	}

	got := parity.Dump(outer{Name: "x", Inner: &inner{Value: "y"}, hidden: "z"})
	want := "$.Name\tstring\t\"x\"\n$.Inner.Value\tstring\t\"y\"\n$.Missing\t*parity_test.inner\tnull\n"

	if got != want {
		t.Errorf("dump differs\n--- want\n%s\n--- got\n%s", want, got)
	}
}

func TestGoldenComparesWithTheFile(t *testing.T) {
	t.Parallel()

	if parity.Updating() {
		t.Skip("goldens are being rewritten")
	}

	parity.Golden(t, filepath.Join("testdata", "golden-check.txt"), "one\ntwo\n")
}

func TestFixturesCoverEveryForgeAndVariant(t *testing.T) {
	t.Parallel()

	counts := map[string]int{}

	for _, fixture := range parity.Fixtures(t) {
		counts[fixture.Forge]++

		if fixture.Name() != fixture.Forge+"/"+fixture.Variant {
			t.Errorf("name %q does not join forge and variant", fixture.Name())
		}

		if _, err := os.Stat(filepath.Join(fixture.ServicesDir(), "service.yml")); err != nil {
			t.Errorf("%s: %s", fixture.Name(), err)
		}
	}

	want := map[string]int{"kubernetes": 1, "mariadb": 1, "postgresql": 1, "rabbitmq": 5, "redis": 3, "valkey": 3}
	for forge, count := range want {
		if counts[forge] != count {
			t.Errorf("%s has %d fixtures, want %d", forge, counts[forge], count)
		}
	}
}

func TestRenderedManifestsSkipsFailures(t *testing.T) {
	t.Parallel()

	rendered := parity.RenderedManifests(t)
	if len(rendered) < 30 {
		t.Fatalf("found %d rendered manifests, want at least 30", len(rendered))
	}

	for _, manifest := range rendered {
		if strings.HasPrefix(manifest.Body, "ERROR:") || strings.Contains(manifest.Name, "/") {
			t.Errorf("unexpected rendered manifest %q", manifest.Name)
		}
	}
}
