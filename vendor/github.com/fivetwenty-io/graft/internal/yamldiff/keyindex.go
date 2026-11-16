package yamldiff

import "github.com/fivetwenty-io/graft/internal/yamlnode"

// KeyIndex finds the entries of mappings by key text without scanning
// the mapping for each lookup. It indexes a mapping the first time it
// is asked about it, so one index serves every lookup under that
// mapping. A mapping must not change while an index holds it. The zero
// value is not ready for use, so make one with NewKeyIndex.
type KeyIndex struct {
	maps map[*yamlnode.Node]*mapKeys
}

// NewKeyIndex returns an empty KeyIndex.
func NewKeyIndex() *KeyIndex {
	return &KeyIndex{maps: make(map[*yamlnode.Node]*mapKeys)}
}

// keyHit is the first value stored under a key text and the number of
// entries that have it.
type keyHit struct {
	value *yamlnode.Node
	count int
}

// mapKeys holds a mapping's entries by key text, once for keys as
// written, once for keys with aliases followed, and once for the keys
// that are scalars after following aliases.
type mapKeys struct {
	raw, followed, scalar map[string]keyHit
}

func (h keyHit) add(value *yamlnode.Node) keyHit {
	if h.count == 0 {
		h.value = value
	}
	h.count++
	return h
}

func (ix *KeyIndex) of(m *yamlnode.Node) *mapKeys {
	if keys, ok := ix.maps[m]; ok {
		return keys
	}
	keys := &mapKeys{
		raw:      make(map[string]keyHit),
		followed: make(map[string]keyHit),
		scalar:   make(map[string]keyHit),
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k, v := m.Content[i], m.Content[i+1]
		keys.raw[k.Value] = keys.raw[k.Value].add(v)
		f := yamlnode.FollowAlias(k)
		keys.followed[f.Value] = keys.followed[f.Value].add(v)
		if f.Kind == yamlnode.ScalarNode {
			keys.scalar[f.Value] = keys.scalar[f.Value].add(v)
		}
	}
	ix.maps[m] = keys
	return keys
}

// Value returns the value of the first entry of the mapping m whose key
// has the text, and how many entries have it. The key is matched as
// written when from is true, and with aliases followed otherwise.
func (ix *KeyIndex) Value(m *yamlnode.Node, text string, from bool) (*yamlnode.Node, int) {
	keys := ix.of(m)
	hit := keys.followed[text]
	if from {
		hit = keys.raw[text]
	}
	return hit.value, hit.count
}

// ScalarValue returns the value of the first entry of the mapping m
// whose key is a scalar, with aliases followed, that has the text, and
// how many entries have it.
func (ix *KeyIndex) ScalarValue(m *yamlnode.Node, text string) (*yamlnode.Node, int) {
	hit := ix.of(m).scalar[text]
	return hit.value, hit.count
}
