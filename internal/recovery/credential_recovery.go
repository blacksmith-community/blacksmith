package recovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"blacksmith/internal/broker"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/reconciler"
	"blacksmith/pkg/vault"
	"github.com/hashicorp/vault/api"
)

// Static errors for err113 compliance.
var (
	ErrCredentialsLostButCanBeRegenerated        = errors.New("credentials lost but can be regenerated - requires manual intervention")
	ErrUnableToRecoverCredentials                = errors.New("unable to recover credentials for instance")
	ErrNoValidBackupsFound                       = errors.New("no valid backups found")
	ErrNoBackupsFound                            = errors.New("no backups found")
	ErrNoCredentialsInNewFormatBackups           = errors.New("no credentials in new-format backups")
	ErrNoCredentialsFoundInBackup                = errors.New("no credentials found in backup")
	ErrNoLegacyBackupIndexFound                  = errors.New("no legacy backup index found")
	ErrNoLegacyBackupsInIndex                    = errors.New("no legacy backups in index")
	ErrNoCredentialsInLegacyBackups              = errors.New("no credentials in legacy backups")
	ErrVaultClientExtractionNotImplemented       = errors.New("vault client extraction not implemented yet")
	ErrDecompressionNotImplementedInRecovery     = errors.New("decompression not implemented in credential recovery yet")
	ErrInstanceNotFoundInIndex                   = errors.New("instance not found in index")
	ErrCredentialRecoveryPlanNotFound            = errors.New("plan not found")
	ErrVerificationFailed                        = errors.New("verification failed")
	ErrInstanceNotFound                          = errors.New("instance not found")
	ErrServiceTypeDoesNotSupportEasyRegeneration = errors.New("service type does not support easy regeneration")
	ErrFailedToRecoverCredentialsForInstances    = errors.New("failed to recover credentials for instances")
	ErrNoCredentialsFoundForInstance             = errors.New("no credentials found for instance")
	ErrMissingRequiredFieldInCredentials         = errors.New("missing required field in credentials")
	ErrMissingPasswordInRedisCredentials         = errors.New("missing password in Redis credentials")
	ErrDeploymentNotFound                        = errors.New("deployment not found")
)

// CredentialRecovery provides functions to recover lost credentials.
type CredentialRecovery struct {
	broker *broker.Broker
	vault  *internalVault.Vault
	logger logger.Logger
}

// NewCredentialRecovery creates a new credential recovery handler.
func NewCredentialRecovery(broker *broker.Broker, vault *internalVault.Vault) *CredentialRecovery {
	return &CredentialRecovery{
		broker: broker,
		vault:  vault,
		logger: logger.Get().Named("credential-recovery"),
	}
}

// RecoverCredentialsForInstance attempts to recover credentials for a specific instance.
func (cr *CredentialRecovery) RecoverCredentialsForInstance(ctx context.Context, instanceID string) error {
	cr.logger.Info("Starting credential recovery for instance %s", instanceID)

	// Step 1: Check if credentials already exist
	credsPath := instanceID + "/credentials"

	var existingCreds map[string]interface{}

	exists, err := cr.vault.Get(ctx, credsPath, &existingCreds)
	if exists && err == nil && len(existingCreds) > 0 {
		cr.logger.Info("Credentials already exist for instance %s, no recovery needed", instanceID)

		return nil
	}

	// Step 2: Try to recover from backups
	cr.logger.Info("Attempting to recover credentials from backups")

	recovered, err := cr.recoverFromBackups(ctx, instanceID)
	if err == nil && recovered {
		cr.logger.Info("Successfully recovered credentials from backups")

		return nil
	}

	// Step 3: Try to recover from BOSH deployment
	cr.logger.Info("Attempting to recover credentials from BOSH deployment")

	recovered, err = cr.recoverFromBOSH(ctx, instanceID)
	if err == nil && recovered {
		cr.logger.Info("Successfully recovered credentials from BOSH")

		return nil
	}

	// Step 4: If all else fails, check if we can regenerate
	cr.logger.Info("WARNING: Unable to recover existing credentials, checking if regeneration is possible")

	err = cr.checkRegenerationPossible(ctx, instanceID)
	if err == nil {
		cr.logger.Info("Credentials can be regenerated - manual intervention required")

		return ErrCredentialsLostButCanBeRegenerated
	}

	return fmt.Errorf("unable to recover credentials for instance %s: %w", instanceID, ErrUnableToRecoverCredentials)
}

// backupEntry represents a backup entry with metadata.
type backupEntry struct {
	sha256    string
	timestamp int64
}

