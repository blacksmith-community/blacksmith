package services_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"blacksmith/internal/services"
)

// writeServiceDir writes a service.yml and one directory per plan, each
// holding the files given for it, and returns the service directory.
func writeServiceDir(t *testing.T, serviceYML string, plans map[string]map[string]string) string {
	t.Helper()

	dir := t.TempDir()

	err := os.WriteFile(filepath.Join(dir, "service.yml"), []byte(serviceYML), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	for name, files := range plans {
		if _, ok := files["manifest.yml"]; !ok {
			files["manifest.yml"] = "meta:\n  size: small\n"
		}

		for file, body := range files {
			path := filepath.Join(dir, name, file)

			err = os.MkdirAll(filepath.Dir(path), 0o750)
			if err != nil {
				t.Fatal(err)
			}

			err = os.WriteFile(path, []byte(body), 0o600)
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	return dir
}

// publishedJSON returns the catalog services as the platform reads them.
func publishedJSON(t *testing.T, service services.Service) []map[string]interface{} {
	t.Helper()

	data, err := json.Marshal(services.Catalog([]services.Service{service}))
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}

	var published []map[string]interface{}

	err = json.Unmarshal(data, &published)
	if err != nil {
		t.Fatalf("unmarshal catalog: %v", err)
	}

	return published
}

// planFile and schemaFile are the files a plan directory declares itself in.
const (
	planFile   = "plan.yml"
	schemaFile = "schema.json"
)

// smallPlan is the plan readDeclaringService declares a move from.
const smallPlan = "small"

// readDeclaringService reads a service whose service.yml and plans set
// every declaration about plan changes, context updates, and maintenance.
func readDeclaringService(t *testing.T) services.Service {
	t.Helper()

	dir := writeServiceDir(t, `id: kv
name: kv
description: a key-value store
bindable: true
plan_updateable: true
allow_context_updates: true
init_idempotent: true
maintenance_info:
  version: 1.2.3
  description: the forge's maintenance version
`, map[string]map[string]string{
		smallPlan: {planFile: `id: small
name: small
plan_updateable: true
update_to: [large]
ephemeral_update_ok: true
free: false
maintenance_info:
  version: 1.2.3
`},
		"large": {planFile: "id: large\nname: large\nplan_updateable: false\n"},
		"fixed": {planFile: "id: fixed\nname: fixed\n"},
	})

	service, err := services.ReadService(dir)
	if err != nil {
		t.Fatalf("read service: %v", err)
	}

	return service
}

// requireDeclarationsDecoded fails the test when a declaration in
// service.yml or plan.yml did not reach the broker's catalog structs.
func requireDeclarationsDecoded(t *testing.T, service services.Service) {
	t.Helper()

	if !service.PlanUpdateable || !service.AllowContextUpdates || !service.InitIdempotent {
		t.Errorf("service.yml declarations were dropped: %+v", service)
	}

	if service.MaintenanceInfo == nil || service.MaintenanceInfo.Version != "1.2.3" {
		t.Errorf("service.yml maintenance_info was dropped: %+v", service.MaintenanceInfo)
	}

	plans := map[string]services.Plan{}
	for _, plan := range service.Plans {
		plans[plan.Name] = plan
	}

	want := services.Plan{
		PlanUpdateable:    boolPointer(true),
		UpdateTo:          []string{"large"},
		EphemeralUpdateOK: true,
		Free:              boolPointer(false),
		MaintenanceInfo:   &services.MaintenanceInfo{Version: "1.2.3"},
	}

	small := plans[smallPlan]
	got := services.Plan{
		PlanUpdateable:    small.PlanUpdateable,
		UpdateTo:          small.UpdateTo,
		EphemeralUpdateOK: small.EphemeralUpdateOK,
		Free:              small.Free,
		MaintenanceInfo:   small.MaintenanceInfo,
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan.yml declarations of small decoded as %+v, want %+v", got, want)
	}

	if large := plans["large"]; !reflect.DeepEqual(large.PlanUpdateable, boolPointer(false)) {
		t.Errorf("plan.yml plan_updateable false of large decoded as %v", large.PlanUpdateable)
	}

	if fixed := plans["fixed"]; fixed.PlanUpdateable != nil || fixed.UpdateTo != nil {
		t.Errorf("a plan that declares nothing decoded declarations: %+v", fixed)
	}
}

// requirePublishedPlans fails the test when a published plan offers a plan
// change or maintenance_info, or publishes the wrong free flag.
func requirePublishedPlans(t *testing.T, published map[string]interface{}) {
	t.Helper()

	publishedPlans, _ := published["plans"].([]interface{})
	if len(publishedPlans) != 3 {
		t.Fatalf("the catalog publishes %d plans, want 3", len(publishedPlans))
	}

	for _, entry := range publishedPlans {
		plan, _ := entry.(map[string]interface{})

		if plan["plan_updateable"] != false {
			t.Errorf("plan %v publishes plan_updateable %v, want false", plan["name"], plan["plan_updateable"])
		}

		if _, ok := plan["maintenance_info"]; ok {
			t.Errorf("plan %v publishes maintenance_info, want it left out", plan["name"])
		}

		wantFree := plan["name"] != smallPlan
		if plan["free"] != wantFree {
			t.Errorf("plan %v publishes free %v, want %v", plan["name"], plan["free"], wantFree)
		}
	}
}

// The plan-change declarations in service.yml and plan.yml reach the
// broker's catalog structs, while the catalog the platform reads keeps
// publishing plan_updateable false, because this release cannot change a
// plan. Every service publishes allow_context_updates true, and
// maintenance_info stays unpublished.
//
//nolint:paralleltest // kept serial, as the other catalog tests are
func TestCatalog_PublishesPlanUpdateableFromForge(t *testing.T) {
	service := readDeclaringService(t)

	requireDeclarationsDecoded(t, service)

	published := publishedJSON(t, service)[0]

	if published["plan_updateable"] != false {
		t.Errorf("the service publishes plan_updateable %v, want false", published["plan_updateable"])
	}

	if published["allow_context_updates"] != true {
		t.Errorf("the service publishes allow_context_updates %v, want true", published["allow_context_updates"])
	}

	if value, ok := published["maintenance_info"]; ok {
		t.Errorf("the service publishes maintenance_info %v, want it left out", value)
	}

	requirePublishedPlans(t, published)
}

// The computed flag is true only when every condition holds, and false
// when any one of them is missing.
func TestCatalog_PlanUpdateableNeedsEveryCondition(t *testing.T) {
	t.Parallel()

	service := services.Service{PlanUpdateable: true, InitIdempotent: true}
	plan := services.Plan{UpdateTo: []string{"kv-large"}}

	if !services.PlanUpdateable(true, service, plan) {
		t.Fatal("a plan that meets every condition is not updateable")
	}

	cases := map[string]struct {
		supported bool
		service   services.Service
		plan      services.Plan
	}{
		"the broker does not support plan changes": {false, service, plan},
		"the plan declares no transitions":         {true, service, services.Plan{}},
		"the forge's init is not idempotent":       {true, services.Service{PlanUpdateable: true}, plan},
		"the service allows none and the plan is silent": {
			true, services.Service{InitIdempotent: true}, plan,
		},
		"the plan refuses what its service allows": {
			true, service, services.Plan{UpdateTo: plan.UpdateTo, PlanUpdateable: boolPointer(false)},
		},
	}

	for name, tc := range cases {
		if services.PlanUpdateable(tc.supported, tc.service, tc.plan) {
			t.Errorf("%s, yet the plan is updateable", name)
		}
	}

	allowed := services.Plan{UpdateTo: plan.UpdateTo, PlanUpdateable: boolPointer(true)}
	if !services.PlanUpdateable(true, services.Service{InitIdempotent: true}, allowed) {
		t.Error("a plan that allows changes its service is silent on is not updateable")
	}
}

// publishedSchemaJSON is the schema.json of the schema publishing test.
const publishedSchemaJSON = `{"$schema": "http://json-schema.org/draft-04/schema#", "type": "object", "properties": {"maxmemory-policy": {"type": "string"}}, "additionalProperties": false}`

// A plan's parameter schema, from plan.yml or from schema.json, is
// published for both create and update.
//
//nolint:paralleltest // kept serial, as the other catalog tests are
func TestCatalog_PublishesSchemas(t *testing.T) {
	dir := writeServiceDir(t, "id: kv\nname: kv\ndescription: a key-value store\nbindable: true\n", map[string]map[string]string{
		"from-plan-yml": {planFile: `id: from-plan-yml
schemas:
  parameters:
    $schema: http://json-schema.org/draft-04/schema#
    type: object
    properties:
      maxmemory-policy:
        type: string
    additionalProperties: false
`},
		"from-schema-json": {planFile: "id: from-schema-json\n", schemaFile: publishedSchemaJSON},
		"none":             {planFile: "id: none\n"},
	})

	service, err := services.ReadService(dir)
	if err != nil {
		t.Fatalf("read service: %v", err)
	}

	var want map[string]interface{}

	err = json.Unmarshal([]byte(publishedSchemaJSON), &want)
	if err != nil {
		t.Fatal(err)
	}

	publishedPlans, _ := publishedJSON(t, service)[0]["plans"].([]interface{})

	for _, entry := range publishedPlans {
		plan, _ := entry.(map[string]interface{})
		name, _ := plan["name"].(string)

		if name == "none" {
			if _, ok := plan["schemas"]; ok {
				t.Errorf("plan none publishes schemas %v, want them left out", plan["schemas"])
			}

			continue
		}

		schemas, _ := plan["schemas"].(map[string]interface{})
		instance, _ := schemas["service_instance"].(map[string]interface{})

		for _, operation := range []string{"create", "update"} {
			published, _ := instance[operation].(map[string]interface{})
			if !reflect.DeepEqual(published["parameters"], want) {
				t.Errorf("plan %s publishes %s parameters %v, want %v", name, operation, published["parameters"], want)
			}
		}
	}
}

// A plan declares its schema in one place, and a schema.json must hold a
// JSON object.
//
//nolint:paralleltest // kept serial, as the other catalog tests are
func TestReadPlanRefusesAnAmbiguousOrMalformedSchema(t *testing.T) {
	cases := map[string]map[string]string{
		"declared twice": {
			planFile:   "id: twice\nschemas:\n  parameters:\n    type: object\n",
			schemaFile: `{"type": "object"}`,
		},
		"not JSON":      {planFile: "id: broken\n", schemaFile: "type: object\n"},
		"not an object": {planFile: "id: list\n", schemaFile: `["type"]`},
		"a JSON null":   {planFile: "id: null-schema\n", schemaFile: "null"},
	}

	for name, files := range cases {
		dir := writePlanDir(t, files)

		_, err := services.ReadPlan(dir)
		if err == nil {
			t.Errorf("%s: the plan was read, want an error", name)
		}
	}
}
