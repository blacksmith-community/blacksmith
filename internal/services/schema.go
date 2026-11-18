package services

import (
	"errors"
	"fmt"
)

// The OSB spec asks for JSON Schema draft-04, and Cloud Controller
// refuses a whole catalog when one schema in it fails any of its checks.
// So the broker publishes a plan's schema only when it falls inside a
// strict draft-04 profile, which schema_profile.go defines, where every
// rule is enough on its own for Cloud Controller to accept the schema in
// any process, whatever schemas that process has cached before. A schema
// outside the profile is published without, and the refusal names the
// member that broke the rule and what to change. The profile refuses some
// schemas Cloud Controller would accept, such as ones with an id, a
// remote $ref, or another draft, which is the safe direction.
const (
	maxParameterSchemaLen = 64 * 1024

	// maxSchemaNesting bounds how deeply a schema's JSON nests, well
	// inside what Ruby checks without running out of stack.
	maxSchemaNesting = 32
)

// draft4MetaSchemaURI is the $schema a published schema names. The
// profile also takes it without its trailing #.
const draft4MetaSchemaURI = "http://json-schema.org/draft-04/schema#"

// Errors a parameter schema is refused with.
var (
	ErrPlanSchemaTooLarge    = errors.New("the parameter schema is longer than 65,536 characters of JSON")
	ErrPlanSchemaNoMeta      = errors.New("the parameter schema has no $schema key naming its meta-schema")
	ErrPlanSchemaUnknownMeta = errors.New("the parameter schema doesn't name the draft-04 meta-schema")
	ErrPlanSchemaExternal    = errors.New("the parameter schema refers to a schema outside itself")
	ErrPlanSchemaNotEncoded  = errors.New("the parameter schema cannot be encoded as JSON")
	ErrPlanSchemaInvalid     = errors.New("the parameter schema is not a valid JSON Schema draft-04 schema")
	ErrPlanSchemaUnsupported = errors.New("the parameter schema uses JSON Schema the broker doesn't publish")
)

// ValidateParameterSchema checks schema the way the broker decides to
// publish it. Its JSON, as Cloud Controller writes it back, must be at
// most 65,536 characters, which is Cloud Controller's own limit. Its
// $schema must be neither missing, null, nor false, which Cloud
// Controller also checks. And it must fall inside the profile, which is
// enough for Cloud Controller's meta-schema check and its validation of
// an empty object against the schema to pass.
func ValidateParameterSchema(schema map[string]interface{}) error {
	served, err := servedSchema(schema)
	if err != nil {
		return err
	}

	if length := rubyJSONLength(served); length > maxParameterSchemaLen {
		return fmt.Errorf("%w: it is %d characters, so shorten its descriptions or move repeated parts into definitions",
			ErrPlanSchemaTooLarge, length)
	}

	if !truthy(served.get("$schema")) {
		return fmt.Errorf("%w: add \"$schema\": %q at the root", ErrPlanSchemaNoMeta, draft4MetaSchemaURI)
	}

	if path, deep := tooDeep(served, "", 0); deep {
		return profileFault(ErrPlanSchemaUnsupported, path,
			fmt.Sprintf("nests more than %d levels deep", maxSchemaNesting),
			"move the inner part into the root's definitions and refer to it with $ref")
	}

	return checkProfile(served)
}

// truthy is Ruby's truth, where only nil and false are false.
func truthy(value any) bool {
	return value != nil && value != false
}

// tooDeep reports the path of the first value in value's JSON that sits
// deeper than maxSchemaNesting, where value is at depth.
func tooDeep(value any, path string, depth int) (string, bool) {
	switch typed := value.(type) {
	case *rubyHash:
		if depth >= maxSchemaNesting {
			return path, true
		}

		for _, key := range typed.keys {
			if found, deep := tooDeep(typed.values[key], path+"/"+pointerToken(key), depth+1); deep {
				return found, true
			}
		}
	case []any:
		if depth >= maxSchemaNesting {
			return path, true
		}

		for index, member := range typed {
			if found, deep := tooDeep(member, fmt.Sprintf("%s/%d", path, index), depth+1); deep {
				return found, true
			}
		}
	}

	return "", false
}
