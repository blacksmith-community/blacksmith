package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The OSB spec requires every schema in a catalog to be a JSON Schema
// draft-04 schema, and Cloud Controller refuses a whole catalog when one
// schema in it is not one, is larger than 64 KB, or refers to anything
// outside itself.
const (
	draft4MetaSchema      = "http://json-schema.org/draft-04/schema#"
	maxParameterSchemaLen = 64 * 1024
	parameterSchemaURL    = "blacksmith:///plan/schema.json"
)

// Errors a parameter schema is refused with.
var (
	ErrPlanSchemaTooLarge   = errors.New("the parameter schema is larger than 64 KB")
	ErrPlanSchemaNotDraft4  = errors.New("the parameter schema names a meta-schema other than JSON Schema draft-04")
	ErrPlanSchemaExternal   = errors.New("the parameter schema refers to a schema outside itself")
	ErrPlanSchemaNotEncoded = errors.New("the parameter schema cannot be encoded as JSON")
	ErrPlanSchemaInvalid    = errors.New("the parameter schema is not a valid JSON Schema draft-04 schema")
	errSchemaLoadRefused    = errors.New("the broker loads no schema from outside the plan")
)

// refusingLoader loads nothing, so compiling a schema never reaches a
// file or the network.
type refusingLoader struct{}

func (refusingLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("%w: %s", errSchemaLoadRefused, url)
}

// ValidateParameterSchema checks schema the way Cloud Controller checks
// a catalog schema. It must encode to at most 64 KB of JSON, name no
// meta-schema but draft-04, hold no $ref outside itself, and be valid
// against the JSON Schema draft-04 meta-schema.
func ValidateParameterSchema(schema map[string]interface{}) error {
	data, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPlanSchemaNotEncoded, err)
	}

	if len(data) > maxParameterSchemaLen {
		return fmt.Errorf("%w: it is %d bytes", ErrPlanSchemaTooLarge, len(data))
	}

	if named, ok := schema["$schema"]; ok && named != draft4MetaSchema {
		return fmt.Errorf("%w: it names %v", ErrPlanSchemaNotDraft4, named)
	}

	if ref, found := externalRef(schema); found {
		return fmt.Errorf("%w: $ref %q", ErrPlanSchemaExternal, ref)
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPlanSchemaNotEncoded, err)
	}

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(refusingLoader{})

	err = compiler.AddResource(parameterSchemaURL, doc)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPlanSchemaInvalid, err)
	}

	_, err = compiler.Compile(parameterSchemaURL)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPlanSchemaInvalid, err)
	}

	return nil
}

// externalRef returns the first $ref in value that does not point inside
// the schema itself, which is any $ref that does not start with "#".
func externalRef(value interface{}) (string, bool) {
	switch typed := value.(type) {
	case map[string]interface{}:
		if ref, ok := typed["$ref"].(string); ok && !strings.HasPrefix(ref, "#") {
			return ref, true
		}

		for _, child := range typed {
			if ref, found := externalRef(child); found {
				return ref, true
			}
		}
	case []interface{}:
		for _, child := range typed {
			if ref, found := externalRef(child); found {
				return ref, true
			}
		}
	}

	return "", false
}
