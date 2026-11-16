// Package cursorpath reads and writes the dotted path syntax shared by
// graft's tree package and its pkg/yaml document API.
package cursorpath

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

// NameFields are the fields a bare list segment is matched against, in
// order.
var NameFields = []string{"name", "key", "id"}

// SyntaxError is a path that can't be read, with the byte position of
// the character that stopped it.
type SyntaxError struct {
	Problem  string
	Position int
}

// Error returns the error message for SyntaxError.
func (e SyntaxError) Error() string {
	return fmt.Sprintf("syntax error: %s at position %d", e.Problem, e.Position)
}

// Parse splits a cursor path into its nodes. A '.' outside brackets
// ends a node, "[...]" makes one node of whatever it holds, and a
// leading "$" node is dropped.
func Parse(s string) ([]string, error) {
	var nodes []string
	node := bytes.NewBuffer([]byte{})
	bracketed := false

	push := func() {
		if node.Len() == 0 {
			return
		}
		nodeStr := node.String()
		if len(nodes) == 0 && nodeStr == "$" {
			node.Reset()
			return
		}

		nodes = append(nodes, nodeStr)
		node.Reset()
	}

	for pos, c := range s {
		switch c {
		case '.':
			if bracketed {
				node.WriteRune(c)
			} else {
				push()
			}

		case '[':
			if bracketed {
				return nil, SyntaxError{Problem: "unexpected '['", Position: pos}
			}
			push()
			bracketed = true

		case ']':
			if !bracketed {
				return nil, SyntaxError{Problem: "unexpected ']'", Position: pos}
			}
			push()
			bracketed = false

		default:
			node.WriteRune(c)
		}
	}
	push()

	return nodes, nil
}

// predicatePattern matches a list-predicate segment such as "name=primary".
var predicatePattern = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*)=(.*)$`)

// Predicate reports whether seg has the field=value shape that selects
// a list item by one of its fields.
func Predicate(seg string) (field, value string, ok bool) {
	m := predicatePattern.FindStringSubmatch(seg)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// Quote spells node so that Parse reads it back as one node. A node that
// contains a '.' is wrapped in brackets, because a bare '.' would split it
// into several nodes. Any other node is returned unchanged, and so is a
// node that already holds a '[' or ']', since no path spelling can carry
// such a node whole.
func Quote(node string) string {
	if strings.Contains(node, ".") && !strings.ContainsAny(node, "[]") {
		return "[" + node + "]"
	}
	return node
}
