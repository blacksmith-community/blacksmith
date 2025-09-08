package reconciler

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

// Static error variables to satisfy err113.
var (
	ErrVaultNotExpectedType            = errors.New("vault is not of expected type")
	ErrFailedToVerifyCredentialStorage = errors.New("failed to verify credential storage")
	ErrInvalidDeploymentNameFormat     = errors.New("invalid deployment name format")
	ErrCouldNotDeterminePlan           = errors.New("could not determine plan from deployment name")
	ErrBoshClientNoGetDeployment       = errors.New("boshClient does not implement GetDeployment method")
	ErrDeploymentHasNoManifest         = errors.New("deployment has no manifest")
	ErrUnsupportedServiceType          = errors.New("unsupported service type")
	ErrNoCredentialsFoundInManifest    = errors.New("no credentials found in manifest")
	ErrNoInstanceGroupsFoundInManifest = errors.New("no instance_groups found in manifest")
	ErrInvalidInstanceGroupsFormat     = errors.New("invalid instance_groups format")
	ErrNoJobsFoundInInstanceGroup      = errors.New("no jobs found in instance group")
	ErrRabbitmqJobNotFound             = errors.New("rabbitmq job not found in manifest")
	ErrRedisJobNotFound                = errors.New("redis job not found in manifest")
	ErrPostgresJobNotFound             = errors.New("postgres job not found in manifest")
	ErrFailedToPopulateCredentials     = errors.New("failed to populate credentials")
)

// CredentialPopulator handles fetching and storing missing credentials during reconciliation.
type CredentialPopulator struct {
	updater Updater
	logger  Logger
}

// NewCredentialPopulator creates a new credential populator.
func NewCredentialPopulator(updater Updater, logger Logger) *CredentialPopulator {
	return &CredentialPopulator{
		updater: updater,
		logger:  logger,
	}
}

// EnsureCredentials checks if credentials exist and fetches them from BOSH if missing.
func (cp *CredentialPopulator) EnsureCredentials(ctx context.Context, instanceID string, deploymentInfo DeploymentInfo, boshClient interface{}, broker interface{}) error {
	cp.logger.Debugf("Checking credentials for instance %s", instanceID)

	// Check if credentials already exist in Vault
	credsPath := instanceID + "/credentials"

	vault, ok := cp.updater.(*VaultUpdater).vault.(VaultInterface)
	if !ok {
		return ErrVaultNotExpectedType
	}

	existingCreds, err := vault.Get(credsPath)
	if err == nil && len(existingCreds) > 0 {
		cp.logger.Debugf("Instance %s already has credentials (%d fields)", instanceID, len(existingCreds))

		return nil
	}

	// Credentials missing - need to fetch from BOSH
	cp.logger.Infof("Instance %s missing credentials, fetching from BOSH deployment", instanceID)

	// Extract service type from deployment name
	serviceType := cp.extractServiceType(deploymentInfo.Name)
	if !cp.requiresCredentials(serviceType) {
		cp.logger.Debugf("Service type %s does not require credentials", serviceType)

		return nil
	}

	// Get plan information from the broker
	plan, err := cp.getPlanFromDeployment(deploymentInfo, broker)
	if err != nil {
		return fmt.Errorf("failed to determine plan for deployment %s: %w", deploymentInfo.Name, err)
	}

	cp.logger.Infof("Fetching credentials from BOSH for %s (plan: %s)", deploymentInfo.Name, plan.ID)

	// Extract credentials from BOSH manifest
	creds, err := cp.FetchCredsFromBOSH(instanceID, plan, boshClient)
	if err != nil {
		return fmt.Errorf("failed to fetch credentials from BOSH: %w", err)
	}

	// Store credentials in Vault
	cp.logger.Infof("Storing fetched credentials for instance %s", instanceID)

	err = vault.Put(credsPath, creds)
	if err != nil {
		return fmt.Errorf("failed to store credentials: %w", err)
	}

	// Verify storage
	verifyCreds, err := vault.Get(credsPath)
	if err != nil || len(verifyCreds) == 0 {
		return ErrFailedToVerifyCredentialStorage
	}

	// Add metadata about credential recovery
	metadataPath := instanceID + "/metadata"

	metadata, getErr := vault.Get(metadataPath)
	if getErr != nil || metadata == nil {
		metadata = make(map[string]interface{})
	}

	metadata["credentials_populated_at"] = time.Now().Format(time.RFC3339)
	metadata["credentials_populated_by"] = "reconciler"
	metadata["credentials_source"] = "bosh_deployment"

	_ = vault.Put(metadataPath, metadata)

	cp.logger.Infof("Successfully populated credentials for instance %s from BOSH", instanceID)

	return nil
}

