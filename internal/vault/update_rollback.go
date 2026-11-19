package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"blacksmith/pkg/logger"
)

// The phases an update's rollback record passes through.
const (
	// UpdatePhaseRollingBack is the phase of an update whose deploy failed
	// while the broker redeploys the previous manifest.
	UpdatePhaseRollingBack = "rolling_back"
	// UpdatePhaseRolledBack is the phase of an update whose rollback
	// restored the previous deployment.
	UpdatePhaseRolledBack = "rolled_back"
	// UpdatePhaseRollbackFailed is the phase of an update whose rollback
	// failed too.
	UpdatePhaseRollbackFailed = TaskStateRollbackFailed
)

// UpdateRollback is what the broker did when an update's deploy failed.
// It lives in the staged update, so every later write of the update keeps
// it, and it names no manifest content.
type UpdateRollback struct {
	// Setting and SettingSource are the update_rollback setting, and where
	// it came from, at the moment the deploy failed.
	Setting       bool   `json:"setting"`
	SettingSource string `json:"setting_source"`
	// FailedTaskID and FailedContextID name the update's deploy that
	// failed.
	FailedTaskID    int    `json:"failed_task_id"`
	FailedContextID string `json:"failed_context_id"`
	// Phase is empty when no rollback ran, and otherwise one of the
	// UpdatePhase values.
	Phase string `json:"phase,omitempty"`
	// ContextID, ManifestDigest, and TaskID describe the rollback deploy:
	// the context ID it carries, the SHA-256 digest of the manifest it
	// redeploys, and the task it started, once known.
	ContextID      string `json:"context_id,omitempty"`
	ManifestDigest string `json:"manifest_digest,omitempty"`
	TaskID         int    `json:"task_id,omitempty"`
}

// ErrUpdateNotInFlight is returned when a rollback write finds that the
// record no longer holds the update's deploy in flight.
var ErrUpdateNotInFlight = errors.New("the task record no longer holds the update in flight")

// TrackRollingBack records that the update whose deploy with
// rollback.FailedContextID failed now redeploys the previous manifest
// under rollback.ContextID. The record's context ID becomes the
// rollback's, so a lost answer is resolved by it, and the action stays
// update. It writes only while the record holds that update unsettled,
// and returns an error that wraps ErrUpdateNotInFlight otherwise.
func (vault *Vault) TrackRollingBack(ctx context.Context, instanceID, description string, rollback UpdateRollback) error {
	if rollback.ContextID == "" {
		return fmt.Errorf("%w: instance %s, rollback of an update", ErrEmptyContextID, instanceID)
	}

	rollback.Phase = UpdatePhaseRollingBack

	return vault.writeUpdateRollback(ctx, instanceID, description, rollback.FailedContextID, progressState(0), 0, rollback, false)
}

// StagedRollback returns the rollback the staged update in a task record
// names, or nil when it names none.
func StagedRollback(record map[string]interface{}) *UpdateRollback {
	staged, _ := record[TaskRecordUpdateKey].(map[string]interface{})
	if staged["rollback"] == nil {
		return nil
	}

	encoded, err := json.Marshal(staged["rollback"])
	if err != nil {
		return nil
	}

	var rollback UpdateRollback

	err = json.Unmarshal(encoded, &rollback)
	if err != nil {
		return nil
	}

	return &rollback
}

// SettleUpdateFailure records the end of an update whose deploy failed,
// with rollback saying what the broker did about it. state is failed, or
// rollback_failed when the rollback failed too. contextID is the context
// ID on record, which is the rollback's when one ran. It writes only
// while the record holds that update unsettled, with its outcome unknown,
// or failed by the resolver of a lost answer, all under the same context
// ID, and returns an error that wraps ErrUpdateNotInFlight otherwise.
func (vault *Vault) SettleUpdateFailure(ctx context.Context, instanceID, state, description, contextID string, rollback UpdateRollback) error {
	if state != eventStateFailed && state != TaskStateRollbackFailed {
		return fmt.Errorf("%w: instance %s, an update cannot settle as %q", ErrUpdateNotInFlight, instanceID, state)
	}

	return vault.writeUpdateRollback(ctx, instanceID, description, contextID, state, -1, rollback, true)
}

// writeUpdateRollback writes the update's record in state with taskID and
// rollback, keeping every other field, while the record holds the update
// with contextID unsettled. The write that starts a rollback also needs
// the record to name the failed deploy's task. A settling write may also
// replace an unknown outcome, or the failure the resolver of a lost
// answer recorded, under the same context ID.
func (vault *Vault) writeUpdateRollback(ctx context.Context, instanceID, description, contextID, state string, taskID int, rollback UpdateRollback, settling bool) error {
	logger := logger.Get().Named("vault track update rollback " + instanceID)

	guard := func(current map[string]interface{}) error {
		action, _ := current["action"].(string)
		recordedContextID, _ := current[taskRecordContextIDKey].(string)
		recordedState, _ := current["state"].(string)
		recordedTask, _ := recordedTaskID(current)

		inFlight := recordedState == progressState(0) || recordedState == progressState(1)
		if settling {
			inFlight = inFlight || recordedState == eventStateFailed || recordedState == TaskStateOutcomeUnknown
		} else {
			inFlight = inFlight && recordedTask == rollback.FailedTaskID
		}

		if action != actionUpdate || contextID == "" || recordedContextID != contextID || !inFlight {
			return fmt.Errorf("%w: instance %s records the %s state of a %q with context ID %q, not the update with context ID %q",
				ErrUpdateNotInFlight, instanceID, recordedState, action, recordedContextID, contextID)
		}

		return nil
	}

	build := func(replaced map[string]interface{}) interface{} {
		record := maps.Clone(replaced)
		record["state"] = state
		record["task"] = taskID
		record["description"] = description
		record["updated_at"] = time.Now().Unix()

		if rollback.Phase == UpdatePhaseRollingBack {
			record[taskRecordContextIDKey] = rollback.ContextID
		}

		delete(record, TaskRecordUnknownSinceKey)

		staged, _ := replaced[TaskRecordUpdateKey].(map[string]interface{})
		staged = maps.Clone(staged)

		if staged == nil {
			staged = map[string]interface{}{}
		}

		staged["rollback"] = rollback
		record[TaskRecordUpdateKey] = staged

		return record
	}

	err := vault.writeTaskRecordGuarded(ctx, instanceID, actionUpdate, logger, guard, build)
	if err != nil {
		return err
	}

	err = vault.appendHistory(ctx, instanceID, actionUpdate, description, progressHistoryExtras(state, taskID))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrHistoryNotAppended, err)
	}

	return nil
}
