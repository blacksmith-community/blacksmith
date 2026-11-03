// Package interfaces provides extended keyword recognition for the graft parser.
//
// This file extends the keyword lookup system with additional categorization,
// alias handling, and utility functions for keyword processing during tokenization.
package interfaces

import (
	"strings"
	"unicode"
)

// Boolean and null literal constants.
const (
	literalTrue  = "true"
	literalFalse = "false"
	literalNull  = "null"
	literalNil   = "nil"
)

// KeywordCategory categorizes keywords by their purpose.
type KeywordCategory int

const (
	// CategoryControlFlow keywords control program flow: if, else, for, etc.
	CategoryControlFlow KeywordCategory = iota

	// CategoryLogical keywords are logical operators: and, or, not.
	CategoryLogical

	// CategoryLiteral keywords represent literal values: null, nil, true, false.
	CategoryLiteral

	// CategoryOperator keywords are operator names: grab, vault, etc.
	CategoryOperator

	// CategoryBuiltin keywords are built-in functions: range.
	CategoryBuiltin

	// CategoryUnknown for non-keyword tokens.
	CategoryUnknown KeywordCategory = -1
)

// String returns the string representation of a keyword category.
func (c KeywordCategory) String() string {
	switch c {
	case CategoryControlFlow:
		return "ControlFlow"
	case CategoryLogical:
		return "Logical"
	case CategoryLiteral:
		return "Literal"
	case CategoryOperator:
		return "Operator"
	case CategoryBuiltin:
		return "Builtin"
	case CategoryUnknown:
		return "Unknown"
	}
	return "Unknown"
}

// keywordCategories maps token types to their categories.
var keywordCategories = map[TokenType]KeywordCategory{
	// Control flow
	TokenIf:      CategoryControlFlow,
	TokenElif:    CategoryControlFlow,
	TokenElse:    CategoryControlFlow,
	TokenFi:      CategoryControlFlow,
	TokenFor:     CategoryControlFlow,
	TokenWhile:   CategoryControlFlow,
	TokenDone:    CategoryControlFlow,
	TokenCase:    CategoryControlFlow,
	TokenWhen:    CategoryControlFlow,
	TokenDefault: CategoryControlFlow,
	TokenEsac:    CategoryControlFlow,
	TokenIn:      CategoryControlFlow,

	// Built-in
	TokenRange: CategoryBuiltin,
}

// logicalKeywords are words recognized as logical operators in expressions.
var logicalKeywords = map[string]bool{
	"and": true,
	"or":  true,
	"not": true,
}

// literalKeywords are words that represent literal values.
var literalKeywords = map[string]bool{
	"true":  true,
	"false": true,
	"null":  true,
	"nil":   true,
}

// controlFlowAliases maps alternative keyword spellings to their canonical form.
var controlFlowAliases = map[string]string{
	"elsif":    "elif",
	"endif":    "fi",
	"endfor":   "done",
	"endwhile": "done",
	"endcase":  "esac",
}

// GetKeywordCategory returns the category of a keyword token type.
// Returns CategoryUnknown if the token type is not a keyword.
func GetKeywordCategory(t TokenType) KeywordCategory {
	if cat, ok := keywordCategories[t]; ok {
		return cat
	}
	if t == TokenBoolean {
		return CategoryLiteral
	}
	if t == TokenNull {
		return CategoryLiteral
	}
	if t == TokenOperatorName {
		return CategoryOperator
	}
	return CategoryUnknown
}

// IsControlFlowKeyword returns true if the token type is a control flow keyword.
func IsControlFlowKeyword(t TokenType) bool {
	return GetKeywordCategory(t) == CategoryControlFlow
}

// IsLogicalKeywordStr returns true if the string is a logical keyword (and, or, not).
func IsLogicalKeywordStr(s string) bool {
	return logicalKeywords[strings.ToLower(s)]
}

// IsLiteralKeyword returns true if the string is a literal keyword (true, false, null, nil).
func IsLiteralKeyword(s string) bool {
	return literalKeywords[strings.ToLower(s)]
}

// IsOperatorKeyword returns true if the token type represents an operator name.
func IsOperatorKeyword(t TokenType) bool {
	return t == TokenOperatorName
}

// IsBoolLiteral returns true if the string represents a boolean literal.
// Recognized values: "true", "false" (case-insensitive).
func IsBoolLiteral(s string) bool {
	lower := strings.ToLower(s)
	return lower == literalTrue || lower == literalFalse
}

// IsNullLiteral returns true if the string represents a null literal.
// Recognized values: "null", "nil", "~" (case-insensitive for null/nil).
func IsNullLiteral(s string) bool {
	if s == "~" {
		return true
	}
	lower := strings.ToLower(s)
	return lower == literalNull || lower == literalNil
}

