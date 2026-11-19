package broker

import (
	"fmt"

	"blacksmith/internal/services"
	"blacksmith/pkg/logger"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// refuseInvalidParameters checks params against the parameter schema of
// the plan serviceID and planID name, and returns a 400 InvalidParameters
// that names each parameter and the rule it breaks when they don't match.
// A plan with no published schema accepts any parameters. A plan the
// catalog doesn't hold is left to the caller's own plan check.
func (b *Broker) refuseInvalidParameters(instanceID, serviceID, planID string, params map[string]any, logger logger.Logger) error {
	plan, found := b.Plans[serviceID+"/"+planID]
	if !found {
		return nil
	}

	err := services.ValidateParameters(plan.ParameterSchema(), params)
	if err != nil {
		logger.Warning("Refusing the parameters sent for instance %s on plan %s: %s", instanceID, planID, err)

		return fmt.Errorf("%w: %w", osbapi.ErrInvalidParameters, err)
	}

	return nil
}
