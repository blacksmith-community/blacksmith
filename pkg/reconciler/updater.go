package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

type vaultUpdater struct {
	vault        interface{} // Will be replaced with actual Vault type
	logger       Logger
	backupConfig BackupConfig
}

// NewVaultUpdater creates a new vault updater
func NewVaultUpdater(vault interface{}, logger Logger, backupConfig BackupConfig) Updater {
	return &vaultUpdater{
		vault:        vault,
		logger:       logger,
		backupConfig: backupConfig,
	}
}

// UpdateInstance updates an instance in vault
func (u *vaultUpdater) UpdateInstance(ctx context.Context, instance *InstanceData) error {
	if instance == nil {
		return fmt.Errorf("instance is nil")
	}

	u.logDebug("Updating instance %s in vault", instance.ID)

	// Create backup of existing instance data before any updates if enabled
	if u.backupConfig.Enabled {
		if err := u.backupInstance(instance.ID); err != nil {
			u.logWarning("Failed to create backup for instance %s: %s (continuing anyway)", instance.ID, err)
		}
	}

	// Check for existing credentials - NEVER overwrite
	credsPath := fmt.Sprintf("%s/credentials", instance.ID)
	existingCreds, credsErr := u.getFromVault(credsPath)
	hasCredentials := credsErr == nil && len(existingCreds) > 0

	if instance.Metadata == nil {
		instance.Metadata = make(map[string]interface{})
	}
	instance.Metadata["has_credentials"] = hasCredentials
	if hasCredentials {
		u.logDebug("Instance %s has existing credentials - preserving", instance.ID)
	} else {
		u.logWarning("Instance %s is missing credentials - will attempt to fetch from BOSH", instance.ID)
		instance.Metadata["needs_credential_recovery"] = true
	}

	// Check for existing bindings
	bindingsPath := fmt.Sprintf("%s/bindings", instance.ID)
	existingBindings, bindingsErr := u.getFromVault(bindingsPath)
	hasBindings := bindingsErr == nil && len(existingBindings) > 0

	if hasBindings {
		instance.Metadata["has_bindings"] = true
		instance.Metadata["bindings_count"] = len(existingBindings)

		// Store binding IDs for reference
		bindingIDs := make([]string, 0, len(existingBindings))
		for id := range existingBindings {
			bindingIDs = append(bindingIDs, id)
		}
		instance.Metadata["binding_ids"] = bindingIDs
		u.logDebug("Instance %s has %d existing bindings - preserving", instance.ID, len(existingBindings))
	}

	// Prepare data for vault storage using Unix timestamps (seconds) like the main broker
	vaultData := map[string]interface{}{
		"service_id":      instance.ServiceID,
		"plan_id":         instance.PlanID,
		"deployment_name": instance.DeploymentName,
		"created":         instance.CreatedAt.Unix(),    // Use Unix timestamp for UI compatibility
		"updated":         instance.UpdatedAt.Unix(),    // Use Unix timestamp for UI compatibility
		"last_synced_at":  instance.LastSyncedAt.Unix(), // Use Unix timestamp for UI compatibility
		"reconciled":      true,
		"reconciled_at":   time.Now().Unix(),
	}

	// Add plan object with name if available in metadata
	if planName, ok := instance.Metadata["plan_name"].(string); ok && planName != "" {
		vaultData["plan"] = map[string]interface{}{
			"id":   instance.PlanID,
			"name": planName,
		}
	} else {
		// Fallback to just plan_id if name not available
		vaultData["plan"] = map[string]interface{}{
			"id":   instance.PlanID,
			"name": instance.PlanID, // Use ID as fallback
		}
	}

	// Add service name if available
	if serviceName, ok := instance.Metadata["service_name"].(string); ok && serviceName != "" {
		vaultData["service_name"] = serviceName
	}

	// Store manifest separately if present
	if instance.Manifest != "" {
		manifestPath := fmt.Sprintf("%s/manifest", instance.ID)
		manifestData := map[string]interface{}{
			"manifest":      instance.Manifest,
			"updated_at":    time.Now().Unix(), // Use Unix timestamp
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

		// Detect what changed for history tracking
		changes := u.detectChanges(existingMeta, instance.Metadata)

		// Add reconciliation entry to history with change information
		u.addReconciliationHistory(mergedMetadata, changes)

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

	// Parse Unix timestamps - handle both old RFC3339 strings and new Unix timestamps
	if v, ok := indexData["created"].(float64); ok {
		instance.CreatedAt = time.Unix(int64(v), 0)
	} else if v, ok := indexData["created"].(int64); ok {
		instance.CreatedAt = time.Unix(v, 0)
	} else if v, ok := indexData["created_at"].(string); ok {
		// Fallback to RFC3339 for backward compatibility
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			instance.CreatedAt = t
		}
	}

	if v, ok := indexData["updated"].(float64); ok {
		instance.UpdatedAt = time.Unix(int64(v), 0)
	} else if v, ok := indexData["updated"].(int64); ok {
		instance.UpdatedAt = time.Unix(v, 0)
	} else if v, ok := indexData["updated_at"].(string); ok {
		// Fallback to RFC3339 for backward compatibility
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			instance.UpdatedAt = t
		}
	}

	if v, ok := indexData["last_synced_at"].(float64); ok {
		instance.LastSyncedAt = time.Unix(int64(v), 0)
	} else if v, ok := indexData["last_synced_at"].(int64); ok {
		instance.LastSyncedAt = time.Unix(v, 0)
	} else if v, ok := indexData["last_synced_at"].(string); ok {
		// Fallback to RFC3339 for backward compatibility
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

// detectChanges compares old and new metadata to detect what changed
func (u *vaultUpdater) detectChanges(old, new map[string]interface{}) map[string]interface{} {
	changes := make(map[string]interface{})

	// Check if releases changed
	oldReleases, oldHasReleases := old["releases"]
	newReleases, newHasReleases := new["releases"]
	if oldHasReleases && newHasReleases {
		changes["releases_changed"] = !u.deepEqual(oldReleases, newReleases)
	} else if newHasReleases {
		changes["releases_changed"] = true
	} else {
		changes["releases_changed"] = false
	}

	// Check if stemcells changed
	oldStemcells, oldHasStemcells := old["stemcells"]
	newStemcells, newHasStemcells := new["stemcells"]
	if oldHasStemcells && newHasStemcells {
		changes["stemcells_changed"] = !u.deepEqual(oldStemcells, newStemcells)
	} else if newHasStemcells {
		changes["stemcells_changed"] = true
	} else {
		changes["stemcells_changed"] = false
	}

	// Check if VMs changed
	oldVMs, oldHasVMs := old["vms"]
	newVMs, newHasVMs := new["vms"]
	if oldHasVMs && newHasVMs {
		changes["vms_changed"] = !u.deepEqual(oldVMs, newVMs)
	} else if newHasVMs {
		changes["vms_changed"] = true
	} else {
		changes["vms_changed"] = false
	}

	// Track field additions/removals
	addedFields := []string{}
	removedFields := []string{}

	for k := range new {
		if _, exists := old[k]; !exists {
			addedFields = append(addedFields, k)
		}
	}

	for k := range old {
		if _, exists := new[k]; !exists {
			removedFields = append(removedFields, k)
		}
	}

	if len(addedFields) > 0 {
		changes["fields_added"] = addedFields
	}
	if len(removedFields) > 0 {
		changes["fields_removed"] = removedFields
	}

	return changes
}

// deepEqual performs a simple deep equality check
func (u *vaultUpdater) deepEqual(a, b interface{}) bool {
	// Simple implementation - could be enhanced
	aStr := fmt.Sprintf("%v", a)
	bStr := fmt.Sprintf("%v", b)
	return aStr == bStr
}

// addReconciliationHistory adds a reconciliation entry to the history
func (u *vaultUpdater) addReconciliationHistory(metadata map[string]interface{}, changes map[string]interface{}) {
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

	// Build description based on changes
	description := "Instance reconciled from BOSH deployment"
	if len(changes) > 0 {
		if changes["releases_changed"] == true {
			description += " - releases updated"
		}
		if changes["stemcells_changed"] == true {
			description += " - stemcells updated"
		}
		if changes["vms_changed"] == true {
			description += " - VMs updated"
		}
	}

	// Add enhanced reconciliation entry
	entry := map[string]interface{}{
		"timestamp":        time.Now().Unix(),
		"iso_timestamp":    time.Now().Format(time.RFC3339),
		"action":           "reconciliation",
		"description":      description,
		"source":           "deployment_reconciler",
		"state":            "active",
		"deployment":       metadata["deployment_name"],
		"match_confidence": metadata["match_confidence"],
		"match_reason":     metadata["match_reason"],
	}

	// Add changes if present
	if len(changes) > 0 {
		entry["changes"] = changes
	}

	history = append(history, entry)

	// Limit history to last 100 entries
	if len(history) > 100 {
		history = history[len(history)-100:]
	}

	metadata["history"] = history
}

// Vault interaction methods with proper implementation

func (u *vaultUpdater) putToVault(path string, data map[string]interface{}) error {
	u.logDebug("Storing data at vault path: %s", path)

	// Type assert vault to VaultInterface to access Put method
	vault, ok := u.vault.(VaultInterface)
	if !ok {
		return fmt.Errorf("vault is not of expected type VaultInterface")
	}

	return vault.Put(path, data)
}

func (u *vaultUpdater) getFromVault(path string) (map[string]interface{}, error) {
	u.logDebug("Getting data from vault path: %s", path)

	// Type assert vault to VaultInterface to access Get method
	vault, ok := u.vault.(VaultInterface)
	if !ok {
		return nil, fmt.Errorf("vault is not of expected type VaultInterface")
	}

	var data map[string]interface{}
	exists, err := vault.Get(path, &data)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("no data found at path %s", path)
	}

	return data, nil
}

func (u *vaultUpdater) updateIndex(instanceID string, data map[string]interface{}) error {
	u.logDebug("Updating index for instance: %s", instanceID)

	// Type assert vault to VaultInterface to access GetIndex method
	vault, ok := u.vault.(VaultInterface)
	if !ok {
		return fmt.Errorf("vault is not of expected type VaultInterface")
	}

	idx, err := vault.GetIndex("db")
	if err != nil {
		return fmt.Errorf("failed to get vault index: %w", err)
	}

	idx.Data[instanceID] = data
	return idx.Save()
}

func (u *vaultUpdater) getFromIndex(instanceID string) (map[string]interface{}, error) {
	u.logDebug("Getting instance from index: %s", instanceID)

	// Type assert vault to VaultInterface to access GetIndex method
	vault, ok := u.vault.(VaultInterface)
	if !ok {
		return nil, fmt.Errorf("vault is not of expected type VaultInterface")
	}

	idx, err := vault.GetIndex("db")
	if err != nil {
		return nil, fmt.Errorf("failed to get vault index: %w", err)
	}

	raw, exists := idx.Lookup(instanceID)
	if !exists {
		return nil, NotFoundError{Resource: "instance", ID: instanceID}
	}

	// Convert raw data to map
	data, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid data type for instance %s", instanceID)
	}

	return data, nil
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

// backupInstance creates a timestamped backup of instance data before updates
func (u *vaultUpdater) backupInstance(instanceID string) error {
	// Get all current instance data (exclude backups path itself)
	allData := make(map[string]interface{})

	// Get main instance data from index
	indexData, err := u.getFromIndex(instanceID)
	if err != nil {
		// No existing data to backup
		u.logDebug("No existing data to backup for instance %s", instanceID)
		return nil
	}
	allData["index"] = indexData

	// Get metadata if exists
	metadataPath := fmt.Sprintf("%s/metadata", instanceID)
	if metadata, err := u.getFromVault(metadataPath); err == nil {
		allData["metadata"] = metadata
	}

	// Get manifest if exists
	manifestPath := fmt.Sprintf("%s/manifest", instanceID)
	if manifest, err := u.getFromVault(manifestPath); err == nil {
		allData["manifest"] = manifest
	}

	// Get credentials if exists - CRITICAL for service recovery
	credsPath := fmt.Sprintf("%s/credentials", instanceID)
	if creds, err := u.getFromVault(credsPath); err == nil {
		allData["credentials"] = creds
		u.logDebug("Backed up credentials for instance %s", instanceID)
	} else {
		u.logDebug("No credentials found to backup for instance %s", instanceID)
	}

	// Get bindings if exists
	bindingsPath := fmt.Sprintf("%s/bindings", instanceID)
	if bindings, err := u.getFromVault(bindingsPath); err == nil {
		allData["bindings"] = bindings
		u.logDebug("Backed up bindings for instance %s", instanceID)
	}

	// Calculate SHA256 hash of the data
	dataBytes, err := json.Marshal(allData)
	if err != nil {
		return fmt.Errorf("failed to marshal backup data: %w", err)
	}

	hash := sha256.Sum256(dataBytes)
	sha256Sum := hex.EncodeToString(hash[:])

	// Check if backup with this SHA256 already exists
	backupPath := fmt.Sprintf("%s/backups/%s", instanceID, sha256Sum)
	existing, err := u.getFromVault(backupPath)
	if err == nil && len(existing) > 0 {
		u.logDebug("Backup with SHA256 %s already exists for instance %s - skipping", sha256Sum, instanceID)
		return nil
	}

	// Add backup metadata
	timestamp := time.Now().Unix()
	backupData := map[string]interface{}{
		"data":       allData,
		"timestamp":  timestamp,
		"created_at": time.Now().Format(time.RFC3339),
		"sha256":     sha256Sum,
	}

	// Store the backup
	err = u.putToVault(backupPath, backupData)
	if err != nil {
		return fmt.Errorf("failed to store backup: %w", err)
	}

	u.logDebug("Created backup for instance %s with SHA256 %s at %s", instanceID, sha256Sum, backupPath)

	// Update backup index with new backup info
	err = u.updateBackupIndex(instanceID, sha256Sum, timestamp)
	if err != nil {
		u.logWarning("Failed to update backup index for instance %s: %s", instanceID, err)
	}

	// Clean old backups if cleanup is enabled
	if u.backupConfig.Cleanup {
		retention := u.backupConfig.Retention
		if retention <= 0 {
			retention = 5 // Default retention of 5 backups
		}
		u.cleanOldBackups(instanceID, retention)
	}

	return nil
}

// updateBackupIndex updates the backup index with new backup information
func (u *vaultUpdater) updateBackupIndex(instanceID, sha256Sum string, timestamp int64) error {
	backupIndexPath := fmt.Sprintf("%s/backups/index", instanceID)

	var backupIndex map[string]interface{}
	exists, err := u.vault.(VaultInterface).Get(backupIndexPath, &backupIndex)
	if err != nil || !exists {
		// Create new backup index
		backupIndex = make(map[string]interface{})
		backupIndex["backups"] = []map[string]interface{}{}
	}

	// Get current backup list
	var backups []map[string]interface{}
	if backupList, ok := backupIndex["backups"].([]interface{}); ok {
		for _, b := range backupList {
			if backup, ok := b.(map[string]interface{}); ok {
				backups = append(backups, backup)
			}
		}
	}

	// Check if this SHA256 already exists
	for _, backup := range backups {
		if sha256, ok := backup["sha256"].(string); ok && sha256 == sha256Sum {
			// Already exists, don't add duplicate
			return nil
		}
	}

	// Add new backup entry
	newBackup := map[string]interface{}{
		"sha256":    sha256Sum,
		"timestamp": timestamp,
		"created":   time.Now().Format(time.RFC3339),
	}
	backups = append(backups, newBackup)

	// Update index
	backupIndex["backups"] = backups
	backupIndex["last_updated"] = timestamp

	return u.vault.(VaultInterface).Put(backupIndexPath, backupIndex)
}

// cleanOldBackups removes old backup entries, keeping only the most recent N backups
func (u *vaultUpdater) cleanOldBackups(instanceID string, keepCount int) {
	if keepCount <= 0 {
		u.logDebug("Backup cleanup disabled for %s (keepCount: %d)", instanceID, keepCount)
		return
	}

	// Get backup index
	backupIndexPath := fmt.Sprintf("%s/backups/index", instanceID)
	
	var backupIndex map[string]interface{}
	exists, err := u.vault.(VaultInterface).Get(backupIndexPath, &backupIndex)
	if err != nil || !exists {
		u.logDebug("No backup index found for %s", instanceID)
		return
	}

	// Get current backup list
	var backups []map[string]interface{}
	if backupList, ok := backupIndex["backups"].([]interface{}); ok {
		for _, b := range backupList {
			if backup, ok := b.(map[string]interface{}); ok {
				backups = append(backups, backup)
			}
		}
	} else if backupList, ok := backupIndex["backups"].([]map[string]interface{}); ok {
		backups = backupList
	}

	if len(backups) <= keepCount {
		u.logDebug("No cleanup needed for %s - have %d backups, keeping %d", instanceID, len(backups), keepCount)
		return
	}

	// Sort backups by timestamp (newest first)
	for i := 0; i < len(backups)-1; i++ {
		for j := i + 1; j < len(backups); j++ {
			var timestamp1, timestamp2 int64
			
			if ts, ok := backups[i]["timestamp"].(float64); ok {
				timestamp1 = int64(ts)
			} else if ts, ok := backups[i]["timestamp"].(int64); ok {
				timestamp1 = ts
			}
			
			if ts, ok := backups[j]["timestamp"].(float64); ok {
				timestamp2 = int64(ts)
			} else if ts, ok := backups[j]["timestamp"].(int64); ok {
				timestamp2 = ts
			}
			
			if timestamp1 < timestamp2 {
				backups[i], backups[j] = backups[j], backups[i]
			}
		}
	}

	// Delete old backups (keep only the most recent keepCount)
	if len(backups) > keepCount {
		toDelete := backups[keepCount:]
		for _, backup := range toDelete {
			if sha256Sum, ok := backup["sha256"].(string); ok {
				backupPath := fmt.Sprintf("%s/backups/%s", instanceID, sha256Sum)
				if err := u.vault.(VaultInterface).Delete(backupPath); err != nil {
					u.logWarning("Failed to delete old backup %s: %s", backupPath, err)
				} else {
					u.logDebug("Deleted old backup %s", backupPath)
				}
			}
		}

		// Keep only the recent backups
		backups = backups[:keepCount]
	}

	// Update backup index
	backupIndex["backups"] = backups
	backupIndex["last_cleanup"] = time.Now().Unix()

	if err := u.vault.(VaultInterface).Put(backupIndexPath, backupIndex); err != nil {
		u.logWarning("Failed to update backup index for %s: %s", instanceID, err)
	} else {
		u.logDebug("Backup cleanup for %s completed - keeping %d backups", instanceID, len(backups))
	}
}

func (u *vaultUpdater) logError(format string, args ...interface{}) {
	// Will be replaced with actual logger call
	fmt.Printf("[ERROR] updater: "+format+"\n", args...)
}

func (u *vaultUpdater) logWarning(format string, args ...interface{}) {
	// Will be replaced with actual logger call
	fmt.Printf("[WARNING] updater: "+format+"\n", args...)
}
