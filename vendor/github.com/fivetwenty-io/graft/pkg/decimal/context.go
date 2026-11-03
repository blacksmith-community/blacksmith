package decimal

import (
	"errors"
	"math"
)

// Rounding names the rule that drops the digits past a Context's precision.
type Rounding uint8

// RoundHalfEven rounds to the nearest value that fits and breaks an exact
// tie toward an even last digit, which is IEEE 754's default rule and
// decNumber's.
const RoundHalfEven Rounding = 0

// Context carries the precision and the rounding rule for division, the one
// operation whose result may not terminate, along with the bounds Quo
// enforces on its quotient. A field that is zero or negative takes
// Context128's value, a Precision above MaxDigits is MaxDigits, and a
// Rounding other than RoundHalfEven rounds half-even, because that is the
// only rule the package implements.
type Context struct {
	// Precision is the number of significant digits a rounded quotient keeps.
	Precision int
	// Rounding is the rule that drops the digits past Precision.
	Rounding Rounding
	// MaxDigits bounds the significant digits of a rounded quotient. Parse,
	// Add, Sub, Mul, and Rem always enforce Context128's digit bound.
	MaxDigits int
	// MaxExponent bounds the adjusted exponent of a quotient, in either
	// direction, and Quo checks it from the operands before it divides.
	MaxExponent int32
}

const (
	defaultPrecision   = 34
	defaultMaxDigits   = 100000
	defaultMaxExponent = 999999999
)

// Context128 is IEEE 754's decimal128 context, 34 significant digits
// rounded half-even, with graft's bounds of 100,000 significant digits and
// an adjusted exponent of plus or minus 999,999,999. Parse, Add, Sub, Mul,
// and Rem always enforce these bounds.
var Context128 = Context{Precision: defaultPrecision, Rounding: RoundHalfEven, MaxDigits: defaultMaxDigits, MaxExponent: defaultMaxExponent}

var (
	// ErrDivisionByZero reports a quotient or a remainder whose divisor is
	// zero.
	ErrDivisionByZero = errors.New("division by zero")
	// ErrTooManyDigits reports an operand or a result with more significant
	// digits than the bound allows. Its text names Context128's bound.
	ErrTooManyDigits = errors.New("number exceeds 100000 significant digits")
	// ErrExponentRange reports an operand or a result whose adjusted
	// exponent lies past the bound. Its text names Context128's bound.
	ErrExponentRange = errors.New("number exponent exceeds 999999999")
)

// limits is a Context resolved for use: every field set, and widened to
// int64 so that bound arithmetic on exponents cannot overflow.
type limits struct {
	precision int64
	maxDigits int64
	maxExp    int64
}

var defaultLimits = limits{precision: defaultPrecision, maxDigits: defaultMaxDigits, maxExp: defaultMaxExponent}

func (c Context) limits() limits {
	l := defaultLimits
	if c.MaxDigits > 0 {
		l.maxDigits = int64(c.MaxDigits)
	}
	if c.MaxExponent > 0 {
		l.maxExp = int64(c.MaxExponent)
	}
	if c.Precision > 0 {
		l.precision = int64(c.Precision)
	}
	l.precision = min(l.precision, l.maxDigits)
	return l
}

// checkExp reports whether a nonzero coefficient of nd digits whose last
// digit has the exponent exp stays inside the exponent bound, and inside
// the int32 a Decimal stores its exponent in.
func (l limits) checkExp(exp, nd int64) error {
	adj := exp + nd - 1
	if adj > l.maxExp || adj < -l.maxExp || exp < math.MinInt32 || exp > math.MaxInt32 {
		return ErrExponentRange
	}
	return nil
}

// clampExp pulls a zero's exponent into range, as decNumber clamps it,
// because a zero needs no coefficient and so never breaks a bound.
func (l limits) clampExp(exp int64) int64 {
	return max(-l.maxExp, min(exp, l.maxExp))
}
