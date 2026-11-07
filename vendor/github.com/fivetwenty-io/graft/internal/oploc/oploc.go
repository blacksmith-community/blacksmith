// Package oploc indexes the paths of the operators in one evaluation
// phase by their canonical form, so an operator that asks which other
// operators sit at or under a path, while the dependency graph is built,
// pays for each path once rather than once per question. The evaluator
// builds the index for the operator paths it hands to Dependencies. The
// operators package reads it through a hook that pkg/graft installs, so
// the evaluator exports no field for it.
package oploc

import (
	"strconv"
	"strings"

	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// Index holds the operator paths of one phase, keyed by their canonical
// form. A nil *Index holds nothing.
type Index struct {
	locs  []*tree.Cursor
	canon []string
	at    map[string]bool
	under map[string][]int
}

// New indexes locs, each canonicalized against root. A path that does
// not resolve against root is indexed as written.
func New(locs []*tree.Cursor, root interface{}) *Index {
	idx := &Index{
		locs:  locs,
		canon: make([]string, len(locs)),
		at:    make(map[string]bool, len(locs)),
		under: make(map[string][]int, len(locs)),
	}
	for i, loc := range locs {
		nodes := Canonical(loc, root).Nodes
		k := key(nodes)
		idx.canon[i] = k
		idx.at[k] = true
		for n := 0; n <= len(nodes); n++ {
			p := key(nodes[:n])
			idx.under[p] = append(idx.under[p], i)
		}
	}
	return idx
}

// Covers reports whether idx was built for locs, which it was when both
// name the same slice.
func (idx *Index) Covers(locs []*tree.Cursor) bool {
	if idx == nil || len(idx.locs) != len(locs) {
		return false
	}
	return len(locs) == 0 || &idx.locs[0] == &locs[0]
}

// AtOrAbove reports whether an operator sits at the canonical path nodes
// or at a path above it, other than the document root.
func (idx *Index) AtOrAbove(nodes []string) bool {
	if idx == nil {
		return false
	}
	for n := 1; n <= len(nodes); n++ {
		if idx.at[key(nodes[:n])] {
			return true
		}
	}
	return false
}

// Under returns the operator paths at or under the canonical path nodes,
// in the order they were indexed, leaving out the operator at the
// canonical path self. An empty nodes stands for the whole document.
func (idx *Index) Under(nodes, self []string) []*tree.Cursor {
	if idx == nil {
		return nil
	}
	skip := key(self)
	hits := idx.under[key(nodes)]
	out := make([]*tree.Cursor, 0, len(hits))
	for _, i := range hits {
		if self != nil && idx.canon[i] == skip {
			continue
		}
		out = append(out, idx.locs[i])
	}
	return out
}

// Canonical returns c with list entries named by index, the form the
// dependency graph keys operators by, or c itself when it does not
// resolve against root.
func Canonical(c *tree.Cursor, root interface{}) *tree.Cursor {
	if canon, err := c.Canonical(root); err == nil {
		return canon
	}
	return c
}

// Key encodes nodes so that no two paths share a key, even when a node
// holds a dot.
func Key(nodes []string) string {
	return key(nodes)
}

// key is Key.
func key(nodes []string) string {
	var b strings.Builder
	for _, n := range nodes {
		b.WriteString(strconv.Itoa(len(n)))
		b.WriteByte(':')
		b.WriteString(n)
	}
	return b.String()
}

// lookup returns the Index the evaluator ev built, or nil.
var lookup func(ev any) *Index

// SetLookup installs the function For calls. pkg/graft calls it once,
// from an init function.
func SetLookup(f func(ev any) *Index) {
	lookup = f
}

// For returns the Index of locs for the evaluator ev, whose tree is
// root. It is the one ev built when ev built it for locs, and a fresh
// one otherwise.
func For(ev any, locs []*tree.Cursor, root interface{}) *Index {
	if lookup != nil {
		if idx := lookup(ev); idx.Covers(locs) {
			return idx
		}
	}
	return New(locs, root)
}
