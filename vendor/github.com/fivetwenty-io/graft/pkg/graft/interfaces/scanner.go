// Package interfaces defines core types for the graft parser and evaluator.
package interfaces

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Scanner tokenizes operator expressions from source text.
// It implements a simple, position-tracking lexer with peek capabilities.
//
// The scanner recognizes:
//   - Literals: integers, floats, strings (double-quoted), raw strings (single-quoted)
//   - Identifiers: alphanumeric names starting with a letter or underscore
//   - Keywords: if, else, for, in, etc.
//   - Operators: +, -, *, /, ==, !=, &&, ||, etc.
//   - Delimiters: (, ), [, ], {, }, comma, etc.
//   - Operator markers: (( and ))
//   - Named operators: grab, concat, vault, static_ips, etc.
//
// Example usage:
//
//	scanner := NewScanner("grab foo.bar || \"default\"")
//	for {
//	    tok := scanner.Scan()
//	    if tok.Type == TokenEOF {
//	        break
//	    }
//	    fmt.Println(tok)
//	}
type Scanner struct {
	source []byte // Source text being scanned
	file   string // Source file name (optional)

	// Position tracking
	offset     int // Current byte offset in source
	line       int // Current line number (1-based)
	column     int // Current column number (1-based)
	lineOffset int // Byte offset of current line start

	// Peek cache for lookahead
	peeked    []Token // Cached peeked tokens
	peekIndex int     // Current position in peek cache

	// Error state
	errors []error
}

// NewScanner creates a new Scanner for the given source string.
// The scanner is immediately ready to use with Scan() or Peek().
func NewScanner(source string) *Scanner {
	return &Scanner{
		source: []byte(source),
		line:   1,
		column: 1,
	}
}

// NewScannerWithFile creates a new Scanner with file information for error reporting.
func NewScannerWithFile(source, filename string) *Scanner {
	s := NewScanner(source)
	s.file = filename
	return s
}

// Reset reinitializes the scanner with new source text.
// This clears all state including peeked tokens and errors.
func (s *Scanner) Reset(source string) {
	s.source = []byte(source)
	s.offset = 0
	s.line = 1
	s.column = 1
	s.lineOffset = 0
	s.peeked = nil
	s.peekIndex = 0
	s.errors = nil
}

// Scan returns the next token from the source.
// Returns TokenEOF when the end of input is reached.
// Returns TokenInvalid with an error message for invalid tokens.
func (s *Scanner) Scan() Token {
	// Return peeked token if available
	if s.peekIndex < len(s.peeked) {
		tok := s.peeked[s.peekIndex]
		s.peekIndex++
		// Clear consumed tokens periodically
		if s.peekIndex >= len(s.peeked) {
			s.peeked = nil
			s.peekIndex = 0
		}
		return tok
	}

	return s.scanToken()
}

// Peek returns the next token without advancing the scanner.
// Multiple calls to Peek return the same token until Scan is called.
func (s *Scanner) Peek() Token {
	return s.PeekN(0)
}

// PeekN returns the token n positions ahead without advancing the scanner.
// PeekN(0) is equivalent to Peek().
func (s *Scanner) PeekN(n int) Token {
	// Ensure we have enough peeked tokens
	needed := s.peekIndex + n + 1
	for len(s.peeked) < needed {
		tok := s.scanToken()
		s.peeked = append(s.peeked, tok)
		if tok.Type == TokenEOF {
			break
		}
	}

	idx := s.peekIndex + n
	if idx < len(s.peeked) {
		return s.peeked[idx]
	}
	// Return last token (should be EOF) if beyond end
	if len(s.peeked) > 0 {
		return s.peeked[len(s.peeked)-1]
	}
	return s.makeEOFToken()
}

// Pos returns the current position in the source.
func (s *Scanner) Pos() Position {
	return Position{
		Line:   s.line,
		Column: s.column,
		Offset: s.offset,
		File:   s.file,
	}
}