// FetchCredsFromBOSH extracts credentials from the deployed BOSH manifest (exported for use by recovery tools).
func (cp *CredentialPopulator) FetchCredsFromBOSH(instanceID string, plan Plan, boshClient interface{}) (map[string]interface{}, error) {
	deploymentName := plan.ID + "-" + instanceID
	cp.logger.Infof("Fetching manifest from BOSH for deployment %s", deploymentName)

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
		return nil, ErrBoshClientNoGetDeployment
	}

	deployment, err := director.GetDeployment(deploymentName)
	if err != nil {
		return nil, fmt.Errorf("failed to get deployment from BOSH: %w", err)
	}

	if deployment.Manifest == "" {
		return nil, ErrDeploymentHasNoManifest
	}

	// Parse the YAML manifest
	var manifest map[string]interface{}

	err = yaml.Unmarshal([]byte(deployment.Manifest), &manifest)
	if err != nil {
		return nil, fmt.Errorf("failed to parse manifest YAML: %w", err)
	}

	// Extract credentials from the manifest
	// Look for instance_groups -> jobs -> properties
	creds := make(map[string]interface{})

	// Extract based on service type
	serviceType := cp.extractServiceType(deploymentName)

	switch serviceType {
	case serviceTypeRabbitMQReconciler:
		// RabbitMQ credentials are in instance_groups[0].jobs[rabbitmq].properties
		err := cp.extractRabbitMQCreds(manifest, creds)
		if err != nil {
			return nil, fmt.Errorf("failed to extract RabbitMQ credentials: %w", err)
		}
	case serviceTypeRedisReconciler:
		err := cp.extractRedisCreds(manifest, creds)
		if err != nil {
			return nil, fmt.Errorf("failed to extract Redis credentials: %w", err)
		}
	case serviceTypePostgreSQL:
		err := cp.extractPostgreSQLCreds(manifest, creds)
		if err != nil {
			return nil, fmt.Errorf("failed to extract PostgreSQL credentials: %w", err)
		}
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedServiceType, serviceType)
	}

	if len(creds) == 0 {
		return nil, ErrNoCredentialsFoundInManifest
	}

	cp.logger.Infof("Successfully extracted %d credential fields from manifest", len(creds))

	return creds, nil
}

