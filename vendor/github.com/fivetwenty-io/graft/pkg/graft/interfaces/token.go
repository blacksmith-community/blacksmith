// Package interfaces defines core types for the graft parser and evaluator.
package interfaces

import (
	"fmt"
)

// TokenType represents the type of a token in the operator expression language.
// Token types are categorized by function and match the design document specification.
type TokenType int

// TokenType constants.
const (
	// TokenEOF represents end of input.
	TokenEOF TokenType = iota
	// TokenInvalid represents a lexical error / invalid token.
	TokenInvalid

	// TokenOperatorStart represents (( - operator expression start.
	TokenOperatorStart
	// TokenOperatorEnd represents )) - operator expression end.
	TokenOperatorEnd

	// TokenInteger represents an integer literal: 42, -17, 0xFF.
	TokenInteger
	// TokenFloat represents a float literal: 3.14, -2.5, 1e10.
	TokenFloat
	// TokenString represents a string literal: "hello".
	TokenString
	// TokenRawString represents a raw string literal: 'world' (single-quoted).
	TokenRawString
	// TokenBoolean represents a boolean literal: true, false.
	TokenBoolean
	// TokenNull represents a null literal: null, nil, ~.
	TokenNull

	// TokenIdentifier represents an identifier: foo, bar_baz.
	TokenIdentifier
	// TokenReference represents a path reference: meta.name, foo.bar[0].
	TokenReference
	// TokenEnvironment represents an environment variable: $ENV_VAR, $HOME.
	TokenEnvironment

	// TokenPlus represents the + operator.
	TokenPlus
	// TokenMinus represents the - operator.
	TokenMinus
	// TokenStar represents the * operator.
	TokenStar
	// TokenSlash represents the / operator.
	TokenSlash
	// TokenPercent represents the % operator.
	TokenPercent

	// TokenEqual represents the == operator.
	TokenEqual
	// TokenNotEqual represents the != operator.
	TokenNotEqual
	// TokenLess represents the < operator.
	TokenLess
	// TokenGreater represents the > operator.
	TokenGreater
	// TokenLessEqual represents the <= operator.
	TokenLessEqual
	// TokenGreaterEqual represents the >= operator.
	TokenGreaterEqual

	// TokenAnd represents the && operator.
	TokenAnd
	// TokenOr represents the || operator.
	TokenOr
	// TokenNot represents the ! operator.
	TokenNot

	// TokenQuestion represents the ? operator.
	TokenQuestion
	// TokenColon represents the : operator.
	TokenColon

	// TokenLeftParen represents the ( delimiter.
	TokenLeftParen
	// TokenRightParen represents the ) delimiter.
	TokenRightParen
	// TokenLeftBracket represents the [ delimiter.
	TokenLeftBracket
	// TokenRightBracket represents the ] delimiter.
	TokenRightBracket
	// TokenLeftBrace represents the { delimiter.
	TokenLeftBrace
	// TokenRightBrace represents the } delimiter.
	TokenRightBrace
	// TokenComma represents the , delimiter.
	TokenComma
	// TokenDot represents the . delimiter.
	TokenDot
	// TokenAt represents the @ delimiter.
	TokenAt
	// TokenPipe represents the | delimiter.
	TokenPipe

	// TokenIf represents the if keyword.
	TokenIf
	// TokenElif represents the elif keyword.
	TokenElif
	// TokenElse represents the else keyword.
	TokenElse
	// TokenFi represents the fi (endif) keyword.
	TokenFi
	// TokenFor represents the for keyword.
	TokenFor
	// TokenIn represents the in keyword.
	TokenIn
	// TokenDone represents the done (endfor/endwhile) keyword.
	TokenDone
	// TokenWhile represents the while keyword.
	TokenWhile
	// TokenCase represents the case keyword.
	TokenCase
	// TokenWhen represents the when keyword.
	TokenWhen
	// TokenDefault represents the default keyword.
	TokenDefault
	// TokenEsac represents the esac (endcase) keyword.
	TokenEsac
	// TokenOn represents the on (for merge on key) keyword.
	TokenOn
	// TokenBefore represents the before (for insert before) keyword.
	TokenBefore
	// TokenAfter represents the after (for insert after) keyword.
	TokenAfter

	// TokenRange represents the range keyword.
	TokenRange

	// TokenOperatorName represents operator names: grab, concat, vault, etc.
	TokenOperatorName

	// tokenTypeCount is an internal marker for counting tokens.
	tokenTypeCount
)

