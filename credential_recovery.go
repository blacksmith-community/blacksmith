package main

import (
	"fmt"
	"strings"
	"time"

	"blacksmith/pkg/reconciler"
	"github.com/hashicorp/vault/api"
)

// CredentialRecovery provides functions to recover lost credentials
type CredentialRecovery struct {
	broker *Broker
	vault  *Vault
	logger *Log
}

// NewCredentialRecovery creates a new credential recovery handler
func NewCredentialRecovery(broker *Broker, vault *Vault) *CredentialRecovery {
	return &CredentialRecovery{
		broker: broker,
		vault:  vault,
		logger: Logger.Wrap("credential-recovery"),
	}
}

// RecoverCredentialsForInstance attempts to recover credentials for a specific instance
func (cr *CredentialRecovery) RecoverCredentialsForInstance(instanceID string) error {
	cr.logger.Info("Starting credential recovery for instance %s", instanceID)

	// Step 1: Check if credentials already exist
	credsPath := fmt.Sprintf("%s/credentials", instanceID)
	var existingCreds map[string]interface{}
	exists, err := cr.vault.Get(credsPath, &existingCreds)
	if exists && err == nil && len(existingCreds) > 0 {
		cr.logger.Info("Credentials already exist for instance %s, no recovery needed", instanceID)
		return nil
	}

	// Step 2: Try to recover from backups
	cr.logger.Info("Attempting to recover credentials from backups")
	if recovered, err := cr.recoverFromBackups(instanceID); err == nil && recovered {
		cr.logger.Info("Successfully recovered credentials from backups")
		return nil
	}

	// Step 3: Try to recover from BOSH deployment
	cr.logger.Info("Attempting to recover credentials from BOSH deployment")
	if recovered, err := cr.recoverFromBOSH(instanceID); err == nil && recovered {
		cr.logger.Info("Successfully recovered credentials from BOSH")
		return nil
	}

	// Step 4: If all else fails, check if we can regenerate
	cr.logger.Info("WARNING: Unable to recover existing credentials, checking if regeneration is possible")
	if err := cr.checkRegenerationPossible(instanceID); err == nil {
		cr.logger.Info("Credentials can be regenerated - manual intervention required")
		return fmt.Errorf("credentials lost but can be regenerated - requires manual intervention")
	}

	return fmt.Errorf("unable to recover credentials for instance %s", instanceID)
}

// recoverFromBackups attempts to recover credentials from vault backups using the new format
func (cr *CredentialRecovery) recoverFromBackups(instanceID string) (bool, error) {
	cr.logger.Debug("Attempting to recover from new backup format for instance %s", instanceID)

	// First try to get the vault client for advanced operations
	vaultClient, err := cr.getVaultClient()
	if err != nil {
		cr.logger.Debug("Could not get vault client, falling back to VaultInterface: %s", err)
		return cr.recoverFromBackupsLegacy(instanceID)
	}

	// List available backups
	listPath := fmt.Sprintf("secret/metadata/backups/%s", instanceID)
	listResp, err := vaultClient.Logical().List(listPath)
	if err != nil || listResp == nil {
		cr.logger.Debug("No backups found for instance %s at new location", instanceID)
		return cr.recoverFromBackupsLegacy(instanceID)
	}

	// Parse backup entries and sort by timestamp
	type backupEntry struct {
		sha256    string
		timestamp int64
	}

	var backups []backupEntry
	if listResp.Data != nil {
		if keys, ok := listResp.Data["keys"].([]interface{}); ok {
			for _, keyInterface := range keys {
				if sha256, ok := keyInterface.(string); ok {
					// Read the backup to get its timestamp
					backupDataPath := fmt.Sprintf("secret/data/backups/%s/%s", instanceID, sha256)
					secret, err := vaultClient.Logical().Read(backupDataPath)
					if err != nil || secret == nil {
						cr.logger.Debug("Failed to read backup %s: %s", sha256, err)
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
								}

								backups = append(backups, backupEntry{
									sha256:    sha256,
									timestamp: timestamp,
								})
							}
						}
					}
				}
			}
		}
	}

	if len(backups) == 0 {
		cr.logger.Debug("No valid backups found for instance %s", instanceID)
		return false, fmt.Errorf("no valid backups found")
	}

	// Sort backups by timestamp (newest first)
	for i := 0; i < len(backups)-1; i++ {
		for j := i + 1; j < len(backups); j++ {
			if backups[i].timestamp < backups[j].timestamp {
				backups[i], backups[j] = backups[j], backups[i]
			}
		}
	}

	// Try to recover from most recent backup first
	for _, backup := range backups {
		cr.logger.Debug("Attempting recovery from backup %s (timestamp: %d)", backup.sha256, backup.timestamp)

		// Read the backup
		backupDataPath := fmt.Sprintf("secret/data/backups/%s/%s", instanceID, backup.sha256)
		secret, err := vaultClient.Logical().Read(backupDataPath)
		if err != nil || secret == nil {
			cr.logger.Debug("Failed to read backup %s: %s", backup.sha256, err)
			continue
		}

		// Extract archive data
		var archiveData string
		if secret.Data != nil {
			if data, ok := secret.Data["data"].(map[string]interface{}); ok {
				if archive, ok := data["archive"].(string); ok {
					archiveData = archive
				}
			}
		}

		if archiveData == "" {
			cr.logger.Debug("Backup %s has no archive data", backup.sha256)
			continue
		}

		// Decompress and decode the backup
		if err := cr.restoreFromCompressedBackup(instanceID, archiveData, backup.sha256); err != nil {
			cr.logger.Debug("Failed to restore from backup %s: %s", backup.sha256, err)
			continue
		}

		return true, nil
	}

	cr.logger.Debug("No credentials found in any new-format backups")
	return false, fmt.Errorf("no credentials in new-format backups")
}

