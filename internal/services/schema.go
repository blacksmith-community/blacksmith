package services

import (
	"errors"
	"fmt"
)

// The OSB spec asks for JSON Schema draft-04, and Cloud Controller
// refuses a whole catalog when one schema in it fails any of its checks.
// A plan whose schema Cloud Controller would refuse is published without
// it, so the broker runs the same checks, in the same order, and refuses
// a schema exactly when Cloud Controller does. Where the broker can't
// tell, it refuses, so it never publishes a schema Cloud Controller
// refuses.
const (
	maxParameterSchemaLen = 64 * 1024

	// maxSchemaNesting bounds how deeply a schema's JSON nests, well
	// inside what Ruby checks without running out of stack.
	maxSchemaNesting = 32
)

// Errors a parameter schema is refused with.
var (
	ErrPlanSchemaTooLarge    = errors.New("the parameter schema is longer than 65,536 characters of JSON")
	ErrPlanSchemaNoMeta      = errors.New("the parameter schema has no $schema key naming its meta-schema")
	ErrPlanSchemaUnknownMeta = errors.New("the parameter schema names a meta-schema Cloud Controller doesn't know")
	ErrPlanSchemaExternal    = errors.New("the parameter schema refers to a schema outside itself")
	ErrPlanSchemaNotEncoded  = errors.New("the parameter schema cannot be encoded as JSON")
	ErrPlanSchemaInvalid     = errors.New("the parameter schema is not a valid JSON Schema draft-04 schema")
	ErrPlanSchemaUnusable    = errors.New("the parameter schema makes Cloud Controller raise an error")
	ErrPlanSchemaUnverified  = errors.New("the broker can't verify that Cloud Controller accepts the parameter schema")
)

// ValidateParameterSchema checks schema the way Cloud Controller checks
// a catalog schema. Its JSON, as Cloud Controller writes it back, must be
// at most 65,536 characters. Its $schema must be neither missing, null,
// nor false. It must be valid against the draft-04 meta-schema. And an
// empty object validated against it must raise nothing but a validation
// error, so every meta-schema it names is one the json-schema gem knows,
// every $ref the gem follows resolves inside it, and every
// patternProperties key the gem compiles is a Ruby regular expression.
func ValidateParameterSchema(schema map[string]interface{}) error {
	served, err := servedSchema(schema)
	if err != nil {
		return err
	}

	if length := rubyJSONLength(served); length > maxParameterSchemaLen {
		return fmt.Errorf("%w: it is %d characters", ErrPlanSchemaTooLarge, length)
	}

	if !truthy(served.get("$schema")) {
		return ErrPlanSchemaNoMeta
	}

	if nesting(served) > maxSchemaNesting {
		return fmt.Errorf("%w: its JSON nests more than %d deep", ErrPlanSchemaUnverified, maxSchemaNesting)
	}

	err = checkDraft4Meta(served, "")
	if err != nil {
		return schemaRefusal(err)
	}

	return schemaRefusal(gemAccepts(served))
}

// schemaRefusal is the error a plan's schema is refused with for what
// the gem raised, and nil for nothing.
func schemaRefusal(err error) error {
	if err == nil || errors.Is(err, ErrPlanSchemaInvalid) {
		return err
	}

	switch faultKindOf(err) {
	case faultUnknownMeta:
		return fmt.Errorf("%w: %w", ErrPlanSchemaUnknownMeta, err)
	case faultReadRefused:
		return fmt.Errorf("%w: %w", ErrPlanSchemaExternal, err)
	case faultUnmodelled:
		return fmt.Errorf("%w: %w", ErrPlanSchemaUnverified, err)
	case faultRaised, faultValidation, faultSchema, faultStack:
	}

	return fmt.Errorf("%w: %w", ErrPlanSchemaUnusable, err)
}

// nesting is how deeply value's JSON nests.
func nesting(value any) int {
	deepest := 0

	switch typed := value.(type) {
	case *rubyHash:
		for _, key := range typed.keys {
			deepest = max(deepest, nesting(typed.values[key]))
		}
	case []any:
		for _, member := range typed {
			deepest = max(deepest, nesting(member))
		}
	default:
		return 0
	}

	return deepest + 1
}