// Token represents a lexical token in the operator expression language.
type Token struct {
	Type    TokenType   // The type of the token
	Value   interface{} // Parsed value for literals (int64, float64, string, bool, nil)
	Literal string      // The literal/raw text of the token as it appears in source
	Pos     Position    // Start position in source
	End     Position    // End position in source
	Error   string      // Error message for TokenInvalid
}

// String returns a human-readable representation of the token.
func (t Token) String() string {
	if t.Error != "" {
		return fmt.Sprintf("Token{%s, error: %q, at %s}", t.Type, t.Error, t.Pos)
	}
	if t.Value != nil {
		return fmt.Sprintf("Token{%s, Value: %v, Literal: %q, Pos: %s}", t.Type, t.Value, t.Literal, t.Pos)
	}
	if t.Literal != "" {
		return fmt.Sprintf("Token{%s, Literal: %q, Pos: %s}", t.Type, t.Literal, t.Pos)
	}
	return fmt.Sprintf("Token{%s, Pos: %s}", t.Type, t.Pos)
}

// Range returns the source range covered by this token.
func (t Token) Range() Range {
	return NewRange(t.Pos, t.End)
}

// IsZero returns true if the token is uninitialized.
func (t Token) IsZero() bool {
	return t.Type == 0 && t.Literal == "" && t.Pos.IsZero()
}

// IsEOF returns true if the token is an end-of-file token.
func (t Token) IsEOF() bool {
	return t.Type == TokenEOF
}

// IsInvalid returns true if the token represents a lexical error.
func (t Token) IsInvalid() bool {
	return t.Type == TokenInvalid
}

// NewToken creates a new token with the specified type, literal, and positions.
func NewToken(typ TokenType, literal string, pos, end Position) Token {
	return Token{
		Type:    typ,
		Literal: literal,
		Pos:     pos,
		End:     end,
	}
}

// NewLiteralToken creates a new token with a parsed value.
func NewLiteralToken(typ TokenType, value interface{}, literal string, pos, end Position) Token {
	return Token{
		Type:    typ,
		Value:   value,
		Literal: literal,
		Pos:     pos,
		End:     end,
	}
}

// NewIntToken creates a new integer token.
func NewIntToken(value int64, literal string, pos, end Position) Token {
	return Token{
		Type:    TokenInteger,
		Value:   value,
		Literal: literal,
		Pos:     pos,
		End:     end,
	}
}

// NewFloatToken creates a new float token.
func NewFloatToken(value float64, literal string, pos, end Position) Token {
	return Token{
		Type:    TokenFloat,
		Value:   value,
		Literal: literal,
		Pos:     pos,
		End:     end,
	}
}

// NewStringToken creates a new string token.
func NewStringToken(value, literal string, pos, end Position) Token {
	return Token{
		Type:    TokenString,
		Value:   value,
		Literal: literal,
		Pos:     pos,
		End:     end,
	}
}

// NewBoolToken creates a new boolean token.
func NewBoolToken(value bool, literal string, pos, end Position) Token {
	return Token{
		Type:    TokenBoolean,
		Value:   value,
		Literal: literal,
		Pos:     pos,
		End:     end,
	}
}

// NewNullToken creates a new null token.
func NewNullToken(literal string, pos, end Position) Token {
	return Token{
		Type:    TokenNull,
		Value:   nil,
		Literal: literal,
		Pos:     pos,
		End:     end,
	}
}

// NewIdentToken creates a new identifier token.
func NewIdentToken(name string, pos, end Position) Token {
	return Token{
		Type:    TokenIdentifier,
		Value:   name,
		Literal: name,
		Pos:     pos,
		End:     end,
	}
}

// NewEnvVarToken creates a new environment variable token.
func NewEnvVarToken(name, literal string, pos, end Position) Token {
	return Token{
		Type:    TokenEnvironment,
		Value:   name, // The variable name without $
		Literal: literal,
		Pos:     pos,
		End:     end,
	}
}

// NewRefToken creates a new reference token.
func NewRefToken(path string, pos, end Position) Token {
	return Token{
		Type:    TokenReference,
		Value:   path,
		Literal: path,
		Pos:     pos,
		End:     end,
	}
}

// NewEOFToken creates an EOF token at the given position.
func NewEOFToken(pos Position) Token {
	return Token{
		Type:    TokenEOF,
		Literal: "",
		Pos:     pos,
		End:     pos,
	}
}

