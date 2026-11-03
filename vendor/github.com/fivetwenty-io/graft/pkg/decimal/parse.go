package decimal

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Parse reads a number in JSON's syntax, with a leading plus sign also
// allowed, and holds it exactly as written. It never rounds, and it keeps
// trailing zeros, so 3.0e2 has the coefficient 30 and the exponent 1.
//
// A literal with more than 100,000 significant digits, or whose adjusted
// exponent lies past plus or minus 999,999,999, is over-bound. For one of
// those, Parse returns ErrTooManyDigits or ErrExponentRange together with a
// usable Decimal, the way strconv.ParseFloat returns ErrRange together with
// the nearest double. That Decimal keeps the literal's digits and exponent
// as text but has no coefficient. It prints, compares, changes sign, and
// converts to the nearest double, and its Err method returns the same
// error, which every arithmetic operation on it returns too. Both bounds
// are checked from the text before any coefficient is built. A zero is
// never over-bound, because Parse clamps its exponent into range, as
// decNumber does.
func Parse(s string) (Decimal, error) {
	lit, ok := scan(s)
	if !ok {
		return Decimal{}, fmt.Errorf("invalid number %q", clip(s))
	}
	return lit.decimal(s, defaultLimits)
}

// MustParse is Parse for a literal known to be valid and in range, and it
// panics on any error, an over-bound literal included.
func MustParse(s string) Decimal {
	d, err := Parse(s)
	if err != nil {
		panic(fmt.Sprintf("decimal.MustParse(%q): %v", clip(s), err))
	}
	return d
}

// literal is a number's text split into its parts by scan.
type literal struct {
	neg       bool
	whole     string // the digits before the point
	frac      string // the digits after the point, empty without a point
	expNeg    bool
	expDigits string // the exponent's digits, empty without an exponent
}

// scan splits s by JSON's number grammar, allowing a leading plus sign, and
// reports false when s does not match it.
func scan(s string) (literal, bool) {
	var l literal
	i := 0
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		l.neg = s[i] == '-'
		i++
	}
	j := scanWhole(s, i)
	if j == i {
		return l, false
	}
	l.whole, i = s[i:j], j
	if i < len(s) && s[i] == '.' {
		j = skipDigits(s, i+1)
		if j == i+1 {
			return l, false
		}
		l.frac, i = s[i+1:j], j
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		l.expNeg, l.expDigits, i = scanExponent(s, i+1)
		if l.expDigits == "" {
			return l, false
		}
	}
	return l, i == len(s)
}

// scanWhole returns the end of the whole part starting at i, which is a
// lone 0 or a run of digits that starts with 1 through 9, or i when there
// is none.
func scanWhole(s string, i int) int {
	switch {
	case i >= len(s):
		return i
	case s[i] == '0':
		return i + 1
	case s[i] >= '1' && s[i] <= '9':
		return skipDigits(s, i)
	}
	return i
}

func scanExponent(s string, i int) (bool, string, int) {
	neg := false
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		neg = s[i] == '-'
		i++
	}
	j := skipDigits(s, i)
	return neg, s[i:j], j
}

func skipDigits(s string, i int) int {
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
}

// decimal builds the Decimal a scanned literal spells, checking the bounds
// from the digit counts before it builds any coefficient.
func (l literal) decimal(s string, lim limits) (Decimal, error) {
	whole := strings.TrimLeft(l.whole, "0")
	frac := l.frac
	if whole == "" {
		frac = strings.TrimLeft(frac, "0")
	}
	n := int64(len(whole) + len(frac))
	exp, fits := l.exponent()
	if n == 0 {
		return Decimal{exp: exp32(lim.clampExp(exp)), neg: l.neg}, nil
	}
	if n > lim.maxDigits {
		return l.overBound(s, whole+frac, ErrTooManyDigits), ErrTooManyDigits
	}
	if adj := exp + n - 1; !fits || adj > lim.maxExp || adj < -lim.maxExp {
		return l.overBound(s, whole+frac, ErrExponentRange), ErrExponentRange
	}
	return coefficient(l.neg, whole, frac, n, exp), nil
}

// exponent returns the exponent of the literal's last digit, and reports
// false when the exponent the literal wrote has more than 18 digits, which
// no int64 arithmetic can hold safely. An exponent that does not fit comes
// back saturated with its sign, which is all a zero needs.
func (l literal) exponent() (int64, bool) {
	t := strings.TrimLeft(l.expDigits, "0")
	if len(t) > 18 {
		if l.expNeg {
			return math.MinInt64, false
		}
		return math.MaxInt64, false
	}
	var e int64
	for i := range len(t) {
		e = e*10 + int64(t[i]-'0')
	}
	if l.expNeg {
		e = -e
	}
	return e - int64(len(l.frac)), true
}

// coefficient builds a finite Decimal from n significant digits, which are
// whole followed by frac.
func coefficient(neg bool, whole, frac string, n, exp int64) Decimal {
	if n < int64(len(pow10Uint)) {
		var m uint64
		for _, part := range []string{whole, frac} {
			for i := range len(part) {
				m = m*10 + uint64(part[i]-'0')
			}
		}
		return Decimal{small: m, exp: exp32(exp), neg: neg}
	}
	c, _ := new(big.Int).SetString(whole+frac, 10) // scan accepted every byte as a digit
	if c.IsUint64() {
		return Decimal{small: c.Uint64(), exp: exp32(exp), neg: neg}
	}
	return Decimal{coef: c, nd: n, exp: exp32(exp), neg: neg}
}

// overBound builds the Decimal for a literal past a bound, which keeps its
// significant digits and its exponent as text in place of a coefficient.
func (l literal) overBound(s, digits string, err error) Decimal {
	exp, fits := l.exponent()
	e := wideOf(exp)
	if !fits {
		e = wideInt{neg: l.expNeg, digits: strings.TrimLeft(l.expDigits, "0")}.add(-int64(len(l.frac)))
	}
	// s is well formed, and a literal past float64's range comes back as
	// the infinity or zero we want, with ErrRange.
	approx, _ := strconv.ParseFloat(s, 64)
	return Decimal{
		form: overBound,
		neg:  l.neg,
		over: &overText{err: err, digits: digits, exp: e, approx: math.Abs(approx)},
	}
}

// clip shortens a long input for an error message, so that a malformed
// megabyte of digits does not become a megabyte of error text.
func clip(s string) string {
	const limit = 64
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}
