package graft

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/cppforlife/go-patch/patch"

	"github.com/fivetwenty-io/graft/internal/errtext"
	"github.com/fivetwenty-io/graft/internal/yamlcodec"
	"github.com/fivetwenty-io/graft/internal/yamlnode"
)

// RootIsArrayError indicates a YAML document's root value is an array
// rather than a map/hash - the shape `graft merge --go-patch` (and
// ParseFile's own always-on equivalent) retries as a go-patch operation
// list instead of failing outright. Exported, with a private message
// field, so every caller's exact wording goes through NewRootIsArrayError
// rather than duplicating this type.
type RootIsArrayError struct {
	msg string
}

// Error implements the error interface.
func (r RootIsArrayError) Error() string {
	return r.msg
}

// NewRootIsArrayError constructs a RootIsArrayError with msg as its
// Error() text. Exported so callers with their own root-type probe and
// message wording (e.g. cmd/graft's parseYAML) can still produce the one
// shared type IsArrayError recognizes, instead of defining their own.
func NewRootIsArrayError(msg string) error {
	return RootIsArrayError{msg: msg}
}

// IsArrayError reports whether err is (or wraps) a RootIsArrayError.
func IsArrayError(err error) bool {
	var rootArrayErr RootIsArrayError
	return errors.As(err, &rootArrayErr)
}

// DetectArrayRoot performs a lightweight generic YAML unmarshal of data
// solely to classify its root value, the same probe cmd/graft's
// parseOneYamlFile has always done before deciding whether to retry a
// file as go-patch. It returns nil for a map root or a blank/null
// document (neither is an array), RootIsArrayError for an array root, and
// a plain error for a YAML syntax error or any other non-map root shape
// (string, number, bool). It exists because ParseYAML's own non-map-root
// error does not distinguish "array" from other invalid root shapes, and
// a go-patch retry (ParseGoPatch) only makes sense for an array root.
//
// The reliable signal is IsArrayError: a byte-scan fast path may return
// nil for documents whose full parse would have produced a syntax or
// non-map-root error, so callers must fall through to their real parse
// (which reports those errors itself) whenever IsArrayError is false.
func DetectArrayRoot(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}

	// Fast path: when a byte scan proves the root cannot be an array,
	// skip the full classification parse. Callers that only ask
	// IsArrayError get the same answer; callers that would have seen a
	// syntax or non-map-root error surface it from their real parse
	// immediately after.
	if rootCannotBeArray(data) {
		return nil
	}

	// The full classification parse is the first thing to tokenize data,
	// so it reads only the first document, and nesting past 10,000
	// levels fails here. A caller that falls through to ParseYAML gets
	// the same error from it.
	first, err := FirstMergeDocument(data)
	if err != nil {
		return err
	}

	return detectArrayRootFull(first)
}

// rootCannotBeArray scans data's first content byte to prove, without
// parsing, that the document root is not an array. It skips blank lines,
// comment lines, and `---` document-start markers, then answers true only
// for bytes that can never begin an array root: an alphanumeric, `_`,
// quote, or `{` starts a plain/quoted scalar, a mapping key, or a flow
// mapping. Anything else - `-`, `[`, node properties (`&`, `!`), YAML
// directives, block scalars, and so on - stays ambiguous (false) and is
// left to the full parse. False negatives are fine; a false positive
// would misroute an array-rooted document, so the whitelist is strict.
func rootCannotBeArray(data []byte) bool {
	// Strip a UTF-8 BOM if present.
	data = yamlnode.TrimBOM(data)

	for len(data) > 0 {
		line := data
		if idx := bytes.IndexByte(data, '\n'); idx >= 0 {
			line = data[:idx]
			data = data[idx+1:]
		} else {
			data = nil
		}

		line = bytes.TrimLeft(line, " \t\r")
		if len(line) == 0 || line[0] == '#' {
			continue
		}

		// `---` followed by whitespace or end of line is a document
		// start; the root node may share its line.
		if bytes.HasPrefix(line, []byte("---")) {
			rest := line[3:]
			if len(rest) == 0 {
				continue
			}
			if rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\r' {
				rest = bytes.TrimLeft(rest, " \t\r")
				if len(rest) == 0 || rest[0] == '#' {
					continue
				}
				return isDefiniteNonArrayStart(rest[0])
			}
			// `---x` is a plain scalar, not a marker; fall through to
			// classify its first byte (`-`), which stays ambiguous.
		}

		return isDefiniteNonArrayStart(line[0])
	}

	// Only blanks and comments: an empty document, but let the caller's
	// existing blank check own that answer.
	return false
}

// isDefiniteNonArrayStart reports whether a root node beginning with c
// can never be an array.
func isDefiniteNonArrayStart(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '_', c == '"', c == '\'', c == '{':
		return true
	}
	return false
}

