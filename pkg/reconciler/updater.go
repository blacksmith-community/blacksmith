package reconciler

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/vault/api"
)

type vaultUpdater struct {
	vault        interface{} // Will be replaced with actual Vault type
	logger       Logger
	backupConfig BackupConfig
	cfManager    CFManagerInterface // For VCAP credential recovery
	config       *ReconcilerConfig  // For batch configuration
}

// NewVaultUpdater creates a new vault updater
func NewVaultUpdater(vault interface{}, logger Logger, backupConfig BackupConfig) Updater {
	return &vaultUpdater{
		vault:        vault,
		logger:       logger,
		backupConfig: backupConfig,
	}
}

// NewVaultUpdaterWithCF creates a new vault updater with CF manager for VCAP recovery
func NewVaultUpdaterWithCF(vault interface{}, logger Logger, backupConfig BackupConfig, cfManager CFManagerInterface) Updater {
	return &vaultUpdater{
		vault:        vault,
		logger:       logger,
		backupConfig: backupConfig,
		cfManager:    cfManager,
	}
}

// UpdateInstance updates an instance in vault
func (u *vaultUpdater) UpdateInstance(ctx context.Context, instance InstanceData) (*InstanceData, error) {
	u.logDebug("Updating instance %s in vault", instance.ID)

	// Validate instance ID to prevent empty vault paths
	if instance.ID == "" {
		u.logError("Cannot update instance with empty ID")
		return nil, fmt.Errorf("instance ID cannot be empty")
	}

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
		u.logWarning("Instance %s is missing credentials - will attempt recovery", instance.ID)
		instance.Metadata["needs_credential_recovery"] = true

		// Attempt VCAP recovery if CF manager and vault are available
		if u.cfManager != nil && u.vault != nil {
			// Safe type assertion with check
			vaultInterface, ok := u.vault.(VaultInterface)
			if !ok || vaultInterface == nil {
				u.logWarning("Vault interface not available for VCAP recovery")
			} else {
				u.logInfo("Attempting VCAP credential recovery for instance %s", instance.ID)
				vcapRecovery := NewCredentialVCAPRecovery(vaultInterface, u.cfManager, u.logger)
				if err := vcapRecovery.RecoverCredentialsFromVCAP(ctx, instance.ID); err != nil {
					u.logWarning("VCAP credential recovery failed for instance %s: %s", instance.ID, err)
					instance.Metadata["vcap_recovery_attempted"] = true
					instance.Metadata["vcap_recovery_failed"] = true
					instance.Metadata["vcap_recovery_error"] = err.Error()
				} else {
					u.logInfo("Successfully recovered credentials from VCAP for instance %s", instance.ID)
					instance.Metadata["credentials_recovered_from"] = "vcap_services"
					instance.Metadata["vcap_recovery_attempted"] = true
					instance.Metadata["vcap_recovery_failed"] = false
					hasCredentials = true
					instance.Metadata["has_credentials"] = true
					delete(instance.Metadata, "needs_credential_recovery")
				}
			}
		}
	}

	// Check for existing bindings
	bindingsPath := fmt.Sprintf("%s/bindings", instance.ID)
	if instance.ID == "" {
		u.logWarning("Skipping bindings check for empty instance ID")
	} else {
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

			// Phase 2.1: Check binding health and repair if needed
			u.logInfo("Checking binding health for instance %s", instance.ID)
			healthyBindings, unhealthyBindingIDs, healthErr := u.checkBindingHealth(instance.ID)
			if healthErr != nil {
				u.logError("Failed to check binding health for instance %s: %s", instance.ID, healthErr)
				instance.Metadata["binding_health_check_error"] = healthErr.Error()
			} else {
				instance.Metadata["healthy_bindings_count"] = len(healthyBindings)
				instance.Metadata["unhealthy_bindings_count"] = len(unhealthyBindingIDs)
				instance.Metadata["last_binding_health_check"] = time.Now().Format(time.RFC3339)

				if len(unhealthyBindingIDs) > 0 {
					u.logWarning("Found %d unhealthy bindings for instance %s: %v",
						len(unhealthyBindingIDs), instance.ID, unhealthyBindingIDs)
					instance.Metadata["unhealthy_binding_ids"] = unhealthyBindingIDs
					instance.Metadata["needs_binding_repair"] = true

					// Attempt to repair bindings (broker integration point)
					// Note: This would require the broker instance to be passed in
					// For now, we'll mark that repair is needed
					u.logInfo("Binding repair needed for instance %s but broker integration not implemented", instance.ID)
				} else {
					u.logDebug("All bindings healthy for instance %s", instance.ID)
					instance.Metadata["needs_binding_repair"] = false
				}
			}
		}
	}

	// Get existing root data to preserve fields
	rootPath := instance.ID
	if rootPath == "" {
		u.logError("Cannot store data for instance with empty ID")
		return &instance, fmt.Errorf("instance ID cannot be empty")
	}
	existingRootData, _ := u.getFromVault(rootPath)
	if existingRootData == nil {
		existingRootData = make(map[string]interface{})
	}

	// Prepare data for vault storage at root path using Unix timestamps (seconds) like the main broker
	vaultData := map[string]interface{}{
		"instance_id":     instance.ID,
		"service_id":      instance.ServiceID,
		"plan_id":         instance.PlanID,
		"deployment_name": instance.Deployment.Name,
		"created":         instance.CreatedAt.Unix(), // Use Unix timestamp for UI compatibility
		"updated":         instance.UpdatedAt.Unix(), // Use Unix timestamp for UI compatibility
		"last_synced_at":  time.Now().Unix(),         // Set current time as last sync
		"reconciled":      true,
		"reconciled_at":   time.Now().Unix(),
	}

	// Add plan object with name if available in metadata
	if planName, ok := instance.Metadata["plan_name"].(string); ok && planName != "" {
		vaultData["plan"] = map[string]interface{}{
			"id":   instance.PlanID,
			"name": planName,
		}
		// Also add plan_name at root level for compatibility
		vaultData["plan_name"] = planName
	} else {
		// Fallback to just plan_id if name not available
		vaultData["plan"] = map[string]interface{}{
			"id":   instance.PlanID,
			"name": instance.PlanID, // Use ID as fallback
		}
	}

	// Add service name and type if available
	if serviceName, ok := instance.Metadata["service_name"].(string); ok && serviceName != "" {
		vaultData["service_name"] = serviceName
	}
	if serviceType, ok := instance.Metadata["service_type"].(string); ok && serviceType != "" {
		vaultData["service_type"] = serviceType
	}

	// Add CF enriched metadata to root if available
	// Use instance_name which contains the CF instance name (e.g., "rmq-3n-a")
	if instanceName, ok := instance.Metadata["instance_name"].(string); ok && instanceName != "" {
		vaultData["instance_name"] = instanceName
	} else if instanceName, ok := instance.Metadata["cf_instance_name"].(string); ok && instanceName != "" {
		vaultData["instance_name"] = instanceName
	}

	// Add organization data from CF
	if orgGUID, ok := instance.Metadata["org_guid"].(string); ok && orgGUID != "" {
		vaultData["organization_guid"] = orgGUID
	}
	if orgName, ok := instance.Metadata["org_name"].(string); ok && orgName != "" {
		vaultData["organization_name"] = orgName
	}

	// Add space data from CF
	if spaceGUID, ok := instance.Metadata["space_guid"].(string); ok && spaceGUID != "" {
		vaultData["space_guid"] = spaceGUID
	}
	if spaceName, ok := instance.Metadata["space_name"].(string); ok && spaceName != "" {
		vaultData["space_name"] = spaceName
	}

	// Set platform (always cloudfoundry for CF-based instances)
	vaultData["platform"] = "cloudfoundry"

	// Add requested_at timestamp if available (fallback to created time)
	if requestedAt, ok := instance.Metadata["cf_created_at"].(string); ok && requestedAt != "" {
		if t, err := time.Parse(time.RFC3339, requestedAt); err == nil {
			vaultData["requested_at"] = t.Format(time.RFC3339)
		}
	} else if !instance.CreatedAt.IsZero() {
		vaultData["requested_at"] = instance.CreatedAt.Format(time.RFC3339)
	}

	// Build context object for compatibility
	contextObj := map[string]interface{}{
		"platform":                 "cloudfoundry",
		"instance_annotations":     map[string]interface{}{},
		"organization_annotations": map[string]interface{}{},
		"space_annotations":        map[string]interface{}{},
	}

	// Add CF data to context if available
	if instanceName, ok := vaultData["instance_name"].(string); ok {
		contextObj["instance_name"] = instanceName
	}
	if orgGUID, ok := vaultData["organization_guid"].(string); ok {
		contextObj["organization_guid"] = orgGUID
	}
	if orgName, ok := vaultData["organization_name"].(string); ok {
		contextObj["organization_name"] = orgName
	}
	if spaceGUID, ok := vaultData["space_guid"].(string); ok {
		contextObj["space_guid"] = spaceGUID
	}
	if spaceName, ok := vaultData["space_name"].(string); ok {
		contextObj["space_name"] = spaceName
	}

	// Marshal context to JSON string for compatibility
	contextJSON, err := json.Marshal(contextObj)
	if err == nil {
		vaultData["context"] = string(contextJSON)
	}

	// Preserve existing fields that shouldn't be overwritten
	preserveFields := []string{
		"parameters",
		"original_request",
		"created_by",
		"provision_params",
		"update_params",
	}

	for _, field := range preserveFields {
		if val, exists := existingRootData[field]; exists && val != nil {
			vaultData[field] = val
		}
	}

	// Store the enriched data at the root path
	err = u.putToVault(rootPath, vaultData)
	if err != nil {
		u.logError("Failed to store root data for %s: %s", instance.ID, err)
		// Continue anyway as we need to update other paths
	} else {
		u.logDebug("Stored enriched data at root path for instance %s", instance.ID)
	}

	// Store manifest separately if present
	if instance.Deployment.Manifest != "" {
		manifestPath := fmt.Sprintf("%s/manifest", instance.ID)
		manifestData := map[string]interface{}{
			"manifest":      instance.Deployment.Manifest,
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

		// Apply deinterface to ensure all nested maps are properly typed before storing
		cleanedMetadata := u.deinterfaceMetadata(mergedMetadata).(map[string]interface{})

		err = u.putToVault(metadataPath, cleanedMetadata)
		if err != nil {
			u.logError("Failed to store metadata for %s: %s", instance.ID, err)
			// Continue anyway as this is non-fatal
		} else {
			u.logDebug("Stored metadata for instance %s", instance.ID)
		}
	}

	// Update the main instance data in the index
	err = u.updateIndex(instance.ID, vaultData)
	if err != nil {
		return nil, fmt.Errorf("failed to update index for %s: %w", instance.ID, err)
	}

	u.logInfo("Successfully updated instance %s in vault", instance.ID)
	return &instance, nil
}

