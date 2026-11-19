package services_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"blacksmith/internal/services"
)

func redisLikeSchema(t *testing.T) map[string]interface{} {
	t.Helper()

	var schema map[string]interface{}

	decoder := json.NewDecoder(strings.NewReader(`{
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
	}`))
	decoder.UseNumber()

	err := decoder.Decode(&schema)
	if err != nil {
		t.Fatal(err)
	}

	return schema
}

func TestValidateParameters_AcceptsMatchingParameters(t *testing.T) {
	t.Parallel()

	err := services.ValidateParameters(redisLikeSchema(t), map[string]interface{}{
		"maxmemory-policy":        "allkeys-lru",
		"slowlog-max-len":         float64(128),
		"slowlog-log-slower-than": float64(1),
		"notify-keyspace-events":  "Ex",
		"ratio":                   0.3,
		"tags":                    []interface{}{"ab", "cd"},
		"nested":                  map[string]interface{}{"a": true},
	})
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
		params map[string]interface{}
		want   string
	}{
		"unknown key":       {map[string]interface{}{"maxmemory": "1gb"}, `parameter "maxmemory" is not a parameter this plan accepts`},
		"wrong type":        {map[string]interface{}{"slowlog-max-len": "many"}, `parameter "slowlog-max-len" must be a value of type integer`},
		"not an integer":    {map[string]interface{}{"slowlog-max-len": 1.5}, `must be a value of type integer`},
		"enum":              {map[string]interface{}{"maxmemory-policy": "volatile-ttl"}, "is not one of the values the plan allows"},
		"maximum":           {map[string]interface{}{"slowlog-max-len": float64(100001)}, "must be less than or equal to 100000"},
		"exclusive minimum": {map[string]interface{}{"slowlog-log-slower-than": float64(0)}, "must be greater than 0"},
		"pattern":           {map[string]interface{}{"notify-keyspace-events": "Q"}, "doesn't match the pattern"},
		"max length":        {map[string]interface{}{"notify-keyspace-events": "KEKEKEKEKEKEK"}, "at most 12 characters"},
		"multiple of":       {map[string]interface{}{"ratio": 0.35}, "must be a multiple of 0.1"},
		"unique items":      {map[string]interface{}{"tags": []interface{}{"ab", "ab"}}, "must not repeat an item"},
		"max items":         {map[string]interface{}{"tags": []interface{}{"ab", "cd", "ef"}}, "at most 2 items"},
		"ref min length":    {map[string]interface{}{"tags": []interface{}{"a"}}, `parameter "tags/0" must be at least 2 characters long`},
		"required":          {map[string]interface{}{"nested": map[string]interface{}{}}, `parameter "nested/a" is required`},
		"no additional":     {map[string]interface{}{"nested": map[string]interface{}{"a": true, "b": 1.0}}, `parameter "nested/b" is not a parameter this plan accepts`},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := services.ValidateParameters(redisLikeSchema(t), tc.params)
			if !errors.Is(err, services.ErrParametersInvalid) {
				t.Fatalf("got %v, want ErrParametersInvalid", err)
			}

			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal %q doesn't say %q", err, tc.want)
			}
		})
	}
}

func TestValidateParameters_RefusalNamesNoValue(t *testing.T) {
	t.Parallel()

	err := services.ValidateParameters(redisLikeSchema(t), map[string]interface{}{"maxmemory-policy": "s3cr3t-value"})
	if err == nil || strings.Contains(err.Error(), "s3cr3t-value") {
		t.Fatalf("the refusal must name the parameter and never its value, got %v", err)
	}
}

func TestValidateParameters_ExplicitAdditionalPropertiesAllowsUndeclaredKeys(t *testing.T) {
	t.Parallel()

	schema := map[string]interface{}{
		"$schema":              "http://json-schema.org/draft-04/schema#",
		"properties":           map[string]interface{}{"a": map[string]interface{}{"type": "string"}},
		"additionalProperties": map[string]interface{}{"type": "integer"},
	}

	err := services.ValidateParameters(schema, map[string]interface{}{"b": float64(3)})
	if err != nil {
		t.Fatalf("an undeclared key the schema's additionalProperties accepts was refused: %s", err)
	}

	err = services.ValidateParameters(schema, map[string]interface{}{"b": "three"})
	if !errors.Is(err, services.ErrParametersInvalid) {
		t.Fatalf("an undeclared key additionalProperties refuses was accepted: %v", err)
	}
}

func TestPlanParameterSchemaSkipsARefusedSchema(t *testing.T) {
	t.Parallel()

	plan := services.Plan{Schemas: &services.Schemas{Parameters: map[string]interface{}{"type": "object"}}}
	if plan.ParameterSchema() == nil {
		t.Fatal("a published schema was not used")
	}

	plan.SchemaRefused = "refused"
	if plan.ParameterSchema() != nil {
		t.Fatal("a schema the catalog refused was used to check parameters")
	}
}
