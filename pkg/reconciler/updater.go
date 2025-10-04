package reconciler

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"blacksmith/pkg/vault"
	"github.com/hashicorp/vault/api"
)

// Static errors for err113 compliance.
var (
	ErrMissingCredentials = errors.New("missing credentials")
)

const (
	sourceReconciler = "reconciler"
)

var (
	ErrInstanceIDCannotBeEmpty             = errors.New("instance ID cannot be empty")
	ErrDeinterfaceMetadataUnexpectedType   = errors.New("deinterfaceMetadata returned unexpected type")
	ErrCredentialsMustBeMap                = errors.New("credentials must be of type map[string]interface{}")
	ErrBrokerNotAvailableForReconstruction = errors.New("broker not available for reconstruction")
	ErrBrokerReturnedNoCredentials         = errors.New("broker returned no credentials")
	ErrFailedToRepairBindings              = errors.New("failed to repair bindings")
	ErrInstanceNotFound                    = errors.New("instance not found")
	ErrIndexNotFound                       = errors.New("index not found")
	ErrInvalidDataTypeForInstance          = errors.New("invalid data type for instance")
	ErrUnableToAccessVaultClientForExport  = errors.New("unable to access vault client for export")
)

type VaultUpdater struct {
	vault        interface{} // Will be replaced with actual Vault type
	logger       Logger
	backupConfig BackupConfig
	cfManager    CFManagerInterface // For VCAP credential recovery
	config       *ReconcilerConfig  // For batch configuration
}

// NewVaultUpdater creates a new vault updater.
func NewVaultUpdater(vault interface{}, logger Logger, backupConfig BackupConfig) *VaultUpdater {
	return &VaultUpdater{
		vault:        vault,
		logger:       logger,
		backupConfig: backupConfig,
	}
}

// NewVaultUpdaterWithCF creates a new vault updater with CF manager for VCAP recovery.
func NewVaultUpdaterWithCF(vault interface{}, logger Logger, backupConfig BackupConfig, cfManager CFManagerInterface) *VaultUpdater {
	return &VaultUpdater{
		vault:        vault,
		logger:       logger,
		backupConfig: backupConfig,
		cfManager:    cfManager,
	}
}

func (u *VaultUpdater) UpdateInstance(ctx context.Context, instance InstanceData) (*InstanceData, error) {
	u.logger.Debugf("Updating instance %s in vault", instance.ID)

	// Validate instance ID
	err := u.validateInstanceID(instance.ID)
	if err != nil {
		return nil, err
	}

	// Create backup if enabled
	u.createBackupIfEnabled(instance.ID)

	// Initialize instance metadata if needed
	if instance.Metadata == nil {
		instance.Metadata = make(map[string]interface{})
	}

	// Process credentials and bindings
	err = u.processCredentialsAndBindings(ctx, &instance)
	if err != nil {
		return &instance, err
	}

	// Store all instance data
	err = u.storeInstanceData(instance)
	if err != nil {
		return nil, err
	}

	u.logger.Infof("Successfully updated instance %s in vault", instance.ID)

	return &instance, nil
}

