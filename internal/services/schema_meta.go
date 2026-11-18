package services

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// draft4MetaSchemaJSON is the draft-04 meta-schema the json-schema gem
// ships, with its members in the gem's order. A $ref into it resolves
// here, as it does in Cloud Controller.
const draft4MetaSchemaJSON = `{"id":"http://json-schema.org/draft-04/schema#","$schema":"http://json-schema.org/draft-04/schema#","description":"Core schema meta-schema","definitions":{"schemaArray":{"type":"array","minItems":1,"items":{"$ref":"#"}},"positiveInteger":{"type":"integer","minimum":0},"positiveIntegerDefault0":{"allOf":[{"$ref":"#/definitions/positiveInteger"},{"default":0}]},"simpleTypes":{"enum":["array","boolean","integer","null","number","object","string"]},"stringArray":{"type":"array","items":{"type":"string"},"minItems":1,"uniqueItems":true}},"type":"object","properties":{"id":{"type":"string","format":"uri"},"$schema":{"type":"string","format":"uri"},"title":{"type":"string"},"description":{"type":"string"},"default":{},"multipleOf":{"type":"number","minimum":0,"exclusiveMinimum":true},"maximum":{"type":"number"},"exclusiveMaximum":{"type":"boolean","default":false},"minimum":{"type":"number"},"exclusiveMinimum":{"type":"boolean","default":false},"maxLength":{"$ref":"#/definitions/positiveInteger"},"minLength":{"$ref":"#/definitions/positiveIntegerDefault0"},"pattern":{"type":"string","format":"regex"},"additionalItems":{"anyOf":[{"type":"boolean"},{"$ref":"#"}],"default":{}},"items":{"anyOf":[{"$ref":"#"},{"$ref":"#/definitions/schemaArray"}],"default":{}},"maxItems":{"$ref":"#/definitions/positiveInteger"},"minItems":{"$ref":"#/definitions/positiveIntegerDefault0"},"uniqueItems":{"type":"boolean","default":false},"maxProperties":{"$ref":"#/definitions/positiveInteger"},"minProperties":{"$ref":"#/definitions/positiveIntegerDefault0"},"required":{"$ref":"#/definitions/stringArray"},"additionalProperties":{"anyOf":[{"type":"boolean"},{"$ref":"#"}],"default":{}},"definitions":{"type":"object","additionalProperties":{"$ref":"#"},"default":{}},"properties":{"type":"object","additionalProperties":{"$ref":"#"},"default":{}},"patternProperties":{"type":"object","additionalProperties":{"$ref":"#"},"default":{}},"dependencies":{"type":"object","additionalProperties":{"anyOf":[{"$ref":"#"},{"$ref":"#/definitions/stringArray"}]}},"enum":{"type":"array","minItems":1,"uniqueItems":true},"type":{"anyOf":[{"$ref":"#/definitions/simpleTypes"},{"type":"array","items":{"$ref":"#/definitions/simpleTypes"},"minItems":1,"uniqueItems":true}]},"allOf":{"$ref":"#/definitions/schemaArray"},"anyOf":{"$ref":"#/definitions/schemaArray"},"oneOf":{"$ref":"#/definitions/schemaArray"},"not":{"$ref":"#"}},"dependencies":{"exclusiveMaximum":["maximum"],"exclusiveMinimum":["minimum"]},"default":{}}`

// isDraft4SimpleType reports whether name is a type name the draft-04
// meta-schema allows.
func isDraft4SimpleType(name string) bool {
	switch name {
	case "array", "boolean", "integer", "null", "number", "object", "string":
		return true
	default:
		return false
	}
}

// checkDraft4Meta is Cloud Controller's first check, the json-schema gem
// validating value against the draft-04 meta-schema, written out by hand
// with the gem's semantics. An integer is a number written without a
// decimal point or an exponent, as Oj reads it, and a uri is anything
// Addressable parses.
//
//nolint:cyclop,gocyclo,gocognit,funlen // one case per meta-schema property keeps it readable against the meta-schema
func checkDraft4Meta(value any, path string) error {
	schema, ok := value.(*rubyHash)
	if !ok {
		return metaFault(path, "is not an object")
	}

	for _, key := range schema.keys {
		member := schema.values[key]
		memberPath := path + "/" + key

		var err error

		switch key {
		case "id", "$schema":
			err = metaURI(member, memberPath)
		case "title", "description", "pattern":
			if _, ok := member.(string); !ok {
				err = metaFault(memberPath, "is not a string")
			}
		case "multipleOf":
			if number, ok := member.(json.Number); !ok || numberSign(number) <= 0 {
				err = metaFault(memberPath, "is not a number greater than 0")
			}
		case "maximum", "minimum":
			if _, ok := member.(json.Number); !ok {
				err = metaFault(memberPath, "is not a number")
			}
		case "exclusiveMaximum", "exclusiveMinimum", "uniqueItems":
			if _, ok := member.(bool); !ok {
				err = metaFault(memberPath, "is not a boolean")
			}
		case "maxLength", "minLength", "maxItems", "minItems", "maxProperties", "minProperties":
			if number, ok := member.(json.Number); !ok || !isRubyInteger(number) || numberSign(number) < 0 {
				err = metaFault(memberPath, "is not an integer of memberPath least 0")
			}
		case "additionalItems", keyAdditionalProperties:
			if _, ok := member.(bool); !ok {
				err = checkDraft4Meta(member, memberPath)
			}
		case "items":
			if list, ok := member.([]any); ok {
				err = metaSchemaArray(list, memberPath)
			} else {
				err = checkDraft4Meta(member, memberPath)
			}
		case "required":
			err = metaStringArray(member, memberPath)
		case keyDefinitions, keyProperties, keyPatternProperties:
			err = metaSchemaMap(member, memberPath, false)
		case keyDependencies:
			err = metaSchemaMap(member, memberPath, true)
		case "enum":
			list, ok := member.([]any)
			if !ok || len(list) == 0 || !rubyUnique(list) {
				err = metaFault(memberPath, "is not an array of memberPath least one unique value")
			}
		case keyType:
			err = metaType(member, memberPath)
		case keyAllOf, keyAnyOf, keyOneOf:
			list, _ := member.([]any)
			err = metaSchemaArray(list, memberPath)
		case keyNot:
			err = checkDraft4Meta(member, memberPath)
		}

		if err != nil {
			return err
		}
	}

	for _, pair := range [][2]string{{"exclusiveMaximum", "maximum"}, {"exclusiveMinimum", "minimum"}} {
		if schema.has(pair[0]) && !schema.has(pair[1]) {
			return metaFault(path+"/"+pair[0], "is set without "+pair[1])
		}
	}

	return nil
}

