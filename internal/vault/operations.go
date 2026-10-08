package vault

import (
	"context"
	"errors"
	"fmt"
	"time"

	"blacksmith/pkg/logger"
	vaultPkg "blacksmith/pkg/vault"
)

const (
	eventStateFailed = "failed"
)

// ErrEmptyContextID is returned when a context ID to record is empty.
var ErrEmptyContextID = errors.New("the context ID to record is empty")

// taskRecordContextIDKey is the key of the context ID in a task record.
const taskRecordContextIDKey = "context_id"

// TaskRecordDeploymentNameKey is the key of the name of the deployment a
// task record's director request acts on. A deprovision records it with
// the context ID of its delete.
const TaskRecordDeploymentNameKey = "deployment_name"

// TaskStateOutcomeUnknown is the state of a task record whose director
// request was sent and whose answer was lost, so whether the director
// started a task for it is unknown.
const TaskStateOutcomeUnknown = "outcome_unknown"

// TaskRecordUnknownSinceKey is the key of the time a task record's outcome
// became unknown, in RFC 3339 form with nanoseconds, in UTC.
const TaskRecordUnknownSinceKey = "unknown_since"

// Track stores tracking information for a task (deprecated, kept for backward compatibility).
func (vault *Vault) Track(ctx context.Context, instanceID, action string, taskID int, params interface{}) error {
	logger := logger.Get().Named("vault track " + instanceID)
	logger.Debug("tracking action '%s', task %d", action, taskID)

	// Note: We're deprecating storing task IDs in vault
	// This function is kept for backward compatibility but will be removed
	// Task IDs are now retrieved from BOSH deployment events

	unlock, err := vault.taskLocks.lock(ctx, instanceID)
	if err != nil {
		return err
	}
	defer unlock()

	return vault.Put(ctx, instanceID+"/task", trackedTask(action, params))
}

// trackedTaskRecord is the minimal record Track writes for audit purposes.
type trackedTaskRecord struct {
	Action      string      `json:"action"`
	State       string      `json:"state"`
	Description string      `json:"description"`
	UpdatedAt   int64       `json:"updated_at"`
	Params      interface{} `json:"params"`
}

func trackedTask(action string, params interface{}) trackedTaskRecord {
	return trackedTaskRecord{
		Action:      action,
		State:       "in_progress",
		Description: fmt.Sprintf("Operation %s in progress", action),
		UpdatedAt:   time.Now().Unix(),
		Params:      deinterface(params),
	}
}

// TrackProgress stores progress information for a task. When the record it
// replaces is for the same action, the context ID and the deployment name
// that record carries are kept, so progress written after the operation
// recorded them does not drop them.
func (vault *Vault) TrackProgress(ctx context.Context, instanceID, action, description string, taskID int, params interface{}) error {
	return vault.trackProgress(ctx, instanceID, action, description, "", "", taskID, params)
}

// ErrOutcomeUnknownRecorded is returned when a write would replace a task
// record whose operation's outcome is unknown. Only the write that settles
// that outcome may replace it.
var ErrOutcomeUnknownRecorded = errors.New("the task record holds an operation whose outcome is unknown")

// TrackProgressUnlessOutcomeUnknown stores progress information as
// TrackProgress does, unless the current record, for any action, has the
// state outcome_unknown. Then it writes nothing and returns an error that
// wraps ErrOutcomeUnknownRecorded and names the record's action and context
// ID. The check and the write happen under the instance's task record
// lock, and a current record that cannot be read stops the write too.
func (vault *Vault) TrackProgressUnlessOutcomeUnknown(ctx context.Context, instanceID, action, description string, taskID int, params interface{}) error {
	return vault.trackProgressGuarded(ctx, instanceID, action, description, "", "", taskID, params, unlessOutcomeUnknown(instanceID, "", ""))
}

// TrackProgressReplacingOutcomeUnknown stores progress information as
// TrackProgressUnlessOutcomeUnknown does, except that it may also replace
// the unknown outcome of replacedAction with replacedContextID. Any other
// unknown outcome stops the write with an error that wraps
// ErrOutcomeUnknownRecorded. An empty replacedContextID replaces no
// unknown outcome.
func (vault *Vault) TrackProgressReplacingOutcomeUnknown(ctx context.Context, instanceID, action, description, replacedAction, replacedContextID string, taskID int, params interface{}) error {
	return vault.trackProgressGuarded(ctx, instanceID, action, description, "", "", taskID, params, unlessOutcomeUnknown(instanceID, replacedAction, replacedContextID))
}

