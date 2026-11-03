// Package interfaces defines core types for the graft parser and evaluator.
package interfaces

import (
	"fmt"
	"strings"
)

// Control flow keyword constant.
const (
	keywordDone = "done"
)

// OperatorLocation represents the location of a (( ... )) operator expression
// found during pre-scanning of source text.
type OperatorLocation struct {
	Start      Position // Start position of the opening ((
	End        Position // End position of the closing ))
	RawContent string   // The content between (( and )), excluding the delimiters
}

// String returns a human-readable representation of the operator location.
func (o OperatorLocation) String() string {
	return fmt.Sprintf("OperatorLocation{%s-%s, %q}", o.Start, o.End, o.RawContent)
}

// Range returns the source range covered by this operator location.
func (o OperatorLocation) Range() Range {
	return NewRange(o.Start, o.End)
}

// FullContent returns the complete operator expression including delimiters.
func (o OperatorLocation) FullContent() string {
	return "((" + o.RawContent + "))"
}

// PreScanner scans source text to extract all (( ... )) operator expressions
// before full parsing. It handles:
//   - Nested parentheses within expressions
//   - Quoted strings that may contain (( or )) literals
//   - Multiline expressions
//   - Escaped quotes within strings
//
// The prescanner does NOT validate the content of operator expressions;
// it only extracts their locations and raw content.
type PreScanner struct {
	source []byte // Source text being scanned
	file   string // Source file name (optional)

	// Position tracking
	offset     int // Current byte offset in source
	line       int // Current line number (1-based)
	column     int // Current column number (1-based)
	lineOffset int // Byte offset of current line start
}

// NewPreScanner creates a new PreScanner for the given source string.
func NewPreScanner(source string) *PreScanner {
	return &PreScanner{
		source: []byte(source),
		line:   1,
		column: 1,
	}
}

// NewPreScannerWithFile creates a new PreScanner with file information for error reporting.
func NewPreScannerWithFile(source, filename string) *PreScanner {
	ps := NewPreScanner(source)
	ps.file = filename
	return ps
}

// PreScan extracts all (( ... )) operator expressions from the source text.
// It returns a slice of OperatorLocation structs, each representing one
// operator expression found in the source.
//
// The function handles:
//   - Nested parentheses: (( foo.bar[0] )), (( concat("a", (grab b)) ))
//   - Quoted strings containing (( or )): (( "text with (( inside" ))
//   - Multiline expressions
//   - YAML strings that contain literal (( but aren't operators
//
// Returns an error if unbalanced operator delimiters are found.
func PreScan(source string) ([]OperatorLocation, error) {
	ps := NewPreScanner(source)
	return ps.Scan()
}

// PreScanWithFile extracts operator expressions with file information for error reporting.
func PreScanWithFile(source, filename string) ([]OperatorLocation, error) {
	ps := NewPreScannerWithFile(source, filename)
	return ps.Scan()
}

// Scan performs the pre-scan operation and returns all operator locations.
func (ps *PreScanner) Scan() ([]OperatorLocation, error) {
	var locations []OperatorLocation

	for ps.offset < len(ps.source) {
		// Look for opening ((
		if ps.current() == '(' && ps.peek() == '(' {
			loc, err := ps.scanOperatorExpression()
			if err != nil {
				return locations, err
			}
			locations = append(locations, loc)
		} else {
			// Check if we're in a YAML string context and should skip
			// For now, advance past this character
			if ps.current() == '\n' {
				ps.advanceNewline()
			} else {
				ps.advance()
			}
		}
	}

	return locations, nil
}

