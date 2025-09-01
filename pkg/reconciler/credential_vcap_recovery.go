package reconciler

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CredentialVCAPRecovery handles recovering missing credentials from CF VCAP_SERVICES
type CredentialVCAPRecovery struct {
	vault     VaultInterface
	cfManager CFManagerInterface
	logger    Logger
}

// CFManagerInterface defines methods needed from CF connection manager
type CFManagerInterface interface {
	FindAppsByServiceInstance(ctx context.Context, serviceInstanceGUID string) ([]string, error)
	GetAppEnvironmentWithVCAP(ctx context.Context, appGUID string) (map[string]interface{}, error)
}

// NewCredentialVCAPRecovery creates a new VCAP credential recovery handler
func NewCredentialVCAPRecovery(vault VaultInterface, cfManager CFManagerInterface, logger Logger) *CredentialVCAPRecovery {
	return &CredentialVCAPRecovery{
		vault:     vault,
		cfManager: cfManager,
		logger:    logger,
	}
}

// RecoverCredentialsFromVCAP attempts to recover missing credentials from CF app VCAP_SERVICES
func (c *CredentialVCAPRecovery) RecoverCredentialsFromVCAP(ctx context.Context, instanceID string) error {
	c.logger.Info("Attempting to recover credentials from VCAP_SERVICES for instance %s", instanceID)

	// Check if credentials already exist
	credsPath := fmt.Sprintf("%s/credentials", instanceID)
	existingCreds, err := c.vault.Get(credsPath)
	if err == nil && len(existingCreds) > 0 {
		c.logger.Debug("Credentials already exist for instance %s, skipping VCAP recovery", instanceID)
		return nil
	}

	// Find apps bound to this service instance
	appGUIDs, err := c.cfManager.FindAppsByServiceInstance(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("failed to find apps for service instance %s: %w", instanceID, err)
	}

	if len(appGUIDs) == 0 {
		c.logger.Debug("No apps found bound to service instance %s", instanceID)
		return fmt.Errorf("no apps bound to service instance")
	}

	c.logger.Info("Found %d apps bound to service instance %s", len(appGUIDs), instanceID)

	// Try to extract credentials from each app's VCAP_SERVICES
	for _, appGUID := range appGUIDs {
		creds, err := c.extractCredentialsFromApp(ctx, appGUID, instanceID)
		if err != nil {
			c.logger.Debug("Failed to extract credentials from app %s: %s", appGUID, err)
			continue
		}

		if len(creds) > 0 {
			// Found credentials, store them in Vault
			if err := c.storeRecoveredCredentials(instanceID, creds, appGUID); err != nil {
				c.logger.Error("Failed to store recovered credentials: %s", err)
				return err
			}
			return nil
		}
	}

	return fmt.Errorf("no credentials found in any bound app's VCAP_SERVICES")
}

// extractCredentialsFromApp extracts service credentials from an app's VCAP_SERVICES
func (c *CredentialVCAPRecovery) extractCredentialsFromApp(ctx context.Context, appGUID, instanceID string) (map[string]interface{}, error) {
	c.logger.Debug("Fetching environment for app %s", appGUID)

	// Get app environment including VCAP_SERVICES
	envData, err := c.cfManager.GetAppEnvironmentWithVCAP(ctx, appGUID)
	if err != nil {
		return nil, fmt.Errorf("failed to get app environment: %w", err)
	}

	// Extract VCAP_SERVICES
	vcapServices, ok := envData["vcap_services"].(map[string]interface{})
	if !ok {
		c.logger.Debug("No vcap_services found in app environment")
		return nil, fmt.Errorf("no VCAP_SERVICES found")
	}

	// Search through all service types in VCAP_SERVICES
	for serviceType, services := range vcapServices {
		serviceList, ok := services.([]interface{})
		if !ok {
			continue
		}

		for _, service := range serviceList {
			serviceData, ok := service.(map[string]interface{})
			if !ok {
				continue
			}

			// Check if this service instance matches our instance ID
			if serviceInstanceGUID, ok := serviceData["instance_guid"].(string); ok && serviceInstanceGUID == instanceID {
				c.logger.Info("Found matching service instance in VCAP_SERVICES (type: %s)", serviceType)

				// Extract credentials
				if creds, ok := serviceData["credentials"].(map[string]interface{}); ok {
					return c.normalizeCredentials(creds, serviceType), nil
				}
			}
		}
	}

	return nil, fmt.Errorf("service instance %s not found in VCAP_SERVICES", instanceID)
}

