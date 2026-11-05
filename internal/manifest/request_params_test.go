package manifest_test

import (
	"reflect"
	"strings"
	"testing"

	manifestPkg "blacksmith/internal/manifest"
	"blacksmith/internal/services"
	"blacksmith/pkg/utils"

	"github.com/fivetwenty-io/graft/pkg/yaml"
)

// requestParamsPlan is a plan whose defaults overlap the request
// parameters the cases send, and which reads them back whole and by a
// nested path.
func requestParamsPlan() services.Plan {
	return services.Plan{Manifest: map[string]interface{}{
		"meta": map[string]interface{}{
			"something": "from-the-plan",
			"params": map[string]interface{}{
				"outer": map[string]interface{}{"a": "plan-a", "b": "plan-b"},
				"list":  []interface{}{"plan-1", "plan-2"},
			},
		},
		"whole": "(( grab meta.params.outer ))",
		"deep":  `(( grab meta.params.outer.b || "none" ))`,
	}}
}

func renderRequestParams(t *testing.T, params map[string]interface{}) map[string]interface{} {
	t.Helper()

	return decodeRender(t, renderRequestParamsText(t, params))
}

// renderRequestParamsText renders requestParamsPlan with params under
// meta.params and returns the YAML text.
func renderRequestParamsText(t *testing.T, params map[string]interface{}) string {
	t.Helper()

	t.Setenv("CREDENTIALS", "")

	rendered, err := manifestPkg.GenManifest(requestParamsPlan(), "request-params", utils.Wrap("meta.params", params))
	if err != nil {
		t.Fatalf("render: %s", err)
	}

	return rendered
}

func decodeRender(t *testing.T, rendered string) map[string]interface{} {
	t.Helper()

	var decoded map[string]interface{}

	err := yaml.Unmarshal([]byte(rendered), &decoded)
	if err != nil {
		t.Fatalf("decode the render: %s", err)
	}

	return decoded
}

func requestParam(t *testing.T, decoded map[string]interface{}, key string) interface{} {
	t.Helper()

	meta, _ := decoded["meta"].(map[string]interface{})
	params, _ := meta["params"].(map[string]interface{})

	return params[key]
}