// NewErrorToken creates an error token with a message.
func NewErrorToken(message string, pos, end Position) Token {
	return Token{
		Type:    TokenInvalid,
		Value:   message,
		Literal: message,
		Error:   message,
		Pos:     pos,
		End:     end,
	}
}

// tokenTypeStrings maps token types to their string representation.
var tokenTypeStrings = map[TokenType]string{
	TokenEOF:           "EOF",
	TokenInvalid:       "INVALID",
	TokenOperatorStart: "((",
	TokenOperatorEnd:   "))",
	TokenInteger:       "INTEGER",
	TokenFloat:         "FLOAT",
	TokenString:        "STRING",
	TokenRawString:     "RAW_STRING",
	TokenBoolean:       "BOOLEAN",
	TokenNull:          "NULL",
	TokenIdentifier:    "IDENTIFIER",
	TokenReference:     "REFERENCE",
	TokenEnvironment:   "ENVIRONMENT",
	TokenPlus:          "+",
	TokenMinus:         "-",
	TokenStar:          "*",
	TokenSlash:         "/",
	TokenPercent:       "%",
	TokenEqual:         "==",
	TokenNotEqual:      "!=",
	TokenLess:          "<",
	TokenGreater:       ">",
	TokenLessEqual:     "<=",
	TokenGreaterEqual:  ">=",
	TokenAnd:           "&&",
	TokenOr:            "||",
	TokenNot:           "!",
	TokenQuestion:      "?",
	TokenColon:         ":",
	TokenLeftParen:     "(",
	TokenRightParen:    ")",
	TokenLeftBracket:   "[",
	TokenRightBracket:  "]",
	TokenLeftBrace:     "{",
	TokenRightBrace:    "}",
	TokenComma:         ",",
	TokenDot:           ".",
	TokenAt:            "@",
	TokenPipe:          "|",
	// Control flow
	TokenIf:      "if",
	TokenElif:    "elif",
	TokenElse:    "else",
	TokenFi:      "fi",
	TokenFor:     "for",
	TokenIn:      "in",
	TokenDone:    "done",
	TokenWhile:   "while",
	TokenCase:    "case",
	TokenWhen:    "when",
	TokenDefault: "default",
	TokenEsac:    "esac",
	TokenOn:      "on",
	TokenBefore:  "before",
	TokenAfter:   "after",
	// Built-in
	TokenRange:        "range",
	TokenOperatorName: "OPERATOR_NAME",
}

// String returns the string representation of a token type.
func (t TokenType) String() string {
	if s, ok := tokenTypeStrings[t]; ok {
		return s
	}
	return fmt.Sprintf("TokenType(%d)", int(t))
}

// Keywords maps keyword strings to their token types.
// This is used during tokenization to recognize reserved words.
var Keywords = map[string]TokenType{
	// Control flow keywords
	"if":      TokenIf,
	"elif":    TokenElif,
	"else":    TokenElse,
	"fi":      TokenFi,
	"for":     TokenFor,
	"in":      TokenIn,
	"done":    TokenDone,
	"while":   TokenWhile,
	"case":    TokenCase,
	"when":    TokenWhen,
	"default": TokenDefault,
	"esac":    TokenEsac,
	"range":   TokenRange,
	"on":      TokenOn,
	"before":  TokenBefore,
	"after":   TokenAfter,

	// Boolean literals
	"true":  TokenBoolean,
	"false": TokenBoolean,

	// Null literals
	"null": TokenNull,
	"nil":  TokenNull,
}

