package value

import (
	"sync"
	"sync/atomic"
)

// indexThreshold is the entry count below which a linear scan beats
// building a map.
const indexThreshold = 8

// object holds the entries of a KindObject value in insertion order. The
// key index maps a formatted key to its last entry, and it is built on the
// second lookup so that a one-shot read pays nothing for it.
type object struct {
	entries []Entry

	lookups atomic.Int32
	once    sync.Once
	index   map[string]int
}

func newObject(entries []Entry) *object {
	return &object{entries: entries}
}

// find returns the position of the last entry whose key formats to key.
func (o *object) find(key string) (int, bool) {
	if len(o.entries) < indexThreshold || o.lookups.Add(1) < 2 {
		return scanLast(o.entries, key)
	}
	o.once.Do(func() { o.index = buildIndex(o.entries) })
	i, ok := o.index[key]
	return i, ok
}

func scanLast(entries []Entry, key string) (int, bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		if keyString(entries[i].Key) == key {
			return i, true
		}
	}
	return 0, false
}

func buildIndex(entries []Entry) map[string]int {
	m := make(map[string]int, len(entries))
	for i, e := range entries {
		m[keyString(e.Key)] = i
	}
	return m
}

// keyString formats a key node the way the jq-facing accessors see it.
func keyString(k *Value) string {
	switch k.kind {
	case KindString:
		return k.str
	case KindNull:
		return "null"
	case KindFalse:
		return "false"
	case KindTrue:
		return "true"
	case KindNumber:
		if k.lit != "" {
			return k.lit
		}
		return k.num.String()
	case KindArray, KindObject:
	}
	return ""
}