// Request parameters are merged under meta.params and evaluated with the
// plan. No request text reaches an operator. These cases pin what a
// render does with them.
//
//nolint:paralleltest // GenManifest sets CREDENTIALS
func TestRequestParamsRender(t *testing.T) {
	const (
		operator      = "(( grab meta.something ))"
		vaultOperator = `(( vault "secret/request-params/probe:value" ))`
	)

	cases := []struct {
		name   string
		params map[string]interface{}
		key    string
		want   interface{}
		whole  interface{}
		deep   interface{}
	}{
		{
			name:   "top-level operator stays literal",
			params: map[string]interface{}{"probe": operator},
			key:    "probe",
			want:   operator,
			whole:  map[string]interface{}{"a": "plan-a", "b": "plan-b"},
			deep:   "plan-b",
		},
		{
			name:   "top-level list operator stays literal",
			params: map[string]interface{}{"list": []interface{}{operator}},
			key:    "list",
			want:   []interface{}{operator, "plan-2"},
			whole:  map[string]interface{}{"a": "plan-a", "b": "plan-b"},
			deep:   "plan-b",
		},
		{
			name:   "nested operator stays literal",
			params: map[string]interface{}{"outer": map[string]interface{}{"probe": operator}},
			key:    "outer",
			want:   map[string]interface{}{"probe": operator},
			whole:  map[string]interface{}{"probe": operator},
			deep:   "none",
		},
		{
			name: "nested map replaces the plan default whole",
			params: map[string]interface{}{
				"outer": map[string]interface{}{"b": "user-b", "c": "user-c"},
			},
			key:   "outer",
			want:  map[string]interface{}{"b": "user-b", "c": "user-c"},
			whole: map[string]interface{}{"b": "user-b", "c": "user-c"},
			deep:  "user-b",
		},
		{
			name:   "map in a top-level list stays literal",
			params: map[string]interface{}{"list": []interface{}{map[string]interface{}{"probe": operator}}},
			key:    "list",
			want:   []interface{}{map[string]interface{}{"probe": operator}, "plan-2"},
			whole:  map[string]interface{}{"a": "plan-a", "b": "plan-b"},
			deep:   "plan-b",
		},
		{
			name: "list in a nested map stays literal",
			params: map[string]interface{}{
				"outer": map[string]interface{}{"list": []interface{}{operator}},
			},
			key:   "outer",
			want:  map[string]interface{}{"list": []interface{}{operator}},
			whole: map[string]interface{}{"list": []interface{}{operator}},
			deep:  "none",
		},
		{
			// The list still merges into the plan default by index.
			name:   "operator among the strings of a top-level list stays literal",
			params: map[string]interface{}{"list": []interface{}{"user-1", operator, "user-3"}},
			key:    "list",
			want:   []interface{}{"user-1", operator, "user-3"},
			whole:  map[string]interface{}{"a": "plan-a", "b": "plan-b"},
			deep:   "plan-b",
		},
		{
			name: "maps in a top-level list stay literal",
			params: map[string]interface{}{"list": []interface{}{
				map[string]interface{}{"name": "first", "probe": operator},
				map[string]interface{}{"name": "second", "plain": "user-value"},
			}},
			key: "list",
			want: []interface{}{
				map[string]interface{}{"name": "first", "probe": operator},
				map[string]interface{}{"name": "second", "plain": "user-value"},
			},
			whole: map[string]interface{}{"a": "plan-a", "b": "plan-b"},
			deep:  "plan-b",
		},
		{
			name: "vault operator two levels into a map stays literal",
			params: map[string]interface{}{"outer": map[string]interface{}{
				"inner": map[string]interface{}{"secret": vaultOperator},
			}},
			key:   "outer",
			want:  map[string]interface{}{"inner": map[string]interface{}{"secret": vaultOperator}},
			whole: map[string]interface{}{"inner": map[string]interface{}{"secret": vaultOperator}},
			deep:  "none",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decoded := renderRequestParams(t, tc.params)

			if got := requestParam(t, decoded, tc.key); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("meta.params.%s = %#v, want %#v", tc.key, got, tc.want)
			}

			if got := decoded["whole"]; !reflect.DeepEqual(got, tc.whole) {
				t.Errorf("whole = %#v, want %#v", got, tc.whole)
			}

			if got := decoded["deep"]; !reflect.DeepEqual(got, tc.deep) {
				t.Errorf("deep = %#v, want %#v", got, tc.deep)
			}
		})
	}
}

// No request text reaches an operator, so a top-level string, a string in
// a top-level list, and a merge marker in a top-level list all come out as
// the text the request sent. None of them reads the plan, the broker's
// environment, a file, or vault. VAULT_ADDR points at a closed port, so a
// vault lookup would fail the render.
//
//nolint:paralleltest // the test sets environment variables
func TestRequestParamsNeverReachAnOperator(t *testing.T) {
	const probe = "request-params-env-probe-do-not-report"

	t.Setenv("REQUEST_PARAMS_PROBE", probe)
	t.Setenv("VAULT_ADDR", "http://127.0.0.1:1")
	t.Setenv("VAULT_TOKEN", "request-params-not-a-token")

	operators := []string{
		"(( grab meta.something ))",
		"(( grab $REQUEST_PARAMS_PROBE ))",
		`(( concat $REQUEST_PARAMS_PROBE "" ))`,
		`(( vault "secret/x:y" ))`,
		"(( file /etc/hostname ))",
		`(( file "/etc/hostname" ))`,
	}

	for _, operator := range operators {
		t.Run("top-level "+operator, func(t *testing.T) {
			rendered := renderRequestParamsText(t, map[string]interface{}{"probe": operator})

			if strings.Contains(rendered, probe) {
				t.Error("the render holds the probe value from the environment")
			}

			if got := requestParam(t, decodeRender(t, rendered), "probe"); got != operator {
				t.Errorf("meta.params.probe = %#v, want the request text", got)
			}
		})

		t.Run("top-level list "+operator, func(t *testing.T) {
			rendered := renderRequestParamsText(t, map[string]interface{}{"list": []interface{}{operator}})

			if strings.Contains(rendered, probe) {
				t.Error("the render holds the probe value from the environment")
			}

			want := []interface{}{operator, "plan-2"}
			if got := requestParam(t, decodeRender(t, rendered), "list"); !reflect.DeepEqual(got, want) {
				t.Errorf("meta.params.list = %#v, want %#v", got, want)
			}
		})
	}

	t.Run("append marker in a top-level list", func(t *testing.T) {
		params := map[string]interface{}{"list": []interface{}{"(( append ))", "user-3"}}

		want := []interface{}{"(( append ))", "user-3"}
		if got := requestParam(t, renderRequestParams(t, params), "list"); !reflect.DeepEqual(got, want) {
			t.Errorf("meta.params.list = %#v, want %#v", got, want)
		}
	})
}

