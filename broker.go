package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"blacksmith/bosh"
	"blacksmith/pkg/logger"
	"blacksmith/shield"
	"github.com/google/uuid"
	"github.com/pivotal-cf/brokerapi/v8/domain"
	"github.com/pivotal-cf/brokerapi/v8/domain/apiresponses"
	"gopkg.in/yaml.v2"
)

const (
	operationTypeProvision = "provision"
	operationTypeUpdate    = "update"

	// File permissions.
	defaultFilePermissions   = 0600
	defaultScriptPermissions = 0700

	// Timeouts.
	defaultDeleteTimeout = 30 * time.Second

	// Debug output limits.
	debugDataPreviewLength = 500

	// Default retry attempts.
	defaultDeleteRetryAttempts = 3
	credentialTypeDynamic      = "dynamic"
)

// Static errors for err113 compliance.
var (
	ErrPlanNotFound                          = errors.New("plan not found")
	ErrNoForgeDirectoriesFoundViaScan        = errors.New("no forge directories found via auto-scan")
	ErrNoServiceDirectoriesProvided          = errors.New("no service directories provided")
	ErrAsyncOperationsRequired               = errors.New("this service broker requires async operations")
	ErrFailedToTrackServiceRequestInVault    = errors.New("failed to track service request in Vault")
	ErrFailedToUpdateServiceStatusInVault    = errors.New("failed to update service status in Vault")
	ErrFailedToStoreServiceMetadata          = errors.New("failed to store service metadata")
	ErrCouldNotFindInstanceInVaultIndex      = errors.New("could not find instance in vault index")
	ErrCouldNotFindInstanceMetadataInVault   = errors.New("could not find instance metadata in Vault")
	ErrCouldNotParseInstanceProvisionDetails = errors.New("could not parse instance provision details from Vault")
	ErrCouldNotFindRunningVM                 = errors.New("could not find any running VM for the deployment")
	ErrCouldNotFindIPForVM                   = errors.New("could not find any IP for the VM")
	ErrFailedToScheduleShieldBackup          = errors.New("failed to schedule S.H.I.E.L.D. backup")
	ErrProvisionTaskCompletedPostHookFailed  = errors.New("provision task was successfully completed but the post-hook failed")
	ErrUnrecognizedBackendBOSHTask           = errors.New("unrecognized backend BOSH task")
	ErrInvalidStateType                      = errors.New("invalid state type")
	ErrAdminUsernameMustBeString             = errors.New("admin_username must be a string")
	ErrAdminPasswordMustBeString             = errors.New("admin_password must be a string")
	ErrVHostMustBeString                     = errors.New("vhost must be a string")
	ErrUsernameMustBeString                  = errors.New("username must be a string")
	ErrPasswordMustBeString                  = errors.New("password must be a string")
	ErrInvalidAPIURLType                     = errors.New("invalid apiUrl type")
	ErrInvalidCredsType                      = errors.New("invalid creds type")
	ErrFailedToCreateRabbitMQUser            = errors.New("failed to create RabbitMQ user")
	ErrFailedToGrantRabbitMQPermissions      = errors.New("failed to grant RabbitMQ permissions")
	ErrFailedToDeleteRabbitMQUser            = errors.New("failed to delete RabbitMQ user")
	ErrAPIURLMustBeString                    = errors.New("api_url must be a string")
	ErrNotImplemented                        = errors.New("not implemented")
	ErrGetInstanceNotImplemented             = errors.New("GetInstance not implemented")
	ErrGetBindingNotImplemented              = errors.New("GetBinding not implemented")
	ErrCredentialsNotInExpectedFormat        = errors.New("credentials not in expected format")
	ErrNoTasksFoundForDeployment             = errors.New("no tasks found for deployment")
	ErrNoDataFoundForInstance                = errors.New("no data found for instance")
	ErrMissingServiceIDOrPlanID              = errors.New("missing service_id or plan_id in vault data")
	ErrAPIURLNotFoundOrNotString             = errors.New("api_url not found or not a string")
	ErrAdminUsernameNotFoundOrNotString      = errors.New("admin_username not found or not a string")
	ErrAdminPasswordNotFoundOrNotString      = errors.New("admin_password not found or not a string")
	ErrVHostNotFoundOrNotString              = errors.New("vhost not found or not a string")
	ErrCouldNotAssertServiceIDToString       = errors.New("could not assert service id to string")
	ErrCouldNotAssertPlanIDToString          = errors.New("could not assert plan id to string")
	ErrNoHostnameAvailable                   = errors.New("no hostname available")
	ErrHostnameURLUnchanged                  = errors.New("hostname URL unchanged")
	ErrNoIPAddressesAvailable                = errors.New("no IP addresses available")
	ErrAllIPConnectionsFailed                = errors.New("all IP connections failed")
)

type Broker struct {
	Catalog []domain.Service
	Plans   map[string]Plan
	BOSH    bosh.Director
	Vault   *Vault
	Shield  shield.Client
	Config  *Config
}

type Job struct {
	Name       string   `json:"name"`
	Deployment string   `json:"deployment"`
	ID         string   `json:"id"`
	PlanID     string   `json:"plan_id"`
	PlanName   string   `json:"plan_name"`
	FQDN       string   `json:"fqdn"`
	IPs        []string `json:"ips"`
	DNS        []string `json:"dns"`
}

func WriteDataFile(
	instanceID string,
	data []byte,
) error {
	logger := logger.Get().Named("WriteDataFile")
	filename := GetWorkDir() + instanceID + ".json"
	logger.Debug("Writing data file for instance %s to %s (size: %d bytes)", instanceID, filename, len(data))

	err := os.WriteFile(filename, data, defaultFilePermissions)
	if err != nil {
		logger.Error("Failed to write data file %s: %s", filename, err)

		return fmt.Errorf("failed to write data file %s: %w", filename, err)
	} else {
		logger.Debug("Successfully wrote data file %s", filename)
	}

	return nil
}

func WriteYamlFile(
	instanceID string,
	data []byte,
) error {
	logger := logger.Get().Named("WriteYamlFile")
	logger.Debug("Writing YAML file for instance %s (input size: %d bytes)", instanceID, len(data))

	mergedMap := make(map[interface{}]interface{})

	err := yaml.Unmarshal(data, &mergedMap)
	if err != nil {
		logger.Error("Failed to unmarshal data for YAML file: %s", err)
		logger.Debug("Raw data (first 500 chars): %s", string(data[:min(len(data), debugDataPreviewLength)]))

		return fmt.Errorf("failed to unmarshal YAML data: %w", err)
	}

	yamlBytes, err := yaml.Marshal(mergedMap)
	if err != nil {
		logger.Error("Failed to marshal data to YAML: %s", err)
		logger.Debug("Map content: %+v", mergedMap)

		return fmt.Errorf("failed to marshal data to YAML: %w", err)
	}

	filename := GetWorkDir() + instanceID + ".yml"
	logger.Debug("Writing YAML to file: %s (size: %d bytes)", filename, len(yamlBytes))

	err = os.WriteFile(filename, yamlBytes, defaultFilePermissions)
	if err != nil {
		logger.Error("Failed to write YAML file %s: %s", filename, err)

		return fmt.Errorf("failed to write YAML file %s: %w", filename, err)
	} else {
		logger.Debug("Successfully wrote YAML file %s", filename)
	}

	return nil
}

func (b *Broker) FindPlan(
	serviceID string,
	planID string,
) (Plan, error) {
	logger := logger.Get().Named("FindPlan")
	key := fmt.Sprintf("%s/%s", serviceID, planID)
	logger.Debug("Looking up plan with key: %s", key)
	logger.Debug("Total plans in catalog: %d", len(b.Plans))

	if plan, ok := b.Plans[key]; ok {
		logger.Debug("Found plan - Name: %s, Type: %s, Limit: %d", plan.Name, plan.Type, plan.Limit)

		return plan, nil
	}

	logger.Error("Plan not found - serviceID: %s, planID: %s, key: %s", serviceID, planID, key)
	logger.Debug("Available plan keys in catalog:")

	for k := range b.Plans {
		logger.Debug("  - %s", k)
	}

	return Plan{}, fmt.Errorf("%w: %s", ErrPlanNotFound, key)
}

func (b *Broker) Services(ctx context.Context) ([]domain.Service, error) {
	logger := logger.Get().Named("Services")
	logger.Info("Retrieving service catalog")
	logger.Debug("Converting %d brokerapi.Service entries to domain.Service", len(b.Catalog))

	// Convert brokerapi.Service to domain.Service
	services := make([]domain.Service, len(b.Catalog))
	for index, svc := range b.Catalog {
		services[index] = domain.Service{
			ID:                   svc.ID,
			Name:                 svc.Name,
			Description:          svc.Description,
			Bindable:             svc.Bindable,
			InstancesRetrievable: svc.InstancesRetrievable,
			BindingsRetrievable:  svc.BindingsRetrievable,
			PlanUpdatable:        svc.PlanUpdatable,
			Plans:                make([]domain.ServicePlan, len(svc.Plans)),
			Tags:                 svc.Tags,
			Requires:             svc.Requires,
			Metadata:             svc.Metadata,
			DashboardClient:      svc.DashboardClient,
		}
		for j, plan := range svc.Plans {
			services[index].Plans[j] = domain.ServicePlan{
				ID:          plan.ID,
				Name:        plan.Name,
				Description: plan.Description,
				Free:        plan.Free,
				Bindable:    plan.Bindable,
				Metadata:    plan.Metadata,
			}
		}

		logger.Debug("Converted service %s with %d plans", services[index].Name, len(services[index].Plans))
	}

	logger.Info("Successfully retrieved %d services from catalog", len(services))

	return services, nil
}

func (b *Broker) ReadServices(dir ...string) error {
	logger := logger.Get().Named("Broker.ReadServices")
	logger.Info("Starting to read and build service catalog")
	logger.Debug("Service directories provided: %v", dir)

	var serviceDirs []string

	var err error

	// If no directories provided and auto-scan is enabled, scan for forge directories
	switch {
	case len(dir) == 0 && b.Config.Forges.AutoScan:
		logger.Info("No service directories provided, using auto-scan for forge directories")

		serviceDirs = AutoScanForgeDirectories(b.Config)

		if len(serviceDirs) == 0 {
			logger.Error("Auto-scan found no forge directories")

			return ErrNoForgeDirectoriesFoundViaScan
		}
	case len(dir) == 0:
		logger.Error("No service directories provided and auto-scan is disabled")

		return ErrNoServiceDirectoriesProvided
	default:
		serviceDirs = dir
	}

	logger.Debug("Calling ReadServices to read service definitions from %d directories", len(serviceDirs))

	services, err := ReadServices(serviceDirs...)
	if err != nil {
		logger.Error("Failed to read services: %s", err)
		logger.Debug("Error occurred while reading from directories: %v", serviceDirs)

		return err
	}

	logger.Info("Successfully read %d services", len(services))

	logger.Debug("Converting services to broker catalog format")

	b.Catalog = Catalog(services)
	logger.Debug("Catalog created with %d services", len(b.Catalog))

	b.Plans = make(map[string]Plan)
	totalPlans := 0

	for _, s := range services {
		logger.Debug("Processing service %s (ID: %s) with %d plans", s.Name, s.ID, len(s.Plans))

		for _, p := range s.Plans {
			planKey := p.String()
			logger.Info("Adding service/plan %s/%s to catalog (key: %s)", s.ID, p.ID, planKey)
			logger.Debug("Plan details - Name: %s, Type: %s, Limit: %d", p.Name, p.Type, p.Limit)
			b.Plans[planKey] = p
			totalPlans++
		}
	}

	logger.Info("Successfully built catalog with %d services and %d total plans", len(b.Catalog), totalPlans)

	return nil
}

