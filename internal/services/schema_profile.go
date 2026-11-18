package services

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The profile is the draft-04 a published schema may use. Cloud
// Controller runs the json-schema gem twice on a schema. It validates the
// schema against the draft-04 meta-schema, and it validates an empty
// object against the schema, where only a validation error is harmless.
// Each rule below keeps one of those runs from failing.
//
//   - The root names draft-04 in $schema, and no other member does, since
//     the gem refuses a nested $schema it doesn't know.
//   - No member is an id. The gem files a schema with an id under that
//     id in a cache that lasts as long as the Cloud Controller process,
//     and the first schema filed under a key wins, so a $ref can resolve
//     to a schema another catalog filed earlier. A schema with no id is
//     filed under a key the gem derives from its whole body, which an
//     identical schema shares and which no other schema takes by chance.
//     Another broker could still file a schema with that key as its id
//     on purpose, and no broker can guard against that.
//   - A $ref is #/definitions/<name>, naming a definition the root
//     holds, so it always resolves inside the schema itself. No chain of
//     definitions refers back to itself through $ref alone, which would
//     send the gem round the loop until Ruby's stack runs out. A $ref
//     under properties, additionalProperties, or items may point back up,
//     because the gem never follows those for an empty object.
//   - Definitions sit at the root only.
//   - Every other member is one of the keywords below, in the form the
//     meta-schema gives it. Each of them is one the gem either never runs
//     for an empty object or runs only to raise a validation error. The
//     profile leaves out the rest, among them patternProperties, whose
//     keys the gem compiles as Ruby regular expressions, allOf, anyOf,
//     oneOf, and not, which the gem follows for an empty object, and the
//     gem's own extends and disallow, which can load a remote schema.
//
// A pattern is any string. The gem compiles a pattern only to match a
// string, which an empty object never is, and the draft-04 meta-schema's
// regex format is one the gem doesn't check.

// profileKeywords lists the keywords the profile allows, in the order a
// refusal names them.
const profileKeywords = "$schema, $ref, definitions, type, properties, required, additionalProperties, items, " +
	"enum, default, title, description, format, pattern, minimum, maximum, exclusiveMinimum, exclusiveMaximum, " +
	"minLength, maxLength, minItems, maxItems, and uniqueItems"

// Keywords the profile gives a structure of its own.
const (
	keySchema               = "$schema"
	keyRef                  = "$ref"
	keyID                   = "id"
	keyDefinitions          = "definitions"
	keyProperties           = "properties"
	keyAdditionalProperties = "additionalProperties"
	keyItems                = "items"
	keyType                 = "type"
	keyRequired             = "required"
	keyEnum                 = "enum"
	keyDefault              = "default"
)

// definitionRefPrefix starts every $ref the profile allows.
const definitionRefPrefix = "#/definitions/"

// isDefinitionName reports whether a $ref may name a definition name,
// which takes letters, digits, _, and -, and so needs no escaping in a
// URI fragment or a JSON pointer.
func isDefinitionName(name string) bool {
	if name == "" {
		return false
	}

	for _, char := range name {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '_', char == '-':
		default:
			return false
		}
	}

	return true
}

// profileRef is a $ref the walk found, at path, naming a definition.
type profileRef struct {
	path string
	name string
}

// profileWalk checks a schema against the profile, member by member.
type profileWalk struct {
	definitions *rubyHash
	refs        []profileRef
}

// checkProfile reports the first member of root that falls outside the
// profile, or nil.
func checkProfile(root *rubyHash) error {
	walk := &profileWalk{}
	if definitions, ok := root.get(keyDefinitions).(*rubyHash); ok {
		walk.definitions = definitions
	}

	err := walk.schema(root, "", true)
	if err != nil {
		return err
	}

	err = walk.resolveRefs()
	if err != nil {
		return err
	}

	return walk.refLoops()
}

// schema checks value, at path, as a schema object.
func (walk *profileWalk) schema(value any, path string, root bool) error {
	schema, ok := value.(*rubyHash)
	if !ok {
		return metaFault(path, "is not an object", "write it as a JSON object")
	}

	for _, key := range schema.keys {
		err := walk.member(key, schema.values[key], path+"/"+pointerToken(key), root)
		if err != nil {
			return err
		}
	}

	for _, pair := range [][2]string{{"exclusiveMaximum", "maximum"}, {"exclusiveMinimum", "minimum"}} {
		if schema.has(pair[0]) && !schema.has(pair[1]) {
			return metaFault(path+"/"+pair[0], "is set without "+pair[1], "add "+pair[1]+", or remove "+pair[0])
		}
	}

	return nil
}