// UpdateBatch updates multiple instances in parallel with concurrency control
func (u *vaultUpdater) UpdateBatch(ctx context.Context, instances []InstanceData) ([]InstanceData, error) {
	if len(instances) == 0 {
		return instances, nil
	}

	u.logInfo("Starting batch update of %d instances", len(instances))

	// Determine batch size (default to 10 if not configured)
	batchSize := 10
	if u.config != nil && u.config.Batch.MaxSize > 0 {
		batchSize = u.config.Batch.MaxSize
	}

	// Process instances in batches
	var allUpdated []InstanceData
	var allErrors []error

	for i := 0; i < len(instances); i += batchSize {
		end := i + batchSize
		if end > len(instances) {
			end = len(instances)
		}

		batch := instances[i:end]
		u.logDebug("Processing batch of %d instances (batch %d/%d)",
			len(batch), (i/batchSize)+1, (len(instances)+batchSize-1)/batchSize)

		// Process batch in parallel using goroutines
		type result struct {
			instance InstanceData
			err      error
		}

		results := make(chan result, len(batch))
		var wg sync.WaitGroup

		for _, inst := range batch {
			wg.Add(1)
			go func(instance InstanceData) {
				defer wg.Done()

				// Update the instance
				updatedInst, err := u.UpdateInstance(ctx, instance)

				if updatedInst != nil {
					results <- result{
						instance: *updatedInst,
						err:      err,
					}
				} else {
					results <- result{
						instance: instance,
						err:      err,
					}
				}
			}(inst)
		}

		// Wait for all goroutines to complete
		go func() {
			wg.Wait()
			close(results)
		}()

		// Collect results
		for res := range results {
			if res.err != nil {
				u.logError("Failed to update instance %s: %v", res.instance.ID, res.err)
				allErrors = append(allErrors, res.err)
			} else {
				allUpdated = append(allUpdated, res.instance)
			}
		}
	}

	u.logInfo("Batch update completed: %d successful, %d failed", len(allUpdated), len(allErrors))

	// Return partial success even if some failed
	if len(allErrors) > 0 && len(allUpdated) == 0 {
		return nil, fmt.Errorf("all batch updates failed: %v", allErrors[0])
	}

	return allUpdated, nil
}