// RecoverAllMissingCredentials scans all instances and recovers missing credentials.
func (cr *CredentialRecovery) RecoverAllMissingCredentials(ctx context.Context) error {
	cr.logger.Info("Starting recovery scan for all instances")

	// Get all instances from index
	idx, err := cr.vault.GetIndex(ctx, "db")
	if err != nil {
		return fmt.Errorf("failed to get vault index: %w", err)
	}

	recovered := 0
	failed := 0
	skipped := 0

	for instanceID := range idx.Data {
		// Check if credentials exist
		credsPath := instanceID + "/credentials"

		var creds map[string]interface{}

		exists, err := cr.vault.Get(ctx, credsPath, &creds)

		if exists && err == nil && len(creds) > 0 {
			cr.logger.Debug("Instance %s has credentials, skipping", instanceID)

			skipped++

			continue
		}

		// Attempt recovery
		cr.logger.Info("Instance %s missing credentials, attempting recovery", instanceID)

		err = cr.RecoverCredentialsForInstance(ctx, instanceID)
		if err != nil {
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
		return fmt.Errorf("failed to recover credentials for %d instances: %w", failed, ErrFailedToRecoverCredentialsForInstances)
	}

	return nil
}

// VerifyInstanceCredentials verifies that an instance has valid credentials.
func (cr *CredentialRecovery) VerifyInstanceCredentials(ctx context.Context, instanceID string) error {
	credsPath := instanceID + "/credentials"

	var creds map[string]interface{}

	exists, err := cr.vault.Get(ctx, credsPath, &creds)

	if !exists || err != nil {
		return fmt.Errorf("no credentials found for instance %s: %w", instanceID, ErrNoCredentialsFoundForInstance)
	}

	// Check for required fields based on service type
	instance, _, _ := cr.vault.FindInstance(ctx, instanceID)
	if instance != nil {
		serviceType := strings.Split(instance.PlanID, "-")[0]

		switch serviceType {
		case "rabbitmq":
			required := []string{"username", "password", "host", "port"}
			for _, field := range required {
				if _, ok := creds[field]; !ok {
					return fmt.Errorf("missing required field '%s' in credentials: %w", field, ErrMissingRequiredFieldInCredentials)
				}
			}
		case "redis":
			if _, ok := creds["password"]; !ok {
				return ErrMissingPasswordInRedisCredentials
			}
		case "postgresql", "mysql":
			required := []string{"username", "password", "host", "port", "database"}
			for _, field := range required {
				if _, ok := creds[field]; !ok {
					return fmt.Errorf("missing required field '%s' in credentials: %w", field, ErrMissingRequiredFieldInCredentials)
				}
			}
		}
	}

	cr.logger.Debug("Credentials verified for instance %s", instanceID)

	return nil
}

// credRecoveryLoggerWrapper wraps the logger to implement the reconciler logger interface.
type credRecoveryLoggerWrapper struct {
	logger logger.Logger
}

func (l *credRecoveryLoggerWrapper) Debugf(format string, args ...interface{}) {
	l.logger.Debug(format, args...)
}

func (l *credRecoveryLoggerWrapper) Infof(format string, args ...interface{}) {
	l.logger.Info(format, args...)
}

func (l *credRecoveryLoggerWrapper) Warningf(format string, args ...interface{}) {
	l.logger.Info("[WARN] "+format, args...)
}

func (l *credRecoveryLoggerWrapper) Errorf(format string, args ...interface{}) {
	l.logger.Error(format, args...)
}

// sortBackupsByTimestamp sorts backups by timestamp (newest first).
func (cr *CredentialRecovery) sortBackupsByTimestamp(backups []backupEntry) {
	for i := range len(backups) - 1 {
		for j := i + 1; j < len(backups); j++ {
			if backups[i].timestamp < backups[j].timestamp {
				backups[i], backups[j] = backups[j], backups[i]
			}
		}
	}
}

// attemptRecoveryFromBackups tries to recover from each backup in order.
func (cr *CredentialRecovery) attemptRecoveryFromBackups(ctx context.Context, vaultClient *api.Client, instanceID string, backups []backupEntry) (bool, error) {
	for _, backup := range backups {
		cr.logger.Debug("Attempting recovery from backup %s (timestamp: %d)", backup.sha256, backup.timestamp)

		if cr.tryRecoverFromSingleBackup(ctx, vaultClient, instanceID, backup) {
			return true, nil
		}
	}

	cr.logger.Debug("No credentials found in any new-format backups")

	return false, ErrNoCredentialsInNewFormatBackups
}

// tryRecoverFromSingleBackup attempts to recover from a single backup.
func (cr *CredentialRecovery) tryRecoverFromSingleBackup(ctx context.Context, vaultClient *api.Client, instanceID string, backup backupEntry) bool {
	backupDataPath := fmt.Sprintf("secret/data/backups/%s/%s", instanceID, backup.sha256)

	secret, err := vaultClient.Logical().Read(backupDataPath)
	if err != nil || secret == nil {
		cr.logger.Debug("Failed to read backup %s: %s", backup.sha256, err)

		return false
	}

	archiveData := cr.extractArchiveData(secret)
	if archiveData == "" {
		cr.logger.Debug("Backup %s has no archive data", backup.sha256)

		return false
	}

	err = cr.restoreFromCompressedBackup(ctx, instanceID, archiveData, backup.sha256)
	if err != nil {
		cr.logger.Debug("Failed to restore from backup %s: %s", backup.sha256, err)

		return false
	}

	return true
}

// extractArchiveData extracts archive data from a backup secret.
func (cr *CredentialRecovery) extractArchiveData(secret *api.Secret) string {
	if secret.Data == nil {
		return ""
	}

	data, valid := secret.Data["data"].(map[string]interface{})
	if !valid {
		return ""
	}

	archive, valid := data["archive"].(string)
	if !valid {
		return ""
	}

	return archive
}

// restoreFromCompressedBackup restores data from a compressed backup archive.
func (cr *CredentialRecovery) restoreFromCompressedBackup(ctx context.Context, instanceID, archiveData, sha256 string) error {
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

			err := cr.vault.Put(ctx, originalPath, data)
			if err != nil {
				cr.logger.Debug("Failed to restore %s: %s", originalPath, err)

				continue
			}

			cr.logger.Info("Restored credentials from backup %s to %s", sha256, originalPath)

			credentialsFound = true
		}
	}

	if !credentialsFound {
		return ErrNoCredentialsFoundInBackup
	}

	// Add recovery metadata
	metadataPath := instanceID + "/metadata"

	var metadata map[string]interface{}

	_, _ = cr.vault.Get(ctx, metadataPath, &metadata)
	if metadata == nil {
		metadata = make(map[string]interface{})
	}

	metadata["credentials_recovered_at"] = time.Now().Format(time.RFC3339)
	metadata["credentials_recovered_from"] = "backup_" + sha256
	_ = cr.vault.Put(ctx, metadataPath, metadata)

	return nil
}

