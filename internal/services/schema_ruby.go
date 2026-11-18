package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// Cloud Controller reads the broker's catalog with Oj, which keeps a JSON
// object's members in the order they're written, reads a number written
// with a decimal point or an exponent as a BigDecimal and any other number
// as an Integer, and reads a string as itself. The broker writes the
// catalog with encoding/json, so a schema reaches Cloud Controller as the
// values here, and every check below works on them.

// errNotJSONValue is returned when a schema's JSON holds something other
// than one JSON value.
var errNotJSONValue = errors.New("not a single JSON value")

// rubyHash is a JSON object the way Ruby holds it, with its members in
// the order they were written.
type rubyHash struct {
	keys   []string
	values map[string]any
}

func newRubyHash() *rubyHash {
	return &rubyHash{values: map[string]any{}}
}

// set adds a member at the end, or replaces the value of a member that is
// already there in its place, as Ruby's Hash#[]= does.
func (hash *rubyHash) set(key string, value any) {
	if _, ok := hash.values[key]; !ok {
		hash.keys = append(hash.keys, key)
	}

	hash.values[key] = value
}

// get is Ruby's Hash#[], which answers nil for a missing member.
func (hash *rubyHash) get(key string) any {
	return hash.values[key]
}

func (hash *rubyHash) has(key string) bool {
	_, ok := hash.values[key]

	return ok
}

// servedSchema is schema as Cloud Controller reads it from the catalog.
func servedSchema(schema map[string]interface{}) (*rubyHash, error) {
	data, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPlanSchemaNotEncoded, err)
	}

	value, err := decodeRuby(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPlanSchemaNotEncoded, err)
	}

	hash, ok := value.(*rubyHash)
	if !ok {
		return nil, fmt.Errorf("%w: %w", ErrPlanSchemaNotEncoded, errNotJSONValue)
	}

	return hash, nil
}

// decodeRuby reads JSON into *rubyHash, []any, string, json.Number,
// bool, and nil values, keeping each object's member order.
func decodeRuby(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	value, err := decodeRubyValue(decoder)
	if err != nil {
		return nil, err
	}

	_, err = decoder.Token()
	if !errors.Is(err, io.EOF) {
		return nil, errNotJSONValue
	}

	return value, nil
}

func decodeRubyValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("reading JSON: %w", err)
	}

	delim, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}

	switch delim {
	case '{':
		hash := newRubyHash()

		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, fmt.Errorf("reading JSON: %w", err)
			}

			key, _ := keyToken.(string)

			value, err := decodeRubyValue(decoder)
			if err != nil {
				return nil, err
			}

			hash.set(key, value)
		}

		_, err = decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("reading JSON: %w", err)
		}

		return hash, nil
	case '[':
		list := []any{}

		for decoder.More() {
			value, err := decodeRubyValue(decoder)
			if err != nil {
				return nil, err
			}

			list = append(list, value)
		}

		_, err = decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("reading JSON: %w", err)
		}

		return list, nil
	default:
		return nil, errNotJSONValue
	}
}

// isRubyInteger reports whether Oj reads number as an Integer, which it
// does when it has no decimal point and no exponent.
func isRubyInteger(number json.Number) bool {
	return !strings.ContainsAny(string(number), ".eE")
}

// rubyJSONLength is the length, in characters, of value.to_json in Cloud
// Controller, which writes JSON with Oj in rails mode. That mode escapes
// <, >, and & for HTML, and U+2028 and U+2029 for JavaScript, each as six
// characters, and writes a BigDecimal with ActiveSupport's to_s.
func rubyJSONLength(value any) int {
	switch typed := value.(type) {
	case *rubyHash:
		length := len("{}") + max(len(typed.keys)-1, 0)
		for _, key := range typed.keys {
			length += rubyStringLength(key) + 1 + rubyJSONLength(typed.values[key])
		}

		return length
	case []any:
		length := len("[]") + max(len(typed)-1, 0)
		for _, member := range typed {
			length += rubyJSONLength(member)
		}

		return length
	case string:
		return rubyStringLength(typed)
	case json.Number:
		return rubyNumberLength(typed)
	case bool:
		if typed {
			return len("true")
		}

		return len("false")
	default:
		return len("null")
	}
}

// rubyStringLength is the length of text written as a JSON string by Oj
// in rails mode, quotes included.
func rubyStringLength(text string) int {
	length := 2

	for _, char := range text {
		switch {
		case char == '"' || char == '\\' || char == '\b' || char == '\t' || char == '\n' || char == '\f' || char == '\r':
			length += 2
		case char < 0x20 || char == '<' || char == '>' || char == '&' || char == ' ' || char == ' ':
			length += len(`\u0000`)
		default:
			length++
		}
	}

	return length
}

// rubyNumberLength is the length of number as Cloud Controller writes it
// back. An Integer is written as its digits, so the -0 Go writes for a
// negative zero comes back as 0. A BigDecimal is written the way
// ActiveSupport's BigDecimal#to_s writes it, in plain notation with at
// least one digit after the point, such as 1000000000000000000000.0 for
// the 1e+21 Go writes.
func rubyNumberLength(number json.Number) int {
	text := string(number)
	if isRubyInteger(number) {
		if text == "-0" {
			return 1
		}

		return len(text)
	}

	sign := 0
	negative, digits, point := decimalParts(number)

	if negative {
		sign = 1
	}

	switch {
	case digits == "":
		return sign + len("0.0")
	case point <= 0:
		return sign + len("0.") - point + len(digits)
	case point >= len(digits):
		return sign + point + len(".0")
	default:
		return sign + len(digits) + 1
	}
}

// decimalParts writes number as 0.digits times ten to the power point,
// with digits holding no leading or trailing zero, and empty for zero.
func decimalParts(number json.Number) (bool, string, int) {
	text := string(number)
	negative := strings.HasPrefix(text, "-")
	mantissa, exponentText, _ := strings.Cut(strings.ToLower(strings.TrimPrefix(text, "-")), "e")

	exponent := 0
	if exponentText != "" {
		exponent, _ = strconv.Atoi(exponentText)
	}

	whole, fraction, _ := strings.Cut(mantissa, ".")
	digits := strings.TrimLeft(whole+fraction, "0")
	// The point sits after len(whole) digits, moved by the exponent, and
	// counted here from the first significant digit.
	point := len(whole) + exponent - (len(whole+fraction) - len(digits))

	return negative, strings.TrimRight(digits, "0"), point
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
		return strconv.Quote(typed)
	case json.Number:
		return canonicalNumber(typed)
	default:
		return fmt.Sprint(typed)
	}
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
