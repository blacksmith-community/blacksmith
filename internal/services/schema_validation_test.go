package services_test

import (
	"strings"
	"testing"

	"blacksmith/internal/services"
	"blacksmith/pkg/logger"
)

// schemaForge is the forge the schema validation test loads.
const schemaForge = "schemakv"

// draft4 is a schema object with members, which names the draft-04
// meta-schema.
func draft4(members string) string {
	return `{"$schema": "http://json-schema.org/draft-04/schema#", ` + members + `}`
}

// schemaPlans are the plans of the schema validation test, by name, with
// the schema.json each ships and whether that schema is published. Cloud
// Controller refuses a whole catalog when any one schema in it is larger
// than 64 KB, has no $schema, fails the draft-04 meta-schema, or raises
// an error when the json-schema gem checks it, as a $ref outside the
// schema does, so the broker publishes only a schema inside its profile.
func schemaPlans() map[string]struct {
	schema    string
	published bool
} {
	return map[string]struct {
		schema    string
		published bool
	}{
		"valid": {draft4(`"type": "object",
			"properties": {"maxmemory": {"type": "integer", "minimum": 1}, "policy": {"$ref": "#/definitions/policy"}},
			"definitions": {"policy": {"type": "string"}}, "additionalProperties": false`), true},
		"draft-07":     {`{"$schema": "http://json-schema.org/draft-07/schema#", "type": "object"}`, false},
		"remote-ref":   {draft4(`"type": "object", "properties": {"policy": {"$ref": "https://example.com/policy.json"}}`), false},
		"bad-type":     {draft4(`"type": "object", "properties": {"policy": {"type": 5}}`), false},
		"bad-required": {draft4(`"type": "object", "required": []`), false},
		"too-large":    {draft4(`"type": "object", "description": "` + strings.Repeat("x", 65*1024) + `"`), false},

		// Cloud Controller requires the $schema key, and it is the key
		// most often left out of a schema.
		"no-meta-schema":   {`{"type": "object", "properties": {"policy": {"type": "string", "enum": ["lru", "lfu"]}}}`, false},
		"null-meta-schema": {`{"$schema": null, "type": "object"}`, false},

		// Cloud Controller doesn't compile a pattern, and JSON Schema
		// patterns are ECMA-262, which has lookarounds and backreferences
		// that Go's regexp lacks. It does compile a patternProperties key,
		// which the profile leaves out.
		"lookahead-pattern": {draft4(`"type": "object", "properties": {"password": {"type": "string",
			"pattern": "^(?=.*[A-Z])(?=.*\\d).{12,}$"}}`), true},
		"backreference-pattern": {draft4(`"type": "object", "properties": {"pair": {"type": "string", "pattern": "^(a+)\\1$"}}`), true},
		"lookahead-property":    {draft4(`"type": "object", "patternProperties": {"^(?!x-)": {"type": "string"}}`), false},

		// An id lets Cloud Controller resolve a $ref through a schema it
		// cached from an earlier catalog.
		"id": {draft4(`"id": "http://example.com/params", "type": "object", "$ref": "#/definitions/base",
			"definitions": {"base": {"type": "object"}}`), false},

		// The draft-04 meta-schema is named with or without its fragment.
		"draft-04-no-fragment": {`{"$schema": "http://json-schema.org/draft-04/schema", "type": "object"}`, true},

		// Cloud Controller counts the characters of the schema's JSON, so
		// a multibyte character counts once. It writes <, >, and & as
		// six-character escapes, and a decimal such as 1e+300 in full.
		// Each limit case's JSON is 86 characters before its description.
		"multibyte-under-limit": {draft4(`"type": "object", "description": "` + strings.Repeat("é", 40000) + `"`), true},
		"multibyte-over-limit":  {draft4(`"type": "object", "description": "` + strings.Repeat("é", 64*1024) + `"`), false},
		"html-over-limit":       {draft4(`"type": "object", "description": "` + strings.Repeat("<&>", 20000) + `"`), false},
		"html-at-limit": {draft4(`"type": "object", "description": "` +
			strings.Repeat("<&>", 3000) + strings.Repeat("x", 11450) + `"`), true},
		"html-one-over-limit": {draft4(`"type": "object", "description": "` +
			strings.Repeat("<&>", 3000) + strings.Repeat("x", 11451) + `"`), false},
		"decimal-over-limit": {draft4(`"type": "number", "maximum": 1e300, "description": "` +
			strings.Repeat("x", 65400) + `"`), false},

		// A $ref is a reference only where a schema is expected, and
		// data in a default or an enum is not a schema.
		"ref-in-data": {draft4(`"type": "object", "properties": {"link": {"type": "object",
			"default": {"$ref": "https://example.com/a.json"}, "enum": [{"$ref": "https://example.com/b.json"}]}}`), true},
		"remote-ref-in-items":       {draft4(`"type": "object", "properties": {"list": {"type": "array", "items": {"$ref": "https://example.com/item.json"}}}`), false},
		"remote-ref-in-definitions": {draft4(`"type": "object", "definitions": {"unused": {"$ref": "https://example.com/unused.json"}}`), false},
	}
}

