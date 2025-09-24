package vault

import (
	"context"
	"fmt"
	"time"

	"blacksmith/pkg/logger"
	vaultPkg "blacksmith/pkg/vault"
)

const (
	eventStateFailed = "failed"
)

// Track stores tracking information for a task (deprecated, kept for backward compatibility).
func (vault *Vault) Track(ctx context.Context, instanceID, action string, taskID int, params interface{}) error {
	logger := logger.Get().Named("vault track " + instanceID)
	logger.Debug("tracking action '%s', task %d", action, taskID)

	// Note: We're deprecating storing task IDs in vault
	// This function is kept for backward compatibility but will be removed
	// Task IDs are now retrieved from BOSH deployment events

	// Store minimal tracking info for audit purposes
	task := struct {
		Action      string      `json:"action"`
		State       string      `json:"state"`
		Description string      `json:"description"`
		UpdatedAt   int64       `json:"updated_at"`
		Params      interface{} `json:"params"`
	}{
		Action:      action,
		State:       "in_progress",
		Description: fmt.Sprintf("Operation %s in progress", action),
		UpdatedAt:   time.Now().Unix(),
		Params:      deinterface(params),
	}

	return vault.Put(ctx, instanceID+"/task", task)
}

// TrackProgress stores progress information for a task.
func (vault *Vault) TrackProgress(ctx context.Context, instanceID, action, description string, taskID int, params interface{}) error {
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
		Action      string      `json:"action"`
		Task        int         `json:"task"`
		State       string      `json:"state"`
		Description string      `json:"description"`
		UpdatedAt   int64       `json:"updated_at"`
		Params      interface{} `json:"params"`
	}{
		Action:      action,
		Task:        taskID,
		State:       state,
		Description: description,
		UpdatedAt:   time.Now().Unix(),
		Params:      deinterface(params),
	}

	// Log the task being stored for debugging
	logger.Debug("storing task with ID %d, state '%s' at path %s/task", taskID, state, instanceID)

	// Store current state
	err := vault.Put(ctx, instanceID+"/task", task)
	if err != nil {
		logger.Error("failed to store task progress: %s", err)

		return err
	}

	logger.Debug("successfully stored task progress with ID %d", taskID)

	// Append to history
	return vault.AppendHistory(ctx, instanceID, action, description)
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
		// Start fresh if there's an error
		metadata = map[string]interface{}{}
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

	// Limit history to last 50 entries
	if len(history) > vaultPkg.HistoryMaxSize {
		history = history[len(history)-vaultPkg.HistoryMaxSize:]
	}

	// Store history back in metadata
	metadata["history"] = history

	return vault.Put(ctx, metadataPath, metadata)
}

// Index manages vault index data.
func (vault *Vault) Index(ctx context.Context, instanceID string, data interface{}) error {
	idx, err := vault.GetIndex(ctx, "db")
	if err != nil {
		return err
	}

	// If data is nil, remove the instance from the index
	if data == nil {
		delete(idx.Data, instanceID)
	} else {
		switch typedData := data.(type) {
		case vaultPkg.Instance:
			idx.Data[instanceID] = typedData
		case map[string]interface{}:
			idx.Data[instanceID] = typedData
		default:
			dataMap, err := convertToMap(data)
			if err != nil {
				return fmt.Errorf("%w: %w", vaultPkg.ErrIndexedValueMalformed, err)
			}

			idx.Data[instanceID] = dataMap
		}
	}

	err = idx.Save(ctx)
	if err != nil {
		return fmt.Errorf("failed to save index: %w", err)
	}

	return nil
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
		err = convertToStruct(typedData, &instance)
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
	idx, err := vault.GetIndex(ctx, "db")
	if err != nil {
		logger.Debug("failed to get index for cleanup: %s", err)

		return
	}

	delete(idx.Data, instanceID)

	err = idx.Save(ctx)
	if err != nil {
		logger.Debug("failed to save index after cleanup: %s", err)
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

	idx, err := vault.GetIndex(ctx, "db")
	if err != nil {
		return fmt.Errorf("failed to get vault index: %w", err)
	}

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

	// Save back to index
	idx.Data[instanceID] = entry
	if err := idx.Save(ctx); err != nil {
		return fmt.Errorf("failed to save index after updating instance %s: %w", instanceID, err)
	}

	logger.Debug("updated index entry for instance %s with %d fields", instanceID, len(updates))
	return nil
}

// MarkInstanceDeleted marks an instance as deleted in the vault index.
func (vault *Vault) MarkInstanceDeleted(ctx context.Context, instanceID string) error {
	logger := logger.Get().Named("vault")
	logger.Info("marking instance %s as deleted in vault index", instanceID)

	updates := map[string]interface{}{
		"status":          "deleted",
		"deleted_at":      time.Now().Format(time.RFC3339),
		"deleted_by":      "vm-monitor",
		"deletion_reason": "deployment not found in BOSH director",
	}

	if err := vault.UpdateIndexEntry(ctx, instanceID, updates); err != nil {
		return fmt.Errorf("failed to mark instance %s as deleted: %w", instanceID, err)
	}

	logger.Info("successfully marked instance %s as deleted", instanceID)
	return nil
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
