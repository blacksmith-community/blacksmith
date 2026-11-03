package value

import (
	"math"
	"slices"
	"strings"
)

// Compare orders two values by jq's sort order. Kinds sort null, false,
// true, numbers, strings, arrays, and objects. Numbers compare exactly, so
// NaN sorts below every number, another NaN included, and -0 equals 0.
// Strings compare by codepoint, arrays element by element and then by
// length, and objects by their sorted key lists and then by the values in
// that key order.
func Compare(a, b *Value) int {
	return compareWith(a, b, func(x, y *Value) int { return x.num.Cmp(y.num) })
}

// CompareFloat is Compare with numbers compared through their doubles, for
// --float-numbers.
func CompareFloat(a, b *Value) int {
	return compareWith(a, b, compareDoubles)
}

func compareDoubles(x, y *Value) int {
	fx, fy := x.Float(), y.Float()
	switch {
	case math.IsNaN(fx):
		return -1
	case math.IsNaN(fy):
		return 1
	case fx < fy:
		return -1
	case fx > fy:
		return 1
	}
	return 0
}

func compareWith(a, b *Value, num func(x, y *Value) int) int {
	if a.kind != b.kind {
		return cmpInt(int(a.kind), int(b.kind))
	}
	switch a.kind {
	case KindNumber:
		return num(a, b)
	case KindString:
		return strings.Compare(a.str, b.str)
	case KindArray:
		for i := 0; i < len(a.arr) && i < len(b.arr); i++ {
			if c := compareWith(a.arr[i], b.arr[i], num); c != 0 {
				return c
			}
		}
		return cmpInt(len(a.arr), len(b.arr))
	case KindObject:
		ak, bk := sortedKeys(a), sortedKeys(b)
		if c := slices.Compare(ak, bk); c != 0 {
			return c
		}
		for _, k := range ak {
			av, _ := a.Get(k)
			bv, _ := b.Get(k)
			if c := compareWith(av, bv, num); c != 0 {
				return c
			}
		}
	case KindNull, KindFalse, KindTrue:
	}
	return 0
}

func sortedKeys(v *Value) []string {
	keys := v.Keys()
	slices.Sort(keys)
	return slices.Compact(keys)
}

func cmpInt(x, y int) int {
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

// Equal is jq's ==. It is false whenever either side is or contains a NaN,
// so it is not Compare == 0.
func Equal(a, b *Value) bool {
	return equalWith(a, b, func(x, y *Value) bool { return x.num.Equal(y.num) })
}

// EqualFloat is Equal with numbers compared through their doubles, for
// --float-numbers.
func EqualFloat(a, b *Value) bool {
	return equalWith(a, b, func(x, y *Value) bool { return x.Float() == y.Float() })
}

func equalWith(a, b *Value, num func(x, y *Value) bool) bool {
	if a.kind != b.kind {
		return false
	}
	switch a.kind {
	case KindNumber:
		return num(a, b)
	case KindString:
		return a.str == b.str
	case KindArray:
		if len(a.arr) != len(b.arr) {
			return false
		}
		for i := range a.arr {
			if !equalWith(a.arr[i], b.arr[i], num) {
				return false
			}
		}
	case KindObject:
		ak, bk := sortedKeys(a), sortedKeys(b)
		if !slices.Equal(ak, bk) {
			return false
		}
		for _, k := range ak {
			av, _ := a.Get(k)
			bv, _ := b.Get(k)
			if !equalWith(av, bv, num) {
				return false
			}
		}
	case KindNull, KindFalse, KindTrue:
	}
	return true
}