// scanOperatorExpression scans a complete (( ... )) expression.
// The scanner must be positioned at the first ( of the opening ((.
func (ps *PreScanner) scanOperatorExpression() (OperatorLocation, error) {
	startPos := ps.pos()

	// Consume opening ((
	ps.advance() // first (
	ps.advance() // second (

	contentStart := ps.offset
	parenDepth := 0 // Track nested parentheses (not counting the operator delimiters)

	for ps.offset < len(ps.source) {
		ch := ps.current()

		// Handle quoted strings - skip their contents entirely
		if ch == '"' {
			if err := ps.skipDoubleQuotedString(); err != nil {
				return OperatorLocation{}, err
			}
			continue
		}

		if ch == '\'' {
			if err := ps.skipSingleQuotedString(); err != nil {
				return OperatorLocation{}, err
			}
			continue
		}

		// Check for closing ))
		if ch == ')' && ps.peek() == ')' && parenDepth == 0 {
			contentEnd := ps.offset
			rawContent := string(ps.source[contentStart:contentEnd])

			// Consume closing ))
			ps.advance()
			ps.advance()

			endPos := ps.pos()

			return OperatorLocation{
				Start:      startPos,
				End:        endPos,
				RawContent: rawContent,
			}, nil
		}

		// Track nested parentheses
		switch ch {
		case '(':
			parenDepth++
		case ')':
			if parenDepth > 0 {
				parenDepth--
			}
			// If parenDepth is 0 and we have a single ), just advance
		}

		// Handle newlines for line tracking
		if ch == '\n' {
			ps.advanceNewline()
		} else {
			ps.advance()
		}
	}

	// Reached end of input without finding closing ))
	return OperatorLocation{}, ps.makeError(startPos, "unterminated operator expression: missing closing ))")
}

// skipDoubleQuotedString skips over a double-quoted string, handling escape sequences.
// The scanner must be positioned at the opening quote.
func (ps *PreScanner) skipDoubleQuotedString() error {
	startPos := ps.pos()
	ps.advance() // consume opening quote

	for ps.offset < len(ps.source) {
		ch := ps.current()

		if ch == '"' {
			ps.advance() // consume closing quote
			return nil
		}

		if ch == '\\' {
			// Skip escape sequence
			ps.advance() // consume backslash
			if ps.offset < len(ps.source) {
				if ps.current() == '\n' {
					ps.advanceNewline()
				} else {
					ps.advance() // consume escaped character
				}
			}
			continue
		}

		if ch == '\n' {
			// Newlines in double-quoted strings are typically not allowed
			// but we'll track them anyway for position accuracy
			ps.advanceNewline()
		} else {
			ps.advance()
		}
	}

	return ps.makeError(startPos, "unterminated string literal")
}

// skipSingleQuotedString skips over a single-quoted raw string.
// The scanner must be positioned at the opening quote.
// Single-quoted strings do not process escape sequences.
func (ps *PreScanner) skipSingleQuotedString() error {
	startPos := ps.pos()
	ps.advance() // consume opening quote

	for ps.offset < len(ps.source) {
		ch := ps.current()

		if ch == '\'' {
			ps.advance() // consume closing quote
			return nil
		}

		if ch == '\n' {
			// Raw strings can contain newlines
			ps.advanceNewline()
		} else {
			ps.advance()
		}
	}

	return ps.makeError(startPos, "unterminated raw string literal")
}

// current returns the current byte without advancing.
func (ps *PreScanner) current() byte {
	if ps.offset >= len(ps.source) {
		return 0
	}
	return ps.source[ps.offset]
}

// peek returns the next byte without advancing.
func (ps *PreScanner) peek() byte {
	if ps.offset+1 >= len(ps.source) {
		return 0
	}
	return ps.source[ps.offset+1]
}

// advance moves to the next byte and updates column position.
func (ps *PreScanner) advance() {
	if ps.offset < len(ps.source) {
		ps.offset++
		ps.column++
	}
}

// advanceNewline advances past a newline and updates line tracking.
func (ps *PreScanner) advanceNewline() {
	ps.offset++
	ps.line++
	ps.column = 1
	ps.lineOffset = ps.offset
}

// pos returns the current position.
func (ps *PreScanner) pos() Position {
	return Position{
		Line:   ps.line,
		Column: ps.column,
		Offset: ps.offset,
		File:   ps.file,
	}
}

// makeError creates a formatted error with position information.
func (ps *PreScanner) makeError(pos Position, message string) error {
	return &PreScanError{
		Message:  message,
		Position: pos,
	}
}

// PreScanError represents an error encountered during pre-scanning.
type PreScanError struct {
	Message  string
	Position Position
}

// Error implements the error interface.
func (e *PreScanError) Error() string {
	return fmt.Sprintf("prescan error at %s: %s", e.Position, e.Message)
}

