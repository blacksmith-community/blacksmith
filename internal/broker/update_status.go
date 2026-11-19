package broker

import (
	"context"
	"fmt"

	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// updateStatus answers an update poll from the update the broker
// recorded. The update's own goroutine, or re-adoption after a restart,
// settles it, so a poll never settles an update as a provision and never
// deletes anything.
func (b *Broker) updateStatus(_ context.Context, instanceID, deploymentName string, recorded recordedOperation, logger logger.Logger) (osbapi.LastOperationResponse, error) {
	if recorded.Action != operationTypeUpdate {
		if recorded.Action == "" {
			logger.Error("Instance %s: an update poll found no operation on record, so it changes nothing and leaves deployment %s as it is", instanceID, deploymentName)

			return osbapi.LastOperationResponse{State: osbapi.StateFailed, Description: "No update is on record for this instance; its deployment is left as it is"}, nil
		}

		logger.Warning("Instance %s: an update poll found the %s %s on record, which only its own poll settles, so it changes nothing", instanceID, recorded.Action, recorded.State)

		return osbapi.LastOperationResponse{
			State:       osbapi.StateInProgress,
			Description: fmt.Sprintf("The instance's record holds a %s, not an update", recorded.Action),
		}, nil
	}

	switch recorded.State {
	case internalVault.TaskStateSucceeded:
		return osbapi.LastOperationResponse{State: osbapi.StateSucceeded, Description: recorded.Description}, nil
	case recordedStateFailed, internalVault.TaskStateRollbackFailed:
		// A failed update leaves the deployment where BOSH stopped and the
		// stored values at their last good state, so the instance stays
		// usable and the same update may be sent again. Once a rollback
		// failed as well, or a failure was recorded while the rollback
		// still ran, the broker can no longer vouch for the deployment,
		// and a repeated update deploys a fresh render that can repair it.
		usable := recorded.State == recordedStateFailed && !recorded.RollingBack
		repeatable := true

		return osbapi.LastOperationResponse{
			State: osbapi.StateFailed, Description: recorded.Description,
			InstanceUsable: &usable, UpdateRepeatable: &repeatable,
		}, nil
	case internalVault.TaskStateOutcomeUnknown:
		logger.Debug("Instance %s: the outcome of its update's deploy with context ID %s is unknown", instanceID, recorded.ContextID)

		return osbapi.LastOperationResponse{
			State:       osbapi.StateInProgress,
			Description: "Waiting for the director to show whether it started the deploy with context ID " + recorded.ContextID,
		}, nil
	}

	return osbapi.LastOperationResponse{State: osbapi.StateInProgress, Description: recorded.Description}, nil
}