// restoreFromCompressedBackup restores data from a compressed backup archive
func (cr *CredentialRecovery) restoreFromCompressedBackup(instanceID, archiveData, sha256 string) error {
	// Use the same decompression logic as the updater
	// We'll need to implement this or extract it to a shared utility
	decompressedData, err := cr.decompressAndDecode(archiveData)
	if err != nil {
		return fmt.Errorf("failed to decompress backup: %w", err)
	}

	// Look for credentials in the decompressed data
	credentialsFound := false
	for path, data := range decompressedData {
		// Check if this path contains credentials we can restore
		if strings.Contains(path, "/credentials") || strings.Contains(path, "/creds") {
			// Restore to the original path
			originalPath := strings.TrimPrefix(path, "secret/")
			if err := cr.vault.Put(originalPath, data); err != nil {
				cr.logger.Debug("Failed to restore %s: %s", originalPath, err)
				continue
			}

			cr.logger.Info("Restored credentials from backup %s to %s", sha256, originalPath)
			credentialsFound = true
		}
	}

	if !credentialsFound {
		return fmt.Errorf("no credentials found in backup")
	}

	// Add recovery metadata
	metadataPath := fmt.Sprintf("%s/metadata", instanceID)
	var metadata map[string]interface{}
	if _, getErr := cr.vault.Get(metadataPath, &metadata); getErr != nil {
		// Log get error but continue processing
	}
	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	metadata["credentials_recovered_at"] = time.Now().Format(time.RFC3339)
	metadata["credentials_recovered_from"] = fmt.Sprintf("backup_%s", sha256)
	if putErr := cr.vault.Put(metadataPath, metadata); putErr != nil {
		// Log put error but continue processing
	}

	return nil
}

// recoverFromBackupsLegacy fallback to old backup format for compatibility
func (cr *CredentialRecovery) recoverFromBackupsLegacy(instanceID string) (bool, error) {
	cr.logger.Debug("Falling back to legacy backup format recovery for instance %s", instanceID)

	// Check backup index (old format)
	backupIndexPath := fmt.Sprintf("%s/backups/index", instanceID)
	var backupIndex map[string]interface{}
	exists, err := cr.vault.Get(backupIndexPath, &backupIndex)
	if !exists || err != nil {
		cr.logger.Debug("No legacy backup index found for instance %s", instanceID)
		return false, fmt.Errorf("no legacy backup index found")
	}

	// Get list of backups
	backups, ok := backupIndex["backups"].([]interface{})
	if !ok || len(backups) == 0 {
		cr.logger.Debug("No legacy backups found in index for instance %s", instanceID)
		return false, fmt.Errorf("no legacy backups in index")
	}

	// Try to recover from most recent backup first
	for i := len(backups) - 1; i >= 0; i-- {
		switch v := backups[i].(type) {
		case map[string]interface{}:
			if sha256, ok := v["sha256"].(string); ok {
				backupPath := fmt.Sprintf("%s/backups/%s", instanceID, sha256)
				if cr.tryRecoverFromLegacyBackup(instanceID, backupPath, sha256) {
					return true, nil
				}
			}
		}
	}

	cr.logger.Debug("No credentials found in any legacy backups")
	return false, fmt.Errorf("no credentials in legacy backups")
}

