package operators

import (
	"strings"

	graft "github.com/fivetwenty-io/graft/pkg/graft"
)

// ContainsSubOperators checks if an input string contains sub-operator syntax.
func ContainsSubOperators(input string) bool {
	if input == "" {
		return false
	}

	// Quick check for sub-operator characters
	hasParens := strings.Contains(input, "(") || strings.Contains(input, ")")
	hasPipe := strings.Contains(input, "|") && !strings.Contains(input, "||")

	return hasParens || hasPipe
}

// ParseVaultArgs parses vault operator arguments
// This is a simplified version that just returns the args unchanged.
func ParseVaultArgs(args []*graft.Expr) ([]*graft.Expr, bool, error) {
	// Simple implementation - just pass through the arguments
	// More complex parsing with sub-operators is not implemented yet
	return args, false, nil
}
