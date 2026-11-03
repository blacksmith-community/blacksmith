package decimal

import (
	"math/big"
	"math/bits"
	"strings"
)

// Add returns d + b exactly. It returns the other operand at once when
// either is zero, so adding zero never scales a coefficient, and otherwise
// it fails with ErrTooManyDigits before it aligns the operands when the
// aligned coefficient would need more significant digits than the bound
// allows. A sum past either bound fails the same way. The sum of two
// zeros is -0 only when both are -0, a NaN operand gives NaN, and the
// infinities follow IEEE 754, so infinity minus infinity is NaN. An
// over-bound operand fails with its Err.
func (d Decimal) Add(b Decimal) (Decimal, error) {
	if err := boundErr(d, b); err != nil {
		return Decimal{}, err
	}
	switch {
	case d.form == notANumber || b.form == notANumber:
		return NaN(), nil
	case d.form == infinite && b.form == infinite && d.neg != b.neg:
		return NaN(), nil
	case d.form == infinite:
		return d, nil
	case b.form == infinite:
		return b, nil
	case d.IsZero() && b.IsZero():
		return Decimal{exp: min(d.exp, b.exp), neg: d.neg && b.neg}, nil
	case b.IsZero():
		return d, nil
	case d.IsZero():
		return b, nil
	}
	return addFinite(d, b, defaultLimits)
}

// Sub returns d - b exactly, under the rules Add describes.
func (d Decimal) Sub(b Decimal) (Decimal, error) {
	return d.Add(b.Neg())
}

func addFinite(a, b Decimal, lim limits) (Decimal, error) {
	e := min(int64(a.exp), int64(b.exp))
	if max(a.adjusted(), b.adjusted())-e+1 > lim.maxDigits {
		return Decimal{}, ErrTooManyDigits
	}
	if m, neg, ok := addSmall(a, b, e); ok {
		return lim.checkedUint(neg, m, e)
	}
	x, y := a.scaledCoef(int64(a.exp)-e), b.scaledCoef(int64(b.exp)-e)
	if a.neg {
		x.Neg(x)
	}
	if b.neg {
		y.Neg(y)
	}
	x.Add(x, y)
	neg := x.Sign() < 0
	return lim.checked(neg, x.Abs(x), e)
}

// addSmall adds two coefficients aligned to the exponent e in uint64
// arithmetic, and reports false when either aligned coefficient or the sum
// does not fit. An exact cancellation gives +0, as IEEE 754 rounds it.
func addSmall(a, b Decimal, e int64) (uint64, bool, bool) {
	if a.coef != nil || b.coef != nil {
		return 0, false, false
	}
	x, okx := scaleUint(a.small, int64(a.exp)-e)
	y, oky := scaleUint(b.small, int64(b.exp)-e)
	switch {
	case !okx || !oky:
		return 0, false, false
	case a.neg == b.neg:
		s, carry := bits.Add64(x, y, 0)
		return s, a.neg, carry == 0
	case x >= y:
		return x - y, a.neg && x != y, true
	}
	return y - x, b.neg, true
}

// Mul returns d × b exactly. It fails with ErrTooManyDigits before it
// multiplies when the operands' digit counts guarantee a product past the
// bound, and with ErrExponentRange when their exponents guarantee one past
// that bound, and it checks the product itself when the counts land on the
// bound. A zero product has the operands' sign rule from IEEE 754, so
// 0 × -1 is -0, a NaN operand gives NaN, and infinity times zero is NaN. An
// over-bound operand fails with its Err.
func (d Decimal) Mul(b Decimal) (Decimal, error) {
	if err := boundErr(d, b); err != nil {
		return Decimal{}, err
	}
	neg := d.neg != b.neg
	switch {
	case d.form == notANumber || b.form == notANumber:
		return NaN(), nil
	case d.form == infinite || b.form == infinite:
		if d.IsZero() || b.IsZero() {
			return NaN(), nil
		}
		return Decimal{form: infinite, neg: neg}, nil
	case d.IsZero() || b.IsZero():
		return Decimal{exp: exp32(defaultLimits.clampExp(int64(d.exp) + int64(b.exp))), neg: neg}, nil
	}
	return mulFinite(d, b, neg, defaultLimits)
}

