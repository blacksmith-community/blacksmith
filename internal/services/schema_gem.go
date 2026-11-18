package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Cloud Controller checks a catalog schema with the json-schema gem. It
// validates the schema against the draft-04 meta-schema, and then it
// validates an empty object against the schema, refusing the catalog
// when that second step raises anything other than a validation error.
// That step resolves every $ref and id in the schema, loads nothing from
// outside it, and compiles each patternProperties key, so a schema is
// refused for what it refers to as well as for what it is. What follows
// ports that second step, value by value, from the gem's own code.

// gemFaultKind is the class of what the gem raised.
type gemFaultKind int

const (
	// faultRaised is any exception without a kind of its own below.
	faultRaised gemFaultKind = iota
	// faultValidation is JSON::Schema::ValidationError, which Cloud
	// Controller takes as the schema being usable.
	faultValidation
	// faultSchema is JSON::Schema::SchemaError.
	faultSchema
	// faultUnknownMeta is the SchemaError raised for a $schema the gem
	// has no validator for.
	faultUnknownMeta
	// faultReadRefused is JSON::Schema::ReadRefused, raised for a
	// schema the gem would have to load from outside.
	faultReadRefused
	// faultStack is SystemStackError, from a schema that refers to
	// itself without end.
	faultStack
	// faultUnmodelled is a case this port does not decide, which is
	// refused, because Cloud Controller might refuse it.
	faultUnmodelled
)

type gemError struct {
	kind    gemFaultKind
	message string
}

func (err *gemError) Error() string {
	return err.message
}

// gemTokenNames rewrites the broker's stand-ins for the root schema's URI
// and Cloud Controller's working directory, raw or quoted, so a refusal
// reads as the schema wrote it.
var gemTokenNames = strings.NewReplacer( //nolint:gochecknoglobals // a fixed replacer, built once and only read
	gemWorkingDir, ".", strings.ReplaceAll(gemWorkingDir, "\x01", `\x01`), ".",
	gemRootPath, "", strings.ReplaceAll(gemRootPath, "\x01", `\x01`), "",
)

func gemFault(kind gemFaultKind, format string, args ...any) error {
	return &gemError{kind: kind, message: gemTokenNames.Replace(fmt.Sprintf(format, args...))}
}

// faultKindOf is the kind of err, which is faultRaised for an error the
// port didn't make itself.
func faultKindOf(err error) gemFaultKind {
	var fault *gemError
	if errors.As(err, &fault) {
		return fault.kind
	}

	return faultRaised
}

func isValidationFault(err error) bool {
	return err != nil && faultKindOf(err) == faultValidation
}

// Bounds on the port's own work. Ruby raises SystemStackError well
// before a chain of schemas this deep, and Cloud Controller would spend
// minutes on a schema that takes this many steps, so both are refused.
const (
	maxGemDepth = 48
	maxGemSteps = 100000
)

// gemDraft is one of the gem's validators, named by the meta-schema URI
// it is registered under.
type gemDraft struct {
	key     string
	version int
}

// gemDrafts are the gem's registered validators, by the scheme, host,
// and path of their meta-schema URI.
//
//nolint:gochecknoglobals // the gem's validator registry, built once and only read
var gemDrafts = func() map[string]*gemDraft {
	drafts := map[string]*gemDraft{}

	for _, version := range []int{1, 2, 3, 4, 6} {
		for _, name := range []string{"schema", "hyper-schema"} {
			key := fmt.Sprintf("http://json-schema.org/draft-%02d/%s", version, name)
			drafts[key] = &gemDraft{key: key, version: version}
		}
	}

	return drafts
}()

// gemDefaultDraftKey is the validator a schema with no $schema gets.
const gemDefaultDraftKey = "http://json-schema.org/draft-06/schema"

// The draft versions whose rules differ for an empty object.
const (
	draft3 = 3
	draft4 = 4
	draft6 = 6
)

