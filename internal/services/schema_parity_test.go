package services_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"blacksmith/internal/services"
)

// ccSchemaOfSize is Cloud Controller's create_schema_of_size, a draft-04
// schema whose JSON is size characters long.
func ccSchemaOfSize(size int) string {
	const surrounding = 62

	return `{"$schema":"http://json-schema.org/draft-04/schema#","foo":"` + strings.Repeat("x", size-surrounding) + `"}`
}

// parityCase is a schema and the error Cloud Controller's verdict on it
// maps to, which is nil when Cloud Controller accepts it.
type parityCase struct {
	name   string
	schema string
	want   error
}

// ccSpecCases are the schemas of Cloud Controller's schema_spec.rb.
func ccSpecCases() []parityCase {
	return []parityCase{
		{"cc-draft-04", draft4(`"type": "object"`), nil},
		{"cc-draft-06", `{"$schema": "http://json-schema.org/draft-06/schema#"}`, nil},
		{"cc-internal-ref", draft4(`"properties": {"foo": {"type": "integer"}, "bar": {"$ref": "#/properties/foo"}}`), nil},
		{"cc-properties-true", draft4(`"properties": true`), services.ErrPlanSchemaInvalid},
		{"cc-properties-and-any-of-true", draft4(`"properties": true, "anyOf": true`), services.ErrPlanSchemaInvalid},
		{"cc-unknown-meta-text", `{"$schema": "blahblahblah"}`, services.ErrPlanSchemaUnknownMeta},
		{"cc-external-ref", draft4(`"$ref": "http://example.com/ref"`), services.ErrPlanSchemaExternal},
		{"cc-unknown-meta-uri", `{"$schema": "http://example.com/schema"}`, services.ErrPlanSchemaUnknownMeta},
		{"cc-file-ref", draft4(`"$ref": "path/to/schema.json"`), services.ErrPlanSchemaExternal},
		{"cc-random-draft", `{"$schema": "http://json-schema.org/some-random-schema/schema#"}`, services.ErrPlanSchemaUnknownMeta},
		{"cc-no-meta", `{}`, services.ErrPlanSchemaNoMeta},
		{"cc-1-kb", ccSchemaOfSize(1024), nil},
		{"cc-63-kb", ccSchemaOfSize(63 * 1024), nil},
		{"cc-64-kb", ccSchemaOfSize(64 * 1024), nil},
		{"cc-65-kb", ccSchemaOfSize(65 * 1024), services.ErrPlanSchemaTooLarge},
		{"cc-10-mb", ccSchemaOfSize(10 * 1024 * 1024), services.ErrPlanSchemaTooLarge},
	}
}