// OperatorNames maps graft operator names to true.
// These are the built-in operators recognized by graft.
var OperatorNames = map[string]bool{
	// Data operators
	"grab":      true,
	"concat":    true,
	"join":      true,
	"defer":     true,
	"param":     true,
	"prune":     true,
	"inject":    true,
	"stringify": true,
	"parse":     true,
	"static":    true,

	// Secret backends
	"vault":     true,
	"openbao":   true,
	"awsparam":  true,
	"awssm":     true,
	"awssecret": true,
	"nats":      true,

	// Utility operators
	"static_ips": true,
	"ips":        true,
	"calc":       true,
	"cartesian":  true,
	"empty":      true,
	"keys":       true,
	"values":     true,
	"sort":       true,
	"uniq":       true,
	"reverse":    true,
	"shuffle":    true,
	"min":        true,
	"max":        true,
	"sum":        true,
	"length":     true,
	"elem":       true,
	"index":      true,
	"flatten":    true,
	"filter":     true,
	"map":        true,
	"reduce":     true,
	"first":      true,
	"last":       true,
	"floor":      true,
	"ceil":       true,
	"round":      true,
	"abs":        true,
	"mod":        true,
	"pow":        true,
	"sqrt":       true,

	// Type conversion
	"type":          true,
	"bool":          true,
	"int":           true,
	"float":         true,
	"string":        true,
	"base64":        true,
	"base64decode":  true,
	"base64-decode": true,

	// String operators
	"split":      true,
	"replace":    true,
	"regexp":     true,
	"regex":      true,
	"substr":     true,
	"trim":       true,
	"upper":      true,
	"lower":      true,
	"contains":   true,
	"has-prefix": true,
	"has-suffix": true,

	// Existence check
	"defined": true,
	"exists":  true,

	// File operations
	"load": true,
	"file": true,

	// Environment
	"env":     true,
	"raw_env": true,

	// IP/Network
	"ip":   true,
	"net":  true,
	"cidr": true,

	// Hashing
	"md5":    true,
	"sha1":   true,
	"sha256": true,
	"sha512": true,

	// Array operations
	"append":            true,
	"prepend":           true,
	"inline":            true,
	"merge":             true,
	"insert":            true,
	"delete":            true,
	"cartesian-product": true,

	// Misc
	"any":  true,
	"all":  true,
	"none": true,
}

// LookupKeyword returns the token type for a given identifier string.
// If the identifier is a keyword, returns the keyword token type.
// If the identifier is an operator name, returns TokenOperatorName.
// Otherwise, returns TokenIdentifier.
func LookupKeyword(ident string) TokenType {
	if tok, ok := Keywords[ident]; ok {
		return tok
	}
	if OperatorNames[ident] {
		return TokenOperatorName
	}
	return TokenIdentifier
}

// IsLiteral returns true if the token type represents a literal value.
func (t TokenType) IsLiteral() bool {
	switch t {
	case TokenInteger, TokenFloat, TokenString, TokenRawString, TokenBoolean, TokenNull:
		return true
	case TokenEOF, TokenInvalid, TokenOperatorStart, TokenOperatorEnd,
		TokenIdentifier, TokenReference, TokenEnvironment,
		TokenPlus, TokenMinus, TokenStar, TokenSlash, TokenPercent,
		TokenEqual, TokenNotEqual, TokenLess, TokenGreater, TokenLessEqual, TokenGreaterEqual,
		TokenAnd, TokenOr, TokenNot, TokenQuestion, TokenColon,
		TokenLeftParen, TokenRightParen, TokenLeftBracket, TokenRightBracket,
		TokenLeftBrace, TokenRightBrace, TokenComma, TokenDot, TokenAt, TokenPipe,
		TokenIf, TokenElif, TokenElse, TokenFi, TokenFor, TokenIn, TokenDone, TokenWhile,
		TokenCase, TokenWhen, TokenDefault, TokenEsac, TokenOn, TokenBefore, TokenAfter,
		TokenRange, TokenOperatorName, tokenTypeCount:
		return false
	}
	return false
}

// IsArithmeticOperator returns true if the token type is an arithmetic operator.
func (t TokenType) IsArithmeticOperator() bool {
	switch t {
	case TokenPlus, TokenMinus, TokenStar, TokenSlash, TokenPercent:
		return true
	case TokenEOF, TokenInvalid, TokenOperatorStart, TokenOperatorEnd,
		TokenInteger, TokenFloat, TokenString, TokenRawString, TokenBoolean, TokenNull,
		TokenIdentifier, TokenReference, TokenEnvironment,
		TokenEqual, TokenNotEqual, TokenLess, TokenGreater, TokenLessEqual, TokenGreaterEqual,
		TokenAnd, TokenOr, TokenNot, TokenQuestion, TokenColon,
		TokenLeftParen, TokenRightParen, TokenLeftBracket, TokenRightBracket,
		TokenLeftBrace, TokenRightBrace, TokenComma, TokenDot, TokenAt, TokenPipe,
		TokenIf, TokenElif, TokenElse, TokenFi, TokenFor, TokenIn, TokenDone, TokenWhile,
		TokenCase, TokenWhen, TokenDefault, TokenEsac, TokenOn, TokenBefore, TokenAfter,
		TokenRange, TokenOperatorName, tokenTypeCount:
		return false
	}
	return false
}