// PreScannerInterface defines the interface for pre-scanning operator expressions.
type PreScannerInterface interface {
	// Scan performs pre-scanning and returns all operator locations
	Scan() ([]OperatorLocation, error)
}

// Ensure PreScanner implements PreScannerInterface.
var _ PreScannerInterface = (*PreScanner)(nil)

// ExtractOperatorContents extracts just the raw content strings from operator locations.
// This is a convenience function for testing and debugging.
func ExtractOperatorContents(locations []OperatorLocation) []string {
	contents := make([]string, len(locations))
	for i, loc := range locations {
		contents[i] = loc.RawContent
	}
	return contents
}

// TrimOperatorContent removes leading/trailing whitespace from operator content.
// This is useful for normalization before further processing.
func TrimOperatorContent(content string) string {
	return strings.TrimSpace(content)
}

// CountOperators returns the number of operator expressions in the source.
// This is a quick way to check if a source contains any operators.
func CountOperators(source string) (int, error) {
	locations, err := PreScan(source)
	if err != nil {
		return 0, err
	}
	return len(locations), nil
}

// HasOperators returns true if the source contains any (( ... )) expressions.
func HasOperators(source string) bool {
	count, err := CountOperators(source)
	return err == nil && count > 0
}

// ============================================================================
// Control Flow Block Detection
// ============================================================================

// ControlFlowType represents the type of a control flow block.
type ControlFlowType int

const (
	// ControlFlowIf represents an if/elif/else/fi block.
	ControlFlowIf ControlFlowType = iota

	// ControlFlowFor represents a for/done loop block.
	ControlFlowFor

	// ControlFlowWhile represents a while/done loop block.
	ControlFlowWhile

	// ControlFlowCase represents a case/when/default/esac block.
	ControlFlowCase
)

// String returns a human-readable representation of the control flow type.
func (c ControlFlowType) String() string {
	switch c {
	case ControlFlowIf:
		return "if"
	case ControlFlowFor:
		return "for"
	case ControlFlowWhile:
		return "while"
	case ControlFlowCase:
		return "case"
	default:
		return fmt.Sprintf("ControlFlowType(%d)", int(c))
	}
}

// ControlFlowBlock represents a matched control flow structure.
type ControlFlowBlock struct {
	// Type indicates what kind of control flow block this is.
	Type ControlFlowType

	// StartLocation points to the opening keyword (if, for, while, case).
	StartLocation *OperatorLocation

	// EndLocation points to the closing keyword (fi, done, esac).
	EndLocation *OperatorLocation

	// NestedBlocks contains any control flow blocks nested inside this one.
	NestedBlocks []*ControlFlowBlock

	// ElseIfLocations contains locations of elif clauses (for if blocks).
	ElseIfLocations []*OperatorLocation

	// ElseLocation points to the else clause if present (for if blocks).
	ElseLocation *OperatorLocation

	// WhenLocations contains locations of when clauses (for case blocks).
	WhenLocations []*OperatorLocation

	// DefaultLocation points to the default clause if present (for case blocks).
	DefaultLocation *OperatorLocation
}

// String returns a human-readable representation of the control flow block.
func (b *ControlFlowBlock) String() string {
	if b == nil {
		return "ControlFlowBlock{nil}"
	}
	return fmt.Sprintf("ControlFlowBlock{Type: %s, Start: %s, End: %s, Nested: %d}",
		b.Type, b.StartLocation, b.EndLocation, len(b.NestedBlocks))
}

// BlockRange returns the source range covered by this block.
func (b *ControlFlowBlock) BlockRange() Range {
	if b == nil || b.StartLocation == nil || b.EndLocation == nil {
		return NoRange()
	}
	return NewRange(b.StartLocation.Start, b.EndLocation.End)
}

// IsComplete returns true if the block has both start and end locations.
func (b *ControlFlowBlock) IsComplete() bool {
	return b != nil && b.StartLocation != nil && b.EndLocation != nil
}

// controlFlowStarts maps starting keywords to their control flow type.
var controlFlowStarts = map[string]ControlFlowType{
	"if":    ControlFlowIf,
	"for":   ControlFlowFor,
	"while": ControlFlowWhile,
	"case":  ControlFlowCase,
}

