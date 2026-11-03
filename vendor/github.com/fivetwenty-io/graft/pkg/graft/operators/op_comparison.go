package operators

import (
	"fmt"
	"reflect"

	"github.com/fivetwenty-io/graft/pkg/graft"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// ComparisonOperator implements comparison operations (==, !=, <, >, <=, >=)
// This is the basic implementation without type handlers for backward compatibility.
type ComparisonOperator struct {
	op string
}

// Setup initializes the operator.
func (c ComparisonOperator) Setup() error {
	return nil
}

// Phase returns the operator phase.
func (c ComparisonOperator) Phase() graft.OperatorPhase {
	return graft.EvalPhase
}

// Dependencies returns operator dependencies.
func (c ComparisonOperator) Dependencies(_ *graft.Evaluator, args []*graft.Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	deps := make([]*tree.Cursor, 0)
	for _, arg := range args {
		if arg.Type == graft.Reference && arg.Reference != nil {
			deps = append(deps, arg.Reference)
		}
	}
	return append(auto, deps...)
}

// Run executes the comparison.
func (c ComparisonOperator) Run(ev *graft.Evaluator, args []*graft.Expr) (*graft.Response, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("%s operator requires exactly 2 arguments, got %d", c.op, len(args))
	}

	// Evaluate arguments
	vals, err := EvaluateOperatorArgs(ev, args)
	if err != nil {
		return nil, err
	}

	left := vals[0]
	right := vals[1]

	var result bool
	switch c.op {
	case "==":
		result = c.equal(left, right)
	case "!=":
		result = !c.equal(left, right)
	case "<", ">", "<=", ">=":
		cmp, err := c.compare(left, right)
		if err != nil {
			return nil, fmt.Errorf("cannot compare %v and %v: %w", left, right, err)
		}
		switch c.op {
		case "<":
			result = cmp < 0
		case ">":
			result = cmp > 0
		case "<=":
			result = cmp <= 0
		case ">=":
			result = cmp >= 0
		}
	default:
		return nil, fmt.Errorf("unknown comparison operator: %s", c.op)
	}

	return &graft.Response{
		Type:  graft.Replace,
		Value: result,
	}, nil
}

// equal performs deep equality comparison.
func (c ComparisonOperator) equal(a, b interface{}) bool {
	// Handle nil cases
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	// Try numeric comparison first
	aNum, aIsNum := toFloat64(a)
	bNum, bIsNum := toFloat64(b)
	if aIsNum && bIsNum {
		return aNum == bNum
	}

	// Use reflect.DeepEqual for other types
	return reflect.DeepEqual(a, b)
}

// compare performs ordering comparison.
//
//nolint:gocyclo,dupl // comparison logic with type coercion is inherently complex
func (c ComparisonOperator) compare(a, b interface{}) (int, error) {
	// Handle nil cases
	if a == nil && b == nil {
		return 0, nil
	}
	if a == nil {
		return -1, nil // nil is less than any value
	}
	if b == nil {
		return 1, nil // any value is greater than nil
	}

	// Try numeric comparison
	aNum, aIsNum := toFloat64(a)
	bNum, bIsNum := toFloat64(b)
	if aIsNum && bIsNum {
		if aNum < bNum {
			return -1, nil
		} else if aNum > bNum {
			return 1, nil
		}
		return 0, nil
	}

	// Try string comparison
	aStr, aIsStr := a.(string)
	bStr, bIsStr := b.(string)
	if aIsStr && bIsStr {
		if aStr < bStr {
			return -1, nil
		} else if aStr > bStr {
			return 1, nil
		}
		return 0, nil
	}

	// If types don't match, convert to strings
	if aIsNum && bIsStr {
		aStr = fmt.Sprintf("%v", a)
		if aStr < bStr {
			return -1, nil
		} else if aStr > bStr {
			return 1, nil
		}
		return 0, nil
	}
	if aIsStr && bIsNum {
		bStr = fmt.Sprintf("%v", b)
		if aStr < bStr {
			return -1, nil
		} else if aStr > bStr {
			return 1, nil
		}
		return 0, nil
	}

	// Can't compare other types
	return 0, fmt.Errorf("cannot compare %T and %T", a, b)
}

// toFloat64 attempts to convert a value to float64.
func toFloat64(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case int64:
		return float64(val), true
	case int:
		return float64(val), true
	case float32:
		return float64(val), true
	case int32:
		return float64(val), true
	case int16:
		return float64(val), true
	case int8:
		return float64(val), true
	case uint:
		return float64(val), true
	case uint64:
		return float64(val), true
	case uint32:
		return float64(val), true
	case uint16:
		return float64(val), true
	case uint8:
		return float64(val), true
	}
	return 0, false
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	// Register comparison operators
	// Use type-aware comparison operators
	RegisterOp("==", NewTypeAwareEqualOperator())
	RegisterOp("!=", NewTypeAwareNotEqualOperator())
	RegisterOp("<", NewTypeAwareLessOperator())
	RegisterOp(">", NewTypeAwareGreaterOperator())
	RegisterOp("<=", NewTypeAwareLessOrEqualOperator())
	RegisterOp(">=", NewTypeAwareGreaterOrEqualOperator())
}
