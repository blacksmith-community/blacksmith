// Package loopbind names the reserved tree that control-flow loop
// expansion binds loop variables under, and takes that tree's names out
// of text a user reads, so a loop body reads as the user wrote it.
package loopbind

import (
	"regexp"
	"strings"
)

// Key is the reserved top-level key that control-flow expansion stores
// loop bindings under.
const Key = "__graft_loop"

// Prefix matches the prefix the loop rewrite puts in front of a loop
// variable, "__graft_loop.<loop id>.<iteration>.", so that "svc.name"
// in a loop body reads "__graft_loop.l1a2b3c4d5e6f_1.0.svc.name". The
// loop id is "l", twelve hex digits of the source's digest, "_", and a
// sequence number (see controlflow's nextUID).
var Prefix = regexp.MustCompile(`__graft_loop\.l[0-9a-f]+_\d+\.\d+\.`)

// WithoutPrefix returns text with every loop binding prefix taken out,
// so a path reads as the user wrote it in the loop body.
func WithoutPrefix(text string) string {
	if !strings.Contains(text, Key) {
		return text
	}
	return Prefix.ReplaceAllString(text, "")
}

// WithoutText takes the loop binding prefix out of every string in
// data, map keys included, in place, and returns data. A map key whose
// plain form another key already holds keeps its prefix rather than
// overwrite that key. Any value that is not a string, a map, or a list
// is left as it is.
func WithoutText(data interface{}) interface{} {
	switch v := data.(type) {
	case string:
		return WithoutPrefix(v)
	case map[string]interface{}:
		for key, item := range v {
			plain := WithoutPrefix(key)
			if plain == key {
				v[key] = WithoutText(item)
				continue
			}
			if _, taken := v[plain]; taken {
				v[key] = WithoutText(item)
				continue
			}
			delete(v, key)
			v[plain] = WithoutText(item)
		}
		return v
	case []interface{}:
		for i, item := range v {
			v[i] = WithoutText(item)
		}
		return v
	}
	return data
}

// Strip removes the binding tree from doc and takes the binding prefix
// out of every string left in it, in place. It is for a document that
// still holds its bindings, such as an unevaluated merge, when the
// document is shown to a user rather than merged again.
func Strip(doc map[string]interface{}) {
	if _, ok := doc[Key]; !ok {
		return
	}
	delete(doc, Key)
	WithoutText(doc)
}