func (b *Broker) Provision(
	ctx context.Context,
	instanceID string,
	details domain.ProvisionDetails,
	asyncAllowed bool,
) (
	domain.ProvisionedServiceSpec,
	error,
) {
	spec := domain.ProvisionedServiceSpec{IsAsync: true}
	logger := logger.Get().Named(fmt.Sprintf("%s %s/%s", instanceID, details.ServiceID, details.PlanID))

	logger.Info("Starting provision of service instance %s (service: %s, plan: %s)", instanceID, details.ServiceID, details.PlanID)
	logger.Debug("Provision details - OrganizationGUID: %s, SpaceGUID: %s", details.OrganizationGUID, details.SpaceGUID)

	// Check if async is allowed
	if !asyncAllowed {
		logger.Error("Async operations required but not allowed by client")

		return spec, ErrAsyncOperationsRequired
	}

	// Record the initial request
	if err := b.recordInitialRequest(ctx, instanceID, details, logger); err != nil {
		return spec, err
	}

	// Find and validate plan
	plan, err := b.validatePlan(ctx, instanceID, details, logger)
	if err != nil {
		return spec, err
	}

	// Store instance data
	_, err = b.storeInstanceData(ctx, instanceID, details, plan, logger)
	if err != nil {
		return spec, err
	}

	// Launch async provisioning
	b.launchAsyncProvisioning(ctx, instanceID, details, plan, logger)

	logger.Info("Accepted provisioning request for service instance %s", instanceID)

	return spec, nil
}

// recordInitialRequest records the initial provision request in Vault.
func (b *Broker) recordInitialRequest(ctx context.Context, instanceID string, details domain.ProvisionDetails, logger logger.Logger) error {
	logger.Debug("recording service request in vault immediately")

	now := time.Now()

	err := b.Vault.Index(ctx, instanceID, map[string]interface{}{
		"service_id":   details.ServiceID,
		"plan_id":      details.PlanID,
		"created":      now.Unix(), // Keep for backward compatibility
		"requested_at": now.Format(time.RFC3339),
		"status":       "request_received",
	})
	if err != nil {
		logger.Error("failed to record service request in vault: %s", err)

		return ErrFailedToTrackServiceRequestInVault
	}

	logger.Debug("service request recorded in vault with status 'request_received'")

	return nil
}

// validatePlan finds the plan and validates service limits.
func (b *Broker) validatePlan(ctx context.Context, instanceID string, details domain.ProvisionDetails, logger logger.Logger) (*Plan, error) {
	logger.Info("Looking up plan %s for service %s", details.PlanID, details.ServiceID)

	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		logger.Error("Failed to find plan %s/%s: %s", details.ServiceID, details.PlanID, err)

		return nil, err
	}

	logger.Debug("Found plan: %s (limit: %d)", plan.Name, plan.Limit)

	// Check service limits
	logger.Debug("retrieving vault 'db' index (for tracking service usage)")

	database, err := b.Vault.GetIndex(ctx, "db")
	if err != nil {
		logger.Error("failed to get 'db' index out of the vault: %s", err)

		return nil, err
	}

	logger.Info("Checking service limits for plan %s", plan.Name)

	if plan.OverLimit(database) {
		logger.Error("Service limit exceeded for %s/%s (limit: %d)", plan.Service.Name, plan.Name, plan.Limit)

		return nil, apiresponses.ErrPlanQuotaExceeded
	}

	logger.Debug("Service limit check passed")

	// Update status to validated
	return &plan, b.updateInstanceStatus(ctx, instanceID, details, &plan, "validated", logger)
}

// updateInstanceStatus updates the instance status in Vault.
func (b *Broker) updateInstanceStatus(ctx context.Context, instanceID string, details domain.ProvisionDetails, plan *Plan, status string, logger logger.Logger) error {
	logger.Debug("updating service instance status in vault 'db' index to '%s'", status)

	now := time.Now()

	err := b.Vault.Index(ctx, instanceID, map[string]interface{}{
		"service_id":   details.ServiceID,
		"plan_id":      plan.ID,
		"created":      now.Unix(), // Keep for backward compatibility
		"requested_at": now.Format(time.RFC3339),
		"status":       status,
	})
	if err != nil {
		logger.Error("failed to update service status in vault index: %s", err)

		return ErrFailedToUpdateServiceStatusInVault
	}

	return nil
}

// storeInstanceData stores all instance-related data in Vault.
func (b *Broker) storeInstanceData(ctx context.Context, instanceID string, details domain.ProvisionDetails, plan *Plan, logger logger.Logger) (string, error) {
	deploymentName := fmt.Sprintf("%s-%s", details.PlanID, instanceID)
	logger.Debug("deployment name: %s", deploymentName)

	// Store deployment details
	if err := b.storeDeploymentDetails(ctx, instanceID, deploymentName, details, logger); err != nil {
		return "", err
	}

	// Store deployment info and root data
	if err := b.storeDeploymentInfo(ctx, instanceID, deploymentName, details, logger); err != nil {
		return deploymentName, err // Non-fatal, continue
	}

	// Write debug files
	b.writeDebugFiles(instanceID, details, logger)

	// Store plan references
	b.storePlanReferences(ctx, instanceID, plan, logger)

	return deploymentName, nil
}

// storeDeploymentDetails stores the deployment details in Vault.
func (b *Broker) storeDeploymentDetails(ctx context.Context, instanceID, deploymentName string, details domain.ProvisionDetails, logger logger.Logger) error {
	vaultPath := fmt.Sprintf("%s/%s", instanceID, deploymentName)
	logger.Debug("storing details at Vault path: %s", vaultPath)

	err := b.Vault.Put(ctx, vaultPath, map[string]interface{}{
		"details": details,
	})
	if err != nil {
		logger.Error("failed to store details in the vault at path %s: %s", vaultPath, err)
		// Remove from index since we're failing
		if indexErr := b.Vault.Index(ctx, instanceID, nil); indexErr != nil {
			logger.Error("failed to remove instance from index: %s", indexErr)
		}

		return ErrFailedToStoreServiceMetadata
	}

	return nil
}

// storeDeploymentInfo stores deployment info at instance level and root path.
func (b *Broker) storeDeploymentInfo(ctx context.Context, instanceID, deploymentName string, details domain.ProvisionDetails, logger logger.Logger) error {
	deploymentInfo := b.buildDeploymentInfo(instanceID, deploymentName, details)

	// Store deployment info at instance level
	logger.Debug("storing deployment info at instance level: %s/deployment", instanceID)

	if err := b.Vault.Put(ctx, instanceID+"/deployment", deploymentInfo); err != nil {
		logger.Error("failed to store deployment info at instance level: %s", err)
		// Continue anyway, this is not fatal
	}

	// Store flattened data at root instance path for backward compatibility
	logger.Debug("storing flattened data at root instance path: %s", instanceID)

	rootData := b.buildRootData(deploymentInfo, details)

	if err := b.Vault.Put(ctx, instanceID, rootData); err != nil {
		logger.Error("failed to store data at root instance path: %s", err)
		// Continue anyway, this is not fatal
	}

	return nil
}

// buildDeploymentInfo builds the deployment info map.
func (b *Broker) buildDeploymentInfo(instanceID, deploymentName string, details domain.ProvisionDetails) map[string]interface{} {
	deploymentInfo := map[string]interface{}{
		"requested_at":      time.Now().Format(time.RFC3339),
		"organization_guid": details.OrganizationGUID,
		"space_guid":        details.SpaceGUID,
		"service_id":        details.ServiceID,
		"plan_id":           details.PlanID,
		"deployment_name":   deploymentName,
		"instance_id":       instanceID,
	}

	// Parse and add context if available
	b.addContextData(deploymentInfo, details.RawContext)

	return deploymentInfo
}

// buildRootData builds the root data map for backward compatibility.
func (b *Broker) buildRootData(deploymentInfo map[string]interface{}, details domain.ProvisionDetails) map[string]interface{} {
	rootData := make(map[string]interface{})
	for k, v := range deploymentInfo {
		rootData[k] = v
	}

	// Add context and parameters if present
	if len(details.RawContext) > 0 {
		var contextData map[string]interface{}
		if err := json.Unmarshal(details.RawContext, &contextData); err == nil {
			rootData["context"] = contextData
		}
	}

	if len(details.RawParameters) > 0 {
		var paramsData interface{}
		if err := json.Unmarshal(details.RawParameters, &paramsData); err == nil {
			rootData["parameters"] = paramsData
		}
	}

	return rootData
}

// addContextData adds context data to the deployment info.
func (b *Broker) addContextData(deploymentInfo map[string]interface{}, rawContext json.RawMessage) {
	if len(rawContext) == 0 {
		return
	}

	var contextData map[string]interface{}
	if err := json.Unmarshal(rawContext, &contextData); err != nil {
		return
	}

	contextFields := []string{"organization_name", "space_name", "instance_name", "platform"}
	for _, field := range contextFields {
		if value, ok := contextData[field].(string); ok {
			deploymentInfo[field] = value
		}
	}
}

// writeDebugFiles writes debug files for auditing.
func (b *Broker) writeDebugFiles(instanceID string, details domain.ProvisionDetails, logger logger.Logger) {
	if len(details.RawParameters) == 0 {
		return
	}

	if err := WriteDataFile(instanceID, details.RawParameters); err != nil {
		logger.Error("failed to write data file for debugging: %s", err)
	}

	if err := WriteYamlFile(instanceID, details.RawParameters); err != nil {
		logger.Error("failed to write YAML file for debugging: %s", err)
	}
}

// storePlanReferences stores plan file references.
func (b *Broker) storePlanReferences(ctx context.Context, instanceID string, plan *Plan, logger logger.Logger) {
	logger.Debug("storing plan file references for instance %s", instanceID)

	planStorage := NewPlanStorage(b.Vault, b.Config)
	if err := planStorage.StorePlanReferences(ctx, instanceID, *plan); err != nil {
		logger.Error("failed to store plan references: %s", err)
		// Continue anyway, this is not fatal for provisioning
	}
}

// launchAsyncProvisioning launches the async provisioning process.
func (b *Broker) launchAsyncProvisioning(ctx context.Context, instanceID string, details domain.ProvisionDetails, plan *Plan, logger logger.Logger) {
	// Update status to show provisioning is starting
	if err := b.updateInstanceStatus(ctx, instanceID, details, plan, "provisioning_started", logger); err != nil {
		logger.Error("failed to update service status to 'provisioning_started': %s", err)
		// Continue anyway, this is non-fatal
	}

	// Convert details to a map for the async function
	detailsMap := map[string]interface{}{
		"service_id":        details.ServiceID,
		"plan_id":           details.PlanID,
		"organization_guid": details.OrganizationGUID,
		"space_guid":        details.SpaceGUID,
		"raw_parameters":    details.RawParameters,
	}

	// Launch async provisioning in background
	go b.provisionAsync(ctx, instanceID, detailsMap, *plan)
}