// EOF returns true if the scanner has reached the end of input.
func (s *Scanner) EOF() bool {
	// Check peek cache first
	if s.peekIndex < len(s.peeked) {
		return s.peeked[s.peekIndex].Type == TokenEOF
	}
	s.skipWhitespace()
	return s.offset >= len(s.source)
}

// Errors returns any errors encountered during scanning.
func (s *Scanner) Errors() []error {
	return s.errors
}

// Source returns the original source text.
func (s *Scanner) Source() string {
	return string(s.source)
}

// scanToken scans and returns the next token.
//
//nolint:gocyclo // token scanning requires multiple character type checks
func (s *Scanner) scanToken() Token {
	s.skipWhitespace()

	if s.offset >= len(s.source) {
		return s.makeEOFToken()
	}

	startPos := s.Pos()
	ch := s.current()

	// Check for operator markers (( and ))
	if ch == '(' && s.peek() == '(' {
		s.advance()
		s.advance()
		return NewToken(TokenOperatorStart, "((", startPos, s.Pos())
	}
	if ch == ')' && s.peek() == ')' {
		s.advance()
		s.advance()
		return NewToken(TokenOperatorEnd, "))", startPos, s.Pos())
	}

	// Multi-character operators
	if tok := s.scanMultiCharOperator(startPos); tok != nil {
		return *tok
	}

	// Single character tokens
	if tok := s.scanSingleCharToken(startPos); tok != nil {
		return *tok
	}

	// Numbers (including negative and leading decimal)
	if isDigit(ch) || (ch == '-' && isDigit(s.peek())) || (ch == '.' && isDigit(s.peek())) {
		return s.scanNumber(startPos)
	}

	// Double-quoted strings
	if ch == '"' {
		return s.scanString(startPos)
	}

	// Single-quoted raw strings
	if ch == '\'' {
		return s.scanRawString(startPos)
	}

	// Environment variables
	if ch == '$' {
		return s.scanEnvironment(startPos)
	}

	// Identifiers and keywords
	if isIdentStart(ch) {
		return s.scanIdentifier(startPos)
	}

	// Unknown character
	s.advance()
	return s.makeErrorToken(startPos, fmt.Sprintf("unexpected character %q", ch))
}

// scanMultiCharOperator scans multi-character operators.
func (s *Scanner) scanMultiCharOperator(startPos Position) *Token {
	ch := s.current()
	next := s.peek()

	switch ch {
	case '=':
		if next == '=' {
			s.advance()
			s.advance()
			tok := NewToken(TokenEqual, "==", startPos, s.Pos())
			return &tok
		}
	case '!':
		if next == '=' {
			s.advance()
			s.advance()
			tok := NewToken(TokenNotEqual, "!=", startPos, s.Pos())
			return &tok
		}
	case '<':
		if next == '=' {
			s.advance()
			s.advance()
			tok := NewToken(TokenLessEqual, "<=", startPos, s.Pos())
			return &tok
		}
	case '>':
		if next == '=' {
			s.advance()
			s.advance()
			tok := NewToken(TokenGreaterEqual, ">=", startPos, s.Pos())
			return &tok
		}
	case '&':
		if next == '&' {
			s.advance()
			s.advance()
			tok := NewToken(TokenAnd, "&&", startPos, s.Pos())
			return &tok
		}
	case '|':
		if next == '|' {
			s.advance()
			s.advance()
			tok := NewToken(TokenOr, "||", startPos, s.Pos())
			return &tok
		}
	}

	return nil
}

