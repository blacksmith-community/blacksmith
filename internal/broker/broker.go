package broker

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
	"sync"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/config"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/manifest"
	"blacksmith/internal/planstore"
	"blacksmith/internal/services"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/services/common"
	"blacksmith/pkg/utils"
	vaultPkg "blacksmith/pkg/vault"
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

	// Debug output limits.
	debugDataPreviewLength = 500

	// Default retry attempts.
	defaultDeleteRetryAttempts = 3
	credentialTypeDynamic      = "dynamic"

	// Task states.
	taskStateDone  = "done"
	taskStateError = "error"
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
	ErrInvalidServiceInstanceDataType        = errors.New("invalid service instance data type")
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

// Configurable timeouts (can be overridden in tests)
var (
	defaultDeleteTimeout = 30 * time.Second
	defaultRetryBaseDelay = 50 * time.Millisecond
)

// SetDefaultDeleteTimeout allows tests to override the default timeout
func SetDefaultDeleteTimeout(timeout time.Duration) {
	defaultDeleteTimeout = timeout
}

// SetDefaultRetryBaseDelay allows tests to override the retry base delay
func SetDefaultRetryBaseDelay(delay time.Duration) {
	defaultRetryBaseDelay = delay
}

type Broker struct {
	Catalog []domain.Service
	Plans   map[string]services.Plan
	BOSH    bosh.Director
	Vault   *internalVault.Vault
	Shield  shield.Client
	Config  *config.Config

	// VM monitoring integration
	vmMonitor interfaces.VMMonitor

	// Concurrency control for bind/unbind operations
	instanceMu    sync.RWMutex           // Protects InstanceLocks map
	InstanceLocks map[string]*sync.Mutex // Per-instance mutexes (exported for initialization)
}

// IsBroker implements the interfaces.Broker interface.
func (b *Broker) IsBroker() bool {
	return true
}

// GetPlans returns a shallow copy of the broker's service plans keyed by service/plan ID.
func (b *Broker) GetPlans() map[string]services.Plan {
	if b == nil || len(b.Plans) == 0 {
		return map[string]services.Plan{}
	}

	plansCopy := make(map[string]services.Plan, len(b.Plans))
	for key, plan := range b.Plans {
		plansCopy[key] = plan
	}

	return plansCopy
}

// GetVault returns the vault instance for certificate operations.
func (b *Broker) GetVault() interfaces.Vault {
	return b.Vault
}

// SetVMMonitor sets the VM monitor for the broker.
// This is called after broker initialization to enable vm-monitor integration.
func (b *Broker) SetVMMonitor(vmMonitor interfaces.VMMonitor) {
	b.vmMonitor = vmMonitor
}

func WriteDataFile(
	instanceID string,
	data []byte,
) error {
	logger := logger.Get().Named("broker")
	filename := manifest.GetWorkDir() + instanceID + ".json"
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
	logger := logger.Get().Named("broker")
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

	filename := manifest.GetWorkDir() + instanceID + ".yml"
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
) (services.Plan, error) {
	logger := logger.Get().Named("broker")
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

	return services.Plan{}, fmt.Errorf("%w: %s", ErrPlanNotFound, key)
}

