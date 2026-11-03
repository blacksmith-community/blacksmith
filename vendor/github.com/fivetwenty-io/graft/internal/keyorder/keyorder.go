/*
Package keyorder compares object keys the way spruce's YAML encoder orders
them. pkg/sortkeys sorts with it and pkg/query/value builds objects in it,
and neither can import the other, so both import this leaf.

The package imports nothing outside the standard library.
*/
package keyorder

import (
	"math"
	"strconv"
	"unicode"
)

// SpruceLess orders coerced map keys the way spruce's YAML encoder
// (geofffranks/yaml, a yaml.v2 fork) orders typed keys: numeric keys sort
// numerically among themselves and all of them precede string keys, and
// string keys sort with the fork's natural comparison, where digit runs
// compare numerically and non-letters sort before letters by raw rune.
//
// Keys arrive as strings, so numeric classification is recovered by
// re-parsing the key. A quoted "10" and a bare 10 are indistinguishable
// here, which is the documented residual divergence for maps mixing
// quoted-numeric keys with other keys (see docs/spruce/known-gaps.md,
// mixed-key-type-map-encoding-order).
func SpruceLess(a, b string) bool {
	af, aInt, aNum := keyNumericValue(a)
	bf, bInt, bNum := keyNumericValue(b)
	switch {
	case aNum && bNum:
		if aInt && bInt {
			// Exact compare so huge int64 keys don't collapse into
			// the same float64.
			an, _ := strconv.ParseInt(a, 10, 64)
			bn, _ := strconv.ParseInt(b, 10, 64)
			if an != bn {
				return an < bn
			}
		} else if af != bf {
			return af < bf
		}
		if aInt != bInt {
			// Equal values in different shapes: spruce sorts the int
			// key first (reflect.Int < reflect.Float64).
			return aInt
		}
		// Distinct spellings of the same number ("1" vs "01"):
		// fall through to the string comparison so the order never
		// depends on map iteration order.
		return naturalLess(a, b)
	case aNum != bNum:
		// Numeric tier before string tier, regardless of content
		// (spruce compares reflect.Kind values here).
		return aNum
	default:
		return naturalLess(a, b)
	}
}

// keyNumericValue reports whether a coerced key reads as a plain base-10
// number, its value, and whether it has integer shape. Exponent and
// signed forms count ("1e3" sorts at 1000, though its label keeps the
// source spelling); hex, octal, and underscore forms do not, and neither
// do "inf"/"nan" lookalikes that strconv.ParseFloat would accept — spruce
// types none of those as numbers from a bare base-10-looking scalar.
func keyNumericValue(s string) (f float64, isInt bool, ok bool) {
	// Typical keys are words; a base-10 number can only start with a
	// digit, sign, or dot, so skip both strconv calls (and their
	// allocated errors) for everything else. ParseFloat's other
	// spellings ("inf", "nan") are rejected by the IsInf/IsNaN check
	// below anyway, so skipping them here answers identically.
	if s == "" {
		return 0, false, false
	}
	if c := s[0]; (c < '0' || c > '9') && c != '-' && c != '+' && c != '.' {
		return 0, false, false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return float64(n), true, true
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
		return 0, false, false
	}
	return n, false, true
}

// digitRun reads the run of digits starting at index start, returning its
// value and the index one past its last digit. A start position that is
// not a digit yields the empty run — value 0, end == start — which is the
// behavior naturalLess's third documented quirk depends on.
func digitRun(rs []rune, start int) (int64, int) {
	var n int64
	end := start
	for ; end < len(rs) && unicode.IsDigit(rs[end]); end++ {
		n = n*10 + int64(rs[end]-'0')
	}
	return n, end
}

// naturalLess is a verbatim port of the string branch of the sorter in
// spruce's vendored geofffranks/yaml (sorter.go, Copyright 2011-2016
// Canonical Ltd., Apache License 2.0). Its quirks are kept deliberately —
// diverging from spruce here would be a parity bug:
//
//   - a rune position where exactly one side is a letter sorts the
//     non-letter first, so punctuation and digits both precede letters;
//   - a non-digit rune contributes an empty digit run that counts as 0,
//     which is why "int_val" sorts before "int64_val";
//   - unicode.IsDigit matches non-ASCII digits whose r-'0' arithmetic is
//     garbage, and the int64 run accumulator can overflow on absurd runs.
func naturalLess(a, b string) bool {
	ar, br := []rune(a), []rune(b)
	for i := 0; i < len(ar) && i < len(br); i++ {
		if ar[i] == br[i] {
			if !unicode.IsDigit(ar[i]) || !unicode.IsDigit(br[i]) {
				continue
			}
		}
		al := unicode.IsLetter(ar[i])
		bl := unicode.IsLetter(br[i])
		if al && bl {
			return ar[i] < br[i]
		}
		if al || bl {
			return bl
		}
		an, ai := digitRun(ar, i)
		bn, bi := digitRun(br, i)
		if an != bn {
			return an < bn
		}
		if ai != bi {
			return ai < bi
		}
		if ar[i] == br[i] {
			continue
		}
		return ar[i] < br[i]
	}
	return len(ar) < len(br)
}
