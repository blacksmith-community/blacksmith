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
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/config"
	"blacksmith/internal/inflight"
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
	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
	"github.com/google/uuid"
	"gopkg.in/yaml.v2"
)

const (
	// Operation types. They double as the OSB operation data the broker
	// returns when it accepts a request, which the platform sends back on
	// every last_operation poll, and as the action recorded at <instance>/task.
	operationTypeProvision   = "provision"
	operationTypeDeprovision = "deprovision"

	// The task ID the director adapter returns before it knows the real one.
	placeholderTaskID = 1

	// The state TrackProgress records for an operation that failed.
	recordedStateFailed = "failed"

	// The description LastOperation gives a deprovision that has finished.
	descriptionInstanceDeleted = "Instance has been deleted"

	// Deployment-level BOSH event actions that name the task of a deploy or
	// a delete of the deployment itself.
	boshEventObjectDeployment = "deployment"
	boshEventActionCreate     = "create"
	boshEventActionDelete     = "delete"

	// File permissions.
	defaultFilePermissions   = 0600
	defaultScriptPermissions = 0700

	// Default retry attempts.
	defaultDeleteRetryAttempts = 3
	credentialTypeDynamic      = "dynamic"

	// Task states.
	taskStateDone      = "done"
	taskStateError     = "error"
	taskStateCancelled = "cancelled"
	taskStateTimeout   = "timeout"
)

// Static errors for err113 compliance.
var (
	ErrPlanNotFound                          = errors.New("plan not found")
	ErrNoForgeDirectoriesFoundViaScan        = errors.New("no forge directories found via auto-scan")
	ErrNoServiceDirectoriesProvided          = errors.New("no service directories provided")
	ErrUnmarshalYAMLData                     = errors.New("failed to unmarshal YAML data")
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
	defaultDeleteTimeout  = 30 * time.Second
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
	Catalog []osbapi.Service
	Plans   map[string]services.Plan
	BOSH    bosh.Director
	Vault   *internalVault.Vault
	Shield  shield.Client
	Config  *config.Config

	// ServiceContext ends when the broker shuts down. Detached work derives
	// from it. When nil, that work runs under context.Background().
	ServiceContext context.Context

	// VM monitoring integration
	vmMonitor interfaces.VMMonitor

	// Concurrency control for bind/unbind operations
	instanceMu    sync.RWMutex           // Protects InstanceLocks map
	InstanceLocks map[string]*sync.Mutex // Per-instance mutexes (exported for initialization)

	// activeOperations holds the claim of the one provision or deprovision in
	// flight for an instance ID in this process, keyed by instance ID with an
	// *operationClaim value. Provision and Deprovision both claim an ID with
	// LoadOrStore before any write, so one atomic step decides which operation
	// owns it, and the other request is answered from the claim. The claim
	// lasts until the operation's goroutine returns, or until its accept chain
	// fails or panics. LastOperation consults it to tell an operation that has
	// not recorded its task yet from one the broker lost when it restarted.
	activeOperations sync.Map

	// monitorExited, when set, is called with the instance ID each time a
	// deprovision monitor goroutine returns. Production leaves it nil; tests
	// use it to wait for the monitor to stop.
	monitorExited func(instanceID string)

	// CredentialCleaner deletes a confirmed-deleted deployment's variables
	// from the director's CredHub. A nil value turns the feature off.
	CredentialCleaner credentialCleaner

	// CredentialFinder lists the director CredHub paths the orphan sweep
	// examines. The sweep does nothing without it.
	CredentialFinder credentialFinder

	// credentialCleanups tracks the cleanup and sweep goroutines, so tests
	// can wait for them.
	credentialCleanups sync.WaitGroup

	// credentialSweep keeps one sweep pass at a time and at most one an hour.
	credentialSweep credentialSweepState

	// unknownOutcomeClock, when set, replaces time.Now as the clock an
	// unknown outcome is recorded and judged by, so tests can let the
	// grace period pass without waiting for it.
	unknownOutcomeClock func() time.Time

	// unknownOutcomePasses holds an *unknownOutcomePass for each instance
	// whose unknown outcome a poll has tried to settle.
	unknownOutcomePasses sync.Map
}

// operationClaim is the claim an operation holds on an instance ID in
// activeOperations. A later request for the same ID is answered from it.
type operationClaim struct {
	// operation is operationTypeProvision or operationTypeDeprovision.
	operation string
	claimed   time.Time

	// request is a provision's request, kept as a copy so a repeat can be
	// compared with it. A deprovision claim leaves it empty.
	request osbapi.ProvisionRequest

	// launched is set just before the operation's goroutine starts. Until
	// then the accept chain may still fail, so the first request's answer is
	// not known and a repeat cannot be told the operation was accepted.
	launched atomic.Bool
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

// yamlErrorSummary describes a YAML decode error without the fragment of the
// offending value that yaml.v2 puts in a type error, because that value can be
// a credential.
func yamlErrorSummary(err error) string {
	var typeErr *yaml.TypeError
	if errors.As(err, &typeErr) {
		return fmt.Sprintf("%d value(s) in the YAML have a type the target does not accept", len(typeErr.Errors))
	}

	return err.Error()
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
		summary := yamlErrorSummary(err)

		logger.Error("Failed to unmarshal data for YAML file: %s", summary)
		logger.Debug("Unparseable YAML data for instance %s is %d bytes; its content is not logged because it can hold credentials", instanceID, len(data))

		return fmt.Errorf("%w: %s", ErrUnmarshalYAMLData, summary)
	}

	yamlBytes, err := yaml.Marshal(mergedMap)
	if err != nil {
		logger.Error("Failed to marshal data to YAML: %s", err)
		logger.Debug("Merged map for instance %s has %d top-level keys; its content is not logged because it can hold credentials", instanceID, len(mergedMap))

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

func (b *Broker) GetCatalog(ctx context.Context) (*osbapi.Catalog, error) {
	logger := logger.Get().Named("broker")
	logger.Info("Retrieving service catalog")
	logger.Debug("Catalog contains %d services", len(b.Catalog))

	logger.Info("Successfully retrieved %d services from catalog", len(b.Catalog))

	return &osbapi.Catalog{Services: b.Catalog}, nil
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
	details osbapi.ProvisionRequest,
	asyncAllowed bool,
) (
	osbapi.ProvisionResponse,
	bool,
	error,
) {
	logger := logger.Get().Named("broker")

	logger.Info("Starting provision of service instance %s (service: %s, plan: %s)", instanceID, details.ServiceID, details.PlanID)
	logger.Debug("Provision details - OrganizationGUID: %s, SpaceGUID: %s", details.OrganizationGUID, details.SpaceGUID)

	// Check if async is allowed
	if !asyncAllowed {
		logger.Error("Async operations required but not allowed by client")

		return osbapi.ProvisionResponse{}, false, osbapi.ErrAsyncRequired
	}

	// Claim the instance ID before any write. A platform that retries while
	// the first request is still being accepted, or while its goroutine still
	// runs, must not start a second deploy of the same deployment, and a
	// provision must not run alongside a deprovision of the same ID.
	claim := &operationClaim{operation: operationTypeProvision, request: copyProvisionRequest(details), claimed: time.Now()}

	held, claimed := b.claimOperation(instanceID, claim)
	if !claimed {
		return b.answerProvisionWhileClaimed(instanceID, details, held, logger)
	}

	// Until the goroutine takes the claim over, Provision owns it and gives
	// it up on every way out, a panic included.
	handedOff := false

	defer func() {
		if !handedOff {
			b.releaseOperation(instanceID, claim)
		}
	}()

	// An instance the index already has is answered from its records, so a
	// repeat that arrives after the first provision's claim is gone never
	// starts a second deploy over it.
	response, async, answered, err := b.answerProvisionFromIndex(ctx, instanceID, details, logger)
	if answered || err != nil {
		return response, async, err
	}

	// Record the initial request
	err = b.recordInitialRequest(ctx, instanceID, details, logger)
	if err != nil {
		return osbapi.ProvisionResponse{}, false, err
	}

	// Record when this provision was requested before anything deploys, so a
	// delete request left over from an earlier instance with the same GUID is
	// never taken for this instance's.
	err = b.storeProvisionRequestedTimestamp(ctx, instanceID, logger)
	if err != nil {
		return osbapi.ProvisionResponse{}, false, err
	}

	// Find and validate plan
	plan, err := b.validatePlan(ctx, instanceID, details, logger)
	if err != nil {
		return osbapi.ProvisionResponse{}, false, err
	}

	// Store instance data
	_, err = b.storeInstanceData(ctx, instanceID, details, plan, logger)
	if err != nil {
		return osbapi.ProvisionResponse{}, false, err
	}

	// Launch async provisioning
	handedOff, err = b.launchAsyncProvisioning(ctx, instanceID, details, plan, claim, logger)
	if err != nil {
		return osbapi.ProvisionResponse{}, false, err
	}

	logger.Info("Accepted provisioning request for service instance %s", instanceID)

	return osbapi.ProvisionResponse{Operation: operationTypeProvision}, true, nil
}

func (b *Broker) Deprovision(
	ctx context.Context,
	instanceID string,
	details osbapi.DeprovisionRequest,
	asyncAllowed bool,
) (
	osbapi.DeprovisionResponse,
	bool,
	error,
) {
	logger := logger.Get().Named("broker")
	logger.Info("deprovisioning plan (%s) service (%s) instance (%s)", details.PlanID, details.ServiceID, instanceID)

	if !asyncAllowed {
		logger.Error("Async operations required but not allowed by client")

		return osbapi.DeprovisionResponse{}, false, osbapi.ErrAsyncRequired
	}

	// Claim the instance ID before anything is written to the vault, so a
	// refused request leaves no deprovision trace on the index entry, and a
	// provision or a second deprovision cannot start while this one is being
	// accepted.
	claim := &operationClaim{operation: operationTypeDeprovision, claimed: time.Now()}

	held, claimed := b.claimOperation(instanceID, claim)
	if !claimed {
		return b.answerDeprovisionWhileClaimed(instanceID, held, logger)
	}

	// Until the goroutine takes the claim over, Deprovision owns it and gives
	// it up on every way out, a panic included.
	handedOff := false

	defer func() {
		if !handedOff {
			b.releaseOperation(instanceID, claim)
		}
	}()

	err := b.rejectIfOperationInProgress(ctx, instanceID, logger)
	if err != nil {
		return osbapi.DeprovisionResponse{}, false, err
	}

	// An instance the index does not have is gone only when the director
	// confirms its deployment is, and then nothing is recorded for it.
	err = b.refuseDeleteOfGoneInstance(ctx, instanceID, details, logger)
	if err != nil {
		return osbapi.DeprovisionResponse{}, false, err
	}

	err = b.recordDeprovisionRequest(ctx, instanceID, details, logger)
	if err != nil {
		logger.Error("failed to record deprovision request in vault: %s", err)
		// Continue anyway, this is non-fatal for existing instances
	}

	instance, err := b.validateInstanceExists(ctx, instanceID, details, logger)
	if err != nil {
		return osbapi.DeprovisionResponse{}, false, err
	}

	// Record the deprovision as the instance's current operation before
	// answering, so a poll that arrives right after the 202 is answered for
	// this deprovision and not from the provision recorded before it.
	// The write runs detached from the request, because a request that ends
	// here would otherwise leave the deprovision running with no record of it.
	// The write is refused while the record holds an unknown outcome, which
	// may have been recorded since rejectIfOperationInProgress read it.
	trackCtx, cancelTrack := recordContext(ctx)
	err = b.Vault.TrackProgressUnlessOutcomeUnknown(trackCtx, instanceID, operationTypeDeprovision, "Deprovision request accepted", 0, nil)

	cancelTrack()

	if errors.Is(err, internalVault.ErrOutcomeUnknownRecorded) {
		logger.Warning("Rejecting deprovision of instance %s: %s", instanceID, err)

		return osbapi.DeprovisionResponse{}, false, osbapi.ErrConcurrencyError
	}

	if err != nil {
		logger.Error("failed to record the accepted deprovision in vault: %s", err)

		return osbapi.DeprovisionResponse{}, false, fmt.Errorf("failed to record the deprovision of instance %s: %w", instanceID, err)
	}

	b.storeDeleteRequestedTimestamp(ctx, instanceID, logger)
	b.descheduleShieldBackup(instanceID, details.ServiceID, details.PlanID, logger)

	// Launch async deprovisioning in background
	// The deprovision outlives the HTTP request, so it runs under the service
	// context with the monitor's bound. A shutdown ends it, and LastOperation
	// settles what the director task left behind.
	asyncCtx, cancelAsync := context.WithTimeout(b.serviceContext(), taskMonitorTimeout)

	// Nothing after this point can fail the request, so a repeated delete may
	// now be answered as accepted, and the goroutine takes the claim over.
	claim.launched.Store(true)

	handedOff = true

	go func() {
		// The goroutine gives the claim up on every return, a panic included.
		defer b.releaseOperation(instanceID, claim)
		defer cancelAsync()

		b.deprovisionAsync(asyncCtx, instanceID, instance)
	}()

	logger.Info("Accepted deprovisioning request for service instance %s", instanceID)

	return osbapi.DeprovisionResponse{Operation: operationTypeDeprovision}, true, nil
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

	// Notify vm-monitor to start monitoring this service. It watches the
	// deployment name the index entry records, as getInstanceDetails read it.
	if b.vmMonitor != nil {
		addErr := b.vmMonitor.AddService(ctx, instanceID, deployment)
		if addErr != nil {
			logger.Error("failed to add service %s to vm-monitor: %s", instanceID, addErr)
			// Continue anyway, this is non-fatal
		} else {
			logger.Debug("added service %s to vm-monitor for monitoring", instanceID)
		}
	}

	// Get deployment VMs
	vms, err := b.getDeploymentVMs(ctx, deployment, logger)
	if err != nil {
		return err
	}

	// Get and store credentials
	err = b.processCredentials(ctx, instanceID, details, logger)
	if err != nil {
		return err
	}

	// Schedule backup
	err = b.scheduleBackup(ctx, instanceID, details.ServiceID, details.PlanID, vms[0].IPs[0], logger)
	if err != nil {
		return err
	}

	logger.Debug("scheduling of S.H.I.E.L.D. backup for instance '%s' successfully completed", instanceID)

	return nil
}

func (b *Broker) LastOperation(
	ctx context.Context,
	instanceID string,
	details osbapi.LastOperationRequest,
) (
	osbapi.LastOperationResponse,
	error,
) {
	logger := logger.Get().Named("broker")
	logger.Debug("LastOperation called for instance %s", instanceID)

	// Get instance and deployment information
	instance, deploymentName, err := b.getInstanceForOperation(ctx, instanceID, logger)
	if err != nil {
		logger.Error("Error getting instance for operation: %s", err)

		return osbapi.LastOperationResponse{}, err
	}

	if instance == nil {
		return b.pollWithoutIndexEntry(ctx, instanceID, details, logger)
	}

	logger.Debug("Instance %s found, deployment: %s", instanceID, deploymentName)

	recorded, err := b.readRecordedOperation(ctx, instanceID)
	if err != nil {
		logger.Error("Error reading the recorded operation for instance %s: %s", instanceID, err)

		return osbapi.LastOperationResponse{}, err
	}

	operation := pollOperation(details.Operation, recorded)
	logger.Debug("Polling %s of instance %s (recorded action '%s', state '%s', task %d)", operation, instanceID, recorded.Action, recorded.State, recorded.TaskID)

	if operation == operationTypeDeprovision {
		return b.deprovisionStatus(ctx, instanceID, deploymentName, recorded, logger)
	}

	return b.provisionStatus(ctx, instanceID, deploymentName, recorded, logger)
}

func (b *Broker) Bind(
	ctx context.Context,
	instanceID, bindingID string,
	details osbapi.BindRequest,
	asyncAllowed bool,
) (
	osbapi.BindResponse,
	bool,
	error,
) {
	// Acquire lock for this instance to prevent concurrent bind/unbind
	mu := b.GetInstanceLock(instanceID)

	mu.Lock()
	defer mu.Unlock()

	var binding osbapi.BindResponse

	logger := logger.Get().Named("broker")
	logger.Info("Starting bind operation for instance %s, binding %s", instanceID, bindingID)
	logger.Debug("Bind details - Service: %s, Plan: %s, AppGUID: %s", details.ServiceID, details.PlanID, details.AppGUID)

	// Find the plan
	plan, err := b.findPlanForBinding(details, logger)
	if err != nil {
		return binding, false, err
	}

	// Get credentials
	creds, err := b.getInstanceCredentials(ctx, instanceID, plan, logger)
	if err != nil {
		return binding, false, err
	}

	binding, err = b.completeBind(ctx, instanceID, bindingID, creds, logger)
	if err != nil {
		return binding, false, err
	}

	logger.Info("Successfully completed bind operation for binding %s", bindingID)

	return binding, false, nil
}

// completeBind turns an instance's admin credentials into the credentials a
// binding returns. It creates the per-binding RabbitMQ or Valkey user when the
// service needs one, drops the admin fields, and logs the key names only.
func (b *Broker) completeBind(ctx context.Context, instanceID, bindingID string, creds interface{}, logger logger.Logger) (osbapi.BindResponse, error) {
	var binding osbapi.BindResponse

	// Process RabbitMQ dynamic credentials if applicable
	processedCreds, err := b.processRabbitMQCredentials(ctx, bindingID, creds, logger)
	if err != nil {
		return binding, err
	}

	// Process Valkey dynamic credentials if applicable
	processedCreds, err = b.processValkeyCredentials(ctx, instanceID, bindingID, processedCreds, logger)
	if err != nil {
		return binding, err
	}

	// Clean up admin credentials from final output
	b.cleanupAdminCredentials(processedCreds)

	if credsMap, ok := processedCreds.(map[string]interface{}); ok {
		binding.Credentials = credsMap
	}

	logBindingCredentialKeys(logger, bindingID, binding.Credentials)

	return binding, nil
}

func (b *Broker) Unbind(
	ctx context.Context,
	instanceID, bindingID string,
	details osbapi.UnbindRequest,
	asyncAllowed bool,
) (osbapi.UnbindResponse, bool, error) {
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
			return osbapi.UnbindResponse{}, false, err
		}
	}

	if strings.Contains(details.PlanID, "valkey") {
		err := b.handleValkeyUnbind(ctx, instanceID, bindingID, details, logger)
		if err != nil {
			return osbapi.UnbindResponse{}, false, err
		}
	}

	logger.Info("Successfully completed unbind operation for binding %s", bindingID)

	return osbapi.UnbindResponse{}, false, nil
}

