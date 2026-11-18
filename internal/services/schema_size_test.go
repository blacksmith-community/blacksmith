package services_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	"blacksmith/internal/services"
)

// sizeLimit is Cloud Controller's limit on a schema's JSON, in characters.
const sizeLimit = 64 * 1024

// sizeBase is the length of the JSON Cloud Controller writes for a
// draft-04 schema with an empty description and nothing else.
const sizeBase = 70

// sizedSchema is a draft-04 schema whose description holds count copies
// of text and then filler, plus extra members.
func sizedSchema(text string, count, filler int, extra map[string]interface{}) map[string]interface{} {
	schema := map[string]interface{}{
		"$schema":     "http://json-schema.org/draft-04/schema#",
		"description": strings.Repeat(text, count) + strings.Repeat("x", filler),
	}

	for key, value := range extra {
		schema[key] = value
	}

	return schema
}

// requireSizeBoundary checks that schema(filler) is published when its
// JSON, as Cloud Controller writes it, is exactly at the limit, and
// refused as too large one character over it.
func requireSizeBoundary(t *testing.T, filler int, schema func(filler int) map[string]interface{}) {
	t.Helper()

	err := services.ValidateParameterSchema(schema(filler))
	if err != nil {
		t.Fatalf("the broker refuses a schema at the limit: %v", err)
	}

	err = services.ValidateParameterSchema(schema(filler + 1))
	if !errors.Is(err, services.ErrPlanSchemaTooLarge) {
		t.Fatalf("the broker answers %v for a schema one character over the limit, want ErrPlanSchemaTooLarge", err)
	}
}

// Cloud Controller writes a string with the short escapes for the
// quote, the backslash, and \b, \t, \n, \f, and \r, which take two
// characters each. It writes every other control character, and U+2028
// and U+2029, as \uXXXX, which takes six. DEL, the slash, and any other
// character take one, whatever their length in UTF-8.
func TestValidateParameterSchema_CountsEachEscape(t *testing.T) {
	t.Parallel()

	const copies = 1000

	for name, check := range map[string]struct {
		text  string
		width int
	}{
		"quote": {`"`, 2}, "backslash": {`\`, 2}, "backspace": {"\b", 2}, "tab": {"\t", 2}, "newline": {"\n", 2},
		"form-feed": {"\f", 2}, "carriage-return": {"\r", 2},
		"nul": {"\x00", 6}, "start-of-heading": {"\x01", 6}, "escape": {"\x1b", 6}, "unit-separator": {"\x1f", 6},
		"line-separator": {" ", 6}, "paragraph-separator": {" ", 6},
		"delete": {"\x7f", 1}, "slash": {"/", 1}, "accent": {"é", 1}, "emoji": {"\U0001F600", 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			requireSizeBoundary(t, sizeLimit-sizeBase-copies*check.width, func(filler int) map[string]interface{} {
				return sizedSchema(check.text, copies, filler, nil)
			})
		})
	}
}

// Cloud Controller reads a decimal as a BigDecimal and writes it back in
// plain notation, with at least one digit after the point, so a small
// decimal Go writes with an exponent is longer there. An integer is
// written as its digits.
func TestValidateParameterSchema_CountsEachNumber(t *testing.T) {
	t.Parallel()

	// The member is ,"minimum": and the number.
	const member = len(`,"minimum":`)

	for name, check := range map[string]struct {
		number float64
		width  int
	}{
		"tenth":              {0.1, len("0.1")},
		"hundredth":          {0.01, len("0.01")},
		"negative-small":     {-1e-7, len("-0.0000001")},
		"ten-millionth":      {1e-7, len("0.0000001")},
		"fraction":           {2.5, len("2.5")},
		"sextillion":         {1e21, len("1000000000000000000000.0")},
		"integer":            {64, len("64")},
		"negative-integer":   {-3, len("-3")},
		"negative-zero":      {math.Copysign(0, -1), len("0")},
		"small-with-a-digit": {1.5e-5, len("0.000015")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			requireSizeBoundary(t, sizeLimit-sizeBase-member-check.width, func(filler int) map[string]interface{} {
				return sizedSchema("", 0, filler, map[string]interface{}{"minimum": check.number})
			})
		})
	}
}