// UpdateBatch updates multiple instances in parallel with concurrency control.
//
//nolint:funlen
func (u *VaultUpdater) UpdateBatch(ctx context.Context, instances []InstanceData) ([]InstanceData, error) {
	if len(instances) == 0 {
		return instances, nil
	}

	u.logger.Infof("Starting batch update of %d instances", len(instances))

	// Determine batch size (default to 10 if not configured)
	batchSize := 10
	if u.config != nil && u.config.Batch.MaxSize > 0 {
		batchSize = u.config.Batch.MaxSize
	}

	// Process instances in batches
	var (
		allUpdated []InstanceData
		allErrors  []error
	)

	for index := 0; index < len(instances); index += batchSize {
		end := index + batchSize
		if end > len(instances) {
			end = len(instances)
		}

		batch := instances[index:end]
		u.logger.Debugf("Processing batch of %d instances (batch %d/%d)",
			len(batch), (index/batchSize)+1, (len(instances)+batchSize-1)/batchSize)

		// Process batch in parallel using goroutines
		type result struct {
			instance InstanceData
			err      error
		}

		results := make(chan result, len(batch))

		var waitGroup sync.WaitGroup

		for _, inst := range batch {
			waitGroup.Add(1)

			go func(instance InstanceData) {
				defer waitGroup.Done()

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
			waitGroup.Wait()
			close(results)
		}()

		// Collect results
		for res := range results {
			if res.err != nil {
				u.logger.Errorf("Failed to update instance %s: %v", res.instance.ID, res.err)
				allErrors = append(allErrors, res.err)
			} else {
				allUpdated = append(allUpdated, res.instance)
			}
		}
	}

	u.logger.Infof("Batch update completed: %d successful, %d failed", len(allUpdated), len(allErrors))

	// Return partial success even if some failed
	if len(allErrors) > 0 && len(allUpdated) == 0 {
		return nil, fmt.Errorf("all batch updates failed: %w", allErrors[0])
	}

	return allUpdated, nil
}

// CheckBindingHealth verifies that all bindings for an instance are healthy.
func (u *VaultUpdater) CheckBindingHealth(instanceID string) ([]BindingInfo, []string, error) {
	u.logger.Debugf("Checking binding health for instance %s", instanceID)

	bindingsData, err := u.getBindingsData(instanceID)
	if err != nil {
		return nil, nil, nil //nolint:nilerr // No bindings is not an error condition
	}

	var (
		healthyBindings     []BindingInfo
		unhealthyBindingIDs []string
	)

	for bindingID, bindingData := range bindingsData {
		binding := u.createBaseBinding(instanceID, bindingID)

		credentials, err := u.getBindingCredentials(instanceID, bindingID)
		if err != nil {
			unhealthyBindingIDs = append(unhealthyBindingIDs, bindingID)

			continue
		}

		if u.validateCredentialCompleteness(credentials) {
			u.processHealthyBinding(&binding, bindingData)
			healthyBindings = append(healthyBindings, binding)
		} else {
			u.processUnhealthyBinding(&binding, bindingID, instanceID)
			unhealthyBindingIDs = append(unhealthyBindingIDs, bindingID)
		}
	}

	u.logger.Debugf("Binding health check complete for instance %s: %d healthy, %d need reconstruction",
		instanceID, len(healthyBindings), len(unhealthyBindingIDs))

	return healthyBindings, unhealthyBindingIDs, nil
}

// StoreBindingCredentials stores binding credentials in the structured vault format.
func (u *VaultUpdater) StoreBindingCredentials(instanceID, bindingID string, credentials interface{}, metadata map[string]interface{}) error {
	u.logger.Debugf("Storing binding credentials for instance %s, binding %s", instanceID, bindingID)

	// Convert credentials to map[string]interface{} if needed
	var credentialsMap map[string]interface{}
	if credMap, ok := credentials.(map[string]interface{}); ok {
		credentialsMap = credMap
	} else {
		return ErrCredentialsMustBeMap
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
	existingMetadata["stored_by"] = sourceReconciler

	err = u.putToVault(metadataPath, existingMetadata)
	if err != nil {
		u.logger.Warningf("Failed to store binding metadata: %s", err)
		// Don't fail the operation for metadata issues
	}

	// Update the bindings index
	err = u.updateBindingsIndex(instanceID, bindingID, metadata)
	if err != nil {
		u.logger.Warningf("Failed to update bindings index: %s", err)
		// Don't fail the operation for index issues
	}

	u.logger.Infof("Successfully stored binding credentials for %s/%s", instanceID, bindingID)

	return nil
}

// GetBindingCredentials retrieves binding credentials from vault.
func (u *VaultUpdater) GetBindingCredentials(instanceID, bindingID string) (map[string]interface{}, error) {
	credentialsPath := fmt.Sprintf("%s/bindings/%s/credentials", instanceID, bindingID)

	credentials, err := u.getFromVault(credentialsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get binding credentials: %w", err)
	}

	return credentials, nil
}

// GetBindingMetadata retrieves binding metadata from vault.
func (u *VaultUpdater) GetBindingMetadata(instanceID, bindingID string) (map[string]interface{}, error) {
	metadataPath := fmt.Sprintf("%s/bindings/%s/metadata", instanceID, bindingID)

	metadata, err := u.getFromVault(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get binding metadata: %w", err)
	}

	return metadata, nil
}

// RemoveBinding removes a binding from vault storage.
func (u *VaultUpdater) RemoveBinding(instanceID, bindingID string) error {
	u.logger.Infof("Removing binding %s for instance %s", bindingID, instanceID)

	// Remove credentials
	credentialsPath := fmt.Sprintf("%s/bindings/%s/credentials", instanceID, bindingID)
	u.deleteFromVault(credentialsPath)
	u.logger.Warningf("Removed binding credentials (if they existed)")

	// Archive metadata instead of deleting for audit purposes
	metadataPath := fmt.Sprintf("%s/bindings/%s/metadata", instanceID, bindingID)

	metadata, err := u.getFromVault(metadataPath)
	if err == nil {
		metadata["deleted_at"] = time.Now().Format(time.RFC3339)
		metadata["status"] = StatusDeleted

		err := u.putToVault(metadataPath, metadata)
		if err != nil {
			u.logger.Errorf("Failed to update metadata for deleted binding %s: %v", bindingID, err)
		}
	}

	// Update index to mark as deleted
	indexPath := instanceID + "/bindings/index"

	existingIndex, err := u.getFromVault(indexPath)
	if err == nil {
		if bindingEntry, exists := existingIndex[bindingID]; exists {
			if entry, ok := bindingEntry.(map[string]interface{}); ok {
				entry["status"] = StatusDeleted
				entry["deleted_at"] = time.Now().Format(time.RFC3339)
				existingIndex[bindingID] = entry

				err := u.putToVault(indexPath, existingIndex)
				if err != nil {
					u.logger.Errorf("Failed to update index for deleted binding %s: %v", bindingID, err)
				}
			}
		}
	}

	u.logger.Infof("Successfully removed binding %s", bindingID)

	return nil
}

// ListInstanceBindings returns all bindings for an instance.
func (u *VaultUpdater) ListInstanceBindings(instanceID string) (map[string]interface{}, error) {
	indexPath := instanceID + "/bindings/index"

	bindings, err := u.getFromVault(indexPath)
	if err != nil {
		// No bindings found is not an error - ignore the error and return empty map
		return make(map[string]interface{}), nil //nolint:nilerr // No bindings found is not an error condition
	}

	return bindings, nil
}

// GetBindingStatus returns the current status of a binding.
func (u *VaultUpdater) GetBindingStatus(instanceID, bindingID string) (string, error) {
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
		// Credentials not found indicates missing credentials status - ignore the error
		return "missing_credentials", nil //nolint:nilerr // Missing credentials is a valid status, not an error
	}

	return "active", nil
}

// ReconstructBindingWithBroker uses the broker to reconstruct binding credentials.
func (u *VaultUpdater) ReconstructBindingWithBroker(instanceID, bindingID string, broker BrokerInterface) error {
	u.logger.Infof("Reconstructing binding %s for instance %s using broker", bindingID, instanceID)

	if broker == nil {
		return ErrBrokerNotAvailableForReconstruction
	}

	bindingCreds, err := u.getCredentialsFromBroker(broker, instanceID, bindingID)
	if err != nil {
		return err
	}

	creds := u.buildCredentialsMap(bindingCreds)

	metadata := u.buildReconstructionMetadata(instanceID, bindingID, bindingCreds)

	// Store the reconstructed credentials and metadata using standard layout
	err = u.StoreBindingCredentials(instanceID, bindingID, creds, metadata)
	if err != nil {
		u.logger.Errorf("Failed to store reconstructed binding credentials: %s", err)

		return fmt.Errorf("failed to store reconstructed credentials: %w", err)
	}

	u.logger.Infof("Successfully reconstructed and stored binding %s", bindingID)

	return nil
}

// RepairInstanceBindings repairs all unhealthy bindings for an instance using the broker.
func (u *VaultUpdater) RepairInstanceBindings(instanceID string, broker BrokerInterface) error {
	u.logger.Infof("Starting binding repair for instance %s", instanceID)

	// Get the current binding health status
	_, unhealthyBindingIDs, err := u.CheckBindingHealth(instanceID)
	if err != nil {
		return fmt.Errorf("failed to check binding health: %w", err)
	}

	if len(unhealthyBindingIDs) == 0 {
		u.logger.Infof("No unhealthy bindings found for instance %s", instanceID)

		return nil
	}

	u.logger.Infof("Found %d unhealthy bindings to repair: %v", len(unhealthyBindingIDs), unhealthyBindingIDs)

	var repairErrors []string

	repairedCount := 0

	for _, bindingID := range unhealthyBindingIDs {
		u.logger.Infof("Attempting to repair binding %s", bindingID)

		err := u.ReconstructBindingWithBroker(instanceID, bindingID, broker)
		if err != nil {
			u.logger.Errorf("Failed to repair binding %s: %s", bindingID, err)
			repairErrors = append(repairErrors, fmt.Sprintf("binding %s: %s", bindingID, err))

			// Update binding metadata to record the failure
			u.recordReconstructionFailure(instanceID, bindingID, err)
		} else {
			u.logger.Infof("Successfully repaired binding %s", bindingID)

			repairedCount++
		}
	}

	u.logger.Infof("Binding repair completed for instance %s: %d repaired, %d failed",
		instanceID, repairedCount, len(repairErrors))

	if len(repairErrors) > 0 {
		return fmt.Errorf("%w: %d of %d bindings: %s",
			ErrFailedToRepairBindings, len(repairErrors), len(unhealthyBindingIDs), strings.Join(repairErrors, "; "))
	}

	return nil
}

// UpdateInstanceWithBindingRepair extends UpdateInstance to include broker-based binding repair.
func (u *VaultUpdater) UpdateInstanceWithBindingRepair(ctx context.Context, instance InstanceData, broker BrokerInterface) (*InstanceData, error) {
	// First run the standard update
	updatedInstance, err := u.UpdateInstance(ctx, instance)
	if err != nil {
		return nil, err
	}

	if updatedInstance != nil {
		instance = *updatedInstance
	}

	// Check if binding repair is needed and broker is available
	if needsRepair, ok := instance.Metadata["needs_binding_repair"].(bool); ok && needsRepair && broker != nil {
		u.logger.Infof("Instance %s needs binding repair, attempting with broker integration", instance.ID)
		u.handleBindingRepair(ctx, &instance, broker)
	}

	return &instance, nil
}

// GetInstance retrieves an instance from vault.
func (u *VaultUpdater) GetInstance(ctx context.Context, instanceID string) (*InstanceData, error) {
	u.logger.Debugf("Getting instance %s from vault", instanceID)

	// Get from index
	indexData, err := u.GetFromIndex(instanceID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInstanceNotFound, instanceID)
	}

	instance := &InstanceData{
		ID:       instanceID,
		Metadata: make(map[string]interface{}),
	}

	// Parse basic fields
	u.parseBasicFields(instance, indexData)

	// Parse timestamps
	u.parseTimestamps(instance, indexData)

	// Store last_synced_at in metadata
	u.parseLastSyncedAt(instance, indexData)

	// Get manifest and metadata
	u.loadManifestAndMetadata(instance, instanceID)

	return instance, nil
}

// MergeMetadata merges existing and new metadata.
func (u *VaultUpdater) MergeMetadata(existing, newData map[string]interface{}) map[string]interface{} {
	merged := make(map[string]interface{})

	// Copy existing metadata
	for k, v := range existing {
		merged[k] = v
		if k == "history" {
			u.logger.Debugf("MergeMetadata: Copying existing history with %v entries", v)
		}
	}

	// Override with new metadata, but handle special cases
	for key, value := range newData {
		u.logger.Debugf("MergeMetadata: Processing new key %s with value type %T", key, value)

		if key == "history" {
			// Special handling for history field - append instead of replace
			if existingHistory, ok := merged["history"].([]interface{}); ok {
				if newHistory, ok := value.([]interface{}); ok {
					// Append new history entries to existing ones
					merged["history"] = append(existingHistory, newHistory...)
				} else {
					// If new value is not an array, just append it as a single entry
					merged["history"] = append(existingHistory, value)
				}
			} else {
				// No existing history, use the new value
				merged["history"] = value
			}
		} else {
			// For all other fields, override with new value
			merged[key] = value
		}
	}

	u.logger.Debugf("MergeMetadata: Final merged has history: %v", merged["history"] != nil)

	if merged["history"] != nil {
		if h, ok := merged["history"].([]interface{}); ok {
			u.logger.Debugf("MergeMetadata: Final merged history has %d entries", len(h))
		}
	}

	return merged
}

// DetectChanges compares old and new metadata to detect what changed.
func (u *VaultUpdater) DetectChanges(old, newData map[string]interface{}) map[string]interface{} {
	changes := make(map[string]interface{})

	// Check for changes in specific fields
	fieldsToCheck := []string{"releases", "stemcells", "vms"}
	for _, field := range fieldsToCheck {
		changedKey := field + "_changed"
		changes[changedKey] = u.hasFieldChanged(old, newData, field)
	}

	// Track field additions/removals
	u.trackFieldChanges(changes, old, newData)

	return changes
}

func (u *VaultUpdater) UpdateIndex(instanceID string, data map[string]interface{}) error {
	u.logger.Debugf("Updating index for instance: %s", instanceID)

	// Type assert vault to VaultInterface to access GetIndex method
	vault, ok := u.vault.(VaultInterface)
	if !ok {
		return ErrVaultNotExpectedType
	}

	// Get the index from vault at the canonical path
	indexPath := "db"

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
	err = vault.Put(indexPath, indexData)
	if err != nil {
		return fmt.Errorf("failed to update vault index: %w", err)
	}

	return nil
}