func (b *Broker) Deprovision(
	ctx context.Context,
	instanceID string,
	details domain.DeprovisionDetails,
	asyncAllowed bool,
) (
	domain.DeprovisionServiceSpec,
	error,
) {
	logger := logger.Get().Named(fmt.Sprintf("%s %s/%s", instanceID, details.ServiceID, details.PlanID))
	logger.Info("deprovisioning plan (%s) service (%s) instance (%s)", details.PlanID, details.ServiceID, instanceID)

	// Check if async is allowed
	if !asyncAllowed {
		logger.Error("Async operations required but not allowed by client")

		return domain.DeprovisionServiceSpec{}, ErrAsyncOperationsRequired
	}

	// Immediately record deprovision request in vault
	logger.Debug("recording deprovision request in vault immediately")

	now := time.Now()

	err := b.Vault.Index(ctx, instanceID, map[string]interface{}{
		"service_id":               details.ServiceID,
		"plan_id":                  details.PlanID,
		"deprovision_requested_at": now.Format(time.RFC3339),
		"status":                   "deprovision_requested",
	})
	if err != nil {
		logger.Error("failed to record deprovision request in vault: %s", err)
		// Continue anyway, this is non-fatal for existing instances
	}

	// Check if instance exists
	instance, exists, err := b.Vault.FindInstance(ctx, instanceID)
	if err != nil {
		logger.Error("unable to retrieve instance details from vault index: %s", err)

		return domain.DeprovisionServiceSpec{}, err
	}

	if !exists {
		logger.Debug("Instance not found in vault index")
		/* return a 410 Gone to the caller */
		return domain.DeprovisionServiceSpec{}, apiresponses.ErrInstanceDoesNotExist
	}

	// Store delete_requested_at timestamp
	logger.Debug("storing delete_requested_at timestamp in Vault")

	deleteRequestedAt := time.Now()

	// Get existing metadata
	var metadata map[string]interface{}

	exists, err = b.Vault.Get(ctx, instanceID+"/metadata", &metadata)
	if err != nil || !exists {
		metadata = make(map[string]interface{})
	}

	// Add delete_requested_at
	metadata["delete_requested_at"] = deleteRequestedAt.Format(time.RFC3339)

	// Store updated metadata
	err = b.Vault.Put(ctx, instanceID+"/metadata", metadata)
	if err != nil {
		logger.Error("failed to store delete_requested_at timestamp: %s", err)
		// Continue anyway, this is non-fatal
	}

	// Deschedule SHIELD backup early (synchronous but fast)
	logger.Debug("descheduling S.H.I.E.L.D. backup")

	err = b.Shield.DeleteSchedule(instanceID, details)
	if err != nil {
		logger.Error("failed to deschedule S.H.I.E.L.D. backup for instance %s: %s", instanceID, err)
		// Continue anyway, this is non-fatal
	}

	// Launch async deprovisioning in background
	go b.deprovisionAsync(ctx, instanceID, instance)

	logger.Info("Accepted deprovisioning request for service instance %s", instanceID)

	return domain.DeprovisionServiceSpec{IsAsync: true}, nil
}

func (b *Broker) OnProvisionCompleted(
	ctx context.Context,
	logger logger.Logger,
	instanceID string,
) error {
	logger.Debug("provision task was successfully completed; scheduling backup in S.H.I.E.L.D. if required")

	// Update instance with created_at timestamp
	if err := b.updateInstanceTimestamp(ctx, instanceID, logger); err != nil {
		logger.Error("failed to update timestamps: %s", err)
		// Continue anyway, this is non-fatal
	}

	// Get instance details and deployment info
	details, deployment, err := b.getInstanceDetails(ctx, instanceID, logger)
	if err != nil {
		return err
	}

	// Get deployment VMs
	vms, err := b.getDeploymentVMs(deployment, logger)
	if err != nil {
		return err
	}

	// Get and store credentials
	if err := b.processCredentials(ctx, instanceID, details, logger); err != nil {
		return err
	}

	// Schedule backup
	if err := b.scheduleBackup(ctx, instanceID, details, vms[0].IPs[0], logger); err != nil {
		return err
	}

	logger.Debug("scheduling of S.H.I.E.L.D. backup for instance '%s' successfully completed", instanceID)

	return nil
}

// updateInstanceTimestamp updates the instance with created_at timestamp.
func (b *Broker) updateInstanceTimestamp(ctx context.Context, instanceID string, logger logger.Logger) error {
	logger.Debug("updating instance with created_at timestamp")

	createdAt := time.Now()

	// Get existing metadata to preserve history and other fields
	var metadata map[string]interface{}

	exists, err := b.Vault.Get(ctx, instanceID+"/metadata", &metadata)
	if err != nil || !exists {
		metadata = make(map[string]interface{})
	}

	// Add created_at to existing metadata
	metadata["created_at"] = createdAt.Format(time.RFC3339)

	// Store updated metadata
	if err := b.Vault.Put(ctx, instanceID+"/metadata", metadata); err != nil {
		logger.Error("failed to store created_at timestamp: %s", err)
		// Continue anyway, this is non-fatal
	}

	// Update the index with created_at
	return b.updateIndexTimestamp(ctx, instanceID, createdAt, logger)
}

// updateIndexTimestamp updates the index with created_at timestamp.
func (b *Broker) updateIndexTimestamp(ctx context.Context, instanceID string, createdAt time.Time, logger logger.Logger) error {
	logger.Debug("updating index with created_at timestamp")

	idx, err := b.Vault.GetIndex(ctx, "db")
	if err != nil {
		return err
	}

	raw, err := idx.Lookup(instanceID)
	if err != nil {
		return err
	}

	data, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}

	data["created_at"] = createdAt.Format(time.RFC3339)
	if err := b.Vault.Index(ctx, instanceID, data); err != nil {
		logger.Error("failed to index service instance: %s", err)
	}

	return nil
}

// getInstanceDetails retrieves instance details and constructs deployment name.
func (b *Broker) getInstanceDetails(ctx context.Context, instanceID string, logger logger.Logger) (domain.ProvisionDetails, string, error) {
	logger.Debug("fetching instance provision details from Vault")

	// Get instance from index to construct deployment name
	instance, exists, err := b.Vault.FindInstance(ctx, instanceID)
	if err != nil || !exists {
		logger.Error("could not find instance in vault index: %s", err)

		return domain.ProvisionDetails{}, "", ErrCouldNotFindInstanceInVaultIndex
	}

	// Construct deployment name and vault path
	deploymentName := instance.PlanID + "-" + instanceID
	vaultPath := fmt.Sprintf("%s/%s", instanceID, deploymentName)

	// Get details from vault
	details, err := b.extractDetailsFromVault(ctx, vaultPath, instanceID, logger)
	if err != nil {
		return domain.ProvisionDetails{}, "", err
	}

	return details, deploymentName, nil
}

// extractDetailsFromVault extracts provision details from vault.
func (b *Broker) extractDetailsFromVault(ctx context.Context, vaultPath, instanceID string, logger logger.Logger) (domain.ProvisionDetails, error) {
	var detailsMetadata map[string]interface{}

	exists, err := b.Vault.Get(ctx, vaultPath, &detailsMetadata)
	if err != nil {
		// Try legacy path for backward compatibility
		logger.Debug("failed to fetch from new path, trying legacy path")

		exists, err = b.Vault.Get(ctx, instanceID, &detailsMetadata)
		if err != nil {
			logger.Error("failed to fetch instance metadata from Vault: %s", err)

			return domain.ProvisionDetails{}, err
		}
	}

	if !exists {
		return domain.ProvisionDetails{}, fmt.Errorf("%w (path: %s)", ErrCouldNotFindInstanceMetadataInVault, vaultPath)
	}

	return b.parseProvisionDetails(ctx, detailsMetadata, instanceID)
}

// parseProvisionDetails parses provision details from vault metadata.
func (b *Broker) parseProvisionDetails(ctx context.Context, detailsMetadata map[string]interface{}, instanceID string) (domain.ProvisionDetails, error) {
	var details domain.ProvisionDetails

	if detailsData, ok := detailsMetadata["details"]; ok {
		// Convert the details back to ProvisionDetails struct
		if detailsMap, ok := detailsData.(map[string]interface{}); ok {
			if serviceID, ok := detailsMap["service_id"].(string); ok {
				details.ServiceID = serviceID
			}

			if planID, ok := detailsMap["plan_id"].(string); ok {
				details.PlanID = planID
			}
		}
	} else {
		// Fallback: try to read as old format
		_, err := b.Vault.Get(ctx, instanceID, &details)
		if err != nil {
			return domain.ProvisionDetails{}, ErrCouldNotParseInstanceProvisionDetails
		}
	}

	return details, nil
}

// getDeploymentVMs retrieves VMs for the deployment.
func (b *Broker) getDeploymentVMs(deployment string, logger logger.Logger) ([]bosh.VM, error) {
	logger.Debug("fetching deployment VMs metadata")

	vms, err := b.BOSH.GetDeploymentVMs(deployment)
	if err != nil {
		logger.Error("failed to fetch VMs metadata for deployment '%s': %s", deployment, err)

		return nil, fmt.Errorf("failed to get deployment VMs for %s: %w", deployment, err)
	}

	if len(vms) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrCouldNotFindRunningVM, deployment)
	}

	if len(vms[0].IPs) == 0 {
		return nil, fmt.Errorf("%w '%s' (deployment: %s)", ErrCouldNotFindIPForVM, vms[0].ID, deployment)
	}

	return vms, nil
}

// processCredentials fetches and stores credentials.
func (b *Broker) processCredentials(ctx context.Context, instanceID string, details domain.ProvisionDetails, logger logger.Logger) error {
	logger.Debug("fetching instance plan details")

	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		logger.Error("failed to find plan %s/%s: %s", details.ServiceID, details.PlanID, err)

		return err
	}

	logger.Debug("fetching instance credentials directly from BOSH")

	creds, err := GetCreds(instanceID, plan, b.BOSH, logger)
	if err != nil {
		return err
	}

	credsMap, ok := creds.(map[string]interface{})
	if !ok {
		logger.Error("credentials are not in expected map[string]interface{} format")

		return ErrCredentialsNotInExpectedFormat
	}

	return b.storeAndVerifyCredentials(ctx, instanceID, &plan, credsMap, logger)
}

// storeAndVerifyCredentials stores credentials and verifies storage.
func (b *Broker) storeAndVerifyCredentials(ctx context.Context, instanceID string, plan *Plan, creds map[string]interface{}, logger logger.Logger) error {
	// Store credentials in vault
	logger.Debug("storing credentials in vault at %s/credentials", instanceID)

	if err := b.Vault.Put(ctx, instanceID+"/credentials", creds); err != nil {
		logger.Error("CRITICAL: failed to store credentials in vault: %s", err)

		return fmt.Errorf("failed to store credentials in vault: %w", err)
	}

	// Verify credentials were actually stored
	return b.verifyCredentialStorage(ctx, instanceID, plan, logger)
}

// verifyCredentialStorage verifies that credentials were properly stored.
func (b *Broker) verifyCredentialStorage(ctx context.Context, instanceID string, plan *Plan, logger logger.Logger) error {
	logger.Debug("verifying credential storage at %s/credentials", instanceID)

	var verifyCreds map[string]interface{}

	exists, verifyErr := b.Vault.Get(ctx, instanceID+"/credentials", &verifyCreds)

	if !exists || verifyErr != nil {
		logger.Error("CRITICAL: Failed to verify credential storage - exists: %v, error: %s", exists, verifyErr)

		return b.retryCredentialStorage(ctx, instanceID, plan, logger)
	}

	logger.Info("Successfully verified credentials stored for instance %s", instanceID)

	return nil
}

