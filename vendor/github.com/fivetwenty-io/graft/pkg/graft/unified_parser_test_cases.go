package graft

// TestCase represents a parser test case.
type TestCase struct {
	Name        string
	Input       string
	Expected    interface{} // Expected parsed result or error
	Description string
	Category    string
}

// UnifiedParserTestCases contains all test cases for the unified parser.
var UnifiedParserTestCases = []TestCase{
	// Issue 1: Quote Handling
	{
		Category:    "Quote Handling",
		Name:        "grab with quoted string",
		Input:       `(( grab "default" ))`,
		Expected:    `grab operator with literal string "default"`,
		Description: "parseSimpleHash should not remove quotes from string values",
	},
	{
		Category:    "Quote Handling",
		Name:        "nested quotes",
		Input:       `(( concat "value with \"nested\" quotes" ))`,
		Expected:    `concat operator with string containing escaped quotes`,
		Description: "Handle escaped quotes within strings",
	},
	{
		Category:    "Quote Handling",
		Name:        "single quotes",
		Input:       `(( grab 'single quoted' ))`,
		Expected:    `grab operator with single quoted string`,
		Description: "Support single quoted strings",
	},

	// Issue 2: Reference Path Tokenization
	{
		Category:    "Reference Paths",
		Name:        "array indexing",
		Input:       `(( grab x[0].z ))`,
		Expected:    `grab operator with path x[0].z`,
		Description: "Tokenizer should treat x[0].z as a single reference path",
	},
	{
		Category:    "Reference Paths",
		Name:        "map key access",
		Input:       `(( grab map["key"].value ))`,
		Expected:    `grab operator with path map["key"].value`,
		Description: "Handle quoted keys in map access",
	},
	{
		Category:    "Reference Paths",
		Name:        "complex path",
		Input:       `(( grab array[0].map["key"].nested[1].value ))`,
		Expected:    `grab operator with complex nested path`,
		Description: "Handle complex nested paths with multiple indexing operations",
	},
	{
		Category:    "Reference Paths",
		Name:        "environment variable path",
		Input:       `(( grab $VAR.path[0] ))`,
		Expected:    `grab operator with environment variable and path`,
		Description: "Support paths starting with environment variables",
	},

	// Issue 3: Operator Precedence
	{
		Category:    "Precedence",
		Name:        "arithmetic precedence",
		Input:       `(( calc 2 + 3 * 4 ))`,
		Expected:    14, // not 20
		Description: "Multiplication should have higher precedence than addition",
	},
	{
		Category:    "Precedence",
		Name:        "comparison and logical",
		Input:       `(( a > 5 && b < 10 || c == 3 ))`,
		Expected:    `((a > 5) && (b < 10)) || (c == 3)`,
		Description: "Proper precedence: comparison > && > ||",
	},
	{
		Category:    "Precedence",
		Name:        "parentheses override",
		Input:       `(( calc (2 + 3) * 4 ))`,
		Expected:    20,
		Description: "Parentheses should override natural precedence",
	},
	{
		Category:    "Precedence",
		Name:        "ternary precedence",
		Input:       `(( a > 5 ? b + 1 : c * 2 ))`,
		Expected:    `ternary with condition (a > 5), true (b + 1), false (c * 2)`,
		Description: "Ternary should have lowest precedence",
	},

	// Issue 4: Complex Expressions with YAML
	{
		Category:    "Complex Expressions",
		Name:        "grab with YAML fallback",
		Input:       `(( grab user || { name: "default", role: "guest" } ))`,
		Expected:    `grab user with YAML literal fallback`,
		Description: "Parser should handle YAML literals in || expressions",
	},
	{
		Category:    "Complex Expressions",
		Name:        "YAML array literal",
		Input:       `(( grab list || ["item1", "item2", "item3"] ))`,
		Expected:    `grab list with YAML array fallback`,
		Description: "Support YAML array literals",
	},
	{
		Category:    "Complex Expressions",
		Name:        "nested YAML",
		Input:       `(( grab config || { db: { host: "localhost", port: 5432 } } ))`,
		Expected:    `grab config with nested YAML fallback`,
		Description: "Handle nested YAML structures",
	},

	// Issue 5: Parser Fallback Logic
	{
		Category:    "Parser Fallback",
		Name:        "infix expression",
		Input:       `(( base + addend ))`,
		Expected:    `addition operator with base and addend`,
		Description: "Should try infix parser when regex parser fails",
	},
	{
		Category:    "Parser Fallback",
		Name:        "complex infix",
		Input:       `(( base * multiplier + offset ))`,
		Expected:    `(base * multiplier) + offset with proper precedence`,
		Description: "Handle complex infix expressions",
	},

	// Edge Cases
	{
		Category:    "Edge Cases",
		Name:        "empty operator",
		Input:       `(( ))`,
		Expected:    `error: empty operator expression`,
		Description: "Handle empty operator expressions",
	},
	{
		Category:    "Edge Cases",
		Name:        "unclosed quotes",
		Input:       `(( grab "unclosed ))`,
		Expected:    `error: unclosed string literal`,
		Description: "Detect unclosed quotes",
	},
	{
		Category:    "Edge Cases",
		Name:        "invalid syntax",
		Input:       `(( grab || ))`,
		Expected:    `error: unexpected end of expression`,
		Description: "Handle invalid || syntax",
	},
	{
		Category:    "Edge Cases",
		Name:        "unicode in strings",
		Input:       `(( concat "Hello 世界" "🌍" ))`,
		Expected:    `concat with unicode strings`,
		Description: "Support unicode in string literals",
	},

	// Existing Patterns to Preserve
	{
		Category:    "Backwards Compatibility",
		Name:        "space-separated args",
		Input:       `(( concat foo bar baz ))`,
		Expected:    `concat with three reference arguments`,
		Description: "Preserve space-separated argument syntax",
	},
	{
		Category:    "Backwards Compatibility",
		Name:        "comma-separated args",
		Input:       `(( concat foo, bar, baz ))`,
		Expected:    `concat with three reference arguments`,
		Description: "Support comma-separated arguments",
	},
	{
		Category:    "Backwards Compatibility",
		Name:        "operator with target",
		Input:       `(( vault@production "secret/data" ))`,
		Expected:    `vault operator with target 'production'`,
		Description: "Support @target syntax",
	},
	{
		Category:    "Backwards Compatibility",
		Name:        "operator with modifier",
		Input:       `(( grab:nocache meta.data ))`,
		Expected:    `grab operator with :nocache modifier`,
		Description: "Support :modifier syntax",
	},
	{
		Category:    "Backwards Compatibility",
		Name:        "function style",
		Input:       `(( vault("secret/data:password") ))`,
		Expected:    `vault operator with parenthesized argument`,
		Description: "Support function-style operator calls",
	},

	// Combined Complex Cases
	{
		Category:    "Complex Combined",
		Name:        "nested operators with paths",
		Input:       `(( concat (grab users[0].name) " - " (grab users[0].role || "guest") ))`,
		Expected:    `concat with nested grab operators using array paths`,
		Description: "Combine multiple features",
	},
	{
		Category:    "Complex Combined",
		Name:        "arithmetic with references",
		Input:       `(( config.retries * 2 + config.base_delay ))`,
		Expected:    `arithmetic expression with config references`,
		Description: "Mix references and arithmetic",
	},
	{
		Category:    "Complex Combined",
		Name:        "ternary with complex expressions",
		Input:       `(( env.production ? grab prod.config : { debug: true, level: "info" } ))`,
		Expected:    `ternary with environment check and YAML fallback`,
		Description: "Complex ternary expression",
	},
}
