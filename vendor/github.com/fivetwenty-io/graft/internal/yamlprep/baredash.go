package yamlprep

import (
	"bytes"
	"regexp"
	"strings"
)

// bareDashLineRe matches a block-sequence item line consisting only of a
// dash (i.e. no value token at all), optionally followed by a comment.
var bareDashLineRe = regexp.MustCompile(`^-[ \t]*(#.*)?$`)

// mapKeyLineRe matches a plain, single-, or double-quoted YAML mapping
// key at the start of a line.
var mapKeyLineRe = regexp.MustCompile(`^(?:"[^"]*"|'[^']*'|[^-#\s][^:]*?):(\s|$)`)

// blockScalarHeaderRe matches a line that opens a literal (|) or folded
// (>) block scalar, optionally preceded by a mapping key or sequence
// dash, and optionally followed by chomping/indent indicators and a
// trailing comment.
var blockScalarHeaderRe = regexp.MustCompile(`(?:^|:|-)[ \t]*[|>][+-]?\d*[ \t]*(#.*)?$`)

// BareDashRewrites rewrites each bare "-" sequence item that goccy/go-yaml
// v1.19.2 would misparse into "- ~", and it reports the 1-based line
// numbers it rewrote, in ascending order. A comment after the dash stays
// on the line, as "- ~ # note". The yamlnode builder uses those lines to
// give the rewritten nulls yaml.v3's empty value back. Input that needs no
// rewrite comes back as the original slice with nil lines.
//
// goccy misparses a block-sequence item that is a bare "-" with no value
// token when a sibling mapping key follows it at the same or a shallower
// indent. It silently nests the sibling key inside the empty item instead
// of ending the sequence. v1.19.2 is the latest goccy release, so no
// version bump fixes it.
//
// spruce (yaml.v2-family) parses the same bare "-" as an explicit null
// list entry and keeps the following key as a sibling, so rewriting the
// line to "- ~" before goccy parses it matches spruce's semantics.
//
// It tracks literal and folded block scalars (| and >) by indentation and
// skips lines inside them, so a "-" appearing as literal text inside a
// multi-line string is never rewritten. A bare dash followed by another
// sequence item rather than a mapping key is left untouched, since that
// shape does not trigger the goccy bug.
//
// This is a text-level heuristic, not a full YAML parse. It does not track
// flow-style collections or tag and anchor edge cases, and those shapes do
// not exhibit the misparse.
func BareDashRewrites(data []byte) ([]byte, []int) {
	if len(data) == 0 || !hasBareDashLine(data) {
		return data, nil
	}

	lines := strings.Split(string(data), "\n")
	var rewritten []int
	inBlockScalar := false
	blockScalarIndent := 0

	for i, line := range lines {
		trimmed := strings.TrimRight(line, " \t\r")
		content := strings.TrimLeft(trimmed, " \t")
		indent := len(trimmed) - len(content)

		if inBlockScalar {
			if content == "" || indent > blockScalarIndent {
				continue
			}
			inBlockScalar = false
		}

		if blockScalarHeaderRe.MatchString(content) {
			inBlockScalar = true
			blockScalarIndent = indent
			continue
		}

		if m := bareDashLineRe.FindStringSubmatch(content); m != nil && bareDashEndsSequence(lines, i+1, indent) {
			lines[i] = strings.Repeat(" ", indent) + "- ~"
			if m[1] != "" {
				lines[i] += " " + m[1]
			}
			rewritten = append(rewritten, i+1)
		}
	}

	if rewritten == nil {
		return data, nil
	}
	return []byte(strings.Join(lines, "\n")), rewritten
}

// hasBareDashLine reports whether any line's content is a bare "-",
// optionally trailed by whitespace or a comment - the only line shape
// bareDashLineRe (and therefore the sanitizer) can match.
func hasBareDashLine(data []byte) bool {
	for start := 0; start < len(data); {
		line := data[start:]
		if nl := bytes.IndexByte(line, '\n'); nl >= 0 {
			line = line[:nl]
			start += nl + 1
		} else {
			start = len(data)
		}

		i := 0
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i == len(line) || line[i] != '-' {
			continue
		}
		i++
		for i < len(line) && (line[i] == ' ' || line[i] == '\t' || line[i] == '\r') {
			i++
		}
		if i == len(line) || line[i] == '#' {
			return true
		}
	}
	return false
}

// bareDashEndsSequence reports whether the bare dash at indent, whose
// following lines start at index from, is the trailing item goccy
// misparses: the next significant line has to be a mapping key at or
// outside the dash's own indentation. Anything else -- end of document,
// the dash's own more-indented value, a sibling sequence item, a document
// boundary, or a line that is not a mapping key at all -- parses
// correctly as written and is left alone.
func bareDashEndsSequence(lines []string, from, indent int) bool {
	next, nextIndent := nextSignificantLine(lines, from)
	switch {
	case next == "":
		return false
	case nextIndent > indent:
		return false
	case next == "-" || strings.HasPrefix(next, "- ") || strings.HasPrefix(next, "-\t"):
		return false
	case next == "---" || next == "...":
		return false
	}
	return mapKeyLineRe.MatchString(next)
}

// nextSignificantLine returns the trimmed content and indentation of the
// next non-blank, non-comment-only line starting at index from, or ""
// if none remain.
func nextSignificantLine(lines []string, from int) (content string, indent int) {
	for i := from; i < len(lines); i++ {
		raw := strings.TrimRight(lines[i], " \t\r")
		c := strings.TrimLeft(raw, " \t")
		if c == "" || strings.HasPrefix(c, "#") {
			continue
		}
		return c, len(raw) - len(c)
	}
	return "", 0
}