// unlessOutcomeUnknown returns a guard that refuses a write over a record
// whose outcome is unknown, unless that outcome is the one of
// replacedAction with replacedContextID and replacedContextID is not
// empty.
func unlessOutcomeUnknown(instanceID, replacedAction, replacedContextID string) func(current map[string]interface{}) error {
	return func(current map[string]interface{}) error {
		state, _ := current["state"].(string)
		if state != TaskStateOutcomeUnknown {
			return nil
		}

		recordedAction, _ := current["action"].(string)
		contextID, _ := current[taskRecordContextIDKey].(string)

		if replacedContextID != "" && recordedAction == replacedAction && contextID == replacedContextID {
			return nil
		}

		return fmt.Errorf("%w: instance %s, %s with context ID %s", ErrOutcomeUnknownRecorded, instanceID, recordedAction, contextID)
	}
}

// ErrOutcomeUnknownSettled is returned when a write that settles an unknown
// outcome finds that the record no longer holds that outcome, because it is
// gone, holds another state, or names another action or context ID.
var ErrOutcomeUnknownSettled = errors.New("the task record no longer holds the unknown outcome to settle")

// settlingOutcomeUnknown returns a guard that lets a write through only when
// the current record holds the unknown outcome of action with contextID.
func settlingOutcomeUnknown(instanceID, action, contextID string) func(current map[string]interface{}) error {
	return func(current map[string]interface{}) error {
		if current == nil {
			return fmt.Errorf("%w: instance %s has no task record", ErrOutcomeUnknownSettled, instanceID)
		}

		state, _ := current["state"].(string)
		recordedAction, _ := current["action"].(string)
		recordedContextID, _ := current[taskRecordContextIDKey].(string)

		if state != TaskStateOutcomeUnknown || recordedAction != action || recordedContextID != contextID {
			return fmt.Errorf("%w: instance %s records the %s state of a %s with context ID %s, not the unknown outcome of the %s with context ID %s",
				ErrOutcomeUnknownSettled, instanceID, state, recordedAction, recordedContextID, action, contextID)
		}

		return nil
	}
}

// SettleOutcomeUnknown records taskID, a task the director started, or the
// failure -1, as the outcome of the operation whose unknown outcome the
// record holds. It writes only when the record holds the unknown outcome of
// action with contextID, checked under the instance's task record lock, and
// otherwise writes nothing and returns an error that wraps
// ErrOutcomeUnknownSettled. A record that cannot be read stops the write.
func (vault *Vault) SettleOutcomeUnknown(ctx context.Context, instanceID, action, description, contextID string, taskID int) error {
	if contextID == "" {
		return fmt.Errorf("%w: instance %s, action %s, settled outcome", ErrEmptyContextID, instanceID, action)
	}

	if taskID == 0 {
		return fmt.Errorf("%w: instance %s, a write with no task would keep the outcome unknown", ErrOutcomeUnknownSettled, instanceID)
	}

	return vault.trackProgressGuarded(ctx, instanceID, action, description, contextID, "", taskID, nil, settlingOutcomeUnknown(instanceID, action, contextID))
}

// SettleOutcomeUnknownAsDeleted records the deprovision whose unknown
// outcome the record holds as finished, as Track does, once the director
// has said the deployment does not exist. The record it writes has no
// context ID. It writes only under the same check as SettleOutcomeUnknown.
func (vault *Vault) SettleOutcomeUnknownAsDeleted(ctx context.Context, instanceID, action, contextID string) error {
	if contextID == "" {
		return fmt.Errorf("%w: instance %s, action %s, settled delete", ErrEmptyContextID, instanceID, action)
	}

	logger := logger.Get().Named("vault settle deleted " + instanceID)

	return vault.writeTaskRecordGuarded(ctx, instanceID, action, logger, settlingOutcomeUnknown(instanceID, action, contextID), func(map[string]interface{}) interface{} {
		return trackedTask(action, nil)
	})
}