// extractRedisCreds extracts Redis credentials from manifest.
func (cp *CredentialPopulator) extractRedisCreds(manifest map[string]interface{}, creds map[string]interface{}) error {
	// Navigate to instance_groups[0].jobs[redis].properties.redis
	instanceGroups, found := manifest["instance_groups"].([]interface{})
	if !found || len(instanceGroups) == 0 {
		return ErrNoInstanceGroupsFoundInManifest
	}

	firstGroup, isValidMap := instanceGroups[0].(map[string]interface{})
	if !isValidMap {
		return ErrInvalidInstanceGroupsFormat
	}

	jobs, ok := firstGroup["jobs"].([]interface{})
	if !ok {
		return ErrNoJobsFoundInInstanceGroup
	}

	for _, job := range jobs {
		jobMap, found := job.(map[string]interface{})
		if !found {
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
		if host, found := extractHostFromManifest(manifest); found {
			creds["host"] = host
		}

		creds["port"] = 6379

		// Build URI
		if password, ok := creds["password"].(string); ok {
			if host, ok := creds["host"].(string); ok {
				creds["uri"] = fmt.Sprintf("redis://:%s@%s", password, net.JoinHostPort(host, "6379"))
			}
		}

		return nil
	}

	return ErrRedisJobNotFound
}

// extractPostgreSQLCreds extracts PostgreSQL credentials from manifest.
func (cp *CredentialPopulator) extractPostgreSQLCreds(manifest map[string]interface{}, creds map[string]interface{}) error {
	instanceGroups, err := cp.validatePostgreSQLInstanceGroups(manifest)
	if err != nil {
		return err
	}

	jobs, err := cp.getPostgreSQLJobs(instanceGroups[0])
	if err != nil {
		return err
	}

	postgresProps, err := cp.findPostgreSQLProperties(jobs)
	if err != nil {
		return err
	}

	cp.extractPostgreSQLBasicCreds(postgresProps, creds)
	cp.addPostgreSQLHost(manifest, creds)
	cp.buildPostgreSQLURI(creds)

	return nil
}

func (cp *CredentialPopulator) validatePostgreSQLInstanceGroups(manifest map[string]interface{}) ([]interface{}, error) {
	instanceGroups, found := manifest["instance_groups"].([]interface{})
	if !found || len(instanceGroups) == 0 {
		return nil, ErrNoInstanceGroupsFoundInManifest
	}

	return instanceGroups, nil
}

func (cp *CredentialPopulator) getPostgreSQLJobs(firstGroup interface{}) ([]interface{}, error) {
	group, ok := firstGroup.(map[string]interface{})
	if !ok {
		return nil, ErrInvalidInstanceGroupsFormat
	}

	jobs, ok := group["jobs"].([]interface{})
	if !ok {
		return nil, ErrNoJobsFoundInInstanceGroup
	}

	return jobs, nil
}

func (cp *CredentialPopulator) findPostgreSQLProperties(jobs []interface{}) (map[string]interface{}, error) {
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

		return postgresProps, nil
	}

	return nil, ErrPostgresJobNotFound
}

func (cp *CredentialPopulator) extractPostgreSQLBasicCreds(postgresProps map[string]interface{}, creds map[string]interface{}) {
	if username, ok := postgresProps["username"].(string); ok {
		creds["username"] = username
	}

	if password, ok := postgresProps["password"].(string); ok {
		creds["password"] = password
	}

	if database, ok := postgresProps["database"].(string); ok {
		creds["database"] = database
	}

	creds["port"] = 5432
}

func (cp *CredentialPopulator) addPostgreSQLHost(manifest map[string]interface{}, creds map[string]interface{}) {
	if host, found := extractHostFromManifest(manifest); found {
		creds["host"] = host
	}
}

func (cp *CredentialPopulator) buildPostgreSQLURI(creds map[string]interface{}) {
	username, hasUsername := creds["username"].(string)
	password, hasPassword := creds["password"].(string)
	host, hasHost := creds["host"].(string)
	database, hasDatabase := creds["database"].(string)

	if hasUsername && hasPassword && hasHost && hasDatabase {
		creds["uri"] = fmt.Sprintf("postgres://%s:%s@%s/%s", username, password, net.JoinHostPort(host, "5432"), database)
	}
}

// BatchEnsureCredentials processes multiple instances to ensure they have credentials.
func (cp *CredentialPopulator) BatchEnsureCredentials(ctx context.Context, instances []string, deployments map[string]DeploymentInfo, boshClient interface{}, broker interface{}) error {
	populated := 0
	skipped := 0
	failed := 0

	for _, instanceID := range instances {
		deployment, ok := deployments[instanceID]
		if !ok {
			cp.logger.Debugf("No deployment found for instance %s, skipping credential check", instanceID)

			skipped++

			continue
		}

		err := cp.EnsureCredentials(ctx, instanceID, deployment, boshClient, broker)
		if err != nil {
			cp.logger.Errorf("Failed to ensure credentials for %s: %s", instanceID, err)

			failed++
		} else {
			populated++
		}
	}

	cp.logger.Infof("Credential population complete - Populated: %d, Failed: %d, Skipped: %d",
		populated, failed, skipped)

	if failed > 0 {
		return fmt.Errorf("%w for %d instances", ErrFailedToPopulateCredentials, failed)
	}

	return nil
}