// recoverFromBackupsLegacy fallback to old backup format for compatibility.
func (cr *CredentialRecovery) recoverFromBackupsLegacy(ctx context.Context, instanceID string) (bool, error) {
	cr.logger.Debug("Falling back to legacy backup format recovery for instance %s", instanceID)

	// Check backup index (old format)
	backupIndexPath := instanceID + "/backups/index"

	var backupIndex map[string]interface{}

	exists, err := cr.vault.Get(ctx, backupIndexPath, &backupIndex)
	if !exists || err != nil {
		cr.logger.Debug("No legacy backup index found for instance %s", instanceID)

		return false, ErrNoLegacyBackupIndexFound
	}

	// Get list of backups
	backups, ok := backupIndex["backups"].([]interface{})
	if !ok || len(backups) == 0 {
		cr.logger.Debug("No legacy backups found in index for instance %s", instanceID)

		return false, ErrNoLegacyBackupsInIndex
	}

	// Try to recover from most recent backup first
	for i := len(backups) - 1; i >= 0; i-- {
		if v, ok := backups[i].(map[string]interface{}); ok {
			if sha256, ok := v["sha256"].(string); ok {
				backupPath := fmt.Sprintf("%s/backups/%s", instanceID, sha256)
				if cr.tryRecoverFromLegacyBackup(ctx, instanceID, backupPath, sha256) {
					return true, nil
				}
			}
		}
	}

	cr.logger.Debug("No credentials found in any legacy backups")

	return false, ErrNoCredentialsInLegacyBackups
}

// getVaultClient extracts vault client from the vault interface.
func (cr *CredentialRecovery) getVaultClient() (*api.Client, error) {
	// Try to extract vault client from the vault interface
	// This depends on how the vault is implemented in the CredentialRecovery
	// For now, return an error to fall back to legacy mode
	return nil, ErrVaultClientExtractionNotImplemented
}

// decompressAndDecode decompresses and decodes backup data.
func (cr *CredentialRecovery) decompressAndDecode(encodedData string) (map[string]interface{}, error) {
	// This should match the implementation in updater.go
	// For now, return an error to use legacy recovery
	return nil, ErrDecompressionNotImplementedInRecovery
}

