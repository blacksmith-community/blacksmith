package upgrade_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"blacksmith/internal/parity"
	"blacksmith/internal/upgrade"
)

const (
	parityStemcellOS      = "ubuntu-noble"
	parityStemcellVersion = "1.99"
)

// parityReleaseTargets name the releases the rendered manifests hold, a
// release none of them holds, and the bpm release every plan pins.
//
//nolint:gochecknoglobals // constant test data
var parityReleaseTargets = []upgrade.ReleaseTarget{
	{Name: "bpm", Version: "99.0.1"},
	{Name: "valkey-forge", Version: "99.1.0"},
	{Name: "redis-forge", Version: "99.2.0"},
	{Name: "rabbitmq-forge", Version: "99.3.0"},
	{Name: "postgresql-forge", Version: "99.4.0"},
	{Name: "mariadb-forge", Version: "99.5.0"},
	{Name: "kubernetes-forge", Version: "99.6.0"},
	{Name: "absent-release", Version: "99.7.0"},
}

// TestMergeStemcellOverlayParity checks the manifest the stemcell overlay
// produces for every rendered manifest against the recording made when
// spruce merged it, and records what graft makes of it in stemcell-graft.
// The manifest has to match as a YAML tree, and the type of every scalar
// has to stay the same though its style may change. A failure compares
// only that both fail, since the error text is graft's.
func TestMergeStemcellOverlayParity(t *testing.T) {
	t.Parallel()

	for _, rendered := range parity.RenderedManifests(t) {
		t.Run(rendered.Name, func(t *testing.T) {
			t.Parallel()

			old := filepath.Join("testdata", "parity", "stemcell", rendered.Name)
			graft := filepath.Join("testdata", "parity", "stemcell-graft", rendered.Name)

			oldYAML := readRecording(t, old+".yml")

			merged, err := upgrade.MergeStemcellOverlay(rendered.Body, parityStemcellOS, parityStemcellVersion)
			if err != nil {
				if !strings.HasPrefix(oldYAML, "ERROR:") {
					t.Errorf("the recording holds a manifest, but the overlay failed: %v", err)
				}

				parity.Golden(t, graft+".yml", "ERROR: "+err.Error()+"\n")

				return
			}

			if strings.HasPrefix(oldYAML, "ERROR:") {
				t.Fatalf("the recording is an error, but the overlay made a manifest")
			}

			parity.CompareYAML(t, []byte(oldYAML), []byte(merged))

			wantScalars, gotScalars := scalarTypes(readRecording(t, old+".scalars")), scalarTypes(parity.Scalars(merged))
			if !reflect.DeepEqual(wantScalars, gotScalars) {
				t.Errorf("scalar types differ\n--- want\n%s\n--- got\n%s", strings.Join(wantScalars, "\n"), strings.Join(gotScalars, "\n"))
			}

			parity.GoldenYAML(t, graft+".yml", merged)
		})
	}
}

// TestGetCurrentStemcellParity records the stemcell each rendered manifest
// reports, and the same call on the overlay's output. The recordings were
// made when yaml.v2 read the manifest and spruce merged it, and graft
// matches them byte for byte.
func TestGetCurrentStemcellParity(t *testing.T) {
	t.Parallel()

	for _, rendered := range parity.RenderedManifests(t) {
		t.Run(rendered.Name, func(t *testing.T) {
			t.Parallel()

			recorded := map[string]interface{}{}

			recorded["rendered"] = currentStemcell(rendered.Body)

			merged, err := upgrade.MergeStemcellOverlay(rendered.Body, parityStemcellOS, parityStemcellVersion)
			if err == nil {
				recorded["merged"] = currentStemcell(merged)
			}

			parity.Golden(t, filepath.Join("testdata", "parity", "current-stemcell", rendered.Name+".dump"), parity.Dump(recorded))
		})
	}
}

func currentStemcell(manifest string) map[string]interface{} {
	stemcellOS, version, err := upgrade.GetCurrentStemcell(manifest)
	if err != nil {
		return map[string]interface{}{"error": err.Error()}
	}

	return map[string]interface{}{"os": stemcellOS, "version": version}
}

