package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The OSB spec requires every schema in a catalog to be a JSON Schema
// draft-04 schema, and Cloud Controller refuses a whole catalog when one
// schema in it is not one, is longer than 65,536 characters of JSON, or
// refers to anything outside itself. A plan whose schema fails any of
// these checks is published without it, so each check must refuse only
// what Cloud Controller refuses.
const (
	draft4MetaSchema      = "http://json-schema.org/draft-04/schema"
	maxParameterSchemaLen = 64 * 1024

	// A JSON line separator or paragraph separator is one character
	// to Ruby's to_json, and Go writes it as a six-character escape.
	separatorEscapeExtra = len(` `) - 1
)

// Errors a parameter schema is refused with.
var (
	ErrPlanSchemaTooLarge   = errors.New("the parameter schema is longer than 65,536 characters of JSON")
	ErrPlanSchemaNotDraft4  = errors.New("the parameter schema names a meta-schema other than JSON Schema draft-04")
	ErrPlanSchemaExternal   = errors.New("the parameter schema refers to a schema outside itself")
	ErrPlanSchemaNotEncoded = errors.New("the parameter schema cannot be encoded as JSON")
	ErrPlanSchemaInvalid    = errors.New("the parameter schema is not a valid JSON Schema draft-04 schema")
	errSchemaLoadRefused    = errors.New("the broker loads no schema from outside the plan")
	errSchemaNotURI         = errors.New("not a URI")
)

// refusingLoader loads nothing, so compiling a schema never reaches a
// file or the network.
type refusingLoader struct{}

func (refusingLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("%w: %s", errSchemaLoadRefused, url)
}

// anyPattern stands in for a compiled pattern. Cloud Controller never
// compiles a schema's patterns, and JSON Schema patterns are ECMA-262
// expressions, with lookarounds and backreferences that Go's regexp
// can't compile, so every pattern is accepted as written.
type anyPattern string

func (pattern anyPattern) String() string { return string(pattern) }

func (anyPattern) MatchString(string) bool { return true }

func acceptAnyPattern(pattern string) (jsonschema.Regexp, error) {
	return anyPattern(pattern), nil
}

// lenientURI is the uri and uriref format the way the json-schema gem
// Cloud Controller uses checks it, which accepts any string that parses
// as a URI reference, relative ones included.
func lenientURI(name string) *jsonschema.Format {
	return &jsonschema.Format{Name: name, Validate: func(value any) error {
		text, ok := value.(string)
		if !ok {
			return nil
		}

		_, err := url.Parse(text)
		if err != nil {
			return fmt.Errorf("%w: %w", errSchemaNotURI, err)
		}

		return nil
	}}
}

// draft4Meta compiles the draft-04 meta-schema, which the library embeds,
// so nothing is read from a file or the network.
func draft4Meta() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(refusingLoader{})
	compiler.UseRegexpEngine(acceptAnyPattern)
	compiler.RegisterFormat(lenientURI("uri"))
	compiler.RegisterFormat(lenientURI("uriref"))

	meta, err := compiler.Compile(draft4MetaSchema)
	if err != nil {
		return nil, fmt.Errorf("compiling the draft-04 meta-schema: %w", err)
	}

	return meta, nil
}

// ValidateParameterSchema checks schema the way Cloud Controller checks
// a catalog schema. Its JSON must be at most 65,536 characters long, it
// must name no meta-schema but draft-04, it must hold no $ref outside
// itself where a schema is expected, and it must be valid against the
// draft-04 meta-schema. The schema itself is never compiled, so a
// pattern Go can't compile, or a $ref that resolves to nothing inside
// the schema, is not refused, as Cloud Controller does not refuse it.
func ValidateParameterSchema(schema map[string]interface{}) error {
	length, err := jsonLength(schema)
	if err != nil {
		return err
	}

	if length > maxParameterSchemaLen {
		return fmt.Errorf("%w: it is %d characters", ErrPlanSchemaTooLarge, length)
	}

	if named, ok := schema["$schema"]; ok && !namesDraft4(named) {
		return fmt.Errorf("%w: it names %v", ErrPlanSchemaNotDraft4, named)
	}

	if ref, found := externalRef(schema); found {
		return fmt.Errorf("%w: $ref %q", ErrPlanSchemaExternal, ref)
	}

	meta, err := draft4Meta()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPlanSchemaInvalid, err)
	}

	data, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPlanSchemaNotEncoded, err)
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPlanSchemaNotEncoded, err)
	}

	err = meta.Validate(doc)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPlanSchemaInvalid, err)
	}

	return nil
}