// The schema keys the gem's attributes and the meta-schema check look
// for in more than one place.
const (
	keyType                 = "type"
	keyDisallow             = "disallow"
	keyAllOf                = "allOf"
	keyAnyOf                = "anyOf"
	keyOneOf                = "oneOf"
	keyProperties           = "properties"
	keyDefinitions          = "definitions"
	keyNot                  = "not"
	keyDependencies         = "dependencies"
	keyPatternProperties    = "patternProperties"
	keyAdditionalProperties = "additionalProperties"
)

// gemDraft4MetaID is the id of the draft-04 meta-schema, which Cloud
// Controller's first check leaves in the gem's cache.
const gemDraft4MetaID = "http://json-schema.org/draft-04/schema#"

// gemSchema is a JSON::Schema, a value with the URI it resolves against
// and the validator that checks it.
type gemSchema struct {
	body  any
	uri   *rubyURI
	draft *gemDraft
}

// gemRun is one call of JSON::Validator.validate!(schema, {}).
type gemRun struct {
	cache        map[string]*gemSchema
	cacheUnknown bool
	validating   map[string]bool
	removing     map[string]bool
	depth        int
	steps        int
}

// gemAccepts reports, as an error, whether Cloud Controller's second
// check passes root, which is nil when it does.
func gemAccepts(root *rubyHash) error {
	meta, err := draft4MetaGemSchema()
	if err != nil {
		return err
	}

	run := &gemRun{
		cache:      map[string]*gemSchema{gemDraft4MetaID: meta},
		validating: map[string]bool{},
		removing:   map[string]bool{},
	}

	schema, err := run.newSchema(root, &rubyURI{path: gemRootPath}, gemDrafts[gemDefaultDraftKey])
	if err == nil {
		err = run.addSchema(schema)
	}

	if err == nil {
		err = run.build(schema)
	}

	if err == nil {
		err = run.validate(schema)
	}

	if err == nil || isValidationFault(err) {
		return nil
	}

	return err
}

// draft4MetaGemSchema is the draft-04 meta-schema as the gem holds it.
func draft4MetaGemSchema() (*gemSchema, error) {
	body, err := decodeRuby([]byte(draft4MetaSchemaJSON))
	if err != nil {
		return nil, fmt.Errorf("reading the draft-04 meta-schema: %w", err)
	}

	uri, err := parseURIText(gemDraft4MetaID)
	if err != nil {
		return nil, err
	}

	return &gemSchema{body: body, uri: uri, draft: gemDrafts["http://json-schema.org/draft-04/schema"]}, nil
}

func truthy(value any) bool {
	return value != nil && value != false
}

// rubyIndex is value[key] in Ruby, for each kind of value a schema holds.
// A string answers key when it contains key, and nil otherwise.
func rubyIndex(value any, key string) (any, error) {
	switch typed := value.(type) {
	case *rubyHash:
		return typed.get(key), nil
	case string:
		if strings.Contains(typed, key) {
			return key, nil
		}

		return nil, nil //nolint:nilnil // a string without key answers nil, which is no error
	case []any:
		return nil, gemFault(faultRaised, "TypeError: an array indexed by %q", key)
	case json.Number:
		if isRubyInteger(typed) {
			return nil, gemFault(faultRaised, "TypeError: an integer indexed by %q", key)
		}

		return nil, gemFault(faultRaised, "NoMethodError: a decimal indexed by %q", key)
	default:
		return nil, gemFault(faultRaised, "NoMethodError: %v indexed by %q", typed, key)
	}
}

// newSchema is JSON::Schema.new(body, uri, parent).
func (run *gemRun) newSchema(body any, uri *rubyURI, parent *gemDraft) (*gemSchema, error) {
	idValue, err := rubyIndex(body, "id")
	if err != nil {
		return nil, err
	}

	schemaURI := uri

	if text, ok := idValue.(string); ok {
		parsed, err := parseURIText(text)
		if err != nil {
			return nil, err
		}

		if parsed.scheme == nil {
			parsed, err = uri.join(parsed)
			if err != nil {
				return nil, err
			}
		}

		schemaURI = parsed
	}

	schemaURI, err = schemaURI.stripFragment()
	if err != nil {
		return nil, err
	}

	named, err := rubyIndex(body, "$schema")
	if err != nil {
		return nil, err
	}

	draft := parent
	if truthy(named) {
		draft, err = gemDraftFor(named)
		if err != nil {
			return nil, err
		}
	}

	return &gemSchema{body: body, uri: schemaURI, draft: draft}, nil
}