func (u *VaultUpdater) GetFromIndex(instanceID string) (map[string]interface{}, error) {
	u.logger.Debugf("Getting instance from index: %s", instanceID)

	// Type assert vault to VaultInterface to access GetIndex method
	vault, valid := u.vault.(VaultInterface)
	if !valid {
		return nil, ErrVaultNotExpectedType
	}

	// Get the index from vault at the canonical path
	indexPath := "db"

	indexData, err := vault.Get(indexPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get vault index: %w", err)
	}

	if indexData == nil {
		return nil, ErrIndexNotFound
	}

	// Look up the instance in the index
	raw, exists := indexData[instanceID]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrInstanceNotFound, instanceID)
	}

	// Convert raw data to map
	data, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%w %s", ErrInvalidDataTypeForInstance, instanceID)
	}

	return data, nil
}

// backupInfo represents backup metadata for cleanup operations.
type backupInfo struct {
	sha256    string
	timestamp int64
}

// DecompressAndDecode decompresses and decodes base64 encoded backup data (exported for recovery operations).
func (u *VaultUpdater) DecompressAndDecode(encodedData string) (map[string]interface{}, error) {
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

	defer func() { _ = gzipReader.Close() }()

	var decompressed bytes.Buffer

	_, err = decompressed.ReadFrom(gzipReader)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress data: %w", err)
	}

	// Unmarshal JSON
	var result map[string]interface{}

	err = json.Unmarshal(decompressed.Bytes(), &result)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON data: %w", err)
	}

	u.logger.Debugf("Decompressed data from %d bytes to %d bytes",
		len(compressedData), decompressed.Len())

	return result, nil
}

// UpdateInstance updates an instance in vault.
// handleBindingRepair handles binding repair if needed.
func (u *VaultUpdater) handleBindingRepair(ctx context.Context, instance *InstanceData, broker BrokerInterface) {
	repairErr := u.RepairInstanceBindings(instance.ID, broker)
	if repairErr != nil {
		u.logger.Errorf("Binding repair failed for instance %s: %s", instance.ID, repairErr)
		u.updateRepairFailureMetadata(ctx, instance, repairErr)
		u.logger.Warningf("Continuing despite binding repair failure")
	} else {
		u.logger.Infof("Successfully repaired bindings for instance %s", instance.ID)
		u.updateRepairSuccessMetadata(ctx, instance)
	}
}

// updateRepairFailureMetadata updates metadata after binding repair failure.
func (u *VaultUpdater) updateRepairFailureMetadata(ctx context.Context, instance *InstanceData, repairErr error) {
	if instance.Metadata == nil {
		instance.Metadata = make(map[string]interface{})
	}

	instance.Metadata["binding_repair_failed"] = true
	instance.Metadata["binding_repair_error"] = repairErr.Error()
	instance.Metadata["binding_repair_attempted_at"] = time.Now().Format(time.RFC3339)

	_, err := u.UpdateInstance(ctx, *instance)
	if err != nil {
		u.logger.Errorf("Failed to update instance metadata after binding repair failure: %v", err)
	}
}

// updateRepairSuccessMetadata updates metadata after successful binding repair.
func (u *VaultUpdater) updateRepairSuccessMetadata(ctx context.Context, instance *InstanceData) {
	if instance.Metadata == nil {
		instance.Metadata = make(map[string]interface{})
	}

	delete(instance.Metadata, "needs_binding_repair")
	delete(instance.Metadata, "binding_repair_failed")
	delete(instance.Metadata, "binding_repair_error")
	instance.Metadata["binding_repair_completed_at"] = time.Now().Format(time.RFC3339)

	_, err := u.UpdateInstance(ctx, *instance)
	if err != nil {
		u.logger.Errorf("Failed to update instance metadata after binding repair success: %v", err)
	}
}

// validateInstanceID validates that the instance ID is not empty.
func (u *VaultUpdater) validateInstanceID(instanceID string) error {
	if instanceID == "" {
		u.logger.Errorf("Cannot update instance with empty ID")

		return ErrInstanceIDCannotBeEmpty
	}

	return nil
}

// createBackupIfEnabled creates a backup of the instance if backup is enabled.
func (u *VaultUpdater) createBackupIfEnabled(instanceID string) {
	if u.backupConfig.Enabled {
		err := u.backupInstance(instanceID)
		if err != nil {
			u.logger.Warningf("Failed to create backup for instance %s: %s (continuing anyway)", instanceID, err)
		}
	}
}

// processCredentialsAndBindings handles credential recovery and binding health checks.
func (u *VaultUpdater) processCredentialsAndBindings(ctx context.Context, instance *InstanceData) error {
	// Process credentials
	err := u.processInstanceCredentials(ctx, instance)
	if err != nil {
		return err
	}

	// Process bindings
	return u.processInstanceBindings(instance)
}

// processInstanceCredentials handles credential checking and recovery.
func (u *VaultUpdater) processInstanceCredentials(ctx context.Context, instance *InstanceData) error {
	credsPath := instance.ID + "/credentials"
	existingCreds, credsErr := u.getFromVault(credsPath)
	hasCredentials := credsErr == nil && len(existingCreds) > 0

	instance.Metadata["has_credentials"] = hasCredentials

	if hasCredentials {
		u.logger.Debugf("Instance %s has existing credentials - preserving", instance.ID)

		return nil
	}

	// Attempt credential recovery
	u.logger.Warningf("Instance %s is missing credentials - will attempt recovery", instance.ID)
	instance.Metadata["needs_credential_recovery"] = true

	return u.attemptVCAPRecovery(ctx, instance)
}

// attemptVCAPRecovery attempts to recover credentials from VCAP services.
func (u *VaultUpdater) attemptVCAPRecovery(ctx context.Context, instance *InstanceData) error {
	if u.cfManager == nil || u.vault == nil {
		return nil
	}

	vaultInterface, ok := u.vault.(VaultInterface)
	if !ok || vaultInterface == nil {
		u.logger.Warningf("Vault interface not available for VCAP recovery")

		return nil
	}

	u.logger.Infof("Attempting VCAP credential recovery for instance %s", instance.ID)
	vcapRecovery := NewCredentialVCAPRecovery(vaultInterface, u.cfManager, u.logger)

	err := vcapRecovery.RecoverCredentialsFromVCAP(ctx, instance.ID)
	if err != nil {
		u.markVCAPRecoveryFailed(instance, err)
	} else {
		u.markVCAPRecoverySuccess(instance)
	}

	return nil
}

// markVCAPRecoveryFailed marks VCAP recovery as failed in metadata.
func (u *VaultUpdater) markVCAPRecoveryFailed(instance *InstanceData, err error) {
	u.logger.Warningf("VCAP credential recovery failed for instance %s: %s", instance.ID, err)
	instance.Metadata["vcap_recovery_attempted"] = true
	instance.Metadata["vcap_recovery_failed"] = true
	instance.Metadata["vcap_recovery_error"] = err.Error()
}

// markVCAPRecoverySuccess marks VCAP recovery as successful in metadata.
func (u *VaultUpdater) markVCAPRecoverySuccess(instance *InstanceData) {
	u.logger.Infof("Successfully recovered credentials from VCAP for instance %s", instance.ID)
	instance.Metadata["credentials_recovered_from"] = "vcap_services"
	instance.Metadata["vcap_recovery_attempted"] = true
	instance.Metadata["vcap_recovery_failed"] = false
	instance.Metadata["has_credentials"] = true
	delete(instance.Metadata, "needs_credential_recovery")
}

// processInstanceBindings handles binding checking and health validation.
func (u *VaultUpdater) processInstanceBindings(instance *InstanceData) error {
	if instance.ID == "" {
		u.logger.Warningf("Skipping bindings check for empty instance ID")

		return nil
	}

	bindingsPath := instance.ID + "/bindings"
	existingBindings, bindingsErr := u.getFromVault(bindingsPath)
	hasBindings := bindingsErr == nil && len(existingBindings) > 0

	if !hasBindings {
		return nil
	}

	u.updateBindingMetadata(instance, existingBindings)

	return u.checkBindingHealth(instance)
}

// updateBindingMetadata updates binding-related metadata.
func (u *VaultUpdater) updateBindingMetadata(instance *InstanceData, existingBindings map[string]interface{}) {
	instance.Metadata["has_bindings"] = true
	instance.Metadata["bindings_count"] = len(existingBindings)

	// Store binding IDs for reference
	bindingIDs := make([]string, 0, len(existingBindings))
	for id := range existingBindings {
		bindingIDs = append(bindingIDs, id)
	}

	instance.Metadata["binding_ids"] = bindingIDs
	u.logger.Debugf("Instance %s has %d existing bindings - preserving", instance.ID, len(existingBindings))
}

// checkBindingHealth performs binding health checks.
func (u *VaultUpdater) checkBindingHealth(instance *InstanceData) error {
	u.logger.Infof("Checking binding health for instance %s", instance.ID)

	healthyBindings, unhealthyBindingIDs, healthErr := u.CheckBindingHealth(instance.ID)
	if healthErr != nil {
		u.logger.Errorf("Failed to check binding health for instance %s: %s", instance.ID, healthErr)
		instance.Metadata["binding_health_check_error"] = healthErr.Error()

		return nil
	}

	// Extract healthy binding IDs from BindingInfo slice
	healthyBindingIDs := make([]string, len(healthyBindings))
	for i, binding := range healthyBindings {
		healthyBindingIDs[i] = binding.ID
	}

	u.updateBindingHealthMetadata(instance, healthyBindingIDs, unhealthyBindingIDs)

	return nil
}