// TrackProgressWithContextID stores progress information for a task as
// TrackProgress does, and records contextID as the context ID of the
// operation in place of any the replaced record carries. The broker calls
// it before it sends the request that carries the context ID, so a restart
// at any point leaves the ID on record.
func (vault *Vault) TrackProgressWithContextID(ctx context.Context, instanceID, action, description, contextID string, taskID int, params interface{}) error {
	if contextID == "" {
		return fmt.Errorf("%w: instance %s, action %s", ErrEmptyContextID, instanceID, action)
	}

	return vault.trackProgress(ctx, instanceID, action, description, contextID, "", taskID, params)
}

// TrackProgressWithDeployment stores progress information for a task as
// TrackProgressWithContextID does, and records deploymentName as the name
// of the deployment the request acts on, in place of any the replaced
// record carries. A deprovision calls it before it sends its delete, so a
// poll that finds no index entry can ask the director about the
// deployment the delete named.
func (vault *Vault) TrackProgressWithDeployment(ctx context.Context, instanceID, action, description, contextID, deploymentName string, taskID int, params interface{}) error {
	if contextID == "" {
		return fmt.Errorf("%w: instance %s, action %s", ErrEmptyContextID, instanceID, action)
	}

	return vault.trackProgress(ctx, instanceID, action, description, contextID, deploymentName, taskID, params)
}

// trackProgress writes the task record. An empty contextID or deploymentName
// keeps the one the replaced record carries when that record is for the
// same action.
//
// A write over a record of the same action whose outcome is unknown keeps
// the state outcome_unknown, the unknown-since time, and the absence of a
// task ID, unless it settles the outcome. A write settles it when it names
// a task, or the failure -1, or a context ID other than the recorded one.
func (vault *Vault) trackProgress(ctx context.Context, instanceID, action, description, contextID, deploymentName string, taskID int, params interface{}) error {
	return vault.trackProgressGuarded(ctx, instanceID, action, description, contextID, deploymentName, taskID, params, nil)
}

// trackProgressGuarded writes the task record as trackProgress does, and
// writes nothing when guard returns an error. See writeTaskRecordGuarded.
func (vault *Vault) trackProgressGuarded(ctx context.Context, instanceID, action, description, contextID, deploymentName string, taskID int, params interface{}, guard func(current map[string]interface{}) error) error {
	logger := logger.Get().Named("vault track progress " + instanceID)
	logger.Debug("tracking progress for '%s': %s (taskID: %d)", action, description, taskID)

	// Determine the state based on task ID
	var state string

	switch taskID {
	case -1:
		state = eventStateFailed
	case 0:
		state = "initializing"
	default:
		state = "in_progress"
	}

	task := struct {
		Action         string      `json:"action"`
		Task           int         `json:"task"`
		State          string      `json:"state"`
		Description    string      `json:"description"`
		UpdatedAt      int64       `json:"updated_at"`
		Params         interface{} `json:"params"`
		ContextID      string      `json:"context_id,omitempty"`
		DeploymentName string      `json:"deployment_name,omitempty"`
	}{
		Action:         action,
		Task:           taskID,
		State:          state,
		Description:    description,
		UpdatedAt:      time.Now().Unix(),
		Params:         deinterface(params),
		ContextID:      contextID,
		DeploymentName: deploymentName,
	}

	err := vault.writeTaskRecordGuarded(ctx, instanceID, action, logger, guard, func(replaced map[string]interface{}) interface{} {
		if task.ContextID == "" {
			task.ContextID, _ = replaced[taskRecordContextIDKey].(string)
		}

		if task.DeploymentName == "" {
			task.DeploymentName, _ = replaced[TaskRecordDeploymentNameKey].(string)
		}

		kept, keeps := keptUnknownOutcome(replaced, task.ContextID, taskID)
		if keeps {
			logger.Debug("keeping the unknown outcome of context ID %s, unknown since %s, under the progress written", kept.ContextID, kept.UnknownSince)

			kept.Action = action
			kept.Description = description
			kept.UpdatedAt = task.UpdatedAt
			kept.Params = task.Params
			kept.DeploymentName = task.DeploymentName

			return kept
		}

		// Log the task being stored for debugging
		logger.Debug("storing task with ID %d, state '%s' at path %s/task", taskID, state, instanceID)

		return task
	})
	if err != nil {
		logger.Error("failed to store task progress: %s", err)

		return err
	}

	logger.Debug("successfully stored task progress with ID %d", taskID)

	// Append to history
	return vault.AppendHistory(ctx, instanceID, action, description)
}