// Helper methods for the new recovery system
func (cr *CredentialRecovery) getVaultClient() (*api.Client, error) {
	// Try to extract vault client from the vault interface
	// This depends on how the vault is implemented in the CredentialRecovery
	// For now, return an error to fall back to legacy mode
	return nil, fmt.Errorf("vault client extraction not implemented yet")
}

func (cr *CredentialRecovery) decompressAndDecode(encodedData string) (map[string]interface{}, error) {
	// This should match the implementation in updater.go
	// For now, return an error to use legacy recovery
	return nil, fmt.Errorf("decompression not implemented in credential recovery yet")
}

func (cr *CredentialRecovery) tryRecoverFromLegacyBackup(instanceID, backupPath, sha256 string) bool {
	cr.logger.Debug("Checking legacy backup at %s", backupPath)

	var backupData map[string]interface{}
	exists, err := cr.vault.Get(backupPath, &backupData)
	if !exists || err != nil {
		cr.logger.Debug("Could not read legacy backup at %s: %s", backupPath, err)
		return false
	}

	// Check if backup contains credentials in old format
	if data, ok := backupData["data"].(map[string]interface{}); ok {
		if creds, ok := data["credentials"].(map[string]interface{}); ok && len(creds) > 0 {
			cr.logger.Info("Found credentials in legacy backup %s", sha256)

			// Restore credentials
			credsPath := fmt.Sprintf("%s/credentials", instanceID)
			if err := cr.vault.Put(credsPath, creds); err != nil {
				cr.logger.Error("Failed to restore credentials: %s", err)
				return false
			}

			// Add recovery metadata
			metadataPath := fmt.Sprintf("%s/metadata", instanceID)
			var metadata map[string]interface{}
			if _, getErr := cr.vault.Get(metadataPath, &metadata); getErr != nil {
				// Log get error but continue processing
			}
			if metadata == nil {
				metadata = make(map[string]interface{})
			}
			metadata["credentials_recovered_at"] = time.Now().Format(time.RFC3339)
			metadata["credentials_recovered_from"] = fmt.Sprintf("legacy_backup_%s", sha256)
			if putErr := cr.vault.Put(metadataPath, metadata); putErr != nil {
				// Log put error but continue processing
			}

			return true
		}
	}

	return false
}

// recoverFromBOSH attempts to recover credentials from the BOSH deployment manifest
func (cr *CredentialRecovery) recoverFromBOSH(instanceID string) (bool, error) {
	// Get instance details
	instance, exists, err := cr.vault.FindInstance(instanceID)
	if !exists || err != nil {
		cr.logger.Error("Cannot find instance %s in vault index", instanceID)
		return false, fmt.Errorf("instance not found in index")
	}

	// Find the plan
	planKey := fmt.Sprintf("%s/%s", instance.ServiceID, instance.PlanID)
	plan, ok := cr.broker.Plans[planKey]
	if !ok {
		// Try without service ID
		plan, ok = cr.broker.Plans[instance.PlanID]
		if !ok {
			cr.logger.Error("Cannot find plan for instance %s", instanceID)
			return false, fmt.Errorf("plan not found")
		}
	}

	deploymentName := plan.ID + "-" + instanceID
	cr.logger.Info("Attempting to extract credentials from BOSH manifest for deployment %s", deploymentName)

	// Use the credential populator to fetch from manifest
	populator := reconciler.NewCredentialPopulator(nil, &loggerWrapper{logger: cr.logger})
	creds, err := populator.FetchCredsFromBOSH(instanceID, reconciler.Plan{
		ID:   plan.ID,
		Name: plan.Name,
	}, cr.broker.BOSH)
	if err != nil {
		cr.logger.Error("Failed to extract credentials from BOSH manifest: %s", err)
		return false, err
	}

	// Store recovered credentials
	credsPath := fmt.Sprintf("%s/credentials", instanceID)
	if err := cr.vault.Put(credsPath, creds); err != nil {
		cr.logger.Error("Failed to store recovered credentials: %s", err)
		return false, err
	}

	// Verify storage
	var verifyCreds map[string]interface{}
	exists2, err := cr.vault.Get(credsPath, &verifyCreds)
	if !exists2 || err != nil {
		cr.logger.Error("Failed to verify credential storage after recovery")
		return false, fmt.Errorf("verification failed")
	}

	// Add recovery metadata
	metadataPath := fmt.Sprintf("%s/metadata", instanceID)
	var metadata map[string]interface{}
	if _, getErr := cr.vault.Get(metadataPath, &metadata); getErr != nil {
		// Log get error but continue processing
	}
	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	metadata["credentials_recovered_at"] = time.Now().Format(time.RFC3339)
	metadata["credentials_recovered_from"] = "bosh_deployment"
	if putErr := cr.vault.Put(metadataPath, metadata); putErr != nil {
		// Log put error but continue processing
	}

	cr.logger.Info("Successfully recovered credentials from BOSH")
	return true, nil
}

