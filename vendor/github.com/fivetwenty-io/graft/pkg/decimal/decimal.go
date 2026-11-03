package decimal

import (
	"math"
	"math/big"
	"math/bits"
	"strconv"
)

// form says which kind of number a Decimal holds.
type form uint8

const (
	finite     form = iota // a coefficient and an exponent, which is the zero value's form
	infinite               // positive or negative infinity, by the sign
	notANumber             // NaN, which carries no sign
	overBound              // a literal past a bound, which keeps its digits but has no coefficient
)

// Decimal is an exact base-ten number. A finite Decimal is a sign, a
// coefficient, and an exponent, and its value is the coefficient times ten
// to the exponent. The coefficient keeps the trailing zeros it was written
// or computed with, so 1.50 and 1.5 are equal but print differently under
// Scientific. A Decimal may also be positive or negative infinity, NaN, or
// an over-bound literal, which Parse describes.
//
// A Decimal is immutable, and copying one is cheap. The zero value is 0.
type Decimal struct {
	coef  *big.Int  // the coefficient when it is too large for small, and nil otherwise; never modified once set
	over  *overText // what an over-bound literal keeps, and nil for every other form
	small uint64    // the coefficient when coef is nil
	nd    int64     // the coefficient's digit count when coef is set
	exp   int32     // the exponent of the coefficient's last digit
	form  form
	neg   bool
}

// overText is what an over-bound literal keeps in place of a coefficient.
type overText struct {
	err    error   // ErrTooManyDigits or ErrExponentRange
	digits string  // the significant digits as written, from the first nonzero digit on
	exp    wideInt // the exponent of the last digit, which may lie past any machine integer
	approx float64 // the double nearest to the literal's magnitude
}

var (
	// Zero is 0.
	Zero = Decimal{}
	// NegZero is -0, which equals Zero but keeps its sign through
	// multiplication and division and prints as -0.
	NegZero = Decimal{neg: true}
)

// Inf returns negative infinity when sign is negative and positive
// infinity otherwise.
func Inf(sign int) Decimal {
	return Decimal{form: infinite, neg: sign < 0}
}

// NaN returns a NaN, which no Decimal equals and which sorts below every
// other Decimal.
func NaN() Decimal {
	return Decimal{form: notANumber}
}

// FromInt64 returns i exactly.
func FromInt64(i int64) Decimal {
	if i < 0 {
		return Decimal{small: uint64(-(i + 1)) + 1, neg: true} // #nosec G115 -- -(i+1) is never negative and never overflows
	}
	return Decimal{small: uint64(i)} // #nosec G115 -- i is not negative here
}

// FromFloat64 returns the shortest decimal that reads back as f, which is
// the value strconv.FormatFloat(f, 'e', -1, 64) prints, rather than the
// double's exact binary expansion, so FromFloat64(0.1) is 0.1. NaN and the
// infinities map to their Decimal counterparts, and -0 stays -0.
func FromFloat64(f float64) Decimal {
	switch {
	case math.IsNaN(f):
		return NaN()
	case math.IsInf(f, 0):
		return Inf(int(math.Copysign(1, f)))
	}
	return MustParse(strconv.FormatFloat(f, 'e', -1, 64))
}

// Err returns ErrTooManyDigits or ErrExponentRange for an over-bound
// literal, saying which bound it broke, and nil for every other Decimal.
func (d Decimal) Err() error {
	if d.form == overBound {
		return d.over.err
	}
	return nil
}

// IsZero reports whether d is 0 or -0.
func (d Decimal) IsZero() bool {
	return d.form == finite && d.coef == nil && d.small == 0
}

// IsNegative reports d's sign bit, so it is true for -0 and for negative
// infinity, and false for NaN. Sign tells a negative value from -0.
func (d Decimal) IsNegative() bool {
	return d.neg
}

// IsNaN reports whether d is NaN.
func (d Decimal) IsNaN() bool {
	return d.form == notANumber
}

// IsInf reports whether d is infinite, and its sign as 1 or -1 when it is.
func (d Decimal) IsInf() (bool, int) {
	if d.form != infinite {
		return false, 0
	}
	return true, d.signOf()
}

// IsInt reports whether d is finite and has no fractional part. An
// over-bound literal answers false, because answering needs its value.
func (d Decimal) IsInt() bool {
	switch {
	case d.form != finite:
		return false
	case d.exp >= 0 || d.IsZero():
		return true
	}
	k := -int64(d.exp)
	switch {
	case k >= d.numDigits():
		return false
	case d.coef == nil:
		return d.small%pow10Uint[k] == 0
	}
	return new(big.Int).Rem(d.coef, pow10(k)).Sign() == 0
}

