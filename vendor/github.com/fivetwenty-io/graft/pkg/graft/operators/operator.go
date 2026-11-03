package operators

import (
	"github.com/fivetwenty-io/graft/pkg/graft"
)

// RegisterOp registers an operator in the DefaultRegistry.
func RegisterOp(name string, op graft.Operator) {
	if err := graft.RegisterUnifiedOperator(name, op); err != nil {
		graft.DEBUG("Warning: Failed to register %s in unified registry: %v", name, err)
	}
}

// SetupOperators initializes all operators for a given phase using the DefaultRegistry.
func SetupOperators(phase graft.OperatorPhase) error {
	entries := graft.DefaultRegistry.GetByPhase(phase)
	errors := graft.MultiError{Errors: []error{}}
	for _, entry := range entries {
		if err := entry.Implementation.Setup(); err != nil {
			errors.Append(err)
		}
	}
	if len(errors.Errors) > 0 {
		return errors
	}
	return nil
}