// retryCredentialStorage attempts to re-fetch and store credentials.
func (b *Broker) retryCredentialStorage(ctx context.Context, instanceID string, plan *Plan, logger logger.Logger) error {
	logger.Info("Attempting to re-fetch credentials from BOSH")

	creds2, err := GetCreds(instanceID, *plan, b.BOSH, logger)
	if err != nil {
		return fmt.Errorf("failed to verify credential storage and unable to re-fetch: %w", err)
	}

	if err := b.Vault.Put(ctx, instanceID+"/credentials", creds2); err != nil {
		return fmt.Errorf("failed to store credentials after retry: %w", err)
	}

	logger.Info("Successfully stored credentials on retry")

	return nil
}

// scheduleBackup schedules Shield backup for the instance.
func (b *Broker) scheduleBackup(ctx context.Context, instanceID string, details domain.ProvisionDetails, vmIP string, logger logger.Logger) error {
	logger.Debug("scheduling S.H.I.E.L.D. backup for instance '%s'", instanceID)

	// Get credentials for backup scheduling
	creds, err := b.getCredentialsForBackup(ctx, instanceID, logger)
	if err != nil {
		return err
	}

	if err := b.Shield.CreateSchedule(instanceID, details, vmIP, creds); err != nil {
		logger.Error("failed to schedule S.H.I.E.L.D. backup: %s", err)

		return ErrFailedToScheduleShieldBackup
	}

	return nil
}

// getCredentialsForBackup retrieves credentials for backup scheduling.
func (b *Broker) getCredentialsForBackup(ctx context.Context, instanceID string, logger logger.Logger) (map[string]interface{}, error) {
	var creds map[string]interface{}

	exists, err := b.Vault.Get(ctx, instanceID+"/credentials", &creds)
	if err != nil || !exists {
		logger.Error("failed to retrieve credentials for backup scheduling: %s", err)

		return nil, fmt.Errorf("failed to retrieve credentials for backup scheduling: %w", err)
	}

	return creds, nil
}

func (b *Broker) LastOperation(
	ctx context.Context,
	instanceID string,
	details domain.PollDetails,
) (
	domain.LastOperation,
	error,
) {
	logger := logger.Get().Named(instanceID)
	logger.Debug("last-operation check received; checking state of service deployment")

	// Get instance and deployment information
	instance, deploymentName, err := b.getInstanceForOperation(ctx, instanceID, logger)
	if err != nil {
		return domain.LastOperation{}, err
	}

	// Handle case where instance was deleted
	if instance == nil {
		return domain.LastOperation{
			State:       domain.Succeeded,
			Description: "Instance has been deleted",
		}, nil
	}

	// Get the latest task for this deployment
	latestTask, operationType, err := b.getLatestTask(deploymentName, logger)
	if err != nil {
		return b.handleTaskRetrievalError(deploymentName, logger)
	}

	// Handle the operation based on task ID and type
	return b.handleOperationStatus(ctx, instanceID, deploymentName, latestTask, operationType, logger)
}

// getInstanceForOperation retrieves instance information for the operation.
func (b *Broker) getInstanceForOperation(ctx context.Context, instanceID string, logger logger.Logger) (*Instance, string, error) {
	instance, exists, err := b.Vault.FindInstance(ctx, instanceID)
	if err != nil {
		logger.Error("could not find instance details: %s", err)

		return nil, "", err
	}

	if !exists {
		logger.Error("instance %s not found in vault index", instanceID)

		return nil, "", nil // Return nil instance to indicate deletion
	}

	deploymentName := instance.PlanID + "-" + instanceID
	logger.Debug("checking deployment: %s", deploymentName)

	return instance, deploymentName, nil
}

// getLatestTask retrieves the latest task for the deployment.
func (b *Broker) getLatestTask(deploymentName string, logger logger.Logger) (*bosh.Task, string, error) {
	latestTask, operationType, err := b.GetLatestDeploymentTask(deploymentName)
	if err != nil {
		logger.Error("failed to get latest task for deployment %s: %s", deploymentName, err)

		return nil, "", err
	}

	taskID := latestTask.ID
	logger.Debug("latest task for deployment %s: task %d, type '%s', state '%s'", deploymentName, taskID, operationType, latestTask.State)

	return latestTask, operationType, nil
}

// handleTaskRetrievalError handles errors when retrieving tasks.
func (b *Broker) handleTaskRetrievalError(deploymentName string, logger logger.Logger) (domain.LastOperation, error) {
	// If we can't get task info, check if deployment exists
	_, deploymentErr := b.BOSH.GetDeployment(deploymentName)
	if deploymentErr != nil {
		// Deployment doesn't exist - this is expected during provisioning
		logger.Debug("deployment %s does not exist", deploymentName)

		return domain.LastOperation{State: domain.InProgress}, nil //nolint:nilerr // Deployment not existing during provisioning is expected
	}
	// Deployment exists but no tasks found - assume succeeded
	logger.Debug("deployment %s exists but no recent tasks found", deploymentName)

	return domain.LastOperation{State: domain.Succeeded}, nil
}

// handleOperationStatus handles the operation status based on task ID and type.
func (b *Broker) handleOperationStatus(ctx context.Context, instanceID, deploymentName string, latestTask *bosh.Task, operationType string, logger logger.Logger) (domain.LastOperation, error) {
	taskID := latestTask.ID

	switch taskID {
	case 0:
		return b.handleTaskIDZero(ctx, instanceID, deploymentName, operationType, logger)
	case -1:
		return b.handleTaskIDNegativeOne(logger)
	case 1:
		return b.handleTaskIDOne(ctx, instanceID, deploymentName, logger)
	default:
		return b.handleRegularTask(ctx, instanceID, deploymentName, taskID, operationType, logger)
	}
}

// handleTaskIDZero handles the special case where task ID is 0.
func (b *Broker) handleTaskIDZero(ctx context.Context, instanceID, deploymentName, operationType string, logger logger.Logger) (domain.LastOperation, error) {
	logger.Debug("task ID is 0, checking if deployment exists")

	// Check if deployment exists
	_, err := b.BOSH.GetDeployment(deploymentName)
	if err != nil {
		// Deployment doesn't exist - still in progress
		logger.Debug("deployment %s does not exist, operation still in progress", deploymentName)

		return domain.LastOperation{State: domain.InProgress}, nil //nolint:nilerr // Deployment not existing means operation is still in progress
	}

	// Deployment exists with task ID 0 - completed deployment from before the fix
	logger.Info("deployment %s exists with task ID 0, marking as succeeded", deploymentName)

	// Run post-provision hook if this was a provision operation
	if operationType == operationTypeProvision {
		if err := b.OnProvisionCompleted(ctx, logger, instanceID); err != nil {
			logger.Error("provision succeeded but post-hook failed: %s", err)
			// Don't fail the operation, just log the error
		}
	}

	return domain.LastOperation{State: domain.Succeeded}, nil
}

// handleTaskIDNegativeOne handles the special case where task ID is -1.
func (b *Broker) handleTaskIDNegativeOne(logger logger.Logger) (domain.LastOperation, error) {
	// Task failed during initialization
	logger.Error("operation failed during initialization")

	return domain.LastOperation{State: domain.Failed}, nil
}

// handleTaskIDOne handles the special case where task ID is 1.
func (b *Broker) handleTaskIDOne(ctx context.Context, instanceID, deploymentName string, logger logger.Logger) (domain.LastOperation, error) {
	logger.Debug("checking status of new deployment creation")

	_, err := b.BOSH.GetDeployment(deploymentName)
	if err != nil {
		// Deployment doesn't exist yet - still being created
		logger.Debug("deployment %s still being created", deploymentName)

		return domain.LastOperation{State: domain.InProgress}, nil //nolint:nilerr // Deployment not existing means it's still being created
	}

	// Deployment exists, mark as succeeded
	logger.Debug("deployment %s created successfully", deploymentName)

	// Run post-provision hook
	if err := b.OnProvisionCompleted(ctx, logger, instanceID); err != nil {
		return domain.LastOperation{}, ErrProvisionTaskCompletedPostHookFailed
	}

	return domain.LastOperation{State: domain.Succeeded}, nil
}

// handleRegularTask handles regular task IDs for provision and deprovision operations.
func (b *Broker) handleRegularTask(ctx context.Context, instanceID, deploymentName string, taskID int, operationType string, logger logger.Logger) (domain.LastOperation, error) {
	switch operationType {
	case "provision":
		return b.handleProvisionTask(ctx, instanceID, deploymentName, taskID, logger)
	case "deprovision":
		return b.handleDeprovisionTask(ctx, instanceID, deploymentName, taskID, logger)
	default:
		logger.Error("invalid state '%s' found in the vault", operationType)

		return domain.LastOperation{}, fmt.Errorf("%w: %s", ErrInvalidStateType, operationType)
	}
}

// handleProvisionTask handles provision task operations.
func (b *Broker) handleProvisionTask(ctx context.Context, instanceID, deploymentName string, taskID int, logger logger.Logger) (domain.LastOperation, error) {
	logger.Debug("retrieving task %d from BOSH director", taskID)

	task, err := b.BOSH.GetTask(taskID)
	if err != nil {
		logger.Error("failed to retrieve task %d from BOSH director: %s", taskID, err)

		return domain.LastOperation{}, ErrUnrecognizedBackendBOSHTask
	}

	switch task.State {
	case "done":
		if err := b.OnProvisionCompleted(ctx, logger, instanceID); err != nil {
			return domain.LastOperation{}, ErrProvisionTaskCompletedPostHookFailed
		}

		logger.Debug("provision operation succeeded")

		return domain.LastOperation{State: domain.Succeeded}, nil

	case "error":
		logger.Error("provision operation failed!")
		b.cleanupFailedDeployment(deploymentName, logger)

		return domain.LastOperation{State: domain.Failed}, nil

	default:
		logger.Debug("provision operation is still in progress")

		return domain.LastOperation{State: domain.InProgress}, nil
	}
}

// handleDeprovisionTask handles deprovision task operations.
func (b *Broker) handleDeprovisionTask(ctx context.Context, instanceID, deploymentName string, taskID int, logger logger.Logger) (domain.LastOperation, error) {
	logger.Debug("retrieving task %d from BOSH director", taskID)

	task, err := b.BOSH.GetTask(taskID)
	if err != nil {
		logger.Error("failed to retrieve task %d from BOSH director: %s", taskID, err)

		return domain.LastOperation{}, ErrUnrecognizedBackendBOSHTask
	}

	switch task.State {
	case "done":
		return b.handleSuccessfulDeprovision(ctx, instanceID, deploymentName, logger)
	case "error":
		return b.handleFailedDeprovision(deploymentName, logger)
	default:
		logger.Debug("deprovision operation is still in progress")

		return domain.LastOperation{State: domain.InProgress}, nil
	}
}

// cleanupFailedDeployment attempts to clean up a failed deployment.
func (b *Broker) cleanupFailedDeployment(deploymentName string, logger logger.Logger) {
	logger.Debug("checking if failed deployment exists and needs cleanup")

	_, deploymentErr := b.BOSH.GetDeployment(deploymentName)
	if deploymentErr == nil {
		logger.Info("Failed deployment %s still exists, attempting cleanup", deploymentName)
		// Attempt to delete the failed deployment (best effort)
		if cleanupTask, cleanupErr := b.BOSH.DeleteDeployment(deploymentName); cleanupErr != nil {
			logger.Error("Failed to initiate cleanup of failed deployment %s: %s", deploymentName, cleanupErr)
		} else {
			logger.Info("Initiated cleanup of failed deployment %s (task %d)", deploymentName, cleanupTask.ID)
		}
	}
}

