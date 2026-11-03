package services_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"blacksmith/internal/parity"
	"blacksmith/internal/services"
	"blacksmith/pkg/logger"
)

// everyFieldPlan sets every field of a plan and of the service it names,
// including the fields the broker fills itself, and a nested plan that
// sets every field too.
const everyFieldPlan = `id: every-field
name: every-field
description: a plan that sets every field
limit: 7
type: standalone
manifest:
  ignored: true
credentials:
  from-plan: plan-value
  shared: plan-value
initscriptpath: plan-init
manifestpath: plan-manifest
credentialspath: plan-credentials
service:
  id: nested-service
  name: nested-service
  type: nested-type
  description: a nested service
  bindable: true
  tags: [first, second]
  limit: 3
  plans:
  - id: nested-plan
    name: nested-plan
    description: a nested plan
    limit: 2
    type: nested-plan-type
    manifest:
      ignored: true
    credentials:
      nested: nested-value
    initscriptpath: nested-init
    manifestpath: nested-manifest
    credentialspath: nested-credentials
`

// writePlanDir writes each file into a new directory and returns it. A
// plan needs a manifest.yml, so one is added when files has none.
func writePlanDir(t *testing.T, files map[string]string) string {
	t.Helper()

	dir := t.TempDir()

	if _, ok := files["manifest.yml"]; !ok {
		files["manifest.yml"] = "meta:\n  size: small\n"
	}

	for name, body := range files {
		path := filepath.Join(dir, name)

		err := os.MkdirAll(filepath.Dir(path), 0o750)
		if err != nil {
			t.Fatal(err)
		}

		err = os.WriteFile(path, []byte(body), 0o600)
		if err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

// nestedService is the service everyFieldPlan names.
func nestedService() *services.Service {
	return &services.Service{
		ID:          "nested-service",
		Name:        "nested-service",
		Type:        "nested-type",
		Description: "a nested service",
		Bindable:    true,
		Tags:        []string{"first", "second"},
		Limit:       3,
		Plans: []services.Plan{{
			ID:              "nested-plan",
			Name:            "nested-plan",
			Description:     "a nested plan",
			Limit:           2,
			Type:            "nested-plan-type",
			Credentials:     map[string]interface{}{"nested": "nested-value"},
			InitScriptPath:  "nested-init",
			ManifestPath:    "nested-manifest",
			CredentialsPath: "nested-credentials",
		}},
	}
}

// requireEveryField fails the test when a field of value, or of a struct
// it holds, is zero, except for the paths in skip. It keeps the expected
// plans below honest as fields are added.
func requireEveryField(t *testing.T, path string, value reflect.Value, skip map[string]bool) {
	t.Helper()

	switch value.Kind() { //nolint:exhaustive // the remaining kinds are leaves
	case reflect.Ptr:
		if !value.IsNil() {
			requireEveryField(t, path, value.Elem(), skip)
		}
	case reflect.Slice:
		for i := range value.Len() {
			requireEveryField(t, path+"[]", value.Index(i), skip)
		}
	case reflect.Struct:
		for i := range value.NumField() {
			field := path + "." + value.Type().Field(i).Name
			if skip[field] {
				continue
			}

			if value.Field(i).IsZero() {
				t.Errorf("the expected value leaves %s unset", field)
			}

			requireEveryField(t, field, value.Field(i), skip)
		}
	}
}

// requirePlan fails the test when got differs from want, and lists each
// leaf that differs.
func requirePlan(t *testing.T, got, want services.Plan) {
	t.Helper()

	if reflect.DeepEqual(got, want) {
		return
	}

	gotLines := strings.Split(parity.Dump(got), "\n")
	wantLines := strings.Split(parity.Dump(want), "\n")

	for _, line := range wantLines {
		if !slices.Contains(gotLines, line) {
			t.Errorf("missing: %s", line)
		}
	}

	for _, line := range gotLines {
		if !slices.Contains(wantLines, line) {
			t.Errorf("unexpected: %s", line)
		}
	}
}

// TestReadPlanDecodesEveryField reads a plan.yml that sets every field of
// a plan and of its service. Fields with no yaml tag of their own take
// their key from the lower-cased field name, as yaml.v2 matched them.
//
//nolint:paralleltest // kept serial for now, though it shares no process-wide state
func TestReadPlanDecodesEveryField(t *testing.T) {
	// The nested plan's manifest is never decoded, and a plan's service
	// is the one service.yml gives it.
	skip := map[string]bool{
		"Plan.Service.Plans[].Manifest": true,
		"Plan.Service.Plans[].Service":  true,
	}

	t.Run("without credentials.yml", func(t *testing.T) {
		dir := writePlanDir(t, map[string]string{"plan.yml": everyFieldPlan})

		plan, err := services.ReadPlan(dir)
		if err != nil {
			t.Fatalf("read plan: %v", err)
		}

		// The broker sets the init script and manifest paths itself, and
		// with no credentials.yml it empties the credentials and keeps the
		// credentials path plan.yml gives.
		want := services.Plan{
			ID:              "every-field",
			Name:            "every-field",
			Description:     "a plan that sets every field",
			Limit:           7,
			Type:            "standalone",
			Manifest:        map[string]interface{}{"meta": map[string]interface{}{"size": "small"}},
			Credentials:     map[string]interface{}{},
			InitScriptPath:  dir + "/init",
			ManifestPath:    dir + "/manifest.yml",
			CredentialsPath: "plan-credentials",
			Service:         nestedService(),
		}

		requireEveryField(t, "Plan", reflect.ValueOf(want), skip)

		requirePlan(t, plan, want)
	})

	t.Run("with credentials.yml", func(t *testing.T) {
		dir := writePlanDir(t, map[string]string{
			"plan.yml":        everyFieldPlan,
			"credentials.yml": "from-file: file-value\nshared: file-value\n",
		})

		plan, err := services.ReadPlan(dir)
		if err != nil {
			t.Fatalf("read plan: %v", err)
		}

		// credentials.yml adds its keys to the credentials plan.yml gives,
		// and its value wins for a key both set.
		want := services.Plan{
			ID:          "every-field",
			Name:        "every-field",
			Description: "a plan that sets every field",
			Limit:       7,
			Type:        "standalone",
			Manifest:    map[string]interface{}{"meta": map[string]interface{}{"size": "small"}},
			Credentials: map[string]interface{}{
				"from-plan": "plan-value",
				"from-file": "file-value",
				"shared":    "file-value",
			},
			InitScriptPath:  dir + "/init",
			ManifestPath:    dir + "/manifest.yml",
			CredentialsPath: dir + "/credentials.yml",
			Service:         nestedService(),
		}

		requireEveryField(t, "Plan", reflect.ValueOf(want), skip)

		requirePlan(t, plan, want)
	})
}

// TestReadPlanAcceptsAQuotedLimit pins what a quoted number does in the
// integer limit field.
//
//nolint:paralleltest // kept serial for now, though it shares no process-wide state
func TestReadPlanAcceptsAQuotedLimit(t *testing.T) {
	dir := writePlanDir(t, map[string]string{"plan.yml": "id: quoted\nname: quoted\nlimit: \"5\"\n"})

	plan, err := services.ReadPlan(dir)
	if err != nil {
		t.Fatalf("read plan: %v", err)
	}

	if plan.Limit != 5 {
		t.Errorf("limit = %d, want 5", plan.Limit)
	}
}

// TestReadPlanRefusesCredentialsOfTheWrongType loads plans whose
// credentials are not a map. Each load fails with an error that names the
// file that holds them.
//
//nolint:paralleltest // kept serial for now, though it shares no process-wide state
func TestReadPlanRefusesCredentialsOfTheWrongType(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		file  string
	}{
		"a list in plan.yml": {
			files: map[string]string{"plan.yml": "id: wrong\nname: wrong\ncredentials: [1]\n"},
			file:  "plan.yml",
		},
		"a string in plan.yml": {
			files: map[string]string{"plan.yml": "id: wrong\nname: wrong\ncredentials: text\n"},
			file:  "plan.yml",
		},
		"a list in credentials.yml": {
			files: map[string]string{"plan.yml": "id: wrong\nname: wrong\n", "credentials.yml": "- 1\n"},
			file:  "credentials.yml",
		},
		"a string in credentials.yml": {
			files: map[string]string{"plan.yml": "id: wrong\nname: wrong\n", "credentials.yml": "text\n"},
			file:  "credentials.yml",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := writePlanDir(t, tc.files)

			_, err := services.ReadPlan(dir)
			if err == nil {
				t.Fatal("the plan loads, want an error")
			}

			if !strings.Contains(err.Error(), filepath.Join(dir, tc.file)) {
				t.Errorf("error %q does not name %s", err, tc.file)
			}
		})
	}
}

