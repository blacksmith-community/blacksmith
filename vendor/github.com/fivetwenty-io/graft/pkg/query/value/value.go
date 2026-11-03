package value

import (
	"math"

	"github.com/fivetwenty-io/graft/pkg/decimal"
)

// Kind names the type of a Value, in jq's sort order.
type Kind uint8

// The kinds, in jq's sort order.
const (
	KindNull Kind = iota
	KindFalse
	KindTrue
	KindNumber
	KindString
	KindArray
	KindObject
)

// Value is a JSON or YAML value. Values are immutable once built, so the
// updaters copy only the spine from the root to the changed node and share
// everything else, Meta included. The one exception is a container an
// Owner copied for itself and still holds, which only the Owner's holder
// can reach.
type Value struct {
	kind Kind
	num  decimal.Decimal // KindNumber
	lit  string          // KindNumber: the literal as written, while unmodified
	str  string          // KindString
	arr  []*Value        // KindArray
	obj  *object         // KindObject
	meta *Meta
	grow *spare // KindArray, KindObject: storage shared with values grown from it
}

// Pair is a string-keyed member for Obj.
type Pair struct {
	Key   string
	Value *Value
}

// Entry is an object member whose key is a scalar Value, so comments and
// styles ride on the key.
type Entry struct{ Key, Value *Value }

var (
	nullValue  = &Value{kind: KindNull}
	falseValue = &Value{kind: KindFalse}
	trueValue  = &Value{kind: KindTrue}
)

// Null returns the null value.
func Null() *Value { return nullValue }

// Bool returns true or false.
func Bool(b bool) *Value {
	if b {
		return trueValue
	}
	return falseValue
}

// Num converts a double exactly, to the shortest decimal that reads back to
// the same double.
func Num(f float64) *Value {
	// A whole double below 2^53 converts without formatting a string, and
	// the number is the same one. Negative zero takes the general path.
	if f == math.Trunc(f) && math.Abs(f) < 1<<53 && (f != 0 || !math.Signbit(f)) {
		return &Value{kind: KindNumber, num: decimal.FromInt64(int64(f))}
	}
	return &Value{kind: KindNumber, num: decimal.FromFloat64(f)}
}

// NumDec returns a number holding d.
func NumDec(d decimal.Decimal) *Value {
	return &Value{kind: KindNumber, num: d}
}

// NumLit returns a number holding d that prints as lit while it is
// unmodified.
func NumLit(d decimal.Decimal, lit string) *Value {
	return &Value{kind: KindNumber, num: d, lit: lit}
}

// NumText returns a number for a literal past the decimal bounds. It keeps
// the text and has no coefficient, so it prints and compares but fails any
// arithmetic, which Dec().Err() reports.
func NumText(lit string) *Value {
	d, _ := decimal.Parse(lit)
	return &Value{kind: KindNumber, num: d, lit: lit}
}

// Str returns a string value.
func Str(s string) *Value { return &Value{kind: KindString, str: s} }

// Arr returns an array of elems. It takes ownership of the slice, so the
// caller must not modify it afterward.
func Arr(elems ...*Value) *Value { return &Value{kind: KindArray, arr: elems} }

// Obj returns an object whose keys are strings, in the order given.
func Obj(pairs ...Pair) *Value {
	entries := make([]Entry, len(pairs))
	for i, p := range pairs {
		entries[i] = Entry{Str(p.Key), p.Value}
	}
	return &Value{kind: KindObject, obj: newObject(entries)}
}

// ObjEntries returns an object whose keys are scalar values, repeats and
// mixed key types included. It takes ownership of the slice, so the caller
// must not modify it afterward.
func ObjEntries(entries ...Entry) *Value {
	return &Value{kind: KindObject, obj: newObject(entries)}
}

// Kind returns the value's kind.
func (v *Value) Kind() Kind { return v.kind }

// Dec returns a number's exact value, and zero for any other kind.
func (v *Value) Dec() decimal.Decimal { return v.num }

// Bounded reports whether a number has a coefficient. It is false only for
// a NumText value.
func (v *Value) Bounded() bool { return v.num.Err() == nil }

// Float returns the double nearest to a number, and 0 for any other kind.
// For a NumText value that is an infinity or a zero with the literal's sign
// only when the exponent is outside float64's range.
func (v *Value) Float() float64 {
	if v.kind != KindNumber {
		return 0
	}
	return v.num.Float64()
}

// Literal returns the literal a number was read from while it is
// unmodified, and "" otherwise.
func (v *Value) Literal() string { return v.lit }

// Str returns a string's text, and "" for any other kind.
func (v *Value) Str() string { return v.str }

// Elems returns an array's elements as a read-only view of the value's own
// storage. A caller who needs to change them copies the slice, or starts
// from NewArrBuilderFrom.
func (v *Value) Elems() []*Value {
	v.grow.share()
	return v.arr[:len(v.arr):len(v.arr)]
}

// Len is the length of a string in bytes, of an array in elements, and of
// an object in entries.
func (v *Value) Len() int {
	switch v.kind {
	case KindString:
		return len(v.str)
	case KindArray:
		return len(v.arr)
	case KindObject:
		return len(v.obj.entries)
	case KindNull, KindFalse, KindTrue, KindNumber:
	}
	return 0
}