func mulFinite(a, b Decimal, neg bool, lim limits) (Decimal, error) {
	if a.numDigits()+b.numDigits()-1 > lim.maxDigits {
		return Decimal{}, ErrTooManyDigits
	}
	// A product's adjusted exponent is the sum of the operands' or one
	// more.
	if adj := a.adjusted() + b.adjusted(); adj > lim.maxExp || adj+1 < -lim.maxExp {
		return Decimal{}, ErrExponentRange
	}
	exp := int64(a.exp) + int64(b.exp)
	if a.coef == nil && b.coef == nil {
		if hi, lo := bits.Mul64(a.small, b.small); hi == 0 {
			return lim.checkedUint(neg, lo, exp)
		}
	}
	return lim.checked(neg, new(big.Int).Mul(a.bigCoef(), b.bigCoef()), exp)
}

// Rem returns the remainder of d divided by b, truncated toward zero the
// way jq's % and C's fmod truncate, so the result has d's sign and a
// magnitude below b's, and -5 rem 3 is -2. A zero remainder is +0, as jq
// prints it, so -6 rem 3 and -0 rem 3 are both 0. The remainder is exact
// for fractional operands too. jq truncates its operands to integers before
// it divides, and that step belongs to the caller. A dividend smaller than the
// divisor is its own remainder; otherwise Rem fails with ErrTooManyDigits
// before it aligns the operands when the aligned dividend would pass the
// digit bound. A zero divisor fails with ErrDivisionByZero, a NaN operand
// gives NaN first, an infinite dividend gives NaN, and an infinite divisor
// leaves the dividend as it is. An over-bound operand fails with its Err.
func (d Decimal) Rem(b Decimal) (Decimal, error) {
	if err := boundErr(d, b); err != nil {
		return Decimal{}, err
	}
	switch {
	case d.form == notANumber || b.form == notANumber:
		return NaN(), nil
	case b.IsZero():
		return Decimal{}, ErrDivisionByZero
	case d.form == infinite:
		return NaN(), nil
	case d.IsZero():
		return d.Abs(), nil
	case b.form == infinite:
		return d, nil
	}
	return remFinite(d, b, defaultLimits)
}

func remFinite(a, b Decimal, lim limits) (Decimal, error) {
	if a.adjusted() < b.adjusted() {
		return a, nil
	}
	e := min(int64(a.exp), int64(b.exp))
	if a.adjusted()-e+1 > lim.maxDigits {
		return Decimal{}, ErrTooManyDigits
	}
	if a.coef == nil && b.coef == nil {
		x, okx := scaleUint(a.small, int64(a.exp)-e)
		y, oky := scaleUint(b.small, int64(b.exp)-e)
		if okx && oky {
			r := x % y
			return lim.checkedUint(a.neg && r != 0, r, e)
		}
	}
	x := a.scaledCoef(int64(a.exp) - e)
	x.Rem(x, b.scaledCoef(int64(b.exp)-e))
	return lim.checked(a.neg && x.Sign() != 0, x, e)
}

// Quo returns d ÷ b rounded to c.Precision significant digits under
// c.Rounding. A quotient that terminates within that many digits is exact,
// and its trailing zeros are stripped toward the exponent of d less the
// exponent of b, as decNumber strips them, so 1 ÷ 4 is 0.25. Quo checks
// c's exponent bound from the operands before it divides, and it never
// builds a number much wider than the wider operand or the precision. A
// zero divisor fails with ErrDivisionByZero even when the dividend is NaN,
// as jq fails. Otherwise a NaN operand gives NaN, infinity divided by
// infinity is NaN, and a zero quotient keeps the sign rule IEEE 754 gives
// it. An over-bound operand fails with its Err.
func (d Decimal) Quo(b Decimal, c Context) (Decimal, error) {
	if err := boundErr(d, b); err != nil {
		return Decimal{}, err
	}
	neg := d.neg != b.neg
	lim := c.limits()
	switch {
	case b.IsZero():
		return Decimal{}, ErrDivisionByZero
	case d.form == notANumber || b.form == notANumber:
		return NaN(), nil
	case d.form == infinite && b.form == infinite:
		return NaN(), nil
	case d.form == infinite:
		return Decimal{form: infinite, neg: neg}, nil
	case b.form == infinite:
		return Decimal{neg: neg}, nil
	case d.IsZero():
		return Decimal{exp: exp32(lim.clampExp(int64(d.exp) - int64(b.exp))), neg: neg}, nil
	}
	return quoFinite(d, b, neg, lim)
}