// scanSingleCharToken scans single-character tokens.
//
//nolint:gocyclo // large switch for all single-character tokens
func (s *Scanner) scanSingleCharToken(startPos Position) *Token {
	ch := s.current()

	var tokType TokenType
	switch ch {
	case '+':
		tokType = TokenPlus
	case '-':
		// Check if this is a negative number
		if isDigit(s.peek()) {
			return nil // Let scanNumber handle it
		}
		tokType = TokenMinus
	case '*':
		tokType = TokenStar
	case '/':
		tokType = TokenSlash
	case '%':
		tokType = TokenPercent
	case '<':
		tokType = TokenLess
	case '>':
		tokType = TokenGreater
	case '!':
		tokType = TokenNot
	case '?':
		tokType = TokenQuestion
	case ':':
		tokType = TokenColon
	case '(':
		tokType = TokenLeftParen
	case ')':
		tokType = TokenRightParen
	case '[':
		tokType = TokenLeftBracket
	case ']':
		tokType = TokenRightBracket
	case '{':
		tokType = TokenLeftBrace
	case '}':
		tokType = TokenRightBrace
	case ',':
		tokType = TokenComma
	case '.':
		// Check if this is a decimal number (e.g., .5, .001)
		if isDigit(s.peek()) {
			return nil // Let scanNumber handle it
		}
		tokType = TokenDot
	case '@':
		tokType = TokenAt
	case '|':
		tokType = TokenPipe
	default:
		return nil
	}

	s.advance()
	tok := NewToken(tokType, string(ch), startPos, s.Pos())
	return &tok
}

// scanNumber scans an integer or float literal.
// Supports:
//   - Integers: 42, -17, 0
//   - Floats: 3.14, -2.5, .5
//   - Scientific notation: 1e10, 2.5e-3, 1E+5
//   - Hex: 0x1F, 0xFF
//
//nolint:gocyclo // number scanning has many format variations to handle
func (s *Scanner) scanNumber(startPos Position) Token {
	var buf strings.Builder
	isFloat := false

	// Handle negative sign
	if s.current() == '-' {
		buf.WriteByte('-')
		s.advance()
	}

	// Scan integer part
	switch {
	case s.current() == '0':
		buf.WriteByte('0')
		s.advance()
		// Check for hex
		if s.current() == 'x' || s.current() == 'X' {
			buf.WriteByte(s.current())
			s.advance()
			return s.scanHexNumber(startPos, &buf)
		}
	case isDigit(s.current()):
		s.scanDigits(&buf)
	case s.current() == '.' && isDigit(s.peek()):
		// Handle numbers starting with decimal point (e.g., .5)
		isFloat = true
	}

	// Scan decimal part
	if s.current() == '.' {
		next := s.peek()
		// Check if this is a float or a dot accessor
		if isDigit(next) || next == 'e' || next == 'E' {
			isFloat = true
			buf.WriteByte('.')
			s.advance()
			s.scanDigits(&buf)
		}
	}

	// Scan exponent part
	if s.current() == 'e' || s.current() == 'E' {
		isFloat = true
		buf.WriteByte(s.current())
		s.advance()
		if s.current() == '+' || s.current() == '-' {
			buf.WriteByte(s.current())
			s.advance()
		}
		if !isDigit(s.current()) {
			return s.makeErrorToken(startPos, "invalid number: expected digits after exponent")
		}
		s.scanDigits(&buf)
	}

	literal := buf.String()
	endPos := s.Pos()

	if isFloat {
		val, err := strconv.ParseFloat(literal, 64)
		if err != nil {
			return s.makeErrorToken(startPos, fmt.Sprintf("invalid float: %s", err))
		}
		return NewFloatToken(val, literal, startPos, endPos)
	}

	val, err := strconv.ParseInt(literal, 10, 64)
	if err != nil {
		// If int64 overflows, try parsing as float64
		if floatVal, floatErr := strconv.ParseFloat(literal, 64); floatErr == nil {
			return NewFloatToken(floatVal, literal, startPos, endPos)
		}
		return s.makeErrorToken(startPos, fmt.Sprintf("invalid integer: %s", err))
	}
	return NewIntToken(val, literal, startPos, endPos)
}