// TestPlanFilesTreatRepeatedKeys loads plan files that repeat a key.
// plan.yml is a template, so its last value wins and a warning names the
// file and the key. credentials.yml and service.yml refuse a repeat, with
// an error that names the file.
//
//nolint:paralleltest // the test swaps the process-wide logger
func TestPlanFilesTreatRepeatedKeys(t *testing.T) {
	t.Run("plan.yml", func(t *testing.T) {
		capture := &warnCapture{NoOpLogger: &logger.NoOpLogger{}}
		previous := logger.Get()

		logger.Set(capture)
		t.Cleanup(func() { logger.Set(previous) })

		dir := writePlanDir(t, map[string]string{
			"plan.yml": "id: repeats\nname: repeats\nservice:\n  id: first-id\n  id: last-id\n",
		})

		plan, err := services.ReadPlan(dir)
		if err != nil {
			t.Fatalf("read plan: %v", err)
		}

		if plan.Service == nil || plan.Service.ID != "last-id" {
			t.Errorf("service = %#v, want the last id", plan.Service)
		}

		if len(capture.warnings) != 1 || !hasWarning(capture.warnings, filepath.Join(dir, "plan.yml"), "service.id") {
			t.Errorf("want one warning that names plan.yml and service.id, got:\n%s", strings.Join(capture.warnings, "\n"))
		}
	})

	t.Run("credentials.yml", func(t *testing.T) {
		dir := writePlanDir(t, map[string]string{
			"plan.yml":        "id: repeats\nname: repeats\n",
			"credentials.yml": "port: first\nport: last\n",
		})

		_, err := services.ReadPlan(dir)
		if err == nil {
			t.Fatal("the plan loads, want an error")
		}

		if !strings.Contains(err.Error(), filepath.Join(dir, "credentials.yml")) {
			t.Errorf("error %q does not name credentials.yml", err)
		}
	})

	t.Run("service.yml", func(t *testing.T) {
		dir := writePlanDir(t, map[string]string{
			"service.yml":        "id: repeats\nid: repeats-again\nname: repeats\n",
			"small/plan.yml":     "id: small\nname: small\n",
			"small/manifest.yml": "meta: {}\n",
		})

		_, err := services.ReadService(dir)
		if err == nil {
			t.Fatal("the service loads, want an error")
		}

		if !strings.Contains(err.Error(), filepath.Join(dir, "service.yml")) {
			t.Errorf("error %q does not name service.yml", err)
		}
	})
}