// handleSuccessfulDeprovision handles successful deprovision operations.
func (b *Broker) handleSuccessfulDeprovision(ctx context.Context, instanceID, deploymentName string, logger logger.Logger) (domain.LastOperation, error) {
	logger.Debug("deprovision operation succeeded, verifying deployment is actually deleted")

	// Verify deployment is actually gone before declaring success
	_, deploymentErr := b.BOSH.GetDeployment(deploymentName)
	if deploymentErr == nil {
		logger.Error("Deprovision task succeeded but deployment %s still exists", deploymentName)

		return domain.LastOperation{State: domain.Failed}, nil
	}

	logger.Info("Deployment %s has been successfully deleted, removing from Blacksmith index", deploymentName)

	// Store deleted_at timestamp
	b.storeDeletedTimestamp(ctx, instanceID, logger)

	// Remove from index after confirmed deletion
	logger.Debug("removing instance from service index after confirmed deletion")

	if err := b.Vault.Index(ctx, instanceID, nil); err != nil {
		logger.Error("failed to remove service from vault index: %s", err)
		// Continue anyway, the deployment is gone
	}

	logger.Debug("keeping secrets in vault for audit purposes")
	// Note: We intentionally do NOT call b.Vault.Clear(instanceID) here
	// to preserve secrets for auditing purposes
	return domain.LastOperation{State: domain.Succeeded}, nil
}

// handleFailedDeprovision handles failed deprovision operations.
func (b *Broker) handleFailedDeprovision(deploymentName string, logger logger.Logger) (domain.LastOperation, error) {
	logger.Error("deprovision operation failed!")

	// Check if deployment still exists after failed deletion
	_, deploymentErr := b.BOSH.GetDeployment(deploymentName)
	if deploymentErr == nil {
		logger.Error("Deployment %s still exists after failed deletion task", deploymentName)
	} else {
		logger.Info("Deployment %s does not exist despite failed deletion task", deploymentName)
	}

	logger.Debug("keeping instance in index and secrets in vault due to deletion failure")
	// Do NOT remove from index if deletion failed - instance may still be recoverable
	return domain.LastOperation{State: domain.Failed}, nil
}

// storeDeletedTimestamp stores the deleted_at timestamp in Vault.
func (b *Broker) storeDeletedTimestamp(ctx context.Context, instanceID string, logger logger.Logger) {
	logger.Debug("storing deleted_at timestamp in Vault")

	deletedAt := time.Now()

	// Get existing metadata
	var metadata map[string]interface{}

	exists, err := b.Vault.Get(ctx, instanceID+"/metadata", &metadata)
	if err != nil || !exists {
		metadata = make(map[string]interface{})
	}

	// Add deleted_at
	metadata["deleted_at"] = deletedAt.Format(time.RFC3339)

	// Store updated metadata
	if err := b.Vault.Put(ctx, instanceID+"/metadata", metadata); err != nil {
		logger.Error("failed to store deleted_at timestamp: %s", err)
		// Continue anyway, this is non-fatal
	}
}

func (b *Broker) Bind(
	ctx context.Context,
	instanceID, bindingID string,
	details domain.BindDetails,
	asyncAllowed bool,
) (
	domain.Binding,
	error,
) {
	var binding domain.Binding

	logger := logger.Get().Named(fmt.Sprintf("%s %s %s @%s", instanceID, details.ServiceID, details.PlanID, bindingID))
	logger.Info("Starting bind operation for instance %s, binding %s", instanceID, bindingID)
	logger.Debug("Bind details - Service: %s, Plan: %s, AppGUID: %s", details.ServiceID, details.PlanID, details.AppGUID)

	// Find the plan
	plan, err := b.findPlanForBinding(details, logger)
	if err != nil {
		return binding, err
	}

	// Get credentials
	creds, err := b.getInstanceCredentials(instanceID, plan, logger)
	if err != nil {
		return binding, err
	}

	// Process RabbitMQ dynamic credentials if applicable
	processedCreds, err := b.processRabbitMQCredentials(ctx, bindingID, creds, logger)
	if err != nil {
		return binding, err
	}

	// Clean up admin credentials from final output
	b.cleanupAdminCredentials(processedCreds)

	binding.Credentials = processedCreds
	logger.Debug("credentials are: %v", binding)
	logger.Info("Successfully completed bind operation for binding %s", bindingID)

	return binding, nil
}

// findPlanForBinding finds the plan for the binding operation.
func (b *Broker) findPlanForBinding(details domain.BindDetails, logger logger.Logger) (*Plan, error) {
	logger.Info("Looking up plan %s for service %s", details.PlanID, details.ServiceID)
	logger.Debug("Searching in blacksmith catalog for binding plan")

	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		logger.Error("Failed to find plan %s/%s: %s", details.ServiceID, details.PlanID, err)

		return nil, err
	}

	logger.Debug("Found plan: %s", plan.Name)

	return &plan, nil
}

// getInstanceCredentials retrieves credentials for the instance.
func (b *Broker) getInstanceCredentials(instanceID string, plan *Plan, logger logger.Logger) (interface{}, error) {
	logger.Info("Retrieving credentials for instance %s", instanceID)
	logger.Debug("Calling GetCreds for plan %s", plan.Name)

	creds, err := GetCreds(instanceID, *plan, b.BOSH, logger)
	if err != nil {
		logger.Error("Failed to retrieve credentials: %s", err)

		return nil, err
	}

	logger.Debug("Successfully retrieved credentials")

	return creds, nil
}

// processRabbitMQCredentials processes RabbitMQ credentials for dynamic user creation.
func (b *Broker) processRabbitMQCredentials(ctx context.Context, bindingID string, creds interface{}, logger logger.Logger) (interface{}, error) {
	credMap, ok := creds.(map[string]interface{})
	if !ok {
		return creds, nil // Not a map, return as-is
	}

	// Check if this has RabbitMQ API URL (indicator of RabbitMQ service)
	apiUrl, hasAPIURL := credMap["api_url"]
	if !hasAPIURL {
		return creds, nil // Not RabbitMQ, return as-is
	}

	return b.createDynamicRabbitMQUser(ctx, bindingID, credMap, apiUrl, logger)
}

// createDynamicRabbitMQUser creates a dynamic RabbitMQ user for the binding.
func (b *Broker) createDynamicRabbitMQUser(ctx context.Context, bindingID string, credMap map[string]interface{}, apiUrl interface{}, logger logger.Logger) (interface{}, error) {
	// Validate required RabbitMQ credentials
	adminCreds, err := b.validateRabbitMQAdminCredentials(credMap)
	if err != nil {
		return nil, err
	}

	staticCreds, err := b.validateRabbitMQStaticCredentials(credMap)
	if err != nil {
		return nil, err
	}

	apiUrlStr, ok := apiUrl.(string)
	if !ok {
		logger.Error("Invalid apiUrl type: %T", apiUrl)

		return nil, fmt.Errorf("%w: %T", ErrInvalidAPIURLType, apiUrl)
	}

	// Create dynamic user credentials
	usernameDynamic := bindingID
	passwordDynamic := uuid.New().String()

	logger.Info("Creating dynamic RabbitMQ user for binding %s", bindingID)
	logger.Debug("Creating user %s in RabbitMQ at %s", usernameDynamic, apiUrl)

	// Create user and grant permissions
	if err := b.setupRabbitMQUser(ctx, usernameDynamic, passwordDynamic, adminCreds, apiUrlStr, credMap, logger); err != nil {
		return nil, err
	}

	// Replace static credentials with dynamic ones
	return b.replaceCredentialsWithDynamic(credMap, staticCreds, usernameDynamic, passwordDynamic)
}

// validateRabbitMQAdminCredentials validates admin credentials for RabbitMQ.
func (b *Broker) validateRabbitMQAdminCredentials(credMap map[string]interface{}) (*rabbitmqAdminCredentials, error) {
	adminUsername, ok := credMap["admin_username"].(string)
	if !ok {
		return nil, ErrAdminUsernameMustBeString
	}

	adminPassword, ok := credMap["admin_password"].(string)
	if !ok {
		return nil, ErrAdminPasswordMustBeString
	}

	vhost, ok := credMap["vhost"].(string)
	if !ok {
		return nil, ErrVHostMustBeString
	}

	return &rabbitmqAdminCredentials{
		username: adminUsername,
		password: adminPassword,
		vhost:    vhost,
	}, nil
}

// validateRabbitMQStaticCredentials validates static credentials for RabbitMQ.
func (b *Broker) validateRabbitMQStaticCredentials(credMap map[string]interface{}) (*rabbitmqStaticCredentials, error) {
	username, ok := credMap["username"].(string)
	if !ok {
		return nil, ErrUsernameMustBeString
	}

	password, ok := credMap["password"].(string)
	if !ok {
		return nil, ErrPasswordMustBeString
	}

	return &rabbitmqStaticCredentials{
		username: username,
		password: password,
	}, nil
}

// rabbitmqAdminCredentials holds RabbitMQ admin credentials.
type rabbitmqAdminCredentials struct {
	username string
	password string
	vhost    string
}

// rabbitmqStaticCredentials holds static RabbitMQ credentials.
type rabbitmqStaticCredentials struct {
	username string
	password string
}

// setupRabbitMQUser creates the RabbitMQ user and grants permissions.
func (b *Broker) setupRabbitMQUser(ctx context.Context, usernameDynamic, passwordDynamic string, adminCreds *rabbitmqAdminCredentials, apiUrlStr string, credMap map[string]interface{}, logger logger.Logger) error {
	// Create user
	if err := CreateUserPassRabbitMQ(ctx, usernameDynamic, passwordDynamic, adminCreds.username, adminCreds.password, apiUrlStr, b.Config, credMap); err != nil {
		logger.Error("Failed to create RabbitMQ user: %s", err)

		return err
	}

	logger.Debug("Successfully created RabbitMQ user %s", usernameDynamic)

	// Grant permissions
	logger.Debug("Granting permissions to user %s for vhost %s", usernameDynamic, adminCreds.vhost)

	if err := GrantUserPermissionsRabbitMQ(ctx, usernameDynamic, adminCreds.username, adminCreds.password, adminCreds.vhost, apiUrlStr, b.Config, credMap); err != nil {
		logger.Error("Failed to grant permissions to RabbitMQ user %s: %s", usernameDynamic, err)

		return err
	}

	logger.Debug("Successfully granted permissions to user %s", usernameDynamic)

	return nil
}

// replaceCredentialsWithDynamic replaces static credentials with dynamic ones.
func (b *Broker) replaceCredentialsWithDynamic(credMap map[string]interface{}, staticCreds *rabbitmqStaticCredentials, usernameDynamic, passwordDynamic string) (interface{}, error) {
	creds := interface{}(credMap)

	// Replace username in all credential structures
	var err error

	creds, err = yamlGsub(creds, staticCreds.username, usernameDynamic)
	if err != nil {
		return nil, err
	}

	// Replace password in all credential structures
	creds, err = yamlGsub(creds, staticCreds.password, passwordDynamic)
	if err != nil {
		return nil, err
	}

	// Update the credential map directly
	credMap["username"] = usernameDynamic
	credMap["password"] = passwordDynamic
	credMap["credential_type"] = credentialTypeDynamic

	return creds, nil
}

// cleanupAdminCredentials removes admin credentials from the final output.
func (b *Broker) cleanupAdminCredentials(creds interface{}) {
	if credMap, ok := creds.(map[string]interface{}); ok {
		delete(credMap, "admin_username")
		delete(credMap, "admin_password")
	}
}