// renderPlanWithParams renders a plan with request parameters under
// meta.params and returns the decoded render.
func renderPlanWithParams(t *testing.T, manifest map[string]interface{}, params map[string]interface{}) map[string]interface{} {
	t.Helper()

	t.Setenv("CREDENTIALS", "")

	rendered, err := manifestPkg.GenManifest(services.Plan{Manifest: manifest}, "request-params", utils.Wrap("meta.params", params))
	if err != nil {
		t.Fatalf("render: %s", err)
	}

	return decodeRender(t, rendered)
}

// A request list of named maps merges into the plan's list of named maps
// by index, because each request map is a literal and not a map the merge
// can key on name. The first plan item is replaced whole, the rest stay,
// and a name can appear twice. Spruce panicked here, which took the broker
// down, and a merged manifest is the accepted outcome.
//
//nolint:paralleltest // GenManifest sets CREDENTIALS
func TestRequestNamedListMergesByIndex(t *testing.T) {
	plan := map[string]interface{}{
		"meta": map[string]interface{}{
			"params": map[string]interface{}{
				"nodes": []interface{}{
					map[string]interface{}{"name": "a", "size": "plan-a"},
					map[string]interface{}{"name": "b", "size": "plan-b"},
				},
			},
		},
	}

	params := map[string]interface{}{
		"nodes": []interface{}{map[string]interface{}{"name": "b", "size": "user-b"}},
	}

	got := requestParam(t, renderPlanWithParams(t, plan, params), "nodes")

	want := []interface{}{
		map[string]interface{}{"name": "b", "size": "user-b"},
		map[string]interface{}{"name": "b", "size": "plan-b"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("meta.params.nodes = %#v, want %#v", got, want)
	}
}

// A plan can inject a nested request map and list its keys. Spruce failed
// both with "is not a map", because it never looked inside the request
// map, and graft reads through the literal. The operator text the request
// map holds is copied into the plan as text and never evaluated.
//
//nolint:paralleltest // GenManifest sets CREDENTIALS
func TestRequestNestedMapFeedsInjectAndKeys(t *testing.T) {
	const operator = "(( grab meta.something ))"

	plan := map[string]interface{}{
		"meta": map[string]interface{}{
			"something": "from-the-plan",
			"params":    map[string]interface{}{"outer": map[string]interface{}{"a": "plan-a"}},
		},
		"injected": map[string]interface{}{
			".":    "(( inject meta.params.outer ))",
			"kept": "plan-kept",
		},
		"names": "(( keys meta.params.outer ))",
	}

	params := map[string]interface{}{
		"outer": map[string]interface{}{"b": "user-b", "probe": operator},
	}

	decoded := renderPlanWithParams(t, plan, params)

	wantInjected := map[string]interface{}{"b": "user-b", "probe": operator, "kept": "plan-kept"}
	if !reflect.DeepEqual(decoded["injected"], wantInjected) {
		t.Errorf("injected = %#v, want %#v", decoded["injected"], wantInjected)
	}

	wantNames := []interface{}{"b", "probe"}
	if !reflect.DeepEqual(decoded["names"], wantNames) {
		t.Errorf("names = %#v, want %#v", decoded["names"], wantNames)
	}
}