// keptUnknownOutcome returns the state, the context ID, and the
// unknown-since time a progress write keeps from the record it replaces,
// and true, when that record's outcome is unknown and the write does not
// settle it. A write settles it when it names a task, or the failure -1,
// or a context ID other than the recorded one.
func keptUnknownOutcome(replaced map[string]interface{}, contextID string, taskID int) (unknownOutcomeRecord, bool) {
	replacedState, _ := replaced["state"].(string)
	replacedContextID, _ := replaced[taskRecordContextIDKey].(string)

	if replacedState != TaskStateOutcomeUnknown || taskID != 0 || contextID != replacedContextID {
		return unknownOutcomeRecord{}, false
	}

	unknownSince, _ := replaced[TaskRecordUnknownSinceKey].(string)

	return unknownOutcomeRecord{State: TaskStateOutcomeUnknown, ContextID: replacedContextID, UnknownSince: unknownSince}, true
}

// unknownOutcomeRecord is the task record of an operation whose director
// request was sent and whose answer was lost. It has no task ID at all.
type unknownOutcomeRecord struct {
	Action         string      `json:"action"`
	State          string      `json:"state"`
	Description    string      `json:"description"`
	UpdatedAt      int64       `json:"updated_at"`
	Params         interface{} `json:"params"`
	ContextID      string      `json:"context_id"`
	UnknownSince   string      `json:"unknown_since"`
	DeploymentName string      `json:"deployment_name,omitempty"`
}

// Bounds on the write of an unknown outcome. A lost write leaves the
// operation to the older recovery paths, which cannot look its task up by
// the context ID, so the write is tried again a few times within a bound of
// its own.
const (
	outcomeUnknownWriteAttempts = 3
	outcomeUnknownWriteBound    = 10 * time.Second
	outcomeUnknownWriteBackoff  = 100 * time.Millisecond
)

// TrackOutcomeUnknown records that the director request of the operation,
// which carried contextID, was sent and its answer was lost at since. The
// record has the state outcome_unknown and no task ID at all, so nothing
// can take it for a task, or for task 0, which once meant a deployment was
// gone. The params and the deployment name of the replaced record are kept
// when it is for the same action, and the description goes into the
// history as TrackProgress writes it. When the replaced record already
// holds the unknown outcome of the same context ID, its unknown-since time
// is kept.
//
// The write runs detached from ctx's cancellation, under a bound of its
// own, and a failed write is tried up to three times in all within it.
func (vault *Vault) TrackOutcomeUnknown(ctx context.Context, instanceID, action, description, contextID string, since time.Time) error {
	if contextID == "" {
		return fmt.Errorf("%w: instance %s, action %s, unknown outcome", ErrEmptyContextID, instanceID, action)
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), outcomeUnknownWriteBound)
	defer cancel()

	logger := logger.Get().Named("vault track outcome unknown " + instanceID)
	logger.Debug("recording an unknown outcome for '%s' with context ID %s: %s", action, contextID, description)

	sinceText := since.UTC().Format(time.RFC3339Nano)

	build := func(replaced map[string]interface{}) interface{} {
		task := unknownOutcomeRecord{
			Action:       action,
			State:        TaskStateOutcomeUnknown,
			Description:  description,
			UpdatedAt:    time.Now().Unix(),
			ContextID:    contextID,
			UnknownSince: sinceText,
			Params:       replaced["params"],
		}
		task.DeploymentName, _ = replaced[TaskRecordDeploymentNameKey].(string)

		// The same request's outcome recorded again keeps the time it
		// first became unknown.
		replacedState, _ := replaced["state"].(string)
		replacedContextID, _ := replaced[taskRecordContextIDKey].(string)
		replacedSince, _ := replaced[TaskRecordUnknownSinceKey].(string)

		if replacedState == TaskStateOutcomeUnknown && replacedContextID == contextID && replacedSince != "" {
			task.UnknownSince = replacedSince
		}

		return task
	}

	var err error

	for attempt := 1; attempt <= outcomeUnknownWriteAttempts; attempt++ {
		err = vault.writeTaskRecord(ctx, instanceID, action, logger, build)
		if err == nil {
			break
		}

		logger.Error("attempt %d of %d to record the unknown outcome of context ID %s failed: %s", attempt, outcomeUnknownWriteAttempts, contextID, err)

		if attempt == outcomeUnknownWriteAttempts {
			break
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("the unknown outcome of context ID %s was not recorded within %s: %w", contextID, outcomeUnknownWriteBound, err)
		case <-time.After(time.Duration(attempt) * outcomeUnknownWriteBackoff):
		}
	}

	if err != nil {
		return fmt.Errorf("the unknown outcome of context ID %s was not recorded in %d attempts: %w", contextID, outcomeUnknownWriteAttempts, err)
	}

	return vault.AppendHistory(ctx, instanceID, action, description)
}

