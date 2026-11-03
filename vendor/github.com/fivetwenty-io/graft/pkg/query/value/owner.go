package value

// Owner changes in place the one container it holds, so a loop that keeps
// changing the same container, such as a reduce whose state gains a key or
// replaces an element each step, costs constant time a step rather than a
// copy of the container.
//
// An Owner holds only a container it copied for itself, never one it was
// given, so no value built any other way ever changes. It holds one at a
// time, and taking a new one lets the last go. Whoever holds the Owner
// must keep the only reference to the container it holds: Set and
// SetIndex may change that container and return it, so the caller must
// not use the value it passed in afterwards, and must call Disown or
// Release before the container reaches anything else. The container also
// lets itself go when anything reads its storage, through Elems, Entries,
// WithMeta, Append, or the updaters that grow it in shared storage. A
// value an Owner no longer holds is immutable like any other.
//
// The zero Owner is ready to use. An Owner is not safe for concurrent use.
type Owner struct {
	held *spare
}

// Set returns an object with key set to val, as v.Set does. It changes v
// in place when o holds v, and otherwise copies v and holds the copy. o
// lets val go first if it holds it, since val is about to be shared.
// It panics when v is not an object, which is part of the contract.
func (o *Owner) Set(v *Value, key string, val *Value) *Value {
	v.mustBe(KindObject, "Owner.Set")
	o.Disown(val)
	if !o.holds(v) {
		v = o.take(v)
	}
	if i, ok := v.find(key); ok {
		v.obj.entries[i].Value = val
		return v
	}
	n := len(v.obj.entries)
	v.obj.entries = append(v.obj.entries, Entry{Str(key), val})
	s := v.grow
	if s.added == nil {
		s.added = map[string]int{}
	}
	s.added[key] = n
	s.used = n + 1
	return v
}

// SetIndex returns an array with element i set to val, as v.SetIndex does,
// where i may be the array's length to append. It changes v in place when
// o holds v, and otherwise copies v and holds the copy. o lets val go
// first if it holds it.
// It panics when v is not an array or i is outside 0 to v.Len(), which is
// part of the contract.
func (o *Owner) SetIndex(v *Value, i int, val *Value) *Value {
	v.mustBe(KindArray, "Owner.SetIndex")
	if i < 0 || i > len(v.arr) {
		panic("value: Owner.SetIndex outside the array")
	}
	o.Disown(val)
	if !o.holds(v) {
		v = o.take(v)
	}
	if i < len(v.arr) {
		v.arr[i] = val
		return v
	}
	v.arr = append(v.arr, val)
	v.grow.used = len(v.arr)
	return v
}

// Disown lets v go if o holds it, after which v is immutable.
func (o *Owner) Disown(v *Value) {
	if o.holds(v) {
		o.Release()
	}
}

// Release lets go of the container o holds, if any.
func (o *Owner) Release() {
	if o.held != nil {
		o.held.owner = nil
		o.held = nil
	}
}

// holds reports whether o holds v.
func (o *Owner) holds(v *Value) bool {
	return v != nil && v.grow != nil && v.grow.owner == o && o.held == v.grow
}

// take copies v into storage of o's own, with room to grow, and holds the
// copy in place of whatever it held.
func (o *Owner) take(v *Value) *Value {
	o.Release()
	out := v.copyShell()
	n := len(v.arr)
	s := &spare{owner: o}
	if v.kind == KindArray {
		out.arr = make([]*Value, n, max(n+1, room(n+1)))
		copy(out.arr, v.arr)
	} else {
		n = len(v.obj.entries)
		entries := make([]Entry, n, max(n+1, room(n+1)))
		copy(entries, v.obj.entries)
		out.obj = newObject(entries)
		s.base = n
	}
	s.used = n
	out.grow = s
	o.held = s
	return out
}

// share lets a store's owner go before anything else may see its storage.
// Only the owner's holder can reach a held store, so the write races with
// nothing, and a store no Owner holds is never written.
func (s *spare) share() {
	if s != nil && s.owner != nil {
		s.owner.held = nil
		s.owner = nil
	}
}