// gemDraftFor is JSON::Validator.validator_for_uri, which finds a
// validator by the scheme, host, and path of a $schema.
func gemDraftFor(named any) (*gemDraft, error) {
	text, ok := named.(string)
	if !ok {
		return nil, gemFault(faultRaised, "TypeError: a $schema that is not a string")
	}

	uri, err := parseURIText(text)
	if err != nil {
		return nil, err
	}

	key := orEmpty(uri.scheme) + "://" + orEmpty(uri.host) + uri.path

	draft, found := gemDrafts[key]
	if !found {
		return nil, gemFault(faultUnknownMeta, "it names %q", text)
	}

	return draft, nil
}

func orEmpty(text *string) string {
	if text == nil {
		return ""
	}

	return *text
}

// addSchema is JSON::Validator.add_schema, which keeps the first schema
// cached under a key.
func (run *gemRun) addSchema(schema *gemSchema) error {
	key, err := gemSchemaKey(schema.uri)
	if faultKindOf(err) == faultUnmodelled {
		run.cacheUnknown = true

		return nil
	}

	if err != nil {
		return err
	}

	if _, found := run.cache[key]; !found {
		run.cache[key] = schema
	}

	return nil
}

// lookup is JSON::Validator.schema_for_uri on a string.
func (run *gemRun) lookup(key string) (*gemSchema, error) {
	if run.cacheUnknown {
		return nil, gemFault(faultUnmodelled, "a schema is cached under a key that depends on Cloud Controller's files")
	}

	if schema, found := run.cache[key]; found {
		return schema, nil
	}

	uri, err := parseGemURIText(key)
	if err != nil {
		return nil, err
	}

	normalized, err := gemSchemaKey(uri)
	if err != nil {
		return nil, err
	}

	return run.cache[normalized], nil
}

// loadRef is JSON::Validator#load_ref_schema, which refuses any schema
// not already cached, because Cloud Controller reads nothing.
func (run *gemRun) loadRef(parent *gemSchema, ref any) error {
	text, err := refText(ref)
	if err != nil {
		return err
	}

	parsed, err := parseURIText(text)
	if err != nil {
		return err
	}

	stripped := parsed
	if parsed.fragment != nil && *parsed.fragment != "" {
		stripped, err = parsed.withFragment("")
		if err != nil {
			return err
		}
	}

	target := stripped

	if stripped.scheme == nil {
		base, err := parent.uri.stripFragment()
		if err != nil {
			return err
		}

		target, err = joinPath(base, stripped.path)
		if err != nil {
			return err
		}
	}

	key, err := gemSchemaKey(target)
	if err != nil {
		return err
	}

	if run.cacheUnknown {
		return gemFault(faultUnmodelled, "a schema is cached under a key that depends on Cloud Controller's files")
	}

	if _, found := run.cache[key]; found {
		return nil
	}

	return gemFault(faultReadRefused, "the $ref %q names a schema outside the plan's schema", text)
}

// joinPath is base.join(path) for a path string.
func joinPath(base *rubyURI, path string) (*rubyURI, error) {
	if path == "" {
		return base.dup()
	}

	other, err := parseURIText(path)
	if err != nil {
		return nil, err
	}

	return base.join(other)
}