func (b *Broker) Services(ctx context.Context) ([]domain.Service, error) {
	logger := logger.Get().Named("broker")
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
	logger := logger.Get().Named("broker")
	logger.Info("Starting to read and build service catalog")
	logger.Debug("Service directories provided: %v", dir)

	serviceDirs, err := b.determineServiceDirs(dir, logger)
	if err != nil {
		return err
	}

	serviceList, err := b.readServiceList(serviceDirs, logger)
	if err != nil {
		return err
	}

	b.buildCatalogAndPlans(serviceList, logger)

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
	logger := logger.Get().Named("broker")

	logger.Info("Starting provision of service instance %s (service: %s, plan: %s)", instanceID, details.ServiceID, details.PlanID)
	logger.Debug("Provision details - OrganizationGUID: %s, SpaceGUID: %s", details.OrganizationGUID, details.SpaceGUID)

	// Check if async is allowed
	if !asyncAllowed {
		logger.Error("Async operations required but not allowed by client")

		return spec, ErrAsyncOperationsRequired
	}

	// Record the initial request
	err := b.recordInitialRequest(ctx, instanceID, details, logger)
	if err != nil {
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

func (b *Broker) Deprovision(
	ctx context.Context,
	instanceID string,
	details domain.DeprovisionDetails,
	asyncAllowed bool,
) (
	domain.DeprovisionServiceSpec,
	error,
) {
	logger := logger.Get().Named("broker")
	logger.Info("deprovisioning plan (%s) service (%s) instance (%s)", details.PlanID, details.ServiceID, instanceID)

	if !asyncAllowed {
		logger.Error("Async operations required but not allowed by client")

		return domain.DeprovisionServiceSpec{}, ErrAsyncOperationsRequired
	}

	err := b.recordDeprovisionRequest(ctx, instanceID, details, logger)
	if err != nil {
		logger.Error("failed to record deprovision request in vault: %s", err)
		// Continue anyway, this is non-fatal for existing instances
	}

	instance, err := b.validateInstanceExists(ctx, instanceID, logger)
	if err != nil {
		return domain.DeprovisionServiceSpec{}, err
	}

	b.storeDeleteRequestedTimestamp(ctx, instanceID, logger)
	b.descheduleShieldBackup(instanceID, details, logger)

	// Launch async deprovisioning in background
	// Create a detached context that survives the HTTP request lifecycle
	// This ensures deprovisioning can continue even if the request context is cancelled
	asyncCtx := context.WithoutCancel(ctx)
	go b.deprovisionAsync(asyncCtx, instanceID, instance)

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
	err := b.updateInstanceTimestamp(ctx, instanceID, logger)
	if err != nil {
		logger.Error("failed to update timestamps: %s", err)
		// Continue anyway, this is non-fatal
	}

	// Get instance details and deployment info
	details, deployment, err := b.getInstanceDetails(ctx, instanceID, logger)
	if err != nil {
		return err
	}

	// Notify vm-monitor to start monitoring this service
	// Extract planID from deployment name (format: planID-instanceID)
	if b.vmMonitor != nil {
		planID := strings.TrimSuffix(deployment, "-"+instanceID)
		if addErr := b.vmMonitor.AddService(ctx, instanceID, planID); addErr != nil {
			logger.Error("failed to add service %s to vm-monitor: %s", instanceID, addErr)
			// Continue anyway, this is non-fatal
		} else {
			logger.Debug("added service %s to vm-monitor for monitoring", instanceID)
		}
	}

	// Get deployment VMs
	vms, err := b.getDeploymentVMs(deployment, logger)
	if err != nil {
		return err
	}

	// Get and store credentials
	err = b.processCredentials(ctx, instanceID, details, logger)
	if err != nil {
		return err
	}

	// Schedule backup
	err = b.scheduleBackup(ctx, instanceID, details, vms[0].IPs[0], logger)
	if err != nil {
		return err
	}

	logger.Debug("scheduling of S.H.I.E.L.D. backup for instance '%s' successfully completed", instanceID)

	return nil
}

func (b *Broker) LastOperation(
	ctx context.Context,
	instanceID string,
	details domain.PollDetails,
) (
	domain.LastOperation,
	error,
) {
	logger := logger.Get().Named("broker")
	logger.Debug("LastOperation called for instance %s", instanceID)

	// Get instance and deployment information
	instance, deploymentName, err := b.getInstanceForOperation(ctx, instanceID, logger)
	if err != nil {
		logger.Error("Error getting instance for operation: %s", err)

		return domain.LastOperation{}, err
	}

	// Handle case where instance was deleted
	if instance == nil {
		logger.Info("Instance %s not found in index, returning succeeded", instanceID)

		return domain.LastOperation{
			State:       domain.Succeeded,
			Description: "Instance has been deleted",
		}, nil
	}

	logger.Debug("Instance %s found, deployment: %s", instanceID, deploymentName)

	// Get the latest task for this deployment
	latestTask, operationType, err := b.getLatestTask(deploymentName, logger)
	if err != nil {
		// Only log as error if it's not the expected "no tasks found" case
		// (that case is already logged at DEBUG level in getLatestTask)
		if !errors.Is(err, ErrNoTasksFoundForDeployment) {
			logger.Error("Error getting latest task for deployment %s: %s", deploymentName, err)
		}

		return b.handleTaskRetrievalError(deploymentName, logger)
	}

	logger.Debug("Latest task for %s: ID=%d, type=%s, state=%s", deploymentName, latestTask.ID, operationType, latestTask.State)

	// Handle the operation based on task ID and type
	return b.handleOperationStatus(ctx, instanceID, deploymentName, latestTask, operationType, logger)
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
	// Acquire lock for this instance to prevent concurrent bind/unbind
	mu := b.GetInstanceLock(instanceID)

	mu.Lock()
	defer mu.Unlock()

	var binding domain.Binding

	logger := logger.Get().Named("broker")
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

func (b *Broker) Unbind(
	ctx context.Context,
	instanceID, bindingID string,
	details domain.UnbindDetails,
	asyncAllowed bool,
) (domain.UnbindSpec, error) {
	// Acquire lock for this instance to prevent concurrent bind/unbind
	mu := b.GetInstanceLock(instanceID)

	mu.Lock()
	defer mu.Unlock()

	logger := logger.Get().Named("broker")
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
	logger := logger.Get().Named("broker")
	logger.Error("Update operation not implemented")
	logger.Debug("Update request - InstanceID: %s, ServiceID: %s, CurrentPlanID: %s, NewPlanID: %s",
		instanceID, details.ServiceID, details.PlanID, details.PreviousValues.PlanID)
	logger.Debug("Async allowed: %v, Raw parameters: %s", asyncAllowed, string(details.RawParameters))

	// FIXME: implement this!

	return domain.UpdateServiceSpec{}, ErrNotImplemented
}

func (b *Broker) GetInstance(ctx context.Context, instanceID string, details domain.FetchInstanceDetails) (domain.GetInstanceDetailsSpec, error) {
	logger := logger.Get().Named("broker")
	logger.Debug("GetInstance called for instanceID: %s", instanceID)
	logger.Info("GetInstance operation not implemented")
	// Not implemented - return empty spec
	return domain.GetInstanceDetailsSpec{}, ErrGetInstanceNotImplemented
}

func (b *Broker) GetBinding(ctx context.Context, instanceID, bindingID string, details domain.FetchBindingDetails) (domain.GetBindingSpec, error) {
	logger := logger.Get().Named("broker")
	logger.Debug("GetBinding called for instanceID: %s, bindingID: %s", instanceID, bindingID)
	logger.Info("GetBinding operation not implemented")
	// Not implemented - return empty spec
	return domain.GetBindingSpec{}, ErrGetBindingNotImplemented
}

func (b *Broker) GetBindingCredentials(ctx context.Context, instanceID, bindingID string) (*BindingCredentials, error) {
	logger := logger.Get().Named("broker")
	logger.Info("Starting credential reconstruction for instance %s, binding %s", instanceID, bindingID)

	// Check if credentials already exist in vault
	credPath := fmt.Sprintf("%s/bindings/%s/credentials", instanceID, bindingID)

	var existingCreds map[string]interface{}

	exists, err := b.Vault.Get(ctx, credPath, &existingCreds)
	if err != nil {
		logger.Debug("Error checking for existing credentials: %s", err)
	} else if exists {
		logger.Info("Found existing credentials for binding %s, returning from vault", bindingID)

		binding := &BindingCredentials{Raw: existingCreds}
		b.populateBindingCredentials(binding, existingCreds)

		return binding, nil
	}

	logger.Debug("No existing credentials found, creating new credentials")

	plan, err := b.retrievePlanFromVault(ctx, instanceID, logger)
	if err != nil {
		return nil, err
	}

	creds, err := b.getBaseCredentials(instanceID, plan, logger)
	if err != nil {
		return nil, err
	}

	binding, credsMap, err := b.initializeBinding(creds, logger)
	if err != nil {
		return nil, err
	}

	err = b.processDynamicCredentials(ctx, credsMap, bindingID, binding, logger)
	if err != nil {
		return nil, err
	}

	b.populateBindingCredentials(binding, credsMap)

	err = b.storeBindingCredentials(ctx, instanceID, bindingID, binding.Raw, logger)
	if err != nil {
		return nil, err
	}

	logger.Info("Successfully reconstructed credentials for binding %s", bindingID)

	return binding, nil
}

func (b *Broker) LastBindingOperation(ctx context.Context, instanceID, bindingID string, details domain.PollDetails) (domain.LastOperation, error) {
	logger := logger.Get().Named("broker")
	logger.Debug("LastBindingOperation called for instanceID: %s, bindingID: %s", instanceID, bindingID)
	logger.Debug("Returning success immediately as async bindings are not supported")
	// Not implemented - return successful immediately since we don't support async bindings
	return domain.LastOperation{State: domain.Succeeded}, nil
}

func (b *Broker) GetLatestDeploymentTask(deploymentName string) (*bosh.Task, string, error) {
	logger := logger.Get().Named("broker")

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
			taskID, err := strconv.Atoi(event.TaskID)
			if err == nil {
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

func (b *Broker) ServiceWithNoDeploymentCheck(ctx context.Context) (
	[]string,
	error,
) {
	logger := logger.Get().Named("broker")
	logger.Info("Starting check for orphaned service instances with no backing BOSH deployment")

	deploymentNames, err := b.getDeploymentNamesFromBOSH(logger)
	if err != nil {
		return nil, err
	}

	vaultDB, err := b.getVaultDBIndex(ctx, logger)
	if err != nil {
		return nil, err
	}

	removedDeploymentNames := b.processOrphanedInstances(ctx, vaultDB, deploymentNames, logger)

	logger.Info("Orphan check complete - removed %d orphaned instances", len(removedDeploymentNames))

	return removedDeploymentNames, nil
}

// GetInstanceLock returns a mutex for the given instance ID, creating one if it doesn't exist.
func (b *Broker) GetInstanceLock(instanceID string) *sync.Mutex {
	b.instanceMu.RLock()
	mutexForInstance, exists := b.InstanceLocks[instanceID]
	b.instanceMu.RUnlock()

	if exists {
		return mutexForInstance
	}

	// Need to create a new mutex
	b.instanceMu.Lock()
	defer b.instanceMu.Unlock()

	// Double-check after acquiring write lock
	if mutexForInstance, exists = b.InstanceLocks[instanceID]; exists {
		return mutexForInstance
	}

	mutexForInstance = &sync.Mutex{}
	b.InstanceLocks[instanceID] = mutexForInstance

	return mutexForInstance
}

func (b *Broker) determineServiceDirs(dir []string, logger logger.Logger) ([]string, error) {
	switch {
	case len(dir) == 0 && b.Config.Forges.AutoScan:
		logger.Info("No service directories provided, using auto-scan for forge directories")

		serviceDirs := services.AutoScanForgeDirectories(b.Config)

		if len(serviceDirs) == 0 {
			logger.Error("Auto-scan found no forge directories")

			return nil, ErrNoForgeDirectoriesFoundViaScan
		}

		return serviceDirs, nil
	case len(dir) == 0:
		logger.Error("No service directories provided and auto-scan is disabled")

		return nil, ErrNoServiceDirectoriesProvided
	default:
		return dir, nil
	}
}

func (b *Broker) readServiceList(serviceDirs []string, logger logger.Logger) ([]services.Service, error) {
	logger.Debug("Calling ReadServices to read service definitions from %d directories", len(serviceDirs))

	serviceList, err := services.ReadServices(serviceDirs...)
	if err != nil {
		logger.Error("Failed to read services: %s", err)
		logger.Debug("Error occurred while reading from directories: %v", serviceDirs)

		return nil, fmt.Errorf("failed to read services: %w", err)
	}

	logger.Info("Successfully read %d services", len(serviceList))

	return serviceList, nil
}

func (b *Broker) buildCatalogAndPlans(serviceList []services.Service, logger logger.Logger) {
	logger.Debug("Converting services to broker catalog format")

	b.Catalog = services.Catalog(serviceList)
	logger.Debug("Catalog created with %d services", len(b.Catalog))

	b.Plans = make(map[string]services.Plan)
	totalPlans := 0

	for _, s := range serviceList {
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
}

func (b *Broker) recordDeprovisionRequest(ctx context.Context, instanceID string, details domain.DeprovisionDetails, logger logger.Logger) error {
	logger.Debug("recording deprovision request in vault immediately")

	now := time.Now()

	baseData := map[string]interface{}{
		"service_id":               details.ServiceID,
		"plan_id":                  details.PlanID,
		"deprovision_requested_at": now.Format(time.RFC3339),
		"status":                   "deprovision_requested",
	}

	entry := b.buildIndexEntry(ctx, instanceID, baseData, nil, logger)

	err := b.Vault.Index(ctx, instanceID, entry)
	if err != nil {
		return fmt.Errorf("failed to record deprovision request in vault: %w", err)
	}

	return nil
}

func (b *Broker) buildIndexEntry(ctx context.Context, instanceID string, base map[string]interface{}, rawContext json.RawMessage, logger logger.Logger) map[string]interface{} {
	existing := b.getExistingIndexEntry(ctx, instanceID, logger)

	for key, value := range base {
		existing[key] = value
	}

	for key, value := range b.extractContextFields(rawContext, logger) {
		if value != "" {
			existing[key] = value
		}
	}

	return existing
}

func (b *Broker) getExistingIndexEntry(ctx context.Context, instanceID string, logger logger.Logger) map[string]interface{} {
	entry := make(map[string]interface{})

	idx, err := b.Vault.GetIndex(ctx, "db")
	if err != nil {
		logger.Debug("unable to load existing index entry for %s: %v", instanceID, err)

		return entry
	}

	raw, err := idx.Lookup(instanceID)
	if err != nil {
		return entry
	}

	switch typed := raw.(type) {
	case map[string]interface{}:
		for key, value := range typed {
			entry[key] = value
		}
	case map[interface{}]interface{}:
		for key, value := range typed {
			keyStr, ok := key.(string)
			if !ok {
				continue
			}

			entry[keyStr] = value
		}
	case vaultPkg.Instance:
		if typed.ServiceID != "" {
			entry["service_id"] = typed.ServiceID
		}

		if typed.PlanID != "" {
			entry["plan_id"] = typed.PlanID
		}
	default:
		if converted, ok := typed.(map[string]string); ok {
			for key, value := range converted {
				entry[key] = value
			}
		}
	}

	return entry
}

func (b *Broker) extractContextFields(rawContext json.RawMessage, logger logger.Logger) map[string]string {
	if len(rawContext) == 0 {
		return map[string]string{}
	}

	var parsed map[string]interface{}

	err := json.Unmarshal(rawContext, &parsed)
	if err != nil {
		logger.Debug("failed to parse context for index enrichment: %v", err)

		return map[string]string{}
	}

	keys := []string{"organization_name", "space_name", "instance_name", "platform"}
	fields := make(map[string]string, len(keys))

	for _, key := range keys {
		if value, ok := parsed[key].(string); ok && strings.TrimSpace(value) != "" {
			fields[key] = value
		}
	}

	return fields
}

func (b *Broker) validateInstanceExists(ctx context.Context, instanceID string, logger logger.Logger) (*vaultPkg.Instance, error) {
	instance, exists, err := b.Vault.FindInstance(ctx, instanceID)
	if err != nil {
		logger.Error("unable to retrieve instance details from vault index: %s", err)

		return nil, fmt.Errorf("failed to find instance in vault: %w", err)
	}

	if !exists {
		logger.Debug("Instance not found in vault index")

		return nil, apiresponses.ErrInstanceDoesNotExist
	}

	return instance, nil
}

func (b *Broker) storeDeleteRequestedTimestamp(ctx context.Context, instanceID string, logger logger.Logger) {
	logger.Debug("storing delete_requested_at timestamp in Vault")

	deleteRequestedAt := time.Now()

	// Get existing metadata
	var metadata map[string]interface{}

	exists, err := b.Vault.Get(ctx, instanceID+"/metadata", &metadata)
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
}

func (b *Broker) descheduleShieldBackup(instanceID string, details domain.DeprovisionDetails, logger logger.Logger) {
	logger.Debug("descheduling S.H.I.E.L.D. backup")

	err := b.Shield.DeleteSchedule(instanceID, details)
	if err != nil {
		logger.Error("failed to deschedule S.H.I.E.L.D. backup for instance %s: %s", instanceID, err)
		// Continue anyway, this is non-fatal
	}
}

func (b *Broker) retrievePlanFromVault(ctx context.Context, instanceID string, logger logger.Logger) (services.Plan, error) {
	logger.Debug("Retrieving service and plan information from Vault")

	serviceID, planID, err := b.getServiceAndPlanFromVault(ctx, instanceID)
	if err != nil {
		logger.Error("Failed to retrieve service/plan information: %s", err)

		return services.Plan{}, fmt.Errorf("failed to get service/plan info: %w", err)
	}

	logger.Debug("Found service %s, plan %s", serviceID, planID)

	plan, err := b.FindPlan(serviceID, planID)
	if err != nil {
		logger.Error("Failed to find plan %s/%s: %s", serviceID, planID, err)

		return services.Plan{}, fmt.Errorf("failed to find plan: %w", err)
	}

	logger.Debug("Retrieved plan: %s", plan.Name)

	return plan, nil
}

func (b *Broker) storeBindingCredentials(ctx context.Context, instanceID, bindingID string, credentials map[string]interface{}, logger logger.Logger) error {
	path := fmt.Sprintf("%s/bindings/%s/credentials", instanceID, bindingID)
	logger.Debug("Storing binding credentials in Vault", "path", path)

	err := b.Vault.Put(ctx, path, credentials)
	if err != nil {
		logger.Error("Failed to store binding credentials for %s/%s: %s", instanceID, bindingID, err)

		return fmt.Errorf("failed to store binding credentials: %w", err)
	}

	return nil
}

func (b *Broker) getBaseCredentials(instanceID string, plan services.Plan, logger logger.Logger) (interface{}, error) {
	logger.Debug("Retrieving base credentials using GetCreds")

	creds, err := manifest.GetCreds(instanceID, plan, b.BOSH, logger)
	if err != nil {
		logger.Error("Failed to retrieve base credentials: %s", err)

		return nil, fmt.Errorf("failed to get base credentials: %w", err)
	}

	logger.Debug("Successfully retrieved base credentials")

	return creds, nil
}

func (b *Broker) initializeBinding(creds interface{}, logger logger.Logger) (*BindingCredentials, map[string]interface{}, error) {
	binding := &BindingCredentials{
		CredentialType:  "static",
		ReconstructedAt: time.Now().Format(time.RFC3339),
	}

	credMap, ok := creds.(map[string]interface{})
	if !ok {
		logger.Error("Credentials are not in expected map format")

		return nil, nil, ErrCredentialsNotInExpectedFormat
	}

	binding.Raw = make(map[string]interface{})
	for k, v := range credMap {
		binding.Raw[k] = v
	}

	return binding, credMap, nil
}

func (b *Broker) processDynamicCredentials(ctx context.Context, credsMap map[string]interface{}, bindingID string, binding *BindingCredentials, logger logger.Logger) error {
	if _, hasAPI := credsMap["api_url"]; hasAPI {
		logger.Info("Service supports dynamic credentials, processing RabbitMQ user for binding", "bindingID", bindingID)

		err := b.handleDynamicRabbitMQCredentials(ctx, credsMap, bindingID, logger)
		if err != nil {
			logger.Error("Failed to handle dynamic RabbitMQ credentials", "error", err)

			return fmt.Errorf("failed to handle dynamic credentials: %w", err)
		}

		binding.CredentialType = "dynamic"

		logger.Debug("Successfully processed dynamic RabbitMQ credentials")
	}

	return nil
}

func (b *Broker) getDeploymentNamesFromBOSH(logger logger.Logger) (map[string]bool, error) {
	logger.Debug("Fetching all current deployments from BOSH director")

	deployments, err := b.BOSH.GetDeployments()
	if err != nil {
		logger.Error("Failed to get deployments from BOSH director: %s", err)

		return nil, fmt.Errorf("failed to get BOSH deployments: %w", err)
	}

	logger.Debug("Found %d deployments in BOSH director", len(deployments))

	deploymentNames := make(map[string]bool)
	for _, deployment := range deployments {
		deploymentNames[deployment.Name] = true
		logger.Debug("Found BOSH deployment: %s", deployment.Name)
	}

	return deploymentNames, nil
}

func (b *Broker) getVaultDBIndex(ctx context.Context, logger logger.Logger) (*vaultPkg.Index, error) {
	logger.Debug("Fetching vault DB index to check for service instances")

	vaultDB, err := b.Vault.GetVaultDB(ctx)
	if err != nil {
		logger.Error("Failed to get vault DB index: %s", err)

		return nil, fmt.Errorf("failed to get vault db index: %w", err)
	}

	logger.Debug("Found %d service instances in vault DB", len(vaultDB.Data))

	return vaultDB, nil
}

func (b *Broker) processOrphanedInstances(ctx context.Context, vaultDB *vaultPkg.Index, deploymentNames map[string]bool, logger logger.Logger) []string {
	var removedDeploymentNames []string

	logger.Debug("Checking each service instance for corresponding BOSH deployment")

	for instanceID, serviceInstance := range vaultDB.Data {
		logger.Debug("Checking instance: %s", instanceID)

		deploymentName, err := b.validateAndGetDeploymentName(instanceID, serviceInstance, logger)
		if err != nil {
			continue // Error already logged
		}

		if b.isOrphanedInstance(deploymentName, deploymentNames) {
			removedDeploymentNames = append(removedDeploymentNames, deploymentName)
			b.removeOrphanedInstance(ctx, instanceID, deploymentName, logger)
		} else {
			logger.Debug("Instance %s has valid BOSH deployment %s", instanceID, deploymentName)
		}
	}

	return removedDeploymentNames
}

func (b *Broker) validateAndGetDeploymentName(instanceID string, serviceInstance interface{}, logger logger.Logger) (string, error) {
	serviceData, valid := serviceInstance.(map[string]interface{})
	if !valid {
		logger.Error("Could not parse service instance data for %s - unexpected type: %T", instanceID, serviceInstance)

		return "", ErrInvalidServiceInstanceDataType
	}

	service, serviceOK := serviceData["service_id"].(string)
	if !serviceOK {
		logger.Error("Could not parse service_id for instance %s - value type: %T", instanceID, serviceData["service_id"])

		return "", ErrCouldNotAssertServiceIDToString
	}

	logger.Debug("Instance %s - service_id: %s", instanceID, service)

	plan, ok := serviceData["plan_id"].(string)
	if !ok {
		logger.Error("Could not parse plan_id for instance %s - value type: %T", instanceID, serviceData["plan_id"])

		return "", ErrCouldNotAssertPlanIDToString
	}

	logger.Debug("Instance %s - plan_id: %s", instanceID, plan)

	deploymentName := plan + "-" + instanceID
	logger.Debug("Looking for deployment: %s", deploymentName)

	return deploymentName, nil
}

func (b *Broker) isOrphanedInstance(deploymentName string, deploymentNames map[string]bool) bool {
	_, exists := deploymentNames[deploymentName]

	return !exists
}

func (b *Broker) removeOrphanedInstance(ctx context.Context, instanceID, deploymentName string, logger logger.Logger) {
	logger.Info("Found orphaned instance %s - no BOSH deployment named: %s", instanceID, deploymentName)
	logger.Info("Removing orphaned service instance %s from vault DB", instanceID)

	err := b.Vault.Index(ctx, instanceID, nil)
	if err != nil {
		logger.Error("Failed to remove orphaned service instance '%s' from vault DB: %s", instanceID, err)
	} else {
		logger.Debug("Successfully removed orphaned instance %s from vault DB", instanceID)
	}
}

func (b *Broker) recordInitialRequest(ctx context.Context, instanceID string, details domain.ProvisionDetails, logger logger.Logger) error {
	logger.Debug("recording service request in vault immediately")

	now := time.Now()

	baseData := map[string]interface{}{
		"service_id":   details.ServiceID,
		"plan_id":      details.PlanID,
		"created":      now.Unix(), // Keep for backward compatibility
		"requested_at": now.Format(time.RFC3339),
		"status":       "request_received",
	}

	if details.OrganizationGUID != "" {
		baseData["organization_guid"] = details.OrganizationGUID
	}

	if details.SpaceGUID != "" {
		baseData["space_guid"] = details.SpaceGUID
	}

	entry := b.buildIndexEntry(ctx, instanceID, baseData, details.RawContext, logger)

	err := b.Vault.Index(ctx, instanceID, entry)
	if err != nil {
		logger.Error("failed to record service request in vault: %s", err)

		return ErrFailedToTrackServiceRequestInVault
	}

	logger.Debug("service request recorded in vault with status 'request_received'")

	return nil
}

func (b *Broker) validatePlan(ctx context.Context, instanceID string, details domain.ProvisionDetails, logger logger.Logger) (*services.Plan, error) {
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

		return nil, fmt.Errorf("failed to get vault index: %w", err)
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

func (b *Broker) updateInstanceStatus(ctx context.Context, instanceID string, details domain.ProvisionDetails, plan *services.Plan, status string, logger logger.Logger) error {
	logger.Debug("updating service instance status in vault 'db' index to '%s'", status)

	now := time.Now()

	baseData := map[string]interface{}{
		"service_id":   details.ServiceID,
		"plan_id":      plan.ID,
		"created":      now.Unix(), // Keep for backward compatibility
		"requested_at": now.Format(time.RFC3339),
		"status":       status,
	}

	if details.OrganizationGUID != "" {
		baseData["organization_guid"] = details.OrganizationGUID
	}

	if details.SpaceGUID != "" {
		baseData["space_guid"] = details.SpaceGUID
	}

	entry := b.buildIndexEntry(ctx, instanceID, baseData, details.RawContext, logger)

	err := b.Vault.Index(ctx, instanceID, entry)
	if err != nil {
		logger.Error("failed to update service status in vault index: %s", err)

		return ErrFailedToUpdateServiceStatusInVault
	}

	return nil
}

func (b *Broker) storeInstanceData(ctx context.Context, instanceID string, details domain.ProvisionDetails, plan *services.Plan, logger logger.Logger) (string, error) {
	deploymentName := fmt.Sprintf("%s-%s", details.PlanID, instanceID)
	logger.Debug("deployment name: %s", deploymentName)

	// Store deployment details
	err := b.storeDeploymentDetails(ctx, instanceID, deploymentName, details, logger)
	if err != nil {
		return "", err
	}

	// Store deployment info and root data
	b.storeDeploymentInfo(ctx, instanceID, deploymentName, details, logger)

	// Write debug files
	b.writeDebugFiles(instanceID, details, logger)

	// Store plan references
	b.storePlanReferences(ctx, instanceID, plan, logger)

	return deploymentName, nil
}

func (b *Broker) storeDeploymentDetails(ctx context.Context, instanceID, deploymentName string, details domain.ProvisionDetails, logger logger.Logger) error {
	vaultPath := fmt.Sprintf("%s/%s", instanceID, deploymentName)
	logger.Debug("storing details at Vault path: %s", vaultPath)

	err := b.Vault.Put(ctx, vaultPath, map[string]interface{}{
		"details": details,
	})
	if err != nil {
		logger.Error("failed to store details in the vault at path %s: %s", vaultPath, err)
		// Remove from index since we're failing
		indexErr := b.Vault.Index(ctx, instanceID, nil)
		if indexErr != nil {
			logger.Error("failed to remove instance from index: %s", indexErr)
		}

		return ErrFailedToStoreServiceMetadata
	}

	return nil
}

func (b *Broker) storeDeploymentInfo(ctx context.Context, instanceID, deploymentName string, details domain.ProvisionDetails, logger logger.Logger) {
	deploymentInfo := b.buildDeploymentInfo(instanceID, deploymentName, details)

	// Store deployment info at instance level
	logger.Debug("storing deployment info at instance level: %s/deployment", instanceID)

	err := b.Vault.Put(ctx, instanceID+"/deployment", deploymentInfo)
	if err != nil {
		logger.Error("failed to store deployment info at instance level: %s", err)
		// Continue anyway, this is not fatal
	}

	// Store flattened data at root instance path for backward compatibility
	logger.Debug("storing flattened data at root instance path: %s", instanceID)

	rootData := b.buildRootData(deploymentInfo, details)

	err = b.Vault.Put(ctx, instanceID, rootData)
	if err != nil {
		logger.Error("failed to store data at root instance path: %s", err)
		// Continue anyway, this is not fatal
	}
}

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

func (b *Broker) buildRootData(deploymentInfo map[string]interface{}, details domain.ProvisionDetails) map[string]interface{} {
	rootData := make(map[string]interface{})
	for k, v := range deploymentInfo {
		rootData[k] = v
	}

	// Add context and parameters if present
	if len(details.RawContext) > 0 {
		var contextData map[string]interface{}

		err := json.Unmarshal(details.RawContext, &contextData)
		if err == nil {
			rootData["context"] = contextData
		}
	}

	if len(details.RawParameters) > 0 {
		var paramsData interface{}

		err := json.Unmarshal(details.RawParameters, &paramsData)
		if err == nil {
			rootData["parameters"] = paramsData
		}
	}

	return rootData
}

func (b *Broker) addContextData(deploymentInfo map[string]interface{}, rawContext json.RawMessage) {
	if len(rawContext) == 0 {
		return
	}

	var contextData map[string]interface{}

	err := json.Unmarshal(rawContext, &contextData)
	if err != nil {
		return
	}

	contextFields := []string{"organization_name", "space_name", "instance_name", "platform"}
	for _, field := range contextFields {
		if value, ok := contextData[field].(string); ok {
			deploymentInfo[field] = value
		}
	}
}

func (b *Broker) writeDebugFiles(instanceID string, details domain.ProvisionDetails, logger logger.Logger) {
	if len(details.RawParameters) == 0 {
		return
	}

	err := WriteDataFile(instanceID, details.RawParameters)
	if err != nil {
		logger.Error("failed to write data file for debugging: %s", err)
	}

	err = WriteYamlFile(instanceID, details.RawParameters)
	if err != nil {
		logger.Error("failed to write YAML file for debugging: %s", err)
	}
}

func (b *Broker) storePlanReferences(ctx context.Context, instanceID string, plan *services.Plan, logger logger.Logger) {
	logger.Debug("storing plan file references for instance %s", instanceID)

	planStorage := planstore.New(b.Vault, b.Config)

	err := planStorage.StorePlanReferences(ctx, instanceID, *plan)
	if err != nil {
		logger.Error("failed to store plan references: %s", err)
		// Continue anyway, this is not fatal for provisioning
	}
}

func (b *Broker) launchAsyncProvisioning(ctx context.Context, instanceID string, details domain.ProvisionDetails, plan *services.Plan, logger logger.Logger) {
	// Update status to show provisioning is starting
	err := b.updateInstanceStatus(ctx, instanceID, details, plan, "provisioning_started", logger)
	if err != nil {
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
	// Create a detached context that survives the HTTP request lifecycle
	// This ensures provisioning can continue even if the request context is cancelled
	asyncCtx := context.WithoutCancel(ctx)
	go b.provisionAsync(asyncCtx, instanceID, detailsMap, *plan)
}

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
	err = b.Vault.Put(ctx, instanceID+"/metadata", metadata)
	if err != nil {
		logger.Error("failed to store created_at timestamp: %s", err)
		// Continue anyway, this is non-fatal
	}

	// Update the index with created_at
	return b.updateIndexTimestamp(ctx, instanceID, createdAt, logger)
}

func (b *Broker) updateIndexTimestamp(ctx context.Context, instanceID string, createdAt time.Time, logger logger.Logger) error {
	logger.Debug("updating index with created_at timestamp")

	idx, err := b.Vault.GetIndex(ctx, "db")
	if err != nil {
		return fmt.Errorf("failed to get vault index: %w", err)
	}

	raw, err := idx.Lookup(instanceID)
	if err != nil {
		return fmt.Errorf("failed to lookup instance in index: %w", err)
	}

	data, ok := raw.(map[string]interface{})
	if !ok {
		return nil
	}

	data["created_at"] = createdAt.Format(time.RFC3339)

	err = b.Vault.Index(ctx, instanceID, data)
	if err != nil {
		logger.Error("failed to index service instance: %s", err)
	}

	return nil
}

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

func (b *Broker) extractDetailsFromVault(ctx context.Context, vaultPath, instanceID string, logger logger.Logger) (domain.ProvisionDetails, error) {
	var detailsMetadata map[string]interface{}

	exists, err := b.Vault.Get(ctx, vaultPath, &detailsMetadata)
	if err != nil {
		// Try legacy path for backward compatibility
		logger.Debug("failed to fetch from new path, trying legacy path")

		exists, err = b.Vault.Get(ctx, instanceID, &detailsMetadata)
		if err != nil {
			logger.Error("failed to fetch instance metadata from Vault: %s", err)

			return domain.ProvisionDetails{}, fmt.Errorf("failed to fetch instance metadata from vault: %w", err)
		}
	}

	if !exists {
		return domain.ProvisionDetails{}, fmt.Errorf("%w (path: %s)", ErrCouldNotFindInstanceMetadataInVault, vaultPath)
	}

	return b.parseProvisionDetails(ctx, detailsMetadata, instanceID)
}

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

func (b *Broker) processCredentials(ctx context.Context, instanceID string, details domain.ProvisionDetails, logger logger.Logger) error {
	logger.Debug("fetching instance plan details")

	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		logger.Error("failed to find plan %s/%s: %s", details.ServiceID, details.PlanID, err)

		return err
	}

	logger.Debug("fetching instance credentials directly from BOSH")

	creds, err := manifest.GetCreds(instanceID, plan, b.BOSH, logger)
	if err != nil {
		return fmt.Errorf("failed to get credentials: %w", err)
	}

	credsMap, ok := creds.(map[string]interface{})
	if !ok {
		logger.Error("credentials are not in expected map[string]interface{} format")

		return ErrCredentialsNotInExpectedFormat
	}

	return b.storeAndVerifyCredentials(ctx, instanceID, &plan, credsMap, logger)
}

func (b *Broker) storeAndVerifyCredentials(ctx context.Context, instanceID string, plan *services.Plan, creds map[string]interface{}, logger logger.Logger) error {
	// Store credentials in vault
	logger.Debug("storing credentials in vault at %s/credentials", instanceID)

	err := b.Vault.Put(ctx, instanceID+"/credentials", creds)
	if err != nil {
		logger.Error("CRITICAL: failed to store credentials in vault: %s", err)

		return fmt.Errorf("failed to store credentials in vault: %w", err)
	}

	// Verify credentials were actually stored
	return b.verifyCredentialStorage(ctx, instanceID, plan, logger)
}

func (b *Broker) verifyCredentialStorage(ctx context.Context, instanceID string, plan *services.Plan, logger logger.Logger) error {
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

func (b *Broker) retryCredentialStorage(ctx context.Context, instanceID string, plan *services.Plan, logger logger.Logger) error {
	logger.Info("Attempting to re-fetch credentials from BOSH")

	creds2, err := manifest.GetCreds(instanceID, *plan, b.BOSH, logger)
	if err != nil {
		return fmt.Errorf("failed to verify credential storage and unable to re-fetch: %w", err)
	}

	err = b.Vault.Put(ctx, instanceID+"/credentials", creds2)
	if err != nil {
		return fmt.Errorf("failed to store credentials after retry: %w", err)
	}

	logger.Info("Successfully stored credentials on retry")

	return nil
}

func (b *Broker) scheduleBackup(ctx context.Context, instanceID string, details domain.ProvisionDetails, vmIP string, logger logger.Logger) error {
	logger.Debug("scheduling S.H.I.E.L.D. backup for instance '%s'", instanceID)

	// Get credentials for backup scheduling
	creds, err := b.getCredentialsForBackup(ctx, instanceID, logger)
	if err != nil {
		return err
	}

	err = b.Shield.CreateSchedule(instanceID, details, vmIP, creds)
	if err != nil {
		logger.Error("failed to schedule S.H.I.E.L.D. backup: %s", err)

		return ErrFailedToScheduleShieldBackup
	}

	return nil
}

func (b *Broker) getCredentialsForBackup(ctx context.Context, instanceID string, logger logger.Logger) (map[string]interface{}, error) {
	var creds map[string]interface{}

	exists, err := b.Vault.Get(ctx, instanceID+"/credentials", &creds)
	if err != nil || !exists {
		logger.Error("failed to retrieve credentials for backup scheduling: %s", err)

		return nil, fmt.Errorf("failed to retrieve credentials for backup scheduling: %w", err)
	}

	return creds, nil
}

func (b *Broker) getInstanceForOperation(ctx context.Context, instanceID string, logger logger.Logger) (*vaultPkg.Instance, string, error) {
	instance, exists, err := b.Vault.FindInstance(ctx, instanceID)
	if err != nil {
		logger.Error("could not find instance details: %s", err)

		return nil, "", fmt.Errorf("failed to find instance: %w", err)
	}

	if !exists {
		logger.Error("instance %s not found in vault index", instanceID)

		return nil, "", nil // Return nil instance to indicate deletion
	}

	deploymentName := instance.PlanID + "-" + instanceID
	logger.Debug("checking deployment: %s", deploymentName)

	return instance, deploymentName, nil
}

func (b *Broker) getLatestTask(deploymentName string, logger logger.Logger) (*bosh.Task, string, error) {
	latestTask, operationType, err := b.GetLatestDeploymentTask(deploymentName)
	if err != nil {
		// Check if this is the expected "no tasks found" error during early deployment stages
		if errors.Is(err, ErrNoTasksFoundForDeployment) {
			logger.Debug("no tasks found for deployment %s (expected during early deployment stages): %s", deploymentName, err)
		} else {
			logger.Error("failed to get latest task for deployment %s: %s", deploymentName, err)
		}

		return nil, "", err
	}

	taskID := latestTask.ID
	logger.Debug("latest task for deployment %s: task %d, type '%s', state '%s'", deploymentName, taskID, operationType, latestTask.State)

	return latestTask, operationType, nil
}

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
		err := b.OnProvisionCompleted(ctx, logger, instanceID)
		if err != nil {
			logger.Error("provision succeeded but post-hook failed: %s", err)
			// Don't fail the operation, just log the error
		}
	}

	return domain.LastOperation{State: domain.Succeeded}, nil
}

func (b *Broker) handleTaskIDNegativeOne(logger logger.Logger) (domain.LastOperation, error) {
	// Task failed during initialization
	logger.Error("operation failed during initialization")

	return domain.LastOperation{State: domain.Failed}, nil
}

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
	err = b.OnProvisionCompleted(ctx, logger, instanceID)
	if err != nil {
		return domain.LastOperation{}, ErrProvisionTaskCompletedPostHookFailed
	}

	return domain.LastOperation{State: domain.Succeeded}, nil
}

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

func (b *Broker) handleProvisionTask(ctx context.Context, instanceID, deploymentName string, taskID int, logger logger.Logger) (domain.LastOperation, error) {
	logger.Debug("retrieving task %d from BOSH director", taskID)

	task, err := b.BOSH.GetTask(taskID)
	if err != nil {
		logger.Error("failed to retrieve task %d from BOSH director: %s", taskID, err)

		return domain.LastOperation{}, ErrUnrecognizedBackendBOSHTask
	}

	switch task.State {
	case taskStateDone:
		err := b.OnProvisionCompleted(ctx, logger, instanceID)
		if err != nil {
			return domain.LastOperation{}, ErrProvisionTaskCompletedPostHookFailed
		}

		logger.Debug("provision operation succeeded")

		return domain.LastOperation{State: domain.Succeeded}, nil

	case taskStateError:
		logger.Error("provision operation failed!")
		b.cleanupFailedDeployment(deploymentName, logger)

		return domain.LastOperation{State: domain.Failed}, nil

	default:
		logger.Debug("provision operation is still in progress")

		return domain.LastOperation{State: domain.InProgress}, nil
	}
}

func (b *Broker) handleDeprovisionTask(ctx context.Context, instanceID, deploymentName string, taskID int, logger logger.Logger) (domain.LastOperation, error) {
	logger.Debug("retrieving task %d from BOSH director", taskID)

	task, err := b.BOSH.GetTask(taskID)
	if err != nil {
		logger.Error("failed to retrieve task %d from BOSH director: %s", taskID, err)

		return domain.LastOperation{}, ErrUnrecognizedBackendBOSHTask
	}

	switch task.State {
	case taskStateDone:
		return b.handleSuccessfulDeprovision(ctx, instanceID, deploymentName, logger)
	case taskStateError:
		return b.handleFailedDeprovision(deploymentName, logger)
	default:
		logger.Debug("deprovision operation is still in progress")

		return domain.LastOperation{State: domain.InProgress}, nil
	}
}

func (b *Broker) cleanupFailedDeployment(deploymentName string, logger logger.Logger) {
	logger.Debug("checking if failed deployment exists and needs cleanup")

	_, deploymentErr := b.BOSH.GetDeployment(deploymentName)
	if deploymentErr == nil {
		logger.Info("Failed deployment %s still exists, attempting cleanup", deploymentName)
		// Attempt to delete the failed deployment (best effort)
		cleanupTask, cleanupErr := b.BOSH.DeleteDeployment(deploymentName)
		if cleanupErr != nil {
			logger.Error("Failed to initiate cleanup of failed deployment %s: %s", deploymentName, cleanupErr)
		} else {
			logger.Info("Initiated cleanup of failed deployment %s (task %d)", deploymentName, cleanupTask.ID)
		}
	}
}

func (b *Broker) handleSuccessfulDeprovision(ctx context.Context, instanceID, deploymentName string, logger logger.Logger) (domain.LastOperation, error) {
	logger.Info("handleSuccessfulDeprovision called for instance %s, deployment %s", instanceID, deploymentName)

	logger.Debug("Verifying deployment is actually deleted")

	_, deploymentErr := b.BOSH.GetDeployment(deploymentName)
	if deploymentErr == nil {
		logger.Error("Deprovision task succeeded but deployment %s still exists", deploymentName)

		return domain.LastOperation{State: domain.Failed}, nil
	}

	logger.Info("Deployment %s confirmed deleted", deploymentName)

	// Store deleted_at timestamp
	logger.Debug("Storing deleted_at timestamp")
	b.storeDeletedTimestamp(ctx, instanceID, logger)

	// Remove from index after confirmed deletion
	logger.Info("Removing instance %s from service index after confirmed deletion", instanceID)

	err := b.Vault.Index(ctx, instanceID, nil)
	if err != nil {
		logger.Error("Failed to remove service from vault index: %s", err)
		// Continue anyway, the deployment is gone
	} else {
		logger.Info("Successfully removed instance %s from index", instanceID)
	}

	logger.Debug("keeping secrets in vault for audit purposes")
	// Note: We intentionally do NOT call b.Vault.Clear(instanceID) here
	// to preserve secrets for auditing purposes
	return domain.LastOperation{State: domain.Succeeded}, nil
}

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
	err = b.Vault.Put(ctx, instanceID+"/metadata", metadata)
	if err != nil {
		logger.Error("failed to store deleted_at timestamp: %s", err)
		// Continue anyway, this is non-fatal
	}
}

func (b *Broker) findPlanForBinding(details domain.BindDetails, logger logger.Logger) (*services.Plan, error) {
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

func (b *Broker) getInstanceCredentials(instanceID string, plan *services.Plan, logger logger.Logger) (interface{}, error) {
	logger.Info("Retrieving credentials for instance %s", instanceID)
	logger.Debug("Calling GetCreds for plan %s", plan.Name)

	creds, err := manifest.GetCreds(instanceID, *plan, b.BOSH, logger)
	if err != nil {
		logger.Error("Failed to retrieve credentials: %s", err)

		return nil, fmt.Errorf("failed to retrieve credentials: %w", err)
	}

	logger.Debug("Successfully retrieved credentials")

	return creds, nil
}

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

	logger.Info("Creating dynamic RabbitMQ user for binding", "bindingID", bindingID)
	logger.Debug("Creating user in RabbitMQ", "username", usernameDynamic, "apiUrl", apiUrl)

	// Create user and grant permissions
	err = b.setupRabbitMQUser(ctx, usernameDynamic, passwordDynamic, adminCreds, apiUrlStr, credMap, logger)
	if err != nil {
		return nil, err
	}

	// Replace static credentials with dynamic ones
	return b.replaceCredentialsWithDynamic(credMap, staticCreds, usernameDynamic, passwordDynamic)
}

func (b *Broker) validateRabbitMQAdminCredentials(credMap map[string]interface{}) (*rabbitmqAdminCredentials, error) {
	adminUsername, isString := credMap["admin_username"].(string)
	if !isString {
		return nil, ErrAdminUsernameMustBeString
	}

	adminPassword, exists := credMap["admin_password"].(string)
	if !exists {
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

func (b *Broker) validateRabbitMQStaticCredentials(credMap map[string]interface{}) (*rabbitmqStaticCredentials, error) {
	username, isString := credMap["username"].(string)
	if !isString {
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

func (b *Broker) setupRabbitMQUser(ctx context.Context, usernameDynamic, passwordDynamic string, adminCreds *rabbitmqAdminCredentials, apiUrlStr string, credMap map[string]interface{}, logger logger.Logger) error {
	// Create user
	err := CreateUserPassRabbitMQ(ctx, usernameDynamic, passwordDynamic, adminCreds.username, adminCreds.password, apiUrlStr, b.Config, credMap)
	if err != nil {
		logger.Error("Failed to create RabbitMQ user", "error", err)

		return err
	}

	logger.Debug("Successfully created RabbitMQ user", "username", usernameDynamic)

	// Grant permissions
	logger.Debug("Granting permissions to user %s for vhost %s", usernameDynamic, adminCreds.vhost)

	err = GrantUserPermissionsRabbitMQ(ctx, usernameDynamic, adminCreds.username, adminCreds.password, adminCreds.vhost, apiUrlStr, b.Config, credMap)
	if err != nil {
		logger.Error("Failed to grant permissions to RabbitMQ user", "username", usernameDynamic, "error", err)

		return err
	}

	logger.Debug("Successfully granted permissions to user %s", usernameDynamic)

	return nil
}

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

func (b *Broker) cleanupAdminCredentials(creds interface{}) {
	if credMap, ok := creds.(map[string]interface{}); ok {
		delete(credMap, "admin_username")
		delete(credMap, "admin_password")
	}
}

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

func (b *Broker) getRabbitMQCredentials(_ context.Context, instanceID string, plan *services.Plan, logger logger.Logger) (map[string]interface{}, error) {
	logger.Debug("Retrieving admin credentials for RabbitMQ instance")

	creds, err := manifest.GetCreds(instanceID, *plan, b.BOSH, logger)
	if err != nil {
		logger.Error("Failed to retrieve credentials: %s", err)

		return nil, fmt.Errorf("failed to get rabbitmq credentials: %w", err)
	}

	credMap, ok := creds.(map[string]interface{})
	if !ok {
		logger.Error("Invalid creds type: %T", creds)

		return nil, fmt.Errorf("%w: %T", ErrInvalidCredsType, creds)
	}

	return credMap, nil
}

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

	logger.Info("Deleting dynamic RabbitMQ user", "bindingID", bindingID)
	logger.Debug("Calling RabbitMQ API to delete user", "apiUrl", apiUrl)

	// Wrap the delete operation with retry logic
	const (
		maxRetries = 3
		baseDelay  = 1 * time.Second
	)

	err = common.WithRetry(ctx, func() error {
		return DeletetUserRabbitMQ(ctx, bindingID, adminUsername, adminPassword, apiUrl, b.Config, credMap)
	}, maxRetries, baseDelay)
	if err != nil {
		// Check if it's a 404 error which should be considered success
		if strings.Contains(err.Error(), "404") {
			logger.Info("User %s doesn't exist, considering as successful deletion", bindingID)

			return nil
		}

		logger.Error("Failed to delete RabbitMQ user after retries", "bindingID", bindingID, "error", err)

		return fmt.Errorf("failed to delete RabbitMQ user: %w", err)
	}

	logger.Debug("Successfully deleted RabbitMQ user", "bindingID", bindingID)

	return nil
}

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

func (b *Broker) handleDynamicRabbitMQCredentials(ctx context.Context, credsMap map[string]interface{}, bindingID string, logger logger.Logger) error {
	adminCreds, err := b.extractRabbitMQAdminCredentials(credsMap)
	if err != nil {
		return err
	}

	staticCreds, err := b.extractRabbitMQStaticCredentials(credsMap)
	if err != nil {
		return err
	}

	dynamicCreds := b.generateDynamicCredentials(bindingID)

	err = b.createRabbitMQUser(ctx, dynamicCreds, adminCreds, logger)
	if err != nil {
		return err
	}

	err = b.grantRabbitMQPermissions(ctx, dynamicCreds, adminCreds, logger)
	if err != nil {
		return err
	}

	return b.updateCredentialsMap(credsMap, staticCreds, dynamicCreds)
}

type rabbitMQAdminCreds struct {
	apiURL   string
	username string
	password string
	vhost    string
}

type rabbitMQDynamicCreds struct {
	username string
	password string
}

type rabbitMQStaticCreds struct {
	username string
	password string
}

func (b *Broker) extractRabbitMQAdminCredentials(credsMap map[string]interface{}) (*rabbitMQAdminCreds, error) {
	apiURL, exists := credsMap["api_url"].(string)
	if !exists {
		return nil, ErrAPIURLNotFoundOrNotString
	}

	// Expand templates in the API URL if present
	apiURL = b.expandTemplateString(apiURL, credsMap)

	adminUsername, exists := credsMap["admin_username"].(string)
	if !exists {
		return nil, ErrAdminUsernameNotFoundOrNotString
	}

	adminPassword, exists := credsMap["admin_password"].(string)
	if !exists {
		return nil, ErrAdminPasswordNotFoundOrNotString
	}

	vhost, ok := credsMap["vhost"].(string)
	if !ok {
		return nil, ErrVHostNotFoundOrNotString
	}

	return &rabbitMQAdminCreds{
		apiURL:   apiURL,
		username: adminUsername,
		password: adminPassword,
		vhost:    vhost,
	}, nil
}

func (b *Broker) expandTemplateString(templateStr string, credsMap map[string]interface{}) string {
	// If no template markers are found, return the original string
	if !strings.Contains(templateStr, "{{") {
		return templateStr
	}

	// For RabbitMQ, if the host is already resolved but still contains template,
	// look for IPs in the credentials map from the manifest processing
	if strings.Contains(templateStr, "{{.Jobs.rabbitmq.IPs.0}}") {
		return b.expandRabbitMQTemplate(templateStr, credsMap)
	}

	// Return the original string if no expansion was possible
	return templateStr
}

func (b *Broker) expandRabbitMQTemplate(templateStr string, credsMap map[string]interface{}) string {
	// First check if host is already resolved
	if host, ok := credsMap["host"].(string); ok && !strings.Contains(host, "{{") {
		return strings.ReplaceAll(templateStr, "{{.Jobs.rabbitmq.IPs.0}}", host)
	}

	// Otherwise look for the IP in job data that might be in the credentials
	// This would be populated by the manifest generation from BOSH VMs
	ip := b.extractIPFromJobs(credsMap)
	if ip != "" {
		return strings.ReplaceAll(templateStr, "{{.Jobs.rabbitmq.IPs.0}}", ip)
	}

	return templateStr
}

func (b *Broker) extractIPFromJobs(credsMap map[string]interface{}) string {
	jobs, jobsExist := credsMap["jobs"].([]interface{})
	if !jobsExist || len(jobs) == 0 {
		return ""
	}

	for _, job := range jobs {
		jobMap, isJobMap := job.(map[string]interface{})
		if !isJobMap {
			continue
		}

		ips, ipsExist := jobMap["IPs"].([]interface{})
		if !ipsExist || len(ips) == 0 {
			continue
		}

		if ip, isString := ips[0].(string); isString {
			return ip
		}
	}

	return ""
}

func (b *Broker) extractRabbitMQStaticCredentials(credsMap map[string]interface{}) (*rabbitMQStaticCreds, error) {
	username, exists := credsMap["username"].(string)
	if !exists {
		return nil, ErrUsernameMustBeString
	}

	password, ok := credsMap["password"].(string)
	if !ok {
		return nil, ErrPasswordMustBeString
	}

	return &rabbitMQStaticCreds{
		username: username,
		password: password,
	}, nil
}

func (b *Broker) generateDynamicCredentials(bindingID string) *rabbitMQDynamicCreds {
	return &rabbitMQDynamicCreds{
		username: bindingID,
		password: uuid.New().String(),
	}
}

func (b *Broker) createRabbitMQUser(ctx context.Context, dynamicCreds *rabbitMQDynamicCreds, adminCreds *rabbitMQAdminCreds, logger logger.Logger) error {
	logger.Debug("Creating dynamic RabbitMQ user", "username", dynamicCreds.username)

	// Wrap the create operation with retry logic
	const (
		maxRetries = 3
		baseDelay  = 1 * time.Second
	)

	err := common.WithRetry(ctx, func() error {
		return CreateUserPassRabbitMQ(ctx, dynamicCreds.username, dynamicCreds.password,
			adminCreds.username, adminCreds.password, adminCreds.apiURL, b.Config, map[string]interface{}{})
	}, maxRetries, baseDelay)
	if err != nil {
		return fmt.Errorf("failed to create RabbitMQ user after retries: %w", err)
	}

	return nil
}

func (b *Broker) grantRabbitMQPermissions(ctx context.Context, dynamicCreds *rabbitMQDynamicCreds, adminCreds *rabbitMQAdminCreds, logger logger.Logger) error {
	logger.Debug("Granting permissions to user %s for vhost %s", dynamicCreds.username, adminCreds.vhost)

	err := GrantUserPermissionsRabbitMQ(ctx, dynamicCreds.username, adminCreds.username, adminCreds.password, adminCreds.vhost, adminCreds.apiURL, b.Config, map[string]interface{}{})
	if err != nil {
		return fmt.Errorf("failed to grant permissions: %w", err)
	}

	return nil
}

func (b *Broker) updateCredentialsMap(credsMap map[string]interface{}, staticCreds *rabbitMQStaticCreds, dynamicCreds *rabbitMQDynamicCreds) error {
	updatedCreds, err := yamlGsub(credsMap, staticCreds.username, dynamicCreds.username)
	if err != nil {
		return fmt.Errorf("failed to substitute username: %w", err)
	}

	updatedCreds, err = yamlGsub(updatedCreds, staticCreds.password, dynamicCreds.password)
	if err != nil {
		return fmt.Errorf("failed to substitute password: %w", err)
	}

	if updatedMap, ok := updatedCreds.(map[string]interface{}); ok {
		for k, v := range updatedMap {
			credsMap[k] = v
		}
	}

	credsMap["username"] = dynamicCreds.username
	credsMap["password"] = dynamicCreds.password
	credsMap["credential_type"] = "dynamic"

	delete(credsMap, "admin_username")
	delete(credsMap, "admin_password")

	return nil
}

func (b *Broker) populateBindingCredentials(binding *BindingCredentials, credsMap map[string]interface{}) {
	binding.Raw = filterAdminCredentials(credsMap)
	setCredentialType(binding)
	populateBasicFields(binding)
	populateServiceFields(binding)
}

func filterAdminCredentials(credsMap map[string]interface{}) map[string]interface{} {
	filtered := make(map[string]interface{}, len(credsMap))
	for k, v := range credsMap {
		if k != "admin_username" && k != "admin_password" {
			filtered[k] = v
		}
	}

	return filtered
}

func setCredentialType(binding *BindingCredentials) {
	if credType, ok := binding.Raw["credential_type"].(string); ok && credType != "" {
		binding.CredentialType = credType
	} else {
		binding.CredentialType = "static"
	}

	binding.Raw["credential_type"] = binding.CredentialType
}

func populateBasicFields(binding *BindingCredentials) {
	if host, ok := binding.Raw["host"].(string); ok {
		binding.Host = host
	}

	if port, ok := binding.Raw["port"].(float64); ok {
		binding.Port = int(port)
	} else if port, ok := binding.Raw["port"].(int); ok {
		binding.Port = port
	}

	if username, ok := binding.Raw["username"].(string); ok {
		binding.Username = username
	}

	if password, ok := binding.Raw["password"].(string); ok {
		binding.Password = password
	}
}

func populateServiceFields(binding *BindingCredentials) {
	if uri, ok := binding.Raw["uri"].(string); ok {
		binding.URI = uri
	}

	if apiURL, ok := binding.Raw["api_url"].(string); ok {
		binding.APIURL = apiURL
	}

	if vhost, ok := binding.Raw["vhost"].(string); ok {
		binding.Vhost = vhost
	}

	if database, ok := binding.Raw["database"].(string); ok {
		binding.Database = database
	}

	if scheme, ok := binding.Raw["scheme"].(string); ok {
		binding.Scheme = scheme
	}
}

// recordInitialRequest records the initial provision request in Vault.
// validatePlan finds the plan and validates service limits.
// updateInstanceStatus updates the instance status in Vault.
// storeInstanceData stores all instance-related data in Vault.
// storeDeploymentDetails stores the deployment details in Vault.
// storeDeploymentInfo stores deployment info at instance level and root path.
// buildDeploymentInfo builds the deployment info map.
// buildRootData builds the root data map for backward compatibility.
// addContextData adds context data to the deployment info.
// writeDebugFiles writes debug files for auditing.
// storePlanReferences stores plan file references.
// launchAsyncProvisioning launches the async provisioning process.
// updateInstanceTimestamp updates the instance with created_at timestamp.
// updateIndexTimestamp updates the index with created_at timestamp.
// getInstanceDetails retrieves instance details and constructs deployment name.
// extractDetailsFromVault extracts provision details from vault.
// parseProvisionDetails parses provision details from vault metadata.
// getDeploymentVMs retrieves VMs for the deployment.
// processCredentials fetches and stores credentials.
// storeAndVerifyCredentials stores credentials and verifies storage.
// verifyCredentialStorage verifies that credentials were properly stored.
// retryCredentialStorage attempts to re-fetch and store credentials.
// scheduleBackup schedules Shield backup for the instance.
// getCredentialsForBackup retrieves credentials for backup scheduling.
// getInstanceForOperation retrieves instance information for the operation.
// getLatestTask retrieves the latest task for the deployment.
// handleTaskRetrievalError handles errors when retrieving tasks.
// handleOperationStatus handles the operation status based on task ID and type.
// handleTaskIDZero handles the special case where task ID is 0.
// handleTaskIDNegativeOne handles the special case where task ID is -1.
// handleTaskIDOne handles the special case where task ID is 1.
// handleRegularTask handles regular task IDs for provision and deprovision operations.
// handleProvisionTask handles provision task operations.
// handleDeprovisionTask handles deprovision task operations.
// cleanupFailedDeployment attempts to clean up a failed deployment.
// handleSuccessfulDeprovision handles successful deprovision operations.
// handleFailedDeprovision handles failed deprovision operations.
// storeDeletedTimestamp stores the deleted_at timestamp in Vault.
// findPlanForBinding finds the plan for the binding operation.
// getInstanceCredentials retrieves credentials for the instance.
// processRabbitMQCredentials processes RabbitMQ credentials for dynamic user creation.
// createDynamicRabbitMQUser creates a dynamic RabbitMQ user for the binding.
// validateRabbitMQAdminCredentials validates admin credentials for RabbitMQ.
// validateRabbitMQStaticCredentials validates static credentials for RabbitMQ.
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
// replaceCredentialsWithDynamic replaces static credentials with dynamic ones.
// cleanupAdminCredentials removes admin credentials from the final output.
func yamlGsub(
	obj interface{},
	orig string,
	replacement string,
) (
	interface{},
	error,
) {
	logger := logger.Get().Named("broker")
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

	err = yaml.Unmarshal([]byte(replaced), &data)
	if err != nil {
		logger.Error("Failed to unmarshal replaced YAML: %s", err)

		return nil, fmt.Errorf("failed to unmarshal replaced YAML: %w", err)
	}

	logger.Debug("Successfully completed YAML substitution")

	return utils.DeinterfaceMap(data), nil
}

// createHTTPClientForService creates an HTTP client with optional TLS verification skip.
func createHTTPClientForService(serviceName string, cfg *config.Config) *http.Client {
	if cfg != nil && cfg.Services.ShouldSkipTLSVerify(serviceName) {
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
	logger.Debug("Original request URL", "url", originalURL)
	// Extract connection options from credentials
	connOptions := extractConnectionOptions(creds, logger)
	// Try BOSH DNS hostname first
	resp, err := tryHostnameConnection(req, httpClient, connOptions.hostname, originalURL, logger)
	if err == nil {
		return resp, nil
	}
	// Fallback to IP addresses
	resp, err = tryIPConnections(req, httpClient, connOptions.ips, originalURL, logger)
	if err == nil {
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
		logger.Debug("Found BOSH DNS hostname in credentials", "hostname", h)
	}
	// Extract IPs from jobs array
	options.ips = extractIPsFromJobs(creds)
	logger.Debug("Available IPs for fallback", "ips", options.ips)

	return options
}

// extractIPsFromJobs extracts IP addresses from the jobs array in credentials.
func extractIPsFromJobs(creds map[string]interface{}) []string {
	var ips []string

	jobsData, exists := creds["jobs"]
	if !exists {
		return ips
	}

	jobsArray, exists := jobsData.([]interface{})
	if !exists || len(jobsArray) == 0 {
		return ips
	}

	firstJob, exists := jobsArray[0].(map[string]interface{})
	if !exists {
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

	logger.Info("Attempting connection via BOSH DNS", "url", hostnameURL)
	req.URL, _ = url.Parse(hostnameURL)

	resp, err := httpClient.Do(req)
	if err == nil {
		logger.Info("Successfully connected via BOSH DNS hostname")

		return resp, nil
	}

	logger.Debug("BOSH DNS connection failed", "error", err)
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
		logger.Info("Attempting connection via IP", "attempt", i+1, "total", len(ips), "ip", ipAddress)

		resp, err := tryIPConnection(req, httpClient, ipAddress, originalURL, logger)
		if err == nil {
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
		logger.Info("Successfully connected via IP address", "ip", ipAddress)

		return resp, nil
	}

	logger.Debug("IP connection failed", "ip", ipAddress, "error", err)

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
	parsedURL, err := url.Parse(originalURL)
	if err == nil {
		// Check if newHost already contains a port
		_, _, err := net.SplitHostPort(newHost)
		if err == nil {
			// newHost already has a port, use it directly
			parsedURL.Host = newHost
		} else {
			// newHost doesn't have a port, preserve the original port if it exists
			_, port, err := net.SplitHostPort(parsedURL.Host)
			if err == nil {
				parsedURL.Host = net.JoinHostPort(newHost, port)
			} else {
				parsedURL.Host = newHost
			}
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

func isRetryableHTTPError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	// Check for retryable HTTP status codes in error message
	return strings.Contains(errStr, "500") || // Internal Server Error
		strings.Contains(errStr, "502") || // Bad Gateway
		strings.Contains(errStr, "503") || // Service Unavailable
		strings.Contains(errStr, "504") || // Gateway Timeout
		strings.Contains(errStr, "context deadline exceeded") || // Timeout
		strings.Contains(errStr, "connection refused") || // Connection issues
		strings.Contains(errStr, "i/o timeout") || // Network timeout
		strings.Contains(errStr, "EOF") // Network connection closed
}

// httpStatusError wraps an error with HTTP status code information.
type httpStatusError struct {
	statusCode int
	err        error
}

func (e *httpStatusError) Error() string {
	return e.err.Error()
}

func (e *httpStatusError) Unwrap() error {
	return e.err
}

func (e *httpStatusError) StatusCode() int {
	return e.statusCode
}

// retryWithBackoff performs an operation with exponential backoff retry logic.
func retryWithBackoff(ctx context.Context, maxRetries int, baseDelay time.Duration, operation func(context.Context) error, logger logger.Logger) error {
	const maxShift = 10 // Max shift to prevent overflow (2^10 = 1024)

	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Check context before each attempt
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled: %w", ctx.Err())
		default:
		}

		// Create timeout context for this attempt
		attemptCtx, cancel := context.WithTimeout(ctx, defaultDeleteTimeout)
		err := operation(attemptCtx)

		cancel()

		if err == nil {
			return nil
		}

		lastErr = err

		// Check if we should retry
		if !shouldRetry(err, attempt, maxRetries) {
			return err
		}

		// Calculate delay with exponential backoff
		// Safe conversion: attempt is always between 0 and maxRetries (3)
		// Using maxShift to cap the exponential growth
		shiftAmount := min(attempt, maxShift)
		if shiftAmount < 0 {
			shiftAmount = 0
		}
		// #nosec G115 - shiftAmount is bounded between 0 and maxShift(10)
		delay := time.Duration(1<<uint(shiftAmount)) * baseDelay
		logger.Debug("Retrying after delay", "attempt", attempt+1, "delay", delay)

		select {
		case <-time.After(delay):
			continue
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
		}
	}

	return lastErr
}

// shouldRetry determines if an error is retryable based on the error type and attempt count.
func shouldRetry(err error, attempt, maxRetries int) bool {
	if attempt >= maxRetries {
		return false
	}

	// Check if it's an HTTP error
	if isRetryableHTTPError(err) {
		return true
	}

	// Check for specific HTTP status codes in wrapped errors
	var httpErr interface{ StatusCode() int }
	if errors.As(err, &httpErr) {
		statusCode := httpErr.StatusCode()

		return statusCode >= 500 || statusCode == http.StatusRequestTimeout
	}

	return false
}

func CreateUserPassRabbitMQ(ctx context.Context, usernameDynamic, passwordDynamic, adminUsername, adminPassword, apiUrl string, cfg *config.Config, creds map[string]interface{}) error {
	logger := logger.Get().Named("broker")
	logger.Info("Creating RabbitMQ user", "username", usernameDynamic)
	logger.Debug("API URL: %s, Admin user: %s", apiUrl, adminUsername)

	const maxRetries = 3

	createUserFunc := func(attemptCtx context.Context) error {
		data, err := prepareUserCreationPayload(passwordDynamic, logger)
		if err != nil {
			return err
		}

		request, err := buildUserCreationRequest(attemptCtx, apiUrl, usernameDynamic, adminUsername, adminPassword, data, logger)
		if err != nil {
			return err
		}

		resp, err := executeUserCreationRequest(request, cfg, creds, logger)
		if err != nil {
			return err
		}

		defer func() { _ = resp.Body.Close() }()

		return validateUserCreationResponse(resp, usernameDynamic, logger)
	}

	return retryWithBackoff(ctx, maxRetries, defaultRetryBaseDelay, createUserFunc, logger)
}

func prepareUserCreationPayload(passwordDynamic string, logger logger.Logger) ([]byte, error) {
	payload := struct {
		Password string `json:"password"`
		Tags     string `json:"tags"`
	}{Password: passwordDynamic, Tags: "management,policymaker"}

	data, err := json.Marshal(payload)
	if err != nil {
		logger.Error("Failed to marshal user creation payload: %s", err)

		return nil, fmt.Errorf("failed to marshal user creation payload: %w", err)
	}

	logger.Debug("User creation payload: %s", string(data))

	return data, nil
}

func buildUserCreationRequest(ctx context.Context, apiUrl, usernameDynamic, adminUsername, adminPassword string, data []byte, logger logger.Logger) (*http.Request, error) {
	createUrl := apiUrl + "/users/" + usernameDynamic
	logger.Debug("Creating user at URL: %s", createUrl)

	// Don't create a timeout context here - let the caller manage the context
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, createUrl, bytes.NewBuffer(data))
	if err != nil {
		logger.Error("Failed to create HTTP request for user creation: %s", err)

		return nil, fmt.Errorf("failed to create HTTP request: %w", err)
	}

	request.SetBasicAuth(adminUsername, adminPassword)
	request.Header.Set("Content-Type", "application/json")

	return request, nil
}

func executeUserCreationRequest(request *http.Request, cfg *config.Config, creds map[string]interface{}, logger logger.Logger) (*http.Response, error) {
	httpClient := createHTTPClientForService("rabbitmq", cfg)

	logger.Debug("Attempting user creation with BOSH DNS fallback to IP")

	resp, err := tryServiceRequest(request, httpClient, creds, logger)
	if err != nil {
		logger.Error("HTTP request failed for user creation: %s", err)

		return nil, err
	}

	return resp, nil
}

func validateUserCreationResponse(resp *http.Response, usernameDynamic string, logger logger.Logger) error {
	// Accept 201 (Created), 204 (No Content), or 409 (Conflict) as successful
	// 204 or 409 means the user already exists, which is acceptable for idempotency
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		err := fmt.Errorf("%w, status code: %d, response: %s", ErrFailedToCreateRabbitMQUser, resp.StatusCode, string(body))
		logger.Error("Failed to create user", "statusCode", resp.StatusCode, "response", string(body))

		// Return error wrapped with status code for retry logic
		return &httpStatusError{statusCode: resp.StatusCode, err: err}
	}

	if resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusNoContent {
		logger.Info("RabbitMQ user already exists (idempotent success)", "username", usernameDynamic)
	} else {
		logger.Info("Successfully created RabbitMQ user", "username", usernameDynamic)
	}

	return nil
}
func GrantUserPermissionsRabbitMQ(ctx context.Context, usernameDynamic, adminUsername, adminPassword, vhost, apiUrl string, cfg *config.Config, creds map[string]interface{}) error {
	logger := logger.Get().Named("broker")
	logger.Debug("Granting permissions to RabbitMQ user", "username", usernameDynamic)
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

	// Create timeout context for the entire operation
	ctx, cancel := context.WithTimeout(ctx, defaultDeleteTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodPut, permUrl, bytes.NewBuffer(data))
	if err != nil {
		logger.Error("Failed to create HTTP request for permissions: %s", err)

		return fmt.Errorf("failed to create HTTP request for permissions: %w", err)
	}

	request.SetBasicAuth(adminUsername, adminPassword)
	request.Header.Set("Content-Type", "application/json")

	httpClient := createHTTPClientForService("rabbitmq", cfg)

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

	logger.Debug("Successfully granted permissions to RabbitMQ user", "username", usernameDynamic)

	return nil
}
func DeletetUserRabbitMQ(ctx context.Context, bindingID, adminUsername, adminPassword, apiUrl string, cfg *config.Config, creds map[string]interface{}) error {
	logger := logger.Get().Named("broker")
	logger.Info("Deleting RabbitMQ user", "bindingID", bindingID)
	logger.Debug("API URL: %s, Admin user: %s", apiUrl, adminUsername)

	const (
		maxRetries = 3
		baseDelay  = 50 * time.Millisecond
	)

	deleteUserFunc := func(attemptCtx context.Context) error {
		deleteUrl := apiUrl + "/users/" + bindingID
		logger.Debug("Deleting user at URL: %s", deleteUrl)

		request, err := http.NewRequestWithContext(attemptCtx, http.MethodDelete, deleteUrl, nil)
		if err != nil {
			logger.Error("Failed to create HTTP DELETE request: %s", err)

			return fmt.Errorf("failed to create HTTP DELETE request: %w", err)
		}

		request.SetBasicAuth(adminUsername, adminPassword)
		request.Header.Set("Content-Type", "application/json")

		httpClient := createHTTPClientForService("rabbitmq", cfg)

		logger.Debug("Attempting user deletion with BOSH DNS fallback to IP")

		resp, err := tryServiceRequest(request, httpClient, creds, logger)
		if err != nil {
			logger.Error("HTTP request failed for user deletion: %s", err)

			return err
		}

		defer func() { _ = resp.Body.Close() }()

		// Accept both 204 (No Content) and 404 (Not Found) as successful deletion
		// 404 means the user is already gone, which is the desired end state
		if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
			if resp.StatusCode == http.StatusNotFound {
				logger.Info("RabbitMQ user already deleted (idempotent success)", "bindingID", bindingID)
			} else {
				logger.Info("Successfully deleted RabbitMQ user", "bindingID", bindingID)
			}

			return nil
		}

		body, _ := io.ReadAll(resp.Body)
		err = fmt.Errorf("%w, status code: %d, response: %s", ErrFailedToDeleteRabbitMQUser, resp.StatusCode, string(body))
		logger.Error("Failed to delete user", "statusCode", resp.StatusCode, "response", string(body))

		// Create a custom error type that includes status code for retry logic
		return &httpStatusError{statusCode: resp.StatusCode, err: err}
	}

	return retryWithBackoff(ctx, maxRetries, baseDelay, deleteUserFunc, logger)
}

// handleRabbitMQUnbind processes the unbind operation for RabbitMQ services.
// getRabbitMQCredentials retrieves admin credentials for RabbitMQ.
// deleteRabbitMQUser handles the deletion of a RabbitMQ user.
// extractStringCred extracts a string credential from the credentials map.
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
// GetLatestDeploymentTask retrieves the most recent task for a deployment from BOSH.
// getServiceAndPlanFromVault retrieves service and plan IDs from vault storage.
// handleDynamicRabbitMQCredentials processes dynamic RabbitMQ user creation for bindings.
// populateBindingCredentials fills the structured fields from the raw credential map.
// ServiceWithNoDeploymentCheck checks for services with no deployments.
