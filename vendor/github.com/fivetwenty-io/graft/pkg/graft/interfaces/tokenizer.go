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

// consumeSegmentIdentifier consumes an identifier appearing after a dot,
// where a '+' followed by an identifier character continues the same
// segment: genesis's vaultified manifests use map keys like
// "haproxy_ssl+certificate", and spruce lexes the whole whitespace-free
// token as one reference. A '+' before a digit is left alone so "count+1"
// stays arithmetic. The leading segment of a path gets no such treatment.
func consumeSegmentIdentifier(runes []rune, pos int) int {
	pos = consumeIdentifierRun(runes, pos)
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
//
//nolint:gocyclo // complex pattern matching for nested brackets and quotes
func (p *ArrayReferencePattern) Match(input string, offset int) (bool, int) {
	// Match pattern: identifier([expr]|.identifier)*
	// This is more complex as it needs to handle nested brackets and quotes

	runes := []rune(input[offset:])
	if len(runes) == 0 {
		return false, 0
	}

	pos := 0

	// Must start with identifier or environment variable
	switch {
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

	// Look for additional segments: [expr] or .identifier
	for pos < len(runes) {
		switch runes[pos] {
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
				return pos > 0, pos
			}
		case '.':
			pos++ // consume '.'

			// Check what follows the dot
			if pos >= len(runes) {
				return pos > 0, pos // End of input
			}

			// Allow either identifier, number, bracket, or an embedded
			// environment-variable segment after dot
			switch {
			case runes[pos] == '[':
				// Continue with bracket parsing (handled in next iteration)
				continue
			case runes[pos] == '$':
				// An embedded environment-variable segment, e.g.
				// "X.$ENV.field" - spruce substitutes $VAR anywhere in a
				// dotted reference path, not only as the reference's
				// leading segment (the case == '$' branch above, which
				// this mirrors). ResolveEnv (operators/types.go) already
				// substitutes every "$..."-prefixed Reference.Nodes entry
				// wherever it appears, not just the first, so matching the
				// run as part of this one reference token is the only
				// change needed. An invalid name (no identifier
				// immediately after '$') leaves pos unadvanced, falling
				// through the outer loop's default case below and
				// stopping the match at the dot, same as any other
				// invalid continuation.
				if pos+1 < len(runes) && isIdentifierStart(runes[pos+1]) {
					pos++ // consume '$'
					for pos < len(runes) && isIdentifierContinue(runes[pos]) {
						pos++
					}
				}
			case unicode.IsDigit(runes[pos]):
				// Consume numeric index
				for pos < len(runes) && unicode.IsDigit(runes[pos]) {
					pos++
				}
			case isIdentifierStart(runes[pos]):
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
				return pos > 0, pos // Not a valid continuation
			}
		default:
			// End of reference path
			return pos > 0, pos
		}
	}

	return pos > 0, pos
}

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

// scanToken performs the actual tokenization.
func (t *AdvancedTokenizer) scanToken() Token {
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
			if strings.Contains(value, ".") || strings.Contains(value, "[") {
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

// scanNumber scans integer and floating-point numbers.
//
//nolint:gocyclo // number scanning handles integers, floats, and exponents
func (t *AdvancedTokenizer) scanNumber() Token {
	start := t.position
	startLine := t.line
	startColumn := t.column

	var value strings.Builder
	isFloat := false

	// Note: negative sign is handled as separate token (TokenMinus)
	// This allows for proper precedence parsing

	// Scan integer part
	for t.position < len(t.input) && unicode.IsDigit(t.current()) {
		value.WriteRune(t.current())
		t.advance(1)
	}

	// Check for decimal point
	if t.position < len(t.input) && t.current() == '.' {
		// Look ahead to ensure there's a digit after the decimal
		if t.position+1 < len(t.input) && unicode.IsDigit(rune(t.input[t.position+1])) {
			isFloat = true
			value.WriteRune('.')
			t.advance(1)

			// Scan fractional part
			for t.position < len(t.input) && unicode.IsDigit(t.current()) {
				value.WriteRune(t.current())
				t.advance(1)
			}
		}
	}

	// Check for exponent
	if t.position < len(t.input) && (t.current() == 'e' || t.current() == 'E') {
		isFloat = true
		value.WriteRune(t.current())
		t.advance(1)

		// Handle optional sign
		if t.position < len(t.input) && (t.current() == '+' || t.current() == '-') {
			value.WriteRune(t.current())
			t.advance(1)
		}

		// Scan exponent digits
		for t.position < len(t.input) && unicode.IsDigit(t.current()) {
			value.WriteRune(t.current())
			t.advance(1)
		}
	}

	tokenType := TokenInteger
	if isFloat {
		tokenType = TokenFloat
	}

	return Token{
		Type:    tokenType,
		Literal: value.String(),
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