// TestPlanFilesKeepYAML11Scalars reads plain scalars that YAML 1.1 types as
// a boolean, a number, or a timestamp into the string, int, and bool fields
// of a plan and its service, which decode as yaml.v2 decoded them. The
// credentials hold them the way graft reads any value, as a merged
// manifest does, so y and 08 stay strings where yaml.v2 gave true and 8,
// and a yes key stays "yes" where yaml.v2 gave "true".
//
//nolint:paralleltest // kept serial for now, though it shares no process-wide state
func TestPlanFilesKeepYAML11Scalars(t *testing.T) {
	dir := writePlanDir(t, map[string]string{
		"service.yml": "id: y\nname: no\ndescription: 2001-12-14t21:59:43.10-05:00\ntype: 08\nbindable: yes\ntags: [y, no, 08, 2001-12-14]\nlimit: 08\n",
		"small/plan.yml": "id: \"08\"\nname: 08\ndescription: 2001-12-14\ntype: no\nlimit: 08\n" +
			"credentials:\n  from-plan: y\n",
		"small/manifest.yml":    "meta: {}\n",
		"small/credentials.yml": "yes-flag: y\nno-flag: no\nnumber: 08\ndate: 2001-12-14\nyes: yes-key\n1.50: float-key\n010: octal-key\n",
	})

	service, err := services.ReadService(dir)
	if err != nil {
		t.Fatalf("read service: %v", err)
	}

	gotService := []interface{}{service.ID, service.Name, service.Description, service.Type, service.Bindable, service.Tags, service.Limit}
	wantService := []interface{}{"y", "no", "2001-12-14t21:59:43.10-05:00", "08", true, []string{"y", "no", "08", "2001-12-14"}, 8}

	if !reflect.DeepEqual(gotService, wantService) {
		t.Errorf("service decodes as %#v, want %#v", gotService, wantService)
	}

	if len(service.Plans) != 1 {
		t.Fatalf("service has %d plans, want 1", len(service.Plans))
	}

	plan := service.Plans[0]
	gotPlan := []interface{}{plan.ID, plan.Name, plan.Description, plan.Type, plan.Limit}
	wantPlan := []interface{}{"y-08", "08", "2001-12-14", "no", 8}

	if !reflect.DeepEqual(gotPlan, wantPlan) {
		t.Errorf("plan decodes as %#v, want %#v", gotPlan, wantPlan)
	}

	wantCredentials := map[string]interface{}{
		"from-plan": "y",
		"yes-flag":  "y",
		"no-flag":   false,
		"number":    "08",
		"date":      "2001-12-14",
		"yes":       "yes-key",
		"1.5":       "float-key",
		"8":         "octal-key",
	}

	if !reflect.DeepEqual(plan.Credentials, wantCredentials) {
		t.Errorf("credentials decode as %#v, want %#v", plan.Credentials, wantCredentials)
	}
}