// writeTaskRecord writes the record that build returns to <instance>/task,
// holding the instance's task record lock from the read of the record it
// replaces to the write. build gets the replaced record when that record is
// for the same action, and an empty map otherwise. When the replaced record
// cannot be read, build gets an empty map and the write still goes ahead,
// because losing a kept field is better than losing the progress or the
// failure the write records. The caller writes the ID it must not lose
// with that ID in hand, rather than relying on it being kept.
func (vault *Vault) writeTaskRecord(ctx context.Context, instanceID, action string, logger logger.Logger, build func(replaced map[string]interface{}) interface{}) error {
	return vault.writeTaskRecordGuarded(ctx, instanceID, action, logger, nil, build)
}

// writeTaskRecordGuarded writes the task record as writeTaskRecord does.
// When guard is not nil, it gets the current record whatever its action,
// or nil when there is none, and an error it returns stops the write. A
// guarded write also stops when the current record cannot be read, because
// the guard cannot judge it.
func (vault *Vault) writeTaskRecordGuarded(ctx context.Context, instanceID, action string, logger logger.Logger, guard func(current map[string]interface{}) error, build func(replaced map[string]interface{}) interface{}) error {
	unlock, err := vault.taskLocks.lock(ctx, instanceID)
	if err != nil {
		return err
	}
	defer unlock()

	replaced := map[string]interface{}{}

	var current map[string]interface{}

	exists, err := vault.Get(ctx, instanceID+"/task", &current)

	switch {
	case err != nil && guard != nil:
		return fmt.Errorf("could not read %s/task to check it before replacing it: %w", instanceID, err)
	case err != nil:
		logger.Warning("could not read %s/task before replacing it, so nothing it records is kept: %s", instanceID, err)
	case exists && current != nil:
		recordedAction, _ := current["action"].(string)
		if recordedAction == action {
			replaced = current
		}
	}

	if guard != nil {
		if !exists {
			current = nil
		}

		err = guard(current)
		if err != nil {
			return err
		}
	}

	return vault.Put(ctx, instanceID+"/task", build(replaced))
}

// AppendHistory adds an entry to the instance's history log.
func (vault *Vault) AppendHistory(ctx context.Context, instanceID, action, description string) error {
	logger := logger.Get().Named("vault history " + instanceID)
	logger.Debug("appending to history: %s - %s", action, description)

	metadataPath := instanceID + "/metadata"

	// Get existing metadata
	var metadata map[string]interface{}

	exists, err := vault.Get(ctx, metadataPath, &metadata)
	if err != nil {
		logger.Error("failed to get metadata: %s", err)

		// Writing a fresh map over metadata that could not be read would
		// erase the existing history.
		return fmt.Errorf("failed to read %s before appending history: %w", metadataPath, err)
	}

	if !exists {
		metadata = map[string]interface{}{}
	}

	// Extract the history array from metadata
	var history []map[string]interface{}
	if historyData, ok := metadata["history"].([]interface{}); ok {
		// Convert []interface{} to []map[string]interface{}
		for _, entry := range historyData {
			if entryMap, ok := entry.(map[string]interface{}); ok {
				history = append(history, entryMap)
			}
		}
	} else if historyData, ok := metadata["history"].([]map[string]interface{}); ok {
		history = historyData
	} else {
		// If history doesn't exist or is wrong type, start fresh
		history = []map[string]interface{}{}
	}

	// Append new entry
	entry := map[string]interface{}{
		"timestamp":   time.Now().Unix(),
		"action":      action,
		"description": description,
	}
	history = append(history, entry)

	// Apply retention policy (time-based + max entries safety cap)
	history = vaultPkg.FilterHistoryByRetention(history, vaultPkg.DefaultHistoryRetentionDays, vaultPkg.HistoryMaxSize)

	// Store history back in metadata
	metadata["history"] = history

	return vault.Put(ctx, metadataPath, metadata)
}