// updateBindingHealthMetadata updates binding health-related metadata.
func (u *VaultUpdater) updateBindingHealthMetadata(instance *InstanceData, healthyBindings []string, unhealthyBindingIDs []string) {
	instance.Metadata["healthy_bindings_count"] = len(healthyBindings)
	instance.Metadata["unhealthy_bindings_count"] = len(unhealthyBindingIDs)
	instance.Metadata["last_binding_health_check"] = time.Now().Format(time.RFC3339)

	if len(unhealthyBindingIDs) > 0 {
		u.logger.Warningf("Found %d unhealthy bindings for instance %s: %v",
			len(unhealthyBindingIDs), instance.ID, unhealthyBindingIDs)
		instance.Metadata["unhealthy_binding_ids"] = unhealthyBindingIDs
		instance.Metadata["needs_binding_repair"] = true
		u.logger.Infof("Binding repair needed for instance %s but broker integration not implemented", instance.ID)
	} else {
		u.logger.Debugf("All bindings healthy for instance %s", instance.ID)
		instance.Metadata["needs_binding_repair"] = false
	}
}

// storeInstanceData stores all instance data in vault.
func (u *VaultUpdater) storeInstanceData(instance InstanceData) error {
	// Store root data
	err := u.storeRootData(instance)
	if err != nil {
		return err
	}

	// Store manifest if present
	u.storeManifestData(instance)

	// Store metadata if present
	u.storeMetadataWithHistory(instance)

	// Update the main instance data in the index
	vaultData := u.buildVaultData(instance)

	err = u.UpdateIndex(instance.ID, vaultData)
	if err != nil {
		return fmt.Errorf("failed to update index for %s: %w", instance.ID, err)
	}

	return nil
}

// storeRootData stores the main instance data at the root path.
func (u *VaultUpdater) storeRootData(instance InstanceData) error {
	vaultData := u.buildVaultData(instance)

	err := u.putToVault(instance.ID, vaultData)
	if err != nil {
		u.logger.Errorf("Failed to store root data for %s: %s", instance.ID, err)

		return err
	}

	u.logger.Debugf("Stored enriched data at root path for instance %s", instance.ID)

	return nil
}

// buildVaultData builds the vault data map for the instance.
func (u *VaultUpdater) buildVaultData(instance InstanceData) map[string]interface{} {
	// Get existing root data to preserve fields
	existingRootData, _ := u.getFromVault(instance.ID)
	if existingRootData == nil {
		existingRootData = make(map[string]interface{})
	}

	// Resolve instance name with preservation-first logic
	resolvedInstanceName := u.resolveInstanceName(instance, existingRootData)

	// Build base vault data
	vaultData := map[string]interface{}{
		"instance_id":     instance.ID,
		"service_id":      instance.ServiceID,
		"plan_id":         instance.PlanID,
		"deployment_name": instance.Deployment.Name,
		"created":         instance.CreatedAt.Unix(),
		"updated":         instance.UpdatedAt.Unix(),
		"last_synced_at":  time.Now().Unix(),
		"reconciled":      true,
		"reconciled_at":   time.Now().Unix(),
		"platform":        "cloudfoundry",
	}

	// Add enriched metadata
	u.addPlanData(vaultData, instance)
	u.addServiceData(vaultData, instance)
	u.addCFData(vaultData, instance, resolvedInstanceName)
	u.addContextData(vaultData)
	u.preserveExistingFields(vaultData, existingRootData)

	return vaultData
}

// resolveInstanceName resolves the instance name with preservation-first logic.
func (u *VaultUpdater) resolveInstanceName(instance InstanceData, existingRootData map[string]interface{}) string {
	// 1) Preserve existing value from Vault if present
	if existingName, ok := existingRootData["instance_name"].(string); ok && strings.TrimSpace(existingName) != "" {
		return existingName
	}

	// 2) Prefer instance.Metadata["instance_name"] or ["cf_instance_name"]
	if name, ok := instance.Metadata["instance_name"].(string); ok && strings.TrimSpace(name) != "" {
		return name
	}

	if name, ok := instance.Metadata["cf_instance_name"].(string); ok && strings.TrimSpace(name) != "" {
		return name
	}

	// 3) Fall back to instance.Metadata["cf_name"]
	if name, ok := instance.Metadata["cf_name"].(string); ok && strings.TrimSpace(name) != "" {
		return name
	}

	return ""
}

// addPlanData adds plan-related data to vault data.
func (u *VaultUpdater) addPlanData(vaultData map[string]interface{}, instance InstanceData) {
	if planName, ok := instance.Metadata["plan_name"].(string); ok && planName != "" {
		vaultData["plan"] = map[string]interface{}{
			"id":   instance.PlanID,
			"name": planName,
		}
		vaultData["plan_name"] = planName
	} else {
		vaultData["plan"] = map[string]interface{}{
			"id":   instance.PlanID,
			"name": instance.PlanID,
		}
	}
}

// addServiceData adds service-related data to vault data.
func (u *VaultUpdater) addServiceData(vaultData map[string]interface{}, instance InstanceData) {
	if serviceName, ok := instance.Metadata["service_name"].(string); ok && serviceName != "" {
		vaultData["service_name"] = serviceName
	}

	if serviceType, ok := instance.Metadata["service_type"].(string); ok && serviceType != "" {
		vaultData["service_type"] = serviceType
	}
}

// addCFData adds CloudFoundry-related data to vault data.
func (u *VaultUpdater) addCFData(vaultData map[string]interface{}, instance InstanceData, instanceName string) {
	if instanceName != "" {
		vaultData["instance_name"] = instanceName
	}

	u.addOrgData(vaultData, instance)
	u.addSpaceData(vaultData, instance)
	u.addTimestampData(vaultData, instance)
}

// addOrgData adds organization data to vault data.
func (u *VaultUpdater) addOrgData(vaultData map[string]interface{}, instance InstanceData) {
	if orgGUID, ok := instance.Metadata["org_guid"].(string); ok && orgGUID != "" {
		vaultData["organization_guid"] = orgGUID
	} else if orgGUID, ok := instance.Metadata["cf_org_id"].(string); ok && orgGUID != "" {
		vaultData["organization_guid"] = orgGUID
	}

	if orgName, ok := instance.Metadata["org_name"].(string); ok && orgName != "" {
		vaultData["organization_name"] = orgName
	}
}

// addSpaceData adds space data to vault data.
func (u *VaultUpdater) addSpaceData(vaultData map[string]interface{}, instance InstanceData) {
	if spaceGUID, ok := instance.Metadata["space_guid"].(string); ok && spaceGUID != "" {
		vaultData["space_guid"] = spaceGUID
	} else if spaceGUID, ok := instance.Metadata["cf_space_id"].(string); ok && spaceGUID != "" {
		vaultData["space_guid"] = spaceGUID
	}

	if spaceName, ok := instance.Metadata["space_name"].(string); ok && spaceName != "" {
		vaultData["space_name"] = spaceName
	}
}

// addTimestampData adds timestamp data to vault data.
func (u *VaultUpdater) addTimestampData(vaultData map[string]interface{}, instance InstanceData) {
	if requestedAt, ok := instance.Metadata["cf_created_at"].(string); ok && requestedAt != "" {
		t, err := time.Parse(time.RFC3339, requestedAt)
		if err == nil {
			vaultData["requested_at"] = t.Format(time.RFC3339)
		}
	} else if !instance.CreatedAt.IsZero() {
		vaultData["requested_at"] = instance.CreatedAt.Format(time.RFC3339)
	}
}

// addContextData adds context object for compatibility.
func (u *VaultUpdater) addContextData(vaultData map[string]interface{}) {
	contextObj := map[string]interface{}{
		"platform":                 "cloudfoundry",
		"instance_annotations":     map[string]interface{}{},
		"organization_annotations": map[string]interface{}{},
		"space_annotations":        map[string]interface{}{},
	}

	// Add CF data to context if available
	cfFields := []string{"instance_name", "organization_guid", "organization_name", "space_guid", "space_name"}
	for _, field := range cfFields {
		if value, ok := vaultData[field].(string); ok {
			contextObj[field] = value
		}
	}

	// Marshal context to JSON string for compatibility
	contextJSON, err := json.Marshal(contextObj)
	if err == nil {
		vaultData["context"] = string(contextJSON)
	}
}

// preserveExistingFields preserves certain fields from existing vault data.
func (u *VaultUpdater) preserveExistingFields(vaultData, existingRootData map[string]interface{}) {
	preserveFields := []string{
		"parameters", "original_request", "created_by",
		"provision_params", "update_params", "instance_name",
	}

	for _, field := range preserveFields {
		if val, exists := existingRootData[field]; exists && val != nil {
			vaultData[field] = val
		}
	}
}

// storeManifestData stores manifest data if present.
func (u *VaultUpdater) storeManifestData(instance InstanceData) {
	if instance.Deployment.Manifest == "" {
		return
	}

	manifestPath := instance.ID + "/manifest"
	manifestData := map[string]interface{}{
		"manifest":      instance.Deployment.Manifest,
		"updated_at":    time.Now().Unix(),
		"reconciled":    true,
		"reconciled_by": sourceReconciler,
	}

	err := u.putToVault(manifestPath, manifestData)
	if err != nil {
		u.logger.Errorf("Failed to store manifest for %s: %s", instance.ID, err)
	} else {
		u.logger.Debugf("Stored manifest for instance %s", instance.ID)
	}
}