// TestMergeReleaseOverlayParity checks the manifest and the applied targets
// the release bump produces for every rendered manifest against the
// recordings made when spruce's YAML library wrote them, and records what
// the graft writer makes of them beside those.
func TestMergeReleaseOverlayParity(t *testing.T) {
	t.Parallel()

	for _, rendered := range parity.RenderedManifests(t) {
		t.Run(rendered.Name, func(t *testing.T) {
			t.Parallel()

			merged, applied, err := upgrade.MergeReleaseOverlay(rendered.Body, parityReleaseTargets)
			checkReleaseParity(t, "release", rendered.Name, nil, merged, applied, err)
		})
	}
}

// movedRelease pins how one odd input is meant to differ from its
// recording. The comparison has to report exactly these lines and no
// others, so a second difference cannot hide behind the first.
type movedRelease struct {
	reason string
	// differences are the lines parity.YAMLDifferences reports.
	differences []string
	// scalars are the lines of the scalar-type record that only one side
	// has, with - for the recording and + for the graft output.
	scalars []string
	// dumpDiffers is whether the applied targets differ.
	dumpDiffers bool
}

// movedReleaseCases lists the odd inputs whose result is meant to differ
// from the recording. A plain y is a string to Psych and to graft, and the
// recording kept it as a boolean, which is yaml.v2's reading. Graft decodes
// every mapping key to a string, so an integer key comes back quoted.
//
//nolint:gochecknoglobals // constant test data
var movedReleaseCases = map[string]movedRelease{
	"quoted-boolean-names": {
		reason: "a plain y is a string, so the release named y is bumped",
		differences: []string{
			`tree $.releases[1].name: tag !!str, want !!bool`,
			`tree $.releases[1].name: value "y", want "true"`,
			`tree $.releases[1].version: tag !!str, want !!int`,
			`tree $.releases[1].version: value "97", want "1"`,
			"decode $.releases[1].name: want bool\ttrue, got string\t\"y\"",
			"decode $.releases[1].version: want int\t1, got string\t\"97\"",
		},
		scalars: []string{
			"- $[\"releases\"][1][\"name\"]\t!!bool\tbool\t\"true\"",
			"- $[\"releases\"][1][\"version\"]\t!!int\tint\t\"1\"",
			"+ $[\"releases\"][1][\"name\"]\t!!str\tstring\t\"y\"",
			"+ $[\"releases\"][1][\"version\"]\t!!str\tstring\t\"97\"",
		},
		dumpDiffers: true,
	},
	"scalars-and-anchors": {
		reason: "a plain y stays the string y instead of becoming true",
		differences: []string{
			`tree $.releases[1].stemcell.version: tag !!str, want !!bool`,
			`tree $.releases[1].stemcell.version: value "y", want "true"`,
			"decode $.releases[1].stemcell.version: want bool\ttrue, got string\t\"y\"",
		},
		scalars: []string{
			"- $[\"releases\"][1][\"stemcell\"][\"version\"]\t!!bool\tbool\t\"true\"",
			"+ $[\"releases\"][1][\"stemcell\"][\"version\"]\t!!str\tstring\t\"y\"",
		},
	},
	"integer-keys": {
		reason: "map[string]any; no forge template has non-string keys",
		differences: []string{
			`tree $["1"](!!int): missing, want !!str "one"`,
			`tree $.1: extra, got !!str "one"`,
		},
		scalars: []string{
			"- $[\"1\"]#key\t!!int\tint\t\"1\"",
			"+ $[\"1\"]#key\t!!str\tstring\t\"1\"",
		},
	},
}