func yamlGsub(
	obj interface{},
	orig string,
	replacement string,
) (
	interface{},
	error,
) {
	logger := logger.Get().Named("yamlGsub")
	logger.Debug("Performing YAML string substitution - orig: %s, replacement: %s", orig, replacement)

	marshaledData, err := yaml.Marshal(obj)
	if err != nil {
		logger.Error("Failed to marshal object to YAML: %s", err)

		return nil, fmt.Errorf("failed to marshal YAML for substitution: %w", err)
	}

	s := string(marshaledData)
	replaced := strings.ReplaceAll(s, orig, replacement)
	logger.Debug("Replaced %d occurrences", strings.Count(s, orig))

	var data map[interface{}]interface{}

	if err = yaml.Unmarshal([]byte(replaced), &data); err != nil {
		logger.Error("Failed to unmarshal replaced YAML: %s", err)

		return nil, fmt.Errorf("failed to unmarshal replaced YAML: %w", err)
	}

	logger.Debug("Successfully completed YAML substitution")

	return deinterfaceMap(data), nil
}

// createHTTPClientForService creates an HTTP client with optional TLS verification skip.
func createHTTPClientForService(serviceName string, config *Config) *http.Client {
	if config != nil && config.Services.ShouldSkipTLSVerify(serviceName) {
		tr := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 - intentionally skipping TLS verification when configured
		}

		return &http.Client{Transport: tr}
	}

	return http.DefaultClient
}

// tryServiceRequest attempts HTTP request with BOSH DNS first, then falls back to IP.
func tryServiceRequest(req *http.Request, httpClient *http.Client, creds map[string]interface{}, logger logger.Logger) (*http.Response, error) {
	originalURL := req.URL.String()
	logger.Debug("Original request URL: %s", originalURL)

	// Extract connection options from credentials
	connOptions := extractConnectionOptions(creds, logger)

	// Try BOSH DNS hostname first
	if resp, err := tryHostnameConnection(req, httpClient, connOptions.hostname, originalURL, logger); err == nil {
		return resp, nil
	}

	// Fallback to IP addresses
	if resp, err := tryIPConnections(req, httpClient, connOptions.ips, originalURL, logger); err == nil {
		return resp, nil
	}

	// Final attempt with original URL for error reporting
	return tryFinalConnection(req, httpClient, originalURL, connOptions.hostname, connOptions.ips)
}

// connectionOptions holds extracted connection options from credentials.
type connectionOptions struct {
	hostname string
	ips      []string
}

// extractConnectionOptions extracts hostname and IPs from credentials.
func extractConnectionOptions(creds map[string]interface{}, logger logger.Logger) *connectionOptions {
	options := &connectionOptions{}

	// Extract hostname
	if h, ok := creds["hostname"].(string); ok && h != "" {
		options.hostname = h
		logger.Debug("Found BOSH DNS hostname in credentials: %s", h)
	}

	// Extract IPs from jobs array
	options.ips = extractIPsFromJobs(creds)
	logger.Debug("Available IPs for fallback: %v", options.ips)

	return options
}

// extractIPsFromJobs extracts IP addresses from the jobs array in credentials.
func extractIPsFromJobs(creds map[string]interface{}) []string {
	var ips []string

	jobsData, ok := creds["jobs"]
	if !ok {
		return ips
	}

	jobsArray, ok := jobsData.([]interface{})
	if !ok || len(jobsArray) == 0 {
		return ips
	}

	firstJob, ok := jobsArray[0].(map[string]interface{})
	if !ok {
		return ips
	}

	jobIPs, ok := firstJob["ips"].([]interface{})
	if !ok {
		return ips
	}

	for _, ip := range jobIPs {
		if ipStr, ok := ip.(string); ok {
			ips = append(ips, ipStr)
		}
	}

	return ips
}

// tryHostnameConnection attempts connection using BOSH DNS hostname.
func tryHostnameConnection(req *http.Request, httpClient *http.Client, hostname, originalURL string, logger logger.Logger) (*http.Response, error) {
	if hostname == "" {
		return nil, ErrNoHostnameAvailable
	}

	hostnameURL := replaceHostInURL(originalURL, hostname)
	if hostnameURL == originalURL {
		return nil, ErrHostnameURLUnchanged
	}

	logger.Info("Attempting connection via BOSH DNS: %s", hostnameURL)
	req.URL, _ = url.Parse(hostnameURL)

	resp, err := httpClient.Do(req)
	if err == nil {
		logger.Info("Successfully connected via BOSH DNS hostname")

		return resp, nil
	}

	logger.Debug("BOSH DNS connection failed: %s", err)

	// Check if this is a TLS certificate error that might be resolved with IP
	if isTLSError(err) {
		logger.Info("TLS certificate error detected, will try IP fallback")
	}

	return nil, fmt.Errorf("BOSH DNS hostname connection failed: %w", err)
}

// tryIPConnections attempts connections using IP addresses.
func tryIPConnections(req *http.Request, httpClient *http.Client, ips []string, originalURL string, logger logger.Logger) (*http.Response, error) {
	if len(ips) == 0 {
		return nil, ErrNoIPAddressesAvailable
	}

	for i, ipAddress := range ips {
		logger.Info("Attempting connection via IP (%d/%d): %s", i+1, len(ips), ipAddress)

		if resp, err := tryIPConnection(req, httpClient, ipAddress, originalURL, logger); err == nil {
			return resp, nil
		}
	}

	return nil, ErrAllIPConnectionsFailed
}

// tryIPConnection attempts connection using a specific IP address.
func tryIPConnection(req *http.Request, httpClient *http.Client, ipAddress, originalURL string, logger logger.Logger) (*http.Response, error) {
	ipURL := replaceHostInURL(originalURL, ipAddress)
	req.URL, _ = url.Parse(ipURL)

	resp, err := httpClient.Do(req)
	if err == nil {
		logger.Info("Successfully connected via IP address: %s", ipAddress)

		return resp, nil
	}

	logger.Debug("IP connection failed for %s: %s", ipAddress, err)

	return nil, fmt.Errorf("IP connection failed for %s: %w", ipAddress, err)
}

// tryFinalConnection makes a final attempt with the original URL for error reporting.
func tryFinalConnection(req *http.Request, httpClient *http.Client, originalURL, hostname string, ips []string) (*http.Response, error) {
	req.URL, _ = url.Parse(originalURL)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to connect via BOSH DNS (%s) and all IP addresses (%v): %w",
			hostname, ips, err)
	}

	return resp, nil
}

// replaceHostInURL replaces the hostname/IP in a URL with a new host.
func replaceHostInURL(originalURL, newHost string) string {
	if parsedURL, err := url.Parse(originalURL); err == nil {
		// Preserve the port if it exists
		if _, port, err := net.SplitHostPort(parsedURL.Host); err == nil {
			parsedURL.Host = net.JoinHostPort(newHost, port)
		} else {
			parsedURL.Host = newHost
		}

		return parsedURL.String()
	}

	return originalURL
}

// isTLSError checks if the error is related to TLS certificate verification.
func isTLSError(err error) bool {
	return strings.Contains(err.Error(), "tls:") ||
		strings.Contains(err.Error(), "certificate") ||
		strings.Contains(err.Error(), "x509:")
}

func CreateUserPassRabbitMQ(ctx context.Context, usernameDynamic, passwordDynamic, adminUsername, adminPassword, apiUrl string, config *Config, creds map[string]interface{}) error {
	logger := logger.Get().Named("CreateUserPassRabbitMQ")
	logger.Info("Creating RabbitMQ user: %s", usernameDynamic)
	logger.Debug("API URL: %s, Admin user: %s", apiUrl, adminUsername)

	payload := struct {
		Password string `json:"password"`
		Tags     string `json:"tags"`
	}{Password: passwordDynamic, Tags: "management,policymaker"}

	data, err := json.Marshal(payload)
	if err != nil {
		logger.Error("Failed to marshal user creation payload: %s", err)

		return fmt.Errorf("failed to marshal user creation payload: %w", err)
	}

	logger.Debug("User creation payload: %s", string(data))

	createUrl := apiUrl + "/users/" + usernameDynamic
	logger.Debug("Creating user at URL: %s", createUrl)

	ctx, cancel := context.WithTimeout(ctx, defaultDeleteTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodPut, createUrl, bytes.NewBuffer(data))
	if err != nil {
		logger.Error("Failed to create HTTP request for user creation: %s", err)

		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	request.SetBasicAuth(adminUsername, adminPassword)
	request.Header.Set("Content-Type", "application/json")

	httpClient := createHTTPClientForService("rabbitmq", config)

	logger.Debug("Attempting user creation with BOSH DNS fallback to IP")

	resp, err := tryServiceRequest(request, httpClient, creds, logger)
	if err != nil {
		logger.Error("HTTP request failed for user creation: %s", err)

		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		logger.Error("Failed to create user - Status: %d, Response: %s", resp.StatusCode, string(body))

		return fmt.Errorf("%w, status code: %d, response: %s", ErrFailedToCreateRabbitMQUser, resp.StatusCode, string(body))
	}

	logger.Info("Successfully created RabbitMQ user: %s", usernameDynamic)

	return nil
}

func GrantUserPermissionsRabbitMQ(ctx context.Context, usernameDynamic, adminUsername, adminPassword, vhost, apiUrl string, config *Config, creds map[string]interface{}) error {
	logger := logger.Get().Named("GrantUserPermissionsRabbitMQ")
	logger.Debug("Granting permissions to RabbitMQ user: %s", usernameDynamic)
	logger.Debug("API URL: %s, Admin user: %s, vhost: %s", apiUrl, adminUsername, vhost)

	// Create permissions payload
	payload := struct {
		Configure string `json:"configure"`
		Write     string `json:"write"`
		Read      string `json:"read"`
	}{Configure: ".*", Write: ".*", Read: ".*"}

	data, err := json.Marshal(payload)
	if err != nil {
		logger.Error("Failed to marshal permissions payload: %s", err)

		return fmt.Errorf("failed to marshal permissions payload: %w", err)
	}

	logger.Debug("Permissions payload: %s", string(data))

	// URL encode vhost for API call
	encodedVhost := url.QueryEscape(vhost)
	permUrl := apiUrl + "/permissions/" + encodedVhost + "/" + usernameDynamic
	logger.Debug("Setting permissions at URL: %s", permUrl)

	ctx2, cancel2 := context.WithTimeout(ctx, defaultDeleteTimeout)
	defer cancel2()

	request, err := http.NewRequestWithContext(ctx2, http.MethodPut, permUrl, bytes.NewBuffer(data))
	if err != nil {
		logger.Error("Failed to create HTTP request for permissions: %s", err)

		return fmt.Errorf("failed to create HTTP request for permissions: %w", err)
	}

	request.SetBasicAuth(adminUsername, adminPassword)
	request.Header.Set("Content-Type", "application/json")

	httpClient := createHTTPClientForService("rabbitmq", config)

	logger.Debug("Attempting permissions grant with BOSH DNS fallback to IP")

	resp, err := tryServiceRequest(request, httpClient, creds, logger)
	if err != nil {
		logger.Error("HTTP request failed for permissions: %s", err)

		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		logger.Error("Failed to set permissions - Status: %d, Response: %s", resp.StatusCode, string(body))

		return fmt.Errorf("%w, status code: %d, response: %s", ErrFailedToGrantRabbitMQPermissions, resp.StatusCode, string(body))
	}

	logger.Debug("Successfully granted permissions to RabbitMQ user: %s", usernameDynamic)

	return nil
}

func DeletetUserRabbitMQ(ctx context.Context, bindingID, adminUsername, adminPassword, apiUrl string, config *Config, creds map[string]interface{}) error {
	logger := logger.Get().Named("DeleteUserRabbitMQ")
	logger.Info("Deleting RabbitMQ user: %s", bindingID)
	logger.Debug("API URL: %s, Admin user: %s", apiUrl, adminUsername)

	deleteUrl := apiUrl + "/users/" + bindingID
	logger.Debug("Deleting user at URL: %s", deleteUrl)

	ctx, cancel := context.WithTimeout(ctx, defaultDeleteTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, deleteUrl, nil)
	if err != nil {
		logger.Error("Failed to create HTTP DELETE request: %s", err)

		return fmt.Errorf("failed to create HTTP DELETE request: %w", err)
	}

	request.SetBasicAuth(adminUsername, adminPassword)
	request.Header.Set("Content-Type", "application/json")

	httpClient := createHTTPClientForService("rabbitmq", config)

	logger.Debug("Attempting user deletion with BOSH DNS fallback to IP")

	resp, err := tryServiceRequest(request, httpClient, creds, logger)
	if err != nil {
		logger.Error("HTTP request failed for user deletion: %s", err)

		return err
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		logger.Error("Failed to delete user - Status: %d, Response: %s", resp.StatusCode, string(body))

		return fmt.Errorf("%w, status code: %d, response: %s", ErrFailedToDeleteRabbitMQUser, resp.StatusCode, string(body))
	}

	logger.Info("Successfully deleted RabbitMQ user: %s", bindingID)

	return nil
}

// handleRabbitMQUnbind processes the unbind operation for RabbitMQ services.
func (b *Broker) handleRabbitMQUnbind(ctx context.Context, instanceID, bindingID string, details domain.UnbindDetails, logger logger.Logger) error {
	logger.Info("Processing unbind for RabbitMQ service")
	logger.Debug("RabbitMQ plan detected, will delete dynamic user")

	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		logger.Error("Failed to find plan %s/%s: %s", details.ServiceID, details.PlanID, err)

		return err
	}

	creds, err := b.getRabbitMQCredentials(ctx, instanceID, &plan, logger)
	if err != nil {
		return err
	}

	return b.deleteRabbitMQUser(ctx, bindingID, creds, logger)
}