// storeMetadataWithHistory stores metadata with reconciliation history.
func (u *VaultUpdater) storeMetadataWithHistory(instance InstanceData) {
	if len(instance.Metadata) == 0 {
		return
	}

	metadataPath := instance.ID + "/metadata"

	// Get existing metadata to preserve history
	existingMeta, err := u.getFromVault(metadataPath)
	if err != nil {
		u.logger.Debugf("No existing metadata found for %s: %s", instance.ID, err)

		existingMeta = make(map[string]interface{})
	} else {
		u.logger.Debugf("Retrieved existing metadata for %s with %d keys", instance.ID, len(existingMeta))
	}

	u.logger.Debugf("storeMetadataWithHistory: existing metadata has %d keys", len(existingMeta))

	if existingHistory, ok := existingMeta["history"]; ok {
		u.logger.Debugf("storeMetadataWithHistory: existing has history of type %T", existingHistory)

		if h, ok := existingHistory.([]interface{}); ok {
			u.logger.Debugf("storeMetadataWithHistory: existing history has %d entries", len(h))
		}
	}

	// Merge with new metadata
	u.logger.Debugf("Before merge - existing metadata has history: %v", existingMeta["history"] != nil)
	u.logger.Debugf("Before merge - instance metadata has history: %v", instance.Metadata["history"] != nil)
	mergedMetadata := u.MergeMetadata(existingMeta, instance.Metadata)
	u.logger.Debugf("After merge - merged metadata has history: %v", mergedMetadata["history"] != nil)

	u.logger.Debugf("storeMetadataWithHistory: merged metadata has %d keys", len(mergedMetadata))

	if mergedHistory, ok := mergedMetadata["history"]; ok {
		u.logger.Debugf("storeMetadataWithHistory: merged has history of type %T", mergedHistory)

		if h, ok := mergedHistory.([]interface{}); ok {
			u.logger.Debugf("storeMetadataWithHistory: merged history has %d entries", len(h))
		}
	}

	// Detect changes and add reconciliation history
	// Note: Compare against mergedMetadata to properly detect changes
	// since history and other fields may only exist in existing metadata
	changes := u.DetectChanges(existingMeta, mergedMetadata)
	u.addReconciliationHistory(mergedMetadata, changes)

	// Clean and store metadata
	cleanedMetadataRaw := u.deinterfaceMetadata(mergedMetadata)

	cleanedMetadata, ok := cleanedMetadataRaw.(map[string]interface{})
	if !ok {
		u.logger.Errorf("deinterfaceMetadata returned unexpected type for %s: %T", instance.ID, cleanedMetadataRaw)

		return
	}

	err = u.putToVault(metadataPath, cleanedMetadata)
	if err != nil {
		u.logger.Errorf("Failed to store metadata for %s: %s", instance.ID, err)
	} else {
		u.logger.Debugf("Stored metadata for instance %s", instance.ID)
	}
}

// getBindingsData retrieves binding data from vault.
func (u *VaultUpdater) getBindingsData(instanceID string) (map[string]interface{}, error) {
	bindingsPath := instanceID + "/bindings"

	bindingsData, err := u.getFromVault(bindingsPath)
	if err != nil {
		u.logger.Debugf("No bindings found for instance %s: %s", instanceID, err)

		return nil, err
	}

	return bindingsData, nil
}

// createBaseBinding creates a base binding info structure.
func (u *VaultUpdater) createBaseBinding(instanceID, bindingID string) BindingInfo {
	return BindingInfo{
		ID:         bindingID,
		InstanceID: instanceID,
		Status:     "unknown",
	}
}

// getBindingCredentials retrieves credentials for a specific binding.
func (u *VaultUpdater) getBindingCredentials(instanceID, bindingID string) (map[string]interface{}, error) {
	credentialsPath := fmt.Sprintf("%s/bindings/%s/credentials", instanceID, bindingID)

	credentials, err := u.getFromVault(credentialsPath)
	if err != nil || len(credentials) == 0 {
		u.logger.Warningf("Binding %s for instance %s missing credentials", bindingID, instanceID)

		return nil, ErrMissingCredentials
	}

	return credentials, nil
}

// processHealthyBinding handles a healthy binding by extracting metadata.
func (u *VaultUpdater) processHealthyBinding(binding *BindingInfo, bindingData interface{}) {
	binding.Status = "healthy"
	binding.LastVerified = time.Now()

	u.extractBindingMetadata(binding, bindingData)
}

// processUnhealthyBinding handles an unhealthy binding.
func (u *VaultUpdater) processUnhealthyBinding(binding *BindingInfo, bindingID, instanceID string) {
	u.logger.Warningf("Binding %s for instance %s has incomplete credentials", bindingID, instanceID)

	binding.Status = "incomplete_credentials"
}

// extractBindingMetadata extracts metadata from binding data.
func (u *VaultUpdater) extractBindingMetadata(binding *BindingInfo, bindingData interface{}) {
	meta, ok := bindingData.(map[string]interface{})
	if !ok {
		return
	}

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
		createdTime, parseErr := time.Parse(time.RFC3339, createdStr)
		if parseErr == nil {
			binding.CreatedAt = createdTime
		}
	}
}

// validateCredentialCompleteness checks if credentials contain required fields.
func (u *VaultUpdater) validateCredentialCompleteness(credentials map[string]interface{}) bool {
	// Check for basic required fields - these are common across most services
	requiredFields := []string{"host", "port", "username", "password"}

	for _, field := range requiredFields {
		if value, exists := credentials[field]; !exists || value == nil || value == "" {
			// Allow missing non-critical fields, but log for debugging
			u.logger.Debugf("Credential field '%s' is missing or empty", field)
			// Don't fail validation for missing fields as some services may not need all
		}
	}

	// As long as we have some credentials, consider it complete
	// Individual service validation can be more specific if needed
	return len(credentials) > 0
}

// updateBindingsIndex maintains an index of all bindings for an instance.
func (u *VaultUpdater) updateBindingsIndex(instanceID, bindingID string, metadata map[string]interface{}) error {
	indexPath := instanceID + "/bindings/index"

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

// deleteFromVault removes data from vault (placeholder implementation).
func (u *VaultUpdater) deleteFromVault(path string) {
	u.logger.Debugf("Deleting vault path: %s", path)
	// This would call the actual vault delete method
	// For now, this is a placeholder implementation
}

// getCredentialsFromBroker retrieves binding credentials from the broker.
func (u *VaultUpdater) getCredentialsFromBroker(broker BrokerInterface, instanceID, bindingID string) (*BindingCredentials, error) {
	bindingCreds, err := broker.GetBindingCredentials(instanceID, bindingID)
	if err != nil {
		u.logger.Errorf("Broker failed to reconstruct credentials for binding %s: %s", bindingID, err)

		return nil, fmt.Errorf("broker reconstruction failed: %w", err)
	}

	if bindingCreds == nil {
		return nil, ErrBrokerReturnedNoCredentials
	}

	return bindingCreds, nil
}

// buildCredentialsMap builds a credentials map from broker credentials.
func (u *VaultUpdater) buildCredentialsMap(bindingCreds *BindingCredentials) map[string]interface{} {
	creds := make(map[string]interface{})

	// Start with Raw credentials if available
	if len(bindingCreds.Raw) > 0 {
		for key, value := range bindingCreds.Raw {
			creds[key] = value
		}
	}

	u.addStandardCredentialFields(creds, bindingCreds)
	u.addServiceSpecificFields(creds, bindingCreds)

	if _, ok := creds["credential_type"]; !ok && bindingCreds.CredentialType != "" {
		creds["credential_type"] = bindingCreds.CredentialType
	}

	return creds
}

// addStandardCredentialFields adds standard credential fields if not already present.
func (u *VaultUpdater) addStandardCredentialFields(creds map[string]interface{}, bindingCreds *BindingCredentials) {
	if _, ok := creds["host"]; !ok && bindingCreds.Host != "" {
		creds["host"] = bindingCreds.Host
	}

	if _, ok := creds["port"]; !ok && bindingCreds.Port != 0 {
		creds["port"] = bindingCreds.Port
	}

	if _, ok := creds["username"]; !ok && bindingCreds.Username != "" {
		creds["username"] = bindingCreds.Username
	}

	if _, ok := creds["password"]; !ok && bindingCreds.Password != "" {
		creds["password"] = bindingCreds.Password
	}
}

// addServiceSpecificFields adds service-specific credential fields from Raw data.
func (u *VaultUpdater) addServiceSpecificFields(creds map[string]interface{}, bindingCreds *BindingCredentials) {
	if bindingCreds.Raw == nil {
		return
	}

	serviceFields := []string{"uri", "api_url", "vhost", "database", "scheme"}

	for _, field := range serviceFields {
		if _, ok := creds[field]; !ok {
			if value, exists := bindingCreds.Raw[field]; exists {
				creds[field] = value
			}
		}
	}
}

// buildReconstructionMetadata builds metadata for the reconstructed binding.
func (u *VaultUpdater) buildReconstructionMetadata(instanceID, bindingID string, bindingCreds *BindingCredentials) map[string]interface{} {
	metadata := map[string]interface{}{
		"binding_id":            bindingID,
		"instance_id":           instanceID,
		"credential_type":       bindingCreds.CredentialType,
		"reconstructed_at":      time.Now().Format(time.RFC3339),
		"reconstruction_source": "broker",
		"status":                "reconstructed",
	}

	u.enrichMetadataFromIndex(metadata, instanceID)

	return metadata
}

// enrichMetadataFromIndex enriches metadata with service/plan information from the index.
func (u *VaultUpdater) enrichMetadataFromIndex(metadata map[string]interface{}, instanceID string) {
	idxData, err := u.GetFromIndex(instanceID)
	if err != nil || idxData == nil {
		return
	}

	if sid, ok := idxData["service_id"]; ok {
		metadata["service_id"] = sid
	}

	if pid, ok := idxData["plan_id"]; ok {
		metadata["plan_id"] = pid
	}
}

// recordReconstructionFailure records when a binding reconstruction fails.
func (u *VaultUpdater) recordReconstructionFailure(instanceID, bindingID string, err error) {
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
		u.logger.Warningf("Failed to record reconstruction failure metadata: %s", putErr)
	}
}

