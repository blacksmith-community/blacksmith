package value

import "slices"

// ArrBuilder fills an array in place and seals it into an immutable Value.
// It copies its source once, and after Seal the builder is dead. The builder
// holds the Value it will seal, so building costs one allocation for both.
type ArrBuilder struct {
	v    Value
	done bool
}

// NewArrBuilder starts an empty array with room for n elements.
func NewArrBuilder(n int) *ArrBuilder {
	return &ArrBuilder{v: Value{kind: KindArray, arr: make([]*Value, 0, n)}}
}

// NewArrBuilderFrom starts from a copy of base, carrying its Meta.
func NewArrBuilderFrom(base *Value) *ArrBuilder {
	base.mustBe(KindArray, "NewArrBuilderFrom")
	b := &ArrBuilder{v: Value{kind: KindArray, meta: base.meta}}
	b.v.arr = make([]*Value, len(base.arr), len(base.arr)+1)
	copy(b.v.arr, base.arr)
	return b
}

// Append adds an element at the end.
func (b *ArrBuilder) Append(e *Value) {
	b.live()
	b.v.arr = append(b.v.arr, e)
}

// Set replaces element i, which must be in range.
func (b *ArrBuilder) Set(i int, e *Value) {
	b.live()
	b.v.arr[i] = e
}

// Seal returns the finished array. The builder must not be used again.
func (b *ArrBuilder) Seal() *Value {
	b.live()
	b.done = true
	b.v.arr = slices.Clip(b.v.arr)
	return &b.v
}

func (b *ArrBuilder) live() {
	if b.done {
		panic("value: ArrBuilder used after Seal")
	}
}

// ObjBuilder fills an object in place and seals it into an immutable
// Value. It copies its source once, and after Seal the builder is dead.
type ObjBuilder struct {
	v     *Value
	entry []Entry
	index map[string]int // formatted key to last entry, built on demand
}

// NewObjBuilder starts an empty object with room for n entries.
func NewObjBuilder(n int) *ObjBuilder {
	return &ObjBuilder{v: &Value{kind: KindObject}, entry: make([]Entry, 0, n)}
}

// NewObjBuilderFrom starts from a copy of base, carrying its Meta.
func NewObjBuilderFrom(base *Value) *ObjBuilder {
	base.mustBe(KindObject, "NewObjBuilderFrom")
	entries := make([]Entry, len(base.obj.entries), len(base.obj.entries)+1)
	copy(entries, base.obj.entries)
	return &ObjBuilder{v: base.copyShell(), entry: entries}
}

// Set replaces the last entry for key, keeping its key node, or appends a
// new entry.
func (b *ObjBuilder) Set(key string, v *Value) {
	b.live()
	if i, ok := b.find(key); ok {
		b.entry[i].Value = v
		return
	}
	b.add(Entry{Str(key), v}, key)
}

// SetEntry sets the entry whose key formats like k. It replaces the last
// such entry wholesale, so the new key node's comments and style win, and
// otherwise appends.
func (b *ObjBuilder) SetEntry(k, v *Value) {
	b.live()
	key := keyString(k)
	if i, ok := b.find(key); ok {
		b.entry[i] = Entry{k, v}
		return
	}
	b.add(Entry{k, v}, key)
}

// Delete removes every entry whose key formats to key.
func (b *ObjBuilder) Delete(key string) {
	b.live()
	if _, ok := b.find(key); !ok {
		return
	}
	kept := b.entry[:0]
	for _, e := range b.entry {
		if keyString(e.Key) != key {
			kept = append(kept, e)
		}
	}
	clear(b.entry[len(kept):])
	b.entry = kept
	b.index = nil
}

// Seal returns the finished object. The builder must not be used again.
func (b *ObjBuilder) Seal() *Value {
	b.live()
	v := b.v
	v.obj = newObject(slices.Clip(b.entry))
	b.v, b.entry, b.index = nil, nil, nil
	return v
}

func (b *ObjBuilder) live() {
	if b.v == nil {
		panic("value: ObjBuilder used after Seal")
	}
}

func (b *ObjBuilder) find(key string) (int, bool) {
	if len(b.entry) < indexThreshold {
		return scanLast(b.entry, key)
	}
	if b.index == nil {
		b.index = buildIndex(b.entry)
	}
	i, ok := b.index[key]
	return i, ok
}

func (b *ObjBuilder) add(e Entry, key string) {
	b.entry = append(b.entry, e)
	if b.index != nil {
		b.index[key] = len(b.entry) - 1
	}
}