// tryRecoverFromLegacyBackup attempts to recover from a legacy backup.
func (cr *CredentialRecovery) tryRecoverFromLegacyBackup(ctx context.Context, instanceID, backupPath, sha256 string) bool {
	cr.logger.Debug("Checking legacy backup at %s", backupPath)

	var backupData map[string]interface{}

	exists, err := cr.vault.Get(ctx, backupPath, &backupData)
	if !exists || err != nil {
		cr.logger.Debug("Could not read legacy backup at %s: %s", backupPath, err)

		return false
	}

	// Check if backup contains credentials in old format
	if data, ok := backupData["data"].(map[string]interface{}); ok {
		if creds, ok := data["credentials"].(map[string]interface{}); ok && len(creds) > 0 {
			cr.logger.Info("Found credentials in legacy backup %s", sha256)

			// Restore credentials
			credsPath := instanceID + "/credentials"

			err := cr.vault.Put(ctx, credsPath, creds)
			if err != nil {
				cr.logger.Error("Failed to restore credentials: %s", err)

				return false
			}

			// Add recovery metadata
			metadataPath := instanceID + "/metadata"

			var metadata map[string]interface{}

			_, _ = cr.vault.Get(ctx, metadataPath, &metadata)
			if metadata == nil {
				metadata = make(map[string]interface{})
			}

			metadata["credentials_recovered_at"] = time.Now().Format(time.RFC3339)
			metadata["credentials_recovered_from"] = "legacy_backup_" + sha256
			_ = cr.vault.Put(ctx, metadataPath, metadata)

			return true
		}
	}

	return false
}

// recoverFromBOSH attempts to recover credentials from the BOSH deployment manifest.
func (cr *CredentialRecovery) recoverFromBOSH(ctx context.Context, instanceID string) (bool, error) {
	_, plan, err := cr.getInstanceAndPlan(ctx, instanceID)
	if err != nil {
		return false, err
	}

	creds, err := cr.extractCredentialsFromBOSH(instanceID, plan)
	if err != nil {
		return false, err
	}

	err = cr.storeAndVerifyCredentials(ctx, instanceID, creds)
	if err != nil {
		return false, err
	}

	cr.addRecoveryMetadata(ctx, instanceID)
	cr.logger.Info("Successfully recovered credentials from BOSH")

	return true, nil
}

func (cr *CredentialRecovery) getInstanceAndPlan(ctx context.Context, instanceID string) (*vault.Instance, services.Plan, error) {
	instance, exists, err := cr.vault.FindInstance(ctx, instanceID)
	if !exists || err != nil {
		cr.logger.Error("Cannot find instance %s in vault index", instanceID)

		return nil, services.Plan{}, ErrInstanceNotFoundInIndex
	}

	planKey := fmt.Sprintf("%s/%s", instance.ServiceID, instance.PlanID)

	plan, valid := cr.broker.Plans[planKey]
	if !valid {
		// Try without service ID
		plan, valid = cr.broker.Plans[instance.PlanID]
		if !valid {
			cr.logger.Error("Cannot find plan for instance %s", instanceID)

			return nil, services.Plan{}, ErrCredentialRecoveryPlanNotFound
		}
	}

	return instance, plan, nil
}

func (cr *CredentialRecovery) extractCredentialsFromBOSH(instanceID string, plan services.Plan) (map[string]interface{}, error) {
	deploymentName := plan.ID + "-" + instanceID
	cr.logger.Info("Attempting to extract credentials from BOSH manifest for deployment %s", deploymentName)

	populator := reconciler.NewCredentialPopulator(nil, &credRecoveryLoggerWrapper{logger: cr.logger})

	creds, err := populator.FetchCredsFromBOSH(instanceID, reconciler.Plan{
		ID:   plan.ID,
		Name: plan.Name,
	}, cr.broker.BOSH)
	if err != nil {
		cr.logger.Error("Failed to extract credentials from BOSH manifest: %s", err)

		return nil, fmt.Errorf("failed to extract credentials from BOSH manifest: %w", err)
	}

	return creds, nil
}

func (cr *CredentialRecovery) storeAndVerifyCredentials(ctx context.Context, instanceID string, creds map[string]interface{}) error {
	credsPath := instanceID + "/credentials"

	err := cr.vault.Put(ctx, credsPath, creds)
	if err != nil {
		cr.logger.Error("Failed to store recovered credentials: %s", err)

		return fmt.Errorf("failed to store recovered credentials: %w", err)
	}

	// Verify storage
	var verifyCreds map[string]interface{}

	exists, err := cr.vault.Get(ctx, credsPath, &verifyCreds)
	if !exists || err != nil {
		cr.logger.Error("Failed to verify credential storage after recovery")

		return ErrVerificationFailed
	}

	return nil
}