// IsComparisonOperator returns true if the token type is a comparison operator.
func (t TokenType) IsComparisonOperator() bool {
	switch t {
	case TokenEqual, TokenNotEqual, TokenLess, TokenGreater, TokenLessEqual, TokenGreaterEqual:
		return true
	case TokenEOF, TokenInvalid, TokenOperatorStart, TokenOperatorEnd,
		TokenInteger, TokenFloat, TokenString, TokenRawString, TokenBoolean, TokenNull,
		TokenIdentifier, TokenReference, TokenEnvironment,
		TokenPlus, TokenMinus, TokenStar, TokenSlash, TokenPercent,
		TokenAnd, TokenOr, TokenNot, TokenQuestion, TokenColon,
		TokenLeftParen, TokenRightParen, TokenLeftBracket, TokenRightBracket,
		TokenLeftBrace, TokenRightBrace, TokenComma, TokenDot, TokenAt, TokenPipe,
		TokenIf, TokenElif, TokenElse, TokenFi, TokenFor, TokenIn, TokenDone, TokenWhile,
		TokenCase, TokenWhen, TokenDefault, TokenEsac, TokenOn, TokenBefore, TokenAfter,
		TokenRange, TokenOperatorName, tokenTypeCount:
		return false
	}
	return false
}

// IsLogicalOperator returns true if the token type is a logical operator.
func (t TokenType) IsLogicalOperator() bool {
	switch t {
	case TokenAnd, TokenOr, TokenNot:
		return true
	case TokenEOF, TokenInvalid, TokenOperatorStart, TokenOperatorEnd,
		TokenInteger, TokenFloat, TokenString, TokenRawString, TokenBoolean, TokenNull,
		TokenIdentifier, TokenReference, TokenEnvironment,
		TokenPlus, TokenMinus, TokenStar, TokenSlash, TokenPercent,
		TokenEqual, TokenNotEqual, TokenLess, TokenGreater, TokenLessEqual, TokenGreaterEqual,
		TokenQuestion, TokenColon,
		TokenLeftParen, TokenRightParen, TokenLeftBracket, TokenRightBracket,
		TokenLeftBrace, TokenRightBrace, TokenComma, TokenDot, TokenAt, TokenPipe,
		TokenIf, TokenElif, TokenElse, TokenFi, TokenFor, TokenIn, TokenDone, TokenWhile,
		TokenCase, TokenWhen, TokenDefault, TokenEsac, TokenOn, TokenBefore, TokenAfter,
		TokenRange, TokenOperatorName, tokenTypeCount:
		return false
	}
	return false
}

// IsOperator returns true if the token type is any symbolic operator.
func (t TokenType) IsOperator() bool {
	return t.IsArithmeticOperator() || t.IsComparisonOperator() || t.IsLogicalOperator() ||
		t == TokenQuestion || t == TokenColon
}

// IsBinaryOperator returns true if the token type is a binary operator.
func (t TokenType) IsBinaryOperator() bool {
	switch t {
	case TokenPlus, TokenMinus, TokenStar, TokenSlash, TokenPercent,
		TokenEqual, TokenNotEqual, TokenLess, TokenGreater, TokenLessEqual, TokenGreaterEqual,
		TokenAnd, TokenOr:
		return true
	case TokenEOF, TokenInvalid, TokenOperatorStart, TokenOperatorEnd,
		TokenInteger, TokenFloat, TokenString, TokenRawString, TokenBoolean, TokenNull,
		TokenIdentifier, TokenReference, TokenEnvironment,
		TokenNot, TokenQuestion, TokenColon,
		TokenLeftParen, TokenRightParen, TokenLeftBracket, TokenRightBracket,
		TokenLeftBrace, TokenRightBrace, TokenComma, TokenDot, TokenAt, TokenPipe,
		TokenIf, TokenElif, TokenElse, TokenFi, TokenFor, TokenIn, TokenDone, TokenWhile,
		TokenCase, TokenWhen, TokenDefault, TokenEsac, TokenOn, TokenBefore, TokenAfter,
		TokenRange, TokenOperatorName, tokenTypeCount:
		return false
	}
	return false
}