// TestServiceBindableSpellings reads each YAML 1.1 spelling of a boolean
// into the bindable field of a service. A plain y, Y, n, or N is refused
// with an error that names service.yml, where yaml.v2 read it as a
// boolean, and every other spelling decodes as yaml.v2 decoded it.
//
//nolint:paralleltest // kept serial for now, though it shares no process-wide state
func TestServiceBindableSpellings(t *testing.T) {
	spellings := map[string]bool{
		"yes": true, "Yes": true, "YES": true, "on": true, "On": true, "ON": true, "true": true, "True": true, "TRUE": true,
		"no": false, "No": false, "NO": false, "off": false, "Off": false, "OFF": false, "false": false, "False": false, "FALSE": false,
	}

	for _, spelling := range []string{"y", "Y", "yes", "Yes", "YES", "on", "On", "ON", "true", "True", "TRUE", "n", "N", "no", "No", "NO", "off", "Off", "OFF", "false", "False", "FALSE"} {
		t.Run(spelling, func(t *testing.T) {
			dir := writePlanDir(t, map[string]string{
				"service.yml":        "id: spelled\nname: spelled\nbindable: " + spelling + "\n",
				"small/plan.yml":     "id: small\nname: small\n",
				"small/manifest.yml": "meta: {}\n",
			})

			service, err := services.ReadService(dir)

			want, decodes := spellings[spelling]
			if !decodes {
				if err == nil {
					t.Fatalf("bindable: %s decodes as %v, want an error", spelling, service.Bindable)
				}

				if !strings.Contains(err.Error(), filepath.Join(dir, "service.yml")) {
					t.Errorf("error %q does not name service.yml", err)
				}

				return
			}

			if err != nil {
				t.Fatalf("read service: %v", err)
			}

			if service.Bindable != want {
				t.Errorf("bindable: %s decodes as %v, want %v", spelling, service.Bindable, want)
			}
		})
	}
}

// TestReadPlanCredentialsFileShapes reads a credentials.yml that holds
// nothing, a null, or an empty map, over plan.yml credentials and over
// none. Nothing leaves the credentials as plan.yml gives them, a null
// clears them, and an empty map adds no key.
//
//nolint:paralleltest // kept serial for now, though it shares no process-wide state
func TestReadPlanCredentialsFileShapes(t *testing.T) {
	const withCredentials = "id: shapes\nname: shapes\ncredentials:\n  from-plan: plan-value\n"

	const withoutCredentials = "id: shapes\nname: shapes\n"

	fromPlan := map[string]interface{}{"from-plan": "plan-value"}

	cases := map[string]struct {
		plan        string
		credentials string
		want        map[string]interface{}
	}{
		"an empty file over plan credentials":         {withCredentials, "", fromPlan},
		"a comment over plan credentials":             {withCredentials, "# nothing\n", fromPlan},
		"a null over plan credentials":                {withCredentials, "~\n", nil},
		"a null word over plan credentials":           {withCredentials, "null\n", nil},
		"a bare document start over plan credentials": {withCredentials, "---\n", nil},
		"an empty map over plan credentials":          {withCredentials, "{}\n", fromPlan},
		"an empty file without plan credentials":      {withoutCredentials, "", nil},
		"a null without plan credentials":             {withoutCredentials, "null\n", nil},
		"an empty map without plan credentials":       {withoutCredentials, "{}\n", map[string]interface{}{}},
		"a nested map without plan credentials":       {withoutCredentials, "outer:\n  inner: file-value\n", map[string]interface{}{"outer": map[string]interface{}{"inner": "file-value"}}},
		"a nested map replaces a plan key wholesale":  {"id: shapes\nname: shapes\ncredentials:\n  outer:\n    kept: plan-value\n", "outer:\n  inner: file-value\n", map[string]interface{}{"outer": map[string]interface{}{"inner": "file-value"}}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := writePlanDir(t, map[string]string{"plan.yml": tc.plan, "credentials.yml": tc.credentials})

			plan, err := services.ReadPlan(dir)
			if err != nil {
				t.Fatalf("read plan: %v", err)
			}

			if !reflect.DeepEqual(plan.Credentials, tc.want) {
				t.Errorf("credentials = %#v, want %#v", plan.Credentials, tc.want)
			}
		})
	}
}