func (cr *CredentialRecovery) addRecoveryMetadata(ctx context.Context, instanceID string) {
	metadataPath := instanceID + "/metadata"

	var metadata map[string]interface{}

	_, _ = cr.vault.Get(ctx, metadataPath, &metadata)
	if metadata == nil {
		metadata = make(map[string]interface{})
	}

	metadata["credentials_recovered_at"] = time.Now().Format(time.RFC3339)
	metadata["credentials_recovered_from"] = "bosh_deployment"
	_ = cr.vault.Put(ctx, metadataPath, metadata)
}

// checkRegenerationPossible checks if credentials can be regenerated.
func (cr *CredentialRecovery) checkRegenerationPossible(ctx context.Context, instanceID string) error {
	instance, exists, err := cr.vault.FindInstance(ctx, instanceID)
	if !exists || err != nil {
		return ErrInstanceNotFound
	}

	// Check if deployment still exists
	deploymentName := instance.PlanID + "-" + instanceID

	_, err = cr.broker.BOSH.GetDeployment(deploymentName)
	if err != nil {
		cr.logger.Error("Deployment %s does not exist in BOSH", deploymentName)

		return ErrDeploymentNotFound
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

	return fmt.Errorf("service type %s does not support easy regeneration: %w", serviceType, ErrServiceTypeDoesNotSupportEasyRegeneration)
}

// recoverFromBackups attempts to recover credentials from vault backups using the new format.
func (cr *CredentialRecovery) recoverFromBackups(ctx context.Context, instanceID string) (bool, error) {
	cr.logger.Debug("Attempting to recover from new backup format for instance %s", instanceID)

	vaultClient, err := cr.getVaultClient()
	if err != nil {
		cr.logger.Debug("Could not get vault client, falling back to VaultInterface: %s", err)

		return cr.recoverFromBackupsLegacy(ctx, instanceID)
	}

	backups, err := cr.collectBackupsFromVault(vaultClient, instanceID)
	if err != nil {
		return cr.recoverFromBackupsLegacy(ctx, instanceID)
	}

	if len(backups) == 0 {
		cr.logger.Debug("No valid backups found for instance %s", instanceID)

		return false, ErrNoValidBackupsFound
	}

	cr.sortBackupsByTimestamp(backups)

	return cr.attemptRecoveryFromBackups(ctx, vaultClient, instanceID, backups)
}

// collectBackupsFromVault collects all valid backups from Vault.
func (cr *CredentialRecovery) collectBackupsFromVault(vaultClient *api.Client, instanceID string) ([]backupEntry, error) {
	listPath := "secret/metadata/backups/" + instanceID

	listResp, err := vaultClient.Logical().List(listPath)
	if err != nil || listResp == nil {
		cr.logger.Debug("No backups found for instance %s at new location", instanceID)

		return nil, ErrNoBackupsFound
	}

	var backups []backupEntry

	if listResp.Data != nil {
		if keys, ok := listResp.Data["keys"].([]interface{}); ok {
			for _, keyInterface := range keys {
				if sha256, ok := keyInterface.(string); ok {
					backup := cr.processBackupEntry(vaultClient, instanceID, sha256)
					if backup != nil {
						backups = append(backups, *backup)
					}
				}
			}
		}
	}

	return backups, nil
}

// processBackupEntry processes a single backup entry to extract timestamp.
func (cr *CredentialRecovery) processBackupEntry(vaultClient *api.Client, instanceID, sha256 string) *backupEntry {
	backupDataPath := fmt.Sprintf("secret/data/backups/%s/%s", instanceID, sha256)

	secret, err := vaultClient.Logical().Read(backupDataPath)
	if err != nil || secret == nil {
		cr.logger.Debug("Failed to read backup %s: %s", sha256, err)

		return nil
	}

	if secret.Data == nil {
		return nil
	}

	data, valid := secret.Data["data"].(map[string]interface{})
	if !valid {
		return nil
	}

	ts, valid := data["timestamp"]
	if !valid {
		return nil
	}

	timestamp := cr.parseTimestamp(ts)
	if timestamp == 0 {
		return nil
	}

	return &backupEntry{
		sha256:    sha256,
		timestamp: timestamp,
	}
}

// parseTimestamp parses timestamp from various numeric types.
func (cr *CredentialRecovery) parseTimestamp(ts interface{}) int64 {
	switch value := ts.(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	default:
		return 0
	}
}