// Index sets the index entry for instanceID to data, or removes the entry
// when data is nil.
func (vault *Vault) Index(ctx context.Context, instanceID string, data interface{}) error {
	var entry interface{}

	switch typedData := data.(type) {
	case nil:
	case vaultPkg.Instance:
		entry = typedData
	case map[string]interface{}:
		entry = typedData
	default:
		dataMap, err := convertToMap(data)
		if err != nil {
			return fmt.Errorf("%w: %w", vaultPkg.ErrIndexedValueMalformed, err)
		}

		entry = dataMap
	}

	return vault.updateIndex(ctx, func(idx *vaultPkg.Index) (bool, error) {
		// If data is nil, remove the instance from the index
		if entry == nil {
			delete(idx.Data, instanceID)
		} else {
			idx.Data[instanceID] = entry
		}

		return true, nil
	})
}

// GetIndex retrieves a vault index.
func (vault *Vault) GetIndex(ctx context.Context, path string) (*vaultPkg.Index, error) {
	idx, err := vaultPkg.NewIndex(vault, path, ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create vault index: %w", err)
	}

	return idx, nil
}

// FindInstance searches for an instance in the vault index.
func (vault *Vault) FindInstance(ctx context.Context, instanceID string) (*vaultPkg.Instance, bool, error) {
	idx, err := vault.GetIndex(ctx, "db")
	if err != nil {
		return nil, false, fmt.Errorf("failed to get vault index: %w", err)
	}

	return instanceFromIndex(idx, instanceID)
}

// instanceFromIndex decodes the entry for instanceID in idx. A missing entry,
// or one whose data is nil, reports false.
func instanceFromIndex(idx *vaultPkg.Index, instanceID string) (*vaultPkg.Instance, bool, error) {
	data, err := idx.Lookup(instanceID)
	if err != nil {
		if err.Error() == fmt.Sprintf("%s: '%s'", vaultPkg.ErrKeyNotFoundInIndex, instanceID) {
			return nil, false, nil
		}

		return nil, false, fmt.Errorf("failed to lookup instance in index: %w", err)
	}

	// Handle nil data - instance may be in process of deletion
	if data == nil {
		// Instance has nil data in index (likely being deleted)
		return nil, false, nil
	}

	var instance vaultPkg.Instance

	// Handle different types that could be stored in the index
	switch typedData := data.(type) {
	case vaultPkg.Instance:
		// Data is already an Instance struct
		instance = typedData
	case *vaultPkg.Instance:
		// Data is a pointer to an Instance struct
		if typedData != nil {
			instance = *typedData
		}
	case map[string]interface{}:
		// Data is a map that needs to be converted
		err := convertToStruct(typedData, &instance)
		if err != nil {
			return nil, false, fmt.Errorf("failed to convert instance data: %w", err)
		}
	default:
		// Unexpected data type
		return nil, false, vaultPkg.ErrInvalidInstanceData
	}

	return &instance, true, nil
}

// State retrieves the current state of an instance.
func (vault *Vault) State(ctx context.Context, instanceID string) (string, int, map[string]interface{}, error) {
	type TaskState struct {
		State string `json:"state"`
		Task  int    `json:"task"`
	}

	var taskState TaskState

	exists, err := vault.Get(ctx, instanceID+"/task", &taskState)
	if err != nil {
		return "", 0, nil, err
	}

	if !exists {
		return "not_found", 0, nil, nil
	}

	// Get full task data for params
	var fullTask map[string]interface{}

	_, err = vault.Get(ctx, instanceID+"/task", &fullTask)
	if err != nil {
		return taskState.State, taskState.Task, nil, err
	}

	return taskState.State, taskState.Task, fullTask, nil
}