// Sign returns -1 when d is below zero, 1 when it is above zero, and 0 for
// 0, -0, and NaN.
func (d Decimal) Sign() int {
	if d.form == notANumber || d.IsZero() {
		return 0
	}
	return d.signOf()
}

// Int64 returns d as an int64 when d is an integer inside int64's range,
// and reports false otherwise, including for a fractional d, which the
// caller truncates first if it wants the integer part.
func (d Decimal) Int64() (int64, bool) {
	switch {
	case !d.IsInt():
		return 0, false
	case d.IsZero():
		return 0, true
	}
	m, ok := d.integerMagnitude()
	if !ok {
		return 0, false
	}
	switch {
	case !d.neg && m <= math.MaxInt64:
		return int64(m), true // #nosec G115 -- m is at most math.MaxInt64
	case d.neg && m <= 1<<63:
		return -int64(m-1) - 1, true // #nosec G115 -- m-1 is at most math.MaxInt64
	}
	return 0, false
}

// Float64 returns the double nearest to d, which is an infinity past
// float64's range and a signed zero below it. NaN and the infinities map to
// their float64 counterparts, and an over-bound literal returns the double
// nearest to the literal.
func (d Decimal) Float64() float64 {
	if d.form == notANumber {
		return math.NaN()
	}
	f := d.magnitudeFloat()
	if d.neg {
		return -f
	}
	return f
}

func (d Decimal) magnitudeFloat() float64 {
	switch {
	case d.form == infinite:
		return math.Inf(1)
	case d.form == overBound:
		return d.over.approx
	case d.coef == nil && d.small <= 1<<53 && d.exp >= -22 && d.exp <= 22:
		// Both operands are exact doubles, so the one rounding the
		// operation does is the correct one.
		if d.exp >= 0 {
			return float64(d.small) * pow10Float[d.exp]
		}
		return float64(d.small) / pow10Float[-d.exp]
	}
	// The text is always well formed, and a value past float64's range
	// comes back as the infinity or zero we want, with ErrRange.
	f, _ := strconv.ParseFloat(d.layout().digits+"e"+strconv.FormatInt(int64(d.exp), 10), 64)
	return f
}

// Reduce returns d with the trailing zeros stripped from its coefficient,
// which changes how Scientific prints it and nothing else. A zero reduces
// to 0 or -0 with the exponent 0. Infinities, NaN, and over-bound literals
// come back unchanged.
func (d Decimal) Reduce() Decimal {
	switch {
	case d.form != finite:
		return d
	case d.IsZero():
		return Decimal{neg: d.neg}
	case d.coef == nil:
		m, e := d.small, int64(d.exp)
		for m%10 == 0 {
			m /= 10
			e++
		}
		return Decimal{small: m, exp: exp32(e), neg: d.neg}
	case d.coef.Bit(0) == 1:
		return d
	}
	s := d.coef.Text(10)
	t := int64(len(s))
	for s[t-1] == '0' {
		t--
	}
	zeros := int64(len(s)) - t
	if zeros == 0 {
		return d
	}
	return finiteOf(d.neg, new(big.Int).Quo(d.coef, pow10(zeros)), int64(d.exp)+zeros)
}

func (d Decimal) signOf() int {
	if d.neg {
		return -1
	}
	return 1
}

// integerMagnitude returns the magnitude of an integer d that IsInt
// accepted, when it fits a uint64.
func (d Decimal) integerMagnitude() (uint64, bool) {
	k := int64(d.exp)
	switch {
	case k >= 0 && d.coef == nil:
		return scaleUint(d.small, k)
	case k >= 0:
		return 0, false
	case d.coef == nil:
		return d.small / pow10Uint[-k], true
	}
	w := new(big.Int).Quo(d.coef, pow10(-k))
	return w.Uint64(), w.IsUint64()
}

// bigCoef returns the coefficient as a big.Int, which the caller must not
// modify, because it may be d's own.
func (d Decimal) bigCoef() *big.Int {
	if d.coef != nil {
		return d.coef
	}
	return new(big.Int).SetUint64(d.small)
}

// numDigits returns the coefficient's digit count, where 0 has one digit.
func (d Decimal) numDigits() int64 {
	if d.coef != nil {
		return d.nd
	}
	return digitsUint(d.small)
}

// adjusted returns the exponent of the coefficient's first digit, which is
// the exponent the number has in scientific notation.
func (d Decimal) adjusted() int64 {
	return int64(d.exp) + d.numDigits() - 1
}

