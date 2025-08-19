package reconciler

import (
	"context"
	"fmt"
	"time"
)

type vaultUpdater struct {
	vault  interface{} // Will be replaced with actual Vault type
	logger Logger
}

// NewVaultUpdater creates a new vault updater
func NewVaultUpdater(vault interface{}, logger Logger) Updater {
	return &vaultUpdater{
		vault:  vault,
		logger: logger,
	}
}

// UpdateInstance updates an instance in vault
func (u *vaultUpdater) UpdateInstance(ctx context.Context, instance *InstanceData) error {
	if instance == nil {
		return fmt.Errorf("instance is nil")
	}

	u.logDebug("Updating instance %s in vault", instance.ID)

	// Prepare data for vault storage
	vaultData := map[string]interface{}{
		"service_id":      instance.ServiceID,
		"plan_id":         instance.PlanID,
		"deployment_name": instance.DeploymentName,
		"created_at":      instance.CreatedAt.Format(time.RFC3339),
		"updated_at":      instance.UpdatedAt.Format(time.RFC3339),
		"last_synced_at":  instance.LastSyncedAt.Format(time.RFC3339),
		"reconciled":      true,
		"reconciled_at":   time.Now().Format(time.RFC3339),
	}

	// Store manifest separately if present
	if instance.Manifest != "" {
		manifestPath := fmt.Sprintf("%s/manifest", instance.ID)
		manifestData := map[string]interface{}{
			"manifest":      instance.Manifest,
			"updated_at":    time.Now().Format(time.RFC3339),
			"reconciled":    true,
			"reconciled_by": "reconciler",
		}

		err := u.putToVault(manifestPath, manifestData)
		if err != nil {
			u.logError("Failed to store manifest for %s: %s", instance.ID, err)
			// Continue anyway as this is non-fatal
		} else {
			u.logDebug("Stored manifest for instance %s", instance.ID)
		}
	}

	// Store metadata if present
	if len(instance.Metadata) > 0 {
		metadataPath := fmt.Sprintf("%s/metadata", instance.ID)

		// Get existing metadata to preserve history
		existingMeta, err := u.getFromVault(metadataPath)
		if err != nil {
			u.logDebug("No existing metadata found for %s: %s", instance.ID, err)
			existingMeta = make(map[string]interface{})
		}

		// Merge with new metadata
		mergedMetadata := u.mergeMetadata(existingMeta, instance.Metadata)

		// Add reconciliation entry to history
		u.addReconciliationHistory(mergedMetadata)

		err = u.putToVault(metadataPath, mergedMetadata)
		if err != nil {
			u.logError("Failed to store metadata for %s: %s", instance.ID, err)
			// Continue anyway as this is non-fatal
		} else {
			u.logDebug("Stored metadata for instance %s", instance.ID)
		}
	}

	// Update the main instance data in the index
	err := u.updateIndex(instance.ID, vaultData)
	if err != nil {
		return fmt.Errorf("failed to update index for %s: %w", instance.ID, err)
	}

	u.logInfo("Successfully updated instance %s in vault", instance.ID)
	return nil
}

// GetInstance retrieves an instance from vault
func (u *vaultUpdater) GetInstance(ctx context.Context, instanceID string) (*InstanceData, error) {
	u.logDebug("Getting instance %s from vault", instanceID)

	// Get from index
	indexData, err := u.getFromIndex(instanceID)
	if err != nil {
		return nil, NotFoundError{Resource: "instance", ID: instanceID}
	}

	instance := &InstanceData{
		ID:       instanceID,
		Metadata: make(map[string]interface{}),
	}

	// Parse basic fields
	if v, ok := indexData["service_id"].(string); ok {
		instance.ServiceID = v
	}
	if v, ok := indexData["plan_id"].(string); ok {
		instance.PlanID = v
	}
	if v, ok := indexData["deployment_name"].(string); ok {
		instance.DeploymentName = v
	}

	// Parse timestamps
	if v, ok := indexData["created_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			instance.CreatedAt = t
		}
	}
	if v, ok := indexData["updated_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			instance.UpdatedAt = t
		}
	}
	if v, ok := indexData["last_synced_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			instance.LastSyncedAt = t
		}
	}

	// Get manifest
	manifestPath := fmt.Sprintf("%s/manifest", instanceID)
	manifestData, err := u.getFromVault(manifestPath)
	if err == nil {
		if manifest, ok := manifestData["manifest"].(string); ok {
			instance.Manifest = manifest
		}
	} else {
		u.logDebug("No manifest found for instance %s", instanceID)
	}

	// Get metadata
	metadataPath := fmt.Sprintf("%s/metadata", instanceID)
	metadata, err := u.getFromVault(metadataPath)
	if err == nil {
		instance.Metadata = metadata
	} else {
		u.logDebug("No metadata found for instance %s", instanceID)
	}

	return instance, nil
}