// getRabbitMQCredentials retrieves admin credentials for RabbitMQ.
func (b *Broker) getRabbitMQCredentials(ctx context.Context, instanceID string, plan *Plan, logger logger.Logger) (map[string]interface{}, error) {
	logger.Debug("Retrieving admin credentials for RabbitMQ instance")

	creds, err := GetCreds(instanceID, *plan, b.BOSH, logger)
	if err != nil {
		logger.Error("Failed to retrieve credentials: %s", err)

		return nil, err
	}

	credMap, ok := creds.(map[string]interface{})
	if !ok {
		logger.Error("Invalid creds type: %T", creds)

		return nil, fmt.Errorf("%w: %T", ErrInvalidCredsType, creds)
	}

	return credMap, nil
}

// deleteRabbitMQUser handles the deletion of a RabbitMQ user.
func (b *Broker) deleteRabbitMQUser(ctx context.Context, bindingID string, credMap map[string]interface{}, logger logger.Logger) error {
	adminUsername, err := b.extractStringCred(credMap, "admin_username", logger)
	if err != nil {
		return err
	}

	adminPassword, err := b.extractStringCred(credMap, "admin_password", logger)
	if err != nil {
		return err
	}

	apiUrl, err := b.extractStringCred(credMap, "api_url", logger)
	if err != nil {
		return err
	}

	logger.Info("Deleting dynamic RabbitMQ user %s", bindingID)
	logger.Debug("Calling RabbitMQ API at %s to delete user", apiUrl)

	err = DeletetUserRabbitMQ(ctx, bindingID, adminUsername, adminPassword, apiUrl, b.Config, credMap)
	if err != nil {
		logger.Error("Failed to delete RabbitMQ user %s: %s", bindingID, err)

		return err
	}

	logger.Debug("Successfully deleted RabbitMQ user %s", bindingID)

	return nil
}

// extractStringCred extracts a string credential from the credentials map.
func (b *Broker) extractStringCred(credMap map[string]interface{}, key string, logger logger.Logger) (string, error) {
	value, ok := credMap[key].(string)
	if !ok {
		logger.Error("%s must be a string", key)

		switch key {
		case "admin_username":
			return "", ErrAdminUsernameMustBeString
		case "admin_password":
			return "", ErrAdminPasswordMustBeString
		case "api_url":
			return "", ErrAPIURLMustBeString
		default:
			return "", fmt.Errorf("invalid credential type for %s: %w", key, ErrInvalidCredsType)
		}
	}

	return value, nil
}

func (b *Broker) Unbind(
	ctx context.Context,
	instanceID, bindingID string,
	details domain.UnbindDetails,
	asyncAllowed bool,
) (domain.UnbindSpec, error) {
	logger := logger.Get().Named(fmt.Sprintf("%s %s %s @%s", instanceID, details.ServiceID, details.PlanID, bindingID))
	logger.Info("Starting unbind operation for instance %s, binding %s", instanceID, bindingID)
	logger.Debug("Unbind details - Service: %s, Plan: %s", details.ServiceID, details.PlanID)

	if strings.Contains(details.PlanID, "rabbitmq") {
		err := b.handleRabbitMQUnbind(ctx, instanceID, bindingID, details, logger)
		if err != nil {
			return domain.UnbindSpec{}, err
		}
	}

	logger.Info("Successfully completed unbind operation for binding %s", bindingID)

	return domain.UnbindSpec{}, nil
}

func (b *Broker) Update(
	ctx context.Context,
	instanceID string,
	details domain.UpdateDetails,
	asyncAllowed bool,
) (
	domain.UpdateServiceSpec,
	error,
) {
	logger := logger.Get().Named(fmt.Sprintf("%s %s %s", instanceID, details.ServiceID, details.PlanID))
	logger.Error("Update operation not implemented")
	logger.Debug("Update request - InstanceID: %s, ServiceID: %s, CurrentPlanID: %s, NewPlanID: %s",
		instanceID, details.ServiceID, details.PlanID, details.PreviousValues.PlanID)
	logger.Debug("Async allowed: %v, Raw parameters: %s", asyncAllowed, string(details.RawParameters))

	// FIXME: implement this!

	return domain.UpdateServiceSpec{}, ErrNotImplemented
}

func (b *Broker) GetInstance(ctx context.Context, instanceID string, details domain.FetchInstanceDetails) (domain.GetInstanceDetailsSpec, error) {
	logger := logger.Get().Named("GetInstance")
	logger.Debug("GetInstance called for instanceID: %s", instanceID)
	logger.Info("GetInstance operation not implemented")
	// Not implemented - return empty spec
	return domain.GetInstanceDetailsSpec{}, ErrGetInstanceNotImplemented
}

func (b *Broker) GetBinding(ctx context.Context, instanceID, bindingID string, details domain.FetchBindingDetails) (domain.GetBindingSpec, error) {
	logger := logger.Get().Named("GetBinding")
	logger.Debug("GetBinding called for instanceID: %s, bindingID: %s", instanceID, bindingID)
	logger.Info("GetBinding operation not implemented")
	// Not implemented - return empty spec
	return domain.GetBindingSpec{}, ErrGetBindingNotImplemented
}

