package manifest_test

import (
	"testing"

	manifestPkg "blacksmith/internal/manifest"
	"blacksmith/internal/services"

	"github.com/fivetwenty-io/graft/pkg/yaml"
)

// TestGenManifestMergesUserParameters renders a plan with no parameters,
// with one, and with a nested one, and checks the YAML graft writes.
//
//nolint:paralleltest // GenManifest sets CREDENTIALS
func TestGenManifestMergesUserParameters(t *testing.T) {
	const source = "---\nmeta:\n  user: redis\nfoo: bar\n"

	cases := []struct {
		name   string
		params map[string]interface{}
		want   string
	}{
		{
			name:   "without user parameters",
			params: map[string]interface{}{},
			want:   "foo: bar\nmeta:\n  user: redis\n",
		},
		{
			name: "with user parameters",
			params: map[string]interface{}{
				"meta": map[string]interface{}{
					"params": map[string]interface{}{"extra": "value"},
				},
			},
			want: "foo: bar\nmeta:\n  params:\n    extra: value\n  user: redis\n",
		},
		{
			name: "with multi-level user parameters",
			params: map[string]interface{}{
				"meta": map[string]interface{}{
					"params": map[string]interface{}{
						"extra": map[string]interface{}{"second": "value"},
					},
				},
			},
			want: "foo: bar\nmeta:\n  params:\n    extra:\n      second: value\n  user: redis\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREDENTIALS", "")

			var decoded map[string]interface{}

			err := yaml.Unmarshal([]byte(source), &decoded)
			if err != nil {
				t.Fatalf("decode the plan: %s", err)
			}

			rendered, err := manifestPkg.GenManifest(services.Plan{Manifest: decoded}, "instance-id", tc.params)
			if err != nil {
				t.Fatalf("render: %s", err)
			}

			if rendered != tc.want {
				t.Errorf("rendered\n%s\nwant\n%s", rendered, tc.want)
			}
		})
	}
}
