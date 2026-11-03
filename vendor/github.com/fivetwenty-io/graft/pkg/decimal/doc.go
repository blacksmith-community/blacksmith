/*
Package decimal provides exact base-ten numbers, which is the number model
behind graft jq and graft yq.

A Decimal holds a number exactly as it was written or computed, so 0.1 plus
0.2 is 0.3, 100000000000000000000 plus 1 is 100000000000000000001, and
9007199254740993 minus 9007199254740992 is 1. Parsing never rounds.
Addition, subtraction, multiplication, the remainder, comparison, and the
integral roundings are exact. Division is the one operation whose result
may not terminate, so Quo rounds its quotient to a Context's precision, and
Context128 is IEEE 754's decimal128 context of 34 significant digits
rounded half-even. FromFloat64 brings in the result of a float64
computation as the shortest decimal that reads back to the same double.

# Bounds

Exact numbers have a cost, and a literal such as 1e999999999 is cheap to
write while a product of two of them could otherwise allocate without
limit. A Decimal therefore holds at most 100,000 significant digits and an
adjusted exponent of plus or minus 999,999,999. Every operation checks the
bounds from its operands' digit counts and exponents before it allocates,
so a rejected operation costs nothing but the check, and the errors are
ErrTooManyDigits and ErrExponentRange.

The bounds apply to the math and never to the text. Parse returns a literal
past a bound as an over-bound Decimal along with the error. That Decimal
keeps the literal's digits, prints and compares as written, and converts to
the nearest double, while every arithmetic operation on it fails with the
error its Err method returns.

# Printing

String prints a computed result. It strips trailing zeros and prints plain
digits for an adjusted exponent from -6 to 20, so every int64 prints as
digits, and exponent form beyond that. Scientific prints decNumber's
canonical form of an unchanged literal, as jq prints one, so 3.0e2 prints
as 3.0E+2. FloatString prints with the placement jq 1.8.2 gives the result
of double arithmetic.

# Stability

This package is stable. The module's v1 promise applies to it, which is
that we add exported names and never remove or change one. The package
imports nothing outside the standard library.
*/
package decimal
