package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dlclark/regexp2/v2"
)

// ErrParametersInvalid reports that an instance's parameters don't match
// the plan's parameter schema. Its message names each parameter and the
// rule it breaks, and never a value, since a tenant's parameter can hold
// a secret.
var ErrParametersInvalid = errors.New("the parameters don't match the plan's parameter schema")

// patternMatchTimeout bounds the match of one schema pattern against one
// parameter, since a backtracking pattern can take a very long time over
// a value a tenant chose.
const patternMatchTimeout = 100 * time.Millisecond

// maxParameterProblems caps the problems a refusal lists.
const maxParameterProblems = 10

// ParameterSchema returns the schema the plan's instance parameters are
// checked against, or nil when the plan has none. A schema the catalog
// refused to publish is not used either, so the broker checks exactly
// what the platform was told.
func (p Plan) ParameterSchema() map[string]interface{} {
	if p.Schemas == nil || p.SchemaRefused != "" || len(p.Schemas.Parameters) == 0 {
		return nil
	}

	return p.Schemas.Parameters
}

// ValidateParameters checks params against schema, a draft-04 schema
// inside the profile the catalog publishes. A nil schema accepts any
// parameters. Beyond the schema's own rules, a top-level key the schema
// doesn't declare in its properties is refused, unless the schema's
// additionalProperties is true or a schema, because a parameter the
// manifest ignores would otherwise look accepted and do nothing.
func ValidateParameters(schema map[string]interface{}, params map[string]interface{}) error {
	if schema == nil {
		return nil
	}

	check := parameterCheck{definitions: mapValue(schema[keyDefinitions])}

	value := map[string]interface{}{}
	for key, item := range params {
		value[key] = item
	}

	root := check.resolve(schema)
	if _, declared := root[keyProperties]; declared {
		if _, explicit := root[keyAdditionalProperties]; !explicit {
			properties := mapValue(root[keyProperties])
			for _, key := range sortedParameterKeys(value) {
				if _, known := properties[key]; !known {
					check.fault(pointerFor("", key), "is not a parameter this plan accepts")
				}
			}
		}
	}

	check.validate(schema, value, "")

	if len(check.problems) == 0 {
		return nil
	}

	problems := check.problems
	if len(problems) > maxParameterProblems {
		problems = append(problems[:maxParameterProblems:maxParameterProblems], fmt.Sprintf("and %d more", len(check.problems)-maxParameterProblems))
	}

	return fmt.Errorf("%w: %s", ErrParametersInvalid, strings.Join(problems, "; "))
}

// parameterCheck collects the problems one validation finds.
type parameterCheck struct {
	definitions map[string]interface{}
	problems    []string
}

func (check *parameterCheck) fault(path, problem string) {
	if path == "" {
		path = "the parameters"
	} else {
		path = "parameter " + strconv.Quote(path)
	}

	check.problems = append(check.problems, path+" "+problem)
}

// resolve follows a schema's $ref through the root's definitions. The
// profile allows only #/definitions/<name> and refuses a loop, and the
// hop bound is a second line against one.
func (check *parameterCheck) resolve(schema map[string]interface{}) map[string]interface{} {
	for hops := 0; hops <= maxSchemaNesting; hops++ {
		ref, isString := schema[keyRef].(string)
		if !isString {
			return schema
		}

		next := mapValue(check.definitions[strings.TrimPrefix(ref, definitionRefPrefix)])
		if next == nil {
			return map[string]interface{}{}
		}

		schema = next
	}

	return map[string]interface{}{}
}

func (check *parameterCheck) validate(raw interface{}, value interface{}, path string) {
	schema := mapValue(raw)
	if schema == nil {
		return
	}

	schema = check.resolve(schema)

	if types, present := schema[keyType]; present && !matchesType(types, value) {
		check.fault(path, "must be "+typeNames(types))

		return
	}

	if enum, present := schema[keyEnum].([]interface{}); present && !inEnum(enum, value) {
		check.fault(path, "is not one of the values the plan allows")
	}

	switch typed := value.(type) {
	case string:
		check.validateString(schema, typed, path)
	case []interface{}:
		check.validateArray(schema, typed, path)
	case map[string]interface{}:
		check.validateObject(schema, typed, path)
	default:
		if number, isNumber := numberValue(value); isNumber {
			check.validateNumber(schema, number, path)
		}
	}
}

