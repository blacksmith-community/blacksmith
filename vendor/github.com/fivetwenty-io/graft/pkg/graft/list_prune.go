package graft

import (
	"context"
	"reflect"
	"regexp"
)

// pruneEntryRx matches a list entry that is a (( prune )) call and
// nothing more, the way the merger matches one, so a spaced spelling
// counts too. An entry with more text after the call is a plain string.
var pruneEntryRx = regexp.MustCompile(`^\s*\Q((\E\s*prune\s*\Q))\E\s*$`)

// pruneCallRx matches any string that starts with a (( prune )) call,
// the way the merger matches a map value.
var pruneCallRx = regexp.MustCompile(`^\s*\Q((\E\s*prune\s*\Q))\E`)

// isPruneEntry reports whether the list entry v is a (( prune )) call.
func isPruneEntry(v interface{}) bool {
	s, ok := v.(string)
	return ok && pruneEntryRx.MatchString(s)
}

// isPruneCall reports whether v starts with a (( prune )) call, which
// is enough for the merger to act on it as a map value.
func isPruneCall(v interface{}) bool {
	s, ok := v.(string)
	return ok && pruneCallRx.MatchString(s)
}

// mergedKey is the context key a merge sets when it hands its result to
// Evaluate, since the merge has already handled the result's lists.
type mergedKey struct{}

// withMerged returns a copy of ctx that tells Evaluate its document
// comes from a merge.
func withMerged(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, mergedKey{}, true)
}

// fromMerge reports whether ctx carries a merge's result.
func fromMerge(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	merged, _ := ctx.Value(mergedKey{}).(bool)
	return merged
}

// dropPruneEntries removes every (( prune )) entry from the lists in
// data, at any depth, in place. A merge removes these entries as it
// merges each document, before any operator runs, so an operator that
// reads the list never sees the call's text, and an index past the
// entry names the entry after it. A document evaluated with no merge
// gets the same treatment here. A map or list that appears in more than
// one place is handled once, and each place gets the same result.
func dropPruneEntries(data map[string]interface{}) {
	w := pruneEntryWalk{
		maps:  map[uintptr]bool{},
		lists: map[listSpan][]interface{}{},
	}
	w.value(data)
}

// pruneEntryWalk is one dropPruneEntries walk. maps holds the maps
// already walked, and lists holds what each list already walked became.
type pruneEntryWalk struct {
	maps  map[uintptr]bool
	lists map[listSpan][]interface{}
}

// listSpan names a list by the address of its first entry and its
// length, since two lists cut from one slice can start at the same
// entry and end at different ones.
type listSpan struct {
	p uintptr
	n int
}

// value returns v with the (( prune )) entries gone from its lists, and
// whether v itself had to change. It writes only where an entry goes,
// so a document with none is left untouched.
func (w *pruneEntryWalk) value(v interface{}) (interface{}, bool) {
	switch t := v.(type) {
	case map[string]interface{}:
		if t == nil {
			return v, false
		}
		p := reflect.ValueOf(t).Pointer()
		if w.maps[p] {
			return v, false
		}
		w.maps[p] = true
		for k, e := range t {
			if out, changed := w.value(e); changed {
				t[k] = out
			}
		}
		return v, false
	case []interface{}:
		if len(t) == 0 {
			return v, false
		}
		p := listSpan{p: reflect.ValueOf(t).Pointer(), n: len(t)}
		if out, ok := w.lists[p]; ok {
			return out, len(out) != len(t)
		}
		out := t
		for i, e := range t {
			if isPruneEntry(e) {
				out = make([]interface{}, i, len(t))
				copy(out, t[:i])
				for _, rest := range t[i+1:] {
					if !isPruneEntry(rest) {
						out = append(out, rest)
					}
				}
				break
			}
		}
		w.lists[p] = out
		for i, e := range out {
			if inner, changed := w.value(e); changed {
				out[i] = inner
			}
		}
		return out, len(out) != len(t)
	}
	return v, false
}