// extractServiceType extracts the service type from deployment name.
func (cp *CredentialPopulator) extractServiceType(deploymentName string) string {
	// Deployment names are typically: {service-type}-{instance-id}
	// e.g., "rabbitmq-standalone-abc123" or "redis-cluster-def456"
	parts := strings.Split(deploymentName, "-")
	if len(parts) > 0 {
		// Handle cases like "rabbitmq-standalone-{uuid}"
		if parts[0] == serviceTypeRabbitMQReconciler || parts[0] == serviceTypeRedisReconciler || parts[0] == serviceTypePostgreSQL || parts[0] == serviceTypeMySQL {
			return parts[0]
		}
		// Handle compound names
		if len(parts) > 1 && parts[1] == "standalone" || parts[1] == "cluster" {
			return parts[0]
		}
	}

	return ""
}

// requiresCredentials checks if a service type needs credentials.
func (cp *CredentialPopulator) requiresCredentials(serviceType string) bool {
	credentialServices := map[string]bool{
		serviceTypeRabbitMQReconciler: true,
		serviceTypeRedisReconciler:    true,
		serviceTypePostgreSQL:         true,
		serviceTypeMySQL:              true,
		"vault":                       true,
	}

	return credentialServices[serviceType]
}

// getPlanFromDeployment determines the plan from deployment information.
func (cp *CredentialPopulator) getPlanFromDeployment(deployment DeploymentInfo, broker interface{}) (Plan, error) {
	// Extract plan ID from deployment name
	// Deployment names follow pattern: {plan-id}-{instance-id}
	parts := strings.Split(deployment.Name, "-")
	if len(parts) < MinCredentialParts {
		return Plan{}, fmt.Errorf("%w: %s", ErrInvalidDeploymentNameFormat, deployment.Name)
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
							cp.logger.Debugf("Found plan %s for deployment %s", plan.ID, deployment.Name)

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

	return Plan{}, fmt.Errorf("%w: %s", ErrCouldNotDeterminePlan, deployment.Name)
}

// looksLikeUUID checks if a string looks like a UUID or instance ID.
func (cp *CredentialPopulator) looksLikeUUID(input string) bool {
	// Simple heuristic: UUIDs are 32+ hex characters with optional dashes
	// Instance IDs in Blacksmith are typically 8+ character hex strings
	if len(input) < MinPasswordLength {
		return false
	}

	// Remove dashes and check if it's all hex
	cleaned := strings.ReplaceAll(input, "-", "")
	if len(cleaned) < MinPasswordLength {
		return false
	}

	for _, c := range cleaned {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}

	return true
}

// extractRabbitMQCreds extracts RabbitMQ credentials from manifest.
func (cp *CredentialPopulator) extractRabbitMQCreds(manifest map[string]interface{}, creds map[string]interface{}) error {
	rabbitmqProps, err := cp.findRabbitMQProperties(manifest)
	if err != nil {
		return err
	}

	cp.extractRabbitMQBasicCreds(rabbitmqProps, creds)
	cp.extractRabbitMQHost(manifest, creds)
	cp.addRabbitMQDefaults(creds)
	cp.buildRabbitMQProtocols(creds)
	cp.buildRabbitMQURI(creds)

	return nil
}

// findRabbitMQProperties finds the RabbitMQ properties in the manifest.
func (cp *CredentialPopulator) findRabbitMQProperties(manifest map[string]interface{}) (map[string]interface{}, error) {
	instanceGroups, found := manifest["instance_groups"].([]interface{})
	if !found || len(instanceGroups) == 0 {
		return nil, ErrNoInstanceGroupsFoundInManifest
	}

	firstGroup, groupFound := instanceGroups[0].(map[string]interface{})
	if !groupFound {
		return nil, ErrInvalidInstanceGroupsFormat
	}

	jobs, ok := firstGroup["jobs"].([]interface{})
	if !ok {
		return nil, ErrNoJobsFoundInInstanceGroup
	}

	for _, job := range jobs {
		jobMap, ok := job.(map[string]interface{})
		if !ok {
			continue
		}

		if jobMap["name"] != "rabbitmq" {
			continue
		}

		properties, propertiesFound := jobMap["properties"].(map[string]interface{})
		if !propertiesFound {
			continue
		}

		rabbitmqProps, ok := properties["rabbitmq"].(map[string]interface{})
		if !ok {
			continue
		}

		return rabbitmqProps, nil
	}

	return nil, ErrRabbitmqJobNotFound
}

// extractRabbitMQBasicCreds extracts basic credentials from RabbitMQ properties.
func (cp *CredentialPopulator) extractRabbitMQBasicCreds(rabbitmqProps, creds map[string]interface{}) {
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
}

// extractRabbitMQHost extracts the host from the manifest's instance groups.
func (cp *CredentialPopulator) extractRabbitMQHost(manifest map[string]interface{}, creds map[string]interface{}) {
	vms, ok := manifest["instance_groups"].([]interface{})
	if !ok || len(vms) == 0 {
		return
	}

	firstVM, ok := vms[0].(map[string]interface{})
	if !ok {
		return
	}

	networks, ok := firstVM["networks"].([]interface{})
	if !ok || len(networks) == 0 {
		return
	}

	network, ok := networks[0].(map[string]interface{})
	if !ok {
		return
	}

	if staticIPs, ok := network["static_ips"].([]interface{}); ok && len(staticIPs) > 0 {
		creds["host"] = staticIPs[0]
	}
}

// addRabbitMQDefaults adds default ports for RabbitMQ.
func (cp *CredentialPopulator) addRabbitMQDefaults(creds map[string]interface{}) {
	creds["port"] = 5672
	creds["mgmt_port"] = 15672
}

// buildRabbitMQProtocols builds the protocols map for RabbitMQ.
func (cp *CredentialPopulator) buildRabbitMQProtocols(creds map[string]interface{}) {
	creds["protocols"] = map[string]interface{}{
		"amqp": map[string]interface{}{
			"host": creds["host"],
			"port": defaultRabbitMQPort,
		},
		"management": map[string]interface{}{
			"host": creds["host"],
			"port": defaultRabbitMQMgmtPort,
		},
	}
}

// buildRabbitMQURI builds the AMQP URI for RabbitMQ.
func (cp *CredentialPopulator) buildRabbitMQURI(creds map[string]interface{}) {
	username, hasUsername := creds["username"].(string)
	password, hasPassword := creds["password"].(string)
	host, hasHost := creds["host"].(string)
	vhost, hasVhost := creds["vhost"].(string)

	if hasUsername && hasPassword && hasHost && hasVhost {
		creds["uri"] = fmt.Sprintf("amqp://%s:%s@%s/%s", username, password, net.JoinHostPort(host, "5672"), vhost)
	}
}

// extractHostFromManifest extracts the host from the first VM's static IPs.
func extractHostFromManifest(manifest map[string]interface{}) (interface{}, bool) {
	vms, ok := manifest["instance_groups"].([]interface{})
	if !ok || len(vms) == 0 {
		return nil, false
	}

	firstVM, ok := vms[0].(map[string]interface{})
	if !ok {
		return nil, false
	}

	networks, ok := firstVM["networks"].([]interface{})
	if !ok || len(networks) == 0 {
		return nil, false
	}

	network, ok := networks[0].(map[string]interface{})
	if !ok {
		return nil, false
	}

	staticIPs, ok := network["static_ips"].([]interface{})
	if !ok || len(staticIPs) == 0 {
		return nil, false
	}

	return staticIPs[0], true
}
