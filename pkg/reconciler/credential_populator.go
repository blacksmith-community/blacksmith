package reconciler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

// CredentialPopulator handles fetching and storing missing credentials during reconciliation
type CredentialPopulator struct {
	updater Updater
	logger  Logger
}

// NewCredentialPopulator creates a new credential populator
func NewCredentialPopulator(updater Updater, logger Logger) *CredentialPopulator {
	return &CredentialPopulator{
		updater: updater,
		logger:  logger,
	}
}

// EnsureCredentials checks if credentials exist and fetches them from BOSH if missing
func (cp *CredentialPopulator) EnsureCredentials(ctx context.Context, instanceID string, deploymentInfo DeploymentInfo, boshClient interface{}, broker interface{}) error {
	cp.logger.Debug("Checking credentials for instance %s", instanceID)

	// Check if credentials already exist in Vault
	credsPath := fmt.Sprintf("%s/credentials", instanceID)
	vault, ok := cp.updater.(*vaultUpdater).vault.(VaultInterface)
	if !ok {
		return fmt.Errorf("vault is not of expected type")
	}

	var existingCreds map[string]interface{}
	exists, err := vault.Get(credsPath, &existingCreds)

	if exists && err == nil && len(existingCreds) > 0 {
		cp.logger.Debug("Instance %s already has credentials (%d fields)", instanceID, len(existingCreds))
		return nil
	}

	// Credentials missing - need to fetch from BOSH
	cp.logger.Info("Instance %s missing credentials, fetching from BOSH deployment", instanceID)

	// Extract service type from deployment name
	serviceType := cp.extractServiceType(deploymentInfo.Name)
	if !cp.requiresCredentials(serviceType) {
		cp.logger.Debug("Service type %s does not require credentials", serviceType)
		return nil
	}

	// Get plan information from the broker
	plan, err := cp.getPlanFromDeployment(deploymentInfo, broker)
	if err != nil {
		return fmt.Errorf("failed to determine plan for deployment %s: %w", deploymentInfo.Name, err)
	}

	cp.logger.Info("Fetching credentials from BOSH for %s (plan: %s)", deploymentInfo.Name, plan.ID)

	// Extract credentials from BOSH manifest
	creds, err := cp.FetchCredsFromBOSH(instanceID, plan, boshClient)
	if err != nil {
		return fmt.Errorf("failed to fetch credentials from BOSH: %w", err)
	}

	// Store credentials in Vault
	cp.logger.Info("Storing fetched credentials for instance %s", instanceID)
	if err := vault.Put(credsPath, creds); err != nil {
		return fmt.Errorf("failed to store credentials: %w", err)
	}

	// Verify storage
	var verifyCreds map[string]interface{}
	exists, err = vault.Get(credsPath, &verifyCreds)
	if !exists || err != nil {
		return fmt.Errorf("failed to verify credential storage")
	}

	// Add metadata about credential recovery
	metadataPath := fmt.Sprintf("%s/metadata", instanceID)
	var metadata map[string]interface{}
	vault.Get(metadataPath, &metadata)
	if metadata == nil {
		metadata = make(map[string]interface{})
	}
	metadata["credentials_populated_at"] = time.Now().Format(time.RFC3339)
	metadata["credentials_populated_by"] = "reconciler"
	metadata["credentials_source"] = "bosh_deployment"
	vault.Put(metadataPath, metadata)

	cp.logger.Info("Successfully populated credentials for instance %s from BOSH", instanceID)
	return nil
}

// extractServiceType extracts the service type from deployment name
func (cp *CredentialPopulator) extractServiceType(deploymentName string) string {
	// Deployment names are typically: {service-type}-{instance-id}
	// e.g., "rabbitmq-standalone-abc123" or "redis-cluster-def456"
	parts := strings.Split(deploymentName, "-")
	if len(parts) > 0 {
		// Handle cases like "rabbitmq-standalone-{uuid}"
		if parts[0] == "rabbitmq" || parts[0] == "redis" || parts[0] == "postgresql" || parts[0] == "mysql" {
			return parts[0]
		}
		// Handle compound names
		if len(parts) > 1 && parts[1] == "standalone" || parts[1] == "cluster" {
			return parts[0]
		}
	}
	return ""
}

// requiresCredentials checks if a service type needs credentials
func (cp *CredentialPopulator) requiresCredentials(serviceType string) bool {
	credentialServices := map[string]bool{
		"rabbitmq":   true,
		"redis":      true,
		"postgresql": true,
		"mysql":      true,
		"vault":      true,
	}
	return credentialServices[serviceType]
}