// Keys returns an object's keys formatted as strings, in entry order.
func (v *Value) Keys() []string {
	if v.kind != KindObject {
		return nil
	}
	keys := make([]string, len(v.obj.entries))
	for i, e := range v.obj.entries {
		keys[i] = keyString(e.Key)
	}
	return keys
}

// Entries returns an object's entries as a read-only view of the value's own
// storage. A caller who needs to change them copies the slice, or starts
// from NewObjBuilderFrom.
func (v *Value) Entries() []Entry {
	if v.kind != KindObject {
		return nil
	}
	v.grow.share()
	return v.obj.entries[:len(v.obj.entries):len(v.obj.entries)]
}

// Get returns the value of the last entry whose key formats to key.
func (v *Value) Get(key string) (*Value, bool) {
	if v.kind != KindObject {
		return nil, false
	}
	i, ok := v.find(key)
	if !ok {
		return nil, false
	}
	return v.obj.entries[i].Value, true
}

// Index returns the i-th element of an array. A negative i counts from the
// end.
func (v *Value) Index(i int) (*Value, bool) {
	if v.kind != KindArray {
		return nil, false
	}
	if i < 0 {
		i += len(v.arr)
	}
	if i < 0 || i >= len(v.arr) {
		return nil, false
	}
	return v.arr[i], true
}

// Meta returns the value's presentation details, or nil.
func (v *Value) Meta() *Meta { return v.meta }

// WithMeta returns a copy of v carrying m.
func (v *Value) WithMeta(m *Meta) *Value {
	v.grow.share()
	c := *v
	c.meta = m
	return &c
}

// copyShell returns a copy of v's header with the same Meta, ready to take
// a new payload.
func (v *Value) copyShell() *Value {
	return &Value{kind: v.kind, meta: v.meta}
}

func (v *Value) mustBe(k Kind, op string) {
	if v.kind != k {
		panic("value: " + op + " on a value of the wrong kind")
	}
}

// Set returns a copy of an object with key set to val. It replaces the last
// entry for the key and keeps that entry's key node, and otherwise appends
// a new entry. Meta is carried.
// It panics when v is not an object, which is part of the contract.
func (v *Value) Set(key string, val *Value) *Value {
	v.mustBe(KindObject, "Set")
	i, ok := v.find(key)
	if !ok {
		return v.add(Entry{Str(key), val}, key)
	}
	return v.replace(i, Entry{v.obj.entries[i].Key, val})
}

// SetEntry returns a copy of an object with the entry whose key formats
// like k set to val. It replaces the last such entry wholesale, so the new
// key node's comments and style win, and otherwise appends. Meta is
// carried.
// It panics when v is not an object, which is part of the contract.
func (v *Value) SetEntry(k, val *Value) *Value {
	v.mustBe(KindObject, "SetEntry")
	key := keyString(k)
	i, ok := v.find(key)
	if !ok {
		return v.add(Entry{k, val}, key)
	}
	return v.replace(i, Entry{k, val})
}

// replace returns a copy of an object with entry i replaced by e.
func (v *Value) replace(i int, e Entry) *Value {
	entries := make([]Entry, len(v.obj.entries))
	copy(entries, v.obj.entries)
	entries[i] = e
	out := v.copyShell()
	out.obj = newObject(entries)
	return out
}

// Delete returns a copy of an object without any entry whose key formats
// to key. Meta is carried.
// It panics when v is not an object, which is part of the contract.
func (v *Value) Delete(key string) *Value {
	v.mustBe(KindObject, "Delete")
	entries := make([]Entry, 0, len(v.obj.entries))
	for _, e := range v.obj.entries {
		if keyString(e.Key) != key {
			entries = append(entries, e)
		}
	}
	out := v.copyShell()
	out.obj = newObject(entries)
	return out
}

// SetIndex returns a copy of an array with element i set to val. A negative
// i counts from the end, and an i past the end pads with nulls. It panics
// for a negative i before the start. Meta is carried.
// It panics when v is not an array, which is part of the contract.
func (v *Value) SetIndex(i int, val *Value) *Value {
	v.mustBe(KindArray, "SetIndex")
	if i < 0 {
		i += len(v.arr)
		if i < 0 {
			panic("value: SetIndex before the start of the array")
		}
	}
	if i == len(v.arr) {
		return v.Append(val)
	}
	if i > len(v.arr) {
		// A jump past the end is no growth pattern, so the copy keeps no
		// spare room.
		return v.extend(i-len(v.arr)+1, false, func(tail []*Value) {
			for j := range tail {
				tail[j] = nullValue
			}
			tail[len(tail)-1] = val
		})
	}
	arr := make([]*Value, len(v.arr))
	copy(arr, v.arr)
	arr[i] = val
	out := v.copyShell()
	out.arr = arr
	return out
}

// DeleteIndex returns a copy of an array without element i, and v itself
// when i is out of range. Meta is carried.
// It panics when v is not an array, which is part of the contract.
func (v *Value) DeleteIndex(i int) *Value {
	v.mustBe(KindArray, "DeleteIndex")
	if i < 0 {
		i += len(v.arr)
	}
	if i < 0 || i >= len(v.arr) {
		return v
	}
	arr := make([]*Value, 0, len(v.arr)-1)
	arr = append(arr, v.arr[:i]...)
	arr = append(arr, v.arr[i+1:]...)
	out := v.copyShell()
	out.arr = arr
	return out
}