func quoFinite(a, b Decimal, neg bool, lim limits) (Decimal, error) {
	// A quotient's adjusted exponent is the difference of the operands'
	// or one less, and rounding up can add one back.
	if adj := a.adjusted() - b.adjusted(); adj-1 > lim.maxExp || adj < -lim.maxExp {
		return Decimal{}, ErrExponentRange
	}
	// Scale one side so that the integer quotient has precision+1 or
	// precision+2 digits, which leaves at least one digit to round on, and
	// keep the remainder as a sticky bit for everything past it.
	shift := lim.precision + 1 - a.numDigits() + b.numDigits()
	num, den := a.bigCoef(), b.bigCoef()
	if shift >= 0 {
		num = new(big.Int).Mul(num, pow10(shift))
	} else {
		den = new(big.Int).Mul(den, pow10(-shift))
	}
	q, r := new(big.Int).QuoRem(num, den, new(big.Int))
	exp := int64(a.exp) - int64(b.exp) - shift
	q, exp, exact := roundHalfEven(q, r.Sign() != 0, exp, lim.precision)
	if exact {
		q, exp = trimZeros(q, exp, int64(a.exp)-int64(b.exp))
	}
	return lim.checked(neg, q, exp)
}

// roundHalfEven rounds the positive q, whose last digit has the exponent
// exp, to prec digits, where sticky says that a nonzero remainder lies
// past q's last digit. It returns the rounded coefficient, its exponent,
// and whether no nonzero digit was dropped.
func roundHalfEven(q *big.Int, sticky bool, exp, prec int64) (*big.Int, int64, bool) {
	drop := digitsBig(q) - prec
	if drop <= 0 {
		return q, exp, !sticky
	}
	unit := pow10(drop)
	r := new(big.Int)
	q.QuoRem(q, unit, r)
	exp += drop
	exact := r.Sign() == 0 && !sticky
	half := r.Lsh(r, 1).Cmp(unit)
	if half > 0 || half == 0 && (sticky || q.Bit(0) == 1) {
		q.Add(q, bigOne)
		if q.Cmp(pow10(prec)) == 0 {
			q.Quo(q, bigTen)
			exp++
		}
	}
	return q, exp, exact
}

// trimZeros strips trailing zeros from the positive q while its exponent
// stays at or below ideal.
func trimZeros(q *big.Int, exp, ideal int64) (*big.Int, int64) {
	t, r := new(big.Int), new(big.Int)
	for exp < ideal {
		t.QuoRem(q, bigTen, r)
		if r.Sign() != 0 {
			break
		}
		q, t = t, q
		exp++
	}
	return q, exp
}

// Cmp compares d and b as a sort order and returns -1, 0, or 1. Numbers
// compare by value, so 1.50 and 1.5 are equal, and -0 and 0 are equal.
// NaN sorts below everything, which is jq's order: a NaN receiver answers
// -1 against every argument, another NaN included, and a NaN argument
// answers 1 against every other receiver. An over-bound literal compares
// exactly by its digits and exponent; a caller that treats comparison as a
// use of the value checks Err first.
func (d Decimal) Cmp(b Decimal) int {
	switch {
	case d.form == notANumber:
		return -1
	case b.form == notANumber:
		return 1
	}
	ds, bs := d.Sign(), b.Sign()
	switch {
	case ds != bs:
		return compare(int64(ds), int64(bs))
	case ds == 0:
		return 0
	}
	return ds * cmpMagnitude(d, b)
}

// Equal reports whether d and b have the same value. NaN equals nothing,
// itself included, and -0 equals 0.
func (d Decimal) Equal(b Decimal) bool {
	if d.form == notANumber || b.form == notANumber {
		return false
	}
	return d.Cmp(b) == 0
}