func (check *parameterCheck) validateString(schema map[string]interface{}, value, path string) {
	length := float64(utf8.RuneCountInString(value))

	if limit, present := numberValue(schema["minLength"]); present && length < limit {
		check.fault(path, fmt.Sprintf("must be at least %s characters long", formatNumber(limit)))
	}

	if limit, present := numberValue(schema["maxLength"]); present && length > limit {
		check.fault(path, fmt.Sprintf("must be at most %s characters long", formatNumber(limit)))
	}

	pattern, present := schema["pattern"].(string)
	if !present {
		return
	}

	compiled, err := regexp2.Compile(pattern, regexp2.None)
	if err != nil {
		check.fault(path, "can't be checked, because the plan's pattern for it doesn't compile")

		return
	}

	compiled.MatchTimeout = patternMatchTimeout

	matched, err := compiled.MatchString(value)

	switch {
	case err != nil:
		check.fault(path, "can't be checked against the plan's pattern in time")
	case !matched:
		check.fault(path, "doesn't match the pattern the plan requires")
	}
}

func (check *parameterCheck) validateNumber(schema map[string]interface{}, value float64, path string) {
	if limit, present := numberValue(schema["minimum"]); present {
		exclusive, _ := schema["exclusiveMinimum"].(bool)
		if value < limit || (exclusive && value == limit) {
			check.fault(path, boundText("greater than", exclusive, limit))
		}
	}

	if limit, present := numberValue(schema["maximum"]); present {
		exclusive, _ := schema["exclusiveMaximum"].(bool)
		if value > limit || (exclusive && value == limit) {
			check.fault(path, boundText("less than", exclusive, limit))
		}
	}

	if divisor, present := numberValue(schema["multipleOf"]); present && divisor > 0 && !isMultiple(value, divisor) {
		check.fault(path, "must be a multiple of "+formatNumber(divisor))
	}
}

func (check *parameterCheck) validateArray(schema map[string]interface{}, value []interface{}, path string) {
	count := float64(len(value))

	if limit, present := numberValue(schema["minItems"]); present && count < limit {
		check.fault(path, fmt.Sprintf("must have at least %s items", formatNumber(limit)))
	}

	if limit, present := numberValue(schema["maxItems"]); present && count > limit {
		check.fault(path, fmt.Sprintf("must have at most %s items", formatNumber(limit)))
	}

	if unique, _ := schema["uniqueItems"].(bool); unique && !uniqueItems(value) {
		check.fault(path, "must not repeat an item")
	}

	switch items := schema[keyItems].(type) {
	case map[string]interface{}:
		for index, item := range value {
			check.validate(items, item, pointerFor(path, strconv.Itoa(index)))
		}
	case []interface{}:
		for index, item := range value {
			if index < len(items) {
				check.validate(items[index], item, pointerFor(path, strconv.Itoa(index)))
			}
		}
	}
}

func (check *parameterCheck) validateObject(schema map[string]interface{}, value map[string]interface{}, path string) {
	count := float64(len(value))

	if limit, present := numberValue(schema["minProperties"]); present && count < limit {
		check.fault(path, fmt.Sprintf("must have at least %s keys", formatNumber(limit)))
	}

	if limit, present := numberValue(schema["maxProperties"]); present && count > limit {
		check.fault(path, fmt.Sprintf("must have at most %s keys", formatNumber(limit)))
	}

	if required, present := schema[keyRequired].([]interface{}); present {
		for _, name := range required {
			key, isString := name.(string)
			if !isString {
				continue
			}

			if _, found := value[key]; !found {
				check.fault(pointerFor(path, key), "is required")
			}
		}
	}

	properties := mapValue(schema[keyProperties])

	for _, key := range sortedParameterKeys(value) {
		if property, declared := properties[key]; declared {
			check.validate(property, value[key], pointerFor(path, key))

			continue
		}

		switch additional := schema[keyAdditionalProperties].(type) {
		case bool:
			if !additional {
				check.fault(pointerFor(path, key), "is not a parameter this plan accepts")
			}
		case map[string]interface{}:
			check.validate(additional, value[key], pointerFor(path, key))
		}
	}
}