func (u *VaultUpdater) parseBasicFields(instance *InstanceData, indexData map[string]interface{}) {
	if v, ok := indexData["service_id"].(string); ok {
		instance.ServiceID = v
	}

	if v, ok := indexData["plan_id"].(string); ok {
		instance.PlanID = v
	}

	if v, ok := indexData["deployment_name"].(string); ok {
		instance.Deployment.Name = v
	}
}

func (u *VaultUpdater) parseTimestamps(instance *InstanceData, indexData map[string]interface{}) {
	u.parseCreatedTimestamp(instance, indexData)
	u.parseUpdatedTimestamp(instance, indexData)
}

func (u *VaultUpdater) parseCreatedTimestamp(instance *InstanceData, indexData map[string]interface{}) {
	if v, ok := indexData["created"].(float64); ok {
		instance.CreatedAt = time.Unix(int64(v), 0)
	} else if v, ok := indexData["created"].(int64); ok {
		instance.CreatedAt = time.Unix(v, 0)
	} else if v, ok := indexData["created_at"].(string); ok {
		// Fallback to RFC3339 for backward compatibility
		t, err := time.Parse(time.RFC3339, v)
		if err == nil {
			instance.CreatedAt = t
		}
	}
}

func (u *VaultUpdater) parseUpdatedTimestamp(instance *InstanceData, indexData map[string]interface{}) {
	if v, ok := indexData["updated"].(float64); ok {
		instance.UpdatedAt = time.Unix(int64(v), 0)
	} else if v, ok := indexData["updated"].(int64); ok {
		instance.UpdatedAt = time.Unix(v, 0)
	} else if v, ok := indexData["updated_at"].(string); ok {
		// Fallback to RFC3339 for backward compatibility
		t, err := time.Parse(time.RFC3339, v)
		if err == nil {
			instance.UpdatedAt = t
		}
	}
}

func (u *VaultUpdater) parseLastSyncedAt(instance *InstanceData, indexData map[string]interface{}) {
	if v, ok := indexData["last_synced_at"]; ok {
		if instance.Metadata == nil {
			instance.Metadata = make(map[string]interface{})
		}

		instance.Metadata["last_synced_at"] = v
	}
}

func (u *VaultUpdater) loadManifestAndMetadata(instance *InstanceData, instanceID string) {
	// Get manifest
	manifestPath := instanceID + "/manifest"

	manifestData, err := u.getFromVault(manifestPath)
	if err == nil {
		if manifest, ok := manifestData["manifest"].(string); ok {
			instance.Deployment.Manifest = manifest
		}
	} else {
		u.logger.Debugf("No manifest found for instance %s", instanceID)
	}

	// Get metadata
	metadataPath := instanceID + "/metadata"

	metadata, err := u.getFromVault(metadataPath)
	if err == nil {
		instance.Metadata = metadata
	} else {
		u.logger.Debugf("No metadata found for instance %s", instanceID)
	}
}

func (u *VaultUpdater) hasFieldChanged(old, newData map[string]interface{}, fieldName string) bool {
	oldValue, oldExists := old[fieldName]
	newValue, newExists := newData[fieldName]

	switch {
	case oldExists && newExists:
		return !u.deepEqual(oldValue, newValue)
	case newExists:
		return true
	default:
		return false
	}
}

func (u *VaultUpdater) trackFieldChanges(changes map[string]interface{}, old, newData map[string]interface{}) {
	addedFields := u.findAddedFields(old, newData)
	removedFields := u.findRemovedFields(old, newData)

	if len(addedFields) > 0 {
		changes["fields_added"] = addedFields
	}

	if len(removedFields) > 0 {
		changes["fields_removed"] = removedFields
	}
}

func (u *VaultUpdater) findAddedFields(old, newData map[string]interface{}) []string {
	addedFields := []string{}

	for k := range newData {
		if _, exists := old[k]; !exists {
			addedFields = append(addedFields, k)
		}
	}

	return addedFields
}

func (u *VaultUpdater) findRemovedFields(old, newData map[string]interface{}) []string {
	removedFields := []string{}

	for k := range old {
		if _, exists := newData[k]; !exists {
			removedFields = append(removedFields, k)
		}
	}

	return removedFields
}

// deepEqual performs a simple deep equality check.
func (u *VaultUpdater) deepEqual(a, b interface{}) bool {
	// Simple implementation - could be enhanced
	aStr := fmt.Sprintf("%v", a)
	bStr := fmt.Sprintf("%v", b)

	return aStr == bStr
}