// ParseBoolLiteral parses a boolean literal string and returns its value.
// Returns the boolean value and true if successfully parsed, or false and false if not a boolean.
func ParseBoolLiteral(s string) (value, ok bool) {
	lower := strings.ToLower(s)
	switch lower {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

// IsKeywordIdentifier returns true if the string is any keyword (control flow, logical, literal, or operator).
// This checks against all registered keywords in the Keywords map plus logical keywords.
func IsKeywordIdentifier(s string) bool {
	lower := strings.ToLower(s)
	if _, ok := Keywords[lower]; ok {
		return true
	}
	if OperatorNames[lower] {
		return true
	}
	if logicalKeywords[lower] {
		return true
	}
	return false
}

// NormalizeKeywordAlias normalizes keyword aliases to their canonical form.
// For example, "elsif" -> "elif", "endif" -> "fi", etc.
// Returns the input lowercased if no alias mapping exists.
func NormalizeKeywordAlias(s string) string {
	lower := strings.ToLower(s)
	if canonical, ok := controlFlowAliases[lower]; ok {
		return canonical
	}
	return lower
}

// LookupKeywordWithAlias looks up a keyword, first normalizing any aliases.
// This allows accepting both "elsif" and "elif" as the same keyword.
func LookupKeywordWithAlias(s string) TokenType {
	normalized := NormalizeKeywordAlias(s)
	return LookupKeyword(normalized)
}

// ControlFlowKeywords returns a list of all control flow keywords.
func ControlFlowKeywords() []string {
	return []string{
		"if", "elif", "else", "fi",
		"for", "in", "done",
		"while",
		"case", "when", "default", "esac",
	}
}

// ControlFlowAliases returns a list of all control flow keyword aliases.
func ControlFlowAliases() []string {
	return []string{
		"elsif", "endif", "endfor", "endwhile", "endcase",
	}
}

// LogicalKeywordsList returns a list of all logical keywords.
func LogicalKeywordsList() []string {
	return []string{"and", "or", "not"}
}

// LiteralKeywordsList returns a list of all literal keywords.
func LiteralKeywordsList() []string {
	return []string{"true", "false", "null", "nil"}
}

// AllOperatorNames returns a list of all operator name keywords.
func AllOperatorNames() []string {
	names := make([]string, 0, len(OperatorNames))
	for name := range OperatorNames {
		names = append(names, name)
	}
	return names
}

// IsValidPathStart returns true if the string appears to start a path reference.
// Path references include:
//
//   - Identifiers followed by dot or bracket: "meta.name", "foo[0]"
//   - Dot-prefixed paths: ".meta.name", ".[0]"
//   - Root references: "."
func IsValidPathStart(s string) bool {
	if s == "" {
		return false
	}

	// Root reference or dot-prefixed path
	if s[0] == '.' {
		return true
	}

	// Check if it starts with a valid identifier character
	r := rune(s[0])
	if !isPathIdentStart(r) {
		return false
	}

	// Look for path continuation indicators
	for i, r := range s {
		if i == 0 {
			continue
		}
		// Path continuation: dot or bracket
		if r == '.' || r == '[' {
			return true
		}
		// Still in identifier part
		if !isPathIdentChar(r) {
			break
		}
	}

	// A single identifier is also a valid path start
	return isValidPathIdentifier(s)
}

// IsValidPathSegment returns true if the string is a valid path segment.
// A path segment is either:
//
//   - A valid identifier: foo, bar_baz, _private
//   - A numeric index: 0, 1, 42
//   - A quoted string: "key", 'key'
func IsValidPathSegment(s string) bool {
	if s == "" {
		return false
	}

	// Check for numeric index
	if isNumericPathIndex(s) {
		return true
	}

	// Check for quoted string
	if (s[0] == '"' || s[0] == '\'') && len(s) >= 2 && s[len(s)-1] == s[0] {
		return true
	}

	// Check for valid identifier
	return isValidPathIdentifier(s)
}

// isPathIdentStart returns true if the rune can start a path identifier.
func isPathIdentStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_'
}

// isPathIdentChar returns true if the rune can be part of a path identifier.
func isPathIdentChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-'
}

// isValidPathIdentifier returns true if the string is a valid path identifier.
func isValidPathIdentifier(s string) bool {
	if s == "" {
		return false
	}

	for i, r := range s {
		if i == 0 {
			if !isPathIdentStart(r) {
				return false
			}
		} else {
			if !isPathIdentChar(r) {
				return false
			}
		}
	}
	return true
}

// isNumericPathIndex returns true if the string is a valid numeric index.
func isNumericPathIndex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// KeywordInfo provides metadata about a keyword.
type KeywordInfo struct {
	Name      string          // The keyword string
	TokenType TokenType       // The corresponding token type
	Category  KeywordCategory // The keyword category
	Aliases   []string        // Alternative spellings (e.g., "elsif" for "elif")
}

// GetKeywordInfo returns information about a keyword.
// Returns nil if the string is not a keyword.
func GetKeywordInfo(s string) *KeywordInfo {
	lower := strings.ToLower(s)

	// Check for aliases first
	if canonical, ok := controlFlowAliases[lower]; ok {
		lower = canonical
	}

	// Check Keywords map
	if tokType, ok := Keywords[lower]; ok {
		info := &KeywordInfo{
			Name:      lower,
			TokenType: tokType,
			Category:  GetKeywordCategory(tokType),
		}

		// Find aliases for this keyword
		for alias, canonical := range controlFlowAliases {
			if canonical == lower {
				info.Aliases = append(info.Aliases, alias)
			}
		}

		return info
	}

	// Check OperatorNames
	if OperatorNames[lower] {
		return &KeywordInfo{
			Name:      lower,
			TokenType: TokenOperatorName,
			Category:  CategoryOperator,
		}
	}

	// Check logical keywords
	if logicalKeywords[lower] {
		return &KeywordInfo{
			Name:      lower,
			TokenType: TokenIdentifier, // logical keywords are handled specially
			Category:  CategoryLogical,
		}
	}

	return nil
}

// RegisterOperatorName adds a new operator name to the OperatorNames map.
// This allows custom operators to be registered dynamically.
func RegisterOperatorName(name string) {
	OperatorNames[strings.ToLower(name)] = true
}

// UnregisterOperatorName removes an operator name from the OperatorNames map.
func UnregisterOperatorName(name string) {
	delete(OperatorNames, strings.ToLower(name))
}

// IsRegisteredOperator returns true if the name is a registered operator.
func IsRegisteredOperator(name string) bool {
	return OperatorNames[strings.ToLower(name)]
}