// build is JSON::Validator#build_schemas, which caches every schema with
// an id and loads every $ref, in the places it looks for them.
//
//nolint:cyclop,gocognit,funlen // one branch per place build_schemas looks keeps it readable against the gem
func (run *gemRun) build(parent *gemSchema) error {
	body, ok := parent.body.(*rubyHash)
	if !ok {
		return nil
	}

	if ref := body.get("$ref"); truthy(ref) {
		err := run.loadRef(parent, ref)
		if err != nil {
			return err
		}
	}

	switch extends := body.get("extends").(type) {
	case string:
		err := run.loadRef(parent, extends)
		if err != nil {
			return err
		}
	case []any:
		for _, member := range extends {
			err := run.handle(parent, member)
			if err != nil {
				return err
			}
		}
	}

	for _, key := range []string{keyType, keyDisallow} {
		members, _ := body.get(key).([]any)
		for _, member := range members {
			err := run.handle(parent, member)
			if err != nil {
				return err
			}
		}
	}

	for _, key := range []string{keyDefinitions, keyProperties, keyPatternProperties} {
		members, err := eachSecond(body.get(key))
		if err != nil {
			return err
		}

		for _, member := range members {
			err = run.handle(parent, member)
			if err != nil {
				return err
			}
		}
	}

	for _, key := range []string{keyAdditionalProperties, "additionalItems", keyDependencies, "extends"} {
		err := run.handle(parent, body.get(key))
		if err != nil {
			return err
		}
	}

	for _, key := range []string{keyAllOf, keyAnyOf, keyOneOf, keyNot} {
		if members, ok := body.get(key).([]any); ok {
			for _, member := range members {
				err := run.handle(parent, member)
				if err != nil {
					return err
				}
			}
		}
	}

	items := body.get("items")
	if truthy(items) {
		members, ok := items.([]any)
		if !ok {
			members = []any{items}
		}

		for _, member := range members {
			err := run.handle(parent, member)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// eachSecond is what value.each { |_k, inner| } yields as inner: a
// hash's values, or the second member of each array in an array.
func eachSecond(value any) ([]any, error) {
	switch typed := value.(type) {
	case nil, bool:
		if !truthy(typed) {
			return nil, nil
		}
	case *rubyHash:
		values := make([]any, 0, len(typed.keys))
		for _, key := range typed.keys {
			values = append(values, typed.values[key])
		}

		return values, nil
	case []any:
		values := []any{}

		for _, member := range typed {
			if pair, ok := member.([]any); ok && len(pair) >= 2 {
				values = append(values, pair[1])
			}
		}

		return values, nil
	}

	return nil, gemFault(faultRaised, "NoMethodError: each on %T", value)
}

// handle is JSON::Validator#handle_schema.
func (run *gemRun) handle(parent *gemSchema, value any) error {
	body, ok := value.(*rubyHash)
	if !ok {
		return nil
	}

	uri, err := parent.uri.dup()
	if err != nil {
		return err
	}

	schema, err := run.newSchema(body, uri, parent.draft)
	if err != nil {
		return err
	}

	if truthy(body.get("id")) {
		err = run.addSchema(schema)
		if err != nil {
			return err
		}
	}

	return run.build(schema)
}

// visitKey identifies a schema by its value, URI, and validator. The
// same key met again inside its own check repeats forever in Ruby.
func visitKey(schema *gemSchema) string {
	text, _ := schema.uri.String()

	return fmt.Sprintf("%p|%s|%s", schema.body, text, schema.draft.key)
}

func (run *gemRun) enter(schema *gemSchema, active map[string]bool) (string, error) {
	run.steps++
	if run.steps > maxGemSteps {
		return "", gemFault(faultUnmodelled, "checking the schema takes more than %d steps", maxGemSteps)
	}

	if run.depth >= maxGemDepth {
		return "", gemFault(faultUnmodelled, "the schema's references nest more than %d deep", maxGemDepth)
	}

	key := ""
	if _, ok := schema.body.(*rubyHash); ok {
		key = visitKey(schema)
		if active[key] {
			return "", gemFault(faultStack, "the schema refers to itself without end")
		}

		active[key] = true
	}

	run.depth++

	return key, nil
}

func (run *gemRun) leave(key string, active map[string]bool) {
	run.depth--

	if key != "" {
		delete(active, key)
	}
}

// validate is JSON::Schema#validate on an empty object, which calls the
// validator's attribute for each key of the schema, in order.
func (run *gemRun) validate(schema *gemSchema) error {
	key, err := run.enter(schema, run.validating)
	if err != nil {
		return err
	}
	defer run.leave(key, run.validating)

	body, ok := schema.body.(*rubyHash)
	if !ok {
		return gemFault(faultRaised, "NoMethodError: each on %T", schema.body)
	}

	for _, name := range body.keys {
		err = run.attribute(schema, body, name)
		if err != nil {
			return err
		}
	}

	return nil
}

// attribute runs the attribute named name, when the schema's validator
// has one that does anything with an empty object.
//
//nolint:cyclop,gocyclo,gocognit,funlen // one case per attribute keeps the port readable against the gem
func (run *gemRun) attribute(schema *gemSchema, body *rubyHash, name string) error {
	version := schema.draft.version
	modern := version >= draft4

	switch {
	case name == keyType && modern, name == keyDisallow && modern:
		return typeV4(body.get(keyType))
	case name == keyType && !modern:
		return run.typeUnion(schema, body.get(keyType), false)
	case name == keyDisallow && !modern:
		return run.typeUnion(schema, body.get(keyDisallow), true)
	case name == keyAllOf && modern, name == keyAnyOf && modern, name == keyOneOf && modern:
		return run.combine(schema, name, body.get(name))
	case name == keyNot && modern:
		return run.not(schema, body.get(keyNot))
	case name == "minProperties" && modern:
		return compareZero(body.get(name), 1)
	case name == "maxProperties" && modern:
		return compareZero(body.get(name), -1)
	case name == "enum":
		return enumHasEmptyObject(body.get("enum"))
	case name == "const" && version == draft6:
		if hash, ok := body.get("const").(*rubyHash); ok && len(hash.keys) == 0 {
			return nil
		}

		return gemFault(faultValidation, "const")
	case name == keyProperties:
		return run.properties(version, body.get(keyProperties))
	case name == "required" && modern:
		return required(body.get("required"))
	case name == keyPatternProperties && version >= draft3:
		return compileEachPattern(body.get(keyPatternProperties))
	case name == keyAdditionalProperties:
		kind := body.get(keyType)
		if kind != nil && kind != "object" {
			return nil
		}

		return run.removeValidProperties(schema, schema.draft)
	case name == keyDependencies && version >= draft3:
		switch body.get(keyDependencies).(type) {
		case *rubyHash, []any:
			return nil
		}

		return gemFault(faultRaised, "NoMethodError: each on dependencies")
	case name == "propertyNames" && version == draft6:
		if names, ok := body.get("propertyNames").(*rubyHash); ok {
			_, err := run.newSchema(names, schema.uri, schema.draft)

			return err
		}

		return nil
	case name == "extends":
		return run.extends(schema, body.get("extends"))
	case name == "$ref" && version >= draft3:
		sub, err := run.referenced(body, schema, schema.draft)
		if err != nil {
			return err
		}

		if sub == nil {
			return gemFault(faultValidation, "$ref")
		}

		return run.validate(sub)
	}

	return nil
}

// matchesEmptyObject reports whether an empty object is of type kind.
// Only string, number, integer, boolean, array, and null rule it out, and
// any other type name, or a value that is no type name, matches it.
func matchesEmptyObject(kind any) bool {
	text, ok := kind.(string)
	if !ok {
		return true
	}

	switch text {
	case "string", "number", "integer", "boolean", "array", "null":
		return false
	default:
		return true
	}
}

// typeV4 is TypeV4Attribute.
func typeV4(kinds any) error {
	list, ok := kinds.([]any)
	if !ok {
		list = []any{kinds}
	}

	for _, kind := range list {
		if matchesEmptyObject(kind) {
			return nil
		}
	}

	return gemFault(faultValidation, keyType)
}

// typeUnion is TypeAttribute, for type and for disallow.
func (run *gemRun) typeUnion(schema *gemSchema, kinds any, disallow bool) error {
	list, union := kinds.([]any)
	if !union {
		list = []any{kinds}
	}

	valid := false

	for _, kind := range list {
		switch typed := kind.(type) {
		case string:
			valid = matchesEmptyObject(typed)
		case *rubyHash:
			if !union {
				break
			}

			sub, err := run.newSchema(typed, schema.uri, schema.draft)
			if err != nil {
				return err
			}

			err = run.validate(sub)
			if err != nil && !isValidationFault(err) {
				return err
			}

			valid = err == nil
		}

		if valid {
			break
		}
	}

	if valid == disallow {
		return gemFault(faultValidation, keyType)
	}

	return nil
}

// eachWithIndex is what value.each_with_index yields.
func eachWithIndex(value any) ([]any, error) {
	switch typed := value.(type) {
	case []any:
		return typed, nil
	case *rubyHash:
		pairs := make([]any, 0, len(typed.keys))
		for _, key := range typed.keys {
			pairs = append(pairs, []any{key, typed.values[key]})
		}

		return pairs, nil
	}

	return nil, gemFault(faultRaised, "NoMethodError: each_with_index on %T", value)
}

// combine is AllOfAttribute, AnyOfAttribute, and OneOfAttribute.
func (run *gemRun) combine(schema *gemSchema, name string, value any) error {
	members, err := eachWithIndex(value)
	if err != nil {
		return err
	}

	failures := 0

	for _, member := range members {
		sub, err := run.newSchema(member, schema.uri, schema.draft)
		if err != nil {
			return err
		}

		err = run.validate(sub)
		if err != nil && !isValidationFault(err) {
			return err
		}

		if err != nil {
			failures++
		} else if name == keyAnyOf {
			return nil
		}
	}

	switch {
	case name == keyAllOf && failures == 0:
		return nil
	case name == keyOneOf && failures == len(members)-1:
		return nil
	}

	return gemFault(faultValidation, "%s", name)
}

// not is NotAttribute.
func (run *gemRun) not(schema *gemSchema, value any) error {
	sub, err := run.newSchema(value, schema.uri, schema.draft)
	if err != nil {
		return err
	}

	err = run.validate(sub)
	if err == nil {
		return gemFault(faultValidation, keyNot)
	}

	if isValidationFault(err) {
		return nil
	}

	return err
}

// compareZero is a properties limit on an empty object, which fails when
// limit compares to 0 with sign, and raises when limit is no number.
func compareZero(limit any, sign int) error {
	number, ok := limit.(json.Number)
	if !ok {
		return gemFault(faultRaised, "ArgumentError: comparison of Integer with %v failed", limit)
	}

	if numberSign(number) == sign {
		return gemFault(faultValidation, "properties limit")
	}

	return nil
}

// numberSign is -1, 0, or 1 as number is negative, zero, or positive.
func numberSign(number json.Number) int {
	negative, digits, _ := decimalParts(number)

	switch {
	case digits == "":
		return 0
	case negative:
		return -1
	default:
		return 1
	}
}

// enumHasEmptyObject is EnumAttribute on an empty object.
func enumHasEmptyObject(value any) error {
	switch typed := value.(type) {
	case []any:
		for _, member := range typed {
			if hash, ok := member.(*rubyHash); ok && len(hash.keys) == 0 {
				return nil
			}
		}

		return gemFault(faultValidation, "enum")
	case *rubyHash:
		return gemFault(faultValidation, "enum")
	case string:
		return gemFault(faultRaised, "TypeError: include? on a string")
	}

	return gemFault(faultRaised, "NoMethodError: include? on %T", value)
}

// properties is PropertiesOptionalAttribute in drafts 1 and 2,
// PropertiesAttribute in draft 3, and PropertiesV4Attribute after it.
func (run *gemRun) properties(version int, value any) error {
	members, err := eachPair(value)
	if err != nil {
		return err
	}

	if version >= draft4 {
		return nil
	}

	for _, member := range members {
		if version == draft3 {
			required, err := fetchRequired(member)
			if err != nil {
				return err
			}

			if truthy(required) {
				return gemFault(faultValidation, "required property")
			}

			continue
		}

		optional, err := rubyIndex(member, "optional")
		if err != nil {
			return err
		}

		if !truthy(optional) {
			return gemFault(faultValidation, "required property")
		}
	}

	return nil
}

// eachPair is what value.each { |key, inner| } yields as inner.
func eachPair(value any) ([]any, error) {
	switch typed := value.(type) {
	case *rubyHash:
		values := make([]any, 0, len(typed.keys))
		for _, key := range typed.keys {
			values = append(values, typed.values[key])
		}

		return values, nil
	case []any:
		values := make([]any, 0, len(typed))

		for _, member := range typed {
			pair, ok := member.([]any)
			if ok && len(pair) >= 2 {
				values = append(values, pair[1])
			} else {
				values = append(values, nil)
			}
		}

		return values, nil
	}

	return nil, gemFault(faultRaised, "NoMethodError: each on %T", value)
}

// fetchRequired is property_schema.fetch('required') { nil }.
func fetchRequired(value any) (any, error) {
	switch typed := value.(type) {
	case *rubyHash:
		return typed.get("required"), nil
	case []any:
		return nil, gemFault(faultRaised, "TypeError: fetch on an array")
	}

	return nil, gemFault(faultRaised, "NoMethodError: fetch on %T", value)
}

// required is RequiredAttribute on an empty object.
func required(value any) error {
	switch typed := value.(type) {
	case []any:
		if len(typed) > 0 {
			return gemFault(faultValidation, "required")
		}

		return nil
	case *rubyHash:
		if len(typed.keys) > 0 {
			return gemFault(faultValidation, "required")
		}

		return nil
	}

	return gemFault(faultRaised, "NoMethodError: each on %T", value)
}

// compileEachPattern is PatternPropertiesAttribute on an empty object,
// which compiles each key as a Ruby regular expression.
func compileEachPattern(value any) error {
	var patterns []any

	switch typed := value.(type) {
	case *rubyHash:
		for _, key := range typed.keys {
			patterns = append(patterns, key)
		}
	case []any:
		for _, member := range typed {
			if pair, ok := member.([]any); ok {
				var first any
				if len(pair) > 0 {
					first = pair[0]
				}

				patterns = append(patterns, first)
			} else {
				patterns = append(patterns, member)
			}
		}
	default:
		return gemFault(faultRaised, "NoMethodError: each on %T", value)
	}

	for _, pattern := range patterns {
		err := compileRubyPattern(pattern)
		if err != nil {
			return err
		}
	}

	return nil
}

func compileRubyPattern(pattern any) error {
	text, ok := pattern.(string)
	if !ok {
		return gemFault(faultRaised, "TypeError: Regexp.new on %T", pattern)
	}

	return checkRubyRegexp(text)
}

// removeValidProperties is AdditionalPropertiesAttribute's
// remove_valid_properties on an empty object, which reads properties,
// compiles patternProperties, and follows extends.
//
//nolint:cyclop // it follows remove_valid_properties branch for branch
func (run *gemRun) removeValidProperties(schema *gemSchema, draft *gemDraft) error {
	key, err := run.enter(schema, run.removing)
	if err != nil {
		return err
	}
	defer run.leave(key, run.removing)

	properties, err := rubyIndex(schema.body, keyProperties)
	if err != nil {
		return err
	}

	if _, ok := properties.(*rubyHash); truthy(properties) && !ok {
		return gemFault(faultRaised, "NoMethodError: keys on %T", properties)
	}

	patterns, err := rubyIndex(schema.body, keyPatternProperties)
	if err != nil {
		return err
	}

	if patterns != nil {
		hash, ok := patterns.(*rubyHash)
		if !ok {
			return gemFault(faultRaised, "NoMethodError: each_key on %T", patterns)
		}

		for _, pattern := range hash.keys {
			err = checkRubyRegexp(pattern)
			if err != nil {
				return err
			}
		}
	}

	extends, err := rubyIndex(schema.body, "extends")
	if err != nil || !truthy(extends) {
		return err
	}

	members, ok := extends.([]any)
	if !ok {
		members = []any{extends}
	}

	for _, member := range members {
		extended, err := run.extended(member, schema, draft)
		if err != nil {
			return err
		}

		if extended != nil {
			err = run.removeValidProperties(extended, draft)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// extends is ExtendsAttribute.
func (run *gemRun) extends(schema *gemSchema, value any) error {
	members, ok := value.([]any)
	if !ok {
		members = []any{value}
	}

	for _, member := range members {
		extended, err := run.extended(member, schema, schema.draft)
		if err != nil {
			return err
		}

		if extended == nil {
			return gemFault(faultValidation, "extends")
		}

		err = run.validate(extended)
		if err != nil {
			return err
		}
	}

	return nil
}

// extended is ExtendsAttribute.get_extended_uri_and_schema.
func (run *gemRun) extended(member any, current *gemSchema, draft *gemDraft) (*gemSchema, error) {
	body, ok := member.(*rubyHash)
	if !ok {
		return nil, nil //nolint:nilnil // the gem extends nothing by a member that isn't a hash
	}

	if truthy(body.get("$ref")) {
		referenced, err := run.referenced(body, current, draft)
		if err != nil {
			return nil, err
		}

		if referenced != nil {
			if len(body.keys) == 1 {
				return referenced, nil
			}

			target, ok := referenced.body.(*rubyHash)
			if !ok {
				return nil, gemFault(faultRaised, "NoMethodError: merge on %T", referenced.body)
			}

			body = target.merge(body.without("$ref"))
		}
	}

	return run.newSchema(body, current.uri, draft)
}

// referenced is RefAttribute.get_referenced_uri_and_schema, which finds
// the schema a $ref names among those cached, and nil when none is.
func (run *gemRun) referenced(body *rubyHash, current *gemSchema, draft *gemDraft) (*gemSchema, error) {
	resolved, err := normalizeRef(body.get("$ref"), current.uri)
	if err != nil {
		return nil, err
	}

	text, err := resolved.String()
	if err != nil {
		return nil, err
	}

	fields := rubySplit(text, "#")
	if len(fields) == 0 {
		return nil, gemFault(faultRaised, "NoMethodError: + on nil")
	}

	cached, err := run.lookup(fields[0] + "#")
	if err != nil || cached == nil {
		return nil, err
	}

	if strings.Contains(text, "%") {
		return nil, gemFault(faultUnmodelled, "the $ref %q is percent-encoded", text)
	}

	reparsed, err := parseGemURIText(text)
	if err != nil {
		return nil, err
	}

	target := cached.body

	for _, segment := range rubySplit(orEmpty(reparsed.fragment), "/") {
		if segment == "" {
			continue
		}

		segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~0", "~"), "~1", "/")

		if list, ok := target.([]any); ok {
			target, err = rubyArrayIndex(list, segment)
		} else {
			target, err = rubyIndex(target, segment)
		}

		if err != nil {
			return nil, err
		}

		if target == nil {
			return nil, gemFault(faultSchema, "the $ref %q names nothing in its schema", text)
		}
	}

	return run.newSchema(target, resolved, draft)
}

// rubyArrayIndex is list[segment.to_i].
//
//nolint:cyclop // String#to_i's sign, digits, and underscores, then Array#[]'s bounds
func rubyArrayIndex(list []any, segment string) (any, error) {
	index := 0
	negative := false

	if segment != "" && (segment[0] == '+' || segment[0] == '-') {
		negative = segment[0] == '-'
		segment = segment[1:]
	}

	digits := ""

	for position := range len(segment) {
		char := segment[position]
		if isASCIIDigit(char) {
			digits += string(char)

			continue
		}

		if char == '_' && digits != "" && position+1 < len(segment) && isASCIIDigit(segment[position+1]) {
			continue
		}

		break
	}

	digits = strings.TrimLeft(digits, "0")
	if digits != "" {
		parsed, err := strconv.ParseInt(digits, 10, 64)
		if err != nil {
			return nil, gemFault(faultRaised, "RangeError: index %s", digits)
		}

		if parsed > int64(len(list)) {
			return nil, nil //nolint:nilnil // an index past the end answers nil, which is no error
		}

		index = int(parsed)
	}

	if negative {
		index = -index
	}

	if index < 0 {
		index += len(list)
	}

	if index < 0 || index >= len(list) {
		return nil, nil //nolint:nilnil // an index past either end answers nil, which is no error
	}

	return list[index], nil
}