// addReconciliationHistory adds a reconciliation entry to the history.
func (u *VaultUpdater) addReconciliationHistory(metadata map[string]interface{}, changes map[string]interface{}) {
	var history []map[string]interface{}

	u.logger.Debugf("addReconciliationHistory: metadata has history: %v", metadata["history"] != nil)

	if metadata["history"] != nil {
		u.logger.Debugf("addReconciliationHistory: history type is %T", metadata["history"])
	}

	if h, ok := metadata["history"].([]interface{}); ok {
		u.logger.Debugf("addReconciliationHistory: found []interface{} history with %d entries", len(h))

		for _, entry := range h {
			if e, ok := entry.(map[string]interface{}); ok {
				history = append(history, e)
			} else {
				u.logger.Debugf("addReconciliationHistory: skipping entry of type %T", entry)
			}
		}
	} else if h, ok := metadata["history"].([]map[string]interface{}); ok {
		u.logger.Debugf("addReconciliationHistory: found []map[string]interface{} history with %d entries", len(h))
		history = h
	} else {
		u.logger.Debugf("addReconciliationHistory: history is not a recognized array type")
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

	u.logger.Debugf("addReconciliationHistory: history before append has %d entries", len(history))
	history = append(history, entry)
	u.logger.Debugf("addReconciliationHistory: history after adding reconciliation entry has %d entries", len(history))

	// Apply retention policy (time-based + max entries safety cap)
	u.logger.Debugf("Calling FilterHistoryByRetention with retentionDays=%d, maxSize=%d", historyRetentionDays, historyMaxSize)
	history = vault.FilterHistoryByRetention(history, historyRetentionDays, historyMaxSize)
	u.logger.Debugf("History after retention filter: %d entries", len(history))

	metadata["history"] = history
	u.logger.Debugf("addReconciliationHistory: metadata now has history with %d entries", len(history))
}

// Vault interaction methods with proper implementation

func (u *VaultUpdater) putToVault(path string, data map[string]interface{}) error {
	u.logger.Debugf("Storing data at vault path: %s", path)

	// Check if vault is nil
	if u.vault == nil {
		u.logger.Errorf("Vault is not initialized - cannot store data at path: %s", path)

		return ErrVaultNotInitialized
	}

	// Type assert vault to VaultInterface to access Put method
	vault, ok := u.vault.(VaultInterface)
	if !ok {
		return ErrVaultNotExpectedType
	}

	err := vault.Put(path, data)
	if err != nil {
		return fmt.Errorf("failed to put data to vault at path %s: %w", path, err)
	}

	return nil
}

func (u *VaultUpdater) getFromVault(path string) (map[string]interface{}, error) {
	u.logger.Debugf("Getting data from vault path: %s", path)

	// Check if vault is nil
	if u.vault == nil {
		u.logger.Errorf("Vault is not initialized - cannot get data from path: %s", path)

		return nil, ErrVaultNotInitialized
	}

	// Type assert vault to VaultInterface to access Get method
	vault, ok := u.vault.(VaultInterface)
	if !ok {
		return nil, ErrVaultNotExpectedType
	}

	data, err := vault.Get(path)
	if err != nil {
		return nil, fmt.Errorf("failed to get data from vault at path %s: %w", path, err)
	}
	// Return empty map without error if no data found
	if data == nil {
		return map[string]interface{}{}, nil
	}

	return data, nil
}

// backupInstance creates a backup of instance data using the new format and storage location.
func (u *VaultUpdater) backupInstance(instanceID string) error {
	u.logger.Debugf("Creating backup for instance %s", instanceID)

	// Export all data from the instance path using the new vault export functionality
	exportedData, err := u.exportVaultPath(instanceID)
	if err != nil {
		return fmt.Errorf("failed to export vault data for instance %s: %w", instanceID, err)
	}

	// If no data to backup, skip
	if len(exportedData) == 0 {
		u.logger.Debugf("No data to backup for instance %s", instanceID)

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
		u.logger.Debugf("Backup with SHA256 %s already exists for instance %s - skipping", sha256Sum, instanceID)

		return nil
	}

	// Create backup entry with new format
	timestamp := time.Now().Unix()
	backupEntry := map[string]interface{}{
		"timestamp": timestamp,
		"archive":   compressedArchive,
	}

	// Store the backup at new location (no secret/ prefix)
	err = u.putToVault(backupPath, backupEntry)
	if err != nil {
		return fmt.Errorf("failed to store backup at %s: %w", backupPath, err)
	}

	u.logger.Debugf("Created backup for instance %s with SHA256 %s at %s", instanceID, sha256Sum, backupPath)

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

// cleanOldBackupsNewFormat removes old backups using the new storage format.
func (u *VaultUpdater) cleanOldBackupsNewFormat(instanceID string, keepCount int) {
	if keepCount <= 0 {
		u.logger.Debugf("Backup cleanup disabled for %s (keepCount: %d)", instanceID, keepCount)

		return
	}

	u.logger.Debugf("Cleaning old backups for instance %s, keeping %d most recent", instanceID, keepCount)

	vaultClient := u.getVaultClientForBackupCleanup()
	if vaultClient == nil {
		return
	}

	backups := u.collectBackupInfoForCleanup(vaultClient, instanceID)

	if len(backups) <= keepCount {
		u.logger.Debugf("No cleanup needed for %s - have %d backups, keeping %d", instanceID, len(backups), keepCount)

		return
	}

	u.sortBackupsByTimestamp(backups)
	u.deleteOldBackups(vaultClient, instanceID, backups, keepCount)
}

// getVaultClientForBackupCleanup gets a vault client for backup cleanup operations.
func (u *VaultUpdater) getVaultClientForBackupCleanup() *api.Client {
	vaultClient, ok := u.vault.(*api.Client)
	if !ok {
		// Fallback: try to get client through interface conversion
		if vaultInterface, ok := u.vault.(interface{ GetClient() *api.Client }); ok {
			vaultClient = vaultInterface.GetClient()
		} else {
			u.logger.Warningf("Unable to access vault client for backup cleanup")

			return nil
		}
	}

	return vaultClient
}

// collectBackupInfoForCleanup collects information about all backups for an instance.
func (u *VaultUpdater) collectBackupInfoForCleanup(vaultClient *api.Client, instanceID string) []backupInfo {
	metadataPath := "secret/metadata/backups/" + instanceID

	listResp, err := vaultClient.Logical().List(metadataPath)
	if err != nil || listResp == nil {
		u.logger.Debugf("No backups found to clean up for instance %s", instanceID)

		return nil
	}

	var backups []backupInfo

	if listResp.Data != nil {
		if keys, ok := listResp.Data["keys"].([]interface{}); ok {
			for _, keyInterface := range keys {
				if sha256, ok := keyInterface.(string); ok {
					backup := u.processBackupEntry(vaultClient, instanceID, sha256)
					if backup != nil {
						backups = append(backups, *backup)
					}
				}
			}
		}
	}

	return backups
}

// processBackupEntry processes a single backup entry and returns backup info if valid.
func (u *VaultUpdater) processBackupEntry(vaultClient *api.Client, instanceID, sha256 string) *backupInfo {
	backupDataPath := fmt.Sprintf("secret/data/backups/%s/%s", instanceID, sha256)

	secret, err := vaultClient.Logical().Read(backupDataPath)
	if err != nil || secret == nil {
		u.logger.Warningf("Failed to read backup %s for cleanup: %s", sha256, err)

		return nil
	}

	if secret.Data == nil {
		u.logger.Warningf("Backup %s has nil secret.Data", sha256)

		return nil
	}

	data, ok := secret.Data["data"].(map[string]interface{})
	if !ok {
		u.logger.Warningf("Backup %s has unexpected data structure: %T", sha256, secret.Data["data"])

		return nil
	}

	return u.extractBackupTimestamp(instanceID, sha256, data)
}

// extractBackupTimestamp extracts timestamp from backup data and handles cleanup of invalid backups.
func (u *VaultUpdater) extractBackupTimestamp(instanceID, sha256 string, data map[string]interface{}) *backupInfo {
	timestampData, exists := data["timestamp"]
	if !exists {
		u.handleBackupWithoutTimestamp(instanceID, sha256, data)

		return nil
	}

	timestamp := u.parseTimestampValue(sha256, timestampData)
	if timestamp == 0 {
		return nil
	}

	return &backupInfo{
		sha256:    sha256,
		timestamp: timestamp,
	}
}

// handleBackupWithoutTimestamp handles backups that don't have a timestamp field.
func (u *VaultUpdater) handleBackupWithoutTimestamp(instanceID, sha256 string, data map[string]interface{}) {
	fields := make([]string, 0, len(data))
	for k := range data {
		fields = append(fields, k)
	}

	u.logger.Warningf("Backup %s missing timestamp field, has fields: %v", sha256, fields)

	// Check if this might be a legacy backup format
	if _, hasArchive := data["archive"]; hasArchive {
		u.logger.Debugf("Backup %s appears to be missing timestamp but has archive data", sha256)

		return // Don't delete - might be recoverable or valid in different format
	}

	// Only delete if it's clearly invalid
	u.logger.Warningf("Backup %s has no valid backup data - removing", sha256)
	u.deleteInvalidBackup(instanceID, sha256)
}

// parseTimestampValue parses various timestamp formats from backup data.
func (u *VaultUpdater) parseTimestampValue(sha256 string, timestampData interface{}) int64 {
	switch value := timestampData.(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	case json.Number:
		// Handle json.Number type that might come from Vault
		timestamp, err := value.Int64()
		if err == nil {
			return timestamp
		} else {
			u.logger.Warningf("Invalid json.Number timestamp in backup %s: %v", sha256, err)

			return 0
		}
	default:
		// Log the actual value for debugging before deciding to delete
		u.logger.Warningf("Unexpected timestamp type in backup %s: type=%T, value=%v", sha256, timestampData, timestampData)

		return 0 // Don't delete immediately - might be a valid backup with different format
	}
}

// deleteInvalidBackup removes a backup that has no valid data.
func (u *VaultUpdater) deleteInvalidBackup(instanceID, sha256 string) {
	vaultClient := u.getVaultClientForBackupCleanup()
	if vaultClient == nil {
		return
	}

	dataPath := fmt.Sprintf("secret/data/backups/%s/%s", instanceID, sha256)
	metadataPath := fmt.Sprintf("secret/metadata/backups/%s/%s", instanceID, sha256)

	_, err := vaultClient.Logical().Delete(dataPath)
	if err != nil {
		u.logger.Warningf("Failed to delete invalid backup data at %s: %s", dataPath, err)
	}

	_, err = vaultClient.Logical().Delete(metadataPath)
	if err != nil {
		u.logger.Debugf("Failed to delete invalid backup metadata at %s: %s", metadataPath, err)
	}

	u.logger.Infof("Removed backup %s without valid data", sha256)
}

// sortBackupsByTimestamp sorts backups by timestamp (newest first).
func (u *VaultUpdater) sortBackupsByTimestamp(backups []backupInfo) {
	for i := range len(backups) - 1 {
		for j := i + 1; j < len(backups); j++ {
			if backups[i].timestamp < backups[j].timestamp {
				backups[i], backups[j] = backups[j], backups[i]
			}
		}
	}
}

// deleteOldBackups deletes old backups, keeping only the most recent ones.
func (u *VaultUpdater) deleteOldBackups(vaultClient *api.Client, instanceID string, backups []backupInfo, keepCount int) {
	if len(backups) <= keepCount {
		return
	}

	toDelete := backups[keepCount:]
	for _, backup := range toDelete {
		u.deleteBackupFiles(vaultClient, instanceID, backup.sha256)
	}

	u.logger.Debugf("Backup cleanup for %s completed - deleted %d old backups, keeping %d",
		instanceID, len(toDelete), keepCount)
}

// deleteBackupFiles deletes both data and metadata files for a backup.
func (u *VaultUpdater) deleteBackupFiles(vaultClient *api.Client, instanceID, sha256 string) {
	backupPath := fmt.Sprintf("secret/data/backups/%s/%s", instanceID, sha256)

	_, err := vaultClient.Logical().Delete(backupPath)
	if err != nil {
		u.logger.Warningf("Failed to delete old backup %s: %s", backupPath, err)
	} else {
		u.logger.Debugf("Deleted old backup %s", backupPath)
	}

	// Also delete metadata
	backupMetadataPath := fmt.Sprintf("secret/metadata/backups/%s/%s", instanceID, sha256)

	_, err = vaultClient.Logical().Delete(backupMetadataPath)
	if err != nil {
		u.logger.Warningf("Failed to delete old backup metadata %s: %s", backupMetadataPath, err)
	}
}

// cleanLegacyInstanceBackups removes old backup data stored at secret/{instanceID}/backups.
//
//nolint:funlen
func (u *VaultUpdater) cleanLegacyInstanceBackups(instanceID string) {
	u.logger.Debugf("Checking for legacy backup data for instance %s", instanceID)

	// Try to access the vault client for direct operations
	vaultClient, valid := u.vault.(*api.Client)
	if !valid {
		// Fallback: try to get client through interface conversion
		if vaultInterface, ok := u.vault.(interface{ GetClient() *api.Client }); ok {
			vaultClient = vaultInterface.GetClient()
		} else {
			u.logger.Warningf("Unable to access vault client for legacy backup cleanup")

			return
		}
	}

	// List all backup entries under secret/{instanceID}/backups/
	legacyBackupPath := fmt.Sprintf("secret/metadata/%s/backups", instanceID)

	listResp, err := vaultClient.Logical().List(legacyBackupPath)
	if err != nil {
		u.logger.Debugf("No legacy backup data found for instance %s: %v", instanceID, err)

		return
	}

	if listResp == nil || listResp.Data == nil {
		u.logger.Debugf("No legacy backup entries found for instance %s", instanceID)

		return
	}

	// Get the list of keys (backup timestamps)
	keysRaw, valid := listResp.Data["keys"]
	if !valid {
		u.logger.Debugf("No keys found in legacy backup list response for instance %s", instanceID)

		return
	}

	keys, ok := keysRaw.([]interface{})
	if !ok || len(keys) == 0 {
		u.logger.Debugf("No legacy backup entries to clean for instance %s", instanceID)

		return
	}

	u.logger.Infof("Found %d legacy backup entries for instance %s, cleaning up", len(keys), instanceID)

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
			u.logger.Warningf("Failed to delete legacy backup data at %s: %s", dataPath, err)
		} else {
			deletedCount++

			u.logger.Debugf("Deleted legacy backup: %s", dataPath)
		}

		// Delete the metadata version
		metadataPath := fmt.Sprintf("secret/metadata/%s/backups/%s", instanceID, key)

		_, err = vaultClient.Logical().Delete(metadataPath)
		if err != nil {
			u.logger.Debugf("Failed to delete backup metadata at %s: %s", metadataPath, err)
		}
	}

	// After deleting all subdirectories, try to delete the parent backup path
	parentDataPath := fmt.Sprintf("secret/data/%s/backups", instanceID)

	_, err = vaultClient.Logical().Delete(parentDataPath)
	if err != nil {
		u.logger.Debugf("Failed to delete parent backup path %s: %s", parentDataPath, err)
	}

	parentMetadataPath := fmt.Sprintf("secret/metadata/%s/backups", instanceID)

	_, err = vaultClient.Logical().Delete(parentMetadataPath)
	if err != nil {
		u.logger.Debugf("Failed to delete parent backup metadata %s: %s", parentMetadataPath, err)
	}

	if deletedCount > 0 {
		u.logger.Infof("Successfully cleaned up %d legacy backup entries for instance %s", deletedCount, instanceID)
	}
}

