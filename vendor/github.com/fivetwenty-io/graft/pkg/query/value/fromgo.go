package value

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/fivetwenty-io/graft/internal/keyorder"
	"github.com/fivetwenty-io/graft/pkg/decimal"
)

// FromGo converts a Go value as the YAML and JSON decoders produce it into
// a Value. It takes nil, bool, string, every integer and float type,
// decimal.Decimal, a Value (which it returns as it is), []interface{},
// []string, and map[string]interface{}. A map becomes an object whose keys
// follow spruce's order, because a Go map has none of its own. Any other
// type is an error that names it and where it sits.
func FromGo(v interface{}) (*Value, error) {
	return fromGo(v, "")
}

func fromGo(v interface{}, at string) (*Value, error) {
	switch x := v.(type) {
	case []string:
		elems := make([]*Value, len(x))
		for i, s := range x {
			elems[i] = Str(s)
		}
		return Arr(elems...), nil
	case []interface{}:
		elems := make([]*Value, len(x))
		for i, e := range x {
			c, err := fromGo(e, at+"["+strconv.Itoa(i)+"]")
			if err != nil {
				return nil, err
			}
			elems[i] = c
		}
		return Arr(elems...), nil
	case map[string]interface{}:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.SliceStable(keys, func(i, j int) bool { return keyorder.SpruceLess(keys[i], keys[j]) })
		pairs := make([]Pair, len(keys))
		for i, k := range keys {
			c, err := fromGo(x[k], at+"."+k)
			if err != nil {
				return nil, err
			}
			pairs[i] = Pair{Key: k, Value: c}
		}
		return Obj(pairs...), nil
	}
	if sv, ok := scalarFromGo(v); ok {
		return sv, nil
	}
	if at == "" {
		at = "the root"
	}
	return nil, fmt.Errorf("value: cannot convert %T at %s", v, at)
}

// scalarFromGo converts the Go types that hold one value, and reports
// false for any other.
func scalarFromGo(v interface{}) (*Value, bool) {
	switch x := v.(type) {
	case nil:
		return Null(), true
	case *Value:
		if x == nil {
			return Null(), true
		}
		return x, true
	case bool:
		return Bool(x), true
	case string:
		return Str(x), true
	case float32:
		return Num(float64(x)), true
	case float64:
		return Num(x), true
	case decimal.Decimal:
		return NumDec(x), true
	}
	return integerFromGo(v)
}

func integerFromGo(v interface{}) (*Value, bool) {
	switch x := v.(type) {
	case int:
		return NumDec(decimal.FromInt64(int64(x))), true
	case int8:
		return NumDec(decimal.FromInt64(int64(x))), true
	case int16:
		return NumDec(decimal.FromInt64(int64(x))), true
	case int32:
		return NumDec(decimal.FromInt64(int64(x))), true
	case int64:
		return NumDec(decimal.FromInt64(x)), true
	case uint:
		return uintValue(uint64(x)), true
	case uint8:
		return uintValue(uint64(x)), true
	case uint16:
		return uintValue(uint64(x)), true
	case uint32:
		return uintValue(uint64(x)), true
	case uint64:
		return uintValue(x), true
	}
	return nil, false
}

func uintValue(u uint64) *Value {
	d, err := decimal.Parse(strconv.FormatUint(u, 10))
	if err != nil {
		return Num(float64(u))
	}
	return NumDec(d)
}

// ToGoNumbers chooses the Go type ToGo gives a number.
type ToGoNumbers uint8

// The number types. ToGoDecimal, the zero value, keeps every digit.
const (
	// ToGoDecimal returns a decimal.Decimal.
	ToGoDecimal ToGoNumbers = iota
	// ToGoInt64 returns an int64 when the number is an integer that fits,
	// and a decimal.Decimal otherwise.
	ToGoInt64
	// ToGoFloat64 returns the nearest float64.
	ToGoFloat64
)

// ToGoOptions tunes ToGo.
type ToGoOptions struct {
	// Numbers is the Go type a number becomes.
	Numbers ToGoNumbers
}

// ToGo converts a Value to the Go value FromGo reads back: nil, bool,
// string, a number as ToGoOptions.Numbers chooses, []interface{}, and
// map[string]interface{}. A map has no order and no repeated keys, so the
// last entry for a key wins and the key order is lost.
func ToGo(v *Value, opts ...ToGoOptions) interface{} {
	var o ToGoOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	return toGo(v, o)
}

func toGo(v *Value, o ToGoOptions) interface{} {
	if v == nil {
		return nil
	}
	switch v.Kind() {
	case KindFalse:
		return false
	case KindTrue:
		return true
	case KindNumber:
		switch o.Numbers {
		case ToGoFloat64:
			return v.Float()
		case ToGoInt64:
			if n, ok := v.Dec().Int64(); ok {
				return n
			}
		case ToGoDecimal:
		}
		return v.Dec()
	case KindString:
		return v.Str()
	case KindArray:
		out := make([]interface{}, len(v.arr))
		for i, e := range v.arr {
			out[i] = toGo(e, o)
		}
		return out
	case KindObject:
		out := make(map[string]interface{}, len(v.obj.entries))
		for _, e := range v.obj.entries {
			out[keyString(e.Key)] = toGo(e.Value, o)
		}
		return out
	case KindNull:
	}
	return nil
}