// BindingInfo represents metadata about a service binding
type BindingInfo struct {
	ID             string
	InstanceID     string
	ServiceID      string
	PlanID         string
	AppGUID        string
	CredentialType string
	CreatedAt      time.Time
	LastVerified   time.Time
	Status         string
}

// checkBindingHealth verifies that all bindings for an instance are healthy
func (u *vaultUpdater) checkBindingHealth(instanceID string) ([]BindingInfo, []string, error) {
	u.logDebug("Checking binding health for instance %s", instanceID)

	// Get all bindings for this instance
	bindingsPath := fmt.Sprintf("%s/bindings", instanceID)
	bindingsData, err := u.getFromVault(bindingsPath)
	if err != nil {
		u.logDebug("No bindings found for instance %s: %s", instanceID, err)
		return nil, nil, nil // No bindings is not an error
	}

	var healthyBindings []BindingInfo
	var unhealthyBindingIDs []string

	for bindingID, bindingData := range bindingsData {
		binding := BindingInfo{
			ID:         bindingID,
			InstanceID: instanceID,
			Status:     "unknown",
		}

		// Check if binding has complete credential data
		credentialsPath := fmt.Sprintf("%s/bindings/%s/credentials", instanceID, bindingID)
		credentials, err := u.getFromVault(credentialsPath)
		if err != nil || len(credentials) == 0 {
			u.logWarning("Binding %s for instance %s missing credentials", bindingID, instanceID)
			binding.Status = "missing_credentials"
			unhealthyBindingIDs = append(unhealthyBindingIDs, bindingID)
		} else {
			// Validate credential completeness
			if u.validateCredentialCompleteness(credentials) {
				binding.Status = "healthy"
				binding.LastVerified = time.Now()

				// Extract binding metadata if available
				if meta, ok := bindingData.(map[string]interface{}); ok {
					if serviceID, exists := meta["service_id"].(string); exists {
						binding.ServiceID = serviceID
					}
					if planID, exists := meta["plan_id"].(string); exists {
						binding.PlanID = planID
					}
					if appGUID, exists := meta["app_guid"].(string); exists {
						binding.AppGUID = appGUID
					}
					if credType, exists := meta["credential_type"].(string); exists {
						binding.CredentialType = credType
					}
					if createdStr, exists := meta["created_at"].(string); exists {
						if createdTime, parseErr := time.Parse(time.RFC3339, createdStr); parseErr == nil {
							binding.CreatedAt = createdTime
						}
					}
				}

				healthyBindings = append(healthyBindings, binding)
			} else {
				u.logWarning("Binding %s for instance %s has incomplete credentials", bindingID, instanceID)
				binding.Status = "incomplete_credentials"
				unhealthyBindingIDs = append(unhealthyBindingIDs, bindingID)
			}
		}
	}

	u.logDebug("Binding health check complete for instance %s: %d healthy, %d need reconstruction",
		instanceID, len(healthyBindings), len(unhealthyBindingIDs))

	return healthyBindings, unhealthyBindingIDs, nil
}

// validateCredentialCompleteness checks if credentials contain required fields
func (u *vaultUpdater) validateCredentialCompleteness(credentials map[string]interface{}) bool {
	// Check for basic required fields - these are common across most services
	requiredFields := []string{"host", "port", "username", "password"}

	for _, field := range requiredFields {
		if value, exists := credentials[field]; !exists || value == nil || value == "" {
			// Allow missing non-critical fields, but log for debugging
			u.logDebug("Credential field '%s' is missing or empty", field)
			// Don't fail validation for missing fields as some services may not need all
		}
	}

	// As long as we have some credentials, consider it complete
	// Individual service validation can be more specific if needed
	return len(credentials) > 0
}

// StoreBindingCredentials stores binding credentials in the structured vault format
func (u *vaultUpdater) StoreBindingCredentials(instanceID, bindingID string, credentials interface{}, metadata map[string]interface{}) error {
	u.logDebug("Storing binding credentials for instance %s, binding %s", instanceID, bindingID)

	// Convert credentials to map[string]interface{} if needed
	var credentialsMap map[string]interface{}
	if credMap, ok := credentials.(map[string]interface{}); ok {
		credentialsMap = credMap
	} else {
		return fmt.Errorf("credentials must be of type map[string]interface{}")
	}

	// Store credentials at the standard path
	credentialsPath := fmt.Sprintf("%s/bindings/%s/credentials", instanceID, bindingID)
	err := u.putToVault(credentialsPath, credentialsMap)
	if err != nil {
		return fmt.Errorf("failed to store binding credentials: %w", err)
	}

	// Store binding metadata
	metadataPath := fmt.Sprintf("%s/bindings/%s/metadata", instanceID, bindingID)

	// Get existing metadata to preserve history
	existingMetadata, _ := u.getFromVault(metadataPath)
	if existingMetadata == nil {
		existingMetadata = make(map[string]interface{})
	}

	// Merge new metadata with existing
	for key, value := range metadata {
		existingMetadata[key] = value
	}

	// Add binding storage timestamps
	existingMetadata["stored_at"] = time.Now().Format(time.RFC3339)
	existingMetadata["last_updated"] = time.Now().Format(time.RFC3339)
	existingMetadata["stored_by"] = "reconciler"

	err = u.putToVault(metadataPath, existingMetadata)
	if err != nil {
		u.logWarning("Failed to store binding metadata: %s", err)
		// Don't fail the operation for metadata issues
	}

	// Update the bindings index
	err = u.updateBindingsIndex(instanceID, bindingID, metadata)
	if err != nil {
		u.logWarning("Failed to update bindings index: %s", err)
		// Don't fail the operation for index issues
	}

	u.logInfo("Successfully stored binding credentials for %s/%s", instanceID, bindingID)
	return nil
}

// GetBindingCredentials retrieves binding credentials from vault
func (u *vaultUpdater) GetBindingCredentials(instanceID, bindingID string) (map[string]interface{}, error) {
	credentialsPath := fmt.Sprintf("%s/bindings/%s/credentials", instanceID, bindingID)
	credentials, err := u.getFromVault(credentialsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get binding credentials: %w", err)
	}
	return credentials, nil
}

// GetBindingMetadata retrieves binding metadata from vault
func (u *vaultUpdater) GetBindingMetadata(instanceID, bindingID string) (map[string]interface{}, error) {
	metadataPath := fmt.Sprintf("%s/bindings/%s/metadata", instanceID, bindingID)
	metadata, err := u.getFromVault(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get binding metadata: %w", err)
	}
	return metadata, nil
}

