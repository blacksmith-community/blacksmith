// Package interfaces defines core types for the graft parser and evaluator.
package interfaces

import (
	"fmt"
	"strings"
)

// Position represents a location in source code.
// All positions are 1-based for lines and columns to match editor conventions.
// Offset is 0-based to match byte indexing.
type Position struct {
	Line   int    // 1-based line number
	Column int    // 1-based column number
	Offset int    // 0-based byte offset
	File   string // Source file name (optional)
}

// String returns a human-readable representation of the position.
// Format: "file:line:column" or "line:column" if no file is set.
func (p Position) String() string {
	if p.File != "" {
		return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Column)
	}
	return fmt.Sprintf("%d:%d", p.Line, p.Column)
}

// IsZero returns true if the position is uninitialized (all zero values).
func (p Position) IsZero() bool {
	return p.Line == 0 && p.Column == 0 && p.Offset == 0 && p.File == ""
}

// Before returns true if this position comes before the other position.
// Positions are compared by offset if in the same file, otherwise by line and column.
func (p Position) Before(other Position) bool {
	return p.Compare(other) < 0
}

// After returns true if this position comes after the other position.
// Positions are compared by offset if in the same file, otherwise by line and column.
func (p Position) After(other Position) bool {
	return p.Compare(other) > 0
}

// Compare returns -1 if p < other, 0 if p == other, 1 if p > other.
// Comparison is by file (lexicographically), then by offset.
// If offsets are both zero, falls back to line then column comparison.
func (p Position) Compare(other Position) int {
	// Compare files first
	if p.File != other.File {
		if p.File < other.File {
			return -1
		}
		return 1
	}

	// Compare by offset if available
	if p.Offset != 0 || other.Offset != 0 {
		if p.Offset < other.Offset {
			return -1
		}
		if p.Offset > other.Offset {
			return 1
		}
		return 0
	}

	// Fall back to line/column comparison
	if p.Line != other.Line {
		if p.Line < other.Line {
			return -1
		}
		return 1
	}

	if p.Column != other.Column {
		if p.Column < other.Column {
			return -1
		}
		return 1
	}

	return 0
}

// NoPosition returns a zero position, indicating no position information.
func NoPosition() Position {
	return Position{}
}

// NewPosition creates a position with the specified line, column, and offset.
// Line and column should be 1-based; offset should be 0-based.
func NewPosition(line, column, offset int) Position {
	return Position{
		Line:   line,
		Column: column,
		Offset: offset,
	}
}

// NewPositionWithFile creates a position with file information.
func NewPositionWithFile(line, column, offset int, file string) Position {
	return Position{
		Line:   line,
		Column: column,
		Offset: offset,
		File:   file,
	}
}

// Range represents a span of source code from Start to End.
// Both Start and End are inclusive.
type Range struct {
	Start Position
	End   Position
}

// String returns a human-readable representation of the range.
// Format: "start-end" where start and end are position strings.
func (r Range) String() string {
	if r.Start.File == r.End.File && r.Start.File != "" {
		// Same file, show compact format
		return fmt.Sprintf("%s:%d:%d-%d:%d",
			r.Start.File,
			r.Start.Line, r.Start.Column,
			r.End.Line, r.End.Column)
	}
	return fmt.Sprintf("%s-%s", r.Start.String(), r.End.String())
}

// Contains returns true if the given position is within this range (inclusive).
func (r Range) Contains(pos Position) bool {
	// Position must be >= Start and <= End
	return !pos.Before(r.Start) && !pos.After(r.End)
}

// Overlaps returns true if this range overlaps with another range.
// Two ranges overlap if they share at least one position.
func (r Range) Overlaps(other Range) bool {
	// Ranges overlap if neither ends before the other starts
	return !r.End.Before(other.Start) && !other.End.Before(r.Start)
}

// Length returns the byte length of the range (End.Offset - Start.Offset).
// Returns 0 if offsets are not set.
func (r Range) Length() int {
	if r.Start.Offset == 0 && r.End.Offset == 0 {
		return 0
	}
	length := r.End.Offset - r.Start.Offset
	if length < 0 {
		return 0
	}
	return length
}

// IsZero returns true if the range is uninitialized.
func (r Range) IsZero() bool {
	return r.Start.IsZero() && r.End.IsZero()
}

// Merge combines two ranges into a single range that spans both.
// The resulting range starts at the earlier start position and
// ends at the later end position.
func (r Range) Merge(other Range) Range {
	var start, end Position

	if r.Start.Before(other.Start) {
		start = r.Start
	} else {
		start = other.Start
	}

	if r.End.After(other.End) {
		end = r.End
	} else {
		end = other.End
	}

	return Range{Start: start, End: end}
}

// NoRange returns a zero range, indicating no range information.
func NoRange() Range {
	return Range{}
}

// NewRange creates a range from start to end positions.
func NewRange(start, end Position) Range {
	return Range{Start: start, End: end}
}

// PositionMapper tracks positions for error reporting.
// It maintains a mapping from byte offsets to line/column positions
// for efficient position lookups.
type PositionMapper struct {
	source      string
	sourceFile  string
	lineOffsets []int // Byte offset of each line start (0-indexed by line number - 1)
}

