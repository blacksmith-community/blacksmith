package services_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"blacksmith/internal/services"
)

// ccSchemaOfSize is Cloud Controller's create_schema_of_size, a draft-04
// schema whose JSON is size characters long, with its filler held in a
// description so that the profile allows it.
func ccSchemaOfSize(size int) string {
	const prefix = `{"$schema":"http://json-schema.org/draft-04/schema#","description":"`

	const suffix = `"}`

	return prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix
}

// profileCase is a schema and the error the broker refuses it with,
// which is nil when the broker publishes it.
type profileCase struct {
	name   string
	schema string
	want   error
}

// ccSpecCases are the schemas of Cloud Controller's schema_spec.rb. The
// profile refuses three that Cloud Controller accepts, a draft-06 schema
// and a $ref into properties, which is the safe direction.
func ccSpecCases() []profileCase {
	return []profileCase{
		{"cc-draft-04", draft4(`"type": "object"`), nil},
		{"cc-draft-06", `{"$schema": "http://json-schema.org/draft-06/schema#"}`, services.ErrPlanSchemaUnknownMeta},
		{"cc-internal-ref", draft4(`"properties": {"foo": {"type": "integer"}, "bar": {"$ref": "#/properties/foo"}}`),
			services.ErrPlanSchemaUnsupported},
		{"cc-properties-true", draft4(`"properties": true`), services.ErrPlanSchemaInvalid},
		{"cc-properties-and-any-of-true", draft4(`"properties": true, "anyOf": true`), services.ErrPlanSchemaUnsupported},
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

// profileCases each take one rule of the profile.
//
//nolint:funlen // one line per case keeps the rules side by side
func profileCases() []profileCase {
	return []profileCase{
		// Cloud Controller files a schema with an id in a cache that
		// outlives the catalog, so a $ref can resolve through an id to a
		// body some earlier schema filed.
		{"id-with-ref-through-it", draft4(`"id": "http://example.com/valkey-params", "$ref": "#/definitions/tls",
			"definitions": {"tls": {"type": "object"}}`), services.ErrPlanSchemaUnsupported},
		{"id-with-absolute-ref", draft4(`"id": "urn:x", "$ref": "urn:x#/definitions/a", "definitions": {"a": {"type": "string"}}`),
			services.ErrPlanSchemaExternal},
		{"id-in-property", draft4(`"properties": {"a": {"id": "urn:a", "type": "string"}}`), services.ErrPlanSchemaUnsupported},
		{"id-in-definition", draft4(`"definitions": {"a": {"id": "a.json"}}`), services.ErrPlanSchemaUnsupported},
		{"property-named-id", draft4(`"type": "object", "properties": {"id": {"type": "string"}}, "required": ["id"]`), nil},
		{"id-in-data", draft4(`"properties": {"a": {"default": {"id": "x"}, "enum": [{"id": "x"}, {"$ref": "#"}]}}`), nil},

		// The root alone names the meta-schema, and only draft-04.
		{"draft-04-no-fragment", `{"$schema": "http://json-schema.org/draft-04/schema"}`, nil},
		{"draft-04-other-fragment", `{"$schema": "http://json-schema.org/draft-04/schema#foo"}`, services.ErrPlanSchemaUnknownMeta},
		{"draft-04-hyper-schema", `{"$schema": "http://json-schema.org/draft-04/hyper-schema#"}`, services.ErrPlanSchemaUnknownMeta},
		{"draft-03", `{"$schema": "http://json-schema.org/draft-03/schema#"}`, services.ErrPlanSchemaUnknownMeta},
		{"meta-not-a-string", `{"$schema": 4}`, services.ErrPlanSchemaUnknownMeta},
		{"nested-draft-04", draft4(`"properties": {"a": {"$schema": "http://json-schema.org/draft-04/schema#"}}`),
			services.ErrPlanSchemaUnsupported},

		// A $ref names a definition the root holds.
		{"ref-to-definition", draft4(`"$ref": "#/definitions/a", "definitions": {"a": {"type": "object"}}`), nil},
		{"ref-chain", draft4(`"$ref": "#/definitions/a", "definitions": {"a": {"$ref": "#/definitions/b"}, "b": {"type": "object"}}`), nil},
		{"recursive-through-properties", draft4(`"$ref": "#/definitions/node", "definitions": {"node": {"type": "object",
			"properties": {"children": {"type": "array", "items": {"$ref": "#/definitions/node"}}},
			"additionalProperties": {"$ref": "#/definitions/node"}}}`), nil},
		{"ref-to-root", draft4(`"$ref": "#"`), services.ErrPlanSchemaUnsupported},
		{"ref-into-definition", draft4(`"$ref": "#/definitions/a/properties/b",
			"definitions": {"a": {"properties": {"b": {}}}}`), services.ErrPlanSchemaUnsupported},
		{"ref-escaped-name", draft4(`"$ref": "#/definitions/a~1b", "definitions": {"a/b": {}}`), services.ErrPlanSchemaUnsupported},
		{"ref-to-nothing", draft4(`"$ref": "#/definitions/nope"`), services.ErrPlanSchemaInvalid},
		{"ref-not-a-string", draft4(`"$ref": 5`), services.ErrPlanSchemaInvalid},
		{"remote-ref-in-items", draft4(`"items": {"$ref": "https://example.com/item.json"}`), services.ErrPlanSchemaExternal},
		{"remote-ref-in-unused-definition", draft4(`"definitions": {"a": {"$ref": "https://example.com/a.json"}}`),
			services.ErrPlanSchemaExternal},
		{"ref-self-loop", draft4(`"definitions": {"a": {"$ref": "#/definitions/a"}}`), services.ErrPlanSchemaUnsupported},
		{"ref-loop", draft4(`"$ref": "#/definitions/a",
			"definitions": {"a": {"$ref": "#/definitions/b"}, "b": {"$ref": "#/definitions/a"}}`), services.ErrPlanSchemaUnsupported},
		{"nested-definitions", draft4(`"properties": {"a": {"definitions": {"b": {}}}}`), services.ErrPlanSchemaUnsupported},

		// Every other keyword is one the profile lists.
		{"pattern-properties", draft4(`"patternProperties": {"^x-": {}}`), services.ErrPlanSchemaUnsupported},
		{"all-of", draft4(`"allOf": [{"type": "object"}]`), services.ErrPlanSchemaUnsupported},
		{"any-of", draft4(`"anyOf": [{"type": "object"}]`), services.ErrPlanSchemaUnsupported},
		{"one-of", draft4(`"oneOf": [{"type": "object"}]`), services.ErrPlanSchemaUnsupported},
		{"not-keyword", draft4(`"not": {"type": "string"}`), services.ErrPlanSchemaUnsupported},
		{"dependencies-keyword", draft4(`"dependencies": {"a": ["b"]}`), services.ErrPlanSchemaUnsupported},
		{"extends", draft4(`"extends": "https://example.com/base.json"`), services.ErrPlanSchemaUnsupported},
		{"disallow", draft4(`"disallow": "string"`), services.ErrPlanSchemaUnsupported},
		{"additional-items", draft4(`"additionalItems": false`), services.ErrPlanSchemaUnsupported},
		{"multiple-of", draft4(`"multipleOf": 2`), services.ErrPlanSchemaUnsupported},
		{"unknown-keyword", draft4(`"properties": {"a": {"x-display": "Name"}}`), services.ErrPlanSchemaUnsupported},
		{"items-array", draft4(`"items": [{"type": "string"}]`), services.ErrPlanSchemaUnsupported},

		// Each keyword takes the form the draft-04 meta-schema gives it.
		{"exclusive-maximum-alone", draft4(`"exclusiveMaximum": true`), services.ErrPlanSchemaInvalid},
		{"exclusive-maximum", draft4(`"maximum": 5, "exclusiveMaximum": true`), nil},
		{"exclusive-minimum-not-boolean", draft4(`"minimum": 5, "exclusiveMinimum": 1`), services.ErrPlanSchemaInvalid},
		{"duplicate-decimal-enum", draft4(`"enum": [1.5, 1.50]`), services.ErrPlanSchemaInvalid},
		// The broker serves a schema as encoding/json writes it, which
		// writes 1.0 as 1 and 1e2 as 100.
		{"integer-and-decimal-enum", draft4(`"enum": [1, 1.0]`), services.ErrPlanSchemaInvalid},
		{"integral-decimal-min-length", draft4(`"minLength": 1.0, "maxLength": 1e2`), nil},
		{"empty-enum", draft4(`"enum": []`), services.ErrPlanSchemaInvalid},
		{"decimal-min-length", draft4(`"minLength": 1.5`), services.ErrPlanSchemaInvalid},
		{"negative-max-items", draft4(`"maxItems": -1`), services.ErrPlanSchemaInvalid},
		{"large-max-length", draft4(`"maxLength": 1e300`), services.ErrPlanSchemaInvalid},
		{"string-minimum", draft4(`"minimum": "1"`), services.ErrPlanSchemaInvalid},
		{"unknown-type", draft4(`"type": "any"`), services.ErrPlanSchemaInvalid},
		{"repeated-type", draft4(`"type": ["string", "string"]`), services.ErrPlanSchemaInvalid},
		{"type-list", draft4(`"type": ["string", "null"]`), nil},
		{"empty-required", draft4(`"required": []`), services.ErrPlanSchemaInvalid},
		{"repeated-required", draft4(`"required": ["a", "a"]`), services.ErrPlanSchemaInvalid},
		{"additional-properties-number", draft4(`"additionalProperties": 1`), services.ErrPlanSchemaInvalid},
		{"property-not-an-object", draft4(`"properties": {"a": true}`), services.ErrPlanSchemaInvalid},
		{"format-not-a-string", draft4(`"format": 5`), services.ErrPlanSchemaInvalid},

		// Cloud Controller never compiles a pattern, so any string is one.
		{"lookahead-pattern", draft4(`"properties": {"p": {"type": "string", "pattern": "^(?=.*[A-Z])(?=.*\\d).{12,}$"}}`), nil},
		{"unbalanced-pattern", draft4(`"properties": {"p": {"type": "string", "pattern": "(["}}`), nil},
		{"pattern-not-a-string", draft4(`"pattern": ["a"]`), services.ErrPlanSchemaInvalid},

		// The JSON nests at most 32 deep.
		{"nested-32", draft4(`"default": ` + strings.Repeat("[", 31) + strings.Repeat("]", 31)), nil},
		{"nested-33", draft4(`"default": ` + strings.Repeat("[", 32) + strings.Repeat("]", 32)), services.ErrPlanSchemaUnsupported},

		// A schema of the kind a forge ships.
		{"forge-schema", draft4(`"title": "Valkey parameters", "type": "object", "additionalProperties": false,
			"required": ["maxmemory-policy"],
			"definitions": {"memory": {"type": "integer", "minimum": 64, "maximum": 65536}},
			"properties": {
				"maxmemory-policy": {"type": "string", "enum": ["noeviction", "allkeys-lru"], "default": "noeviction"},
				"maxmemory": {"$ref": "#/definitions/memory", "description": "MB of memory <for> the instance & its keys"},
				"tags": {"type": "array", "items": {"type": "string", "minLength": 1, "maxLength": 63, "pattern": "^[a-z]+$"},
					"minItems": 0, "maxItems": 8, "uniqueItems": true},
				"backup": {"type": ["object", "null"], "properties": {"cron": {"type": "string", "format": "cron"}},
					"default": null}}`), nil},
	}
}

// decodeSchema reads a case's schema the way a plan's schema.json is.
func decodeSchema(t *testing.T, text string) map[string]interface{} {
	t.Helper()

	var schema map[string]interface{}

	err := json.Unmarshal([]byte(text), &schema)
	if err != nil {
		t.Fatalf("decode %s: %v", text, err)
	}

	return schema
}

// TestValidateParameterSchema_FollowsTheProfile checks each schema
// against the profile. A schema is published exactly when want is nil.
func TestValidateParameterSchema_FollowsTheProfile(t *testing.T) {
	t.Parallel()

	for _, check := range append(ccSpecCases(), profileCases()...) {
		t.Run(check.name, func(t *testing.T) {
			t.Parallel()

			err := services.ValidateParameterSchema(decodeSchema(t, check.schema))
			if check.want == nil && err != nil {
				t.Fatalf("the profile allows %s, and the broker refuses it: %v", check.name, err)
			}

			if check.want != nil && !errors.Is(err, check.want) {
				t.Fatalf("the broker refuses %s with %v, want %v", check.name, err, check.want)
			}
		})
	}
}

// A refusal reaches the Attention required box, so it names the member
// it refuses and what to change, in words an operator reads, with no
// Ruby exception or Go type in it.
func TestValidateParameterSchema_RefusalsSayWhatToChange(t *testing.T) {
	t.Parallel()

	foreign := []string{
		"Error", "NoMethod", "SystemStack", "Regexp", "interface {}", "[]any", "map[", "*services", "json.Number",
		"rubyHash", "float64", "%!",
	}

	for _, check := range append(ccSpecCases(), profileCases()...) {
		if check.want == nil {
			continue
		}

		err := services.ValidateParameterSchema(decodeSchema(t, check.schema))
		if err == nil {
			t.Errorf("the broker publishes %s", check.name)

			continue
		}

		if !strings.Contains(err.Error(), ", so ") && !errors.Is(err, services.ErrPlanSchemaNoMeta) {
			t.Errorf("the refusal of %s doesn't say what to change: %v", check.name, err)
		}

		for _, word := range foreign {
			if strings.Contains(err.Error(), word) {
				t.Errorf("the refusal of %s shows %q: %v", check.name, word, err)
			}
		}
	}
}

// A refusal names the member that broke the rule by its JSON pointer.
func TestValidateParameterSchema_NamesTheMember(t *testing.T) {
	t.Parallel()

	for schema, want := range map[string]string{
		draft4(`"properties": {"a": {"minLength": -1}}`): "/properties/a/minLength is not an integer of at least 0",
		draft4(`"enum": []`):                             "/enum is not an array of at least one unique value",
		draft4(`"id": "http://example.com/valkey-params", "$ref": "#/definitions/tls", "definitions": {"tls": {}}`): "/id is an id, " +
			"so remove it, because Cloud Controller resolves references through ids it cached from earlier schemas",
		draft4(`"properties": {"a/b": {"patternProperties": {}}}`): "/properties/a~1b/patternProperties is a keyword the " +
			"broker doesn't publish, so remove it, and name each parameter under properties",
		draft4(`"definitions": {"a": {"$ref": "#/definitions/b"}, "b": {"$ref": "#/definitions/a"}}`): "/definitions/b/$ref " +
			"leads back to /definitions/a through $ref alone",
		draft4(`"properties": {"a": {"$ref": "https://example.com/a.json"}}`): "/properties/a/$ref refers to a schema " +
			"outside this one, so copy that schema into the root's definitions",
		draft4(`"default": ` + strings.Repeat("[", 32) + strings.Repeat("]", 32)): "/default" + strings.Repeat("/0", 31) +
			" nests more than 32 levels deep",
		`{"type": "object"}`: `add "$schema": "http://json-schema.org/draft-04/schema#" at the root`,
		`{"$schema": "http://json-schema.org/draft-06/schema#"}`: `/$schema is not the draft-04 meta-schema, so set it to ` +
			`"http://json-schema.org/draft-04/schema#"`,
	} {
		err := services.ValidateParameterSchema(decodeSchema(t, schema))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal of %s is %v, want it to say %q", schema, err, want)
		}
	}
}
