package manifest_test

import (
	"testing"

	manifestPkg "blacksmith/internal/manifest"
	"blacksmith/internal/services"

	"github.com/fivetwenty-io/graft/pkg/yaml"
)

// TestGenManifestReadsSlashesAsPaths pins that a slash inside a reference
// is part of the path, as spruce read it. Credentials templates name a VM
// as jobs.<name>/<index>, and jobs.n/2 is the key n/2 even when a key n
// exists, never n divided by two.
//
//nolint:paralleltest // GenManifest sets CREDENTIALS
func TestGenManifestReadsSlashesAsPaths(t *testing.T) {
	t.Setenv("CREDENTIALS", "")

	const source = `jobs:
  postgresql/0:
    ips: [10.0.0.11]
  n: 8
  n/2: slash-key
first_ip: (( grab jobs.postgresql/0.ips[0] ))
uri: (( concat "postgres://" jobs.postgresql/0.ips[0] ":5432" ))
slash_key: (( grab jobs.n/2 ))
fallback: (( grab jobs.missing/0.ips[0] || jobs.postgresql/0.ips[0] ))
`

	var decoded map[string]interface{}

	err := yaml.Unmarshal([]byte(source), &decoded)
	if err != nil {
		t.Fatalf("decode the plan: %s", err)
	}

	rendered, err := manifestPkg.GenManifest(services.Plan{Manifest: decoded}, "instance-id")
	if err != nil {
		t.Fatalf("render: %s", err)
	}

	var got map[string]interface{}

	err = yaml.Unmarshal([]byte(rendered), &got)
	if err != nil {
		t.Fatalf("decode the render: %s", err)
	}

	want := map[string]interface{}{
		"first_ip":  "10.0.0.11",
		"uri":       "postgres://10.0.0.11:5432",
		"slash_key": "slash-key",
		"fallback":  "10.0.0.11",
	}

	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s is %#v, want %#v", key, got[key], value)
		}
	}
}
