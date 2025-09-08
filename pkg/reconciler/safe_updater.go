package reconciler

import (
	"context"
	"errors"
	"fmt"
)

// Static errors for err113 compliance.
var (
	ErrInstanceMissingCredentials = errors.New("instance missing required credentials")
)

// SafeVaultUpdater wraps VaultUpdater with additional safeguards for critical paths.
type SafeVaultUpdater struct {
	*VaultUpdater

	protectedPaths []string
}

// NewSafeVaultUpdater creates a new SafeVaultUpdater with protection for critical paths.
func NewSafeVaultUpdater(vault interface{}, logger Logger, backupConfig BackupConfig) *SafeVaultUpdater {
	baseUpdater := NewVaultUpdater(vault, logger, backupConfig)

	return &SafeVaultUpdater{
		VaultUpdater: baseUpdater,
	}
}

// UpdateInstance updates an instance with protection for critical paths.
func (u *SafeVaultUpdater) UpdateInstance(ctx context.Context, instance InstanceData) (*InstanceData, error) {
	u.logger.Infof("Safe update starting for instance %s", instance.ID)

	// Step 1: Check and preserve protected paths
	protectedData := make(map[string]map[string]interface{})

	for _, path := range u.protectedPaths {
		fullPath := fmt.Sprintf("secret/%s%s", instance.ID, path)
		if data, err := u.getFromVault(fullPath); err == nil && len(data) > 0 {
			protectedData[path] = data
			u.logger.Infof("Protected path %s exists with %d entries, will preserve", fullPath, len(data))
		}
	}

	// Step 2: Create backup before any modifications
	if u.backupConfig.Enabled {
		err := u.backupInstance(instance.ID)
		if err != nil {
			u.logger.Warningf("Failed to create backup for instance %s: %s (continuing anyway)", instance.ID, err)
		}
	}

	// Step 3: Perform the regular update
	updatedInstance, err := u.VaultUpdater.UpdateInstance(ctx, instance)
	if err != nil {
		return nil, fmt.Errorf("base update failed: %w", err)
	}

	// Step 4: Verify protected paths are still intact
	for path, originalData := range protectedData {
		fullPath := fmt.Sprintf("secret/%s%s", instance.ID, path)
		currentData, err := u.getFromVault(fullPath)

		if err != nil || len(currentData) == 0 {
			u.logger.Errorf("CRITICAL: Protected path %s was lost during update, restoring", fullPath)
			// Restore the protected data
			err := u.putToVault(fullPath, originalData)
			if err != nil {
				return nil, fmt.Errorf("failed to restore protected path %s: %w", fullPath, err)
			}

			u.logger.Infof("Successfully restored protected path %s", fullPath)
		} else {
			u.logger.Debugf("Protected path %s verified intact", fullPath)
		}
	}

	// Step 5: Final verification
	if err := u.verifyInstanceIntegrity(instance.ID); err != nil {
		return nil, fmt.Errorf("instance integrity check failed: %w", err)
	}

	u.logger.Infof("Safe update completed successfully for instance %s", instance.ID)

	return updatedInstance, nil
}

// requiresCredentials returns true if the service type requires stored credentials.
func requiresCredentials(serviceID string) bool {
	// Services that require credentials to be stored
	credentialServices := map[string]bool{
		"rabbitmq":   true,
		"redis":      true,
		"postgresql": true,
		"mysql":      true,
		"vault":      true,
	}

	return credentialServices[serviceID]
}

// DeleteInstance safely removes an instance with verification.
func (u *SafeVaultUpdater) DeleteInstance(ctx context.Context, instanceID string) error {
	u.logger.Infof("Safe delete requested for instance %s", instanceID)

	// Create final backup before deletion using new backup format
	if u.backupConfig.Enabled && u.backupConfig.BackupOnDelete {
		err := u.backupInstance(instanceID)
		if err != nil {
			u.logger.Errorf("Failed to create final backup: %s", err)
			// Don't fail deletion due to backup failure
		} else {
			u.logger.Infof("Created final backup for instance %s before deletion", instanceID)
		}
	}

	// Remove from index
	err := u.UpdateIndex(instanceID, nil)
	if err != nil {
		return fmt.Errorf("failed to remove from index: %w", err)
	}

	u.logger.Infof("Instance %s safely removed from index (data preserved in vault)", instanceID)

	return nil
}

// verifyInstanceIntegrity checks that an instance has all required data.
func (u *SafeVaultUpdater) verifyInstanceIntegrity(instanceID string) error {
	// Check credentials exist for service instances that need them
	credsPath := instanceID + "/credentials"

	indexData, err := u.GetFromIndex(instanceID)
	if err != nil {
		return fmt.Errorf("failed to get instance from index: %w", err)
	}

	// Check if this is a service that requires credentials
	if serviceID, ok := indexData["service_id"].(string); ok {
		if requiresCredentials(serviceID) {
			if _, err := u.getFromVault(credsPath); err != nil {
				return fmt.Errorf("%w: %s", ErrInstanceMissingCredentials, instanceID)
			}
		}
	}

	// Check manifest exists
	manifestPath := instanceID + "/manifest"
	if _, err := u.getFromVault(manifestPath); err != nil {
		u.logger.Warningf("Instance %s missing manifest (may be okay for some services)", instanceID)
	}

	return nil
}