// BindingCredentials represents the complete credential structure for a service binding.
type BindingCredentials struct {
	// Core credentials from GetCreds
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`

	// Service-specific fields
	URI    string `json:"uri,omitempty"`     // For databases
	APIURL string `json:"api_url,omitempty"` // For RabbitMQ
	Vhost  string `json:"vhost,omitempty"`   // For RabbitMQ

	// Additional dynamic fields
	Database string `json:"database,omitempty"`
	Scheme   string `json:"scheme,omitempty"`

	// Metadata
	CredentialType  string `json:"credential_type,omitempty"`  // "static" or "dynamic"
	ReconstructedAt string `json:"reconstructed_at,omitempty"` // RFC3339 timestamp

	// Store the raw credential map for services with custom fields
	Raw map[string]interface{} `json:"-"`
}

// GetBindingCredentials reconstructs the binding credentials for a given instance and binding
// This function is used by the reconciler to restore missing or corrupted binding data.
func (b *Broker) GetBindingCredentials(ctx context.Context, instanceID, bindingID string) (*BindingCredentials, error) {
	logger := logger.Get().Named(fmt.Sprintf("GetBindingCredentials %s %s", instanceID, bindingID))
	logger.Info("Starting credential reconstruction for instance %s, binding %s", instanceID, bindingID)

	// Phase 1.2A: Retrieve service and plan information from Vault
	logger.Debug("Retrieving service and plan information from Vault")

	serviceID, planID, err := b.getServiceAndPlanFromVault(ctx, instanceID)
	if err != nil {
		logger.Error("Failed to retrieve service/plan information: %s", err)

		return nil, fmt.Errorf("failed to get service/plan info: %w", err)
	}

	logger.Debug("Found service %s, plan %s", serviceID, planID)

	// Find the plan details
	plan, err := b.FindPlan(serviceID, planID)
	if err != nil {
		logger.Error("Failed to find plan %s/%s: %s", serviceID, planID, err)

		return nil, fmt.Errorf("failed to find plan: %w", err)
	}

	logger.Debug("Retrieved plan: %s", plan.Name)

	// Phase 1.2B: Get base credentials using existing GetCreds function
	logger.Debug("Retrieving base credentials using GetCreds")

	creds, err := GetCreds(instanceID, plan, b.BOSH, logger)
	if err != nil {
		logger.Error("Failed to retrieve base credentials: %s", err)

		return nil, fmt.Errorf("failed to get base credentials: %w", err)
	}

	logger.Debug("Successfully retrieved base credentials")

	// Phase 1.2C: Handle dynamic credential creation if needed
	binding := &BindingCredentials{
		CredentialType:  "static",
		ReconstructedAt: time.Now().Format(time.RFC3339),
	}

	// Convert credentials to map for processing
	var credsMap map[string]interface{}
	if credMap, ok := creds.(map[string]interface{}); ok {
		credsMap = credMap

		binding.Raw = make(map[string]interface{})
		for k, v := range credMap {
			binding.Raw[k] = v
		}
	} else {
		logger.Error("Credentials are not in expected map format")

		return nil, ErrCredentialsNotInExpectedFormat //nolint:nilerr // Intentional nil return with error
	}

	// Check if this service supports dynamic credentials (RabbitMQ case)
	if _, hasAPI := credsMap["api_url"]; hasAPI {
		logger.Info("Service supports dynamic credentials, processing RabbitMQ user for binding %s", bindingID)

		err = b.handleDynamicRabbitMQCredentials(ctx, credsMap, bindingID, logger)
		if err != nil {
			logger.Error("Failed to handle dynamic RabbitMQ credentials: %s", err)

			return nil, fmt.Errorf("failed to handle dynamic credentials: %w", err)
		}

		binding.CredentialType = "dynamic"

		logger.Debug("Successfully processed dynamic RabbitMQ credentials")
	}

	// Phase 1.2D: Populate the structured credential fields
	b.populateBindingCredentials(binding, credsMap)

	logger.Info("Successfully reconstructed credentials for binding %s", bindingID)

	return binding, nil
}

func (b *Broker) LastBindingOperation(ctx context.Context, instanceID, bindingID string, details domain.PollDetails) (domain.LastOperation, error) {
	logger := logger.Get().Named("LastBindingOperation")
	logger.Debug("LastBindingOperation called for instanceID: %s, bindingID: %s", instanceID, bindingID)
	logger.Debug("Returning success immediately as async bindings are not supported")
	// Not implemented - return successful immediately since we don't support async bindings
	return domain.LastOperation{State: domain.Succeeded}, nil
}

// GetLatestDeploymentTask retrieves the most recent task for a deployment from BOSH.
func (b *Broker) GetLatestDeploymentTask(deploymentName string) (*bosh.Task, string, error) {
	logger := logger.Get().Named("GetLatestDeploymentTask " + deploymentName)

	// Get events for this deployment to find task IDs
	events, err := b.BOSH.GetEvents(deploymentName)
	if err != nil {
		logger.Error("failed to get events for deployment %s: %s", deploymentName, err)

		return nil, "", fmt.Errorf("failed to get events for deployment %s: %w", deploymentName, err)
	}

	// Extract unique task IDs from events and determine the latest one
	taskIDs := make(map[int]bool)

	var maxTaskID int

	for _, event := range events {
		if event.TaskID != "" {
			// Parse task ID from string to int
			if taskID, err := strconv.Atoi(event.TaskID); err == nil {
				taskIDs[taskID] = true

				if taskID > maxTaskID {
					maxTaskID = taskID
				}
			}
		}
	}

	if maxTaskID == 0 {
		logger.Debug("no task IDs found in events for deployment %s", deploymentName)

		return nil, "", fmt.Errorf("%w: %s", ErrNoTasksFoundForDeployment, deploymentName)
	}

	logger.Debug("found %d unique task IDs in events, latest is %d", len(taskIDs), maxTaskID)

	// Get the latest task details
	latestTask, err := b.BOSH.GetTask(maxTaskID)
	if err != nil {
		logger.Error("failed to get task %d: %s", maxTaskID, err)

		return nil, "", fmt.Errorf("failed to get task %d: %w", maxTaskID, err)
	}

	// Determine operation type from task description
	desc := strings.ToLower(latestTask.Description)

	var operationType string

	switch {
	case strings.Contains(desc, "delet") || strings.Contains(desc, "deprovision"):
		operationType = "deprovision"
	case strings.Contains(desc, "creat") || strings.Contains(desc, "deploy") || strings.Contains(desc, "provision"):
		operationType = operationTypeProvision
	case strings.Contains(desc, "update"):
		operationType = operationTypeUpdate
	default:
		// Default to provision for other operations
		operationType = operationTypeProvision
	}

	logger.Debug("latest task for deployment %s: task %d (%s) - %s", deploymentName, latestTask.ID, operationType, latestTask.Description)

	return latestTask, operationType, nil
}

// getServiceAndPlanFromVault retrieves service and plan IDs from vault storage.
func (b *Broker) getServiceAndPlanFromVault(ctx context.Context, instanceID string) (string, string, error) {
	var serviceID, planID string

	// Try the deployment path first (newer format)
	deploymentPath := instanceID + "/deployment"

	var deploymentData map[string]interface{}

	exists, err := b.Vault.Get(ctx, deploymentPath, &deploymentData)
	if err == nil && exists && deploymentData != nil {
		if sid, ok := deploymentData["service_id"].(string); ok {
			serviceID = sid
		}

		if pid, ok := deploymentData["plan_id"].(string); ok {
			planID = pid
		}

		if serviceID != "" && planID != "" {
			return serviceID, planID, nil
		}
	}

	// Fallback to root instance path (backward compatibility)
	var instanceData map[string]interface{}

	exists, err = b.Vault.Get(ctx, instanceID, &instanceData)
	if err != nil {
		return "", "", fmt.Errorf("failed to get instance data from vault: %w", err)
	}

	if !exists || instanceData == nil {
		return "", "", fmt.Errorf("%w: %s", ErrNoDataFoundForInstance, instanceID)
	}

	if sid, ok := instanceData["service_id"].(string); ok {
		serviceID = sid
	}

	if pid, ok := instanceData["plan_id"].(string); ok {
		planID = pid
	}

	if serviceID == "" || planID == "" {
		return "", "", ErrMissingServiceIDOrPlanID
	}

	return serviceID, planID, nil
}

// handleDynamicRabbitMQCredentials processes dynamic RabbitMQ user creation for bindings.
func (b *Broker) handleDynamicRabbitMQCredentials(ctx context.Context, credsMap map[string]interface{}, bindingID string, logger logger.Logger) error {
	apiURL, ok := credsMap["api_url"].(string)
	if !ok {
		return ErrAPIURLNotFoundOrNotString
	}

	adminUsername, ok := credsMap["admin_username"].(string)
	if !ok {
		return ErrAdminUsernameNotFoundOrNotString
	}

	adminPassword, ok := credsMap["admin_password"].(string)
	if !ok {
		return ErrAdminPasswordNotFoundOrNotString
	}

	vhost, ok := credsMap["vhost"].(string)
	if !ok {
		return ErrVHostNotFoundOrNotString
	}

	// Dynamic credentials use bindingID as username
	usernameDynamic := bindingID
	passwordDynamic := uuid.New().String()

	usernameStatic, ok := credsMap["username"].(string)
	if !ok {
		logger.Error("username must be a string")

		return ErrUsernameMustBeString
	}

	passwordStatic, ok := credsMap["password"].(string)
	if !ok {
		logger.Error("password must be a string")

		return ErrPasswordMustBeString
	}

	logger.Debug("Creating dynamic RabbitMQ user %s", usernameDynamic)

	err := CreateUserPassRabbitMQ(ctx, usernameDynamic, passwordDynamic, adminUsername, adminPassword, apiURL, b.Config, credsMap)
	if err != nil {
		return fmt.Errorf("failed to create RabbitMQ user: %w", err)
	}

	logger.Debug("Granting permissions to user %s for vhost %s", usernameDynamic, vhost)

	err = GrantUserPermissionsRabbitMQ(ctx, usernameDynamic, adminUsername, adminPassword, vhost, apiURL, b.Config, credsMap)
	if err != nil {
		return fmt.Errorf("failed to grant permissions: %w", err)
	}

	// Update credentials with dynamic values
	updatedCreds, err := yamlGsub(credsMap, usernameStatic, usernameDynamic)
	if err != nil {
		return fmt.Errorf("failed to substitute username: %w", err)
	}

	updatedCreds, err = yamlGsub(updatedCreds, passwordStatic, passwordDynamic)
	if err != nil {
		return fmt.Errorf("failed to substitute password: %w", err)
	}

	// Update the credentials map
	if updatedMap, ok := updatedCreds.(map[string]interface{}); ok {
		for k, v := range updatedMap {
			credsMap[k] = v
		}
	}

	credsMap["username"] = usernameDynamic
	credsMap["password"] = passwordDynamic
	credsMap["credential_type"] = "dynamic"

	return nil
}

// populateBindingCredentials fills the structured fields from the raw credential map.
func (b *Broker) populateBindingCredentials(binding *BindingCredentials, credsMap map[string]interface{}) {
	// Populate standard fields
	if host, ok := credsMap["host"].(string); ok {
		binding.Host = host
	}

	if port, ok := credsMap["port"].(float64); ok {
		binding.Port = int(port)
	} else if port, ok := credsMap["port"].(int); ok {
		binding.Port = port
	}

	if username, ok := credsMap["username"].(string); ok {
		binding.Username = username
	}

	if password, ok := credsMap["password"].(string); ok {
		binding.Password = password
	}

	if uri, ok := credsMap["uri"].(string); ok {
		binding.URI = uri
	}

	if apiURL, ok := credsMap["api_url"].(string); ok {
		binding.APIURL = apiURL
	}

	if vhost, ok := credsMap["vhost"].(string); ok {
		binding.Vhost = vhost
	}

	if database, ok := credsMap["database"].(string); ok {
		binding.Database = database
	}

	if scheme, ok := credsMap["scheme"].(string); ok {
		binding.Scheme = scheme
	}

	if credType, ok := credsMap["credential_type"].(string); ok {
		binding.CredentialType = credType
	}

	// Remove admin credentials from the map (they shouldn't be in binding responses)
	delete(credsMap, "admin_username")
	delete(credsMap, "admin_password")
}

func (b *Broker) serviceWithNoDeploymentCheck(ctx context.Context) (
	[]string,
	error,
) {
	logger := logger.Get().Named("serviceWithNoDeploymentCheck")
	logger.Info("Starting check for orphaned service instances with no backing BOSH deployment")

	logger.Debug("Fetching all current deployments from BOSH director")

	deployments, err := b.BOSH.GetDeployments()
	if err != nil {
		logger.Error("Failed to get deployments from BOSH director: %s", err)

		return nil, fmt.Errorf("failed to get BOSH deployments: %w", err)
	}

	logger.Debug("Found %d deployments in BOSH director", len(deployments))

	// turn deployments from a slice of deployments into a map of
	// string to bool because all we care about is the name of the deployment (the string here)
	deploymentNames := make(map[string]bool)
	for _, deployment := range deployments {
		deploymentNames[deployment.Name] = true
		logger.Debug("Found BOSH deployment: %s", deployment.Name)
	}
	// grab the vault DB json blob out of vault
	logger.Debug("Fetching vault DB index to check for service instances")

	vaultDB, err := b.Vault.getVaultDB(ctx)
	if err != nil {
		logger.Error("Failed to get vault DB index: %s", err)

		return nil, err
	}

	logger.Debug("Found %d service instances in vault DB", len(vaultDB.Data))

	// loop through all current instances in the "db"
	// check bosh director for each instance in the "db"
	var removedDeploymentNames []string

	logger.Debug("Checking each service instance for corresponding BOSH deployment")

	for instanceID, serviceInstance := range vaultDB.Data {
		logger.Debug("Checking instance: %s", instanceID)

		if serviceData, ok := serviceInstance.(map[string]interface{}); ok {
			service, serviceOK := serviceData["service_id"].(string)
			if !serviceOK {
				logger.Error("Could not parse service_id for instance %s - value type: %T", instanceID, serviceData["service_id"])

				return nil, ErrCouldNotAssertServiceIDToString
			}

			logger.Debug("Instance %s - service_id: %s", instanceID, service)

			plan, ok := serviceData["plan_id"].(string)
			if !ok {
				logger.Error("Could not parse plan_id for instance %s - value type: %T", instanceID, serviceData["plan_id"])

				return nil, ErrCouldNotAssertPlanIDToString
			}

			logger.Debug("Instance %s - plan_id: %s", instanceID, plan)

			currentDeployment := plan + "-" + instanceID
			logger.Debug("Looking for deployment: %s", currentDeployment)

			// deployments are named as instance.PlanID + "-" + instanceID
			if _, ok := deploymentNames[currentDeployment]; !ok {
				// if the deployment name isn't listed in our director then delete it from vault
				logger.Info("Found orphaned instance %s - no BOSH deployment named: %s", instanceID, currentDeployment)
				removedDeploymentNames = append(removedDeploymentNames, currentDeployment)

				logger.Info("Removing orphaned service instance %s from vault DB", instanceID)

				err := b.Vault.Index(ctx, instanceID, nil)
				if err != nil {
					logger.Error("Failed to remove orphaned service instance '%s' from vault DB: %s", instanceID, err)
				} else {
					logger.Debug("Successfully removed orphaned instance %s from vault DB", instanceID)
				}
			} else {
				logger.Debug("Instance %s has valid BOSH deployment %s", instanceID, currentDeployment)
			}
		} else {
			logger.Error("Could not parse service instance data for %s - unexpected type: %T", instanceID, serviceInstance)
		}
	}

	logger.Info("Orphan check complete - removed %d orphaned instances", len(removedDeploymentNames))

	return removedDeploymentNames, nil
}