// member checks one member of a schema object, the key at path.
func (walk *profileWalk) member(key string, value any, path string, root bool) error {
	switch key {
	case keySchema:
		return metaSchemaMember(value, path, root)
	case keyID:
		return profileFault(ErrPlanSchemaUnsupported, path, "is an id",
			"remove it, because Cloud Controller resolves references through ids it cached from earlier schemas")
	case keyRef:
		return walk.ref(value, path)
	case keyDefinitions:
		if !root {
			return profileFault(ErrPlanSchemaUnsupported, path, "holds definitions below the root",
				"move them into the root's definitions")
		}

		return walk.schemaMap(value, path)
	case keyProperties:
		return walk.schemaMap(value, path)
	case keyAdditionalProperties, keyItems:
		return walk.subschema(key, value, path)
	case keyType:
		return metaType(value, path)
	case keyRequired:
		return metaStringArray(value, path)
	case keyEnum:
		return metaEnum(value, path)
	case keyDefault:
		return nil
	}

	return valueMember(key, value, path)
}

// subschema checks additionalProperties, which is a boolean or a schema,
// and items, which the profile takes as one schema.
func (walk *profileWalk) subschema(key string, value any, path string) error {
	switch value.(type) {
	case bool:
		if key == keyAdditionalProperties {
			return nil
		}
	case []any:
		if key == keyItems {
			return profileFault(ErrPlanSchemaUnsupported, path, "is an array of schemas", "give items one schema object")
		}
	case *rubyHash:
		return walk.schema(value, path, false)
	}

	if key == keyAdditionalProperties {
		return metaFault(path, "is neither true, false, nor a schema object", "write true, false, or a schema object")
	}

	return metaFault(path, "is not an object", "write it as a JSON object")
}

// metaEnum is an array of at least one value with no value twice.
func metaEnum(value any, path string) error {
	if list, isList := value.([]any); !isList || len(list) == 0 || !rubyUnique(list) {
		return metaFault(path, "is not an array of at least one unique value", "list at least one value, and each value once")
	}

	return nil
}

// metaSchemaMember checks a $schema, which only the root may have.
func metaSchemaMember(value any, path string, root bool) error {
	if !root {
		return profileFault(ErrPlanSchemaUnsupported, path, "names a meta-schema below the root",
			"remove it, because the root's $schema covers the whole schema")
	}

	if text, ok := value.(string); ok && (text == draft4MetaSchemaURI || text == strings.TrimSuffix(draft4MetaSchemaURI, "#")) {
		return nil
	}

	return profileFault(ErrPlanSchemaUnknownMeta, path, "is not the draft-04 meta-schema",
		fmt.Sprintf("set it to %q, the draft the OSB spec asks for", draft4MetaSchemaURI))
}

// valueMember checks a keyword that holds a plain value and no schema.
func valueMember(key string, value any, path string) error {
	var valid bool

	var problem, fix string

	switch key {
	case "title", "description", "format", "pattern":
		_, valid = value.(string)
		problem, fix = "is not a string", "write it as a JSON string"
	case "minimum", "maximum":
		_, valid = value.(json.Number)
		problem, fix = "is not a number", "write it as a JSON number"
	case "exclusiveMinimum", "exclusiveMaximum", "uniqueItems":
		_, valid = value.(bool)
		problem, fix = "is not true or false", "write it as true or false"
	case "minLength", "maxLength", "minItems", "maxItems":
		number, isNumber := value.(json.Number)
		valid = isNumber && isRubyInteger(number) && !strings.HasPrefix(string(number), "-")
		problem, fix = "is not an integer of at least 0", "write it as a whole number of 0 or more, with no decimal point or exponent"
	default:
		return profileFault(ErrPlanSchemaUnsupported, path, "is a keyword the broker doesn't publish",
			unsupportedKeywordFix(key)+". The broker publishes "+profileKeywords)
	}

	if !valid {
		return metaFault(path, problem, fix)
	}

	return nil
}

// unsupportedKeywordFix is what to do instead of a keyword outside the
// profile.
func unsupportedKeywordFix(key string) string {
	switch key {
	case "patternProperties":
		return "remove it, and name each parameter under properties or allow the rest with additionalProperties"
	case "allOf", "anyOf", "oneOf", "not":
		return "remove it, and describe the value with type, enum, and properties"
	case "dependencies":
		return "remove it, and list the parameters that must be given in required"
	default:
		return "remove it"
	}
}