// detectArrayRootFull is DetectArrayRoot's original full-parse
// classification, kept separate so the fast path can be tested for
// equivalence against it.
func detectArrayRootFull(data []byte) error {
	raw, err := yamlcodec.DecodeFirstGeneric(QuoteInjectKeys(data))
	if err != nil {
		return fmt.Errorf("failed to parse YAML: %w", plainYAMLError(err))
	}

	switch raw.(type) {
	case map[string]interface{}, nil:
		return nil
	case []interface{}:
		return RootIsArrayError{msg: "root of YAML document is not a hash/map: root is an array"}
	default:
		return fmt.Errorf("root of YAML document is not a hash/map: found %T", raw)
	}
}

// ParseGoPatch parses data as a go-patch operation list - an array of
// go-patch op definitions - the format an array-rooted document is
// retried as once DetectArrayRoot/IsArrayError signal that retry is
// worth attempting. The returned patch.Ops is normally wrapped with
// NewGoPatchDocument before being merged.
func ParseGoPatch(data []byte) (patch.Ops, error) {
	data, err := FirstMergeDocument(data)
	if err != nil {
		return nil, err
	}

	// The op definitions keep their fields' text, and their values are
	// then read the way a merge input reads them (see goPatchValues).
	opdefs := []patch.OpDefinition{}
	err = yamlcodec.DecodeFirst(data, &opdefs)
	if err == nil {
		err = goPatchValues(data, opdefs)
	}
	if err != nil {
		// Wording, capitalization, and the trailing newline ("Root of YAML
		// document is not a hash/map. Tried parsing it as go-patch, but
		// got: <err>\n") are pinned by cmd/graft/main_test.go's
		// `graft merge --go-patch` stderr assertion - keep them
		// byte-identical even though the rest of this package favors a
		// lowercase "root of...". This is verbatim the HEAD (064cdea)
		// cmd/graft parseGoPatch call: ansi.Errorf with a *balanced*
		// @R{...got} brace. In default/off/auto (non-TTY) mode that
		// resolves to the same plain text a plain fmt.Errorf would
		// produce, but under --color=on it renders real ANSI codes around
		// "...but got" (F13) - a plain fmt.Errorf can't reproduce that, so
		// this must stay ansi.Errorf, not be simplified to fmt.Errorf.
		return nil, errtext.WithPlain(
			errtext.Colorf("@R{Root of YAML document is not a hash/map. Tried parsing it as go-patch, but got}: %s\n", err),
			"Root of YAML document is not a hash/map. Tried parsing it as go-patch, but got: "+plainYAMLError(err).Error())
	}

	ops, err := patch.NewOpsFromDefinitions(opdefs)
	if err != nil {
		// "@R{" prefix and capital "Unable" are pinned by
		// cmd/graft/main_test.go's `graft merge --go-patch` stderr
		// assertion. HEAD (064cdea) cmd/graft's parseGoPatch built this
		// with ansi.Errorf("@R{Unable to parse go-patch definitions: %s\n",
		// err) - an unbalanced brace (no closing "}") that ansi's markup
		// parser can never resolve, so it always emitted the literal
		// "@R{Unable..." text verbatim, in every color/TTY mode. That
		// literal, brace-bug-and-all text IS the observable contract; keep
		// it byte-identical, including the trailing "\n" the old format
		// string carried.
		//nolint:staticcheck,revive // capitalized "@R{Unable" is the pinned literal HEAD text, brace bug included
		//lint:ignore ST1005 same, for the standalone staticcheck run
		return nil, errtext.WithPlain(
			fmt.Errorf("@R{Unable to parse go-patch definitions: %w\n", err),
			"Unable to parse go-patch definitions")
	}
	return ops, nil
}

// goPatchValues replaces the value of each op in opdefs, which
// DecodeFirst read from data, with the value a merge input would hold.
// So a plain yes in a value becomes a boolean, while a quoted word, a
// !!str word, and a block scalar stay strings. An op that has no value
// keeps none, so NewOpsFromDefinitions still reports it.
func goPatchValues(data []byte, opdefs []patch.OpDefinition) error {
	generic, err := yamlcodec.DecodeFirstGeneric(data)
	if err != nil {
		return err
	}
	items, ok := generic.([]any)
	if !ok || len(items) != len(opdefs) {
		return fmt.Errorf("go-patch ops decoded as %T, not a list of %d ops", generic, len(opdefs))
	}
	for i := range opdefs {
		if opdefs[i].Value == nil {
			continue
		}
		op, ok := items[i].(map[string]any)
		if !ok {
			return fmt.Errorf("go-patch op %d decoded as %T, not a map", i, items[i])
		}
		value := op["value"]
		opdefs[i].Value = &value
	}
	return nil
}