// getPlanFromDeployment determines the plan from deployment information
func (cp *CredentialPopulator) getPlanFromDeployment(deployment DeploymentInfo, broker interface{}) (Plan, error) {
	// Extract plan ID from deployment name
	// Deployment names follow pattern: {plan-id}-{instance-id}
	parts := strings.SplitN(deployment.Name, "-", -1)
	if len(parts) < 2 {
		return Plan{}, fmt.Errorf("invalid deployment name format: %s", deployment.Name)
	}

	// The instance ID is typically the last UUID part
	// The plan ID is everything before that
	for i := len(parts) - 1; i >= 0; i-- {
		if cp.looksLikeUUID(parts[i]) {
			// The instance ID would be strings.Join(parts[i:], "-")
			planID := strings.Join(parts[:i], "-")

			// Now we need to find this plan in the broker
			if b, ok := broker.(BrokerInterface); ok {
				services := b.GetServices()
				for _, svc := range services {
					for _, plan := range svc.Plans {
						if plan.ID == planID {
							cp.logger.Debug("Found plan %s for deployment %s", plan.ID, deployment.Name)
							return plan, nil
						}
					}
				}
			}

			// Fallback: create a minimal plan object
			return Plan{
				ID:   planID,
				Name: planID,
			}, nil
		}
	}

	return Plan{}, fmt.Errorf("could not determine plan from deployment name: %s", deployment.Name)
}

// looksLikeUUID checks if a string looks like a UUID or instance ID
func (cp *CredentialPopulator) looksLikeUUID(s string) bool {
	// Simple heuristic: UUIDs are 32+ hex characters with optional dashes
	// Instance IDs in Blacksmith are typically 8+ character hex strings
	if len(s) < 8 {
		return false
	}

	// Remove dashes and check if it's all hex
	cleaned := strings.ReplaceAll(s, "-", "")
	if len(cleaned) < 8 {
		return false
	}

	for _, c := range cleaned {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}

	return true
}

// FetchCredsFromBOSH extracts credentials from the deployed BOSH manifest (exported for use by recovery tools)
func (cp *CredentialPopulator) FetchCredsFromBOSH(instanceID string, plan Plan, boshClient interface{}) (map[string]interface{}, error) {
	deploymentName := plan.ID + "-" + instanceID
	cp.logger.Info("Fetching manifest from BOSH for deployment %s", deploymentName)

	// Import the bosh package types
	// The boshClient should be a bosh.Director
	type DeploymentDetail struct {
		Name     string `json:"name"`
		Manifest string `json:"manifest"`
	}

	type BOSHDirector interface {
		GetDeployment(name string) (*DeploymentDetail, error)
	}

	director, ok := boshClient.(BOSHDirector)
	if !ok {
		return nil, fmt.Errorf("boshClient does not implement GetDeployment method")
	}

	deployment, err := director.GetDeployment(deploymentName)
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment from BOSH: %w", err)
	}

	if deployment.Manifest == "" {
		return nil, fmt.Errorf("deployment has no manifest")
	}

	// Parse the YAML manifest
	var manifest map[string]interface{}
	if err := yaml.Unmarshal([]byte(deployment.Manifest), &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest YAML: %w", err)
	}

	// Extract credentials from the manifest
	// Look for instance_groups -> jobs -> properties
	creds := make(map[string]interface{})

	// Extract based on service type
	serviceType := cp.extractServiceType(deploymentName)

	switch serviceType {
	case "rabbitmq":
		// RabbitMQ credentials are in instance_groups[0].jobs[rabbitmq].properties
		if err := cp.extractRabbitMQCreds(manifest, creds); err != nil {
			return nil, fmt.Errorf("failed to extract RabbitMQ credentials: %w", err)
		}
	case "redis":
		if err := cp.extractRedisCreds(manifest, creds); err != nil {
			return nil, fmt.Errorf("failed to extract Redis credentials: %w", err)
		}
	case "postgresql":
		if err := cp.extractPostgreSQLCreds(manifest, creds); err != nil {
			return nil, fmt.Errorf("failed to extract PostgreSQL credentials: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported service type: %s", serviceType)
	}

	if len(creds) == 0 {
		return nil, fmt.Errorf("no credentials found in manifest")
	}

	cp.logger.Info("Successfully extracted %d credential fields from manifest", len(creds))
	return creds, nil
}

