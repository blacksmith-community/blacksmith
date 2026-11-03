package value

import "github.com/fivetwenty-io/graft/pkg/decimal"

// CanonicalLiteral returns lit in decNumber's to-scientific-string form,
// which is how jq prints a literal it has not changed. The coefficient keeps
// its digits as written, so 3.0e2 becomes 3.0E+2 and 1e1000 becomes
// 1E+1000. Text that is not a number comes back unchanged.
func CanonicalLiteral(lit string) string {
	d, err := decimal.Parse(lit)
	if err != nil && d.Err() == nil {
		return lit
	}
	return d.Scientific()
}