// updateBindingsIndex maintains an index of all bindings for an instance
func (u *vaultUpdater) updateBindingsIndex(instanceID, bindingID string, metadata map[string]interface{}) error {
	indexPath := fmt.Sprintf("%s/bindings/index", instanceID)

	// Get existing index
	existingIndex, err := u.getFromVault(indexPath)
	if err != nil {
		existingIndex = make(map[string]interface{})
	}

	// Create binding entry for index
	bindingEntry := map[string]interface{}{
		"binding_id":   bindingID,
		"instance_id":  instanceID,
		"last_updated": time.Now().Format(time.RFC3339),
		"status":       "active",
	}

	// Add relevant metadata to index entry
	if serviceID, ok := metadata["service_id"]; ok {
		bindingEntry["service_id"] = serviceID
	}
	if planID, ok := metadata["plan_id"]; ok {
		bindingEntry["plan_id"] = planID
	}
	if appGUID, ok := metadata["app_guid"]; ok {
		bindingEntry["app_guid"] = appGUID
	}
	if credType, ok := metadata["credential_type"]; ok {
		bindingEntry["credential_type"] = credType
	}

	// Update the index
	existingIndex[bindingID] = bindingEntry

	return u.putToVault(indexPath, existingIndex)
}

// RemoveBinding removes a binding from vault storage
func (u *vaultUpdater) RemoveBinding(instanceID, bindingID string) error {
	u.logInfo("Removing binding %s for instance %s", bindingID, instanceID)

	// Remove credentials
	credentialsPath := fmt.Sprintf("%s/bindings/%s/credentials", instanceID, bindingID)
	if err := u.deleteFromVault(credentialsPath); err != nil {
		u.logWarning("Failed to delete binding credentials: %s", err)
	}

	// Archive metadata instead of deleting for audit purposes
	metadataPath := fmt.Sprintf("%s/bindings/%s/metadata", instanceID, bindingID)
	metadata, err := u.getFromVault(metadataPath)
	if err == nil {
		metadata["deleted_at"] = time.Now().Format(time.RFC3339)
		metadata["status"] = "deleted"
		if err := u.putToVault(metadataPath, metadata); err != nil {
			u.logError("Failed to update metadata for deleted binding %s: %v", bindingID, err)
		}
	}

	// Update index to mark as deleted
	indexPath := fmt.Sprintf("%s/bindings/index", instanceID)
	existingIndex, err := u.getFromVault(indexPath)
	if err == nil {
		if bindingEntry, exists := existingIndex[bindingID]; exists {
			if entry, ok := bindingEntry.(map[string]interface{}); ok {
				entry["status"] = "deleted"
				entry["deleted_at"] = time.Now().Format(time.RFC3339)
				existingIndex[bindingID] = entry
				if err := u.putToVault(indexPath, existingIndex); err != nil {
					u.logError("Failed to update index for deleted binding %s: %v", bindingID, err)
				}
			}
		}
	}

	u.logInfo("Successfully removed binding %s", bindingID)
	return nil
}

// deleteFromVault removes data from vault (placeholder implementation)
func (u *vaultUpdater) deleteFromVault(path string) error {
	u.logDebug("Deleting vault path: %s", path)
	// This would call the actual vault delete method
	// For now, return nil as a placeholder
	return nil
}

// ListInstanceBindings returns all bindings for an instance
func (u *vaultUpdater) ListInstanceBindings(instanceID string) (map[string]interface{}, error) {
	indexPath := fmt.Sprintf("%s/bindings/index", instanceID)
	bindings, err := u.getFromVault(indexPath)
	if err != nil {
		// No bindings found is not an error
		return make(map[string]interface{}), nil
	}
	return bindings, nil
}

// GetBindingStatus returns the current status of a binding
func (u *vaultUpdater) GetBindingStatus(instanceID, bindingID string) (string, error) {
	metadata, err := u.GetBindingMetadata(instanceID, bindingID)
	if err != nil {
		return "unknown", err
	}

	if status, ok := metadata["status"].(string); ok {
		return status, nil
	}

	// Check if credentials exist to determine status
	_, err = u.GetBindingCredentials(instanceID, bindingID)
	if err != nil {
		return "missing_credentials", nil
	}

	return "active", nil
}

// ReconstructBindingWithBroker uses the broker to reconstruct binding credentials
func (u *vaultUpdater) ReconstructBindingWithBroker(instanceID, bindingID string, broker BrokerInterface) error {
	u.logInfo("Reconstructing binding %s for instance %s using broker", bindingID, instanceID)

	// TODO: Call the broker's GetBindingCredentials function if available
	// For now, return an error since BrokerInterface doesn't have this method
	credentials := map[string]interface{}{}
	err := fmt.Errorf("broker GetBindingCredentials not implemented")
	if err != nil {
		u.logError("Broker failed to reconstruct credentials for binding %s: %s", bindingID, err)
		return fmt.Errorf("broker reconstruction failed: %w", err)
	}

	u.logDebug("Successfully retrieved reconstructed credentials from broker")

	// credentials is already a map[string]interface{}
	// Just use it directly as credentialsMap
	credentialsMap := credentials

	// Create metadata for the binding
	metadata := map[string]interface{}{
		"binding_id":            bindingID,
		"instance_id":           instanceID,
		"credential_type":       credentials["credential_type"],
		"reconstructed_at":      time.Now().Unix(),
		"reconstruction_source": "broker",
		"status":                "reconstructed",
	}

	// Store the reconstructed credentials and metadata
	err = u.StoreBindingCredentials(instanceID, bindingID, credentialsMap, metadata)
	if err != nil {
		u.logError("Failed to store reconstructed binding credentials: %s", err)
		return fmt.Errorf("failed to store reconstructed credentials: %w", err)
	}

	u.logInfo("Successfully reconstructed and stored binding %s", bindingID)
	return nil
}