// jsonLength is the length of schema.to_json in Cloud Controller, which
// counts characters, not bytes. Ruby writes a non-ASCII character, and
// <, >, and &, as themselves. Go escapes U+2028 and U+2029 even with
// HTML escaping off, so each of those counts once.
func jsonLength(schema map[string]interface{}) (int, error) {
	var buffer bytes.Buffer

	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)

	err := encoder.Encode(schema)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrPlanSchemaNotEncoded, err)
	}

	data := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	separators := bytes.Count(data, []byte(` `)) + bytes.Count(data, []byte(` `))

	return utf8.RuneCount(data) - separators*separatorEscapeExtra, nil
}

// namesDraft4 reports whether a $schema value names the draft-04
// meta-schema, which is written with or without its empty fragment.
func namesDraft4(named interface{}) bool {
	text, ok := named.(string)

	return ok && strings.TrimSuffix(text, "#") == draft4MetaSchema
}

// externalRef returns the first $ref, where a schema is expected, that
// points outside the schema. A $ref is inside when it is a fragment of
// the schema, or names the id of the schema or of one inside it. A $ref
// in data, such as a default or an enum, is not a reference at all.
func externalRef(schema map[string]interface{}) (string, bool) {
	ids := map[string]bool{}

	walkSchemas(schema, func(sub map[string]interface{}) {
		if id, ok := sub["id"].(string); ok {
			ids[withoutFragment(id)] = true
		}
	})

	var external string

	walkSchemas(schema, func(sub map[string]interface{}) {
		ref, ok := sub["$ref"].(string)
		if !ok || external != "" || strings.HasPrefix(ref, "#") || ids[withoutFragment(ref)] {
			return
		}

		external = ref
	})

	return external, external != ""
}

func withoutFragment(uri string) string {
	before, _, _ := strings.Cut(uri, "#")

	return before
}

// walkSchemas calls visit on schema and on every schema inside it, in
// the places the json-schema gem looks for one when it loads a schema's
// references. Those are type and disallow unions, definitions,
// properties, and patternProperties values, additionalProperties,
// additionalItems, and dependencies when they hold a schema, items,
// extends, and the members of allOf, anyOf, oneOf, and an array not.
func walkSchemas(schema map[string]interface{}, visit func(map[string]interface{})) {
	visit(schema)

	each := func(value interface{}) {
		switch typed := value.(type) {
		case map[string]interface{}:
			walkSchemas(typed, visit)
		case []interface{}:
			for _, member := range typed {
				if sub, ok := member.(map[string]interface{}); ok {
					walkSchemas(sub, visit)
				}
			}
		}
	}

	for _, key := range []string{"type", "disallow", "allOf", "anyOf", "oneOf", "items", "extends"} {
		each(schema[key])
	}

	if not, ok := schema["not"].([]interface{}); ok {
		each(not)
	}

	for _, key := range []string{"definitions", "properties", "patternProperties", "dependencies"} {
		members, _ := schema[key].(map[string]interface{})
		for _, member := range members {
			if sub, ok := member.(map[string]interface{}); ok {
				walkSchemas(sub, visit)
			}
		}
	}

	for _, key := range []string{"additionalProperties", "additionalItems"} {
		if sub, ok := schema[key].(map[string]interface{}); ok {
			walkSchemas(sub, visit)
		}
	}
}