// schemaMap checks an object of schemas, such as properties.
func (walk *profileWalk) schemaMap(value any, path string) error {
	members, ok := value.(*rubyHash)
	if !ok {
		return metaFault(path, "is not an object", "write it as a JSON object of schemas")
	}

	for _, key := range members.keys {
		err := walk.schema(members.values[key], path+"/"+pointerToken(key), false)
		if err != nil {
			return err
		}
	}

	return nil
}

// ref checks a $ref and records the definition it names.
func (walk *profileWalk) ref(value any, path string) error {
	text, ok := value.(string)
	if !ok {
		return metaFault(path, "is not a string", "write it as \"#/definitions/<name>\"")
	}

	if !strings.HasPrefix(text, "#") {
		return profileFault(ErrPlanSchemaExternal, path, "refers to a schema outside this one",
			"copy that schema into the root's definitions and refer to it as #/definitions/<name>")
	}

	name, found := strings.CutPrefix(text, definitionRefPrefix)
	if !found || !isDefinitionName(name) {
		return profileFault(ErrPlanSchemaUnsupported, path, "is not of the form #/definitions/<name>",
			"move the schema it refers to into the root's definitions, under a name of letters, digits, _, and -, "+
				"and refer to it there")
	}

	walk.refs = append(walk.refs, profileRef{path: path, name: name})

	return nil
}

// resolveRefs checks that every $ref names a definition the root holds.
func (walk *profileWalk) resolveRefs() error {
	for _, ref := range walk.refs {
		if walk.definitions == nil || !walk.definitions.has(ref.name) {
			return metaFault(ref.path, "names a definition the root doesn't hold",
				"add "+ref.name+" to the root's definitions, or correct the name")
		}
	}

	return nil
}

// refLoops refuses a definition whose $ref, followed from definition to
// definition, comes back to it.
func (walk *profileWalk) refLoops() error {
	if walk.definitions == nil {
		return nil
	}

	for _, start := range walk.definitions.keys {
		seen := map[string]bool{}

		for name := start; ; {
			seen[name] = true

			next, ok := directRef(walk.definitions.get(name))
			if !ok || !walk.definitions.has(next) {
				break
			}

			if next == start {
				return profileFault(ErrPlanSchemaUnsupported, "/definitions/"+pointerToken(name)+"/$ref",
					"leads back to /definitions/"+pointerToken(start)+" through $ref alone",
					"give one definition in the loop a schema of its own in place of its $ref")
			}

			if seen[next] {
				break
			}

			name = next
		}
	}

	return nil
}

// directRef is the definition a schema's own $ref names.
func directRef(value any) (string, bool) {
	schema, isSchema := value.(*rubyHash)
	if !isSchema {
		return "", false
	}

	text, isText := schema.get(keyRef).(string)
	if !isText {
		return "", false
	}

	return strings.CutPrefix(text, definitionRefPrefix)
}

// metaType is a simple type name, or an array of at least one with no
// name twice.
func metaType(value any, path string) error {
	const problem = "is not a type name or an array of unique type names"

	const fix = "use array, boolean, integer, null, number, object, or string, alone or in an array that names each once"

	if name, ok := value.(string); ok && isDraft4SimpleType(name) {
		return nil
	}

	list, ok := value.([]any)
	if !ok || len(list) == 0 || !rubyUnique(list) {
		return metaFault(path, problem, fix)
	}

	for _, member := range list {
		if name, ok := member.(string); !ok || !isDraft4SimpleType(name) {
			return metaFault(path, problem, fix)
		}
	}

	return nil
}

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

// metaStringArray is the meta-schema's stringArray, an array of at
// least one string with no string twice.
func metaStringArray(value any, path string) error {
	const problem, fix = "is not an array of at least one unique string", "list at least one name, and each name once"

	list, ok := value.([]any)
	if !ok || len(list) == 0 || !rubyUnique(list) {
		return metaFault(path, problem, fix)
	}

	for _, member := range list {
		if _, ok := member.(string); !ok {
			return metaFault(path, problem, fix)
		}
	}

	return nil
}

// metaFault is a refusal for a member the draft-04 meta-schema rejects.
func metaFault(path, problem, fix string) error {
	return profileFault(ErrPlanSchemaInvalid, path, problem, fix)
}

// profileFault is a refusal of kind for the member at path, saying what
// is wrong with it and what to change.
func profileFault(kind error, path, problem, fix string) error {
	where := path
	if where == "" {
		where = "the schema"
	}

	return fmt.Errorf("%w: %s %s, so %s", kind, where, problem, fix)
}

// pointerToken is key as one token of a JSON pointer.
func pointerToken(key string) string {
	return strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}