func (b *Broker) Update(
	ctx context.Context,
	instanceID string,
	details osbapi.UpdateRequest,
	asyncAllowed bool,
) (
	osbapi.UpdateResponse,
	bool,
	error,
) {
	logger := logger.Get().Named("broker")
	logger.Error("Update operation not implemented")

	prevPlanID := ""
	if details.PreviousValues != nil {
		prevPlanID = details.PreviousValues.PlanID
	}
	logger.Debug("Update request - InstanceID: %s, ServiceID: %s, CurrentPlanID: %s, NewPlanID: %s",
		instanceID, details.ServiceID, details.PlanID, prevPlanID)

	// FIXME: implement this!

	return osbapi.UpdateResponse{}, false, ErrNotImplemented
}

func (b *Broker) GetInstance(ctx context.Context, instanceID string, details osbapi.FetchInstanceRequest) (osbapi.FetchInstanceResponse, error) {
	logger := logger.Get().Named("broker")
	logger.Debug("GetInstance called for instanceID: %s", instanceID)
	logger.Info("GetInstance operation not implemented")
	// Not implemented - return empty spec
	return osbapi.FetchInstanceResponse{}, ErrGetInstanceNotImplemented
}

func (b *Broker) GetBinding(ctx context.Context, instanceID, bindingID string, details osbapi.FetchBindingRequest) (osbapi.FetchBindingResponse, error) {
	logger := logger.Get().Named("broker")
	logger.Debug("GetBinding called for instanceID: %s, bindingID: %s", instanceID, bindingID)
	logger.Info("GetBinding operation not implemented")
	// Not implemented - return empty spec
	return osbapi.FetchBindingResponse{}, ErrGetBindingNotImplemented
}