// extractRabbitMQCreds extracts RabbitMQ credentials from manifest
func (cp *CredentialPopulator) extractRabbitMQCreds(manifest map[string]interface{}, creds map[string]interface{}) error {
	// Navigate to instance_groups[0].jobs[rabbitmq].properties.rabbitmq
	instanceGroups, ok := manifest["instance_groups"].([]interface{})
	if !ok || len(instanceGroups) == 0 {
		return fmt.Errorf("no instance_groups found in manifest")
	}

	firstGroup, ok := instanceGroups[0].(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid instance_groups format")
	}

	jobs, ok := firstGroup["jobs"].([]interface{})
	if !ok {
		return fmt.Errorf("no jobs found in instance group")
	}

	for _, job := range jobs {
		jobMap, ok := job.(map[string]interface{})
		if !ok {
			continue
		}

		if jobMap["name"] != "rabbitmq" {
			continue
		}

		properties, ok := jobMap["properties"].(map[string]interface{})
		if !ok {
			continue
		}

		rabbitmqProps, ok := properties["rabbitmq"].(map[string]interface{})
		if !ok {
			continue
		}

		// Extract admin credentials
		if adminUser, ok := rabbitmqProps["admin_user"].(string); ok {
			creds["admin_username"] = adminUser
			creds["username"] = adminUser // for backward compatibility
		}
		if adminPass, ok := rabbitmqProps["admin_pass"].(string); ok {
			creds["admin_password"] = adminPass
			creds["password"] = adminPass // for backward compatibility
		}

		// Extract app credentials
		if appUser, ok := rabbitmqProps["app_user"].(string); ok {
			creds["app_username"] = appUser
		}
		if appPass, ok := rabbitmqProps["app_pass"].(string); ok {
			creds["app_password"] = appPass
		}

		// Extract vhost
		if vhost, ok := rabbitmqProps["vhost"].(string); ok {
			creds["vhost"] = vhost
		}

		// Extract erlang cookie
		if cookie, ok := rabbitmqProps["erlang_cookie"].(string); ok {
			creds["cookie"] = cookie
		}

		// Extract host/port from first VM
		vms, ok := manifest["instance_groups"].([]interface{})
		if ok && len(vms) > 0 {
			firstVM, ok := vms[0].(map[string]interface{})
			if ok {
				networks, ok := firstVM["networks"].([]interface{})
				if ok && len(networks) > 0 {
					network, ok := networks[0].(map[string]interface{})
					if ok {
						if staticIPs, ok := network["static_ips"].([]interface{}); ok && len(staticIPs) > 0 {
							creds["host"] = staticIPs[0]
						}
					}
				}
			}
		}

		// Default RabbitMQ ports
		creds["port"] = 5672
		creds["mgmt_port"] = 15672

		// Build protocols
		creds["protocols"] = map[string]interface{}{
			"amqp": map[string]interface{}{
				"host": creds["host"],
				"port": 5672,
			},
			"management": map[string]interface{}{
				"host": creds["host"],
				"port": 15672,
			},
		}

		// Build URI
		if username, ok := creds["username"].(string); ok {
			if password, ok := creds["password"].(string); ok {
				if host, ok := creds["host"].(string); ok {
					if vhost, ok := creds["vhost"].(string); ok {
						creds["uri"] = fmt.Sprintf("amqp://%s:%s@%s:5672/%s", username, password, host, vhost)
					}
				}
			}
		}

		return nil
	}

	return fmt.Errorf("rabbitmq job not found in manifest")
}

// extractRedisCreds extracts Redis credentials from manifest
func (cp *CredentialPopulator) extractRedisCreds(manifest map[string]interface{}, creds map[string]interface{}) error {
	// Navigate to instance_groups[0].jobs[redis].properties.redis
	instanceGroups, ok := manifest["instance_groups"].([]interface{})
	if !ok || len(instanceGroups) == 0 {
		return fmt.Errorf("no instance_groups found in manifest")
	}

	firstGroup, ok := instanceGroups[0].(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid instance_groups format")
	}

	jobs, ok := firstGroup["jobs"].([]interface{})
	if !ok {
		return fmt.Errorf("no jobs found in instance group")
	}

	for _, job := range jobs {
		jobMap, ok := job.(map[string]interface{})
		if !ok {
			continue
		}

		if jobMap["name"] != "redis" {
			continue
		}

		properties, ok := jobMap["properties"].(map[string]interface{})
		if !ok {
			continue
		}

		redisProps, ok := properties["redis"].(map[string]interface{})
		if !ok {
			continue
		}

		// Extract password
		if password, ok := redisProps["password"].(string); ok {
			creds["password"] = password
		}

		// Extract host from first VM
		vms, ok := manifest["instance_groups"].([]interface{})
		if ok && len(vms) > 0 {
			firstVM, ok := vms[0].(map[string]interface{})
			if ok {
				networks, ok := firstVM["networks"].([]interface{})
				if ok && len(networks) > 0 {
					network, ok := networks[0].(map[string]interface{})
					if ok {
						if staticIPs, ok := network["static_ips"].([]interface{}); ok && len(staticIPs) > 0 {
							creds["host"] = staticIPs[0]
						}
					}
				}
			}
		}

		creds["port"] = 6379

		// Build URI
		if password, ok := creds["password"].(string); ok {
			if host, ok := creds["host"].(string); ok {
				creds["uri"] = fmt.Sprintf("redis://:%s@%s:6379", password, host)
			}
		}

		return nil
	}

	return fmt.Errorf("redis job not found in manifest")
}