// exportVaultPath recursively exports all secrets from a vault path
// Returns a map in the format compatible with "safe export".
func (u *VaultUpdater) exportVaultPath(basePath string) (map[string]interface{}, error) {
	result := make(map[string]interface{})

	// Try to access the vault client directly
	vaultClient, ok := u.vault.(*api.Client)
	if !ok {
		// Fallback: try to get client through interface conversion
		if vaultInterface, ok := u.vault.(interface{ GetClient() *api.Client }); ok {
			vaultClient = vaultInterface.GetClient()
		} else {
			return nil, ErrUnableToAccessVaultClientForExport
		}
	}

	// Export all keys under the base path using the logical API
	err := u.exportPathRecursive(vaultClient, basePath, basePath, result)
	if err != nil {
		return nil, fmt.Errorf("failed to export path %s: %w", basePath, err)
	}

	return result, nil
}

// exportPathRecursive recursively exports all secrets from a vault path using the logical API.
func (u *VaultUpdater) exportPathRecursive(client *api.Client, currentPath, basePath string, result map[string]interface{}) error {
	u.logger.Debugf("Exporting vault path: %s", currentPath)

	listPath := "secret/metadata/" + currentPath
	dataPath := "secret/data/" + currentPath

	listResp, err := client.Logical().List(listPath)
	if err != nil || listResp == nil {
		return u.exportSingleSecret(client, dataPath, currentPath, result)
	}

	return u.processListResponse(client, listResp, currentPath, basePath, result)
}

// exportSingleSecret exports a single secret if it exists.
func (u *VaultUpdater) exportSingleSecret(client *api.Client, dataPath, currentPath string, result map[string]interface{}) error {
	secret, err := client.Logical().Read(dataPath)
	if err != nil || secret == nil {
		u.logger.Debugf("Path %s has no secrets or subpaths", currentPath)

		return nil //nolint:nilerr // Missing path or secret is not an error in this context
	}

	u.storeSecretData(secret, currentPath, result)

	return nil
}

// processListResponse processes the list response and handles directories and files.
func (u *VaultUpdater) processListResponse(client *api.Client, listResp *api.Secret, currentPath, basePath string, result map[string]interface{}) error {
	if listResp.Data == nil {
		return nil
	}

	keys, ok := listResp.Data["keys"].([]interface{})
	if !ok {
		return nil
	}

	for _, keyInterface := range keys {
		key, ok := keyInterface.(string)
		if !ok {
			continue
		}

		subPath := u.buildSubPath(currentPath, key)

		if strings.HasSuffix(key, "/") {
			u.exportDirectory(client, subPath, basePath, result)
		} else {
			u.exportSecretFile(client, subPath, result)
		}
	}

	return nil
}

// buildSubPath constructs the subpath from current path and key.
func (u *VaultUpdater) buildSubPath(currentPath, key string) string {
	if currentPath == "" {
		return key
	}

	return currentPath + "/" + key
}

// exportDirectory exports a directory by recursing into it.
func (u *VaultUpdater) exportDirectory(client *api.Client, subPath, basePath string, result map[string]interface{}) {
	dirPath := subPath[:len(subPath)-1] // Remove trailing slash

	err := u.exportPathRecursive(client, dirPath, basePath, result)
	if err != nil {
		u.logger.Warningf("Failed to export directory %s: %s", dirPath, err)
	}
}

// exportSecretFile exports a single secret file.
func (u *VaultUpdater) exportSecretFile(client *api.Client, subPath string, result map[string]interface{}) {
	subDataPath := "secret/data/" + subPath

	secret, err := client.Logical().Read(subDataPath)
	if err != nil || secret == nil {
		u.logger.Warningf("Failed to get secret %s: %s", subPath, err)

		return
	}

	u.storeSecretData(secret, subPath, result)
}

// storeSecretData extracts and stores secret data from KV v2 format.
func (u *VaultUpdater) storeSecretData(secret *api.Secret, currentPath string, result map[string]interface{}) {
	if secret.Data == nil {
		return
	}

	data, ok := secret.Data["data"]
	if !ok {
		return
	}

	secretPath := "secret/" + currentPath
	result[secretPath] = data
	u.logger.Debugf("Exported secret: %s", secretPath)
}

// compressAndEncode compresses and base64 encodes the backup data.
func (u *VaultUpdater) compressAndEncode(data map[string]interface{}) (string, error) {
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

	_, err = gzipWriter.Write(jsonData)
	if err != nil {
		_ = gzipWriter.Close() // #nosec G104 - Best effort close on error path

		return "", fmt.Errorf("failed to write compressed data: %w", err)
	}

	err = gzipWriter.Close()
	if err != nil {
		return "", fmt.Errorf("failed to close gzip writer: %w", err)
	}

	// Base64 encode the compressed data
	encoded := base64.StdEncoding.EncodeToString(compressed.Bytes())

	u.logger.Debugf("Compressed data from %d bytes to %d bytes (%.2f%% compression)",
		len(jsonData), len(compressed.Bytes()),
		float64(len(compressed.Bytes()))/float64(len(jsonData))*compressionRatioPercent)

	return encoded, nil
}

// deinterfaceMetadata converts map[interface{}]interface{} structures to map[string]interface{}.
func (u *VaultUpdater) deinterfaceMetadata(data interface{}) interface{} {
	switch value := data.(type) {
	case map[interface{}]interface{}:
		result := make(map[string]interface{})

		for key, val := range value {
			keyStr := fmt.Sprintf("%v", key)
			result[keyStr] = u.deinterfaceMetadata(val)
		}

		return result
	case map[string]interface{}:
		result := make(map[string]interface{})
		for key, val := range value {
			result[key] = u.deinterfaceMetadata(val)
		}

		return result
	case []interface{}:
		result := make([]interface{}, len(value))
		for index, val := range value {
			result[index] = u.deinterfaceMetadata(val)
		}

		return result
	default:
		return data
	}
}
