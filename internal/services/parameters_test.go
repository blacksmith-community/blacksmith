package services_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"blacksmith/internal/services"
)

// decodeObject decodes a JSON object, with its numbers as json.Number
// when useNumber is set, as the catalog reads schema.json.
func decodeObject(t *testing.T, text string, useNumber bool) map[string]interface{} {
	t.Helper()

	var object map[string]interface{}

	decoder := json.NewDecoder(strings.NewReader(text))
	if useNumber {
		decoder.UseNumber()
	}

	err := decoder.Decode(&object)
	if err != nil {
		t.Fatal(err)
	}

	return object
}

func redisLikeSchema(t *testing.T) map[string]interface{} {
	t.Helper()

	return decodeObject(t, `{
		"$schema": "http://json-schema.org/draft-04/schema#",
		"type": "object",
		"properties": {
			"maxmemory-policy": {"type": "string", "enum": ["noeviction", "allkeys-lru"]},
			"slowlog-max-len": {"type": "integer", "minimum": 0, "maximum": 100000},
			"slowlog-log-slower-than": {"type": "integer", "minimum": 0, "exclusiveMinimum": true},
			"notify-keyspace-events": {"type": "string", "pattern": "^[KEg$lshzxeAtm]*$", "maxLength": 12},
			"ratio": {"type": "number", "multipleOf": 0.1},
			"tags": {"type": "array", "items": {"$ref": "#/definitions/tag"}, "uniqueItems": true, "maxItems": 2},
			"nested": {"type": "object", "required": ["a"], "additionalProperties": false, "properties": {"a": {"type": "boolean"}}}
		},
		"definitions": {"tag": {"type": "string", "minLength": 2}}
	}`, true)
}

func TestValidateParameters_AcceptsMatchingParameters(t *testing.T) {
	t.Parallel()

	err := services.ValidateParameters(redisLikeSchema(t), decodeObject(t, `{
		"maxmemory-policy": "allkeys-lru", "slowlog-max-len": 128, "slowlog-log-slower-than": 1,
		"notify-keyspace-events": "Ex", "ratio": 0.3, "tags": ["ab", "cd"], "nested": {"a": true}
	}`, false))
	if err != nil {
		t.Fatalf("matching parameters were refused: %s", err)
	}
}

func TestValidateParameters_NilSchemaAcceptsAnything(t *testing.T) {
	t.Parallel()

	err := services.ValidateParameters(nil, map[string]interface{}{"anything": "goes"})
	if err != nil {
		t.Fatalf("a plan without a schema refused a parameter: %s", err)
	}
}

func TestValidateParameters_RefusesEachRule(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		params string
		want   string
	}{
		"unknown key":       {`{"maxmemory": "1gb"}`, `parameter "maxmemory" is not a parameter this plan accepts`},
		"wrong type":        {`{"slowlog-max-len": "many"}`, `parameter "slowlog-max-len" must be a value of type integer`},
		"not an integer":    {`{"slowlog-max-len": 1.5}`, `must be a value of type integer`},
		"enum":              {`{"maxmemory-policy": "volatile-ttl"}`, "is not one of the values the plan allows"},
		"maximum":           {`{"slowlog-max-len": 100001}`, "must be less than or equal to 100000"},
		"exclusive minimum": {`{"slowlog-log-slower-than": 0}`, "must be greater than 0"},
		"pattern":           {`{"notify-keyspace-events": "Q"}`, "doesn't match the pattern"},
		"max length":        {`{"notify-keyspace-events": "KEKEKEKEKEKEK"}`, "at most 12 characters"},
		"multiple of":       {`{"ratio": 0.35}`, "must be a multiple of 0.1"},
		"unique items":      {`{"tags": ["ab", "ab"]}`, "must not repeat an item"},
		"max items":         {`{"tags": ["ab", "cd", "ef"]}`, "at most 2 items"},
		"ref min length":    {`{"tags": ["a"]}`, `parameter "tags/0" must be at least 2 characters long`},
		"required":          {`{"nested": {}}`, `parameter "nested/a" is required`},
		"no additional":     {`{"nested": {"a": true, "b": 1}}`, `parameter "nested/b" is not a parameter this plan accepts`},
	}

	for name, refusal := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := services.ValidateParameters(redisLikeSchema(t), decodeObject(t, refusal.params, false))
			if !errors.Is(err, services.ErrParametersInvalid) {
				t.Fatalf("got %v, want ErrParametersInvalid", err)
			}

			if !strings.Contains(err.Error(), refusal.want) {
				t.Fatalf("refusal %q doesn't say %q", err, refusal.want)
			}
		})
	}
}

func TestValidateParameters_RefusalNamesNoValue(t *testing.T) {
	t.Parallel()

	err := services.ValidateParameters(redisLikeSchema(t), decodeObject(t, `{"maxmemory-policy": "s3cr3t-value"}`, false))
	if err == nil || strings.Contains(err.Error(), "s3cr3t-value") {
		t.Fatalf("the refusal must name the parameter and never its value, got %v", err)
	}
}

func TestValidateParameters_ExplicitAdditionalPropertiesAllowsUndeclaredKeys(t *testing.T) {
	t.Parallel()

	schema := decodeObject(t, `{
		"$schema": "http://json-schema.org/draft-04/schema#",
		"properties": {"a": {"type": "string"}},
		"additionalProperties": {"type": "integer"}
	}`, false)

	err := services.ValidateParameters(schema, decodeObject(t, `{"b": 3}`, false))
	if err != nil {
		t.Fatalf("an undeclared key the schema's additionalProperties accepts was refused: %s", err)
	}

	err = services.ValidateParameters(schema, decodeObject(t, `{"b": "three"}`, false))
	if !errors.Is(err, services.ErrParametersInvalid) {
		t.Fatalf("an undeclared key additionalProperties refuses was accepted: %v", err)
	}
}

func TestPlanParameterSchemaSkipsARefusedSchema(t *testing.T) {
	t.Parallel()

	plan := services.Plan{Schemas: &services.Schemas{Parameters: decodeObject(t, `{"type": "object"}`, false)}}
	if plan.ParameterSchema() == nil {
		t.Fatal("a published schema was not used")
	}

	plan.SchemaRefused = "refused"
	if plan.ParameterSchema() != nil {
		t.Fatal("a schema the catalog refused was used to check parameters")
	}
}