// extractPostgreSQLCreds extracts PostgreSQL credentials from manifest
func (cp *CredentialPopulator) extractPostgreSQLCreds(manifest map[string]interface{}, creds map[string]interface{}) error {
	// Navigate to instance_groups[0].jobs[postgres].properties.postgres
	instanceGroups, ok := manifest["instance_groups"].([]interface{})
	if !ok || len(instanceGroups) == 0 {
		return fmt.Errorf("no instance_groups found in manifest")
	}

	firstGroup, ok := instanceGroups[0].(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid instance_groups format")
	}

	jobs, ok := firstGroup["jobs"].([]interface{})
	if !ok {
		return fmt.Errorf("no jobs found in instance group")
	}

	for _, job := range jobs {
		jobMap, ok := job.(map[string]interface{})
		if !ok {
			continue
		}

		if jobMap["name"] != "postgres" {
			continue
		}

		properties, ok := jobMap["properties"].(map[string]interface{})
		if !ok {
			continue
		}

		postgresProps, ok := properties["postgres"].(map[string]interface{})
		if !ok {
			continue
		}

		// Extract credentials
		if username, ok := postgresProps["username"].(string); ok {
			creds["username"] = username
		}
		if password, ok := postgresProps["password"].(string); ok {
			creds["password"] = password
		}
		if database, ok := postgresProps["database"].(string); ok {
			creds["database"] = database
		}

		// Extract host from first VM
		vms, ok := manifest["instance_groups"].([]interface{})
		if ok && len(vms) > 0 {
			firstVM, ok := vms[0].(map[string]interface{})
			if ok {
				networks, ok := firstVM["networks"].([]interface{})
				if ok && len(networks) > 0 {
					network, ok := networks[0].(map[string]interface{})
					if ok {
						if staticIPs, ok := network["static_ips"].([]interface{}); ok && len(staticIPs) > 0 {
							creds["host"] = staticIPs[0]
						}
					}
				}
			}
		}

		creds["port"] = 5432

		// Build URI
		if username, ok := creds["username"].(string); ok {
			if password, ok := creds["password"].(string); ok {
				if host, ok := creds["host"].(string); ok {
					if database, ok := creds["database"].(string); ok {
						creds["uri"] = fmt.Sprintf("postgres://%s:%s@%s:5432/%s", username, password, host, database)
					}
				}
			}
		}

		return nil
	}

	return fmt.Errorf("postgres job not found in manifest")
}

// BatchEnsureCredentials processes multiple instances to ensure they have credentials
func (cp *CredentialPopulator) BatchEnsureCredentials(ctx context.Context, instances []string, deployments map[string]DeploymentInfo, boshClient interface{}, broker interface{}) error {
	populated := 0
	skipped := 0
	failed := 0

	for _, instanceID := range instances {
		deployment, ok := deployments[instanceID]
		if !ok {
			cp.logger.Debug("No deployment found for instance %s, skipping credential check", instanceID)
			skipped++
			continue
		}

		err := cp.EnsureCredentials(ctx, instanceID, deployment, boshClient, broker)
		if err != nil {
			cp.logger.Error("Failed to ensure credentials for %s: %s", instanceID, err)
			failed++
		} else {
			populated++
		}
	}

	cp.logger.Info("Credential population complete - Populated: %d, Failed: %d, Skipped: %d",
		populated, failed, skipped)

	if failed > 0 {
		return fmt.Errorf("failed to populate credentials for %d instances", failed)
	}

	return nil
}