// scanHexNumber scans a hexadecimal number after "0x" prefix.
func (s *Scanner) scanHexNumber(startPos Position, buf *strings.Builder) Token {
	if !isHexDigit(s.current()) {
		return s.makeErrorToken(startPos, "invalid hex number: expected hex digits after 0x")
	}

	for isHexDigit(s.current()) {
		buf.WriteByte(s.current())
		s.advance()
	}

	literal := buf.String()
	val, err := strconv.ParseInt(literal, 0, 64)
	if err != nil {
		return s.makeErrorToken(startPos, fmt.Sprintf("invalid hex number: %s", err))
	}
	return NewIntToken(val, literal, startPos, s.Pos())
}

// scanDigits scans consecutive digits into the buffer.
func (s *Scanner) scanDigits(buf *strings.Builder) {
	for isDigit(s.current()) {
		buf.WriteByte(s.current())
		s.advance()
	}
}

// scanString scans a double-quoted string with escape sequences.
// Supports: \n, \t, \r, \\, \", \', \0, \xNN, \uNNNN.
func (s *Scanner) scanString(startPos Position) Token {
	s.advance() // consume opening quote

	var buf strings.Builder
	var literalBuf strings.Builder
	literalBuf.WriteByte('"')

	for {
		if s.offset >= len(s.source) {
			return s.makeErrorToken(startPos, "unterminated string literal")
		}

		ch := s.current()

		if ch == '"' {
			literalBuf.WriteByte('"')
			s.advance() // consume closing quote
			return NewStringToken(buf.String(), literalBuf.String(), startPos, s.Pos())
		}

		if ch == '\n' {
			return s.makeErrorToken(startPos, "unterminated string literal: newline in string")
		}

		if ch == '\\' {
			literalBuf.WriteByte('\\')
			s.advance()
			escaped, escapeLiteral, err := s.scanEscapeSequence()
			if err != nil {
				return s.makeErrorToken(startPos, err.Error())
			}
			buf.WriteString(escaped)
			literalBuf.WriteString(escapeLiteral)
		} else {
			buf.WriteByte(ch)
			literalBuf.WriteByte(ch)
			s.advance()
		}
	}
}

// scanEscapeSequence handles escape sequences in strings.
// Returns (interpreted value, literal representation, error).
func (s *Scanner) scanEscapeSequence() (interpreted, literal string, err error) {
	if s.offset >= len(s.source) {
		return "", "", fmt.Errorf("unexpected end of string after escape character")
	}

	ch := s.current()
	s.advance()

	switch ch {
	case 'n':
		return "\n", "n", nil
	case 't':
		return "\t", "t", nil
	case 'r':
		return "\r", "r", nil
	case '\\':
		return "\\", "\\", nil
	case '"':
		return "\"", "\"", nil
	case '\'':
		return "'", "'", nil
	case '0':
		return "\x00", "0", nil
	case 'x':
		// Hex escape \xNN
		val, literal, err := s.scanHexEscape(2)
		return val, "x" + literal, err
	case 'u':
		// Unicode escape \uNNNN
		val, literal, err := s.scanHexEscape(4)
		return val, "u" + literal, err
	case 'U':
		// Extended unicode escape \UNNNNNNNN
		val, literal, err := s.scanHexEscape(8)
		return val, "U" + literal, err
	default:
		return "", "", fmt.Errorf("invalid escape sequence: \\%c", ch)
	}
}

// scanHexEscape scans n hex digits and returns the corresponding character.
func (s *Scanner) scanHexEscape(n int) (valStr, litStr string, err error) {
	var val rune
	var litBuf strings.Builder
	for i := 0; i < n; i++ {
		if s.offset >= len(s.source) {
			return "", "", fmt.Errorf("invalid escape sequence: expected %d hex digits", n)
		}
		ch := s.current()
		if !isHexDigit(ch) {
			return "", "", fmt.Errorf("invalid escape sequence: expected hex digit, got %q", ch)
		}
		litBuf.WriteByte(ch)
		val = val*16 + hexValue(ch)
		s.advance()
	}
	return string(val), litBuf.String(), nil
}