// scaledCoef returns a new big.Int holding the coefficient times ten to the
// k, which the caller owns.
func (d Decimal) scaledCoef(k int64) *big.Int {
	if k == 0 {
		return new(big.Int).Set(d.bigCoef())
	}
	return new(big.Int).Mul(d.bigCoef(), pow10(k))
}

// finiteOf builds a finite Decimal from a magnitude it takes ownership of
// and an exponent the caller has checked.
func finiteOf(neg bool, c *big.Int, exp int64) Decimal {
	if c.IsUint64() {
		return Decimal{small: c.Uint64(), exp: exp32(exp), neg: neg}
	}
	return Decimal{coef: c, nd: digitsBig(c), exp: exp32(exp), neg: neg}
}

// checked builds the finite result of an operation from a magnitude it
// takes ownership of, failing when the result breaks a bound.
func (l limits) checked(neg bool, c *big.Int, exp int64) (Decimal, error) {
	if c.IsUint64() {
		return l.checkedUint(neg, c.Uint64(), exp)
	}
	nd := digitsBig(c)
	if nd > l.maxDigits {
		return Decimal{}, ErrTooManyDigits
	}
	if err := l.checkExp(exp, nd); err != nil {
		return Decimal{}, err
	}
	return Decimal{coef: c, nd: nd, exp: exp32(exp), neg: neg}, nil
}

func (l limits) checkedUint(neg bool, m uint64, exp int64) (Decimal, error) {
	if m == 0 {
		return Decimal{exp: exp32(l.clampExp(exp)), neg: neg}, nil
	}
	nd := digitsUint(m)
	if nd > l.maxDigits {
		return Decimal{}, ErrTooManyDigits
	}
	if err := l.checkExp(exp, nd); err != nil {
		return Decimal{}, err
	}
	return Decimal{small: m, exp: exp32(exp), neg: neg}, nil
}

// exp32 narrows an exponent its caller has already checked against the
// bounds, which lie well inside int32.
func exp32(e int64) int32 {
	return int32(e) // #nosec G115 -- every caller checks or clamps e first
}

var pow10Uint = func() [20]uint64 {
	var t [20]uint64
	t[0] = 1
	for i := 1; i < len(t); i++ {
		t[i] = t[i-1] * 10
	}
	return t
}()

// pow10Float holds the powers of ten a double represents exactly.
var pow10Float = func() [23]float64 {
	var t [23]float64
	t[0] = 1
	for i := 1; i < len(t); i++ {
		t[i] = t[i-1] * 10
	}
	return t
}()

var (
	bigOne = big.NewInt(1)
	bigTen = big.NewInt(10)
)

var pow10Big = func() [64]*big.Int {
	var t [64]*big.Int
	t[0] = big.NewInt(1)
	for i := 1; i < len(t); i++ {
		t[i] = new(big.Int).Mul(t[i-1], bigTen)
	}
	return t
}()

// pow10 returns ten to the k for k at least zero, which the caller must
// not modify, because small powers are shared.
func pow10(k int64) *big.Int {
	if k < int64(len(pow10Big)) {
		return pow10Big[k]
	}
	return new(big.Int).Exp(bigTen, big.NewInt(k), nil)
}

// scaleUint returns x times ten to the k when the product fits a uint64.
func scaleUint(x uint64, k int64) (uint64, bool) {
	switch {
	case x == 0:
		return 0, true
	case k >= int64(len(pow10Uint)):
		return 0, false
	}
	hi, lo := bits.Mul64(x, pow10Uint[k])
	return lo, hi == 0
}

// digitsUint returns x's decimal digit count, where 0 has one digit.
func digitsUint(x uint64) int64 {
	if x == 0 {
		return 1
	}
	// 1233/4096 is just above log10(2), so t is the digit count or one
	// less.
	t := int64(bits.Len64(x)) * 1233 >> 12
	if x < pow10Uint[t] {
		return t
	}
	return t + 1
}

// digitsBig returns the decimal digit count of a positive x. A number of n
// bits has between (n-1)·log10(2)+1 and n·log10(2)+1 digits, and the two
// bounds differ by at most one, so one comparison settles the count when
// they differ.
func digitsBig(x *big.Int) int64 {
	n := float64(x.BitLen())
	lower := int64((n-1)*math.Log10(2)) + 1
	upper := int64(n*math.Log10(2)) + 1
	if lower == upper || x.Cmp(pow10(lower)) < 0 {
		return lower
	}
	return upper
}