// A forge plan's schema is published only when Cloud Controller accepts
// it, and Cloud Controller checks it against the draft-04 meta-schema,
// as the OSB spec asks. A schema it would refuse is left out of the
// catalog with an error that names the forge and the plan, and the rest
// of the catalog, the plan included, is still served.
//
//nolint:paralleltest // the test swaps the process-wide logger
func TestCatalog_PublishesOnlyValidDraft4Schemas(t *testing.T) {
	capture := &logCapture{NoOpLogger: &logger.NoOpLogger{}}
	previous := logger.Get()

	logger.Set(capture)
	t.Cleanup(func() { logger.Set(previous) })

	files := map[string]map[string]string{}
	for name, plan := range schemaPlans() {
		files[name] = map[string]string{planFile: "id: " + name + "\n", schemaFile: plan.schema}
	}

	service, err := services.ReadService(writeServiceDir(t, "id: "+schemaForge+"\nname: "+schemaForge+"\ndescription: kv\nbindable: true\n", files))
	if err != nil {
		t.Fatalf("an invalid schema stopped the forge from loading: %v", err)
	}

	publishedPlans, _ := publishedJSON(t, service)[0]["plans"].([]interface{})
	if len(publishedPlans) != len(schemaPlans()) {
		t.Fatalf("the catalog publishes %d plans, want all %d", len(publishedPlans), len(schemaPlans()))
	}

	for _, entry := range publishedPlans {
		plan, _ := entry.(map[string]interface{})
		name, _ := plan["name"].(string)
		_, hasSchemas := plan["schemas"]

		if want := schemaPlans()[name].published; hasSchemas != want {
			t.Errorf("plan %s publishes schemas: %v, want %v", name, hasSchemas, want)
		}

		if !schemaPlans()[name].published && !hasErrorNaming(capture, schemaForge, name) {
			t.Errorf("no error line names forge %s and plan %s", schemaForge, name)
		}
	}

	// The plan keeps the reason its schema was refused, which /b/status
	// reports for the Attention required box.
	for _, plan := range service.Plans {
		refused := plan.SchemaRefused != ""
		if want := !schemaPlans()[plan.Name].published; refused != want {
			t.Errorf("plan %s records a refused schema: %v (%q), want %v", plan.Name, refused, plan.SchemaRefused, want)
		}
	}
}

// hasErrorNaming reports whether an error line names the forge and plan.
func hasErrorNaming(capture *logCapture, forge, plan string) bool {
	capture.mu.Lock()
	defer capture.mu.Unlock()

	for _, line := range capture.lines {
		if strings.HasPrefix(line, "ERROR ") && strings.Contains(line, "forge "+forge) && strings.Contains(line, "plan "+plan+" ") {
			return true
		}
	}

	return false
}