// scanRawString scans a single-quoted raw string (no escape processing).
func (s *Scanner) scanRawString(startPos Position) Token {
	s.advance() // consume opening quote

	var buf strings.Builder
	var literalBuf strings.Builder
	literalBuf.WriteByte('\'')

	for {
		if s.offset >= len(s.source) {
			return s.makeErrorToken(startPos, "unterminated raw string literal")
		}

		ch := s.current()

		if ch == '\'' {
			literalBuf.WriteByte('\'')
			s.advance() // consume closing quote
			tok := NewStringToken(buf.String(), literalBuf.String(), startPos, s.Pos())
			tok.Type = TokenRawString
			return tok
		}

		// Raw strings allow newlines
		if ch == '\n' {
			s.line++
			s.column = 0 // will be incremented by advance()
			s.lineOffset = s.offset + 1
		}

		buf.WriteByte(ch)
		literalBuf.WriteByte(ch)
		s.advance()
	}
}

// scanEnvironment scans an environment variable reference ($VAR or ${VAR}).
func (s *Scanner) scanEnvironment(startPos Position) Token {
	s.advance() // consume $

	if s.offset >= len(s.source) {
		return s.makeErrorToken(startPos, "unexpected end of input after $")
	}

	var nameBuf strings.Builder
	var literalBuf strings.Builder
	literalBuf.WriteByte('$')

	// Handle ${VAR} syntax
	if s.current() == '{' {
		literalBuf.WriteByte('{')
		s.advance()
		for s.offset < len(s.source) && s.current() != '}' {
			if !isEnvChar(s.current()) {
				return s.makeErrorToken(startPos, fmt.Sprintf("invalid character in environment variable: %q", s.current()))
			}
			nameBuf.WriteByte(s.current())
			literalBuf.WriteByte(s.current())
			s.advance()
		}
		if s.offset >= len(s.source) {
			return s.makeErrorToken(startPos, "unterminated environment variable: expected '}'")
		}
		literalBuf.WriteByte('}')
		s.advance() // consume }
		return NewEnvVarToken(nameBuf.String(), literalBuf.String(), startPos, s.Pos())
	}

	// Handle $VAR syntax
	if !isEnvStart(s.current()) {
		return s.makeErrorToken(startPos, fmt.Sprintf("invalid environment variable name: expected letter or underscore, got %q", s.current()))
	}

	for s.offset < len(s.source) && isEnvChar(s.current()) {
		nameBuf.WriteByte(s.current())
		literalBuf.WriteByte(s.current())
		s.advance()
	}

	return NewEnvVarToken(nameBuf.String(), literalBuf.String(), startPos, s.Pos())
}

// scanIdentifier scans an identifier or keyword.
func (s *Scanner) scanIdentifier(startPos Position) Token {
	var buf strings.Builder

	for s.offset < len(s.source) && isIdentChar(s.current()) {
		buf.WriteByte(s.current())
		s.advance()
	}

	value := buf.String()
	endPos := s.Pos()

	// Check if it's a keyword or named operator
	tokType := LookupKeyword(value)

	// Handle boolean and null literals specially
	switch value {
	case literalTrue:
		return NewBoolToken(true, value, startPos, endPos)
	case literalFalse:
		return NewBoolToken(false, value, startPos, endPos)
	case literalNull, literalNil:
		return NewNullToken(value, startPos, endPos)
	}

	if tokType != TokenIdentifier {
		return NewToken(tokType, value, startPos, endPos)
	}

	return NewIdentToken(value, startPos, endPos)
}

// skipWhitespace skips whitespace and comments.
func (s *Scanner) skipWhitespace() {
	for s.offset < len(s.source) {
		switch ch := s.current(); ch {
		case ' ', '\t', '\r':
			s.advance()
		case '\n':
			s.advance()
			s.line++
			s.column = 1
			s.lineOffset = s.offset
		case '#':
			// Skip comment until end of line
			s.skipLineComment()
		default:
			return
		}
	}
}

// skipLineComment skips a # comment to end of line.
func (s *Scanner) skipLineComment() {
	for s.offset < len(s.source) && s.current() != '\n' {
		s.advance()
	}
}

