package postprocess

import (
	"context"
	"regexp"
)

// DefaultRedactionMask is the value NewSecurityRedactor uses when called
// with an empty mask string.
const DefaultRedactionMask = "***REDACTED***"

// procNameSecurityRedactor is the Name() SecurityRedactor reports.
const procNameSecurityRedactor = "security-redactor"

// SecurityRedactor replaces the value of any map entry whose key matches
// one of Patterns with Mask. Patterns are matched against key names, not
// values: SecurityRedactor has no way to know a scalar "looks like" a
// secret, so it relies entirely on the field being named for what it
// holds (the same assumption graft's documented examples - "password",
// "secret", "api_key", "token" - make).
//
// Each pattern is compiled as a case-insensitive regular expression. A
// pattern that fails to compile is matched literally instead (via
// regexp.QuoteMeta), so NewSecurityRedactor never needs an error return
// and never panics on a caller-supplied pattern.
//
// The entire value at a matching key - scalar, map, or slice - is
// replaced with Mask; SecurityRedactor does not attempt to redact only
// part of a nested structure, and it does not recurse into a matched
// key's value looking for more matches (there is nothing left to look
// at: the whole subtree is already replaced).
type SecurityRedactor struct {
	// Patterns is the list of key-name patterns to match, case-insensitively.
	Patterns []string

	// Mask is the replacement value for a matching key. Empty means
	// DefaultRedactionMask (set by NewSecurityRedactor; a SecurityRedactor
	// built by struct literal with the zero Mask redacts to the empty
	// string, matching Go's ordinary zero-value semantics).
	Mask string

	compiled []*regexp.Regexp
}

// NewSecurityRedactor creates a SecurityRedactor for the given key-name
// patterns. An empty mask defaults to DefaultRedactionMask.
func NewSecurityRedactor(patterns []string, mask string) *SecurityRedactor {
	if mask == "" {
		mask = DefaultRedactionMask
	}

	r := &SecurityRedactor{
		Patterns: append([]string(nil), patterns...),
		Mask:     mask,
		compiled: make([]*regexp.Regexp, 0, len(patterns)),
	}
	for _, p := range patterns {
		re, err := regexp.Compile("(?i)" + p)
		if err != nil {
			re = regexp.MustCompile("(?i)" + regexp.QuoteMeta(p))
		}
		r.compiled = append(r.compiled, re)
	}
	return r
}

// Name returns the processor name.
func (r *SecurityRedactor) Name() string { return procNameSecurityRedactor }

// Phase returns when the processor should run.
func (r *SecurityRedactor) Phase() Phase { return PhaseLate }

// Priority runs SecurityRedactor before KeySorter (priority 100) within
// PhaseLate, so redaction sees plain maps rather than the *SortedMap
// wrapper KeySorter introduces at the document root.
func (r *SecurityRedactor) Priority() int { return 60 }

// Process replaces the value at every key matching Patterns with Mask.
func (r *SecurityRedactor) Process(_ context.Context, doc interface{}, _ *Metadata) (interface{}, error) {
	if len(r.compiled) == 0 {
		return doc, nil
	}
	return r.redact(doc), nil
}

func (r *SecurityRedactor) matches(key string) bool {
	for _, re := range r.compiled {
		if re.MatchString(key) {
			return true
		}
	}
	return false
}

func (r *SecurityRedactor) redact(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(val))
		for k, elem := range val {
			if r.matches(k) {
				result[k] = r.Mask
				continue
			}
			result[k] = r.redact(elem)
		}
		return result

	case []interface{}:
		result := make([]interface{}, len(val))
		for i, elem := range val {
			result[i] = r.redact(elem)
		}
		return result

	default:
		return v
	}
}