// controlFlowEnds maps ending keywords to their control flow type.
// Note: "done" can end both "for" and "while" blocks.
var controlFlowEnds = map[string]ControlFlowType{
	"fi":   ControlFlowIf,
	"esac": ControlFlowCase,
	// "done" handled specially since it can end both for and while
}

// controlFlowEndAliases maps ending keyword aliases to their canonical form.
var controlFlowEndAliases = map[string]string{
	"endif":    "fi",
	"endfor":   keywordDone,
	"endwhile": keywordDone,
	"endcase":  "esac",
}

// controlFlowMiddle maps middle keywords to their description.
var controlFlowMiddle = map[string]string{
	"elif":    "elif",
	"elsif":   "elif", // alias
	"else":    "else",
	"when":    "when",
	"default": "default",
}

// IsControlFlowStart checks if the content represents a control flow start keyword.
// Returns the control flow type and true if it is a start keyword, otherwise returns
// the zero value and false.
func IsControlFlowStart(content string) (ControlFlowType, bool) {
	// Extract the keyword from the content (may contain expressions)
	keyword := extractControlFlowKeyword(content)
	keyword = strings.ToLower(keyword)

	if cfType, ok := controlFlowStarts[keyword]; ok {
		return cfType, true
	}
	return 0, false
}

// IsControlFlowEnd checks if the content represents a control flow end keyword.
// Returns the control flow type and true if it is an end keyword, otherwise returns
// the zero value and false.
func IsControlFlowEnd(content string) (ControlFlowType, bool) {
	keyword := extractControlFlowKeyword(content)
	keyword = strings.ToLower(keyword)

	// Check for aliases first
	if canonical, ok := controlFlowEndAliases[keyword]; ok {
		keyword = canonical
	}

	// Check for "done" which can end both for and while
	if keyword == keywordDone {
		// Return ControlFlowFor as default, but caller should check context
		return ControlFlowFor, true
	}

	if cfType, ok := controlFlowEnds[keyword]; ok {
		return cfType, true
	}
	return 0, false
}

// IsControlFlowMiddle checks if the content represents a control flow middle keyword
// (elif, else, when, default).
// Returns the normalized keyword name and true if it is a middle keyword.
func IsControlFlowMiddle(content string) (string, bool) {
	keyword := extractControlFlowKeyword(content)
	keyword = strings.ToLower(keyword)

	if normalized, ok := controlFlowMiddle[keyword]; ok {
		return normalized, true
	}
	return "", false
}

// extractControlFlowKeyword extracts the first keyword from content that may contain expressions.
// For example, "if x > 0" returns "if", "for i in range(10)" returns "for".
func extractControlFlowKeyword(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}

	// Find the first word (delimited by space, parenthesis, or other operators)
	for i, r := range content {
		if !isControlFlowKeywordChar(r) {
			return content[:i]
		}
	}
	return content
}

// isControlFlowKeywordChar returns true if the rune can be part of a keyword.
func isControlFlowKeywordChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}

// ControlFlowError represents an error in control flow block matching.
type ControlFlowError struct {
	Message  string
	Location *OperatorLocation
}

func (e *ControlFlowError) Error() string {
	if e.Location != nil {
		return fmt.Sprintf("%s at line %d, column %d", e.Message, e.Location.Start.Line, e.Location.Start.Column)
	}
	return e.Message
}

// blockStackEntry is used internally during block detection to track
// blocks and their indices.
type blockStackEntry struct {
	block *ControlFlowBlock
	index int
}

