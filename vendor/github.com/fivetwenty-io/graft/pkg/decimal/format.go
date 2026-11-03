package decimal

import (
	"strconv"
	"strings"
)

// String prints d by graft's rule for a computed result. It strips the
// coefficient's trailing zeros and prints a plain decimal when the
// adjusted exponent is between -6 and 20 inclusive, so every int64 prints
// as digits, and otherwise prints exponent form with a lowercase e, a sign,
// and at least two exponent digits, as in 1e+21 and 1.5e-07. The
// infinities print as Infinity and -Infinity and NaN prints as NaN, which
// an encoder maps to its own spelling.
func (d Decimal) String() string {
	if s, ok := d.specialString(); ok {
		return s
	}
	l := d.layout().reduced()
	if adj, ok := l.adjusted(); ok && adj >= -6 && adj <= 20 {
		return l.plain()
	}
	return l.exponential('e', 2)
}

// Scientific prints d by decNumber's to-scientific-string rule, which is
// how jq prints a literal it has not changed. The coefficient keeps its
// trailing zeros, the number prints plainly when its exponent is zero or
// negative and its adjusted exponent is at least -6, and otherwise it
// prints in exponent form with an uppercase E and a sign, as in 3.0E+2.
func (d Decimal) Scientific() string {
	if s, ok := d.specialString(); ok {
		return s
	}
	l := d.layout()
	if adj, ok := l.adjusted(); ok && l.exp <= 0 && adj >= -6 {
		return l.plain()
	}
	return l.exponential('E', 1)
}

// FloatString prints d by the placement rule of jq's jvp_dtoa_fmt, which
// is how jq 1.8.2 prints the result of double arithmetic, for a value made
// by FromFloat64. It strips trailing zeros, and with n digits left it
// prints exponent form, with a lowercase e, a sign, and at least two
// exponent digits, when the adjusted exponent is -5 or below or is n+15 or
// above, so 1e16 prints as 1e+16, 1.5e16 as 15000000000000000, and 0.00001
// as 1e-05. Otherwise it prints a plain decimal.
func (d Decimal) FloatString() string {
	if s, ok := d.specialString(); ok {
		return s
	}
	l := d.layout().reduced()
	if adj, ok := l.adjusted(); ok && adj >= -4 && adj < int64(len(l.digits))+15 {
		return l.plain()
	}
	return l.exponential('e', 2)
}

func (d Decimal) specialString() (string, bool) {
	switch {
	case d.form == notANumber:
		return "NaN", true
	case d.form == infinite && d.neg:
		return "-Infinity", true
	case d.form == infinite:
		return "Infinity", true
	}
	return "", false
}

// layout is a number's text before the printing rules place a point in it:
// a sign, the coefficient's digits, and the exponent of the last digit.
type layout struct {
	neg    bool
	digits string
	exp    int64
	wide   *wideInt // the exponent instead of exp, for an over-bound literal whose exponent has more than 18 digits
}

func (d Decimal) layout() layout {
	switch {
	case d.form == overBound:
		return d.over.layout(d.neg)
	case d.coef != nil:
		return layout{neg: d.neg, digits: d.coef.Text(10), exp: int64(d.exp)}
	}
	return layout{neg: d.neg, digits: strconv.FormatUint(d.small, 10), exp: int64(d.exp)}
}

func (o *overText) layout(neg bool) layout {
	if e, ok := o.exp.int64(); ok {
		return layout{neg: neg, digits: o.digits, exp: e}
	}
	return layout{neg: neg, digits: o.digits, wide: &o.exp}
}

// reduced strips the trailing zeros from the digits, and leaves a zero as
// the single digit 0 with the exponent 0.
func (l layout) reduced() layout {
	t := strings.TrimRight(l.digits, "0")
	if t == "" {
		return layout{neg: l.neg, digits: "0"}
	}
	n := int64(len(l.digits) - len(t))
	l.digits = t
	if l.wide != nil {
		w := l.wide.add(n)
		l.wide = &w
	} else {
		l.exp += n
	}
	return l
}

// adjusted returns the exponent of the first digit, and reports false when
// the exponent lies past an int64, where only exponent form applies.
func (l layout) adjusted() (int64, bool) {
	if l.wide != nil {
		return 0, false
	}
	return l.exp + int64(len(l.digits)) - 1, true
}

// adjustedWide returns the exponent of the first digit at any size.
func (l layout) adjustedWide() wideInt {
	if l.wide == nil {
		return wideOf(l.exp + int64(len(l.digits)) - 1)
	}
	return l.wide.add(int64(len(l.digits)) - 1)
}

// plain prints the digits with the point placed by the exponent, padding
// with zeros on either side. The printing rules call it only for exponents
// that need a handful of zeros at most.
func (l layout) plain() string {
	var b strings.Builder
	if l.neg {
		b.WriteByte('-')
	}
	point := int64(len(l.digits)) + l.exp // the number of digits before the point
	switch {
	case l.exp >= 0:
		b.WriteString(l.digits)
		for range l.exp {
			b.WriteByte('0')
		}
	case point > 0:
		b.WriteString(l.digits[:point])
		b.WriteByte('.')
		b.WriteString(l.digits[point:])
	default:
		b.WriteString("0.")
		for range -point {
			b.WriteByte('0')
		}
		b.WriteString(l.digits)
	}
	return b.String()
}

// exponential prints one digit, the rest after a point, the marker, the
// adjusted exponent's sign, and its digits padded to minDigits.
func (l layout) exponential(marker byte, minDigits int) string {
	var b strings.Builder
	if l.neg {
		b.WriteByte('-')
	}
	b.WriteByte(l.digits[0])
	if len(l.digits) > 1 {
		b.WriteByte('.')
		b.WriteString(l.digits[1:])
	}
	b.WriteByte(marker)
	adj := l.adjustedWide()
	if adj.neg {
		b.WriteByte('-')
	} else {
		b.WriteByte('+')
	}
	for range minDigits - len(adj.digits) {
		b.WriteByte('0')
	}
	b.WriteString(adj.digits)
	return b.String()
}