// NewPositionMapper creates a mapper for the given source.
// The filename is optional and used for error reporting.
func NewPositionMapper(source, filename string) *PositionMapper {
	pm := &PositionMapper{
		source:     source,
		sourceFile: filename,
	}
	pm.buildLineOffsets()
	return pm
}

// buildLineOffsets computes the byte offset of each line start.
func (pm *PositionMapper) buildLineOffsets() {
	pm.lineOffsets = []int{0} // Line 1 starts at offset 0

	for i := 0; i < len(pm.source); i++ {
		if pm.source[i] == '\n' {
			pm.lineOffsets = append(pm.lineOffsets, i+1)
		}
	}
}

// PositionAt returns the position corresponding to the given byte offset.
// Returns a zero position if the offset is out of bounds.
func (pm *PositionMapper) PositionAt(offset int) Position {
	if offset < 0 || offset > len(pm.source) {
		return NoPosition()
	}

	// Binary search for the line containing this offset
	line := pm.findLine(offset)
	column := offset - pm.lineOffsets[line-1] + 1 // 1-based column

	return Position{
		Line:   line,
		Column: column,
		Offset: offset,
		File:   pm.sourceFile,
	}
}

// findLine returns the 1-based line number containing the given offset.
func (pm *PositionMapper) findLine(offset int) int {
	// Binary search for the line
	low, high := 0, len(pm.lineOffsets)-1

	for low <= high {
		mid := (low + high) / 2
		if pm.lineOffsets[mid] <= offset {
			if mid == len(pm.lineOffsets)-1 || pm.lineOffsets[mid+1] > offset {
				return mid + 1 // Convert to 1-based
			}
			low = mid + 1
		} else {
			high = mid - 1
		}
	}

	return 1 // Default to line 1
}

// OffsetAt returns the byte offset for the given line and column (1-based).
// Returns -1 if the line or column is out of bounds.
func (pm *PositionMapper) OffsetAt(line, column int) int {
	if line < 1 || line > len(pm.lineOffsets) {
		return -1
	}

	lineStart := pm.lineOffsets[line-1]
	offset := lineStart + column - 1

	// Validate the offset is within bounds
	if offset < 0 || offset > len(pm.source) {
		return -1
	}

	return offset
}

// LineText returns the text of the specified line (1-based).
// Returns an empty string if the line is out of bounds.
func (pm *PositionMapper) LineText(line int) string {
	if line < 1 || line > len(pm.lineOffsets) {
		return ""
	}

	lineStart := pm.lineOffsets[line-1]
	var lineEnd int

	if line < len(pm.lineOffsets) {
		lineEnd = pm.lineOffsets[line] - 1 // Exclude the newline
	} else {
		lineEnd = len(pm.source)
	}

	if lineEnd < lineStart {
		return ""
	}

	return pm.source[lineStart:lineEnd]
}

// RangeText returns the text covered by the given range.
// Returns an empty string if the range is invalid.
func (pm *PositionMapper) RangeText(r Range) string {
	start := r.Start.Offset
	end := r.End.Offset

	if start < 0 || end < 0 || start > len(pm.source) || end > len(pm.source) {
		return ""
	}

	if end < start {
		return ""
	}

	return pm.source[start:end]
}

// FormatError formats an error message with source context.
// It shows the relevant source line with a caret pointing to the error location.
// The length parameter specifies how many characters to underline.
// The hint parameter provides additional guidance (optional).
func (pm *PositionMapper) FormatError(pos Position, length int, message, hint string) string {
	var result strings.Builder

	// Error location header
	if pos.File != "" {
		_, _ = fmt.Fprintf(&result, "Error at %s:%d:%d:\n", pos.File, pos.Line, pos.Column)
	} else {
		_, _ = fmt.Fprintf(&result, "Error at line %d, column %d:\n", pos.Line, pos.Column)
	}

	// Show the source line
	lineText := pm.LineText(pos.Line)
	if lineText != "" {
		_, _ = fmt.Fprintf(&result, "  %s\n", lineText)

		// Add caret/underline
		if pos.Column >= 1 {
			caretPos := pos.Column + 1 // Account for the 2-space indent
			result.WriteString(strings.Repeat(" ", caretPos))

			underlineLen := length
			if underlineLen < 1 {
				underlineLen = 1
			}
			result.WriteString(strings.Repeat("^", underlineLen))
			result.WriteString("\n")
		}
	}

	// Error message
	result.WriteString(message)

	// Optional hint
	if hint != "" {
		_, _ = fmt.Fprintf(&result, "\n\nHint: %s", hint)
	}

	return result.String()
}

// Source returns the original source text.
func (pm *PositionMapper) Source() string {
	return pm.source
}

// SourceFile returns the source file name.
func (pm *PositionMapper) SourceFile() string {
	return pm.sourceFile
}

// LineCount returns the total number of lines in the source.
func (pm *PositionMapper) LineCount() int {
	return len(pm.lineOffsets)
}
