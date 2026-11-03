package operators

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/dlclark/regexp2/v2"

	"github.com/fivetwenty-io/graft/internal/utils/ansi"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// SplitOperator handles string splitting operations.
type SplitOperator struct{}

// Setup ...
func (SplitOperator) Setup() error {
	return nil
}

// Phase ...
func (SplitOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies returns the nodes that (( split ... )) requires to be resolved.
func (SplitOperator) Dependencies(ev *Evaluator, args []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	deps := make([]*tree.Cursor, 0, len(auto))
	deps = append(deps, auto...)

	// Skip the first argument (separator) and process the rest
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == nil {
			continue
		}

		// For reference arguments, add them as dependencies
		if arg.Type == Reference && arg.Reference != nil {
			deps = append(deps, arg.Reference)
		}
	}

	return deps
}

// Run executes the split operator.
//
//nolint:gocyclo // split handles literal strings and PCRE regex patterns
func (SplitOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( split ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( split ... )) operation at $%s\n", ev.Here)

	if len(args) == 0 {
		DEBUG("  no arguments supplied to (( split ... )) operation.")
		return nil, ansi.Errorf("no arguments specified to @c{(( split ... ))}")
	}

	if len(args) < 2 {
		DEBUG("  too few arguments supplied to (( split ... )) operation.")
		return nil, ansi.Errorf("too few arguments supplied to @c{(( split ... ))}")
	}

	if len(args) > 2 {
		DEBUG("  too many arguments supplied to (( split ... )) operation.")
		return nil, ansi.Errorf("too many arguments supplied to @c{(( split ... ))}")
	}

	// First argument: separator (must be literal)
	if args[0].Type != Literal {
		DEBUG("     [0]: separator is not a literal")
		return nil, ansi.Errorf("split operator only accepts literal argument for the separator")
	}

	separator := fmt.Sprintf("%v", args[0].Literal)
	DEBUG("     [0]: separator will be: %v", separator)
	if separator != "" {
		DEBUG("     [0]: separator length: %d, first char: %c", len(separator), separator[0])
	} else {
		DEBUG("     [0]: separator is empty string")
	}

	// Second argument: string to split (can be literal or reference)
	val, err := ResolveOperatorArgument(ev, args[1])
	if err != nil {
		DEBUG("     [1]: resolution failed\n    error: %s", err)
		// Maintain backward compatibility with error messages
		if args[1].Type == Reference {
			return nil, ansi.Errorf("Unable to resolve @c{`%s`}: %s", args[1].Reference, err)
		}
		return nil, err
	}

	if val == nil {
		DEBUG("     [1]: argument resolved to nil")
		return nil, ansi.Errorf("cannot split nil value in @c{(( split ... ))}")
	}

	// Convert value to string and split
	strVal := fmt.Sprintf("%v", val)
	DEBUG("     [1]: resolved to string value: %s", strVal)
	DEBUG("     [1]: string length: %d bytes", len(strVal))
	if strVal != "" {
		DEBUG("     [1]: first few bytes: %x", []byte(strVal[:minInt(len(strVal), 20)]))
	}
	DEBUG("     separator bytes: %x", []byte(separator))

	// Handle edge cases
	if separator == "" {
		// Split into individual characters
		DEBUG("  empty separator: splitting into individual characters")
		result := make([]interface{}, len(strVal))
		for i, char := range strVal {
			result[i] = string(char)
		}
		return &Response{
			Type:  Replace,
			Value: result,
		}, nil
	}

	// Check if separator starts with / (indicating regex pattern)
	var parts []string
	if separator != "" && separator[0] == '/' {
		// Extract regex pattern (everything after the /)
		pattern := separator[1:]
		DEBUG("  detected PCRE regex pattern: %s", pattern)
		DEBUG("  input string to split: %s", strVal)

		// Compile the regex
		re, err := regexp2.Compile(pattern, regexp2.None)
		if err != nil {
			DEBUG("  failed to compile PCRE regex: %s", err)
			return nil, ansi.Errorf("invalid PCRE regex pattern @c{%s}: %s", pattern, err)
		}

		// Split using regex. regexp2 v2 has its own Regexp.Split, but
		// pcreSplit stays so the empty-piece and zero-width handling that
		// graft has always produced cannot drift with the library's.
		parts = pcreSplit(re, strVal)
		DEBUG("  PCRE regex split into %d parts", len(parts))
		if len(parts) == 1 && parts[0] == strVal {
			DEBUG("  WARNING: regex didn't split anything, pattern might not be matching")
		}
	} else {
		// Normal literal string split
		parts = strings.Split(strVal, separator)
		DEBUG("  literal split into %d parts", len(parts))
	}

	// Convert to interface slice
	result := make([]interface{}, len(parts))
	for i, part := range parts {
		result[i] = part
	}

	DEBUG("  split result: %v", result)
	return &Response{
		Type:  Replace,
		Value: result,
	}, nil
}

// minInt returns the minimum of two integers.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// pcreSplit implements string splitting using PCRE regex (regexp2).
func pcreSplit(re *regexp2.Regexp, text string) []string {
	var result []string
	lastEndByte := 0

	// Convert text to runes for proper indexing (regexp2 uses rune indices)
	textRunes := []rune(text)

	// Find all matches using regexp2's API
	match, err := re.FindStringMatch(text)
	if err != nil || match == nil {
		// No matches found, return the whole string
		DEBUG("    pcreSplit: no matches found, returning whole string")
		return []string{text}
	}
	DEBUG("    pcreSplit: found first match at rune index %d", match.RuneIndex)

	// Process all matches
	for match != nil {
		// Convert rune indices to byte indices
		matchStartRune := match.RuneIndex
		matchString := match.String()
		matchLengthRunes := len([]rune(matchString))
		matchEndRune := matchStartRune + matchLengthRunes

		// Calculate byte positions
		matchStartByte := len(string(textRunes[:matchStartRune]))
		matchEndByte := len(string(textRunes[:matchEndRune]))

		DEBUG("    pcreSplit: match %q at rune index %d (byte %d), length %d runes (bytes %d-%d)",
			matchString, matchStartRune, matchStartByte, matchLengthRunes, matchStartByte, matchEndByte)

		// Add the part before this match
		if matchStartByte > lastEndByte {
			result = append(result, text[lastEndByte:matchStartByte])
		} else if matchStartByte == lastEndByte && lastEndByte == 0 {
			// Empty string at the beginning
			result = append(result, "")
		}

		// For zero-width matches, we need to handle them specially
		if matchEndByte == matchStartByte && lastEndByte == matchStartByte && lastEndByte != 0 {
			// This is a zero-width match at the same position, skip it
			// But we need to advance by at least 1 rune to avoid infinite loop
			if lastEndByte < len(text) {
				// Find the next rune boundary
				runeValue, runeSize := utf8.DecodeRuneInString(text[lastEndByte:])
				if runeValue != utf8.RuneError && runeSize > 0 {
					result = append(result, text[lastEndByte:lastEndByte+runeSize])
					lastEndByte += runeSize
				} else {
					// Fallback to single byte
					result = append(result, text[lastEndByte:lastEndByte+1])
					lastEndByte++
				}
			}
		} else {
			lastEndByte = matchEndByte
		}

		// Get next match
		match, err = re.FindNextMatch(match)
		if err != nil {
			break
		}
	}

	// Add the remaining part after the last match
	if lastEndByte <= len(text) {
		result = append(result, text[lastEndByte:])
	}

	return result
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("split", SplitOperator{})
}