// IsUnaryOperator returns true if the token type can be a unary operator.
func (t TokenType) IsUnaryOperator() bool {
	switch t {
	case TokenNot, TokenMinus:
		return true
	case TokenEOF, TokenInvalid, TokenOperatorStart, TokenOperatorEnd,
		TokenInteger, TokenFloat, TokenString, TokenRawString, TokenBoolean, TokenNull,
		TokenIdentifier, TokenReference, TokenEnvironment,
		TokenPlus, TokenStar, TokenSlash, TokenPercent,
		TokenEqual, TokenNotEqual, TokenLess, TokenGreater, TokenLessEqual, TokenGreaterEqual,
		TokenAnd, TokenOr, TokenQuestion, TokenColon,
		TokenLeftParen, TokenRightParen, TokenLeftBracket, TokenRightBracket,
		TokenLeftBrace, TokenRightBrace, TokenComma, TokenDot, TokenAt, TokenPipe,
		TokenIf, TokenElif, TokenElse, TokenFi, TokenFor, TokenIn, TokenDone, TokenWhile,
		TokenCase, TokenWhen, TokenDefault, TokenEsac, TokenOn, TokenBefore, TokenAfter,
		TokenRange, TokenOperatorName, tokenTypeCount:
		return false
	}
	return false
}

// IsKeyword returns true if the token type represents a control flow keyword.
func (t TokenType) IsKeyword() bool {
	switch t {
	case TokenIf, TokenElif, TokenElse, TokenFi,
		TokenFor, TokenIn, TokenDone, TokenWhile,
		TokenCase, TokenWhen, TokenDefault, TokenEsac,
		TokenRange, TokenOn, TokenBefore, TokenAfter:
		return true
	case TokenEOF, TokenInvalid, TokenOperatorStart, TokenOperatorEnd,
		TokenInteger, TokenFloat, TokenString, TokenRawString, TokenBoolean, TokenNull,
		TokenIdentifier, TokenReference, TokenEnvironment,
		TokenPlus, TokenMinus, TokenStar, TokenSlash, TokenPercent,
		TokenEqual, TokenNotEqual, TokenLess, TokenGreater, TokenLessEqual, TokenGreaterEqual,
		TokenAnd, TokenOr, TokenNot, TokenQuestion, TokenColon,
		TokenLeftParen, TokenRightParen, TokenLeftBracket, TokenRightBracket,
		TokenLeftBrace, TokenRightBrace, TokenComma, TokenDot, TokenAt, TokenPipe,
		TokenOperatorName, tokenTypeCount:
		return false
	}
	return false
}

// IsDelimiter returns true if the token type is a delimiter.
func (t TokenType) IsDelimiter() bool {
	switch t {
	case TokenLeftParen, TokenRightParen,
		TokenLeftBracket, TokenRightBracket,
		TokenLeftBrace, TokenRightBrace,
		TokenComma, TokenDot, TokenAt, TokenPipe,
		TokenColon, TokenQuestion:
		return true
	case TokenEOF, TokenInvalid, TokenOperatorStart, TokenOperatorEnd,
		TokenInteger, TokenFloat, TokenString, TokenRawString, TokenBoolean, TokenNull,
		TokenIdentifier, TokenReference, TokenEnvironment,
		TokenPlus, TokenMinus, TokenStar, TokenSlash, TokenPercent,
		TokenEqual, TokenNotEqual, TokenLess, TokenGreater, TokenLessEqual, TokenGreaterEqual,
		TokenAnd, TokenOr, TokenNot,
		TokenIf, TokenElif, TokenElse, TokenFi, TokenFor, TokenIn, TokenDone, TokenWhile,
		TokenCase, TokenWhen, TokenDefault, TokenEsac, TokenOn, TokenBefore, TokenAfter,
		TokenRange, TokenOperatorName, tokenTypeCount:
		return false
	}
	return false
}