// TestMergeReleaseOverlayParityHandlesOddInput checks how the bump treats
// input that is not a deployed manifest.
func TestMergeReleaseOverlayParityHandlesOddInput(t *testing.T) {
	t.Parallel()

	inputs := map[string]string{
		"not-yaml":             "name: [unterminated\n",
		"no-releases":          "name: bare\n",
		"releases-not-a-list":  "releases: nope\n",
		"release-not-a-map":    "releases:\n- just-a-string\n- name: bpm\n  version: 1\n",
		"numeric-version":      "releases:\n- name: bpm\n  version: 1.4\n  url: https://example.test/bpm.tgz\n  sha1: sha256:fixture\n",
		"scalars-and-anchors":  "releases:\n- &r {name: bpm, version: \"1\", stemcell: {os: x, version: y}}\n- *r\n",
		"integer-keys":         "1: one\nreleases:\n- name: bpm\n  version: 2\n",
		"quoted-boolean-names": "releases:\n- name: \"yes\"\n  version: 1\n- name: y\n  version: 1\n",
	}

	targets := []upgrade.ReleaseTarget{{Name: "bpm", Version: "99"}, {Name: "yes", Version: "98"}, {Name: "y", Version: "97"}}

	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			merged, applied, err := upgrade.MergeReleaseOverlay(input, targets)
			checkReleaseParity(t, "release-odd", name, movedFor(name), merged, applied, err)
		})
	}
}

// checkReleaseParity compares the bump's result with the recording in
// testdata/parity/<dir>, which spruce-era code made and which stays as it
// was, then records the result in testdata/parity/<dir>-graft. The manifest
// has to match as a YAML tree and decode, the type of every scalar has to
// stay the same though its style may change, and the applied targets have
// to match byte for byte. A case in movedReleaseCases is meant to differ,
// and the test requires that it does, so the exemption cannot go stale. A
// failure compares only that both fail, since the error text is graft's.
func checkReleaseParity(t *testing.T, dir, name string, moved *movedRelease, merged string, applied []upgrade.ReleaseTarget, err error) {
	t.Helper()

	old := filepath.Join("testdata", "parity", dir, name)
	graft := filepath.Join("testdata", "parity", dir+"-graft", name)

	oldYAML := readRecording(t, old+".yml")

	if err != nil {
		if !strings.HasPrefix(oldYAML, "ERROR:") {
			t.Errorf("the recording holds a manifest, but the bump failed: %v", err)
		}

		parity.Golden(t, graft+".yml", "ERROR: "+err.Error()+"\n")

		return
	}

	if strings.HasPrefix(oldYAML, "ERROR:") {
		t.Fatalf("the recording is an error, but the bump made a manifest")
	}

	oldDump := readRecording(t, old+".dump")
	dump := parity.Dump(applied)

	wantScalars, gotScalars := scalarTypes(readRecording(t, old+".scalars")), scalarTypes(parity.Scalars(merged))

	if moved == nil {
		parity.CompareYAML(t, []byte(oldYAML), []byte(merged))

		if !reflect.DeepEqual(wantScalars, gotScalars) {
			t.Errorf("scalar types differ\n--- want\n%s\n--- got\n%s", strings.Join(wantScalars, "\n"), strings.Join(gotScalars, "\n"))
		}

		if dump != oldDump {
			t.Errorf("applied targets differ\n--- want\n%s\n--- got\n%s", oldDump, dump)
		}
	} else {
		if got := parity.YAMLDifferences([]byte(oldYAML), []byte(merged)); !reflect.DeepEqual(got, moved.differences) {
			t.Errorf("the differences (%s) are not the pinned ones\n--- pinned\n%s\n--- got\n%s",
				moved.reason, strings.Join(moved.differences, "\n"), strings.Join(got, "\n"))
		}

		if got := scalarDifferences(wantScalars, gotScalars); !reflect.DeepEqual(got, moved.scalars) {
			t.Errorf("the scalar differences are not the pinned ones\n--- pinned\n%s\n--- got\n%s",
				strings.Join(moved.scalars, "\n"), strings.Join(got, "\n"))
		}

		if (dump != oldDump) != moved.dumpDiffers {
			t.Errorf("applied targets differ is %v, want %v", dump != oldDump, moved.dumpDiffers)
		}
	}

	parity.GoldenYAML(t, graft+".yml", merged)
	parity.Golden(t, graft+".dump", dump)
}

func movedFor(name string) *movedRelease {
	moved, ok := movedReleaseCases[name]
	if !ok {
		return nil
	}

	return &moved
}

