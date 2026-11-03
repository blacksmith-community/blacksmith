//
// Copyright (c) 2011-2019 Canonical Ltd
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Ported from go.yaml.in/yaml/v3 v3.0.4 sorter.go and modified for graft:
// Less is split into helpers to meet graft's lint rules, with the same
// comparisons in the same order, and two exported wrappers are added.

package yamlnode

import (
	"reflect"
	"sort"
	"unicode"
)

// KeyLess reports whether map key a sorts before b in yaml.v3's order:
// numbers and booleans before strings, digit runs compared numerically,
// and, at the first differing rune, a non-letter before a letter unless
// the previous rune was a digit.
func KeyLess(a, b reflect.Value) bool {
	return keyList{a, b}.Less(0, 1)
}

// SortKeys sorts map keys in yaml.v3's order with sort.Sort, which is not
// stable, exactly as yaml.v3's encoder does.
func SortKeys(keys []reflect.Value) {
	sort.Sort(keyList(keys))
}

// keyList orders map keys the way yaml.v3's encoder does.
type keyList []reflect.Value

func (l keyList) Len() int      { return len(l) }
func (l keyList) Swap(i, j int) { l[i], l[j] = l[j], l[i] }

// Less unwraps interfaces and pointers, orders numbers and booleans by
// value and then by kind, orders other mixed kinds by kind, and compares
// two strings with stringKeyLess.
func (l keyList) Less(i, j int) bool {
	a, b := unwrapKey(l[i]), unwrapKey(l[j])
	ak, bk := a.Kind(), b.Kind()
	af, aok := keyFloat(a)
	bf, bok := keyFloat(b)
	if aok && bok {
		if af != bf {
			return af < bf
		}
		if ak != bk {
			return ak < bk
		}
		return numLess(a, b)
	}
	if ak != reflect.String || bk != reflect.String {
		return ak < bk
	}
	return stringKeyLess([]rune(a.String()), []rune(b.String()))
}

// unwrapKey follows Elem through non-nil interfaces and pointers.
func unwrapKey(v reflect.Value) reflect.Value {
	for (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) && !v.IsNil() {
		v = v.Elem()
	}
	return v
}

// stringKeyLess compares two strings rune by rune. At the first
// difference, two letters compare by code point, a letter and a
// non-letter put the non-letter first unless the previous rune was a
// digit, and two non-letters compare as digit runs. A string that is a
// prefix of the other sorts first.
func stringKeyLess(ar, br []rune) bool {
	digits := false
	for i := 0; i < len(ar) && i < len(br); i++ {
		if ar[i] == br[i] {
			digits = unicode.IsDigit(ar[i])
			continue
		}
		al, bl := unicode.IsLetter(ar[i]), unicode.IsLetter(br[i])
		if al && bl {
			return ar[i] < br[i]
		}
		if al || bl {
			if digits {
				return al
			}
			return bl
		}
		return digitRunLess(ar, br, i)
	}
	return len(ar) < len(br)
}

// digitRunLess compares the digit runs that start at index i, where the
// two strings first differ and neither rune is a letter. A run that
// continues a nonzero number keeps its leading zeros significant, a
// shorter run wins a tie in value, and the differing runes break the
// last tie.
func digitRunLess(ar, br []rune, i int) bool {
	var ai, bi int
	var an, bn int64
	if ar[i] == '0' || br[i] == '0' {
		for j := i - 1; j >= 0 && unicode.IsDigit(ar[j]); j-- {
			if ar[j] != '0' {
				an = 1
				bn = 1
				break
			}
		}
	}
	for ai = i; ai < len(ar) && unicode.IsDigit(ar[ai]); ai++ {
		an = an*10 + int64(ar[ai]-'0')
	}
	for bi = i; bi < len(br) && unicode.IsDigit(br[bi]); bi++ {
		bn = bn*10 + int64(br[bi]-'0')
	}
	if an != bn {
		return an < bn
	}
	if ai != bi {
		return ai < bi
	}
	return ar[i] < br[i]
}

// keyFloat returns a float value for v if it is a number or a bool, and
// whether it is one.
func keyFloat(v reflect.Value) (f float64, ok bool) {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int()), true
	case reflect.Float32, reflect.Float64:
		return v.Float(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return float64(v.Uint()), true
	case reflect.Bool:
		if v.Bool() {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

// numLess reports whether a < b. Both must have the same numeric or bool
// kind, which Less guarantees.
func numLess(a, b reflect.Value) bool {
	switch a.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return a.Int() < b.Int()
	case reflect.Float32, reflect.Float64:
		return a.Float() < b.Float()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return a.Uint() < b.Uint()
	case reflect.Bool:
		return !a.Bool() && b.Bool()
	default:
		panic("not a number")
	}
}