// gemPathCases each take a path of the json-schema gem that the broker
// once got wrong.
func gemPathCases() []parityCase {
	return []parityCase{
		// A nested $schema the gem doesn't know raises SchemaError.
		{"nested-draft-07", draft4(`"properties": {"a": {"$schema": "http://json-schema.org/draft-07/schema#"}}`),
			services.ErrPlanSchemaUnknownMeta},

		// The gem registers an id as it reaches it, so a $ref resolves
		// only to an id the walk has already passed.
		{"ref-to-id-declared-earlier", draft4(`"definitions": {"x": {"id": "urn:x", "type": "string"}},
			"properties": {"a": {"$ref": "urn:x"}}`), nil},
		{"ref-to-id-declared-later", draft4(`"$ref": "urn:x", "definitions": {"x": {"id": "urn:x", "type": "string"}}`),
			services.ErrPlanSchemaExternal},
		{"ref-to-own-id", draft4(`"id": "urn:x", "$ref": "urn:x#/definitions/a", "definitions": {"a": {"type": "string"}}`), nil},
		{"relative-id", draft4(`"id": "http://example.com/root.json", "definitions": {"a": {"id": "a.json", "type": "string"}},
			"properties": {"p": {"$ref": "a.json"}}`), nil},
		{"draft-04-meta-ref", draft4(`"properties": {"a": {"$ref": "http://json-schema.org/draft-04/schema#"}}`), nil},
		{"draft-04-meta-fragment-ref", draft4(`"$ref": "http://json-schema.org/draft-04/schema#/definitions/positiveInteger"`), nil},

		// The gem knows every draft from 1 to 6 and each hyper-schema.
		{"draft-03", `{"$schema": "http://json-schema.org/draft-03/schema#", "type": "object"}`, nil},
		{"draft-06", `{"$schema": "http://json-schema.org/draft-06/schema#", "type": "object"}`, nil},
		{"draft-04-hyper-schema", `{"$schema": "http://json-schema.org/draft-04/hyper-schema#", "type": "object"}`, nil},
		{"draft-04-other-fragment", `{"$schema": "http://json-schema.org/draft-04/schema#foo", "type": "object"}`, nil},

		// The gem's draft-04 dependencies never follows a $ref, nor does
		// not when the data is the empty object, which the gem checks.
		{"remote-ref-in-dependencies", draft4(`"dependencies": {"a": {"$ref": "https://example.com/x"}}`), nil},
		{"remote-ref-in-not", draft4(`"not": {"$ref": "https://example.com/x"}`), nil},
		{"string-dependencies", draft4(`"dependencies": {"$ref": "https://example.com/x"}`), services.ErrPlanSchemaInvalid},
		{"remote-ref-in-items", draft4(`"items": {"$ref": "https://example.com/item.json"}`), services.ErrPlanSchemaExternal},

		// Addressable parses an id of %zz, and its key never resolves.
		{"unparsed-percent-id", draft4(`"id": "%zz"`), nil},
		{"unparsed-percent-id-in-property", draft4(`"properties": {"a": {"id": "%zz"}}`), nil},

		// The gem compiles each root patternProperties key with Ruby's
		// Regexp, and the broker refuses a key outside the subset it
		// checks.
		{"empty-negated-class", draft4(`"patternProperties": {"[^]": {}}`), services.ErrPlanSchemaUnverified},
		{"unbounded-look-behind", draft4(`"patternProperties": {"(?<=a+)b": {}}`), services.ErrPlanSchemaUnverified},
		{"negative-look-ahead", draft4(`"patternProperties": {"^(?!x-)": {}}`), nil},

		// A $ref back to an enclosing schema overflows Ruby's stack.
		{"self-ref", draft4(`"$ref": "#"`), services.ErrPlanSchemaUnusable},
		{"ref-to-nothing", draft4(`"$ref": "#/definitions/nope"`), services.ErrPlanSchemaUnusable},
		{"relative-ref-under-id", draft4(`"id": "%zz", "$ref": "#"`), services.ErrPlanSchemaUnverified},
		{"ref-cycle", draft4(`"definitions": {"a": {"$ref": "#/definitions/b"}, "b": {"$ref": "#/definitions/a"}},
			"allOf": [{"$ref": "#/definitions/a"}]`), services.ErrPlanSchemaUnusable},

		// The draft-04 meta-schema's own rules.
		{"exclusive-maximum-alone", draft4(`"exclusiveMaximum": true`), services.ErrPlanSchemaInvalid},
		{"exclusive-maximum", draft4(`"maximum": 5, "exclusiveMaximum": true`), nil},
		{"duplicate-decimal-enum", draft4(`"enum": [1.5, 1.50]`), services.ErrPlanSchemaInvalid},
	}
}

// TestValidateParameterSchema_MatchesCloudController checks each schema
// against the verdict Cloud Controller reaches on it. A schema is
// accepted exactly when want is nil.
func TestValidateParameterSchema_MatchesCloudController(t *testing.T) {
	t.Parallel()

	for _, check := range append(ccSpecCases(), gemPathCases()...) {
		t.Run(check.name, func(t *testing.T) {
			t.Parallel()

			var schema map[string]interface{}

			err := json.Unmarshal([]byte(check.schema), &schema)
			if err != nil {
				t.Fatalf("decode %s: %v", check.name, err)
			}

			err = services.ValidateParameterSchema(schema)
			if check.want == nil && err != nil {
				t.Fatalf("Cloud Controller accepts %s, and the broker refuses it: %v", check.name, err)
			}

			if check.want != nil && !errors.Is(err, check.want) {
				t.Fatalf("Cloud Controller refuses %s, and the broker returns %v, not %v", check.name, err, check.want)
			}

			if err != nil && strings.Contains(strconv.Quote(err.Error()), `\x01`) {
				t.Errorf("the refusal of %s shows a stand-in URI: %v", check.name, err)
			}
		})
	}
}
