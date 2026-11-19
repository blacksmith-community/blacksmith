package vault

import (
	"context"
	"errors"
	"fmt"
	"time"

	"blacksmith/pkg/logger"
)

// TaskRecordUpdateKey is the key of an update's staged values in its task
// record. The staged plan, parameters, and context live only there until
// the update succeeds, and every later write of the same update keeps
// them, so a restart or a lost answer never loses what the tenant asked
// for.
const TaskRecordUpdateKey = "update"

// StagedUpdate is what an accepted update will commit to the instance's
// records once its deploy succeeds.
type StagedUpdate struct {
	// Fingerprint is the digest of the target plan and the merged
	// parameters, so a repeated request finds the same operation.
	Fingerprint string `json:"fingerprint"`
	// PreviousPlanID and PlanID are the plan the instance is on and the
	// plan the update moves it to, which are the same until plan changes
	// are supported.
	PreviousPlanID string `json:"previous_plan_id"`
	PlanID         string `json:"plan_id"`
	// Parameters is the merged parameter set the deploy renders.
	Parameters map[string]interface{} `json:"parameters"`
	// Context is the context the request carried, when it carried one.
	Context map[string]interface{} `json:"context,omitempty"`
	// ChangedKeys names the parameter keys the update adds, changes, or
	// removes, for the audit record. It never holds a value.
	ChangedKeys []string `json:"changed_keys,omitempty"`
	// Actor names the platform user the request's originating identity
	// carries, so the change record of an update that settles after a
	// restart still names who asked for it.
	Actor string `json:"actor,omitempty"`
}

// ErrCleanupOwed is returned when an update would replace the record of a
// failed provision that still owes the delete of its deployment. Replacing
// it would drop the mark and leave the deployment behind.
var ErrCleanupOwed = errors.New("the instance still owes the cleanup of a failed provision")

// ErrUpdateNotAllowed is returned when an update would replace a record
// that holds an operation still running, a deleted instance, or a
// provision that never succeeded.
var ErrUpdateNotAllowed = errors.New("the task record holds an operation an update can't replace")

// ErrInstanceDeleted and ErrInstanceNotReady tell apart two of the
// records an update can't replace, and each comes wrapped together with
// ErrUpdateNotAllowed. The first is a deleted instance or one whose
// deprovision was accepted, and the second is one whose provision hasn't
// succeeded. Any other refusal is an operation still running.
var (
	ErrInstanceDeleted  = errors.New("the instance is deleted or being deleted")
	ErrInstanceNotReady = errors.New("the instance's provision has not succeeded")
)

// UpdateRefusal reports why an update can't replace record, the task
// record of instanceID as it was read, or nil when it can. A nil record
// is an instance with no task record, which takes the update. The update
// handler asks it before anything is written, and TrackUpdateAccepted
// asks it again under the task record lock.
func UpdateRefusal(instanceID string, record map[string]interface{}) error {
	return updateMayReplace(instanceID, record)
}

// TrackUpdateAccepted records an accepted update as the instance's current
// operation, with contextID as the context ID its deploy will carry and
// staged as the values it will commit. The deployment name and the plan
// it was minted under are carried forward from the record it replaces,
// whatever that record's action, so the minted pair never changes on an
// update. When that record names no deployment, deploymentName, the name
// the index records, is written with no plan.
//
// The check and the write happen under the instance's task record lock.
// A record that owes a cleanup stops the write with an error wrapping
// ErrCleanupOwed, an unknown outcome with one wrapping
// ErrOutcomeUnknownRecorded, and a running operation, a deleted instance,
// a deprovision, or a provision that hasn't succeeded with one wrapping
// ErrUpdateNotAllowed. An instance with no record at all, such as one the
// reconciler adopted, takes the update. A record that cannot be read
// stops the write.
func (vault *Vault) TrackUpdateAccepted(ctx context.Context, instanceID, description, contextID, deploymentName string, staged StagedUpdate) error {
	if contextID == "" {
		return fmt.Errorf("%w: instance %s, action update", ErrEmptyContextID, instanceID)
	}

	logger := logger.Get().Named("vault track update accepted " + instanceID)

	var carried map[string]interface{}

	guard := func(current map[string]interface{}) error {
		carried = current

		return updateMayReplace(instanceID, current)
	}

	build := func(map[string]interface{}) interface{} {
		record := progressRecord{
			Action:      actionUpdate,
			Task:        0,
			State:       progressState(0),
			Description: description,
			UpdatedAt:   time.Now().Unix(),
			ContextID:   contextID,
			Update:      staged,
		}

		record.DeploymentName, _ = carried[TaskRecordDeploymentNameKey].(string)
		record.DeploymentPlanID, _ = carried[TaskRecordDeploymentPlanIDKey].(string)

		if record.DeploymentName == "" {
			record.DeploymentName = deploymentName
			record.DeploymentPlanID = ""
		}

		return record
	}

	err := vault.writeTaskRecordGuarded(ctx, instanceID, actionUpdate, logger, guard, build)
	if err != nil {
		return err
	}

	err = vault.appendHistory(ctx, instanceID, actionUpdate, description, progressHistoryExtras(progressState(0), 0))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrHistoryNotAppended, err)
	}

	return nil
}

// actionUpdate is the action an update records.
const actionUpdate = "update"

// updateMayReplace reports why an update can't replace current, or nil
// when it can.
func updateMayReplace(instanceID string, current map[string]interface{}) error {
	if current == nil {
		return nil
	}

	action, _ := current["action"].(string)
	state, _ := current["state"].(string)

	if marked, _ := current[TaskRecordCleanupPendingKey].(bool); marked {
		return fmt.Errorf("%w: instance %s records a failed %s whose deployment is still to be deleted", ErrCleanupOwed, instanceID, action)
	}

	if state == TaskStateOutcomeUnknown {
		contextID, _ := current[taskRecordContextIDKey].(string)

		return fmt.Errorf("%w: instance %s records the %s with context ID %s", ErrOutcomeUnknownRecorded, instanceID, action, contextID)
	}

	switch {
	case action == "deprovision" || state == TaskStateDeleted:
		return fmt.Errorf("%w: %w: instance %s records the %s state of a %s", ErrUpdateNotAllowed, ErrInstanceDeleted, instanceID, state, action)
	case action == "provision" && state != TaskStateSucceeded:
		return fmt.Errorf("%w: %w: instance %s records a provision in the %s state, not one that succeeded", ErrUpdateNotAllowed, ErrInstanceNotReady, instanceID, state)
	case state != TaskStateSucceeded && state != eventStateFailed && state != TaskStateRollbackFailed:
		return fmt.Errorf("%w: instance %s records a %s that is still %s", ErrUpdateNotAllowed, instanceID, action, state)
	}

	return nil
}

// TaskStateRollbackFailed is the state of an update whose deploy failed
// and whose rollback deploy failed too.
const TaskStateRollbackFailed = "rollback_failed"