// cmpMagnitude compares the magnitudes of two nonzero numbers that are not
// NaN.
func cmpMagnitude(a, b Decimal) int {
	switch {
	case a.form == infinite || b.form == infinite:
		return compare(boolInt(a.form == infinite), boolInt(b.form == infinite))
	case a.form == overBound || b.form == overBound:
		return cmpText(a, b)
	}
	if aa, ba := a.adjusted(), b.adjusted(); aa != ba {
		return compare(aa, ba)
	}
	// Equal adjusted exponents mean the exponents differ by no more than
	// the digit counts do, so aligning stays within the wider operand.
	e := min(int64(a.exp), int64(b.exp))
	if a.coef == nil && b.coef == nil {
		x, okx := scaleUint(a.small, int64(a.exp)-e)
		y, oky := scaleUint(b.small, int64(b.exp)-e)
		if okx && oky {
			return compare(x, y)
		}
	}
	return a.scaledCoef(int64(a.exp) - e).Cmp(b.scaledCoef(int64(b.exp) - e))
}

// cmpText compares magnitudes by their digit text, which is how an
// over-bound literal, which has no coefficient, compares with anything.
func cmpText(a, b Decimal) int {
	la, lb := a.layout().reduced(), b.layout().reduced()
	if c := la.adjustedWide().cmp(lb.adjustedWide()); c != 0 {
		return c
	}
	// Both digit strings start with a nonzero digit and have no trailing
	// zeros, so with equal exponents the text order is the numeric order.
	return strings.Compare(la.digits, lb.digits)
}

// Neg returns d with its sign flipped, so the negation of 0 is -0. NaN
// comes back unchanged.
func (d Decimal) Neg() Decimal {
	if d.form != notANumber {
		d.neg = !d.neg
	}
	return d
}

// Abs returns d with its sign cleared.
func (d Decimal) Abs() Decimal {
	d.neg = false
	return d
}

// integralMode says which way an integral rounding goes.
type integralMode uint8

const (
	toFloor integralMode = iota
	toCeil
	toZero
	toNearest
)

// Floor returns the largest integer not above d. Like Ceil, Trunc, and
// Round, it is exact and unbounded, keeps the sign of a zero result as
// IEEE 754 does, so the floor of -0.5 is -1 and its ceiling is -0, and
// returns infinities, NaN, over-bound literals, and integers unchanged.
func (d Decimal) Floor() Decimal { return d.integral(toFloor) }

// Ceil returns the smallest integer not below d.
func (d Decimal) Ceil() Decimal { return d.integral(toCeil) }

// Trunc returns d with its fractional part dropped.
func (d Decimal) Trunc() Decimal { return d.integral(toZero) }

// Round returns the integer nearest to d, with a tie rounded away from
// zero, as C's round and jq's round break it.
func (d Decimal) Round() Decimal { return d.integral(toNearest) }

func (d Decimal) integral(m integralMode) Decimal {
	switch {
	case d.form != finite || d.exp >= 0:
		return d
	case d.IsZero():
		return Decimal{neg: d.neg}
	}
	k := -int64(d.exp)
	if d.coef == nil && k < int64(len(pow10Uint)) {
		unit := pow10Uint[k]
		w, f := d.small/unit, d.small%unit
		if m.roundsAway(d.neg, f != 0, compare(f, unit-f)) {
			w++
		}
		return Decimal{small: w, neg: d.neg}
	}
	if k > d.numDigits() {
		// The magnitude is below a tenth, so only the direction matters,
		// and ten to the k is never built.
		w := uint64(0)
		if m.roundsAway(d.neg, true, -1) {
			w = 1
		}
		return Decimal{small: w, neg: d.neg}
	}
	unit := pow10(k)
	w, f := new(big.Int).QuoRem(d.bigCoef(), unit, new(big.Int))
	fraction := f.Sign() != 0
	if m.roundsAway(d.neg, fraction, f.Lsh(f, 1).Cmp(unit)) {
		w.Add(w, bigOne)
	}
	return finiteOf(d.neg, w, 0)
}

// roundsAway reports whether rounding a magnitude's integer part moves it
// one away from zero, given the sign, whether a fraction was dropped, and
// how the fraction compares with one half.
func (m integralMode) roundsAway(neg, fraction bool, half int) bool {
	switch m {
	case toFloor:
		return neg && fraction
	case toCeil:
		return !neg && fraction
	case toNearest:
		return half >= 0
	default:
		return false
	}
}

// boundErr returns the bound an over-bound operand broke, so an operation
// that needs the operand's value fails the way the operand did.
func boundErr(a, b Decimal) error {
	if err := a.Err(); err != nil {
		return err
	}
	return b.Err()
}

func compare[T int64 | uint64](x, y T) int {
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
