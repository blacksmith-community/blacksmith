package sortkeys

import (
	"fmt"
	"slices"
	"sort"

	"github.com/fivetwenty-io/graft/internal/keyorder"
	"github.com/fivetwenty-io/graft/pkg/query/value"
)

// Mode names a key ordering.
type Mode string

// The orderings. None leaves keys where they are, Lexical compares by
// codepoint, and Spruce reproduces the order spruce's YAML encoder writes.
const (
	None    Mode = "none"
	Lexical Mode = "lexical"
	Spruce  Mode = "spruce"
)

// Parse reads a mode name.
func Parse(s string) (Mode, error) {
	switch m := Mode(s); m {
	case None, Lexical, Spruce:
		return m, nil
	}
	return "", fmt.Errorf("unknown sort mode %q (none, lexical, or spruce)", s)
}

// Less returns the comparison for m, or nil for None.
func Less(m Mode) func(a, b string) bool {
	switch m {
	case Lexical:
		return func(a, b string) bool { return a < b }
	case Spruce:
		return keyorder.SpruceLess
	case None:
	}
	return nil
}

// Keys returns a sorted copy of keys. None returns keys itself.
func Keys(keys []string, m Mode) []string {
	less := Less(m)
	if less == nil {
		return keys
	}
	out := slices.Clone(keys)
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

// Sort returns v with every object's entries stably sorted by key, at all
// depths. Entries keep their key nodes, key types, Meta, and duplicates, so
// only their order changes. None returns v itself.
func Sort(v *value.Value, m Mode) *value.Value {
	less := Less(m)
	if less == nil {
		return v
	}
	return sortValue(v, less)
}

func sortValue(v *value.Value, less func(a, b string) bool) *value.Value {
	switch v.Kind() {
	case value.KindArray:
		elems := v.Elems()
		out := make([]*value.Value, len(elems))
		for i, e := range elems {
			out[i] = sortValue(e, less)
		}
		return value.Arr(out...).WithMeta(v.Meta())
	case value.KindObject:
		entries := v.Entries()
		keys := v.Keys()
		order := make([]int, len(entries))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(i, j int) bool { return less(keys[order[i]], keys[order[j]]) })
		out := make([]value.Entry, len(entries))
		for i, at := range order {
			out[i] = value.Entry{Key: entries[at].Key, Value: sortValue(entries[at].Value, less)}
		}
		return value.ObjEntries(out...).WithMeta(v.Meta())
	case value.KindNull, value.KindFalse, value.KindTrue, value.KindNumber, value.KindString:
	}
	return v
}