func metaFault(path, problem string) error {
	if path == "" {
		path = "the schema"
	}

	return fmt.Errorf("%w: %s %s", ErrPlanSchemaInvalid, path, problem)
}

// metaURI is a string with the uri format, which the gem checks by
// parsing it with Addressable.
func metaURI(value any, path string) error {
	text, ok := value.(string)
	if !ok {
		return metaFault(path, "is not a string")
	}

	_, err := parseURIText(text)
	if err != nil && faultKindOf(err) == faultUnmodelled {
		return err
	}

	if err != nil {
		return metaFault(path, "is not a URI")
	}

	return nil
}

// metaSchemaArray is the meta-schema's schemaArray, an array of at
// least one schema.
func metaSchemaArray(list []any, path string) error {
	if len(list) == 0 {
		return metaFault(path, "is not an array of at least one schema")
	}

	for index, member := range list {
		err := checkDraft4Meta(member, fmt.Sprintf("%s/%d", path, index))
		if err != nil {
			return err
		}
	}

	return nil
}

// metaStringArray is the meta-schema's stringArray, an array of at
// least one string with no string twice.
func metaStringArray(value any, path string) error {
	list, ok := value.([]any)
	if !ok || len(list) == 0 || !rubyUnique(list) {
		return metaFault(path, "is not an array of at least one unique string")
	}

	for _, member := range list {
		if _, ok := member.(string); !ok {
			return metaFault(path, "is not an array of at least one unique string")
		}
	}

	return nil
}

// metaSchemaMap is an object of schemas, where a dependency may also be
// a stringArray.
func metaSchemaMap(value any, path string, dependencies bool) error {
	members, ok := value.(*rubyHash)
	if !ok {
		return metaFault(path, "is not an object")
	}

	for _, key := range members.keys {
		member := members.values[key]
		if _, isList := member.([]any); dependencies && isList {
			err := metaStringArray(member, path+"/"+key)
			if err != nil {
				return err
			}

			continue
		}

		err := checkDraft4Meta(member, path+"/"+key)
		if err != nil {
			return err
		}
	}

	return nil
}

// metaType is a simple type name, or an array of at least one with no
// name twice.
func metaType(value any, path string) error {
	if name, ok := value.(string); ok && isDraft4SimpleType(name) {
		return nil
	}

	list, ok := value.([]any)
	if !ok || len(list) == 0 || !rubyUnique(list) {
		return metaFault(path, "is not a type name or an array of unique type names")
	}

	for _, member := range list {
		if name, ok := member.(string); !ok || !isDraft4SimpleType(name) {
			return metaFault(path, "is not a type name or an array of unique type names")
		}
	}

	return nil
}

// rubyUnique reports whether no two members of list are eql? in Ruby,
// which is what Array#uniq! compares, so 1 and 1.0 differ.
func rubyUnique(list []any) bool {
	seen := map[string]bool{}

	for _, member := range list {
		key := rubyEqlKey(member)
		if seen[key] {
			return false
		}

		seen[key] = true
	}

	return true
}

// rubyEqlKey is a string two values share exactly when they are eql? in
// Ruby. A hash's members are compared regardless of their order.
func rubyEqlKey(value any) string {
	switch typed := value.(type) {
	case *rubyHash:
		members := make([]string, 0, len(typed.keys))
		for _, key := range typed.keys {
			members = append(members, rubyEqlKey(key)+":"+rubyEqlKey(typed.values[key]))
		}

		slices.Sort(members)

		return "{" + strings.Join(members, ",") + "}"
	case []any:
		members := make([]string, 0, len(typed))
		for _, member := range typed {
			members = append(members, rubyEqlKey(member))
		}

		return "[" + strings.Join(members, ",") + "]"
	case string:
		return strconvQuote(typed)
	case json.Number:
		return canonicalNumber(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func strconvQuote(text string) string {
	return strconv.Quote(text)
}

// canonicalNumber is a number's class and value, written one way, so an
// Integer and a BigDecimal never share it, and two BigDecimals of equal
// value always do.
func canonicalNumber(number json.Number) string {
	text := string(number)
	if isRubyInteger(number) {
		sign := ""
		if strings.HasPrefix(text, "-") {
			sign = "-"
			text = text[1:]
		}

		digits := strings.TrimLeft(text, "0")
		if digits == "" {
			return "i0"
		}

		return "i" + sign + digits
	}

	negative, digits, point := decimalParts(number)
	if digits == "" {
		return "d0"
	}

	return fmt.Sprintf("d%t0.%se%d", negative, digits, point)
}