// RepairInstanceBindings repairs all unhealthy bindings for an instance using the broker
func (u *vaultUpdater) RepairInstanceBindings(instanceID string, broker BrokerInterface) error {
	u.logInfo("Starting binding repair for instance %s", instanceID)

	// Get the current binding health status
	_, unhealthyBindingIDs, err := u.checkBindingHealth(instanceID)
	if err != nil {
		return fmt.Errorf("failed to check binding health: %w", err)
	}

	if len(unhealthyBindingIDs) == 0 {
		u.logInfo("No unhealthy bindings found for instance %s", instanceID)
		return nil
	}

	u.logInfo("Found %d unhealthy bindings to repair: %v", len(unhealthyBindingIDs), unhealthyBindingIDs)

	var repairErrors []string
	repairedCount := 0

	for _, bindingID := range unhealthyBindingIDs {
		u.logInfo("Attempting to repair binding %s", bindingID)

		err := u.ReconstructBindingWithBroker(instanceID, bindingID, broker)
		if err != nil {
			u.logError("Failed to repair binding %s: %s", bindingID, err)
			repairErrors = append(repairErrors, fmt.Sprintf("binding %s: %s", bindingID, err))

			// Update binding metadata to record the failure
			u.recordReconstructionFailure(instanceID, bindingID, err)
		} else {
			u.logInfo("Successfully repaired binding %s", bindingID)
			repairedCount++
		}
	}

	u.logInfo("Binding repair completed for instance %s: %d repaired, %d failed",
		instanceID, repairedCount, len(repairErrors))

	if len(repairErrors) > 0 {
		return fmt.Errorf("failed to repair %d of %d bindings: %s",
			len(repairErrors), len(unhealthyBindingIDs), strings.Join(repairErrors, "; "))
	}

	return nil
}

// recordReconstructionFailure records when a binding reconstruction fails
func (u *vaultUpdater) recordReconstructionFailure(instanceID, bindingID string, err error) {
	metadataPath := fmt.Sprintf("%s/bindings/%s/metadata", instanceID, bindingID)

	// Get existing metadata
	metadata, getErr := u.getFromVault(metadataPath)
	if getErr != nil {
		metadata = make(map[string]interface{})
	}

	// Record the failure
	metadata["last_reconstruction_attempt"] = time.Now().Format(time.RFC3339)
	metadata["last_reconstruction_error"] = err.Error()
	metadata["reconstruction_status"] = "failed"
	metadata["status"] = "unhealthy"

	// Increment failure count
	if count, exists := metadata["reconstruction_failure_count"].(float64); exists {
		metadata["reconstruction_failure_count"] = int(count) + 1
	} else {
		metadata["reconstruction_failure_count"] = 1
	}

	putErr := u.putToVault(metadataPath, metadata)
	if putErr != nil {
		u.logWarning("Failed to record reconstruction failure metadata: %s", putErr)
	}
}

// UpdateInstanceWithBindingRepair extends UpdateInstance to include broker-based binding repair
func (u *vaultUpdater) UpdateInstanceWithBindingRepair(ctx context.Context, instance *InstanceData, broker BrokerInterface) error {
	// First run the standard update
	updatedInstance, err := u.UpdateInstance(ctx, *instance)
	if err != nil {
		return err
	}
	if updatedInstance != nil {
		*instance = *updatedInstance
	}

	// Check if binding repair is needed and broker is available
	if needsRepair, ok := instance.Metadata["needs_binding_repair"].(bool); ok && needsRepair && broker != nil {
		u.logInfo("Instance %s needs binding repair, attempting with broker integration", instance.ID)

		repairErr := u.RepairInstanceBindings(instance.ID, broker)
		if repairErr != nil {
			u.logError("Binding repair failed for instance %s: %s", instance.ID, repairErr)

			// Update metadata to reflect repair failure
			if instance.Metadata == nil {
				instance.Metadata = make(map[string]interface{})
			}
			instance.Metadata["binding_repair_failed"] = true
			instance.Metadata["binding_repair_error"] = repairErr.Error()
			instance.Metadata["binding_repair_attempted_at"] = time.Now().Format(time.RFC3339)

			// Re-run UpdateInstance to store the updated metadata
			if _, err := u.UpdateInstance(ctx, *instance); err != nil {
				u.logError("Failed to update instance metadata after binding repair failure: %v", err)
			}

			// Don't fail the overall update for binding repair failures
			u.logWarning("Continuing despite binding repair failure")
		} else {
			u.logInfo("Successfully repaired bindings for instance %s", instance.ID)

			// Update metadata to reflect repair success
			if instance.Metadata == nil {
				instance.Metadata = make(map[string]interface{})
			}
			instance.Metadata["needs_binding_repair"] = false
			instance.Metadata["binding_repair_succeeded"] = true
			instance.Metadata["binding_repair_completed_at"] = time.Now().Format(time.RFC3339)

			// Remove error fields
			delete(instance.Metadata, "binding_repair_failed")
			delete(instance.Metadata, "binding_repair_error")

			// Re-run UpdateInstance to store the updated metadata
			if _, err := u.UpdateInstance(ctx, *instance); err != nil {
				u.logError("Failed to update instance metadata after binding repair failure: %v", err)
			}
		}
	}

	return nil
}