// DetectControlFlowBlocks analyzes a list of operator locations and detects
// matching control flow blocks. It returns all top-level blocks found and any
// errors for mismatched or unclosed blocks.
//
//nolint:gocyclo // control flow block detection requires complex state machine logic
func DetectControlFlowBlocks(locations []OperatorLocation) ([]*ControlFlowBlock, error) {
	if len(locations) == 0 {
		return nil, nil
	}

	var blocks []*ControlFlowBlock
	var stack []blockStackEntry

	for i := range locations {
		loc := &locations[i]

		// Check if this is a start keyword
		if cfType, ok := IsControlFlowStart(loc.RawContent); ok {
			block := &ControlFlowBlock{
				Type:          cfType,
				StartLocation: loc,
			}
			stack = append(stack, blockStackEntry{block: block, index: i})
			continue
		}

		// Check if this is a middle keyword
		if middle, ok := IsControlFlowMiddle(loc.RawContent); ok {
			if len(stack) == 0 {
				return nil, &ControlFlowError{
					Message:  fmt.Sprintf("unexpected %q without matching block start", middle),
					Location: loc,
				}
			}

			current := stack[len(stack)-1].block

			switch middle {
			case "elif":
				if current.Type != ControlFlowIf {
					return nil, &ControlFlowError{
						Message:  fmt.Sprintf("elif found inside %s block, expected if block", current.Type),
						Location: loc,
					}
				}
				current.ElseIfLocations = append(current.ElseIfLocations, loc)

			case "else":
				if current.Type != ControlFlowIf {
					return nil, &ControlFlowError{
						Message:  fmt.Sprintf("else found inside %s block, expected if block", current.Type),
						Location: loc,
					}
				}
				if current.ElseLocation != nil {
					return nil, &ControlFlowError{
						Message:  "duplicate else clause",
						Location: loc,
					}
				}
				current.ElseLocation = loc

			case "when":
				if current.Type != ControlFlowCase {
					return nil, &ControlFlowError{
						Message:  fmt.Sprintf("when found inside %s block, expected case block", current.Type),
						Location: loc,
					}
				}
				current.WhenLocations = append(current.WhenLocations, loc)

			case "default":
				if current.Type != ControlFlowCase {
					return nil, &ControlFlowError{
						Message:  fmt.Sprintf("default found inside %s block, expected case block", current.Type),
						Location: loc,
					}
				}
				if current.DefaultLocation != nil {
					return nil, &ControlFlowError{
						Message:  "duplicate default clause",
						Location: loc,
					}
				}
				current.DefaultLocation = loc
			}
			continue
		}

		// Check if this is an end keyword
		if _, ok := IsControlFlowEnd(loc.RawContent); ok {
			if len(stack) == 0 {
				return nil, &ControlFlowError{
					Message:  fmt.Sprintf("unexpected %q without matching block start", extractControlFlowKeyword(loc.RawContent)),
					Location: loc,
				}
			}

			current := stack[len(stack)-1].block

			// Validate the end keyword matches the block type
			keyword := strings.ToLower(extractControlFlowKeyword(loc.RawContent))

			// Normalize aliases
			if canonical, ok := controlFlowEndAliases[keyword]; ok {
				keyword = canonical
			}

			// Check for matching
			matched := false
			switch keyword {
			case "fi":
				matched = current.Type == ControlFlowIf
			case "done":
				matched = current.Type == ControlFlowFor || current.Type == ControlFlowWhile
			case "esac":
				matched = current.Type == ControlFlowCase
			}

			if !matched {
				expectedEnd := getExpectedEnd(current.Type)
				return nil, &ControlFlowError{
					Message: fmt.Sprintf("mismatched block: found %q but expected %q to close %s block",
						keyword, expectedEnd, current.Type),
					Location: loc,
				}
			}

			// Pop from stack and complete the block
			current.EndLocation = loc
			stack = stack[:len(stack)-1]

			// If there's a parent block, add as nested; otherwise add to top-level
			if len(stack) > 0 {
				parent := stack[len(stack)-1].block
				parent.NestedBlocks = append(parent.NestedBlocks, current)
			} else {
				blocks = append(blocks, current)
			}
			continue
		}
	}

	// Check for unclosed blocks
	if len(stack) > 0 {
		unclosed := stack[len(stack)-1].block
		expectedEnd := getExpectedEnd(unclosed.Type)
		return nil, &ControlFlowError{
			Message:  fmt.Sprintf("unclosed %s block: missing %q", unclosed.Type, expectedEnd),
			Location: unclosed.StartLocation,
		}
	}

	return blocks, nil
}

// getExpectedEnd returns the expected end keyword for a control flow type.
func getExpectedEnd(cfType ControlFlowType) string {
	switch cfType {
	case ControlFlowIf:
		return "fi"
	case ControlFlowFor:
		return keywordDone
	case ControlFlowWhile:
		return keywordDone
	case ControlFlowCase:
		return "esac"
	default:
		return "unknown"
	}
}