// IsControlToken returns true if the token type is a control token.
func (t TokenType) IsControlToken() bool {
	switch t {
	case TokenOperatorStart, TokenOperatorEnd, TokenEOF, TokenInvalid:
		return true
	case TokenInteger, TokenFloat, TokenString, TokenRawString, TokenBoolean, TokenNull,
		TokenIdentifier, TokenReference, TokenEnvironment,
		TokenPlus, TokenMinus, TokenStar, TokenSlash, TokenPercent,
		TokenEqual, TokenNotEqual, TokenLess, TokenGreater, TokenLessEqual, TokenGreaterEqual,
		TokenAnd, TokenOr, TokenNot, TokenQuestion, TokenColon,
		TokenLeftParen, TokenRightParen, TokenLeftBracket, TokenRightBracket,
		TokenLeftBrace, TokenRightBrace, TokenComma, TokenDot, TokenAt, TokenPipe,
		TokenIf, TokenElif, TokenElse, TokenFi, TokenFor, TokenIn, TokenDone, TokenWhile,
		TokenCase, TokenWhen, TokenDefault, TokenEsac, TokenOn, TokenBefore, TokenAfter,
		TokenRange, TokenOperatorName, tokenTypeCount:
		return false
	}
	return false
}

// IsNamedOperator returns true if the token type represents a named graft operator.
func (t TokenType) IsNamedOperator() bool {
	return t == TokenOperatorName
}

// CanStartExpression returns true if this token type can start an expression.
func (t TokenType) CanStartExpression() bool {
	switch t {
	case TokenInteger, TokenFloat, TokenString, TokenRawString,
		TokenBoolean, TokenNull,
		TokenIdentifier, TokenEnvironment, TokenReference,
		TokenLeftParen, TokenNot, TokenMinus,
		TokenOperatorName:
		return true
	case TokenIf, TokenElif, TokenElse, TokenFi,
		TokenFor, TokenIn, TokenDone, TokenWhile,
		TokenCase, TokenWhen, TokenDefault, TokenEsac,
		TokenRange, TokenOn, TokenBefore, TokenAfter:
		return true // Keywords can start expressions
	case TokenEOF, TokenInvalid, TokenOperatorStart, TokenOperatorEnd,
		TokenPlus, TokenStar, TokenSlash, TokenPercent,
		TokenEqual, TokenNotEqual, TokenLess, TokenGreater, TokenLessEqual, TokenGreaterEqual,
		TokenAnd, TokenOr, TokenQuestion, TokenColon,
		TokenRightParen, TokenLeftBracket, TokenRightBracket,
		TokenLeftBrace, TokenRightBrace, TokenComma, TokenDot, TokenAt, TokenPipe,
		tokenTypeCount:
		return false
	}
	return false
}

// Precedence returns the precedence level for the token type.
// Higher values bind tighter. Returns 0 for non-operator tokens.
func (t TokenType) Precedence() int {
	switch t {
	case TokenQuestion, TokenColon:
		return 1 // Ternary (lowest)
	case TokenOr:
		return 2
	case TokenAnd:
		return 3
	case TokenEqual, TokenNotEqual:
		return 4
	case TokenLess, TokenLessEqual, TokenGreater, TokenGreaterEqual:
		return 5
	case TokenPlus, TokenMinus:
		return 6
	case TokenStar, TokenSlash, TokenPercent:
		return 7
	case TokenNot:
		return 8 // Unary
	case TokenEOF, TokenInvalid, TokenOperatorStart, TokenOperatorEnd,
		TokenInteger, TokenFloat, TokenString, TokenRawString, TokenBoolean, TokenNull,
		TokenIdentifier, TokenReference, TokenEnvironment,
		TokenLeftParen, TokenRightParen, TokenLeftBracket, TokenRightBracket,
		TokenLeftBrace, TokenRightBrace, TokenComma, TokenDot, TokenAt, TokenPipe,
		TokenIf, TokenElif, TokenElse, TokenFi, TokenFor, TokenIn, TokenDone, TokenWhile,
		TokenCase, TokenWhen, TokenDefault, TokenEsac, TokenOn, TokenBefore, TokenAfter,
		TokenRange, TokenOperatorName, tokenTypeCount:
		return 0
	}
	return 0
}

// TokenCount returns the total number of defined token types.
// This is useful for testing to ensure all types are accounted for.
func TokenCount() int {
	return int(tokenTypeCount)
}

// Token method wrappers for common checks

// IsLiteral returns true if this token represents a literal value.
func (t Token) IsLiteral() bool {
	return t.Type.IsLiteral()
}

// IsOperator returns true if this token represents an operator.
func (t Token) IsOperator() bool {
	return t.Type.IsOperator()
}

// IsKeyword returns true if this token represents a keyword.
func (t Token) IsKeyword() bool {
	return t.Type.IsKeyword()
}

// IsNamedOperator returns true if this token represents a named operator.
func (t Token) IsNamedOperator() bool {
	return t.Type.IsNamedOperator()
}