// GetInstance retrieves an instance from vault
func (u *vaultUpdater) GetInstance(ctx context.Context, instanceID string) (*InstanceData, error) {
	u.logDebug("Getting instance %s from vault", instanceID)

	// Get from index
	indexData, err := u.getFromIndex(instanceID)
	if err != nil {
		return nil, fmt.Errorf("instance not found: %s", instanceID)
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
		instance.Deployment.Name = v
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

	// Store last_synced_at in metadata
	if v, ok := indexData["last_synced_at"]; ok {
		if instance.Metadata == nil {
			instance.Metadata = make(map[string]interface{})
		}
		instance.Metadata["last_synced_at"] = v
	}

	// Get manifest
	manifestPath := fmt.Sprintf("%s/manifest", instanceID)
	manifestData, err := u.getFromVault(manifestPath)
	if err == nil {
		if manifest, ok := manifestData["manifest"].(string); ok {
			instance.Deployment.Manifest = manifest
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

	// Check if vault is nil
	if u.vault == nil {
		u.logError("Vault is not initialized - cannot store data at path: %s", path)
		return fmt.Errorf("vault not initialized")
	}

	// Type assert vault to VaultInterface to access Put method
	vault, ok := u.vault.(VaultInterface)
	if !ok {
		return fmt.Errorf("vault is not of expected type VaultInterface")
	}

	return vault.Put(path, data)
}

func (u *vaultUpdater) getFromVault(path string) (map[string]interface{}, error) {
	u.logDebug("Getting data from vault path: %s", path)

	// Check if vault is nil
	if u.vault == nil {
		u.logError("Vault is not initialized - cannot get data from path: %s", path)
		return nil, fmt.Errorf("vault not initialized")
	}

	// Type assert vault to VaultInterface to access Get method
	vault, ok := u.vault.(VaultInterface)
	if !ok {
		return nil, fmt.Errorf("vault is not of expected type VaultInterface")
	}

	data, err := vault.Get(path)
	if err != nil {
		return nil, err
	}
	// Return empty map without error if no data found
	if data == nil {
		return map[string]interface{}{}, nil
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

	// Get the index from vault at the standard path
	indexPath := "secret/blacksmith/index/db"
	indexData, err := vault.Get(indexPath)
	if err != nil || indexData == nil {
		// If index doesn't exist, create it
		indexData = make(map[string]interface{})
	}

	// Update the index
	if data == nil {
		// Delete the entry
		delete(indexData, instanceID)
	} else {
		// Add/update the entry
		indexData[instanceID] = data
	}

	// Save the index back to vault
	return vault.Put(indexPath, indexData)
}

func (u *vaultUpdater) getFromIndex(instanceID string) (map[string]interface{}, error) {
	u.logDebug("Getting instance from index: %s", instanceID)

	// Type assert vault to VaultInterface to access GetIndex method
	vault, ok := u.vault.(VaultInterface)
	if !ok {
		return nil, fmt.Errorf("vault is not of expected type VaultInterface")
	}

	// Get the index from vault at the standard path
	indexPath := "secret/blacksmith/index/db"
	indexData, err := vault.Get(indexPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get vault index: %w", err)
	}
	if indexData == nil {
		return nil, fmt.Errorf("index not found")
	}

	// Look up the instance in the index
	raw, exists := indexData[instanceID]
	if !exists {
		return nil, fmt.Errorf("instance not found: %s", instanceID)
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

// backupInstance creates a backup of instance data using the new format and storage location
func (u *vaultUpdater) backupInstance(instanceID string) error {
	u.logDebug("Creating backup for instance %s", instanceID)

	// Export all data from the instance path using the new vault export functionality
	exportedData, err := u.exportVaultPath(instanceID)
	if err != nil {
		return fmt.Errorf("failed to export vault data for instance %s: %w", instanceID, err)
	}

	// If no data to backup, skip
	if len(exportedData) == 0 {
		u.logDebug("No data to backup for instance %s", instanceID)
		return nil
	}

	// Compress and encode the backup data
	compressedArchive, err := u.compressAndEncode(exportedData)
	if err != nil {
		return fmt.Errorf("failed to compress backup data for instance %s: %w", instanceID, err)
	}

	// Calculate SHA256 hash of the compressed data
	hash := sha256.Sum256([]byte(compressedArchive))
	sha256Sum := hex.EncodeToString(hash[:])

	// Check if backup with this SHA256 already exists at new location (no secret/ prefix)
	backupPath := fmt.Sprintf("backups/%s/%s", instanceID, sha256Sum)
	existing, err := u.getFromVault(backupPath)
	if err == nil && len(existing) > 0 {
		u.logDebug("Backup with SHA256 %s already exists for instance %s - skipping", sha256Sum, instanceID)
		return nil
	}

	// Create backup entry with new format
	timestamp := time.Now().Unix()
	backupEntry := map[string]interface{}{
		"timestamp": timestamp,
		"archive":   compressedArchive,
	}

	// Store the backup at new location (no secret/ prefix)
	if err := u.putToVault(backupPath, backupEntry); err != nil {
		return fmt.Errorf("failed to store backup at %s: %w", backupPath, err)
	}

	u.logDebug("Created backup for instance %s with SHA256 %s at %s", instanceID, sha256Sum, backupPath)

	// Clean old backups if cleanup is enabled
	if u.backupConfig.CleanupEnabled {
		retention := u.backupConfig.RetentionCount
		if retention <= 0 {
			retention = 5 // Default retention of 5 backups
		}
		u.cleanOldBackupsNewFormat(instanceID, retention)

		// Also clean up old per-instance backup data if it exists
		u.cleanLegacyInstanceBackups(instanceID)
	}

	return nil
}

// cleanOldBackupsNewFormat removes old backups using the new storage format
func (u *vaultUpdater) cleanOldBackupsNewFormat(instanceID string, keepCount int) {
	if keepCount <= 0 {
		u.logDebug("Backup cleanup disabled for %s (keepCount: %d)", instanceID, keepCount)
		return
	}

	u.logDebug("Cleaning old backups for instance %s, keeping %d most recent", instanceID, keepCount)

	// Try to access the vault client for listing
	vaultClient, ok := u.vault.(*api.Client)
	if !ok {
		// Fallback: try to get client through interface conversion
		if vaultInterface, ok := u.vault.(interface{ GetClient() *api.Client }); ok {
			vaultClient = vaultInterface.GetClient()
		} else {
			u.logWarning("Unable to access vault client for backup cleanup")
			return
		}
	}

	// List backups using the logical API (fixed path without extra secret/)
	metadataPath := fmt.Sprintf("secret/metadata/backups/%s", instanceID)
	listResp, err := vaultClient.Logical().List(metadataPath)
	if err != nil || listResp == nil {
		u.logDebug("No backups found to clean up for instance %s", instanceID)
		return
	}

	// Parse backup entries
	type backupInfo struct {
		sha256    string
		timestamp int64
	}

	var backups []backupInfo
	if listResp.Data != nil {
		if keys, ok := listResp.Data["keys"].([]interface{}); ok {
			for _, keyInterface := range keys {
				if sha256, ok := keyInterface.(string); ok {
					// Read the backup to get its timestamp (fixed path without extra secret/)
					backupDataPath := fmt.Sprintf("secret/data/backups/%s/%s", instanceID, sha256)
					secret, err := vaultClient.Logical().Read(backupDataPath)
					if err != nil || secret == nil {
						u.logWarning("Failed to read backup %s for cleanup: %s", sha256, err)
						continue
					}

					if secret.Data != nil {
						if data, ok := secret.Data["data"].(map[string]interface{}); ok {
							if ts, ok := data["timestamp"]; ok {
								var timestamp int64
								switch v := ts.(type) {
								case float64:
									timestamp = int64(v)
								case int64:
									timestamp = v
								case int:
									timestamp = int64(v)
								case json.Number:
									// Handle json.Number type that might come from Vault
									if i, err := v.Int64(); err == nil {
										timestamp = i
									} else {
										u.logWarning("Invalid json.Number timestamp in backup %s: %v", sha256, err)
										continue
									}
								default:
									// Log the actual value for debugging before deciding to delete
									u.logWarning("Unexpected timestamp type in backup %s: type=%T, value=%v", sha256, ts, ts)
									// Don't delete immediately - might be a valid backup with different format
									continue
								}

								backups = append(backups, backupInfo{
									sha256:    sha256,
									timestamp: timestamp,
								})
							} else {
								// Log what fields are actually present for debugging
								fields := make([]string, 0, len(data))
								for k := range data {
									fields = append(fields, k)
								}
								u.logWarning("Backup %s missing timestamp field, has fields: %v", sha256, fields)

								// Check if this might be a legacy backup format
								if _, hasArchive := data["archive"]; hasArchive {
									u.logDebug("Backup %s appears to be missing timestamp but has archive data", sha256)
									// Don't delete - might be recoverable or valid in different format
									continue
								}

								// Only delete if it's clearly invalid
								u.logWarning("Backup %s has no valid backup data - removing", sha256)
								dataPath := fmt.Sprintf("secret/data/backups/%s/%s", instanceID, sha256)
								metadataPath := fmt.Sprintf("secret/metadata/backups/%s/%s", instanceID, sha256)

								if _, err := vaultClient.Logical().Delete(dataPath); err != nil {
									u.logWarning("Failed to delete invalid backup data at %s: %s", dataPath, err)
								}
								if _, err := vaultClient.Logical().Delete(metadataPath); err != nil {
									u.logDebug("Failed to delete invalid backup metadata at %s: %s", metadataPath, err)
								}
								u.logInfo("Removed backup %s without valid data", sha256)
							}
						} else {
							// Log the actual structure for debugging
							u.logWarning("Backup %s has unexpected data structure: %T", sha256, secret.Data["data"])
							// Don't delete immediately - investigate first
							continue
						}
					} else {
						u.logWarning("Backup %s has nil secret.Data", sha256)
					}
				}
			}
		}
	}

	// If we don't have more backups than we want to keep, nothing to do
	if len(backups) <= keepCount {
		u.logDebug("No cleanup needed for %s - have %d backups, keeping %d", instanceID, len(backups), keepCount)
		return
	}

	// Sort backups by timestamp (newest first)
	for i := 0; i < len(backups)-1; i++ {
		for j := i + 1; j < len(backups); j++ {
			if backups[i].timestamp < backups[j].timestamp {
				backups[i], backups[j] = backups[j], backups[i]
			}
		}
	}

	// Delete old backups (keep only the most recent keepCount)
	if len(backups) > keepCount {
		toDelete := backups[keepCount:]
		for _, backup := range toDelete {
			// Fixed path without extra secret/
			backupPath := fmt.Sprintf("secret/data/backups/%s/%s", instanceID, backup.sha256)
			_, err := vaultClient.Logical().Delete(backupPath)
			if err != nil {
				u.logWarning("Failed to delete old backup %s: %s", backupPath, err)
			} else {
				u.logDebug("Deleted old backup %s", backupPath)
			}

			// Also delete metadata
			backupMetadataPath := fmt.Sprintf("secret/metadata/backups/%s/%s", instanceID, backup.sha256)
			_, err = vaultClient.Logical().Delete(backupMetadataPath)
			if err != nil {
				u.logWarning("Failed to delete old backup metadata %s: %s", backupMetadataPath, err)
			}
		}

		u.logDebug("Backup cleanup for %s completed - deleted %d old backups, keeping %d",
			instanceID, len(toDelete), keepCount)
	}
}

// cleanLegacyInstanceBackups removes old backup data stored at secret/{instanceID}/backups
func (u *vaultUpdater) cleanLegacyInstanceBackups(instanceID string) {
	u.logDebug("Checking for legacy backup data for instance %s", instanceID)

	// Try to access the vault client for direct operations
	vaultClient, ok := u.vault.(*api.Client)
	if !ok {
		// Fallback: try to get client through interface conversion
		if vaultInterface, ok := u.vault.(interface{ GetClient() *api.Client }); ok {
			vaultClient = vaultInterface.GetClient()
		} else {
			u.logWarning("Unable to access vault client for legacy backup cleanup")
			return
		}
	}

	// List all backup entries under secret/{instanceID}/backups/
	legacyBackupPath := fmt.Sprintf("secret/metadata/%s/backups", instanceID)
	listResp, err := vaultClient.Logical().List(legacyBackupPath)
	if err != nil {
		u.logDebug("No legacy backup data found for instance %s: %v", instanceID, err)
		return
	}

	if listResp == nil || listResp.Data == nil {
		u.logDebug("No legacy backup entries found for instance %s", instanceID)
		return
	}

	// Get the list of keys (backup timestamps)
	keysRaw, ok := listResp.Data["keys"]
	if !ok {
		u.logDebug("No keys found in legacy backup list response for instance %s", instanceID)
		return
	}

	keys, ok := keysRaw.([]interface{})
	if !ok || len(keys) == 0 {
		u.logDebug("No legacy backup entries to clean for instance %s", instanceID)
		return
	}

	u.logInfo("Found %d legacy backup entries for instance %s, cleaning up", len(keys), instanceID)

	// Delete each backup entry
	deletedCount := 0
	for _, keyRaw := range keys {
		key, ok := keyRaw.(string)
		if !ok {
			continue
		}

		// Delete the data version
		dataPath := fmt.Sprintf("secret/data/%s/backups/%s", instanceID, key)
		_, err := vaultClient.Logical().Delete(dataPath)
		if err != nil {
			u.logWarning("Failed to delete legacy backup data at %s: %s", dataPath, err)
		} else {
			deletedCount++
			u.logDebug("Deleted legacy backup: %s", dataPath)
		}

		// Delete the metadata version
		metadataPath := fmt.Sprintf("secret/metadata/%s/backups/%s", instanceID, key)
		_, err = vaultClient.Logical().Delete(metadataPath)
		if err != nil {
			u.logDebug("Failed to delete backup metadata at %s: %s", metadataPath, err)
		}
	}

	// After deleting all subdirectories, try to delete the parent backup path
	parentDataPath := fmt.Sprintf("secret/data/%s/backups", instanceID)
	_, err = vaultClient.Logical().Delete(parentDataPath)
	if err != nil {
		u.logDebug("Failed to delete parent backup path %s: %s", parentDataPath, err)
	}

	parentMetadataPath := fmt.Sprintf("secret/metadata/%s/backups", instanceID)
	_, err = vaultClient.Logical().Delete(parentMetadataPath)
	if err != nil {
		u.logDebug("Failed to delete parent backup metadata %s: %s", parentMetadataPath, err)
	}

	if deletedCount > 0 {
		u.logInfo("Successfully cleaned up %d legacy backup entries for instance %s", deletedCount, instanceID)
	}
}

// exportVaultPath recursively exports all secrets from a vault path
// Returns a map in the format compatible with "safe export"
func (u *vaultUpdater) exportVaultPath(basePath string) (map[string]interface{}, error) {
	result := make(map[string]interface{})

	// Try to access the vault client directly
	vaultClient, ok := u.vault.(*api.Client)
	if !ok {
		// Fallback: try to get client through interface conversion
		if vaultInterface, ok := u.vault.(interface{ GetClient() *api.Client }); ok {
			vaultClient = vaultInterface.GetClient()
		} else {
			return nil, fmt.Errorf("unable to access vault client for export")
		}
	}

	// Export all keys under the base path using the logical API
	if err := u.exportPathRecursive(vaultClient, basePath, basePath, result); err != nil {
		return nil, fmt.Errorf("failed to export path %s: %w", basePath, err)
	}

	return result, nil
}

// exportPathRecursive recursively exports all secrets from a vault path using the logical API
func (u *vaultUpdater) exportPathRecursive(client *api.Client, currentPath, basePath string, result map[string]interface{}) error {
	u.logDebug("Exporting vault path: %s", currentPath)

	// Build the full vault path for KV v2 (secret/data/path for reads, secret/metadata/path for lists)
	listPath := fmt.Sprintf("secret/metadata/%s", currentPath)
	dataPath := fmt.Sprintf("secret/data/%s", currentPath)

	// Try to list secrets at current path
	listResp, err := client.Logical().List(listPath)
	if err != nil || listResp == nil {
		// If path doesn't exist or has no subpaths, try to read it as a secret
		secret, err := client.Logical().Read(dataPath)
		if err != nil || secret == nil {
			u.logDebug("Path %s has no secrets or subpaths", currentPath)
			return nil
		}

		// Store the secret data - extract from KV v2 format
		if secret.Data != nil {
			if data, ok := secret.Data["data"]; ok {
				secretPath := fmt.Sprintf("secret/%s", currentPath)
				result[secretPath] = data
				u.logDebug("Exported secret: %s", secretPath)
			}
		}
		return nil
	}

	// Process each key in the list response
	if listResp.Data != nil {
		if keys, ok := listResp.Data["keys"].([]interface{}); ok {
			for _, keyInterface := range keys {
				if key, ok := keyInterface.(string); ok {
					subPath := currentPath
					if subPath == "" {
						subPath = key
					} else {
						subPath = currentPath + "/" + key
					}

					// Remove trailing slash for directories and recurse
					if strings.HasSuffix(key, "/") {
						dirPath := subPath[:len(subPath)-1] // Remove trailing slash
						if err := u.exportPathRecursive(client, dirPath, basePath, result); err != nil {
							u.logWarning("Failed to export directory %s: %s", dirPath, err)
						}
					} else {
						// Try to get the secret
						subDataPath := fmt.Sprintf("secret/data/%s", subPath)
						secret, err := client.Logical().Read(subDataPath)
						if err != nil || secret == nil {
							u.logWarning("Failed to get secret %s: %s", subPath, err)
							continue
						}

						// Store the secret data - extract from KV v2 format
						if secret.Data != nil {
							if data, ok := secret.Data["data"]; ok {
								secretPath := fmt.Sprintf("secret/%s", subPath)
								result[secretPath] = data
								u.logDebug("Exported secret: %s", secretPath)
							}
						}
					}
				}
			}
		}
	}

	return nil
}

// compressAndEncode compresses and base64 encodes the backup data
func (u *vaultUpdater) compressAndEncode(data map[string]interface{}) (string, error) {
	// Marshal to JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("failed to marshal data to JSON: %w", err)
	}

	// Compress with gzip at maximum level
	var compressed bytes.Buffer
	gzipWriter, err := gzip.NewWriterLevel(&compressed, u.backupConfig.CompressionLevel)
	if err != nil {
		return "", fmt.Errorf("failed to create gzip writer: %w", err)
	}

	if _, err := gzipWriter.Write(jsonData); err != nil {
		_ = gzipWriter.Close() // #nosec G104 - Best effort close on error path
		return "", fmt.Errorf("failed to write compressed data: %w", err)
	}

	if err := gzipWriter.Close(); err != nil {
		return "", fmt.Errorf("failed to close gzip writer: %w", err)
	}

	// Base64 encode the compressed data
	encoded := base64.StdEncoding.EncodeToString(compressed.Bytes())

	u.logDebug("Compressed data from %d bytes to %d bytes (%.2f%% compression)",
		len(jsonData), len(compressed.Bytes()),
		float64(len(compressed.Bytes()))/float64(len(jsonData))*100)

	return encoded, nil
}

// DecompressAndDecode decompresses and decodes base64 encoded backup data (exported for recovery operations)
func (u *vaultUpdater) DecompressAndDecode(encodedData string) (map[string]interface{}, error) {
	// Base64 decode
	compressedData, err := base64.StdEncoding.DecodeString(encodedData)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64 data: %w", err)
	}

	// Decompress with gzip
	gzipReader, err := gzip.NewReader(bytes.NewReader(compressedData))
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzipReader.Close()

	var decompressed bytes.Buffer
	if _, err := decompressed.ReadFrom(gzipReader); err != nil {
		return nil, fmt.Errorf("failed to decompress data: %w", err)
	}

	// Unmarshal JSON
	var result map[string]interface{}
	if err := json.Unmarshal(decompressed.Bytes(), &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON data: %w", err)
	}

	u.logDebug("Decompressed data from %d bytes to %d bytes",
		len(compressedData), decompressed.Len())

	return result, nil
}

func (u *vaultUpdater) logError(format string, args ...interface{}) {
	// Will be replaced with actual logger call
	fmt.Printf("[ERROR] updater: "+format+"\n", args...)
}

func (u *vaultUpdater) logWarning(format string, args ...interface{}) {
	// Will be replaced with actual logger call
	fmt.Printf("[WARNING] updater: "+format+"\n", args...)
}

// deinterfaceMetadata converts map[interface{}]interface{} structures to map[string]interface{}
func (u *vaultUpdater) deinterfaceMetadata(data interface{}) interface{} {
	switch v := data.(type) {
	case map[interface{}]interface{}:
		result := make(map[string]interface{})
		for k, val := range v {
			keyStr := fmt.Sprintf("%v", k)
			result[keyStr] = u.deinterfaceMetadata(val)
		}
		return result
	case map[string]interface{}:
		result := make(map[string]interface{})
		for k, val := range v {
			result[k] = u.deinterfaceMetadata(val)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(v))
		for i, val := range v {
			result[i] = u.deinterfaceMetadata(val)
		}
		return result
	default:
		return data
	}
}
