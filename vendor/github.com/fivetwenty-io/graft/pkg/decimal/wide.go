package decimal

import (
	"strconv"
	"strings"
)

// wideInt is an integer held as a sign and decimal digits, which is how an
// over-bound literal keeps an exponent too long for an int64. Its digits
// have no leading zeros, and zero is the single digit 0 with no sign. Every
// operation on it is linear in its length, so a literal with an exponent of
// millions of digits costs no more to read and print than its text.
type wideInt struct {
	neg    bool
	digits string
}

func wideOf(i int64) wideInt {
	s := strconv.FormatInt(i, 10)
	if rest, cut := strings.CutPrefix(s, "-"); cut {
		return wideInt{neg: true, digits: rest}
	}
	return wideInt{digits: s}
}

// int64 returns w when it has at most 18 digits, which leaves room to add
// any digit count to it without overflow, and reports false otherwise.
func (w wideInt) int64() (int64, bool) {
	if len(w.digits) > 18 {
		return 0, false
	}
	i, _ := strconv.ParseInt(w.digits, 10, 64) // at most 18 digits always fit
	if w.neg {
		i = -i
	}
	return i, true
}

func (w wideInt) String() string {
	if w.neg {
		return "-" + w.digits
	}
	return w.digits
}

// add returns w + n.
func (w wideInt) add(n int64) wideInt {
	v := wideOf(n)
	if w.neg == v.neg {
		return wideInt{neg: w.neg, digits: addDigits(w.digits, v.digits)}
	}
	switch c := cmpDigits(w.digits, v.digits); {
	case c == 0:
		return wideInt{digits: "0"}
	case c > 0:
		return wideInt{neg: w.neg, digits: subDigits(w.digits, v.digits)}
	default:
		return wideInt{neg: v.neg, digits: subDigits(v.digits, w.digits)}
	}
}

// cmp returns -1, 0, or 1 as w is less than, equal to, or greater than v.
func (w wideInt) cmp(v wideInt) int {
	switch {
	case w.neg != v.neg:
		return compare(boolInt(v.neg), boolInt(w.neg))
	case w.neg:
		return cmpDigits(v.digits, w.digits)
	}
	return cmpDigits(w.digits, v.digits)
}

// cmpDigits compares two magnitudes written without leading zeros.
func cmpDigits(a, b string) int {
	if len(a) != len(b) {
		return compare(int64(len(a)), int64(len(b)))
	}
	return strings.Compare(a, b)
}

// addDigits returns the sum of two magnitudes.
func addDigits(a, b string) string {
	if len(a) < len(b) {
		a, b = b, a
	}
	out := make([]byte, len(a)+1)
	carry := byte(0)
	for i := range len(a) {
		s := a[len(a)-1-i] - '0' + carry
		if i < len(b) {
			s += b[len(b)-1-i] - '0'
		}
		carry = s / 10
		out[len(out)-1-i] = '0' + s%10
	}
	if carry == 0 {
		return string(out[1:])
	}
	out[0] = '1'
	return string(out)
}

// subDigits returns a - b for magnitudes with a above b.
func subDigits(a, b string) string {
	out := make([]byte, len(a))
	borrow := byte(0)
	for i := range len(a) {
		d := a[len(a)-1-i] - '0'
		var s byte
		if i < len(b) {
			s = b[len(b)-1-i] - '0'
		}
		s += borrow
		borrow = 0
		if d < s {
			d += 10
			borrow = 1
		}
		out[len(out)-1-i] = '0' + d - s
	}
	return strings.TrimLeft(string(out), "0")
}
