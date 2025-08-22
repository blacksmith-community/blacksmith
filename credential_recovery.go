package main

import (
	"fmt"
	"strings"
	"time"

	"blacksmith/pkg/reconciler"
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

// recoverFromBackups attempts to recover credentials from vault backups
func (cr *CredentialRecovery) recoverFromBackups(instanceID string) (bool, error) {
	// Check backup index
	backupIndexPath := fmt.Sprintf("%s/backups/index", instanceID)
	var backupIndex map[string]interface{}
	exists, err := cr.vault.Get(backupIndexPath, &backupIndex)
	if !exists || err != nil {
		cr.logger.Debug("No backup index found for instance %s", instanceID)
		return false, fmt.Errorf("no backup index found")
	}

	// Get list of backups
	backups, ok := backupIndex["backups"].([]interface{})
	if !ok || len(backups) == 0 {
		cr.logger.Debug("No backups found in index for instance %s", instanceID)
		return false, fmt.Errorf("no backups in index")
	}

	// Try to recover from most recent backup first
	for i := len(backups) - 1; i >= 0; i-- {
		var timestamp int64
		switch v := backups[i].(type) {
		case float64:
			timestamp = int64(v)
		case int64:
			timestamp = v
		default:
			continue
		}

		backupPath := fmt.Sprintf("%s/backups/%d", instanceID, timestamp)
		cr.logger.Debug("Checking backup at %s", backupPath)

		var backupData map[string]interface{}
		exists, err := cr.vault.Get(backupPath, &backupData)
		if !exists || err != nil {
			cr.logger.Debug("Could not read backup at %s: %s", backupPath, err)
			continue
		}

		// Check if backup contains credentials
		if creds, ok := backupData["credentials"].(map[string]interface{}); ok && len(creds) > 0 {
			cr.logger.Info("Found credentials in backup from timestamp %d", timestamp)

			// Restore credentials
			credsPath := fmt.Sprintf("%s/credentials", instanceID)
			if err := cr.vault.Put(credsPath, creds); err != nil {
				cr.logger.Error("Failed to restore credentials: %s", err)
				return false, err
			}

			// Verify restoration
			var verifyCreds map[string]interface{}
			exists, err := cr.vault.Get(credsPath, &verifyCreds)
			if exists && err == nil {
				cr.logger.Info("Successfully restored credentials from backup")

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
				metadata["credentials_recovered_from"] = fmt.Sprintf("backup_%d", timestamp)
				if putErr := cr.vault.Put(metadataPath, metadata); putErr != nil {
					// Log put error but continue processing
				}

				return true, nil
			}
		}
	}

	cr.logger.Debug("No credentials found in any backups")
	return false, fmt.Errorf("no credentials in backups")
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
