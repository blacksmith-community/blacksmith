package interfaces

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// TokenizerState represents the internal state of the tokenizer.
type TokenizerState int

// Tokenizer state constants.
const (
	// StateNormal is the default tokenizer state.
	StateNormal TokenizerState = iota
	// StateInString is the state when inside a string literal.
	StateInString
	// StateInSingleQuote is the state when inside a single-quoted string.
	StateInSingleQuote
	// StateInReference is the state when inside a reference path.
	StateInReference
	// StateInOperatorCall is the state when inside an operator call.
	StateInOperatorCall
	// StateInEnvironment is the state when parsing an environment variable.
	StateInEnvironment
)

// TokenizerOptions configures tokenizer behavior.
type TokenizerOptions struct {
	// RecognizeReferencePaths enables parsing complete reference paths as single tokens
	RecognizeReferencePaths bool

	// AllowEnvironmentVars enables parsing of $VAR syntax
	AllowEnvironmentVars bool

	// PreserveWhitespace includes whitespace tokens in output
	PreserveWhitespace bool

	// TrackPositions enables position tracking for all tokens
	TrackPositions bool

	// AllowUnicode enables Unicode support in identifiers and strings
	AllowUnicode bool

	// OperatorName reports whether name is a registered operator. The
	// tokenizer asks it about an identifier that has no left operand
	// before it, because such a name opens an operator call and is not
	// itself an operand, so a '/' after it begins a reference, as in
	// "grab /b". A nil OperatorName names no operator.
	OperatorName func(name string) bool
}

// ReferencePattern defines patterns that should be recognized as reference paths.
type ReferencePattern interface {
	// Match returns true if the input starting at offset matches this pattern
	Match(input string, offset int) (matched bool, length int)

	// Name returns a descriptive name for this pattern
	Name() string
}

// SimpleReferencePattern matches simple dot-separated paths.
type SimpleReferencePattern struct{}

// Name returns the pattern name.
func (p *SimpleReferencePattern) Name() string { return "simple_path" }

// Match matches a simple dot-separated path.
func (p *SimpleReferencePattern) Match(input string, offset int) (matched bool, length int) {
	// Match pattern: identifier(.identifier|.number)*
	// Where identifier is [a-zA-Z_][a-zA-Z0-9_]*
	// and number is [0-9]+

	runes := []rune(input[offset:])
	if len(runes) == 0 || !isIdentifierStart(runes[0]) {
		return false, 0
	}

	pos := consumeIdentifierRun(runes, 0)

	// Only a multi-segment path is a match; a bare identifier is not.
	hasDots := false

	for pos < len(runes) && runes[pos] == '.' {
		hasDots = true
		pos++ // consume '.'

		if pos >= len(runes) {
			break // End of input after dot
		}

		switch {
		case unicode.IsDigit(runes[pos]):
			pos = consumeDigitRun(runes, pos)
		case isIdentifierStart(runes[pos]):
			pos = consumeSegmentIdentifier(runes, pos)
		default:
			return hasDots && pos > 0, pos // not a valid continuation
		}
	}

	return hasDots && pos > 0, pos
}

// consumeIdentifierRun returns the index one past the identifier starting
// at pos.
func consumeIdentifierRun(runes []rune, pos int) int {
	for pos < len(runes) && isIdentifierContinue(runes[pos]) {
		pos++
	}
	return pos
}

// consumeDigitRun returns the index one past the digit run starting at
// pos.
func consumeDigitRun(runes []rune, pos int) int {
	for pos < len(runes) && unicode.IsDigit(runes[pos]) {
		pos++
	}
	return pos
}

// consumeNumericSegment returns the index one past the segment that
// starts with a digit at pos. It takes the name characters that follow,
// as any segment does, and it also takes the '+' of an exponent, as in
// "1e+5", so a number in exponent form followed by a '/' reads as one
// path, the way spruce reads "1e+5/2". The '+' joins only when the run
// before it is digits and a single 'e' or 'E', and a digit comes after
// it, so "2e+3+1" still adds 1. A '-' exponent needs no help, because
// '-' is a name character.
func consumeNumericSegment(runes []rune, pos int) int {
	start := pos
	pos = consumeIdentifierRun(runes, pos)
	if pos+1 < len(runes) && runes[pos] == '+' && unicode.IsDigit(runes[pos+1]) &&
		isExponentMantissa(runes[start:pos]) {
		pos = consumeIdentifierRun(runes, pos+1)
	}
	return pos
}