// current returns the current byte without advancing.
func (s *Scanner) current() byte {
	if s.offset >= len(s.source) {
		return 0
	}
	return s.source[s.offset]
}

// peek returns the next byte without advancing.
func (s *Scanner) peek() byte {
	if s.offset+1 >= len(s.source) {
		return 0
	}
	return s.source[s.offset+1]
}

// advance moves to the next byte and updates position.
func (s *Scanner) advance() {
	if s.offset < len(s.source) {
		s.offset++
		s.column++
	}
}

// makeEOFToken creates an EOF token at the current position.
func (s *Scanner) makeEOFToken() Token {
	return NewEOFToken(s.Pos())
}

// makeErrorToken creates an error token with a message.
func (s *Scanner) makeErrorToken(startPos Position, message string) Token {
	err := fmt.Errorf("%s at %s", message, startPos)
	s.errors = append(s.errors, err)
	return NewErrorToken(message, startPos, s.Pos())
}

// Helper functions for character classification

// isDigit returns true if ch is a decimal digit.
func isDigit(ch byte) bool {
	return ch >= '0' && ch <= '9'
}

// isHexDigit returns true if ch is a hexadecimal digit.
func isHexDigit(ch byte) bool {
	return isDigit(ch) || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')
}

// hexValue returns the numeric value of a hex digit.
func hexValue(ch byte) rune {
	switch {
	case ch >= '0' && ch <= '9':
		return rune(ch - '0')
	case ch >= 'a' && ch <= 'f':
		return rune(ch - 'a' + 10)
	case ch >= 'A' && ch <= 'F':
		return rune(ch - 'A' + 10)
	}
	return 0
}

// isIdentStart returns true if ch can start an identifier.
func isIdentStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
}

// isIdentChar returns true if ch can be part of an identifier.
func isIdentChar(ch byte) bool {
	return isIdentStart(ch) || isDigit(ch) || ch == '-'
}

// isEnvStart returns true if ch can start an environment variable name.
func isEnvStart(ch byte) bool {
	return isIdentStart(ch)
}

// isEnvChar returns true if ch can be part of an environment variable name.
func isEnvChar(ch byte) bool {
	return isIdentStart(ch) || isDigit(ch)
}

// ScannerInterface defines the interface for tokenizing operator expressions.
// This interface allows for different scanner implementations.
type ScannerInterface interface {
	// Scan returns the next token
	Scan() Token

	// Peek returns the next token without consuming it
	Peek() Token

	// PeekN returns the token n positions ahead
	PeekN(n int) Token

	// Pos returns the current position
	Pos() Position

	// EOF returns true if at end of input
	EOF() bool

	// Reset reinitializes with new input
	Reset(source string)
}

// Ensure Scanner implements ScannerInterface.
var _ ScannerInterface = (*Scanner)(nil)

// TokenizeAll tokenizes the entire source and returns all tokens.
// Useful for debugging and testing.
func TokenizeAll(source string) []Token {
	s := NewScanner(source)
	// Estimate capacity based on source length (average ~5 chars per token)
	tokens := make([]Token, 0, len(source)/5+1)
	for {
		tok := s.Scan()
		tokens = append(tokens, tok)
		if tok.Type == TokenEOF {
			break
		}
	}
	return tokens
}

// ScannerError represents an error encountered during scanning.
type ScannerError struct {
	Message  string
	Position Position
	Source   string
}

// Error implements the error interface.
func (e *ScannerError) Error() string {
	return fmt.Sprintf("scanner error at %s: %s", e.Position, e.Message)
}

// IsValidIdentifier checks if a string is a valid identifier.
func IsValidIdentifier(s string) bool {
	if s == "" {
		return false
	}

	// First character must be letter or underscore
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError || (!unicode.IsLetter(r) && r != '_') {
		return false
	}

	// Rest can be letters, digits, underscores, or hyphens
	for i := size; i < len(s); {
		r, size = utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError {
			return false
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return false
		}
		i += size
	}

	return true
}
