// Package tree provides cursor-based navigation for tree data structures.
package tree

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// ParseCursor parses a string path into a Cursor object.
func ParseCursor(s string) (*Cursor, error) {
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
				return nil, SyntaxError{
					Problem:  "unexpected '['",
					Position: pos,
				}
			}
			push()
			bracketed = true

		case ']':
			if !bracketed {
				return nil, SyntaxError{
					Problem:  "unexpected ']'",
					Position: pos,
				}
			}
			push()
			bracketed = false

		default:
			node.WriteRune(c)
		}
	}
	push()

	return &Cursor{
		Nodes: nodes,
	}, nil
}

// BracketsOf re-parses a path string and reports which of its resulting
// nodes were written using bracket notation (e.g. "key[lookup]") versus
// dot notation (e.g. "key.lookup"). ParseCursor normalizes both notations
// into identical Nodes, discarding this distinction, so callers that need
// to treat bracketed segments as dynamic key references (e.g. the grab
// operator) must recover it from the raw source text via this function.
// The returned slice always has the same length as ParseCursor(s).Nodes,
// since both functions share identical node-boundary rules.
func BracketsOf(s string) []bool {
	var bracketedNodes []bool
	node := bytes.NewBuffer([]byte{})
	inBracket := false

	push := func(bracketed bool) {
		if node.Len() == 0 {
			return
		}
		if len(bracketedNodes) == 0 && node.String() == "$" {
			node.Reset()
			return
		}
		bracketedNodes = append(bracketedNodes, bracketed)
		node.Reset()
	}

	for _, c := range s {
		switch c {
		case '.':
			if inBracket {
				node.WriteRune(c)
			} else {
				push(false)
			}

		case '[':
			push(false)
			inBracket = true

		case ']':
			push(true)
			inBracket = false

		default:
			node.WriteRune(c)
		}
	}
	push(false)

	return bracketedNodes
}

// Copy creates a copy of the cursor.
func (c *Cursor) Copy() *Cursor {
	other := &Cursor{Nodes: []string{}}
	other.Nodes = append(other.Nodes, c.Nodes...)
	return other
}

// Contains checks if this cursor contains another cursor.
func (c *Cursor) Contains(other *Cursor) bool {
	if len(other.Nodes) < len(c.Nodes) {
		return false
	}
	match := false
	for i := range c.Nodes {
		if c.Nodes[i] != other.Nodes[i] {
			return false
		}
		match = true
	}
	return match
}

// Under checks if this cursor is under another cursor.
func (c *Cursor) Under(other *Cursor) bool {
	if len(c.Nodes) <= len(other.Nodes) {
		return false
	}
	match := false
	for i := range other.Nodes {
		if c.Nodes[i] != other.Nodes[i] {
			return false
		}
		match = true
	}
	return match
}

// Pop removes and returns the last path component.
func (c *Cursor) Pop() string {
	if len(c.Nodes) == 0 {
		return ""
	}
	last := c.Nodes[len(c.Nodes)-1]
	c.Nodes = c.Nodes[0 : len(c.Nodes)-1]
	return last
}

// Push adds a path component.
func (c *Cursor) Push(n string) {
	c.Nodes = append(c.Nodes, n)
}

// String returns the cursor as a dot-separated string.
func (c *Cursor) String() string {
	return strings.Join(c.Nodes, ".")
}

// QuoteNode returns node spelled so that ParseCursor and SplitPath read it
// back as the single node it is. A node that contains a '.' is wrapped in
// brackets, because a bare '.' would split it into several nodes. Any
// other node is returned unchanged, and so is a node that already holds a
// '[' or ']', since no path spelling can carry such a node whole.
func QuoteNode(node string) string {
	if strings.Contains(node, ".") && !strings.ContainsAny(node, "[]") {
		return "[" + node + "]"
	}
	return node
}

// Quoted returns the cursor as a dot-separated string, like String, but
// with each node spelled by QuoteNode, so that ParseCursor and SplitPath
// read the result back into the same nodes. Use it wherever a path is
// stored as a string and split again later, such as the prune and sort
// lists. For a cursor with no dotted nodes it equals String.
func (c *Cursor) Quoted() string {
	nodes := make([]string, len(c.Nodes))
	for i, node := range c.Nodes {
		nodes[i] = QuoteNode(node)
	}
	return strings.Join(nodes, ".")
}