// Clear removes all data for an instance.
func (vault *Vault) Clear(instanceID string) {
	logger := logger.Get().Named("vault clear " + instanceID)
	ctx := context.Background()

	// List of paths to clear
	paths := []string{
		instanceID + "/task",
		instanceID + "/metadata",
		instanceID + "/credentials",
		instanceID,
	}

	for _, path := range paths {
		err := vault.Delete(ctx, path)
		if err != nil {
			logger.Debug("failed to delete %s (may not exist): %s", path, err)
		} else {
			logger.Debug("deleted %s", path)
		}
	}

	// Remove from index
	err := vault.updateIndex(ctx, func(idx *vaultPkg.Index) (bool, error) {
		delete(idx.Data, instanceID)

		return true, nil
	})
	if err != nil {
		logger.Debug("failed to remove instance from index during cleanup: %s", err)

		return
	}

	logger.Info("cleared all data for instance %s", instanceID)
}

// GetVaultDB retrieves the main vault database index.
func (vault *Vault) GetVaultDB(ctx context.Context) (*vaultPkg.Index, error) {
	logger := logger.Get().Named("task.log")
	logger.Debug("retrieving vault database index")

	idx, err := vault.GetIndex(ctx, "db")
	if err != nil {
		logger.Error("failed to get vault database index: %s", err)

		return nil, err
	}

	logger.Debug("vault database has %d entries", len(idx.Data))

	return idx, nil
}

// UpdateIndexEntry updates specific fields for an instance in the vault index.
func (vault *Vault) UpdateIndexEntry(ctx context.Context, instanceID string, updates map[string]interface{}) error {
	logger := logger.Get().Named("vault")
	logger.Debug("updating index entry for instance %s", instanceID)

	err := vault.updateIndex(ctx, func(idx *vaultPkg.Index) (bool, error) {
		applyIndexUpdates(logger, idx, instanceID, updates)

		return true, nil
	})
	if err != nil {
		return fmt.Errorf("failed to update index entry for instance %s: %w", instanceID, err)
	}

	logger.Debug("updated index entry for instance %s with %d fields", instanceID, len(updates))

	return nil
}

// applyIndexUpdates sets the fields in updates on the entry for instanceID in
// idx, creating the entry when it is missing or is not a map.
func applyIndexUpdates(logger logger.Logger, idx *vaultPkg.Index, instanceID string, updates map[string]interface{}) {
	// Get existing entry or create new one
	var entry map[string]interface{}
	if existing, exists := idx.Data[instanceID]; exists {
		// Type assert existing entry
		if existingMap, ok := existing.(map[string]interface{}); ok {
			entry = existingMap
		} else {
			logger.Warning("instance %s has invalid data type in index, recreating", instanceID)

			entry = make(map[string]interface{})
		}
	} else {
		logger.Warning("instance %s not found in index, creating new entry", instanceID)

		entry = make(map[string]interface{})
	}

	// Apply updates
	for key, value := range updates {
		entry[key] = value
	}

	idx.Data[instanceID] = entry
}

// Helper function to convert interface{} recursively.
func deinterface(option interface{}) interface{} {
	switch option := option.(type) {
	case map[interface{}]interface{}:
		return deinterfaceMap(option)
	case []interface{}:
		return deinterfaceList(option)
	default:
		return option
	}
}

// deinterfaceMap converts a map[interface{}]interface{} to map[string]interface{}.
func deinterfaceMap(o map[interface{}]interface{}) map[string]interface{} {
	m := map[string]interface{}{}
	for k, v := range o {
		m[fmt.Sprintf("%v", k)] = deinterface(v)
	}

	return m
}

// deinterfaceList converts []interface{} by recursively calling deinterface on each element.
func deinterfaceList(o []interface{}) []interface{} {
	l := make([]interface{}, len(o))
	for i, v := range o {
		l[i] = deinterface(v)
	}

	return l
}