// matchesType reports whether value has one of the draft-04 types the
// type keyword names, as one name or a list of names.
func matchesType(types interface{}, value interface{}) bool {
	switch typed := types.(type) {
	case string:
		return hasType(typed, value)
	case []interface{}:
		for _, name := range typed {
			if text, isString := name.(string); isString && hasType(text, value) {
				return true
			}
		}

		return false
	}

	return true
}

func hasType(name string, value interface{}) bool {
	switch name {
	case "null":
		return value == nil
	case "boolean":
		_, isBool := value.(bool)

		return isBool
	case "string":
		_, isString := value.(string)

		return isString
	case "array":
		_, isArray := value.([]interface{})

		return isArray
	case "object":
		_, isObject := value.(map[string]interface{})

		return isObject
	case "number":
		_, isNumber := numberValue(value)

		return isNumber
	case "integer":
		number, isNumber := numberValue(value)

		return isNumber && number == math.Trunc(number) && !math.IsInf(number, 0)
	}

	return false
}

func typeNames(types interface{}) string {
	switch typed := types.(type) {
	case string:
		return "a value of type " + typed
	case []interface{}:
		names := make([]string, 0, len(typed))
		for _, name := range typed {
			names = append(names, fmt.Sprint(name))
		}

		return "a value of type " + strings.Join(names, " or ")
	}

	return "a value of another type"
}

// numberValue returns value as a float64 when it is a JSON or YAML
// number in any of the forms the decoders give.
func numberValue(value interface{}) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case json.Number:
		number, err := typed.Float64()

		return number, err == nil
	}

	return 0, false
}

// isMultiple reports whether value is a whole multiple of divisor, in
// exact decimal arithmetic on the shortest form of each number, so 0.3
// is a multiple of 0.1 as JSON Schema means it.
func isMultiple(value, divisor float64) bool {
	exactValue, okValue := new(big.Rat).SetString(strconv.FormatFloat(value, 'g', -1, 64))
	exactDivisor, okDivisor := new(big.Rat).SetString(strconv.FormatFloat(divisor, 'g', -1, 64))

	if !okValue || !okDivisor || exactDivisor.Sign() == 0 {
		return false
	}

	return new(big.Rat).Quo(exactValue, exactDivisor).IsInt()
}

func boundText(relation string, exclusive bool, limit float64) string {
	if exclusive {
		return "must be " + relation + " " + formatNumber(limit)
	}

	return "must be " + relation + " or equal to " + formatNumber(limit)
}

func formatNumber(number float64) string {
	return strconv.FormatFloat(number, 'g', -1, 64)
}

func inEnum(enum []interface{}, value interface{}) bool {
	for _, allowed := range enum {
		if jsonEqual(allowed, value) {
			return true
		}
	}

	return false
}

func uniqueItems(items []interface{}) bool {
	for first := range items {
		for second := first + 1; second < len(items); second++ {
			if jsonEqual(items[first], items[second]) {
				return false
			}
		}
	}

	return true
}

// jsonEqual compares two JSON values, with numbers compared by value
// whatever form the decoder gave them.
func jsonEqual(first, second interface{}) bool {
	firstNumber, firstIsNumber := numberValue(first)
	secondNumber, secondIsNumber := numberValue(second)

	if firstIsNumber || secondIsNumber {
		return firstIsNumber && secondIsNumber && firstNumber == secondNumber
	}

	switch typed := first.(type) {
	case []interface{}:
		other, isList := second.([]interface{})
		if !isList || len(other) != len(typed) {
			return false
		}

		for index := range typed {
			if !jsonEqual(typed[index], other[index]) {
				return false
			}
		}

		return true
	case map[string]interface{}:
		other, isMap := second.(map[string]interface{})
		if !isMap || len(other) != len(typed) {
			return false
		}

		for key, item := range typed {
			otherItem, found := other[key]
			if !found || !jsonEqual(item, otherItem) {
				return false
			}
		}

		return true
	}

	return reflect.DeepEqual(first, second)
}

func mapValue(value interface{}) map[string]interface{} {
	typed, _ := value.(map[string]interface{})

	return typed
}

func sortedParameterKeys(value map[string]interface{}) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

// pointerFor names a nested parameter by its path of keys, joined by /.
func pointerFor(path, key string) string {
	if path == "" {
		return key
	}

	return path + "/" + key
}