// normalizeCredentials normalizes credentials format based on service type
func (c *CredentialVCAPRecovery) normalizeCredentials(creds map[string]interface{}, serviceType string) map[string]interface{} {
	normalized := make(map[string]interface{})

	// Copy all original credentials
	for k, v := range creds {
		normalized[k] = v
	}

	// Add service-specific normalizations
	switch serviceType {
	case "rabbitmq":
		// Ensure standard fields exist
		if username, ok := creds["username"]; ok {
			normalized["admin_username"] = username
		}
		if password, ok := creds["password"]; ok {
			normalized["admin_password"] = password
		}

		// Extract from protocols if available
		if protocols, ok := creds["protocols"].(map[string]interface{}); ok {
			if amqp, ok := protocols["amqp"].(map[string]interface{}); ok {
				if host, ok := amqp["host"]; ok {
					normalized["host"] = host
				}
				if port, ok := amqp["port"]; ok {
					normalized["port"] = port
				}
				if username, ok := amqp["username"]; ok {
					normalized["username"] = username
				}
				if password, ok := amqp["password"]; ok {
					normalized["password"] = password
				}
				if vhost, ok := amqp["vhost"]; ok {
					normalized["vhost"] = vhost
				}
			}
		}

	case "redis":
		// Ensure password field exists
		if _, ok := normalized["password"]; !ok {
			// Try to extract from URI if available
			if uri, ok := creds["uri"].(string); ok {
				// Parse redis://:[password]@[host]:[port]
				if strings.HasPrefix(uri, "redis://") {
					parts := strings.Split(strings.TrimPrefix(uri, "redis://"), "@")
					if len(parts) == 2 && strings.HasPrefix(parts[0], ":") {
						normalized["password"] = strings.TrimPrefix(parts[0], ":")
					}
				}
			}
		}

	case "postgresql", "mysql":
		// Ensure database field exists
		if _, ok := normalized["database"]; !ok {
			if db, ok := creds["name"]; ok {
				normalized["database"] = db
			}
		}
	}

	return normalized
}

// storeRecoveredCredentials stores recovered credentials in Vault
func (c *CredentialVCAPRecovery) storeRecoveredCredentials(instanceID string, creds map[string]interface{}, sourceAppGUID string) error {
	c.logger.Info("Storing recovered credentials for instance %s", instanceID)

	// Store credentials
	credsPath := fmt.Sprintf("%s/credentials", instanceID)
	if err := c.vault.Put(credsPath, creds); err != nil {
		return fmt.Errorf("failed to store credentials: %w", err)
	}

	// Verify storage
	verifyCreds, err := c.vault.Get(credsPath)
	if err != nil || len(verifyCreds) == 0 {
		return fmt.Errorf("failed to verify credential storage")
	}

	// Add recovery metadata
	metadataPath := fmt.Sprintf("%s/metadata", instanceID)
	metadata, getErr := c.vault.Get(metadataPath)
	if getErr != nil || metadata == nil {
		c.logger.Debug("Could not get existing metadata: %v", getErr)
		metadata = make(map[string]interface{})
	}

	metadata["credentials_recovered_at"] = time.Now().Format(time.RFC3339)
	metadata["credentials_recovered_from"] = "vcap_services"
	metadata["credentials_source_app"] = sourceAppGUID
	metadata["credentials_recovery_method"] = "cf_app_environment"

	if putErr := c.vault.Put(metadataPath, metadata); putErr != nil {
		c.logger.Warning("Failed to update metadata: %s", putErr)
		// Don't fail the operation if metadata update fails
	}

	c.logger.Info("Successfully recovered and stored credentials for instance %s from VCAP_SERVICES", instanceID)
	return nil
}

// BatchRecoverCredentials attempts to recover credentials for multiple instances
func (c *CredentialVCAPRecovery) BatchRecoverCredentials(ctx context.Context, instanceIDs []string) error {
	recovered := 0
	failed := 0
	skipped := 0

	for _, instanceID := range instanceIDs {
		// Check if credentials already exist
		credsPath := fmt.Sprintf("%s/credentials", instanceID)
		creds, err := c.vault.Get(credsPath)

		if err == nil && len(creds) > 0 {
			c.logger.Debug("Instance %s already has credentials, skipping", instanceID)
			skipped++
			continue
		}

		// Attempt recovery
		if err := c.RecoverCredentialsFromVCAP(ctx, instanceID); err != nil {
			c.logger.Error("Failed to recover credentials for %s: %s", instanceID, err)
			failed++
		} else {
			recovered++
		}
	}

	c.logger.Info("VCAP credential recovery complete - Recovered: %d, Failed: %d, Skipped: %d",
		recovered, failed, skipped)

	if failed > 0 {
		return fmt.Errorf("failed to recover credentials for %d instances", failed)
	}

	return nil
}