// mergeMetadata merges existing and new metadata
func (u *vaultUpdater) mergeMetadata(existing, new map[string]interface{}) map[string]interface{} {
	merged := make(map[string]interface{})

	// Copy existing metadata
	for k, v := range existing {
		merged[k] = v
	}

	// Override with new metadata
	for k, v := range new {
		// Special handling for history - preserve it
		if k == "history" && merged["history"] != nil {
			continue
		}
		merged[k] = v
	}

	return merged
}

// addReconciliationHistory adds a reconciliation entry to the history
func (u *vaultUpdater) addReconciliationHistory(metadata map[string]interface{}) {
	var history []map[string]interface{}

	// Extract existing history
	if h, ok := metadata["history"].([]interface{}); ok {
		for _, entry := range h {
			if e, ok := entry.(map[string]interface{}); ok {
				history = append(history, e)
			}
		}
	} else if h, ok := metadata["history"].([]map[string]interface{}); ok {
		history = h
	}

	// Add new reconciliation entry
	entry := map[string]interface{}{
		"timestamp":   time.Now().Unix(),
		"action":      "reconciliation",
		"description": "Instance reconciled from BOSH deployment",
		"source":      "deployment_reconciler",
	}
	history = append(history, entry)

	// Limit history to last 100 entries
	if len(history) > 100 {
		history = history[len(history)-100:]
	}

	metadata["history"] = history
}

// Vault interaction methods - these will be replaced with actual vault calls

func (u *vaultUpdater) putToVault(path string, data map[string]interface{}) error {
	// This will be replaced with actual vault.Put call
	u.logDebug("Storing data at vault path: %s", path)
	// vault.Put(path, data)
	return nil
}

func (u *vaultUpdater) getFromVault(path string) (map[string]interface{}, error) {
	// This will be replaced with actual vault.Get call
	u.logDebug("Getting data from vault path: %s", path)
	// exists, err := vault.Get(path, &data)
	// For now, return empty data
	return make(map[string]interface{}), nil
}

func (u *vaultUpdater) updateIndex(instanceID string, data map[string]interface{}) error {
	// This will be replaced with actual vault index update
	u.logDebug("Updating index for instance: %s", instanceID)
	// idx, err := vault.GetIndex("db")
	// idx.Data[instanceID] = data
	// return idx.Save()
	return nil
}

func (u *vaultUpdater) getFromIndex(instanceID string) (map[string]interface{}, error) {
	// This will be replaced with actual vault index lookup
	u.logDebug("Getting instance from index: %s", instanceID)
	// idx, err := vault.GetIndex("db")
	// raw, err := idx.Lookup(instanceID)
	// For now, return empty data
	return nil, NotFoundError{Resource: "instance", ID: instanceID}
}

// Logging helper methods - these will be replaced with actual logger calls
func (u *vaultUpdater) logDebug(format string, args ...interface{}) {
	// Will be replaced with actual logger call
	fmt.Printf("[DEBUG] updater: "+format+"\n", args...)
}

func (u *vaultUpdater) logInfo(format string, args ...interface{}) {
	// Will be replaced with actual logger call
	fmt.Printf("[INFO] updater: "+format+"\n", args...)
}

func (u *vaultUpdater) logError(format string, args ...interface{}) {
	// Will be replaced with actual logger call
	fmt.Printf("[ERROR] updater: "+format+"\n", args...)
}