// SplitPath splits a dotted path string into its nodes. A path that holds
// a node QuoteNode bracketed is parsed with ParseCursor so that node stays
// whole. Every other path is split on each '.' exactly as
// strings.Split(path, ".") splits it, so a caller that moves from
// strings.Split to SplitPath keeps its behavior for every path it handled
// before. As with ParseCursor, a bracketed path drops a leading "$" node,
// so callers strip any "$." prefix first.
func SplitPath(path string) []string {
	if hasQuotedNode(path) {
		if c, err := ParseCursor(path); err == nil {
			return c.Nodes
		}
	}
	return strings.Split(path, ".")
}

// hasQuotedNode reports whether path holds a bracketed node with a '.'
// inside it, which is the spelling QuoteNode gives a dotted node.
func hasQuotedNode(path string) bool {
	for i := 0; i < len(path); i++ {
		if path[i] != '[' {
			continue
		}
		end := strings.IndexAny(path[i+1:], "[]")
		if end < 0 {
			return false
		}
		if path[i+1+end] == ']' && strings.Contains(path[i+1:i+1+end], ".") {
			return true
		}
	}
	return false
}

// Depth returns the depth of the cursor path.
func (c *Cursor) Depth() int {
	return len(c.Nodes)
}

// Parent returns the parent component name.
func (c *Cursor) Parent() string {
	if len(c.Nodes) < 2 {
		return ""
	}
	return c.Nodes[len(c.Nodes)-2]
}

// Component returns a component by offset from the end.
func (c *Cursor) Component(offset int) string {
	offset = len(c.Nodes) + offset
	if offset < 0 || offset >= len(c.Nodes) {
		return ""
	}
	return c.Nodes[offset]
}

// Set sets a value at the cursor path in the given root map.
// Intermediate maps are created if they don't exist.
func (c *Cursor) Set(root map[string]interface{}, value interface{}) error {
	if len(c.Nodes) == 0 {
		return fmt.Errorf("empty cursor path")
	}

	current := interface{}(root)
	for i := 0; i < len(c.Nodes)-1; i++ {
		node := c.Nodes[i]
		switch container := current.(type) {
		case map[string]interface{}:
			next, exists := container[node]
			if !exists {
				newMap := make(map[string]interface{})
				container[node] = newMap
				current = newMap
			} else {
				current = next
			}
		case []interface{}:
			idx, err := strconv.Atoi(node)
			if err != nil || idx < 0 || idx >= len(container) {
				return fmt.Errorf("invalid array index %q", node)
			}
			current = container[idx]
		default:
			return fmt.Errorf("cannot traverse into %T at %q", current, node)
		}
	}

	lastNode := c.Nodes[len(c.Nodes)-1]
	switch container := current.(type) {
	case map[string]interface{}:
		container[lastNode] = value
		return nil
	case []interface{}:
		idx, err := strconv.Atoi(lastNode)
		if err != nil || idx < 0 || idx >= len(container) {
			return fmt.Errorf("invalid array index %q", lastNode)
		}
		container[idx] = value
		return nil
	default:
		return fmt.Errorf("cannot set on %T", current)
	}
}

// Delete removes the value at the cursor path from the given root map.
func (c *Cursor) Delete(root map[string]interface{}) error {
	if len(c.Nodes) == 0 {
		return fmt.Errorf("empty cursor path")
	}

	current := interface{}(root)
	for i := 0; i < len(c.Nodes)-1; i++ {
		node := c.Nodes[i]
		switch container := current.(type) {
		case map[string]interface{}:
			next, exists := container[node]
			if !exists {
				return NotFoundError{Path: c.Nodes[:i+1]}
			}
			current = next
		case []interface{}:
			idx, err := strconv.Atoi(node)
			if err != nil || idx < 0 || idx >= len(container) {
				return NotFoundError{Path: c.Nodes[:i+1]}
			}
			current = container[idx]
		default:
			return fmt.Errorf("cannot traverse into %T at %q", current, node)
		}
	}

	lastNode := c.Nodes[len(c.Nodes)-1]
	switch container := current.(type) {
	case map[string]interface{}:
		if _, exists := container[lastNode]; !exists {
			return NotFoundError{Path: c.Nodes}
		}
		delete(container, lastNode)
		return nil
	default:
		return fmt.Errorf("cannot delete from %T", current)
	}
}
