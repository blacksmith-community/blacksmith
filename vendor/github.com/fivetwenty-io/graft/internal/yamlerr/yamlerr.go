// Package yamlerr lets pkg/graft describe a YAML decoding error the way
// pkg/yaml's DecodeError does, with a position and a message that quotes
// nothing from the input. The errors goccy returns print the source
// lines around a failure, and those may hold credentials.
//
// pkg/yaml owns the translation and sets Describe as it loads, so the
// two packages never disagree about what a decoding error may say.
package yamlerr

import "fmt"

// Describe returns the line, the column, and the message pkg/yaml's
// DecodeError gives for err. A line or column of 0 means it is unknown.
// Describe is nil until pkg/yaml loads.
var Describe func(err error) (line, column int, msg string)

// Text returns err described in goccy's own shape, "[line:column]
// message", with the message Describe gives. It returns "line N:
// message" when only the line is known, the message alone when neither
// is, and fallback when Describe is not set.
func Text(err error, fallback string) string {
	if Describe == nil {
		return fallback
	}
	line, column, msg := Describe(err)
	switch {
	case line > 0 && column > 0:
		return fmt.Sprintf("[%d:%d] %s", line, column, msg)
	case line > 0:
		return fmt.Sprintf("line %d: %s", line, msg)
	default:
		return msg
	}
}