// scalarDifferences lists the lines only one of two sorted scalar-type
// records has, the recording's first.
func scalarDifferences(want, got []string) []string {
	var differences []string

	has := func(lines []string, line string) bool {
		return slices.Contains(lines, line)
	}

	for _, line := range want {
		if !has(got, line) {
			differences = append(differences, "- "+line)
		}
	}

	for _, line := range got {
		if !has(want, line) {
			differences = append(differences, "+ "+line)
		}
	}

	return differences
}

func readRecording(t *testing.T, path string) string {
	t.Helper()

	body, err := os.ReadFile(path) //nolint:gosec // a golden file
	if err != nil {
		t.Fatalf("read the recording: %s", err)
	}

	return string(body)
}

// scalarTypes lists a Scalars record without the style column and in sorted
// order, so that a scalar may change from plain to quoted, and a key may
// move, while its tag and YAML 1.1 type stay.
func scalarTypes(scalars string) []string {
	var lines []string

	for _, line := range strings.Split(strings.TrimSuffix(scalars, "\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) > 2 {
			fields = append(fields[:1], fields[2:]...)
		}

		lines = append(lines, strings.Join(fields, "\t"))
	}

	sort.Strings(lines)

	return lines
}

// TestReleaseOverlayParityPsych runs the Ruby comparison, which reads both
// sets of recordings as the BOSH director does, on the spruce-era and graft
// recordings. The exemptions are the accepted differences, and the exempt
// counts pin how many of each there are, so a new difference of an
// accepted kind still fails the test.
func TestReleaseOverlayParityPsych(t *testing.T) {
	t.Parallel()

	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("ruby is not installed")
	}

	script, err := filepath.Abs(filepath.Join("..", "..", "hack", "parity", "psych-compare.rb"))
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		args []string
		last string
	}{
		"release": {
			args: []string{"--allow", "symbol-quoting"},
			last: "psych-compare: 0 difference(s), 12 exempt (symbol-quoting 12)",
		},
		"release-odd": {
			args: []string{
				"--allow", "stringified-keys", "--allow", "y-strings",
				"--allow-diff", "quoted-boolean-names.yml", "$.releases[1].version",
				"--skip-error-file", "not-yaml.yml=the error text is graft's",
			},
			last: "psych-compare: 0 difference(s), 5 exempt (stringified-keys 1, skip-error-file 1, y-strings 2, allow-diff 1)",
		},
	}

	for dir, tc := range cases {
		t.Run(dir, func(t *testing.T) {
			t.Parallel()

			base := filepath.Join("testdata", "parity")
			args := append(append([]string{script}, tc.args...), filepath.Join(base, dir), filepath.Join(base, dir+"-graft"))

			out, err := exec.Command(ruby, args...).CombinedOutput() //nolint:gosec // a test runs its own script
			if err != nil {
				t.Fatalf("psych-compare found a difference: %s\n%s", err, out)
			}

			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if last := lines[len(lines)-1]; last != tc.last {
				t.Errorf("last line %q, want %q", last, tc.last)
			}
		})
	}
}

// TestStemcellOverlayParityPsych runs the Ruby comparison on the
// spruce-era and graft recordings of the stemcell overlay. The only
// difference is that graft quotes a string that starts with a colon, which
// Psych reads as a symbol when it is plain, and the exempt count pins how
// many of those there are.
func TestStemcellOverlayParityPsych(t *testing.T) {
	t.Parallel()

	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("ruby is not installed")
	}

	script, err := filepath.Abs(filepath.Join("..", "..", "hack", "parity", "psych-compare.rb"))
	if err != nil {
		t.Fatal(err)
	}

	base := filepath.Join("testdata", "parity")

	out, err := exec.Command(ruby, script, "--allow", "symbol-quoting", //nolint:gosec // a test runs its own script
		filepath.Join(base, "stemcell"), filepath.Join(base, "stemcell-graft")).CombinedOutput()
	if err != nil {
		t.Fatalf("psych-compare found a difference: %s\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")

	want := "psych-compare: 0 difference(s), 14 exempt (symbol-quoting 14)"
	if last := lines[len(lines)-1]; last != want {
		t.Errorf("last line %q, want %q", last, want)
	}
}