// isExponentMantissa reports whether run is one or more digits followed
// by a single 'e' or 'E', the part of "1e+5" before the sign.
func isExponentMantissa(run []rune) bool {
	if len(run) < 2 {
		return false
	}
	last := run[len(run)-1]
	if last != 'e' && last != 'E' {
		return false
	}
	for _, r := range run[:len(run)-1] {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// numberEnd returns the index one past the number that starts at pos:
// digits, then an optional '.' and digits, then an optional exponent. An
// exponent is an 'e' or 'E', an optional sign, and at least one digit,
// so the "e" in "1e" or "3em" is not one. pos may hold the '.' of a
// fraction with no integer part, as in ".5".
func numberEnd(runes []rune, pos int) int {
	pos = consumeDigitRun(runes, pos)
	if pos+1 < len(runes) && runes[pos] == '.' && unicode.IsDigit(runes[pos+1]) {
		pos = consumeDigitRun(runes, pos+1)
	}
	if pos < len(runes) && (runes[pos] == 'e' || runes[pos] == 'E') {
		digits := pos + 1
		if digits < len(runes) && (runes[digits] == '+' || runes[digits] == '-') {
			digits++
		}
		if digits < len(runes) && unicode.IsDigit(runes[digits]) {
			pos = consumeDigitRun(runes, digits)
		}
	}
	return pos
}

// digitLedNameEnd reports whether the run that starts with a digit at
// pos is a name rather than a number, and where the name ends. The run
// is a name when a letter or '_' follows the number at its start, as in
// "3abc", "3em", "1e", or "2e5x", and then the name takes every name
// character after that, '-' included. spruce reads such a run as one key.
// A '-' right after the number is not part of a name, so "3-1" and
// "2e5-1" still subtract.
func digitLedNameEnd(runes []rune, pos int) (int, bool) {
	end := numberEnd(runes, pos)
	if end >= len(runes) || !isIdentifierStart(runes[end]) {
		return pos, false
	}
	return consumeIdentifierRun(runes, end), true
}

// consumeSegmentIdentifier consumes an identifier appearing after a dot,
// where a '+' followed by an identifier character continues the same
// segment: genesis's vaultified manifests use map keys like
// "haproxy_ssl+certificate", and spruce lexes the whole whitespace-free
// token as one reference. A '+' before a digit is left alone so "count+1"
// stays arithmetic. The leading segment of a path gets no such treatment.
func consumeSegmentIdentifier(runes []rune, pos int) int {
	return consumePlusNames(runes, consumeIdentifierRun(runes, pos))
}

// consumePlusNames continues a segment that ends at pos through each
// '+' followed by a name, as consumeSegmentIdentifier describes.
func consumePlusNames(runes []rune, pos int) int {
	for pos+1 < len(runes) && runes[pos] == '+' && isIdentifierStart(runes[pos+1]) {
		pos++ // consume '+'
		pos = consumeIdentifierRun(runes, pos)
	}
	return pos
}

// ArrayReferencePattern matches paths with array indexing.
type ArrayReferencePattern struct{}

// Name returns the pattern name.
func (p *ArrayReferencePattern) Name() string { return "array_path" }

// Match matches a path with array indexing.
func (p *ArrayReferencePattern) Match(input string, offset int) (bool, int) {
	return matchReferencePath([]rune(input[offset:]), false)
}

// matchLeadingSlashPath matches a reference that begins with a '/', as
// in "/b" or "/b/c". Whether a '/' may begin a reference depends on the
// tokens before it, so only the tokenizer calls this, and only once
// slashBeginsReference says the '/' has no left operand.
func matchLeadingSlashPath(input string, offset int) (bool, int) {
	return matchReferencePath([]rune(input[offset:]), true)
}

// matchReferencePath matches a reference path at the start of runes and
// returns its length in runes. With leadingSlash set, the path may begin
// with a '/', which the loop's '/' case consumes along with the segment
// after it.
//
//nolint:gocyclo // complex pattern matching for nested brackets and quotes
func matchReferencePath(runes []rune, leadingSlash bool) (bool, int) {
	// Match pattern: identifier([expr]|.identifier|/text)*
	// This is more complex as it needs to handle nested brackets and quotes

	if len(runes) == 0 {
		return false, 0
	}

	pos := 0

	// A path whose first segment is a number is a reference only when a
	// '/' joins it to more text, as in "8/2" or "0/x", or when a name
	// segment follows the whole number, as in "1e5.x" or "1.foo". Without either
	// the scanner reads it as a number, as it always has. A first segment
	// that starts with a digit but is a name, as "3abc" is, starts the
	// path the way an identifier does.
	numericStart := false
	sawSlash := false
	sawName := false
	finish := func() (bool, int) {
		if numericStart && !sawSlash && !sawName {
			return false, 0
		}
		return pos > 0, pos
	}

	// Must start with identifier, environment variable, or digit, or with
	// a '/' when the caller allows a leading slash
	switch {
	case leadingSlash && runes[pos] == '/':
		// The loop's '/' case below consumes the '/' and its segment.
	case unicode.IsDigit(runes[pos]):
		if end, isName := digitLedNameEnd(runes, pos); isName {
			pos = end
			break
		}
		numericStart = true
		pos = consumeNumericSegment(runes, pos)
	case runes[pos] == '$' && len(runes) > 1 && (runes[1] == '.' || runes[1] == '['):
		// A '$' followed by a '.' or a '[' names the document root, as
		// spruce reads "$.meta.a" and "$[0]". Only the '$' is consumed
		// here, and the loop's '.' and '[' cases read the rest of the
		// path. tree.ParseCursor drops the leading "$" node, so the
		// reference names the same node as the path without it. A "$."
		// with no path after it still matches as "$.", so the parser
		// can refuse it by name instead of reading it as a bad
		// environment variable.
		pos++
	case runes[pos] == '$':
		pos++
		if pos >= len(runes) || !isIdentifierStart(runes[pos]) {
			return false, 0
		}
		// Consume environment variable name
		for pos < len(runes) && isIdentifierContinue(runes[pos]) {
			pos++
		}
	case isIdentifierStart(runes[pos]):
		// Consume first identifier
		for pos < len(runes) && isIdentifierContinue(runes[pos]) {
			pos++
		}
	default:
		return false, 0
	}

	// Look for additional segments: [expr], .identifier, or /text
	for pos < len(runes) {
		switch runes[pos] {
		case '/':
			// A '/' that directly follows a reference character is part
			// of the reference, and so is the text after it up to the
			// next delimiter. This is BOSH's name/index instance notation,
			// as in "jobs.postgresql/0.ips", and it is how spruce reads a
			// '/' in a path. Every rune this loop sits after is part of
			// the reference, so the '/' always qualifies here. A '/' with
			// whitespace before it never reaches this point, because the
			// match stops at the whitespace, so "a / b" and "a /b" stay
			// division. A '+' followed by a name continues the segment,
			// as it does after a '.', so "x/b+c" is one key while "x/b+1"
			// still adds. A '$' name after the '/' stays in the path too,
			// as it does after a '.', so "a/$X" is the one key "a/$X".
			// That segment does not start with '$', so the substitution
			// made when the reference is parsed leaves it literal, which
			// is how spruce reads it. A '+' name after the '$' name
			// continues the segment the same way, so "a/$X+y" is one
			// key. A segment that starts with a digit keeps an
			// exponent's '+', so "x/1e+5" is one key, and a '+' name
			// after it, as before, so "pg/0+meta" is one key.
			sawSlash = true
			pos++ // consume '/'
			switch {
			case pos+1 < len(runes) && runes[pos] == '$' && isIdentifierStart(runes[pos+1]):
				pos = consumeSegmentIdentifier(runes, pos+1)
			case pos < len(runes) && unicode.IsDigit(runes[pos]):
				pos = consumePlusNames(runes, consumeNumericSegment(runes, pos))
			default:
				pos = consumeSegmentIdentifier(runes, pos)
			}
		case '[':
			// Array/map index - need to find matching ]
			bracketDepth := 1
			pos++ // consume '['

			for pos < len(runes) && bracketDepth > 0 {
				switch runes[pos] {
				case '[':
					bracketDepth++
				case ']':
					bracketDepth--
				case '"', '\'':
					// Skip quoted content
					quote := runes[pos]
					pos++
					for pos < len(runes) && runes[pos] != quote {
						if runes[pos] == '\\' {
							pos++ // skip escaped character
						}
						pos++
					}
				}
				pos++
			}

			if bracketDepth > 0 {
				// Unmatched bracket - not a valid reference
				return finish()
			}
		case '.':
			pos++ // consume '.'

			// Check what follows the dot
			if pos >= len(runes) {
				return finish() // End of input
			}

			// Allow either identifier, number, bracket, or an embedded
			// environment-variable segment after dot
			switch {
			case runes[pos] == '.':
				// A second '.' leaves an empty segment. The run stays in
				// this one reference so the parser can refuse the whole
				// path with one message, rather than reading the path up
				// to the first '.' and choking on the rest.
				continue
			case runes[pos] == '[':
				// Continue with bracket parsing (handled in next iteration)
				continue
			case runes[pos] == '/':
				// A '/' right after a '.' starts a segment whose key
				// begins with '/', as spruce reads "m./x". defer writes
				// "arr[0]/x" back out as "arr.0./x", so this keeps
				// graft able to read its own deferred output. The
				// outer loop's '/' case consumes the segment.
				continue
			case runes[pos] == '$':
				// An embedded environment-variable segment, e.g.
				// "X.$ENV.field" - spruce substitutes $VAR anywhere in a
				// dotted reference path, not only as the reference's
				// leading segment (the case == '$' branch above, which
				// this mirrors). The parser substitutes every
				// "$..."-prefixed Reference.Nodes entry when it parses the
				// reference, wherever the entry appears, not just the
				// first, so matching the run as part of this one reference
				// token is the only change needed. An invalid name (no
				// identifier immediately after '$') leaves pos
				// unadvanced, falling through the outer loop's default
				// case below and stopping the match at the dot, same as
				// any other invalid continuation.
				if pos+1 < len(runes) && isIdentifierStart(runes[pos+1]) {
					pos++ // consume '$'
					for pos < len(runes) && isIdentifierContinue(runes[pos]) {
						pos++
					}
				}
			case unicode.IsDigit(runes[pos]) && numericStart:
				// In a path that starts with a digit, the segment after
				// the '.' may be the rest of a number in exponent form,
				// as in "1.5e+3/2", which spruce reads as the key "5e+3/2"
				// inside "1". Without a '/' later in the path, finish
				// rejects the match and the scanner reads the number.
				pos = consumeNumericSegment(runes, pos)
			case unicode.IsDigit(runes[pos]):
				// A segment that starts with a digit is a number, an
				// index such as the 0 in "list.0", or a digit-led name,
				// as the first segment can be. A name takes the rest of
				// the run, so "m.3x" is the key "3x" inside "m", and a
				// number takes its exponent, so "m.3e5" is the key "3e5".
				// spruce reads both that way. A '-' or '+' after the
				// number still ends the path, so "a.3-1" subtracts.
				if end, isName := digitLedNameEnd(runes, pos); isName {
					pos = consumePlusNames(runes, end)
				} else {
					pos = numberEnd(runes, pos)
				}
			case isIdentifierStart(runes[pos]):
				// A name segment right after a whole number makes the
				// path a reference, as spruce reads "1e5.x". A '-'
				// before the dot leaves it arithmetic, so "3-abc.def"
				// still subtracts.
				if numericStart && numberEnd(runes, 0) == pos-1 {
					sawName = true
				}
				// Consume identifier
				for pos < len(runes) && isIdentifierContinue(runes[pos]) {
					pos++
				}
				// A '+' followed by an identifier character continues
				// the same segment (genesis vaultified keys like
				// "haproxy_ssl+certificate"); a '+' before a digit is
				// left alone so "count+1" stays arithmetic.
				for pos+1 < len(runes) && runes[pos] == '+' && isIdentifierStart(runes[pos+1]) {
					pos++ // consume '+'
					for pos < len(runes) && isIdentifierContinue(runes[pos]) {
						pos++
					}
				}
				// Predicate segment support: a dotted
				// segment may carry an inline "field=value" predicate, e.g.
				// "servers.name=primary.host" tokenizes with "name=primary"
				// as one segment, matching the bracketed
				// "servers[name=primary].host" form. Guarded so a doubled
				// '=' ("a==b") and a '=' followed by a quote
				// (`env=="prod"`) are never captured here — both are left
				// for the ordinary "==" comparison tokenization.
				if pos < len(runes) && runes[pos] == '=' &&
					pos+1 < len(runes) && runes[pos+1] != '=' &&
					(isIdentifierStart(runes[pos+1]) || unicode.IsDigit(runes[pos+1])) {
					pos++ // consume '='
					for pos < len(runes) && isIdentifierContinue(runes[pos]) {
						pos++
					}
				}
			default:
				return finish() // Not a valid continuation
			}
		default:
			// End of reference path
			return finish()
		}
	}

	return finish()
}

// headState tracks how far the tokenizer is through an operator head.
type headState int

const (
	// headNone means the last token is not part of an operator head.
	headNone headState = iota
	// headOpen means the last token ends a head that could continue,
	// such as "grab" or the "nocache" in "grab:nocache".
	headOpen
	// headJoin means the last token is a ':' or '@' glued to a head, so
	// a name glued after it continues the head.
	headJoin
)

// AdvancedTokenizer provides enhanced tokenization with path recognition.
type AdvancedTokenizer struct {
	input    string
	position int
	line     int
	column   int
	options  TokenizerOptions
	patterns []ReferencePattern
	state    TokenizerState

	// Lookahead support
	nextToken    *Token
	nextComputed bool
	// Position after the peeked token
	nextPosition int
	nextLine     int
	nextColumn   int

	// leftOperand records whether the last token scanned ends an
	// operand, so that a '/' scanned next has a left operand to divide.
	// slashBeginsReference reads it, and noteScanned keeps it current.
	leftOperand bool

	// callHead records whether the last tokens scanned are an operator
	// head, the operator name with any ":modifier" or "@target" glued to
	// it, as in "vault:nocache@prod". A name glued after a ':' or an '@'
	// in a head is part of the head, not an operand, so a '/' after the
	// whole head still begins a reference. prevEnd is the byte offset
	// where the last scanned token ends, which tells whether the next
	// token is glued to it.
	callHead headState
	prevEnd  int

	// headName records whether the last token scanned is a modifier or
	// target name inside an operator head. HeadName reports it.
	headName bool

	// groupStack tracks the still-open "(" / "((" groups, innermost last:
	// true for a group opened by "((" (needs "))" to close), false for one
	// opened by a bare "(" (needs a single ")"). scanParentheses consults
	// the top entry to decide whether a ')' may combine with an
	// immediately following ')' into a single TokenOperatorEnd, rather than
	// combining unconditionally — see scanParentheses's doc comment.
	groupStack []bool
}

// NewAdvancedTokenizer creates a new tokenizer with the given options.
func NewAdvancedTokenizer(input string, options TokenizerOptions) *AdvancedTokenizer {
	t := &AdvancedTokenizer{
		input:   input,
		line:    1,
		column:  1,
		options: options,
		state:   StateNormal,
	}

	// Add default reference patterns
	if options.RecognizeReferencePaths {
		t.patterns = []ReferencePattern{
			&ArrayReferencePattern{}, // Try complex patterns first
			&SimpleReferencePattern{},
		}
	}

	return t
}

// NextToken returns the next token from the input.
func (t *AdvancedTokenizer) NextToken() *Token {
	if t.nextComputed {
		token := t.nextToken
		t.nextToken = nil
		t.nextComputed = false
		if token != nil {
			// Update position to after the peeked token
			t.position = t.nextPosition
			t.line = t.nextLine
			t.column = t.nextColumn
			return token
		}
	}

	tok := t.scanToken()
	return &tok
}

// PeekToken returns the next token without consuming it.
func (t *AdvancedTokenizer) PeekToken() *Token {
	if !t.nextComputed {
		// Save current position before scanning
		savedPos := t.position
		savedLine := t.line
		savedColumn := t.column

		tok := t.scanToken()
		t.nextToken = &tok
		t.nextComputed = true

		// Save position after the token
		t.nextPosition = t.position
		t.nextLine = t.line
		t.nextColumn = t.column

		// Restore position to before the token
		t.position = savedPos
		t.line = savedLine
		t.column = savedColumn
	}
	return t.nextToken
}

// Position returns the tokenizer's current byte offset into the input.
// Callers use this to detect a scanner arm that returns a token without
// consuming any input, which would otherwise livelock the caller's read loop.
func (t *AdvancedTokenizer) Position() int {
	return t.position
}

// HasMore returns true if there are more tokens to read.
func (t *AdvancedTokenizer) HasMore() bool {
	if t.nextComputed && t.nextToken != nil {
		return t.nextToken.Type != TokenEOF
	}
	return t.position < len(t.input)
}

// Reset resets the tokenizer with new input.
func (t *AdvancedTokenizer) Reset(input string) {
	t.input = input
	t.position = 0
	t.line = 1
	t.column = 1
	t.state = StateNormal
	t.leftOperand = false
	t.callHead = headNone
	t.prevEnd = 0
	t.headName = false
	t.nextToken = nil
	t.nextComputed = false
	t.nextPosition = 0
	t.nextLine = 1
	t.nextColumn = 1
}

// EnablePathMode enables or disables path mode for the tokenizer.
func (t *AdvancedTokenizer) EnablePathMode(enabled bool) {
	t.options.RecognizeReferencePaths = enabled
}

// tokenPosition holds position info for token creation.
type tokenPosition struct {
	start       int
	startLine   int
	startColumn int
}

// scanToken scans the next token and records whether it ends an
// operand. Every scanned token is handed out in order, a peeked one
// included, so the record always describes the token before the next
// one scanned.
func (t *AdvancedTokenizer) scanToken() Token {
	tok := t.scanNextToken()
	t.noteScanned(tok)
	return tok
}

// noteScanned records whether tok ends a left operand. Literals,
// references, environment variables, and closing brackets end one. An
// identifier ends one too, unless it names an operator and has no left
// operand before it, because then it opens an operator call, as "grab"
// does in "(( grab /b ))". The call stays open through a ':' or an '@'
// glued to the head and the name glued after it, so "grab:nocache /b"
// and "vault@prod /b" read "/b" the same way. Every other token, such as
// "((", "(", ",", "||", "?", ":", "!", or a binary operator, leaves no
// left operand.
func (t *AdvancedTokenizer) noteScanned(tok Token) {
	glued := tok.Pos.Offset == t.prevEnd
	t.prevEnd = tok.End.Offset
	t.headName = false
	switch tok.Type {
	case TokenInteger, TokenFloat, TokenString, TokenRawString, TokenBoolean, TokenNull,
		TokenReference, TokenEnvironment, TokenRightParen, TokenOperatorEnd,
		TokenRightBracket, TokenRightBrace:
		t.leftOperand = true
		t.callHead = headNone
	case TokenIdentifier:
		switch {
		case t.callHead == headJoin && glued:
			// The modifier or target name of a head, as "nocache" in
			// "grab:nocache" or "prod" in "vault@prod".
			t.callHead = headOpen
			t.headName = true
		case !t.leftOperand && t.namesOperator(tok.Literal):
			t.callHead = headOpen
		default:
			t.leftOperand = true
			t.callHead = headNone
		}
	case TokenColon, TokenAt:
		// A ':' or '@' glued to the end of a head joins the name after
		// it to the head. One with whitespace before it does not, so the
		// ternary "c ? grab : /b" is unaffected.
		if t.callHead == headOpen && glued {
			t.callHead = headJoin
		} else {
			t.callHead = headNone
		}
		t.leftOperand = false
	default:
		t.leftOperand = false
		t.callHead = headNone
	}
}

// HeadName reports whether the token NextToken last returned is the
// modifier or target name of an operator head, as "nocache" is in
// "grab:nocache" and "prod" is in "vault@prod". Such a name belongs to
// the operator call and never names a key in the document. The answer
// describes the last token scanned, so it holds only when PeekToken has
// not been called since.
func (t *AdvancedTokenizer) HeadName() bool {
	return t.headName
}

// namesOperator reports whether name is a registered operator, through
// the OperatorName option.
func (t *AdvancedTokenizer) namesOperator(name string) bool {
	return t.options.OperatorName != nil && t.options.OperatorName(name)
}

// slashBeginsReference reports whether the '/' at the current position
// begins a reference rather than dividing. This is the one place that
// decides how a '/' with nothing attached before it reads. A '/' begins
// a reference when no left operand comes before it: at the start of an
// expression, right after an operator head that opens a call, such as
// "grab" or "vault:nocache@prod", and after "(", ",", "||", "?", ":", or
// a binary operator. That is how spruce
// reads "(( grab /b ))", as the key "/b". A '/' after an operand divides,
// with or without whitespace between them, so "a / b" and "a /b" still
// divide. A '/' attached to the end of a reference never gets here,
// because the reference patterns consume it as part of the path.
func (t *AdvancedTokenizer) slashBeginsReference() bool {
	return t.options.RecognizeReferencePaths && t.state == StateNormal && !t.leftOperand
}

// scanLeadingSlashReference scans a reference that begins with the '/'
// at the current position, once slashBeginsReference allows it. A '/'
// with no segment attached after it, as in "grab / b", is not a
// reference, so it falls back to the division token, as before.
func (t *AdvancedTokenizer) scanLeadingSlashReference(pos tokenPosition) (Token, bool) {
	matched, length := matchLeadingSlashPath(t.input, t.position)
	if !matched || length < 2 {
		return Token{}, false
	}
	value := t.input[t.position:runeSpanEnd(t.input, t.position, length)]
	t.advance(length)
	return Token{
		Type:    TokenReference,
		Literal: value,
		Pos:     Position{Offset: pos.start, Line: pos.startLine, Column: pos.startColumn},
		End:     Position{Offset: t.position, Line: t.line, Column: t.column},
	}, true
}

// scanNextToken performs the actual tokenization.
func (t *AdvancedTokenizer) scanNextToken() Token {
	t.skipWhitespace()

	if t.position >= len(t.input) {
		return t.makeToken(TokenEOF, "")
	}

	pos := tokenPosition{
		start:       t.position,
		startLine:   t.line,
		startColumn: t.column,
	}

	ch := t.current()

	// A '/' with no left operand before it begins a reference
	if ch == '/' && t.slashBeginsReference() {
		if tok, matched := t.scanLeadingSlashReference(pos); matched {
			return tok
		}
	}

	// Try to match reference patterns first if enabled
	if tok, matched := t.tryMatchReferencePattern(pos); matched {
		return tok
	}

	// Handle string literals
	if ch == '"' || ch == '\'' {
		return t.scanString(ch)
	}

	// Handle environment variables
	if ch == '$' {
		return t.scanDollarSign(pos)
	}

	// Handle parentheses (operators)
	if tok, handled := t.scanParentheses(ch, pos); handled {
		return tok
	}

	// Handle structural tokens
	if tok, handled := t.scanStructuralToken(ch, pos); handled {
		return tok
	}

	// Handle operators
	if tok, handled := t.scanOperatorToken(ch, pos); handled {
		return tok
	}

	// Handle numbers and identifiers
	if unicode.IsDigit(ch) {
		return t.scanNumber()
	}
	if isIdentifierStart(ch) {
		return t.scanIdentifier()
	}

	t.advance(1)
	return t.makeTokenAt(TokenInvalid, string(ch), pos.start, pos.startLine, pos.startColumn)
}

// runeSpanEnd returns the byte offset reached by advancing runeCount runes
// from start in s, clamped to len(s). ReferencePattern.Match reports its
// match length in runes — the same unit advance() consumes — so a caller
// that needs the matched text must convert. Slicing s[start:start+runeCount]
// directly reads past the end for a pattern whose scan overruns the input
// (an unterminated quote inside a bracketed segment, e.g. `A["`, which
// panicked the whole process) and splits a multi-byte rune otherwise.
func runeSpanEnd(s string, start, runeCount int) int {
	end := start
	for i := 0; i < runeCount && end < len(s); i++ {
		_, size := utf8.DecodeRuneInString(s[end:])
		end += size
	}
	return end
}

// tryMatchReferencePattern attempts to match a reference pattern.
func (t *AdvancedTokenizer) tryMatchReferencePattern(pos tokenPosition) (Token, bool) {
	if !t.options.RecognizeReferencePaths || t.state != StateNormal {
		return Token{}, false
	}

	for _, pattern := range t.patterns {
		if matched, length := pattern.Match(t.input, t.position); matched {
			value := t.input[t.position:runeSpanEnd(t.input, t.position, length)]
			if strings.Contains(value, ".") || strings.Contains(value, "[") || strings.Contains(value, "/") {
				t.advance(length)
				return Token{
					Type:    TokenReference,
					Literal: value,
					Pos:     Position{Offset: pos.start, Line: pos.startLine, Column: pos.startColumn},
					End:     Position{Offset: t.position, Line: t.line, Column: t.column},
				}, true
			}
		}
	}
	return Token{}, false
}

// scanDollarSign handles the $ character.
func (t *AdvancedTokenizer) scanDollarSign(pos tokenPosition) Token {
	if t.options.AllowEnvironmentVars {
		return t.scanEnvironmentVar()
	}
	t.advance(1)
	return t.makeTokenAt(TokenInvalid, "unexpected character: $", pos.start, pos.startLine, pos.startColumn)
}

// scanParentheses handles ( and ) characters.
//
// A ')' combines with an immediately following ')' into TokenOperatorEnd
// only when the group it closes was itself opened by "((". groupStack
// (innermost last) tracks how each open group was opened, so a bare-"("
// group's close is always emitted alone: without this, "(join "," (grab
// a)) ))" would swallow the inner group's own ')' into the outer
// wrapper's "))", leaving the parser nothing to close "(join ...)" with.
//
// The opening side combines '(' with a following '(' unconditionally.
// Whether an adjacent pair is a genuine nested marker-open ("(( prune ))"
// as an operand) or two independent grouping opens ("((A && B) || C)")
// isn't decidable here — both look identical until the parser sees what
// follows the parsed content. parseNestedOperator resolves it after the
// fact instead.
func (t *AdvancedTokenizer) scanParentheses(ch rune, pos tokenPosition) (Token, bool) {
	switch ch {
	case '(':
		if t.peek() == '(' {
			t.advance(2)
			t.groupStack = append(t.groupStack, true)
			return t.makeTokenAt(TokenOperatorStart, "((", pos.start, pos.startLine, pos.startColumn), true
		}
		t.advance(1)
		t.groupStack = append(t.groupStack, false)
		return t.makeTokenAt(TokenLeftParen, "(", pos.start, pos.startLine, pos.startColumn), true
	case ')':
		// No tracked open group (e.g. a stray ')' with no matching '(' /
		// "((" — already malformed input the parser will reject) falls
		// back to the historical unconditional-combine behavior, changing
		// nothing about how already-invalid input is reported.
		needsDouble := true
		if n := len(t.groupStack); n > 0 {
			needsDouble = t.groupStack[n-1]
			t.groupStack = t.groupStack[:n-1]
		}
		if needsDouble && t.peek() == ')' {
			t.advance(2)
			return t.makeTokenAt(TokenOperatorEnd, "))", pos.start, pos.startLine, pos.startColumn), true
		}
		t.advance(1)
		return t.makeTokenAt(TokenRightParen, ")", pos.start, pos.startLine, pos.startColumn), true
	}
	return Token{}, false
}

// scanStructuralToken handles structural tokens like brackets, braces, etc.
func (t *AdvancedTokenizer) scanStructuralToken(ch rune, pos tokenPosition) (Token, bool) {
	simpleTokens := map[rune]TokenType{
		'[': TokenLeftBracket,
		']': TokenRightBracket,
		'{': TokenLeftBrace,
		'}': TokenRightBrace,
		',': TokenComma,
		'@': TokenAt,
		':': TokenColon,
		'?': TokenQuestion,
		'~': TokenNull,
	}

	if tokenType, ok := simpleTokens[ch]; ok {
		t.advance(1)
		return t.makeTokenAt(tokenType, string(ch), pos.start, pos.startLine, pos.startColumn), true
	}

	if ch == '.' {
		if t.position+1 < len(t.input) && unicode.IsDigit(rune(t.input[t.position+1])) {
			return t.scanNumber(), true
		}
		t.advance(1)
		return t.makeTokenAt(TokenDot, ".", pos.start, pos.startLine, pos.startColumn), true
	}

	return Token{}, false
}

// scanOperatorToken handles operator tokens.
func (t *AdvancedTokenizer) scanOperatorToken(ch rune, pos tokenPosition) (Token, bool) {
	// Simple single-character operators
	simpleOps := map[rune]TokenType{
		'+': TokenPlus,
		'-': TokenMinus,
		'*': TokenStar,
		'/': TokenSlash,
		'%': TokenPercent,
	}

	if tokenType, ok := simpleOps[ch]; ok {
		t.advance(1)
		return t.makeTokenAt(tokenType, string(ch), pos.start, pos.startLine, pos.startColumn), true
	}

	// Two-character or context-dependent operators
	return t.scanComplexOperator(ch, pos)
}

// scanComplexOperator handles operators that may be one or two characters.
func (t *AdvancedTokenizer) scanComplexOperator(ch rune, pos tokenPosition) (Token, bool) {
	peek := t.peek()

	switch ch {
	case '!':
		if peek == '=' {
			t.advance(2)
			return t.makeTokenAt(TokenNotEqual, "!=", pos.start, pos.startLine, pos.startColumn), true
		}
		t.advance(1)
		return t.makeTokenAt(TokenNot, "!", pos.start, pos.startLine, pos.startColumn), true
	case '=':
		if peek == '=' {
			t.advance(2)
			return t.makeTokenAt(TokenEqual, "==", pos.start, pos.startLine, pos.startColumn), true
		}
		t.advance(1)
		return t.makeTokenAt(TokenInvalid, "unexpected character: =", pos.start, pos.startLine, pos.startColumn), true
	case '<':
		if peek == '=' {
			t.advance(2)
			return t.makeTokenAt(TokenLessEqual, "<=", pos.start, pos.startLine, pos.startColumn), true
		}
		t.advance(1)
		return t.makeTokenAt(TokenLess, "<", pos.start, pos.startLine, pos.startColumn), true
	case '>':
		if peek == '=' {
			t.advance(2)
			return t.makeTokenAt(TokenGreaterEqual, ">=", pos.start, pos.startLine, pos.startColumn), true
		}
		t.advance(1)
		return t.makeTokenAt(TokenGreater, ">", pos.start, pos.startLine, pos.startColumn), true
	case '&':
		if peek == '&' {
			t.advance(2)
			return t.makeTokenAt(TokenAnd, "&&", pos.start, pos.startLine, pos.startColumn), true
		}
		t.advance(1)
		return t.makeTokenAt(TokenInvalid, "unexpected character: &", pos.start, pos.startLine, pos.startColumn), true
	case '|':
		if peek == '|' {
			t.advance(2)
			return t.makeTokenAt(TokenOr, "||", pos.start, pos.startLine, pos.startColumn), true
		}
		t.advance(1)
		return t.makeTokenAt(TokenInvalid, "unexpected character: |", pos.start, pos.startLine, pos.startColumn), true
	}

	return Token{}, false
}

// Helper functions for character classification.
func isIdentifierStart(ch rune) bool {
	return unicode.IsLetter(ch) || ch == '_'
}

func isIdentifierContinue(ch rune) bool {
	return unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_' || ch == '-'
}

// Utility methods for tokenizer implementation.
func (t *AdvancedTokenizer) current() rune {
	if t.position >= len(t.input) {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(t.input[t.position:])
	return r
}

func (t *AdvancedTokenizer) peek() rune {
	if t.position >= len(t.input) {
		return 0
	}
	// Decode current rune to find its size
	_, size := utf8.DecodeRuneInString(t.input[t.position:])
	if t.position+size >= len(t.input) {
		return 0
	}
	r, _ := utf8.DecodeRuneInString(t.input[t.position+size:])
	return r
}

func (t *AdvancedTokenizer) advance(count int) {
	for i := 0; i < count && t.position < len(t.input); i++ {
		r, size := utf8.DecodeRuneInString(t.input[t.position:])
		if r == '\n' {
			t.line++
			t.column = 1
		} else {
			t.column++
		}
		t.position += size
	}
}

func (t *AdvancedTokenizer) skipWhitespace() {
	for t.position < len(t.input) {
		r, _ := utf8.DecodeRuneInString(t.input[t.position:])
		if !unicode.IsSpace(r) {
			break
		}
		t.advance(1)
	}
}

func (t *AdvancedTokenizer) makeToken(tokenType TokenType, value string) Token {
	pos := Position{Offset: t.position, Line: t.line, Column: t.column}
	return Token{
		Type:    tokenType,
		Literal: value,
		Pos:     pos,
		End:     Position{Offset: pos.Offset + len(value), Line: pos.Line, Column: pos.Column + len(value)},
	}
}

func (t *AdvancedTokenizer) makeTokenAt(tokenType TokenType, value string, offset, line, column int) Token {
	pos := Position{Offset: offset, Line: line, Column: column}
	return Token{
		Type:    tokenType,
		Literal: value,
		Pos:     pos,
		End:     Position{Offset: pos.Offset + len(value), Line: pos.Line, Column: pos.Column + len(value)},
	}
}

// scanString scans a quoted string with escape sequence handling.
func (t *AdvancedTokenizer) scanString(quote rune) Token {
	start := t.position
	startLine := t.line
	startColumn := t.column

	t.advance(1) // consume opening quote

	var value strings.Builder
	value.WriteRune(quote) // include opening quote in raw value

	for t.position < len(t.input) {
		ch := t.current()

		if ch == quote {
			value.WriteRune(ch)
			t.advance(1) // consume closing quote
			return Token{
				Type:    TokenString,
				Literal: value.String(),
				Pos:     Position{Offset: start, Line: startLine, Column: startColumn},
				End:     Position{Offset: t.position, Line: t.line, Column: t.column},
			}
		}

		if ch == '\\' {
			// Handle escape sequences
			value.WriteRune(ch)
			t.advance(1)

			if t.position < len(t.input) {
				escaped := t.current()
				value.WriteRune(escaped)
				t.advance(1)
			}
		} else {
			value.WriteRune(ch)
			t.advance(1)
		}
	}

	// Unclosed string
	return Token{
		Type:    TokenInvalid,
		Literal: "unclosed string literal",
		Pos:     Position{Offset: start, Line: startLine, Column: startColumn},
		End:     Position{Offset: t.position, Line: t.line, Column: t.column},
	}
}

// scanNumber scans an integer or a floating-point number, or a name
// that starts with a digit. The number grammar is numberEnd's, so an 'e'
// is an exponent only when a digit follows it or its sign. When a letter
// or '_' follows the number, the whole run is a name, as spruce reads
// "3abc", "3em", "1e", and "2e5x", and the scanner returns it as a
// reference. The '-' of a negative number is a separate TokenMinus, which
// keeps precedence parsing simple.
func (t *AdvancedTokenizer) scanNumber() Token {
	start := t.position
	startLine := t.line
	startColumn := t.column

	runes := []rune(t.input[t.position:])
	end := numberEnd(runes, 0)
	tokenType := TokenInteger
	if nameEnd, isName := digitLedNameEnd(runes, 0); isName {
		end = nameEnd
		tokenType = TokenReference
	} else if strings.ContainsAny(string(runes[:end]), ".eE") {
		tokenType = TokenFloat
	}
	t.advance(end)

	return Token{
		Type:    tokenType,
		Literal: t.input[start:t.position],
		Pos:     Position{Offset: start, Line: startLine, Column: startColumn},
		End:     Position{Offset: t.position, Line: t.line, Column: t.column},
	}
}

// scanIdentifier scans identifiers and keywords.
func (t *AdvancedTokenizer) scanIdentifier() Token {
	start := t.position
	startLine := t.line
	startColumn := t.column

	var value strings.Builder

	// Scan identifier characters
	for t.position < len(t.input) && isIdentifierContinue(t.current()) {
		value.WriteRune(t.current())
		t.advance(1)
	}

	str := value.String()
	tokenType := TokenIdentifier

	// Check for keywords/literals - only specific case variations
	switch str {
	case "true", "TRUE", "True":
		tokenType = TokenBoolean
	case "false", "FALSE", "False":
		tokenType = TokenBoolean
	case "nil", "Nil", "NIL":
		tokenType = TokenNull
	case "null", "Null", "NULL":
		tokenType = TokenNull
	}

	return Token{
		Type:    tokenType,
		Literal: str,
		Pos:     Position{Offset: start, Line: startLine, Column: startColumn},
		End:     Position{Offset: t.position, Line: t.line, Column: t.column},
	}
}

// scanEnvironmentVar scans environment variable references.
func (t *AdvancedTokenizer) scanEnvironmentVar() Token {
	start := t.position
	startLine := t.line
	startColumn := t.column

	var value strings.Builder
	value.WriteRune('$') // include $ in value
	t.advance(1)         // consume $

	// Check for ${VAR} syntax
	if t.position < len(t.input) && t.current() == '{' {
		value.WriteRune('{')
		t.advance(1)

		// Scan until closing }
		for t.position < len(t.input) && t.current() != '}' {
			value.WriteRune(t.current())
			t.advance(1)
		}

		if t.position < len(t.input) && t.current() == '}' {
			value.WriteRune('}')
			t.advance(1)
		} else {
			// Unclosed ${
			return Token{
				Type:    TokenInvalid,
				Literal: "unclosed environment variable ${",
				Pos:     Position{Offset: start, Line: startLine, Column: startColumn},
				End:     Position{Offset: t.position, Line: t.line, Column: t.column},
			}
		}
	} else {
		// Simple $VAR syntax
		if t.position >= len(t.input) || !isIdentifierStart(t.current()) {
			return Token{
				Type:    TokenInvalid,
				Literal: "invalid environment variable syntax",
				Pos:     Position{Offset: start, Line: startLine, Column: startColumn},
				End:     Position{Offset: t.position, Line: t.line, Column: t.column},
			}
		}

		// Scan variable name
		for t.position < len(t.input) && isIdentifierContinue(t.current()) {
			value.WriteRune(t.current())
			t.advance(1)
		}
	}

	return Token{
		Type:    TokenEnvironment,
		Literal: value.String(),
		Pos:     Position{Offset: start, Line: startLine, Column: startColumn},
		End:     Position{Offset: t.position, Line: t.line, Column: t.column},
	}
}
