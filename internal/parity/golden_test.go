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

func TestDumpKeepsDottedKeysApartFromNestedOnes(t *testing.T) {
	t.Parallel()

	dotted := map[string]interface{}{"a.b": 1}
	nested := map[string]interface{}{"a": map[string]interface{}{"b": 1}}

	if parity.Dump(dotted) == parity.Dump(nested) {
		t.Errorf("a dotted key and a nested key dump the same:\n%s", parity.Dump(dotted))
	}

	for key, want := range map[string]string{
		"a.b":         "$[\"a.b\"]\tint\t1\n",
		"a[0]":        "$[\"a[0]\"]\tint\t1\n",
		"a b":         "$[\"a b\"]\tint\t1\n",
		"":            "$[\"\"]\tint\t1\n",
		"a\"b":        "$[\"a\\\"b\"]\tint\t1\n",
		"5(int)":      "$[\"5(int)\"]\tint\t1\n",
		"plain-key_1": "$.plain-key_1\tint\t1\n",
	} {
		if got := parity.Dump(map[string]interface{}{key: 1}); got != want {
			t.Errorf("key %q dumps as %q, want %q", key, got, want)
		}
	}

	if parity.Dump(map[interface{}]interface{}{5: 1}) == parity.Dump(map[string]interface{}{"5(int)": 1}) {
		t.Error("an integer key and a string key that spells it dump the same")
	}
}

func TestDumpTellsANilMapFromAnEmptyOne(t *testing.T) {
	t.Parallel()

	var nilMap map[string]interface{}

	if parity.Dump(nilMap) == parity.Dump(map[string]interface{}{}) {
		t.Error("a nil map and an empty map dump the same")
	}

	if parity.Dump(map[string]interface{}{"a": nilMap}) == parity.Dump(map[string]interface{}{"a": map[string]interface{}{}}) {
		t.Error("a nil map and an empty map dump the same inside a map")
	}
}

func TestScalarsSeesALostQuote(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"2026-01-02", "yes", "no", "on", "0755", "1e3", "1.10", "~", "null", "12:30:45", "0x1f", "true"} {
		plain := parity.Scalars("a: " + value + "\n")
		quoted := parity.Scalars("a: \"" + value + "\"\n")

		if plain == quoted {
			t.Errorf("%s records the same with and without quotes:\n%s", value, plain)
		}
	}
}

func TestScalarsRecordsHowYAML11ResolvesAScalar(t *testing.T) {
	t.Parallel()

	got := parity.Scalars("a: yes\nb: \"yes\"\nc: 0755\nd: 2026-01-02\ne: ~\nf: 1.10\n")

	for _, want := range []string{
		"$[\"a\"]\tplain\t!!str\tbool\t\"yes\"\n",
		"$[\"b\"]\tdouble\t!!str\tstring\t\"yes\"\n",
		"$[\"c\"]\tplain\t!!int\tint\t\"0755\"\n",
		"$[\"d\"]\tplain\t!!timestamp\t",
		"$[\"e\"]\tplain\t!!null\t",
		"$[\"f\"]\tplain\t!!float\tfloat64\t\"1.10\"\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the record lacks %q:\n%s", want, got)
		}
	}
}

func TestScalarsRecordsKeysAliasesAndErrors(t *testing.T) {
	t.Parallel()

	got := parity.Scalars("base: &b {x: 1}\nuse: *b\nyes: v\n")

	for _, want := range []string{"$[\"yes\"]#key", "alias", "$[\"use\"]"} {
		if !strings.Contains(got, want) {
			t.Errorf("the record lacks %q:\n%s", want, got)
		}
	}

	if !strings.HasPrefix(parity.Scalars("a: [1\n"), "ERROR: ") {
		t.Error("text that does not parse is not recorded as an error")
	}
}

// TestCorpusScalars records how the rendered forge manifests and the odd
// documents spell their scalars, which the decoded dumps in other packages
// cannot show.
func TestCorpusScalars(t *testing.T) {
	t.Parallel()

	var recorded strings.Builder

	for _, doc := range parity.Corpus(t) {
		recorded.WriteString(parity.Section(doc.Name, parity.Scalars(doc.Body)))
	}

	parity.Golden(t, filepath.Join("testdata", "scalars", "corpus.txt"), recorded.String())
}