// checkRegenerationPossible checks if credentials can be regenerated
func (cr *CredentialRecovery) checkRegenerationPossible(instanceID string) error {
	instance, exists, err := cr.vault.FindInstance(instanceID)
	if !exists || err != nil {
		return fmt.Errorf("instance not found")
	}

	// Check if deployment still exists
	deploymentName := instance.PlanID + "-" + instanceID
	_, err = cr.broker.BOSH.GetDeployment(deploymentName)
	if err != nil {
		cr.logger.Error("Deployment %s does not exist in BOSH", deploymentName)
		return fmt.Errorf("deployment not found")
	}

	// Check service type
	serviceType := strings.Split(instance.PlanID, "-")[0]
	regeneratable := map[string]bool{
		"redis":      true,  // Redis passwords can be reset
		"postgresql": true,  // PostgreSQL passwords can be reset
		"mysql":      true,  // MySQL passwords can be reset
		"rabbitmq":   false, // RabbitMQ requires more complex recovery
		"vault":      false, // Vault credentials are critical
	}

	if canRegen, ok := regeneratable[serviceType]; ok && canRegen {
		cr.logger.Info("Service type %s supports credential regeneration", serviceType)
		return nil
	}

	return fmt.Errorf("service type %s does not support easy regeneration", serviceType)
}

// RecoverAllMissingCredentials scans all instances and recovers missing credentials
func (cr *CredentialRecovery) RecoverAllMissingCredentials() error {
	cr.logger.Info("Starting recovery scan for all instances")

	// Get all instances from index
	idx, err := cr.vault.GetIndex("db")
	if err != nil {
		return fmt.Errorf("failed to get vault index: %w", err)
	}

	recovered := 0
	failed := 0
	skipped := 0

	for instanceID := range idx.Data {
		// Check if credentials exist
		credsPath := fmt.Sprintf("%s/credentials", instanceID)
		var creds map[string]interface{}
		exists, err := cr.vault.Get(credsPath, &creds)

		if exists && err == nil && len(creds) > 0 {
			cr.logger.Debug("Instance %s has credentials, skipping", instanceID)
			skipped++
			continue
		}

		// Attempt recovery
		cr.logger.Info("Instance %s missing credentials, attempting recovery", instanceID)
		if err := cr.RecoverCredentialsForInstance(instanceID); err != nil {
			cr.logger.Error("Failed to recover credentials for %s: %s", instanceID, err)
			failed++
		} else {
			cr.logger.Info("Successfully recovered credentials for %s", instanceID)
			recovered++
		}
	}

	cr.logger.Info("Recovery scan complete - Recovered: %d, Failed: %d, Skipped: %d",
		recovered, failed, skipped)

	if failed > 0 {
		return fmt.Errorf("failed to recover credentials for %d instances", failed)
	}

	return nil
}

// VerifyInstanceCredentials verifies that an instance has valid credentials
func (cr *CredentialRecovery) VerifyInstanceCredentials(instanceID string) error {
	credsPath := fmt.Sprintf("%s/credentials", instanceID)
	var creds map[string]interface{}
	exists, err := cr.vault.Get(credsPath, &creds)

	if !exists || err != nil {
		return fmt.Errorf("no credentials found for instance %s", instanceID)
	}

	// Check for required fields based on service type
	instance, _, _ := cr.vault.FindInstance(instanceID)
	if instance != nil {
		serviceType := strings.Split(instance.PlanID, "-")[0]

		switch serviceType {
		case "rabbitmq":
			required := []string{"username", "password", "host", "port"}
			for _, field := range required {
				if _, ok := creds[field]; !ok {
					return fmt.Errorf("missing required field '%s' in credentials", field)
				}
			}
		case "redis":
			if _, ok := creds["password"]; !ok {
				return fmt.Errorf("missing password in Redis credentials")
			}
		case "postgresql", "mysql":
			required := []string{"username", "password", "host", "port", "database"}
			for _, field := range required {
				if _, ok := creds[field]; !ok {
					return fmt.Errorf("missing required field '%s' in credentials", field)
				}
			}
		}
	}

	cr.logger.Debug("Credentials verified for instance %s", instanceID)
	return nil
}