// ValidateControlFlowBlocks validates a list of control flow blocks for semantic correctness.
// This includes checking for proper nesting and clause ordering.
func ValidateControlFlowBlocks(blocks []*ControlFlowBlock) error {
	for _, block := range blocks {
		if err := validateBlock(block); err != nil {
			return err
		}
	}
	return nil
}

// validateBlock validates a single control flow block.
func validateBlock(block *ControlFlowBlock) error {
	if block == nil {
		return nil
	}

	// Validate nested blocks
	for _, nested := range block.NestedBlocks {
		if err := validateBlock(nested); err != nil {
			return err
		}
	}

	// Type-specific validation
	switch block.Type {
	case ControlFlowIf:
		// If block can have zero or more elif clauses, zero or one else clause
		// Else must come after all elif clauses
		if block.ElseLocation != nil && len(block.ElseIfLocations) > 0 {
			lastElif := block.ElseIfLocations[len(block.ElseIfLocations)-1]
			if block.ElseLocation.Start.Offset < lastElif.Start.Offset {
				return &ControlFlowError{
					Message:  "else clause must come after all elif clauses",
					Location: block.ElseLocation,
				}
			}
		}

	case ControlFlowCase:
		// Case block should have at least one when clause (or default)
		// Default should come last
		if block.DefaultLocation != nil && len(block.WhenLocations) > 0 {
			lastWhen := block.WhenLocations[len(block.WhenLocations)-1]
			if block.DefaultLocation.Start.Offset < lastWhen.Start.Offset {
				return &ControlFlowError{
					Message:  "default clause should come after all when clauses",
					Location: block.DefaultLocation,
				}
			}
		}
	case ControlFlowFor, ControlFlowWhile:
		// No additional validation for loop blocks
	}

	return nil
}

// FindBlockContaining finds the innermost control flow block containing the given location.
// Returns nil if no block contains the location.
func FindBlockContaining(blocks []*ControlFlowBlock, line, column int) *ControlFlowBlock {
	for _, block := range blocks {
		if found := findBlockContainingRecursive(block, line, column); found != nil {
			return found
		}
	}
	return nil
}

// findBlockContainingRecursive recursively searches for the innermost block containing a position.
func findBlockContainingRecursive(block *ControlFlowBlock, line, column int) *ControlFlowBlock {
	if block == nil || !block.IsComplete() {
		return nil
	}

	// Check if position is within this block's range
	start := block.StartLocation.Start
	end := block.EndLocation.End

	// Position is before block start
	if line < start.Line || (line == start.Line && column < start.Column) {
		return nil
	}

	// Position is after block end
	if line > end.Line || (line == end.Line && column > end.Column) {
		return nil
	}

	// Position is within this block - check nested blocks first
	for _, nested := range block.NestedBlocks {
		if found := findBlockContainingRecursive(nested, line, column); found != nil {
			return found
		}
	}

	// Return this block if no nested block contains the position
	return block
}

// GetBlockDepth returns the nesting depth of a block (0 for top-level blocks).
func GetBlockDepth(blocks []*ControlFlowBlock, target *ControlFlowBlock) int {
	for _, block := range blocks {
		if depth := getBlockDepthRecursive(block, target, 0); depth >= 0 {
			return depth
		}
	}
	return -1 // Not found
}

// getBlockDepthRecursive recursively finds the depth of a target block.
func getBlockDepthRecursive(block, target *ControlFlowBlock, depth int) int {
	if block == target {
		return depth
	}
	for _, nested := range block.NestedBlocks {
		if d := getBlockDepthRecursive(nested, target, depth+1); d >= 0 {
			return d
		}
	}
	return -1
}

// CountNestedBlocks returns the total number of nested blocks within a block (including all levels).
func CountNestedBlocks(block *ControlFlowBlock) int {
	if block == nil {
		return 0
	}
	count := len(block.NestedBlocks)
	for _, nested := range block.NestedBlocks {
		count += CountNestedBlocks(nested)
	}
	return count
}

// GetAllBlocks returns all blocks including nested ones as a flat list.
func GetAllBlocks(blocks []*ControlFlowBlock) []*ControlFlowBlock {
	result := make([]*ControlFlowBlock, 0, len(blocks))
	for _, block := range blocks {
		result = append(result, block)
		result = append(result, GetAllBlocks(block.NestedBlocks)...)
	}
	return result
}