func (b *Broker) GetBindingCredentials(ctx context.Context, instanceID, bindingID string) (*BindingCredentials, error) {
	logger := logger.Get().Named("broker")
	logger.Info("Starting credential reconstruction for instance %s, binding %s", instanceID, bindingID)

	// Check if credentials already exist in vault
	credPath := fmt.Sprintf("%s/bindings/%s/credentials", instanceID, bindingID)

	var existingCreds map[string]interface{}

	exists, err := b.Vault.Get(ctx, credPath, &existingCreds)
	if err != nil {
		// Rebuilding would create a second dynamic user and overwrite the
		// credentials already stored for this binding.
		logger.Error("Error checking for existing credentials: %s", err)

		return nil, fmt.Errorf("failed to read the credentials stored for binding %s: %w", bindingID, err)
	}

	if exists {
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

	creds, err := b.getBaseCredentials(ctx, instanceID, plan, logger)
	if err != nil {
		return nil, err
	}

	binding, credsMap, err := b.initializeBinding(creds, logger)
	if err != nil {
		return nil, err
	}

	err = b.processDynamicCredentials(ctx, instanceID, credsMap, bindingID, binding, logger)
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

func (b *Broker) LastBindingOperation(ctx context.Context, instanceID, bindingID string, details osbapi.LastOperationRequest) (osbapi.LastOperationResponse, error) {
	logger := logger.Get().Named("broker")
	logger.Debug("LastBindingOperation called for instanceID: %s, bindingID: %s", instanceID, bindingID)
	logger.Debug("Returning success immediately as async bindings are not supported")
	// Not implemented - return successful immediately since we don't support async bindings
	return osbapi.LastOperationResponse{State: osbapi.StateSucceeded}, nil
}

func (b *Broker) ServiceWithNoDeploymentCheck(ctx context.Context) (
	[]string,
	error,
) {
	logger := logger.Get().Named("broker")
	logger.Info("Starting check for orphaned service instances with no backing BOSH deployment")

	deploymentNames, err := b.getDeploymentNamesFromBOSH(ctx, logger)
	if err != nil {
		return nil, err
	}

	// An empty list is more likely a failed scan than an empty director, and
	// against it every entry would look orphaned, so nothing is removed. The
	// reconciler's orphan sweep skips an empty scan the same way.
	if len(deploymentNames) == 0 {
		logger.Warning("Orphan check skipped: the BOSH director listed no deployments, so no index entry is removed")

		return nil, nil
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

// claimOperation claims instanceID for claim's operation in one atomic step.
// It reports whether the claim was taken. When it was not, it returns the
// claim another operation holds, or nil when the stored value is not a claim.
func (b *Broker) claimOperation(instanceID string, claim *operationClaim) (*operationClaim, bool) {
	stored, loaded := b.activeOperations.LoadOrStore(instanceID, claim)
	if !loaded {
		return nil, true
	}

	held, ok := stored.(*operationClaim)
	if !ok {
		return nil, false
	}

	return held, false
}

// releaseOperation gives up claim on instanceID. It removes the stored claim
// only when it is this one, so it can never remove a claim another operation
// took after this one.
func (b *Broker) releaseOperation(instanceID string, claim *operationClaim) {
	b.activeOperations.CompareAndDelete(instanceID, claim)
}

// operationActive reports whether an operation of the given type holds the
// claim on instanceID, from the start of its accept chain until its goroutine
// returns.
func (b *Broker) operationActive(instanceID, operation string) bool {
	stored, found := b.activeOperations.Load(instanceID)
	if !found {
		return false
	}

	held, isClaim := stored.(*operationClaim)

	return isClaim && held.operation == operation
}

// answerProvisionWhileClaimed answers a provision request for an instance that
// another operation has claimed in this process. While a deprovision holds the
// claim, the request gets 422 ConcurrencyError before any write, because a
// provision that went ahead would move provision_requested_at past the delete
// request of the running deprovision, and that deprovision's credential
// cleanup would refuse to delete. While a provision holds it, a request whose
// attributes differ gets 409 Conflict. An identical request gets 202 Accepted
// with the first answer's operation once the provisionAsync goroutine has
// started, because the first request was then accepted. While the first
// request is still in its accept chain, its answer is not known yet, so an
// identical request gets 422 ConcurrencyError and the platform retries.
func (b *Broker) answerProvisionWhileClaimed(instanceID string, details osbapi.ProvisionRequest, held *operationClaim, logger logger.Logger) (osbapi.ProvisionResponse, bool, error) {
	switch {
	case held == nil:
		logger.Error("Rejecting provision of instance %s: its operation claim holds a value of an unexpected type", instanceID)

		return osbapi.ProvisionResponse{}, false, osbapi.ErrConcurrencyError
	case held.operation != operationTypeProvision:
		logger.Warning("Rejecting provision of instance %s: its %s has been in progress since %s", instanceID, held.operation, held.claimed.Format(time.RFC3339))

		return osbapi.ProvisionResponse{}, false, fmt.Errorf("%w: instance %s has a %s in progress", osbapi.ErrConcurrencyError, instanceID, held.operation)
	case !provisionRequestsMatch(held.request, details):
		logger.Warning("Rejecting provision of instance %s: a provision with different attributes has been in progress since %s", instanceID, held.claimed.Format(time.RFC3339))

		return osbapi.ProvisionResponse{}, false, fmt.Errorf("%w: instance %s is being provisioned with different attributes", osbapi.ErrInstanceAlreadyExists, instanceID)
	case !held.launched.Load():
		logger.Warning("Rejecting provision of instance %s: an identical provision has been in its accept chain since %s and its outcome is not known yet", instanceID, held.claimed.Format(time.RFC3339))

		return osbapi.ProvisionResponse{}, false, fmt.Errorf("%w: the provision of instance %s is still being accepted", osbapi.ErrConcurrencyError, instanceID)
	}

	logger.Info("Provision of instance %s is already in progress since %s; accepting the repeated request without starting another", instanceID, held.claimed.Format(time.RFC3339))

	return osbapi.ProvisionResponse{Operation: operationTypeProvision}, true, nil
}

// answerDeprovisionWhileClaimed answers a deprovision request for an instance
// that another operation has claimed in this process. While a provision holds
// the claim, the request gets 422 ConcurrencyError before any write, because a
// delete accepted then would tear down the vault index while the create
// deployment task keeps running. While a deprovision holds it, the request is
// answered 202 Accepted with the deprovision operation once the
// deprovisionAsync goroutine has started. While the first deprovision is still
// in its accept chain, its answer is not known yet, so the request gets 422
// ConcurrencyError and the platform retries.
func (b *Broker) answerDeprovisionWhileClaimed(instanceID string, held *operationClaim, logger logger.Logger) (osbapi.DeprovisionResponse, bool, error) {
	switch {
	case held == nil:
		logger.Error("Rejecting deprovision of instance %s: its operation claim holds a value of an unexpected type", instanceID)

		return osbapi.DeprovisionResponse{}, false, osbapi.ErrConcurrencyError
	case held.operation != operationTypeDeprovision:
		logger.Warning("Rejecting deprovision of instance %s: its %s has been in progress since %s", instanceID, held.operation, held.claimed.Format(time.RFC3339))

		return osbapi.DeprovisionResponse{}, false, fmt.Errorf("%w: instance %s has a %s in progress", osbapi.ErrConcurrencyError, instanceID, held.operation)
	case !held.launched.Load():
		logger.Warning("Rejecting deprovision of instance %s: an earlier deprovision has been in its accept chain since %s and its outcome is not known yet", instanceID, held.claimed.Format(time.RFC3339))

		return osbapi.DeprovisionResponse{}, false, fmt.Errorf("%w: the deprovision of instance %s is still being accepted", osbapi.ErrConcurrencyError, instanceID)
	}

	// A repeated request for a deprovision this process is still running is
	// accepted again rather than starting a second delete.
	logger.Info("Deprovision of instance %s is already in progress", instanceID)

	return osbapi.DeprovisionResponse{Operation: operationTypeDeprovision}, true, nil
}

// answerProvisionFromIndex answers a provision request for an instance the
// index already has, from the instance's records, and reports whether it
// answered. An instance with no index entry is not answered, and neither is
// one whose entry a refused accept chain left before the provision was
// accepted, once the director confirms that the deployment the provision
// would create does not exist. Those requests go on as new provisions. An
// index or record that cannot be read fails the request, because taking it
// for a missing instance could deploy over one that exists.
//
// An instance that is marked deleted, is being deprovisioned, or whose
// provision failed gets 409 Conflict, and nothing is deployed over it. Any
// other instance is compared with the request, which matches when its service,
// plan, organization, space, and parameters all equal the instance's. A match
// gets osbapi.ErrInstanceAlreadyProvisioned with an empty response when the
// provision has finished, which the OSB library sends as 200 OK with the body
// {}, and 202 Accepted with the provision
// operation while it runs, so the platform polls last_operation for the
// outcome. A difference gets 409 Conflict.
func (b *Broker) answerProvisionFromIndex(ctx context.Context, instanceID string, details osbapi.ProvisionRequest, logger logger.Logger) (osbapi.ProvisionResponse, bool, bool, error) {
	// The reads run under the bound the accept chain's records use.
	readCtx, cancel := recordContext(ctx)
	defer cancel()

	idx, err := b.Vault.GetIndex(readCtx, "db")
	if err != nil {
		logger.Error("Refusing provision of instance %s: could not read the index to check for an existing instance: %s", instanceID, err)

		return osbapi.ProvisionResponse{}, false, false, fmt.Errorf("could not read the index to check whether instance %s already exists, so the provision was refused: %w", instanceID, err)
	}

	raw, found := idx.Data[instanceID]
	if !found || raw == nil {
		return osbapi.ProvisionResponse{}, false, false, nil
	}

	entry := copyIndexEntry(raw)

	if isDeletedTombstone(entry) {
		logger.Warning("Refusing provision of instance %s: its index entry marks it deleted", instanceID)

		return osbapi.ProvisionResponse{}, false, true, fmt.Errorf("%w: instance %s was deleted, and a provision with the same ID is refused", osbapi.ErrInstanceAlreadyExists, instanceID)
	}

	recorded, err := b.readRecordedOperation(readCtx, instanceID)
	if err != nil {
		logger.Error("Refusing provision of instance %s: could not read its recorded operation: %s", instanceID, err)

		return osbapi.ProvisionResponse{}, false, false, fmt.Errorf("could not read the recorded operation of existing instance %s, so the provision was refused: %w", instanceID, err)
	}

	if !provisionAccepted(entry, recorded) {
		err = b.refuseProvisionOverDeployments(readCtx, instanceID, details, entry, logger)
		if err != nil {
			return osbapi.ProvisionResponse{}, false, errors.Is(err, osbapi.ErrInstanceAlreadyExists), err
		}

		logger.Info("Instance %s has an index entry from a provision that was never accepted; provisioning it as a new instance", instanceID)

		return osbapi.ProvisionResponse{}, false, false, nil
	}

	switch {
	case recorded.Action == operationTypeDeprovision:
		logger.Warning("Refusing provision of instance %s: it is being deprovisioned or its deprovision failed", instanceID)

		return osbapi.ProvisionResponse{}, false, true, fmt.Errorf("%w: instance %s has a deprovision recorded, and a provision with the same ID is refused", osbapi.ErrInstanceAlreadyExists, instanceID)
	case recorded.Action == operationTypeProvision && recorded.State == recordedStateFailed:
		logger.Warning("Refusing provision of instance %s: its provision failed (%s)", instanceID, recorded.Description)

		return osbapi.ProvisionResponse{}, false, true, fmt.Errorf("%w: the provision of instance %s failed, and it must be deleted before a provision with the same ID", osbapi.ErrInstanceAlreadyExists, instanceID)
	}

	difference, err := b.provisionDifference(readCtx, instanceID, entry, details)
	if err != nil {
		logger.Error("Refusing provision of instance %s: could not compare the request with the existing instance: %s", instanceID, err)

		return osbapi.ProvisionResponse{}, false, false, fmt.Errorf("could not compare the provision of instance %s with the existing instance, so it was refused: %w", instanceID, err)
	}

	if difference != "" {
		logger.Warning("Refusing provision of instance %s: the instance exists with attributes that differ from the request's (%s)", instanceID, difference)

		return osbapi.ProvisionResponse{}, false, true, fmt.Errorf("%w: instance %s exists with attributes that differ from the request's (%s)", osbapi.ErrInstanceAlreadyExists, instanceID, difference)
	}

	if createdAt, _ := entry["created_at"].(string); createdAt != "" {
		logger.Info("Instance %s was provisioned with the same attributes at %s; answering the repeat without deploying", instanceID, createdAt)

		return osbapi.ProvisionResponse{}, false, true, osbapi.ErrInstanceAlreadyProvisioned
	}

	logger.Info("Instance %s is being provisioned with the same attributes; accepting the repeat without starting another deploy", instanceID)

	return osbapi.ProvisionResponse{Operation: operationTypeProvision}, true, true, nil
}

// refuseProvisionOverDeployments runs refuseProvisionOverDeployment for the
// deployment the provision would create, named from the request's plan and
// the instance ID, and then for the entry's own deployment when that name
// differs. The entry's deployment is the one it records, or the one its plan
// ID and the instance ID name when it records none. Such a deployment, like
// one the reconciler adopted under another plan, would otherwise be left
// beside a second deployment. An entry with neither a name nor a plan ID
// names no deployment of its own.
func (b *Broker) refuseProvisionOverDeployments(ctx context.Context, instanceID string, details osbapi.ProvisionRequest, entry map[string]interface{}, logger logger.Logger) error {
	requested := details.PlanID + "-" + instanceID

	err := b.refuseProvisionOverDeployment(ctx, instanceID, requested, logger)
	if err != nil {
		return err
	}

	recordedName, _ := entry["deployment_name"].(string)
	entryPlanID, _ := entry["plan_id"].(string)

	if recordedName == "" && entryPlanID == "" {
		return nil
	}

	entryDeployment := indexEntryDeploymentName(instanceID, entry)
	if entryDeployment == requested {
		return nil
	}

	return b.refuseProvisionOverDeployment(ctx, instanceID, entryDeployment, logger)
}

// refuseProvisionOverDeployment asks the director for a deployment that a
// provision of an instance with a partial index entry would deploy over or
// beside. Such an entry records no accepted provision, but a deployment can
// still sit beside it, such as one the reconciler adopted. It returns nil
// only when the director answers with its own deployment-not-found code. A
// deployment that exists answers 409 Conflict, and any other answer fails the
// request, so nothing is deployed over or beside a deployment that may exist.
func (b *Broker) refuseProvisionOverDeployment(ctx context.Context, instanceID, deploymentName string, logger logger.Logger) error {
	_, err := b.BOSH.GetDeployment(ctx, deploymentName)

	switch {
	case err == nil:
		logger.Warning("Refusing provision of instance %s: its index entry records no accepted provision, but deployment %s exists", instanceID, deploymentName)

		return fmt.Errorf("%w: deployment %s of instance %s already exists, and a provision with the same ID is refused", osbapi.ErrInstanceAlreadyExists, deploymentName, instanceID)
	case errors.Is(err, bosh.ErrDeploymentNotFound):
		return nil
	default:
		logger.Error("Refusing provision of instance %s: the director could not confirm whether deployment %s exists: %s", instanceID, deploymentName, err)

		return fmt.Errorf("could not confirm whether deployment %s of instance %s exists, so the provision was refused: %w", deploymentName, instanceID, err)
	}
}

// provisionAccepted reports whether an index entry belongs to an instance the
// broker accepted. The accept chain records the provision as the instance's
// operation just before it answers 202, and an accepted deprovision replaces
// it, so an entry with either belongs to an instance. A created_at shows a
// provision that finished, whatever its record says. An entry with none of
// them was left by an accept chain that refused its request, or by a delete
// of an instance the broker never had.
func provisionAccepted(entry map[string]interface{}, recorded recordedOperation) bool {
	switch recorded.Action {
	case operationTypeProvision, operationTypeDeprovision:
		return true
	}

	created, _ := entry["created_at"].(string)

	return created != ""
}

// provisionDifference compares a provision request with the instance the
// index holds. It names the first attribute that differs, or returns "" when
// they match. The parameters are compared with the details stored at
// <instance>/<deployment> when the instance was provisioned, with an omitted
// object and an empty one treated alike. Stored details that are missing do
// not match any request, because a match cannot be shown.
func (b *Broker) provisionDifference(ctx context.Context, instanceID string, entry map[string]interface{}, details osbapi.ProvisionRequest) (string, error) {
	fields := []struct{ name, requested string }{
		{"service_id", details.ServiceID},
		{"plan_id", details.PlanID},
		{"organization_guid", details.OrganizationGUID},
		{"space_guid", details.SpaceGUID},
	}

	for _, field := range fields {
		stored, _ := entry[field.name].(string)
		if stored != field.requested {
			return field.name, nil
		}
	}

	deploymentName, _ := entry["deployment_name"].(string)
	if deploymentName == "" {
		deploymentName = details.PlanID + "-" + instanceID
	}

	var record map[string]interface{}

	exists, err := b.Vault.Get(ctx, instanceID+"/"+deploymentName, &record)
	if err != nil {
		return "", fmt.Errorf("failed to read the stored details of instance %s: %w", instanceID, err)
	}

	stored, ok := record["details"].(map[string]interface{})
	if !exists || !ok {
		return "parameters, because the stored details are missing", nil
	}

	storedParameters, _ := stored["parameters"].(map[string]interface{})

	same, err := sameJSONObjects(storedParameters, details.Parameters)
	if err != nil {
		return "", fmt.Errorf("failed to compare the parameters of instance %s: %w", instanceID, err)
	}

	if !same {
		return "parameters", nil
	}

	return "", nil
}

// sameJSONObjects reports whether two objects decoded from JSON hold the same
// values. Each is encoded and decoded again first, so a number decoded as a
// json.Number and one decoded as a float64 compare equal. A nil object equals
// an empty one.
func sameJSONObjects(first, second map[string]any) (bool, error) {
	if len(first) == 0 || len(second) == 0 {
		return len(first) == len(second), nil
	}

	canonical := func(object map[string]any) (any, error) {
		encoded, err := json.Marshal(object)
		if err != nil {
			return nil, fmt.Errorf("failed to encode an object: %w", err)
		}

		var decoded any

		err = json.Unmarshal(encoded, &decoded)
		if err != nil {
			return nil, fmt.Errorf("failed to decode an object: %w", err)
		}

		return decoded, nil
	}

	firstValue, err := canonical(first)
	if err != nil {
		return false, err
	}

	secondValue, err := canonical(second)
	if err != nil {
		return false, err
	}

	return reflect.DeepEqual(firstValue, secondValue), nil
}

// provisionRequestsMatch reports whether two provision requests carry the same
// attributes. A nil Context or Parameters map equals an empty one, because the
// Open Service Broker API treats an omitted object and an empty one alike.
func provisionRequestsMatch(first, second osbapi.ProvisionRequest) bool {
	return reflect.DeepEqual(normalizedProvisionRequest(first), normalizedProvisionRequest(second))
}

// normalizedProvisionRequest returns request with a nil Context or Parameters
// map replaced by an empty one.
func normalizedProvisionRequest(request osbapi.ProvisionRequest) osbapi.ProvisionRequest {
	if request.Context == nil {
		request.Context = map[string]any{}
	}

	if request.Parameters == nil {
		request.Parameters = map[string]any{}
	}

	return request
}

// copyProvisionRequest returns a copy of request that shares no map, slice, or
// pointer with it, so the copy is unaffected by later changes to the original.
func copyProvisionRequest(request osbapi.ProvisionRequest) osbapi.ProvisionRequest {
	request.Context = copyJSONMap(request.Context)
	request.Parameters = copyJSONMap(request.Parameters)

	if request.MaintenanceInfo != nil {
		info := *request.MaintenanceInfo
		request.MaintenanceInfo = &info
	}

	return request
}

// copyJSONMap returns a deep copy of a map decoded from JSON, or nil for a nil
// map.
func copyJSONMap(original map[string]any) map[string]any {
	if original == nil {
		return nil
	}

	copied := make(map[string]any, len(original))
	for key, value := range original {
		copied[key] = copyJSONValue(value)
	}

	return copied
}

// copyJSONValue returns a deep copy of a value decoded from JSON. Objects and
// arrays are copied element by element. Every other value a JSON decoder
// produces is a string, number, boolean, or nil, which is copied by value. A
// map[interface{}]interface{} from a YAML decoder is copied as well.
func copyJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return copyJSONMap(typed)
	case map[interface{}]interface{}:
		copied := make(map[interface{}]interface{}, len(typed))
		for key, element := range typed {
			copied[key] = copyJSONValue(element)
		}

		return copied
	case []any:
		if typed == nil {
			return typed
		}

		copied := make([]any, len(typed))
		for index, element := range typed {
			copied[index] = copyJSONValue(element)
		}

		return copied
	default:
		return value
	}
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

func (b *Broker) recordDeprovisionRequest(ctx context.Context, instanceID string, details osbapi.DeprovisionRequest, logger logger.Logger) error {
	logger.Debug("recording deprovision request in vault immediately")

	now := time.Now()

	baseData := map[string]interface{}{
		"service_id":               details.ServiceID,
		"plan_id":                  details.PlanID,
		"deprovision_requested_at": now.Format(time.RFC3339),
		"status":                   "deprovision_requested",
	}

	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	err := b.mergeIndexEntry(recordCtx, instanceID, baseData, nil)
	if err != nil {
		return fmt.Errorf("failed to record deprovision request in vault: %w", err)
	}

	return nil
}

// indexStatusRequestReceived is the status of an index entry whose provision
// was received and has not been validated yet.
const indexStatusRequestReceived = "request_received"

// mergeIndexEntry sets the fields of base, then the non-empty fields taken
// from contextData, on the instance's entry in the 'db' index, keeping every
// other field the entry holds. An instance that is not in the index gets a new
// entry. The read of the entry and the write of the index happen under one
// hold of the index lock, so a change another writer makes to the entry in
// between, such as the vm-monitor's deleted mark, is kept. A base that sets
// the status request_received starts a provision, so it also clears the
// fields a deleted mark left on the entry. A provision can start over an
// entry the vm-monitor marked after the provision found it live, and the
// live entry must not keep part of that mark. An index that
// cannot be read is an error and is not written, because merging into an
// empty entry would erase the fields the stored entry holds. The error is
// logged by the caller, which knows the step it stops.
func (b *Broker) mergeIndexEntry(ctx context.Context, instanceID string, base map[string]interface{}, contextData map[string]any) error {
	fields := make(map[string]interface{}, len(base))

	for key, value := range base {
		fields[key] = value
	}

	for key, value := range b.extractContextFields(contextData) {
		if value != "" {
			fields[key] = value
		}
	}

	err := b.Vault.UpdateIndex(ctx, func(entries map[string]interface{}) (bool, error) {
		entry := copyIndexEntry(entries[instanceID])

		for key, value := range fields {
			entry[key] = value
		}

		// The fields a deleted mark adds to an entry, on top of its status.
		if fields["status"] == indexStatusRequestReceived {
			for _, field := range []string{"deleted_at", "deleted_by", "deletion_reason"} {
				delete(entry, field)
			}
		}

		entries[instanceID] = entry

		return true, nil
	})
	if err != nil {
		return fmt.Errorf("failed to update the index entry for %s: %w", instanceID, err)
	}

	return nil
}

// getExistingIndexEntry returns a copy of the instance's entry in the 'db'
// index, for a caller that only reads it. An instance that is not in the index
// has an empty entry. An index that cannot be read is an error, which the
// caller logs. A caller that changes the entry uses mergeIndexEntry instead,
// so its read and its write happen under one hold of the index lock.
func (b *Broker) getExistingIndexEntry(ctx context.Context, instanceID string) (map[string]interface{}, error) {
	entry := make(map[string]interface{})

	idx, err := b.Vault.GetIndex(ctx, "db")
	if err != nil {
		return nil, fmt.Errorf("failed to read the index entry for %s: %w", instanceID, err)
	}

	raw, err := idx.Lookup(instanceID)
	if err != nil {
		return entry, nil //nolint:nilerr // not being in the index is the normal first write
	}

	return copyIndexEntry(raw), nil
}

// copyIndexEntry returns a copy of an index entry as a map, whatever form it
// was stored in. A missing entry, or one in a form that holds no fields,
// copies as an empty map.
func copyIndexEntry(raw interface{}) map[string]interface{} {
	entry := make(map[string]interface{})

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

func (b *Broker) extractContextFields(contextData map[string]any) map[string]string {
	if len(contextData) == 0 {
		return map[string]string{}
	}

	keys := []string{"organization_name", "space_name", "instance_name", "platform"}
	fields := make(map[string]string, len(keys))

	for _, key := range keys {
		if value, ok := contextData[key].(string); ok && strings.TrimSpace(value) != "" {
			fields[key] = value
		}
	}

	return fields
}

// validateInstanceExists returns the index entry of the instance a
// deprovision deletes. An instance with no entry, which is left when the
// deprovision request could not be recorded, answers 410 Gone only when the
// director confirms its deployment is gone. A deployment that still exists,
// or one whose existence is unknown, fails the request, so the platform
// retries the delete instead of taking the instance for gone.
func (b *Broker) validateInstanceExists(ctx context.Context, instanceID string, details osbapi.DeprovisionRequest, logger logger.Logger) (*vaultPkg.Instance, error) {
	// Deprovision looks the instance up after it has recorded the request, so
	// the lookup is detached as well, and a request that ends in between
	// does not leave a recorded request behind with no answer.
	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	instance, exists, err := b.Vault.FindInstance(recordCtx, instanceID)
	if err != nil {
		logger.Error("unable to retrieve instance details from vault index: %s", err)

		return nil, fmt.Errorf("failed to find instance in vault: %w", err)
	}

	if exists {
		return instance, nil
	}

	deploymentName := details.PlanID + "-" + instanceID

	err = b.deploymentGone(recordCtx, instanceID, deploymentName, logger)
	if err != nil {
		return nil, err
	}

	logger.Error("instance %s has no index entry, but the director still has deployment %s, so the delete was refused", instanceID, deploymentName)

	return nil, fmt.Errorf("%w: instance %s has no index entry, but deployment %s still exists, so the delete must be retried",
		errInstanceUnindexed, instanceID, deploymentName)
}

// errInstanceUnindexed marks a delete that found the instance's deployment
// but no index entry to delete it through.
var errInstanceUnindexed = errors.New("instance has no index entry")

// refuseDeleteOfGoneInstance answers a deprovision of an instance the index
// does not have. The instance is gone when the director confirms that the
// deployment named from the request's plan and the instance ID is gone, and
// the request then answers 410 Gone with nothing recorded. A deployment that
// still exists gets the running-task check that rejectIfOperationInProgress
// gives an indexed instance, and with no task running it is left to the
// deprovision, which records the request and deletes it. An indexed instance
// is left to the deprovision as well.
func (b *Broker) refuseDeleteOfGoneInstance(ctx context.Context, instanceID string, details osbapi.DeprovisionRequest, logger logger.Logger) error {
	readCtx, cancel := recordContext(ctx)
	defer cancel()

	_, exists, err := b.Vault.FindInstance(readCtx, instanceID)
	if err != nil {
		logger.Error("unable to look instance %s up in the vault index: %s", instanceID, err)

		return fmt.Errorf("failed to find instance in vault: %w", err)
	}

	if exists {
		return nil
	}

	deploymentName := details.PlanID + "-" + instanceID

	err = b.deploymentGone(readCtx, instanceID, deploymentName, logger)
	if err != nil {
		return err
	}

	err = b.rejectIfDeploymentBusy(readCtx, instanceID, deploymentName, logger)
	if err != nil {
		return err
	}

	logger.Info("instance %s has no index entry, but the director still has deployment %s, so the delete goes ahead", instanceID, deploymentName)

	return nil
}

// deploymentGone asks the director for the deployment of an instance the
// index does not have. It returns an error wrapping osbapi.ErrGoneError when
// the director answers with its own deployment-not-found code, and nil when
// the deployment exists. Any other answer leaves existence unknown, and the
// error it returns never wraps osbapi.ErrGoneError.
func (b *Broker) deploymentGone(ctx context.Context, instanceID, deploymentName string, logger logger.Logger) error {
	_, err := b.BOSH.GetDeployment(ctx, deploymentName)
	if err == nil {
		return nil
	}

	if errors.Is(err, bosh.ErrDeploymentNotFound) {
		logger.Info("instance %s has no index entry, and the director has no deployment %s, so the instance is gone", instanceID, deploymentName)

		return fmt.Errorf("%w: instance %s has no index entry, and deployment %s does not exist", osbapi.ErrGoneError, instanceID, deploymentName)
	}

	logger.Error("instance %s has no index entry, and the director could not confirm whether deployment %s exists: %s", instanceID, deploymentName, err)

	return fmt.Errorf("instance %s has no index entry, and whether deployment %s exists is unknown: %w", instanceID, deploymentName, err)
}

// rejectIfOperationInProgress returns the OSB concurrency error when BOSH
// still reports a queued or running task for the instance's deployment.
// Accepting a deprovision in that window would tear down the vault index while
// the create deployment task keeps running, which leaves an orphaned
// deployment behind. A provision still in flight in this process holds the
// instance's operation claim, so Deprovision has refused it already.
//
// It reads the instance's recorded operation first, whether or not the
// index has the instance, and returns the concurrency error while that
// operation's outcome is unknown, because a new delete would replace the
// record that holds the context ID and the unknown-since time. A record that
// cannot be read returns an error, because it may hold an unknown outcome.
//
// An instance the index does not have is checked by
// refuseDeleteOfGoneInstance, once the director confirms that the deployment
// named from the request's plan and the instance ID exists. A failed lookup is
// left to refuseDeleteOfGoneInstance as well, which reports it.
func (b *Broker) rejectIfOperationInProgress(ctx context.Context, instanceID string, logger logger.Logger) error {
	recorded, err := b.readRecordedOperation(ctx, instanceID)
	if err != nil {
		logger.Error("could not read the recorded operation of instance %s before accepting its deprovision: %s", instanceID, err)

		return err
	}

	if recorded.State == internalVault.TaskStateOutcomeUnknown {
		logger.Warning("Rejecting deprovision of instance %s: the outcome of its %s with context ID %s is unknown", instanceID, recorded.Action, recorded.ContextID)

		return osbapi.ErrConcurrencyError
	}

	instance, exists, err := b.Vault.FindInstance(ctx, instanceID)
	if err != nil || !exists {
		return nil //nolint:nilerr // refuseDeleteOfGoneInstance re-runs the lookup and surfaces the error
	}

	return b.rejectIfDeploymentBusy(ctx, instanceID, deploymentNameForInstance(instanceID, instance), logger)
}

// rejectIfDeploymentBusy returns the OSB concurrency error when BOSH still
// reports a queued or running task for deploymentName, and an error when the
// director cannot say whether it does.
func (b *Broker) rejectIfDeploymentBusy(ctx context.Context, instanceID, deploymentName string, logger logger.Logger) error {
	task, err := b.BOSH.FindRunningTaskForDeployment(ctx, deploymentName)
	if err != nil {
		logger.Error("could not determine whether deployment %s has a running BOSH task: %s", deploymentName, err)

		return fmt.Errorf("failed to check for a running BOSH task on deployment %s: %w", deploymentName, err)
	}

	if task != nil {
		logger.Warning("Rejecting deprovision of instance %s: BOSH task %d (%s) is still %s", instanceID, task.ID, task.Description, task.State)

		return osbapi.ErrConcurrencyError
	}

	return nil
}

// deploymentNameForInstance prefers the deployment name recorded at provision
// time. Deriving it from PlanID is only a fallback for instances indexed before
// that field existed, because the reconciler can rewrite PlanID afterwards.
func deploymentNameForInstance(instanceID string, instance *vaultPkg.Instance) string {
	if instance.DeploymentName != "" {
		return instance.DeploymentName
	}

	return instance.PlanID + "-" + instanceID
}

// storeProvisionRequestedTimestamp writes provision_requested_at into the
// instance metadata and keeps every other field, including a delete_requested_at
// from an earlier instance with the same GUID. The credential sweep compares
// that delete request with this time. Provision fails when the metadata cannot
// be written, because a deploy that goes ahead without the record leaves a stale
// delete request looking current.
func (b *Broker) storeProvisionRequestedTimestamp(ctx context.Context, instanceID string, logger logger.Logger) error {
	logger.Debug("storing provision_requested_at timestamp in Vault")

	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	var metadata map[string]interface{}

	exists, err := b.Vault.Get(recordCtx, instanceID+"/metadata", &metadata)
	if err != nil {
		logger.Error("failed to read %s/metadata before recording provision_requested_at: %s", instanceID, err)

		return fmt.Errorf("could not read %s/metadata to record when the provision was requested, so the provision was refused. Likely causes are an unreachable or sealed Vault or a token Vault no longer accepts: %w", instanceID, err)
	}

	if !exists || metadata == nil {
		metadata = make(map[string]interface{})
	}

	metadata["provision_requested_at"] = time.Now().Format(time.RFC3339)

	err = b.Vault.Put(recordCtx, instanceID+"/metadata", metadata)
	if err != nil {
		logger.Error("failed to store provision_requested_at in %s/metadata: %s", instanceID, err)

		return fmt.Errorf("could not write provision_requested_at to %s/metadata, so the provision was refused. Likely causes are an unreachable or sealed Vault or a token without write access to the secret path: %w", instanceID, err)
	}

	return nil
}

func (b *Broker) storeDeleteRequestedTimestamp(ctx context.Context, instanceID string, logger logger.Logger) {
	logger.Debug("storing delete_requested_at timestamp in Vault")

	deleteRequestedAt := time.Now()

	// The write must land even when the request has ended, because the
	// CredHub cleanup refuses to run for an instance with no delete request.
	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	// Get existing metadata
	var metadata map[string]interface{}

	exists, err := b.Vault.Get(recordCtx, instanceID+"/metadata", &metadata)
	if err != nil {
		// A fresh map written over metadata that could not be read would
		// erase the history and every timestamp already recorded.
		logger.Error("failed to read %s/metadata before recording delete_requested_at, leaving it unchanged: %s", instanceID, err)

		return
	}

	if !exists || metadata == nil {
		metadata = make(map[string]interface{})
	}

	// Add delete_requested_at
	metadata["delete_requested_at"] = deleteRequestedAt.Format(time.RFC3339)

	// Store updated metadata
	err = b.Vault.Put(recordCtx, instanceID+"/metadata", metadata)
	if err != nil {
		logger.Error("failed to store delete_requested_at timestamp: %s", err)
		// Continue anyway, this is non-fatal
	}
}

func (b *Broker) descheduleShieldBackup(instanceID, serviceID, planID string, logger logger.Logger) {
	logger.Debug("descheduling S.H.I.E.L.D. backup")

	err := b.Shield.DeleteSchedule(instanceID, serviceID, planID)
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

	// The dynamic user these credentials name already exists by now, so the
	// write runs detached from the request that asked for the binding.
	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	err := b.Vault.Put(recordCtx, path, credentials)
	if err != nil {
		logger.Error("Failed to store binding credentials for %s/%s: %s", instanceID, bindingID, err)

		return fmt.Errorf("failed to store binding credentials: %w", err)
	}

	return nil
}

// getDeploymentManifestFromVault reads the deployment manifest stored in Vault for the given instance.
// Returns the manifest YAML string, or empty string if not found (fallback to BOSH_NETWORK).
func (b *Broker) getDeploymentManifestFromVault(instanceID string, logger logger.Logger) string {
	var data map[string]interface{}

	exists, err := b.Vault.Get(context.Background(), instanceID+"/manifest", &data)
	if err != nil || !exists {
		logger.Debug("No manifest found in Vault for instance %s, will use default BOSH_NETWORK for DNS", instanceID)

		return ""
	}

	manifestYAML, ok := data["manifest"].(string)
	if !ok {
		logger.Debug("Manifest data in Vault for instance %s is not a string", instanceID)

		return ""
	}

	return manifestYAML
}

func (b *Broker) getBaseCredentials(ctx context.Context, instanceID string, plan services.Plan, logger logger.Logger) (interface{}, error) {
	logger.Debug("Retrieving base credentials using GetCreds")

	deploymentManifest := b.getDeploymentManifestFromVault(instanceID, logger)

	creds, err := manifest.GetCreds(ctx, instanceID, plan, b.BOSH, deploymentManifest, logger)
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

func (b *Broker) processDynamicCredentials(ctx context.Context, instanceID string, credsMap map[string]interface{}, bindingID string, binding *BindingCredentials, logger logger.Logger) error {
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

	if serviceType, ok := credsMap["service_type"].(string); ok && serviceType == "valkey" {
		logger.Info("Processing Valkey ACL user for binding", "bindingID", bindingID)

		err := b.handleDynamicValkeyCredentials(ctx, instanceID, credsMap, bindingID, logger)
		if err != nil {
			logger.Error("Failed to handle dynamic Valkey credentials", "error", err)

			return fmt.Errorf("failed to handle dynamic Valkey credentials: %w", err)
		}

		binding.CredentialType = "dynamic"

		logger.Debug("Successfully processed dynamic Valkey credentials")
	}

	return nil
}

func (b *Broker) getDeploymentNamesFromBOSH(ctx context.Context, logger logger.Logger) (map[string]bool, error) {
	logger.Debug("Fetching all current deployments from BOSH director")

	deployments, err := b.BOSH.GetDeployments(ctx)
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

// orphanCandidate is an index entry the orphan check found without a
// deployment, with the entry as it was checked.
type orphanCandidate struct {
	instanceID     string
	deploymentName string
	entry          interface{}
}

func (b *Broker) processOrphanedInstances(ctx context.Context, vaultDB *vaultPkg.Index, deploymentNames map[string]bool, logger logger.Logger) []string {
	logger.Debug("Checking each service instance for corresponding BOSH deployment")

	var candidates []orphanCandidate

	for instanceID, serviceInstance := range vaultDB.Data {
		logger.Debug("Checking instance: %s", instanceID)

		if isDeletedTombstone(serviceInstance) {
			logger.Debug("Instance %s is a deleted tombstone awaiting the reconciler sweep, skipping", instanceID)

			continue
		}

		deploymentName, err := b.validateAndGetDeploymentName(instanceID, serviceInstance, logger)
		if err != nil {
			continue // Error already logged
		}

		if !b.isOrphanedInstance(deploymentName, deploymentNames) {
			logger.Debug("Instance %s has valid BOSH deployment %s", instanceID, deploymentName)

			continue
		}

		reason := b.orphanInFlight(ctx, instanceID, serviceInstance)
		if reason != "" {
			logger.Info("Keeping instance %s with no BOSH deployment named %s, because %s", instanceID, deploymentName, reason)

			continue
		}

		reason = b.orphanDeploymentUnconfirmed(ctx, deploymentName)
		if reason != "" {
			logger.Info("Keeping instance %s missing from the BOSH deployment list, because %s", instanceID, reason)

			continue
		}

		candidates = append(candidates, orphanCandidate{instanceID: instanceID, deploymentName: deploymentName, entry: serviceInstance})
	}

	return b.removeOrphanedInstances(ctx, candidates, logger)
}

// orphanInFlight returns why an index entry with no deployment may belong to
// work in flight, or "" when nothing shows that it does, by the rule in
// inflight.Reason. An operation claim this process holds is checked when the
// entry is removed, under the index lock.
func (b *Broker) orphanInFlight(ctx context.Context, instanceID string, serviceInstance interface{}) string {
	entry, _ := serviceInstance.(map[string]interface{})

	return inflight.Reason(entry, func() (map[string]interface{}, bool, error) {
		readCtx, cancel := recordContext(ctx)
		defer cancel()

		var task map[string]interface{}

		exists, err := b.Vault.Get(readCtx, instanceID+"/task", &task)
		if err != nil {
			return nil, false, fmt.Errorf("read %s/task: %w", instanceID, err)
		}

		return task, exists, nil
	})
}

// orphanDeploymentUnconfirmed returns why the director has not confirmed that
// a deployment missing from its list is gone, or "" when it has. The list
// alone never proves a deployment gone, so the director is asked for the
// deployment itself, and only its deployment-not-found code counts. A
// deployment with a task still running on it is not gone either, because the
// task may be creating it. Any other answer leaves the deployment's state
// unknown, and the entry is kept. The reconciler's orphan sweep confirms a
// deployment gone the same way.
func (b *Broker) orphanDeploymentUnconfirmed(ctx context.Context, deploymentName string) string {
	_, err := b.BOSH.GetDeployment(ctx, deploymentName)
	if err == nil {
		return "the director still has deployment " + deploymentName
	}

	if !errors.Is(err, bosh.ErrDeploymentNotFound) {
		return fmt.Sprintf("the director could not confirm that deployment %s is gone (%s)", deploymentName, err)
	}

	task, err := b.BOSH.FindRunningTaskForDeployment(ctx, deploymentName)
	if err != nil {
		return fmt.Sprintf("the director could not say whether a task is running on deployment %s (%s)", deploymentName, err)
	}

	if task != nil {
		return fmt.Sprintf("BOSH task %d (%s) is %s on deployment %s", task.ID, task.Description, task.State, deploymentName)
	}

	return ""
}

// OperationClaimed reports whether an operation of any type holds the claim
// on instanceID in this process. The vm-monitor asks it before it marks an
// instance deleted.
func (b *Broker) OperationClaimed(instanceID string) bool {
	_, held := b.activeOperations.Load(instanceID)

	return held
}

// pollWithoutIndexEntry answers a poll for an instance the index does not
// have. A missing entry proves nothing on its own, because the orphan checks
// can remove the entry of a provision that is still running or that lost its
// deployment. A provision this process is still accepting answers in
// progress. Otherwise only the records that outlive the entry can show the
// instance was deleted, which are a deprovision in <instance>/task, settled
// by deprovisionWithoutIndexEntry, or a delete request in <instance>/metadata
// that follows the instance's creation. Every other poll answers failed, so
// the platform does not take a lost provision for a finished one. A read
// error returns an error, and the platform polls again. A provision poll
// that finds a deprovision record answers failed, because the instance was
// deleted while it was being provisioned. A poll that names no operation
// follows the record's action. When the record names no deployment, the
// deployment is named from the plan the poll names.
func (b *Broker) pollWithoutIndexEntry(ctx context.Context, instanceID string, details osbapi.LastOperationRequest, logger logger.Logger) (osbapi.LastOperationResponse, error) {
	if b.provisionActive(instanceID) {
		logger.Debug("instance %s has no index entry yet, and its provision is still being accepted", instanceID)

		return osbapi.LastOperationResponse{State: osbapi.StateInProgress, Description: "Provision request accepted"}, nil
	}

	readCtx, cancel := recordContext(ctx)
	defer cancel()

	recorded, err := b.readRecordedOperation(readCtx, instanceID)
	if err != nil {
		logger.Error("instance %s has no index entry, and its recorded operation could not be read: %s", instanceID, err)

		return osbapi.LastOperationResponse{}, err
	}

	if recorded.Action == operationTypeDeprovision {
		if details.Operation == operationTypeProvision {
			logger.Error("instance %s has no index entry, and a deprovision is recorded over the provision being polled, so the provision is reported as failed", instanceID)

			return osbapi.LastOperationResponse{
				State:       osbapi.StateFailed,
				Description: fmt.Sprintf("Service instance %s was deleted while it was being provisioned", instanceID),
			}, nil
		}

		return b.deprovisionWithoutIndexEntry(ctx, instanceID, details.PlanID, recorded, logger)
	}

	var metadata map[string]interface{}

	_, err = b.Vault.Get(readCtx, instanceID+"/metadata", &metadata)
	if err != nil {
		logger.Error("instance %s has no index entry, and %s/metadata could not be read: %s", instanceID, instanceID, err)

		return osbapi.LastOperationResponse{}, fmt.Errorf("failed to read the metadata of instance %s: %w", instanceID, err)
	}

	raw, _ := metadata["delete_requested_at"].(string)

	requestedAt, parseErr := time.Parse(time.RFC3339, raw)
	if raw != "" && parseErr == nil {
		followErr := b.checkRequestFollowsCreation(instanceID, metadata, raw, requestedAt)
		if followErr == nil {
			logger.Info("instance %s has no index entry, and a delete request is on record, so it has been deleted", instanceID)

			return osbapi.LastOperationResponse{State: osbapi.StateSucceeded, Description: descriptionInstanceDeleted}, nil
		}

		logger.Info("instance %s has no index entry, and its delete request does not count: %s", instanceID, followErr)
	}

	logger.Error("instance %s has no index entry and no record of a delete, so its operation is reported as failed", instanceID)

	return osbapi.LastOperationResponse{
		State:       osbapi.StateFailed,
		Description: fmt.Sprintf("Blacksmith has no record of service instance %s, so it cannot finish. Delete the service instance.", instanceID),
	}, nil
}

// deprovisionWithoutIndexEntry answers a poll for an instance whose index
// entry is gone and whose last recorded operation is a deprovision. A
// deprovision removes the entry only once the director has said the
// deployment is gone. A record without a context ID, which a deprovision
// leaves when the deployment was gone before its delete and which a
// broker from before context IDs left, answers succeeded. The teardown of
// a deployment that finished after its instance was deprovisioned runs
// with no entry at all, and its records carry the context ID of its
// delete, so a record with a context ID answers succeeded only when the
// director answers that the deployment does not exist. The deployment is
// the one the record names, and the one planID and the instance ID name
// when the record names none. When neither names one, it answers in
// progress without asking. A delete whose outcome is unknown is settled
// by resolveUnknownOutcome. A deployment that still exists answers failed
// when the record says the delete failed, and in progress otherwise.
func (b *Broker) deprovisionWithoutIndexEntry(ctx context.Context, instanceID, planID string, recorded recordedOperation, logger logger.Logger) (osbapi.LastOperationResponse, error) {
	if recorded.ContextID == "" {
		logger.Info("instance %s has no index entry, and its last recorded operation is a deprovision, so it has been deleted", instanceID)

		return osbapi.LastOperationResponse{State: osbapi.StateSucceeded, Description: descriptionInstanceDeleted}, nil
	}

	if recorded.DeploymentName == "" && planID == "" {
		logger.Warning("instance %s has no index entry, its record names no deployment, and the poll names no plan, so whether the delete with context ID %s removed its deployment cannot be checked", instanceID, recorded.ContextID)

		return osbapi.LastOperationResponse{
			State:       osbapi.StateInProgress,
			Description: fmt.Sprintf("The delete with context ID %s is not confirmed, and neither its record nor the poll names the deployment to check", recorded.ContextID),
		}, nil
	}

	deploymentName := deploymentNameForInstance(instanceID, &vaultPkg.Instance{PlanID: planID, DeploymentName: recorded.DeploymentName})

	if recorded.State == internalVault.TaskStateOutcomeUnknown {
		return b.resolveUnknownOutcome(ctx, instanceID, deploymentName, recorded, logger)
	}

	err := b.deploymentGone(ctx, instanceID, deploymentName, logger)

	switch {
	case errors.Is(err, osbapi.ErrGoneError):
		return osbapi.LastOperationResponse{State: osbapi.StateSucceeded, Description: descriptionInstanceDeleted}, nil
	case err != nil:
		return osbapi.LastOperationResponse{}, fmt.Errorf("failed to confirm the delete with context ID %s: %w", recorded.ContextID, err)
	case recorded.State == recordedStateFailed:
		logger.Error("instance %s has no index entry, and the delete of deployment %s with context ID %s failed: %s", instanceID, deploymentName, recorded.ContextID, recorded.Description)

		return osbapi.LastOperationResponse{State: osbapi.StateFailed, Description: recorded.Description}, nil
	}

	logger.Debug("instance %s has no index entry, and deployment %s is still there while its delete with context ID %s runs", instanceID, deploymentName, recorded.ContextID)

	return osbapi.LastOperationResponse{
		State:       osbapi.StateInProgress,
		Description: fmt.Sprintf("Deleting deployment %s with context ID %s: %s", deploymentName, recorded.ContextID, recorded.Description),
	}, nil
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

	deploymentName := indexEntryDeploymentName(instanceID, serviceData)
	logger.Debug("Looking for deployment: %s", deploymentName)

	return deploymentName, nil
}

// indexEntryDeploymentName returns the deployment name an index entry
// records, and builds it from the entry's plan ID and the instance ID only
// when the entry records none, as deploymentNameForInstance does for a
// decoded entry. The reconciler can rewrite the plan ID after the deployment
// was created, and a name built from it then names a deployment that does
// not exist.
func indexEntryDeploymentName(instanceID string, entry map[string]interface{}) string {
	if name, _ := entry["deployment_name"].(string); name != "" {
		return name
	}

	planID, _ := entry["plan_id"].(string)

	return planID + "-" + instanceID
}

// isDeletedTombstone reports whether an index entry is a tombstone left by the
// vm-monitor or the reconciler, through either the "deleted" status or the
// older boolean flag. Such an entry may carry no service or plan fields at
// all, so it is not an orphan candidate here; the reconciler's orphan sweep
// owns its removal.
func isDeletedTombstone(serviceInstance interface{}) bool {
	serviceData, valid := serviceInstance.(map[string]interface{})
	if !valid {
		return false
	}

	if status, ok := serviceData["status"].(string); ok && status == "deleted" {
		return true
	}

	deleted, ok := serviceData["deleted"].(bool)

	return ok && deleted
}

func (b *Broker) isOrphanedInstance(deploymentName string, deploymentNames map[string]bool) bool {
	_, exists := deploymentNames[deploymentName]

	return !exists
}

// removeOrphanedInstances removes the candidates from the index in one
// update, and returns the deployment names of the entries it removed. An
// entry that changed since it was checked, or that an operation has claimed
// since then, is kept, because it may now belong to work in flight.
func (b *Broker) removeOrphanedInstances(ctx context.Context, candidates []orphanCandidate, logger logger.Logger) []string {
	if len(candidates) == 0 {
		return nil
	}

	var removed []string

	err := b.Vault.UpdateIndex(ctx, func(entries map[string]interface{}) (bool, error) {
		removed = nil

		for _, candidate := range candidates {
			current, exists := entries[candidate.instanceID]
			if !exists {
				continue
			}

			if !reflect.DeepEqual(current, candidate.entry) {
				logger.Info("Keeping instance %s, because its index entry changed after the orphan check read it", candidate.instanceID)

				continue
			}

			if b.OperationClaimed(candidate.instanceID) {
				logger.Info("Keeping instance %s, because an operation claimed it during the orphan check", candidate.instanceID)

				continue
			}

			logger.Info("Removing orphaned service instance %s from vault DB, because no BOSH deployment named %s exists", candidate.instanceID, candidate.deploymentName)

			delete(entries, candidate.instanceID)

			removed = append(removed, candidate.deploymentName)
		}

		return len(removed) > 0, nil
	})
	if err != nil {
		logger.Error("Failed to remove orphaned service instances from vault DB: %s", err)

		return nil
	}

	return removed
}

// storeDeploymentNameInIndex records the BOSH deployment name in the 'db' index.
// LastOperation resolves an instance through that index, and the name recorded
// here is what keeps it from having to re-derive one from a mutable plan ID.
func (b *Broker) storeDeploymentNameInIndex(ctx context.Context, instanceID, deploymentName string, logger logger.Logger) {
	baseData := map[string]interface{}{
		"deployment_name": deploymentName,
	}

	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	err := b.mergeIndexEntry(recordCtx, instanceID, baseData, nil)
	if err != nil {
		// Non-fatal: LastOperation falls back to deriving the name from the
		// plan ID. A failed read leaves the entry in the index unchanged.
		logger.Error("failed to record deployment name in vault index, leaving the entry unchanged: %s", err)
	}
}

func (b *Broker) recordInitialRequest(ctx context.Context, instanceID string, details osbapi.ProvisionRequest, logger logger.Logger) error {
	logger.Debug("recording service request in vault immediately")

	now := time.Now()

	baseData := map[string]interface{}{
		"service_id":   details.ServiceID,
		"plan_id":      details.PlanID,
		"created":      now.Unix(), // Keep for backward compatibility
		"requested_at": now.Format(time.RFC3339),
		"status":       indexStatusRequestReceived,
	}

	if details.OrganizationGUID != "" {
		baseData["organization_guid"] = details.OrganizationGUID
	}

	if details.SpaceGUID != "" {
		baseData["space_guid"] = details.SpaceGUID
	}

	// The accept path writes its records detached from the request, so a
	// request that ends part way leaves complete records and not a stale
	// "request_received" entry that counts against the plan limit.
	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	err := b.mergeIndexEntry(recordCtx, instanceID, baseData, details.Context)
	if err != nil {
		logger.Error("failed to record service request in vault: %s", err)

		return ErrFailedToTrackServiceRequestInVault
	}

	logger.Debug("service request recorded in vault with status 'request_received'")

	return nil
}

func (b *Broker) validatePlan(ctx context.Context, instanceID string, details osbapi.ProvisionRequest, logger logger.Logger) (*services.Plan, error) {
	logger.Info("Looking up plan %s for service %s", details.PlanID, details.ServiceID)

	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		logger.Error("Failed to find plan %s/%s: %s", details.ServiceID, details.PlanID, err)

		return nil, err
	}

	logger.Debug("Found plan: %s (limit: %d)", plan.Name, plan.Limit)

	// Check service limits
	logger.Debug("retrieving vault 'db' index (for tracking service usage)")

	indexCtx, cancelIndex := recordContext(ctx)
	database, err := b.Vault.GetIndex(indexCtx, "db")

	cancelIndex()

	if err != nil {
		logger.Error("failed to get 'db' index out of the vault: %s", err)

		return nil, fmt.Errorf("failed to get vault index: %w", err)
	}

	logger.Info("Checking service limits for plan %s", plan.Name)

	if plan.OverLimit(database) {
		logger.Error("Service limit exceeded for %s/%s (limit: %d)", plan.Service.Name, plan.Name, plan.Limit)

		return nil, osbapi.ErrPlanQuotaExceeded
	}

	logger.Debug("Service limit check passed")

	// Update status to validated
	return &plan, b.updateInstanceStatus(ctx, instanceID, details, &plan, "validated", logger)
}

func (b *Broker) updateInstanceStatus(ctx context.Context, instanceID string, details osbapi.ProvisionRequest, plan *services.Plan, status string, logger logger.Logger) error {
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

	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	err := b.mergeIndexEntry(recordCtx, instanceID, baseData, details.Context)
	if err != nil {
		logger.Error("failed to update service status in vault index: %s", err)

		return ErrFailedToUpdateServiceStatusInVault
	}

	return nil
}

func (b *Broker) storeInstanceData(ctx context.Context, instanceID string, details osbapi.ProvisionRequest, plan *services.Plan, logger logger.Logger) (string, error) {
	deploymentName := fmt.Sprintf("%s-%s", details.PlanID, instanceID)
	logger.Debug("deployment name: %s", deploymentName)

	// Store deployment details
	err := b.storeDeploymentDetails(ctx, instanceID, deploymentName, details, logger)
	if err != nil {
		return "", err
	}

	// Store deployment info and root data
	b.storeDeploymentInfo(ctx, instanceID, deploymentName, details, logger)

	// Record the deployment name in the 'db' index as well. LastOperation reads
	// the index, and this is the only authoritative copy of the name there.
	b.storeDeploymentNameInIndex(ctx, instanceID, deploymentName, logger)

	// Write debug files
	b.writeDebugFiles(instanceID, details, logger)

	// Store plan references
	b.storePlanReferences(ctx, instanceID, plan, logger)

	return deploymentName, nil
}

func (b *Broker) storeDeploymentDetails(ctx context.Context, instanceID, deploymentName string, details osbapi.ProvisionRequest, logger logger.Logger) error {
	vaultPath := fmt.Sprintf("%s/%s", instanceID, deploymentName)
	logger.Debug("storing details at Vault path: %s", vaultPath)

	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	err := b.Vault.Put(recordCtx, vaultPath, map[string]interface{}{
		"details": details,
	})
	if err != nil {
		logger.Error("failed to store details in the vault at path %s: %s", vaultPath, err)

		// Remove from index since we're failing. The write may have failed
		// because recordCtx's deadline ran out, and the cleanup would fail
		// the same way on it, so it gets a bound of its own.
		cleanupCtx, cleanupCancel := recordContext(ctx)
		defer cleanupCancel()

		indexErr := b.Vault.Index(cleanupCtx, instanceID, nil)
		if indexErr != nil {
			logger.Error("failed to remove instance from index: %s", indexErr)
		}

		return ErrFailedToStoreServiceMetadata
	}

	return nil
}

func (b *Broker) storeDeploymentInfo(ctx context.Context, instanceID, deploymentName string, details osbapi.ProvisionRequest, logger logger.Logger) {
	deploymentInfo := b.buildDeploymentInfo(instanceID, deploymentName, details)

	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	// Store deployment info at instance level
	logger.Debug("storing deployment info at instance level: %s/deployment", instanceID)

	err := b.Vault.Put(recordCtx, instanceID+"/deployment", deploymentInfo)
	if err != nil {
		logger.Error("failed to store deployment info at instance level: %s", err)
		// Continue anyway, this is not fatal
	}

	// Store flattened data at root instance path for backward compatibility
	logger.Debug("storing flattened data at root instance path: %s", instanceID)

	rootData := b.buildRootData(deploymentInfo, details)

	err = b.Vault.Put(recordCtx, instanceID, rootData)
	if err != nil {
		logger.Error("failed to store data at root instance path: %s", err)
		// Continue anyway, this is not fatal
	}
}

func (b *Broker) buildDeploymentInfo(instanceID, deploymentName string, details osbapi.ProvisionRequest) map[string]interface{} {
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
	b.addContextData(deploymentInfo, details.Context)

	return deploymentInfo
}

func (b *Broker) buildRootData(deploymentInfo map[string]interface{}, details osbapi.ProvisionRequest) map[string]interface{} {
	rootData := make(map[string]interface{})
	for k, v := range deploymentInfo {
		rootData[k] = v
	}

	// Add context and parameters if present
	if len(details.Context) > 0 {
		rootData["context"] = details.Context
	}

	if len(details.Parameters) > 0 {
		rootData["parameters"] = details.Parameters
	}

	return rootData
}

func (b *Broker) addContextData(deploymentInfo map[string]interface{}, contextData map[string]any) {
	if len(contextData) == 0 {
		return
	}

	contextFields := []string{"organization_name", "space_name", "instance_name", "platform"}
	for _, field := range contextFields {
		if value, ok := contextData[field].(string); ok {
			deploymentInfo[field] = value
		}
	}
}

func (b *Broker) writeDebugFiles(instanceID string, details osbapi.ProvisionRequest, logger logger.Logger) {
	if len(details.Parameters) == 0 {
		return
	}

	paramBytes, err := json.Marshal(details.Parameters)
	if err != nil {
		logger.Error("failed to marshal parameters for debugging: %s", err)

		return
	}

	err = WriteDataFile(instanceID, paramBytes)
	if err != nil {
		logger.Error("failed to write data file for debugging: %s", err)
	}

	err = WriteYamlFile(instanceID, paramBytes)
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

// launchAsyncProvisioning records the accepted provision and starts the
// provisionAsync goroutine, which takes over the instance's claim in
// activeOperations and gives it up when it returns. It marks the claim as
// launched just before the goroutine starts, and reports whether it started.
func (b *Broker) launchAsyncProvisioning(ctx context.Context, instanceID string, details osbapi.ProvisionRequest, plan *services.Plan, claim *operationClaim, logger logger.Logger) (bool, error) {
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
		"parameters":        copyJSONMap(details.Parameters),
	}

	// Record the provision as the instance's current operation before
	// answering, so every poll finds the operation the broker accepted.
	trackCtx, cancelTrack := recordContext(ctx)
	err = b.Vault.TrackProgress(trackCtx, instanceID, operationTypeProvision, "Provision request accepted", 0, nil)

	cancelTrack()

	if err != nil {
		logger.Error("failed to record the accepted provision in vault: %s", err)

		return false, fmt.Errorf("failed to record the provision of instance %s: %w", instanceID, err)
	}

	// Launch async provisioning in background
	// The provision outlives the HTTP request, so it runs under the service
	// context with its own deadline instead of the request's.
	asyncCtx, cancelAsync := context.WithTimeout(b.serviceContext(), provisionTimeout)

	// Nothing after this point can fail the request, so an identical repeat
	// may now be answered as accepted.
	claim.launched.Store(true)

	go func() {
		// The claim Provision took stays in place while the goroutine runs,
		// so a deprovision or a repeated provision is answered from it. It
		// is given up on every return, a panic included.
		defer b.releaseOperation(instanceID, claim)
		defer cancelAsync()

		b.provisionAsync(asyncCtx, instanceID, detailsMap, *plan)
	}()

	return true, nil
}

func (b *Broker) updateInstanceTimestamp(ctx context.Context, instanceID string, logger logger.Logger) error {
	logger.Debug("updating instance with created_at timestamp")

	createdAt := time.Now()

	// OnProvisionCompleted runs under the poll's request context, and these
	// writes must land even when that request ends.
	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	// Get existing metadata to preserve history and other fields
	var metadata map[string]interface{}

	exists, err := b.Vault.Get(recordCtx, instanceID+"/metadata", &metadata)
	if err != nil {
		// Writing a fresh map would erase the history and timestamps the
		// metadata already holds, so the metadata is left alone. The index
		// does not depend on it, and CF stops polling once the provision
		// reports success, so created_at still goes into the index.
		readErr := fmt.Errorf("failed to read %s/metadata before recording created_at: %w", instanceID, err)

		// The read may have failed because it used up the bound of recordCtx,
		// so the index write takes a bound of its own.
		indexCtx, cancelIndex := recordContext(ctx)
		indexErr := b.updateIndexTimestamp(indexCtx, instanceID, createdAt, logger)

		cancelIndex()

		if indexErr != nil {
			logger.Error("failed to record created_at in the index after the metadata read failed: %s", indexErr)
		}

		return readErr
	}

	if !exists || metadata == nil {
		metadata = make(map[string]interface{})
	}

	// Add created_at to existing metadata
	metadata["created_at"] = createdAt.Format(time.RFC3339)

	// Store updated metadata
	err = b.Vault.Put(recordCtx, instanceID+"/metadata", metadata)
	if err != nil {
		logger.Error("failed to store created_at timestamp: %s", err)
		// Continue anyway, this is non-fatal
	}

	// Update the index with created_at
	return b.updateIndexTimestamp(recordCtx, instanceID, createdAt, logger)
}

// updateIndexTimestamp sets created_at on the instance's index entry. The
// lookup and the write happen under one hold of the index lock, so a change
// made to the entry meanwhile, such as the vm-monitor's deleted mark, is not
// saved over. An instance missing from the index is an error, and an entry
// that is not a map is left alone. A failed read or save of the index is
// returned for the caller to log.
func (b *Broker) updateIndexTimestamp(ctx context.Context, instanceID string, createdAt time.Time, logger logger.Logger) error {
	logger.Debug("updating index with created_at timestamp")

	err := b.Vault.UpdateIndex(ctx, func(entries map[string]interface{}) (bool, error) {
		raw, exists := entries[instanceID]
		if !exists {
			return false, fmt.Errorf("failed to lookup instance in index: %w: '%s'", vaultPkg.ErrKeyNotFoundInIndex, instanceID)
		}

		data, ok := raw.(map[string]interface{})
		if !ok {
			return false, nil
		}

		data["created_at"] = createdAt.Format(time.RFC3339)

		return true, nil
	})
	if err != nil {
		return fmt.Errorf("failed to record created_at in the index entry for %s: %w", instanceID, err)
	}

	return nil
}

func (b *Broker) getInstanceDetails(ctx context.Context, instanceID string, logger logger.Logger) (osbapi.ProvisionRequest, string, error) {
	logger.Debug("fetching instance provision details from Vault")

	// Get instance from index to construct deployment name
	instance, exists, err := b.Vault.FindInstance(ctx, instanceID)
	if err != nil || !exists {
		logger.Error("could not find instance in vault index: %s", err)

		return osbapi.ProvisionRequest{}, "", ErrCouldNotFindInstanceInVaultIndex
	}

	// The details are stored under the deployment's name, which the entry
	// records, so a plan ID the reconciler rewrote does not hide them.
	deploymentName := instance.DeploymentNameFor(instanceID)
	vaultPath := fmt.Sprintf("%s/%s", instanceID, deploymentName)

	// Get details from vault
	details, err := b.extractDetailsFromVault(ctx, vaultPath, instanceID, logger)
	if err != nil {
		return osbapi.ProvisionRequest{}, "", err
	}

	return details, deploymentName, nil
}

func (b *Broker) extractDetailsFromVault(ctx context.Context, vaultPath, instanceID string, logger logger.Logger) (osbapi.ProvisionRequest, error) {
	var detailsMetadata map[string]interface{}

	exists, err := b.Vault.Get(ctx, vaultPath, &detailsMetadata)
	if err != nil {
		// Try legacy path for backward compatibility
		logger.Debug("failed to fetch from new path, trying legacy path")

		exists, err = b.Vault.Get(ctx, instanceID, &detailsMetadata)
		if err != nil {
			logger.Error("failed to fetch instance metadata from Vault: %s", err)

			return osbapi.ProvisionRequest{}, fmt.Errorf("failed to fetch instance metadata from vault: %w", err)
		}
	}

	if !exists {
		return osbapi.ProvisionRequest{}, fmt.Errorf("%w (path: %s)", ErrCouldNotFindInstanceMetadataInVault, vaultPath)
	}

	return b.parseProvisionDetails(ctx, detailsMetadata, instanceID)
}

func (b *Broker) parseProvisionDetails(ctx context.Context, detailsMetadata map[string]interface{}, instanceID string) (osbapi.ProvisionRequest, error) {
	var details osbapi.ProvisionRequest

	if detailsData, ok := detailsMetadata["details"]; ok {
		// Round-trip through JSON to restore all fields from the stored map
		detailsMap, ok := detailsData.(map[string]interface{})
		if !ok {
			return osbapi.ProvisionRequest{}, fmt.Errorf("%w: unexpected details type %T", ErrCouldNotParseInstanceProvisionDetails, detailsData)
		}

		jsonBytes, err := json.Marshal(detailsMap)
		if err != nil {
			return osbapi.ProvisionRequest{}, fmt.Errorf("%w: marshal: %w", ErrCouldNotParseInstanceProvisionDetails, err)
		}

		err = json.Unmarshal(jsonBytes, &details)
		if err != nil {
			return osbapi.ProvisionRequest{}, fmt.Errorf("%w: unmarshal: %w", ErrCouldNotParseInstanceProvisionDetails, err)
		}
	} else {
		// Fallback: try to read as old format
		_, err := b.Vault.Get(ctx, instanceID, &details)
		if err != nil {
			return osbapi.ProvisionRequest{}, ErrCouldNotParseInstanceProvisionDetails
		}
	}

	return details, nil
}

func (b *Broker) getDeploymentVMs(ctx context.Context, deployment string, logger logger.Logger) ([]bosh.VM, error) {
	logger.Debug("fetching deployment VMs metadata")

	vms, err := b.BOSH.GetDeploymentVMs(ctx, deployment)
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

func (b *Broker) processCredentials(ctx context.Context, instanceID string, details osbapi.ProvisionRequest, logger logger.Logger) error {
	logger.Debug("fetching instance plan details")

	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		logger.Error("failed to find plan %s/%s: %s", details.ServiceID, details.PlanID, err)

		return err
	}

	logger.Debug("fetching instance credentials directly from BOSH")

	deploymentManifest := b.getDeploymentManifestFromVault(instanceID, logger)

	creds, err := manifest.GetCreds(ctx, instanceID, plan, b.BOSH, deploymentManifest, logger)
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

	deploymentManifest := b.getDeploymentManifestFromVault(instanceID, logger)

	creds2, err := manifest.GetCreds(ctx, instanceID, *plan, b.BOSH, deploymentManifest, logger)
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

func (b *Broker) scheduleBackup(ctx context.Context, instanceID, serviceID, planID, vmIP string, logger logger.Logger) error {
	logger.Debug("scheduling S.H.I.E.L.D. backup for instance '%s'", instanceID)

	// Get credentials for backup scheduling
	creds, err := b.getCredentialsForBackup(ctx, instanceID, logger)
	if err != nil {
		return err
	}

	err = b.Shield.CreateSchedule(instanceID, serviceID, planID, vmIP, creds)
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

	// Prefer the deployment name recorded at provision time. Deriving it from
	// PlanID is only a fallback for instances indexed before that field existed:
	// the reconciler can rewrite PlanID, and a plan ID that extends another one
	// (valkey-standalone vs valkey-standalone-classic) then yields the name of a
	// deployment that was never created, so LastOperation polls forever.
	deploymentName := deploymentNameForInstance(instanceID, instance)
	if instance.DeploymentName == "" {
		logger.Debug("no deployment_name recorded for instance %s; derived %s from plan ID", instanceID, deploymentName)
	}

	logger.Debug("checking deployment: %s", deploymentName)

	return instance, deploymentName, nil
}

// recordedOperation is the progress the broker keeps for an instance's
// current operation at <instance>/task: the action it accepted, the tracked
// state, the BOSH task it started once that is known, and a description.
type recordedOperation struct {
	Action      string
	State       string
	TaskID      int
	Description string
	// ContextID is the context ID the operation's director request
	// carries, recorded before the request was sent. It is empty for an
	// operation recorded before the broker chose context IDs.
	ContextID string
	// DeploymentName is the name of the deployment the operation's
	// director request acts on, recorded with ContextID. It is empty for
	// an operation that records none, such as a provision, or one recorded
	// before the broker recorded deployment names.
	DeploymentName string
	// UnknownSince is when the outcome of the operation's director request
	// became unknown. It is zero unless State is outcome_unknown.
	UnknownSince time.Time
}

// hasTask reports whether a real BOSH task ID was recorded. Zero means no task
// yet, and one is the placeholder the director adapter hands back when it has
// not found the task, so neither identifies a task the broker started.
func (r recordedOperation) hasTask() bool {
	return r.TaskID > placeholderTaskID
}

func (b *Broker) readRecordedOperation(ctx context.Context, instanceID string) (recordedOperation, error) {
	state, taskID, task, err := b.Vault.State(ctx, instanceID)
	if err != nil {
		return recordedOperation{}, fmt.Errorf("failed to read the recorded operation for instance %s: %w", instanceID, err)
	}

	recorded := recordedOperation{State: state, TaskID: taskID}
	recorded.Action, _ = task["action"].(string)
	recorded.Description, _ = task["description"].(string)
	recorded.ContextID, _ = task["context_id"].(string)
	recorded.DeploymentName, _ = task[internalVault.TaskRecordDeploymentNameKey].(string)

	if since, isString := task[internalVault.TaskRecordUnknownSinceKey].(string); isString {
		recorded.UnknownSince, err = time.Parse(time.RFC3339Nano, since)
		if err != nil {
			return recordedOperation{}, fmt.Errorf("the recorded operation for instance %s has an unknown-since time %q that is not RFC 3339: %w", instanceID, since, err)
		}
	}

	return recorded, nil
}

// pollOperation picks the operation a last_operation poll asks about. The
// operation data the broker returned when it accepted the request wins. A poll
// without it, such as one for a request accepted before the broker returned
// operation data, falls back to the action the broker last recorded.
func pollOperation(requested string, recorded recordedOperation) string {
	switch requested {
	case operationTypeProvision, operationTypeDeprovision:
		return requested
	}

	if recorded.Action == operationTypeDeprovision {
		return operationTypeDeprovision
	}

	return operationTypeProvision
}

// provisionStatus answers a provision poll from the provision the broker
// recorded. Only the create deployment task the broker started can finish it,
// so a task someone runs by hand on the deployment can neither complete the
// provision early nor hide a failed deploy. A deploy whose answer was lost
// is settled by resolveUnknownOutcome.
func (b *Broker) provisionStatus(ctx context.Context, instanceID, deploymentName string, recorded recordedOperation, logger logger.Logger) (osbapi.LastOperationResponse, error) {
	if recorded.Action != operationTypeProvision {
		recorded = recordedOperation{}
	}

	switch {
	case recorded.State == recordedStateFailed:
		logger.Error("provision of instance %s failed: %s", instanceID, recorded.Description)

		return osbapi.LastOperationResponse{State: osbapi.StateFailed, Description: recorded.Description}, nil
	case recorded.State == internalVault.TaskStateOutcomeUnknown:
		return b.resolveUnknownOutcome(ctx, instanceID, deploymentName, recorded, logger)
	case recorded.hasTask():
		return b.handleProvisionTask(ctx, instanceID, deploymentName, recorded.TaskID, logger)
	case b.provisionActive(instanceID):
		logger.Debug("provision of instance %s has not handed a task to BOSH yet", instanceID)

		return osbapi.LastOperationResponse{State: osbapi.StateInProgress, Description: recorded.Description}, nil
	}

	return b.recoverProvision(ctx, instanceID, deploymentName, logger)
}

// recoverProvision answers for a provision whose goroutine is gone without
// having recorded a create deployment task, which happens when the broker
// restarts while bosh-cli waits on the deploy. BOSH records the deploy as a
// deployment-level "create" event, so that event names the task the broker
// started. Once found, the task is recorded so later polls use it directly.
func (b *Broker) recoverProvision(ctx context.Context, instanceID, deploymentName string, logger logger.Logger) (osbapi.LastOperationResponse, error) {
	taskID, err := b.findDeploymentEventTask(ctx, deploymentName, boshEventActionCreate, logger)
	if err != nil {
		return osbapi.LastOperationResponse{}, err
	}

	if taskID > 0 {
		logger.Info("Recovered create deployment task %d for instance %s from the events of deployment %s", taskID, instanceID, deploymentName)
		b.recordTask(ctx, instanceID, operationTypeProvision, fmt.Sprintf("BOSH deployment in progress (task %d)", taskID), taskID, logger)

		return b.handleProvisionTask(ctx, instanceID, deploymentName, taskID, logger)
	}

	return b.interruptedOperationStatus(ctx, instanceID, deploymentName, "Provisioning was interrupted before the deployment was handed to BOSH", logger)
}

// deprovisionStatus answers a deprovision poll from the deprovision the broker
// recorded. It finishes only on the delete deployment task the broker started,
// or on the director's word that the deployment does not exist, and it never
// runs the provision completion hook. A delete whose answer was lost is
// settled by resolveUnknownOutcome.
func (b *Broker) deprovisionStatus(ctx context.Context, instanceID, deploymentName string, recorded recordedOperation, logger logger.Logger) (osbapi.LastOperationResponse, error) {
	if recorded.Action != operationTypeDeprovision {
		recorded = recordedOperation{}
	}

	switch {
	case recorded.State == recordedStateFailed:
		logger.Error("deprovision of instance %s failed: %s", instanceID, recorded.Description)

		return osbapi.LastOperationResponse{State: osbapi.StateFailed, Description: recorded.Description}, nil
	case recorded.State == internalVault.TaskStateOutcomeUnknown:
		return b.resolveUnknownOutcome(ctx, instanceID, deploymentName, recorded, logger)
	case recorded.hasTask():
		return b.handleDeprovisionTask(ctx, instanceID, deploymentName, recorded.TaskID, logger)
	case b.deprovisionActive(instanceID):
		logger.Debug("deprovision of instance %s has no delete task recorded yet", instanceID)

		return osbapi.LastOperationResponse{State: osbapi.StateInProgress, Description: recorded.Description}, nil
	}

	return b.recoverDeprovision(ctx, instanceID, deploymentName, logger)
}

// recoverDeprovision answers for a deprovision whose goroutine is gone without
// having recorded a delete task, which happens when the broker restarts while
// bosh-cli waits on the delete. BOSH records the delete as a deployment-level
// "delete" event, so that event names the task the broker started. Without
// one, a deployment the director no longer has needs nothing more deleted.
func (b *Broker) recoverDeprovision(ctx context.Context, instanceID, deploymentName string, logger logger.Logger) (osbapi.LastOperationResponse, error) {
	taskID, err := b.findDeploymentEventTask(ctx, deploymentName, boshEventActionDelete, logger)
	if err != nil {
		return osbapi.LastOperationResponse{}, err
	}

	if taskID > 0 {
		logger.Info("Recovered delete deployment task %d for instance %s from the events of deployment %s", taskID, instanceID, deploymentName)
		b.recordTask(ctx, instanceID, operationTypeDeprovision, fmt.Sprintf("BOSH deletion in progress (task %d)", taskID), taskID, logger)

		return b.handleDeprovisionTask(ctx, instanceID, deploymentName, taskID, logger)
	}

	_, err = b.BOSH.GetDeployment(ctx, deploymentName)
	if errors.Is(err, bosh.ErrDeploymentNotFound) {
		logger.Info("Deployment %s is gone and no delete task was recorded for instance %s", deploymentName, instanceID)

		return b.handleSuccessfulDeprovision(ctx, instanceID, deploymentName, logger)
	}

	if err != nil {
		logger.Error("could not check whether deployment %s exists: %s", deploymentName, err)

		return osbapi.LastOperationResponse{}, fmt.Errorf("failed to check whether deployment %s exists: %w", deploymentName, err)
	}

	return b.interruptedOperationStatus(ctx, instanceID, deploymentName, "Deprovisioning was interrupted before the deployment delete started; the deployment still exists, so the delete can be retried", logger)
}

// interruptedOperationStatus answers for an operation that no goroutine in
// this process is running and that left no BOSH task behind. A task still
// running on the deployment may yet be the one the broker started, so that
// keeps the answer in progress; otherwise nothing will finish the operation.
func (b *Broker) interruptedOperationStatus(ctx context.Context, instanceID, deploymentName, description string, logger logger.Logger) (osbapi.LastOperationResponse, error) {
	running, err := b.BOSH.FindRunningTaskForDeployment(ctx, deploymentName)
	if err != nil {
		logger.Error("could not determine whether deployment %s has a running BOSH task: %s", deploymentName, err)

		return osbapi.LastOperationResponse{}, fmt.Errorf("failed to check for a running BOSH task on deployment %s: %w", deploymentName, err)
	}

	if running != nil {
		logger.Debug("instance %s has no recorded task, but BOSH task %d (%s) is still %s", instanceID, running.ID, running.Description, running.State)

		return osbapi.LastOperationResponse{State: osbapi.StateInProgress}, nil
	}

	logger.Error("%s (instance %s, deployment %s)", description, instanceID, deploymentName)

	return osbapi.LastOperationResponse{State: osbapi.StateFailed, Description: description}, nil
}

// findDeploymentEventTask returns the newest task that BOSH recorded a
// deployment-level event with the given action for, or zero when there is
// none. Unlike the newest task overall, this cannot be a task someone ran by
// hand on the deployment, such as ssh, restart, recreate, or cck.
func (b *Broker) findDeploymentEventTask(ctx context.Context, deploymentName, action string, logger logger.Logger) (int, error) {
	events, err := b.BOSH.GetEvents(ctx, deploymentName)
	if err != nil {
		logger.Error("failed to get events for deployment %s: %s", deploymentName, err)

		return 0, fmt.Errorf("failed to get events for deployment %s: %w", deploymentName, err)
	}

	var taskID int

	for _, event := range events {
		if event.ObjectType != boshEventObjectDeployment || event.Action != action {
			continue
		}

		eventTaskID, err := strconv.Atoi(event.TaskID)
		if err != nil {
			continue
		}

		if eventTaskID > taskID {
			taskID = eventTaskID
		}
	}

	return taskID, nil
}

// resolveDeploymentTask returns the task named by the deployment-level event
// with the given action, or zero when the events cannot be read or name none.
func (b *Broker) resolveDeploymentTask(ctx context.Context, deploymentName, action string, logger logger.Logger) int {
	taskID, err := b.findDeploymentEventTask(ctx, deploymentName, action, logger)
	if err != nil {
		logger.Info("Could not look up the %s deployment task of %s, LastOperation will look again: %s", action, deploymentName, err)

		return 0
	}

	if taskID == 0 {
		logger.Info("No %s deployment event names a task for %s yet, LastOperation will look again", action, deploymentName)
	}

	return taskID
}

// recordTask stores a BOSH task ID for the instance's current operation.
func (b *Broker) recordTask(ctx context.Context, instanceID, operation, description string, taskID int, logger logger.Logger) {
	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	err := b.Vault.TrackProgress(recordCtx, instanceID, operation, description, taskID, nil)
	if err != nil {
		logger.Error("failed to record task %d for instance %s: %s", taskID, instanceID, err)
	}
}

func (b *Broker) provisionActive(instanceID string) bool {
	return b.operationActive(instanceID, operationTypeProvision)
}

func (b *Broker) deprovisionActive(instanceID string) bool {
	return b.operationActive(instanceID, operationTypeDeprovision)
}

// isFailedTaskState reports whether a BOSH task ended without succeeding.
func isFailedTaskState(state string) bool {
	return state == taskStateError || state == taskStateCancelled || state == taskStateTimeout
}

func (b *Broker) handleProvisionTask(ctx context.Context, instanceID, deploymentName string, taskID int, logger logger.Logger) (osbapi.LastOperationResponse, error) {
	logger.Debug("retrieving task %d from BOSH director", taskID)

	task, err := b.BOSH.GetTask(ctx, taskID)
	if err != nil {
		logger.Error("failed to retrieve task %d from BOSH director: %s", taskID, err)

		return osbapi.LastOperationResponse{}, ErrUnrecognizedBackendBOSHTask
	}

	switch {
	case task.State == taskStateDone:
		err := b.OnProvisionCompleted(ctx, logger, instanceID)
		if err != nil {
			return osbapi.LastOperationResponse{}, ErrProvisionTaskCompletedPostHookFailed
		}

		logger.Debug("provision operation succeeded")

		return osbapi.LastOperationResponse{State: osbapi.StateSucceeded}, nil

	case isFailedTaskState(task.State):
		description := fmt.Sprintf("BOSH deployment task %d is %s", taskID, task.State)
		logger.Error("provision operation failed: %s", description)

		// Record the failure so later polls answer from it and the cleanup
		// starts only once. bosh-cli waits for the delete task to finish, so
		// the cleanup runs in the background rather than holding up the poll.
		b.recordTask(ctx, instanceID, operationTypeProvision, description, -1, logger)

		// The cleanup outlives this poll; it keeps the poll's values and sets
		// its own deadline.
		go b.cleanupFailedDeployment(ctx, deploymentName, logger)

		return osbapi.LastOperationResponse{State: osbapi.StateFailed, Description: description}, nil

	default:
		logger.Debug("provision operation is still in progress")

		return osbapi.LastOperationResponse{State: osbapi.StateInProgress, Description: fmt.Sprintf("BOSH deployment in progress (task %d)", taskID)}, nil
	}
}

func (b *Broker) handleDeprovisionTask(ctx context.Context, instanceID, deploymentName string, taskID int, logger logger.Logger) (osbapi.LastOperationResponse, error) {
	logger.Debug("retrieving task %d from BOSH director", taskID)

	task, err := b.BOSH.GetTask(ctx, taskID)
	if err != nil {
		logger.Error("failed to retrieve task %d from BOSH director: %s", taskID, err)

		return osbapi.LastOperationResponse{}, ErrUnrecognizedBackendBOSHTask
	}

	switch {
	case task.State == taskStateDone:
		return b.handleSuccessfulDeprovision(ctx, instanceID, deploymentName, logger)
	case isFailedTaskState(task.State):
		logger.Error("deprovision operation failed: BOSH task %d is %s", taskID, task.State)

		return b.handleFailedDeprovision(ctx, deploymentName, logger)
	default:
		logger.Debug("deprovision operation is still in progress")

		return osbapi.LastOperationResponse{State: osbapi.StateInProgress, Description: fmt.Sprintf("BOSH deletion in progress (task %d)", taskID)}, nil
	}
}

// cleanupFailedDeployment deletes a failed deployment on a best-effort basis.
// It keeps the values of ctx but not its cancellation, and gives up after
// failedDeploymentCleanupTimeout.
func (b *Broker) cleanupFailedDeployment(ctx context.Context, deploymentName string, logger logger.Logger) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), failedDeploymentCleanupTimeout)
	defer cancel()

	logger.Debug("checking if failed deployment exists and needs cleanup")

	_, deploymentErr := b.BOSH.GetDeployment(ctx, deploymentName)
	if deploymentErr == nil {
		logger.Info("Failed deployment %s still exists, attempting cleanup", deploymentName)
		// Attempt to delete the failed deployment (best effort)
		cleanupTask, cleanupErr := b.BOSH.DeleteDeployment(ctx, deploymentName)
		if cleanupErr != nil {
			logger.Error("Failed to initiate cleanup of failed deployment %s: %s", deploymentName, cleanupErr)
		} else {
			logger.Info("Initiated cleanup of failed deployment %s (task %d)", deploymentName, cleanupTask.ID)
		}
	}
}

func (b *Broker) handleSuccessfulDeprovision(ctx context.Context, instanceID, deploymentName string, logger logger.Logger) (osbapi.LastOperationResponse, error) {
	logger.Info("handleSuccessfulDeprovision called for instance %s, deployment %s", instanceID, deploymentName)

	logger.Debug("Verifying deployment is actually deleted")

	_, deploymentErr := b.BOSH.GetDeployment(ctx, deploymentName)
	if deploymentErr == nil {
		logger.Error("Deprovision task succeeded but deployment %s still exists", deploymentName)

		return osbapi.LastOperationResponse{State: osbapi.StateFailed, Description: fmt.Sprintf("The delete task finished but deployment %s still exists", deploymentName)}, nil
	}

	// Only the director's own "not found" confirms the delete. Any other
	// error leaves existence unknown, so the instance stays indexed and the
	// platform polls again.
	if !errors.Is(deploymentErr, bosh.ErrDeploymentNotFound) {
		logger.Error("could not confirm that deployment %s is deleted: %s", deploymentName, deploymentErr)

		return osbapi.LastOperationResponse{}, fmt.Errorf("failed to confirm that deployment %s is deleted: %w", deploymentName, deploymentErr)
	}

	logger.Info("Deployment %s confirmed deleted", deploymentName)

	b.cleanupDeploymentCredentials(ctx, instanceID, deploymentName, logger)

	// Store deleted_at timestamp
	logger.Debug("Storing deleted_at timestamp")
	b.storeDeletedTimestamp(ctx, instanceID, logger)

	// Remove from index after confirmed deletion. The write runs detached
	// from the poll, so a poll that ends here does not leave the entry behind.
	logger.Info("Removing instance %s from service index after confirmed deletion", instanceID)

	removeCtx, cancelRemove := recordContext(ctx)
	err := b.Vault.Index(removeCtx, instanceID, nil)

	cancelRemove()

	if err != nil {
		// Answering succeeded now would tell the platform the instance is gone
		// while it is still indexed. In progress makes the next poll confirm
		// the deployment again and retry the removal.
		logger.Error("Failed to remove service from vault index: %s", err)

		return osbapi.LastOperationResponse{State: osbapi.StateInProgress, Description: "Deleting the instance's record"}, nil
	}

	logger.Info("Successfully removed instance %s from index", instanceID)

	logger.Debug("keeping secrets in vault for audit purposes")
	// Note: We intentionally do NOT call b.Vault.Clear(instanceID) here
	// to preserve secrets for auditing purposes
	return osbapi.LastOperationResponse{State: osbapi.StateSucceeded}, nil
}

func (b *Broker) handleFailedDeprovision(ctx context.Context, deploymentName string, logger logger.Logger) (osbapi.LastOperationResponse, error) {
	logger.Error("deprovision operation failed!")

	// Check if deployment still exists after failed deletion
	description := "The BOSH delete task failed"

	_, deploymentErr := b.BOSH.GetDeployment(ctx, deploymentName)
	if deploymentErr == nil {
		logger.Error("Deployment %s still exists after failed deletion task", deploymentName)

		description = fmt.Sprintf("The BOSH delete task failed and deployment %s still exists", deploymentName)
	} else {
		logger.Info("Deployment %s does not exist despite failed deletion task", deploymentName)
	}

	logger.Debug("keeping instance in index and secrets in vault due to deletion failure")
	// Do NOT remove from index if deletion failed - instance may still be recoverable
	return osbapi.LastOperationResponse{State: osbapi.StateFailed, Description: description}, nil
}

func (b *Broker) storeDeletedTimestamp(ctx context.Context, instanceID string, logger logger.Logger) {
	logger.Debug("storing deleted_at timestamp in Vault")

	deletedAt := time.Now()

	// The metadata read, the index read, and the metadata write each run
	// under a bound of their own, so a stalled read cannot use up the time
	// the deleted_at write needs.
	readCtx, cancelRead := recordContext(ctx)

	// Get existing metadata
	var metadata map[string]interface{}

	exists, err := b.Vault.Get(readCtx, instanceID+"/metadata", &metadata)

	cancelRead()

	if err != nil {
		// A fresh map written over metadata that could not be read would
		// erase the history and every timestamp already recorded.
		logger.Error("failed to read %s/metadata before recording deleted_at, leaving it unchanged: %s", instanceID, err)

		return
	}

	if !exists || metadata == nil {
		metadata = make(map[string]interface{})
	}

	// A poll that comes back after a failed index removal reaches this point
	// again. The first deleted_at is the time the deployment was confirmed
	// gone, and a later one would only move it.
	indexCtx, cancelIndex := recordContext(ctx)
	indexEntry := b.lifecycleIndexEntry(indexCtx, instanceID, logger)

	cancelIndex()

	if deletionAlreadyRecorded(metadata, indexEntry) {
		logger.Debug("deleted_at is already recorded for %s, keeping it", instanceID)

		return
	}

	// Add deleted_at
	metadata["deleted_at"] = deletedAt.Format(time.RFC3339)

	// Store updated metadata
	writeCtx, cancelWrite := recordContext(ctx)
	defer cancelWrite()

	err = b.Vault.Put(writeCtx, instanceID+"/metadata", metadata)
	if err != nil {
		logger.Error("failed to store deleted_at timestamp: %s", err)
		// Continue anyway, this is non-fatal
	}
}

// deletionAlreadyRecorded reports whether metadata holds the deleted_at of the
// instance's current lifecycle, so a retried write must keep it. The current
// lifecycle starts at the latest time found among delete_requested_at,
// provision_requested_at, and created_at in metadata and
// deprovision_requested_at in indexEntry. A deleted_at before that start
// belongs to an earlier use of the same instance GUID, or to a delete whose
// request time was never written, and a new one is recorded. A deleted_at at
// or after it is kept. Times have one second resolution, so a deletion
// confirmed in the second its request was made is kept too. When none of the
// start times is on record there is nothing to date the deleted_at against,
// and it is kept. indexEntry may be nil when the index could not be read.
func deletionAlreadyRecorded(metadata, indexEntry map[string]interface{}) bool {
	deletedAt, ok := recordedTime(metadata, "deleted_at")
	if !ok {
		return false
	}

	start, found := latestLifecycleStart(metadata, indexEntry)
	if !found {
		return true
	}

	return !deletedAt.Before(start)
}

// latestLifecycleStart returns the latest lifecycle start time on record in
// metadata and indexEntry, and whether any was found. A field that is absent
// or does not hold an RFC 3339 time is skipped.
func latestLifecycleStart(metadata, indexEntry map[string]interface{}) (time.Time, bool) {
	var (
		latest time.Time
		found  bool
	)

	consider := func(record map[string]interface{}, fields []string) {
		for _, field := range fields {
			recorded, ok := recordedTime(record, field)
			if ok && (!found || recorded.After(latest)) {
				latest, found = recorded, true
			}
		}
	}

	consider(metadata, metadataLifecycleFields)
	consider(indexEntry, indexLifecycleFields)

	return latest, found
}

// recordedTime returns the RFC 3339 time stored under field in record, and
// whether there is one. A nil record has none.
func recordedTime(record map[string]interface{}, field string) (time.Time, bool) {
	raw, ok := record[field].(string)
	if !ok || raw == "" {
		return time.Time{}, false
	}

	recorded, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}

	return recorded, true
}

// lifecycleIndexEntry returns the instance's index entry for dating its
// current lifecycle. An index that cannot be read leaves the entry out, and
// the lifecycle is dated from the metadata alone, because a failed read must
// not stop the deleted_at write.
func (b *Broker) lifecycleIndexEntry(ctx context.Context, instanceID string, logger logger.Logger) map[string]interface{} {
	entry, err := b.getExistingIndexEntry(ctx, instanceID)
	if err != nil {
		logger.Warning("could not read the index entry of %s, so deleted_at is dated from its metadata alone: %s", instanceID, err)

		return nil
	}

	return entry
}

func (b *Broker) findPlanForBinding(details osbapi.BindRequest, logger logger.Logger) (*services.Plan, error) {
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

func (b *Broker) getInstanceCredentials(ctx context.Context, instanceID string, plan *services.Plan, logger logger.Logger) (interface{}, error) {
	logger.Info("Retrieving credentials for instance %s", instanceID)
	logger.Debug("Calling GetCreds for plan %s", plan.Name)

	deploymentManifest := b.getDeploymentManifestFromVault(instanceID, logger)

	creds, err := manifest.GetCreds(ctx, instanceID, *plan, b.BOSH, deploymentManifest, logger)
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
		delete(credMap, "service_type")
	}
}

func (b *Broker) handleRabbitMQUnbind(ctx context.Context, instanceID, bindingID string, details osbapi.UnbindRequest, logger logger.Logger) error {
	logger.Info("Processing unbind for RabbitMQ service")
	logger.Debug("RabbitMQ plan detected, will delete dynamic user")

	plan, err := b.FindPlan(details.ServiceID, details.PlanID)
	if err != nil {
		logger.Error("Failed to find plan %s/%s: %s", details.ServiceID, details.PlanID, err)

		return err
	}

	creds, err := b.getRabbitMQCredentials(ctx, ctx, instanceID, &plan, logger)
	if err != nil {
		return err
	}

	return b.deleteRabbitMQUser(ctx, bindingID, creds, logger)
}

func (b *Broker) getRabbitMQCredentials(ctx context.Context, _ context.Context, instanceID string, plan *services.Plan, logger logger.Logger) (map[string]interface{}, error) {
	logger.Debug("Retrieving admin credentials for RabbitMQ instance")

	deploymentManifest := b.getDeploymentManifestFromVault(instanceID, logger)

	creds, err := manifest.GetCreds(ctx, instanceID, *plan, b.BOSH, deploymentManifest, logger)
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

	// DeletetUserRabbitMQ already takes a 404 answer for a user that is
	// gone and returns nil, so every error here is a failed delete, even
	// one whose text happens to contain "404".
	err = common.WithRetry(ctx, func() error {
		return DeletetUserRabbitMQ(ctx, bindingID, adminUsername, adminPassword, apiUrl, b.Config, credMap)
	}, maxRetries, baseDelay)
	if err != nil {
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
		if k != "admin_username" && k != "admin_password" && k != "service_type" {
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

	logger.Debug("Built RabbitMQ user creation payload (%d bytes; content not logged because it carries the password)", len(data))

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
// getServiceAndPlanFromVault retrieves service and plan IDs from vault storage.
// handleDynamicRabbitMQCredentials processes dynamic RabbitMQ user creation for bindings.
// populateBindingCredentials fills the structured fields from the raw credential map.
// ServiceWithNoDeploymentCheck checks for services with no deployments.

// sortedKeys returns the keys of m in order, so logs can name a credentials
// map's fields without printing its values.
func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

// logBindingCredentialKeys logs the names of a binding's credential fields and
// never their values, which include the per-binding username and password.
func logBindingCredentialKeys(log logger.Logger, bindingID string, creds map[string]interface{}) {
	log.Debug("Binding %s carries credential keys: %s", bindingID, strings.Join(sortedKeys(creds), ", "))
}
