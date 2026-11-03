package operators

import (
	"crypto/rand"
	"fmt"
	"math/big"

	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// ShuffleOperator randomly shuffles list elements.
type ShuffleOperator struct{}

// Setup initializes the operator.
func (ShuffleOperator) Setup() error {
	return nil
}

// Phase returns which phase this operator should run in.
func (ShuffleOperator) Phase() OperatorPhase {
	return EvalPhase
}

// Dependencies returns what keys the operator depends on.
func (ShuffleOperator) Dependencies(_ *Evaluator, _ []*Expr, _ []*tree.Cursor, auto []*tree.Cursor) []*tree.Cursor {
	return auto
}

// Run executes the shuffle operator.
func (ShuffleOperator) Run(ev *Evaluator, args []*Expr) (*Response, error) {
	DEBUG("running (( shuffle ... )) operation at $.%s", ev.Here)
	defer DEBUG("done with (( shuffle ... )) operation at $.%s\n", ev.Here)

	var vals []interface{}

	for i, arg := range args {
		// Use ResolveOperatorArgument to support nested expressions
		val, err := ResolveOperatorArgument(ev, arg)
		if err != nil {
			DEBUG("     [%d]: resolution failed\n    error: %s", i, err)
			return nil, err
		}

		if val == nil {
			DEBUG("  arg[%d]: resolved to nil", i)
			return nil, fmt.Errorf("shuffle operator argument cannot be nil")
		}

		switch v := val.(type) {
		case []interface{}:
			DEBUG("  arg[%d]: found list value with %d elements", i, len(v))
			vals = append(vals, v...)

		case map[string]interface{}:
			DEBUG("     [%d]: resolved to a map; error!", i)
			return nil, fmt.Errorf("shuffle only accepts arrays and scalar values")

		default:
			DEBUG("  arg[%d]: found scalar value '%v'", i, val)
			vals = append(vals, val)
		}
		DEBUG("")
	}

	if len(vals) == 0 {
		DEBUG("  no elements to shuffle, returning empty list")
		return &Response{
			Type:  Replace,
			Value: []interface{}{},
		}, nil
	}

	return &Response{
		Type:  Replace,
		Value: shuffle(vals),
	}, nil
}

// shuffle randomly shuffles the elements of a list using crypto/rand for security.
func shuffle(l []interface{}) []interface{} {
	n := len(l)
	if n <= 1 {
		return l
	}

	// Fisher-Yates shuffle using crypto/rand
	for i := n - 1; i > 0; i-- {
		// Generate a random index from 0 to i (inclusive)
		maxRand := big.NewInt(int64(i + 1))
		jBig, err := rand.Int(rand.Reader, maxRand)
		if err != nil {
			// If crypto/rand fails, fall back to a simple swap
			// This should rarely happen
			DEBUG("crypto/rand failed, using fallback: %v", err)
			j := i / 2
			l[i], l[j] = l[j], l[i]
			continue
		}
		j := int(jBig.Int64())
		l[i], l[j] = l[j], l[i]
	}

	return l
}

//nolint:gochecknoinits // Operator registration must happen at package load time
func init() {
	RegisterOp("shuffle", ShuffleOperator{})
}
