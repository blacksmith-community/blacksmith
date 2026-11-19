package broker

import (
	"context"
	"reflect"
	"runtime"
	"strings"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/services"
	"blacksmith/pkg/logger"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
	"github.com/google/uuid"
)

// CreateValkeyACLUser exposes the bind path's ACL user creation to package
// broker_test, running it against the node or nodes credMap describes.
func (b *Broker) CreateValkeyACLUser(ctx context.Context, instanceID string, credMap map[string]interface{}, username, password string, brokerLogger logger.Logger) error {
	conn, err := extractValkeyConnInfo(credMap)
	if err != nil {
		return err
	}

	return b.createACLUser(ctx, instanceID, conn, username, password, brokerLogger)
}

// DeleteValkeyACLUser exposes the unbind path's ACL user deletion to package
// broker_test, running it against the node or nodes credMap describes.
func (b *Broker) DeleteValkeyACLUser(ctx context.Context, instanceID string, credMap map[string]interface{}, username string, brokerLogger logger.Logger) error {
	conn, err := extractValkeyConnInfo(credMap)
	if err != nil {
		return err
	}

	return b.deleteACLUser(ctx, instanceID, conn, username, brokerLogger)
}

// SetValkeyACLRetry overrides the Valkey ACL retry settings and returns a
// function that restores the previous ones.
func SetValkeyACLRetry(retries int, baseWait time.Duration) func() {
	previousRetries, previousWait := valkeyACLRetries, valkeyACLBaseWait
	valkeyACLRetries, valkeyACLBaseWait = retries, baseWait

	return func() {
		valkeyACLRetries, valkeyACLBaseWait = previousRetries, previousWait
	}
}

// SetTaskPollInterval shortens or lengthens the background monitor's poll
// interval for monitors started from now on, and returns a function that
// restores the previous interval.
func SetTaskPollInterval(d time.Duration) func() {
	previous := taskPollOverride.Swap(int64(d))

	return func() { taskPollOverride.Store(previous) }
}

// SetMonitorExited sets the hook the broker calls, with the instance ID, each
// time a deprovision monitor goroutine returns.
func (b *Broker) SetMonitorExited(hook func(instanceID string)) {
	b.monitorExited = hook
}

// Track exposes the launch of a goroutine that WaitForBackground waits for.
func (b *Broker) Track(name string, fn func()) {
	b.track(name, fn)
}

// WaitForCredentialCleanups blocks until every CredHub cleanup goroutine the
// broker started has returned.
func (b *Broker) WaitForCredentialCleanups() {
	b.credentialCleanups.Wait()
}

// SetCredentialSweepClock replaces the clock the orphan sweep's once-an-hour
// rule reads.
func (b *Broker) SetCredentialSweepClock(now func() time.Time) {
	b.credentialSweep.mu.Lock()
	defer b.credentialSweep.mu.Unlock()

	b.credentialSweep.now = now
}

// SetUnknownOutcomeClock replaces the clock an unknown outcome is recorded
// and judged by. Set it before the broker starts any work.
func (b *Broker) SetUnknownOutcomeClock(now func() time.Time) {
	b.unknownOutcomeClock = now
}

// SetCredentialSweepPassTimeout replaces the deadline of one sweep pass.
func (b *Broker) SetCredentialSweepPassTimeout(timeout time.Duration) {
	b.credentialSweep.mu.Lock()
	defer b.credentialSweep.mu.Unlock()

	b.credentialSweep.passTimeout = timeout
}

// ProvisionActive reports whether a provision of instanceID is in flight in
// this process, from its accept chain until its goroutine returns.
func (b *Broker) ProvisionActive(instanceID string) bool {
	return b.provisionActive(instanceID)
}

// HoldOperationClaim takes the claim on instanceID that an operation of the
// given type holds from its accept chain until its goroutine returns, and
// returns the function that gives it up. It fails the spec when another
// claim is already held.
func (b *Broker) HoldOperationClaim(instanceID, operation string) func() {
	claim := &operationClaim{operation: operation, claimed: time.Now()}

	_, claimed, err := b.claimOperation(instanceID, "", claim)
	if err != nil || !claimed {
		panic("an operation claim is already held on instance " + instanceID)
	}

	return func() { b.releaseOperation(instanceID, claim) }
}

// ClaimStoredWhenGuardFreed takes a claim on instanceID, gives it up, and
// reports whether the claim was still stored when its reservation in the
// mutation guard was freed.
func (b *Broker) ClaimStoredWhenGuardFreed(instanceID string) bool {
	claim := &operationClaim{operation: operationTypeProvision, claimed: time.Now()}

	_, claimed, err := b.claimOperation(instanceID, "", claim)
	if err != nil || !claimed {
		panic("an operation claim is already held on instance " + instanceID)
	}

	var stored bool

	free := claim.release
	claim.release = func() {
		stored = b.OperationClaimed(instanceID)

		if free != nil {
			free()
		}
	}

	b.releaseOperation(instanceID, claim)

	return stored
}

// DeprovisionActive reports whether a deprovision of instanceID is in flight
// in this process, from its accept chain until its goroutine returns.
func (b *Broker) DeprovisionActive(instanceID string) bool {
	return b.deprovisionActive(instanceID)
}

// LogBindingCredentialKeys exposes the bind path's credential logging.
func LogBindingCredentialKeys(log logger.Logger, bindingID string, creds map[string]interface{}) {
	logBindingCredentialKeys(log, bindingID, creds)
}

// PrepareUserCreationPayload exposes the RabbitMQ user payload builder.
func PrepareUserCreationPayload(password string, log logger.Logger) ([]byte, error) {
	return prepareUserCreationPayload(password, log)
}

// CompleteBind exposes the tail of Bind, from the instance's admin credentials
// to the credentials the binding returns.
func (b *Broker) CompleteBind(ctx context.Context, instanceID, bindingID string, creds map[string]interface{}, log logger.Logger) (map[string]interface{}, error) {
	binding, err := b.completeBind(ctx, instanceID, bindingID, creds, log)

	return binding.Credentials, err
}

// RedactedQuery exposes the query string redaction used by the request logs.
func RedactedQuery(rawQuery string) string { return redactedQuery(rawQuery) }

// RetryDeleteDeployment exposes the deprovision delete retry loop. Its delete
// requests carry a fresh context ID.
func (b *Broker) RetryDeleteDeployment(ctx context.Context, deploymentName, instanceID string, maxRetries int) error {
	_, err := b.retryDeleteDeployment(ctx, deploymentName, instanceID, uuid.NewString(), maxRetries)

	return err
}

// RetryDeleteDeploymentWithContextID exposes the deprovision delete retry
// loop with the context ID its delete requests carry.
func (b *Broker) RetryDeleteDeploymentWithContextID(ctx context.Context, deploymentName, instanceID, contextID string, maxRetries int) error {
	_, err := b.retryDeleteDeployment(ctx, deploymentName, instanceID, contextID, maxRetries)

	return err
}

// RetryDeleteDeploymentTask exposes the deprovision delete retry loop with
// the context ID its first delete request carries, and returns the delete
// task it settles on.
func (b *Broker) RetryDeleteDeploymentTask(ctx context.Context, deploymentName, instanceID, contextID string, maxRetries int) (*bosh.Task, error) {
	return b.retryDeleteDeployment(ctx, deploymentName, instanceID, contextID, maxRetries)
}

// SetDeleteRetryBackoff sets the wait that, multiplied by the number of
// the attempt that failed, comes before the next delete attempt. The
// returned function restores the previous value.
func SetDeleteRetryBackoff(d time.Duration) func() {
	previous := deleteRetryBackoffOverride.Swap(int64(d))

	return func() { deleteRetryBackoffOverride.Store(previous) }
}

// DeleteDeploymentWithRetry exposes the delete a deprovision sends under
// contextID, with the record write of the ID that comes before it.
func (b *Broker) DeleteDeploymentWithRetry(ctx context.Context, instanceID, deploymentName, contextID string) error {
	phase := &deprovisioningPhase{
		broker:         b,
		instanceID:     instanceID,
		deploymentName: deploymentName,
		logger:         logger.Get().Named("broker"),
		contextID:      contextID,
	}

	_, err := phase.deleteDeploymentWithRetry(ctx)

	return err
}

// CleanupFailedDeployment exposes the best-effort delete of a failed
// deployment, which decides from the instance's own record.
func (b *Broker) CleanupFailedDeployment(ctx context.Context, instanceID, deploymentName string) {
	b.cleanupFailedDeployment(ctx, instanceID, deploymentName, logger.Get().Named("broker"))
}

// CleanupFailedDeploymentLogged runs the cleanup as CleanupFailedDeployment
// does, logging to log.
func (b *Broker) CleanupFailedDeploymentLogged(ctx context.Context, instanceID, deploymentName string, log logger.Logger) {
	b.cleanupFailedDeployment(ctx, instanceID, deploymentName, log)
}

// TearDownAbandonedDeployment exposes the delete of a deployment whose
// instance was deprovisioned while the deploy was running, for a deploy
// whose task is not known.
func (b *Broker) TearDownAbandonedDeployment(ctx context.Context, instanceID, deploymentName string) {
	b.tearDownAbandonedDeployment(ctx, instanceID, deploymentName, 0, func() {}, logger.Get().Named("broker"))
}

// TearDownAbandonedDeploymentOfTask exposes the same teardown for a deploy
// that ran as the given create task.
func (b *Broker) TearDownAbandonedDeploymentOfTask(ctx context.Context, instanceID, deploymentName string, createTaskID int) {
	b.tearDownAbandonedDeployment(ctx, instanceID, deploymentName, createTaskID, func() {}, logger.Get().Named("broker"))
}

// TearDownAbandonedDeploymentHolding exposes the teardown of a deploy
// whose task is not known, run by a provision that holds the instance's
// reservation in the mutation guard and frees it with release.
func (b *Broker) TearDownAbandonedDeploymentHolding(ctx context.Context, instanceID, deploymentName string, release func()) {
	b.tearDownAbandonedDeployment(ctx, instanceID, deploymentName, 0, release, logger.Get().Named("broker"))
}

// SetAbandonedCreateWait sets how long the teardown of an abandoned
// deployment waits for its cancelled create task to stop. The returned
// function restores the previous value.
func SetAbandonedCreateWait(d time.Duration) func() {
	previous := abandonedCreateWaitOverride.Swap(int64(d))

	return func() { abandonedCreateWaitOverride.Store(previous) }
}

// StoreDeleteRequestedTimestamp exposes the write of delete_requested_at that
// Deprovision makes before it answers.
func (b *Broker) StoreDeleteRequestedTimestamp(ctx context.Context, instanceID string) {
	b.storeDeleteRequestedTimestamp(ctx, instanceID, logger.Get().Named("broker"))
}

// StoreDeletedTimestamp exposes the write of deleted_at that LastOperation
// makes once a deprovision has finished.
func (b *Broker) StoreDeletedTimestamp(ctx context.Context, instanceID string) {
	b.storeDeletedTimestamp(ctx, instanceID, logger.Get().Named("broker"))
}

// StoreDeletedTimestampFromMonitor exposes the write of deleted_at that the
// background deprovision makes once the deployment is gone.
func (b *Broker) StoreDeletedTimestampFromMonitor(ctx context.Context, instanceID string) {
	phase := &deprovisioningPhase{broker: b, instanceID: instanceID, logger: logger.Get().Named("broker")}
	phase.storeDeletedTimestamp(ctx)
}

// UpdateInstanceTimestamp exposes the write of created_at that
// OnProvisionCompleted makes to the instance metadata.
func (b *Broker) UpdateInstanceTimestamp(ctx context.Context, instanceID string) error {
	return b.updateInstanceTimestamp(ctx, instanceID, logger.Get().Named("broker"))
}

// RecordProgress exposes the progress record the provision and deprovision
// goroutines write.
func (b *Broker) RecordProgress(ctx context.Context, instanceID, operation, message string) {
	b.trackProgress(ctx, instanceID, operation, message, 0, nil, logger.Get().Named("broker"))
}

// RecordFailure exposes the failure record the provision and deprovision
// goroutines write.
func (b *Broker) RecordFailure(ctx context.Context, instanceID, operation, message string) {
	b.failWithTracking(ctx, instanceID, operation, message, nil, logger.Get().Named("broker"))
}

// RecordTask exposes the write that stores a BOSH task ID for an instance's
// current operation.
func (b *Broker) RecordTask(ctx context.Context, instanceID, operation, description string, taskID int) {
	b.recordTask(ctx, instanceID, operation, description, taskID, logger.Get().Named("broker"))
}

// StoreBindingCredentials exposes the write of a binding's credentials.
func (b *Broker) StoreBindingCredentials(ctx context.Context, instanceID, bindingID string, credentials map[string]interface{}) error {
	return b.storeBindingCredentials(ctx, instanceID, bindingID, credentials, logger.Get().Named("broker"))
}

// DeleteRabbitMQUser exposes the unbind step that deletes a binding's
// RabbitMQ user, retries included.
func (b *Broker) DeleteRabbitMQUser(ctx context.Context, bindingID string, creds map[string]interface{}) error {
	return b.deleteRabbitMQUser(ctx, bindingID, creds, logger.Get().Named("broker"))
}

// RecordDeprovisionRequest exposes the index write that Deprovision makes
// before it answers.
func (b *Broker) RecordDeprovisionRequest(ctx context.Context, instanceID string, details osbapi.DeprovisionRequest) error {
	_, err := b.recordDeprovisionRequest(ctx, instanceID, details, logger.Get().Named("broker"))

	return err
}

// InstanceDetails exposes the read of an instance's provision details and
// deployment name that the provision completion hook makes.
func (b *Broker) InstanceDetails(ctx context.Context, instanceID string) (osbapi.ProvisionRequest, string, error) {
	return b.getInstanceDetails(ctx, instanceID, logger.Get().Named("broker"))
}

// StoreDeploymentNameInIndex exposes the write of the deployment name into the
// index.
func (b *Broker) StoreDeploymentNameInIndex(ctx context.Context, instanceID, deploymentName string) {
	b.storeDeploymentNameInIndex(ctx, instanceID, deploymentName, logger.Get().Named("broker"))
}

// RecordInitialRequest exposes the index write that Provision makes first.
func (b *Broker) RecordInitialRequest(ctx context.Context, instanceID string, details osbapi.ProvisionRequest) error {
	return b.recordInitialRequest(ctx, instanceID, details, logger.Get().Named("broker"))
}

// UpdateInstanceStatus exposes the index write that records a provision's
// status.
func (b *Broker) UpdateInstanceStatus(ctx context.Context, instanceID string, details osbapi.ProvisionRequest, plan *services.Plan, status string) error {
	return b.updateInstanceStatus(ctx, instanceID, details, plan, status, logger.Get().Named("broker"))
}

// SetRecordWriteTimeout shortens or lengthens the bound on a record write for
// writes made from now on, and returns a function that restores the previous
// bound.
func SetRecordWriteTimeout(d time.Duration) func() {
	previous := recordTimeoutOverride.Swap(int64(d))

	return func() { recordTimeoutOverride.Store(previous) }
}

// UnknownOutcomePassTimeout is the production bound on one pass of the
// unknown-outcome resolver.
const UnknownOutcomePassTimeout = unknownOutcomePassTimeout

// SetUnknownOutcomePassTimeout shortens or lengthens the bound on the
// unknown-outcome resolver passes started from now on, and returns a
// function that restores the previous bound.
func SetUnknownOutcomePassTimeout(d time.Duration) func() {
	previous := unknownOutcomePassOverride.Swap(int64(d))

	return func() { unknownOutcomePassOverride.Store(previous) }
}

// SetFailedDeploymentCleanupTimeout shortens or lengthens the bound on the
// director calls of a failed deployment's cleanup, and returns a function
// that restores the previous bound.
func SetFailedDeploymentCleanupTimeout(d time.Duration) func() {
	previous := cleanupTimeoutOverride.Swap(int64(d))

	return func() { cleanupTimeoutOverride.Store(previous) }
}

// StoreDeploymentDetails exposes the write of a provision's details, and the
// cleanup that follows when it fails.
func (b *Broker) StoreDeploymentDetails(ctx context.Context, instanceID, deploymentName string, details osbapi.ProvisionRequest) error {
	return b.storeDeploymentDetails(ctx, instanceID, deploymentName, details, logger.Get().Named("broker"))
}

// BackgroundGoroutines returns the stack of every goroutine, other than the
// caller's, that is running code of package broker. Between specs it should
// be empty: a provision, deprovision, monitor, or cleanup goroutine still
// running there can write the shared index during the next spec.
func BackgroundGoroutines() []string {
	// Function names carry the package path, for example
	// "blacksmith/internal/broker.recordContext". The test package's own
	// functions are named "blacksmith/internal/broker_test....", so the
	// prefix with its trailing dot does not match them.
	name := runtime.FuncForPC(reflect.ValueOf(recordContext).Pointer()).Name()
	prefix := name[:strings.LastIndex(name, ".")+1]

	buf := make([]byte, 1<<20)

	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]

			break
		}

		buf = make([]byte, 2*len(buf))
	}

	stacks := strings.Split(string(buf), "\n\n")

	var running []string

	// The first stack is the caller's own goroutine.
	for _, stack := range stacks[1:] {
		if strings.Contains(stack, "\n"+prefix) {
			running = append(running, stack)
		}
	}

	return running
}

// RecordedOperation is the operation the broker records for an instance.
type RecordedOperation = recordedOperation

// HasTask exposes whether the recorded operation names a BOSH task.
func (r recordedOperation) HasTask() bool { return r.hasTask() }

// RecordedOperation exposes the read of an instance's recorded operation.
func (b *Broker) RecordedOperation(ctx context.Context, instanceID string) (RecordedOperation, error) {
	return b.readRecordedOperation(ctx, instanceID)
}

// WithdrawIndexFields exposes the change that takes a refused deprovision
// request back off an index entry.
func WithdrawIndexFields(entries map[string]interface{}, instanceID string, fields, previous map[string]interface{}, existed bool) bool {
	return withdrawIndexFields(entries, instanceID, fields, previous, existed)
}

// UnknownOutcomePassState reports whether the broker keeps resolver pass
// state for instanceID, and the error the last pass ended with.
func (b *Broker) UnknownOutcomePassState(instanceID string) (bool, string) {
	value, kept := b.unknownOutcomePasses.Load(instanceID)
	if !kept {
		return false, ""
	}

	pass, _ := value.(*unknownOutcomePass)

	return true, pass.lastError()
}

// ResolveUnknownOutcome exposes a resolver pass over operation, the
// unknown outcome a poll read from the record.
func (b *Broker) ResolveUnknownOutcome(ctx context.Context, instanceID, deploymentName string, operation RecordedOperation) (osbapi.LastOperationResponse, error) {
	return b.resolveUnknownOutcome(ctx, instanceID, deploymentName, operation, logger.Get().Named("broker"))
}

// UnknownOutcomePassReader returns a reader of the last error held by the
// resolver pass state the broker keeps for instanceID now, as a poll that
// loaded that state reads it, or nil when it keeps none.
func (b *Broker) UnknownOutcomePassReader(instanceID string) func() string {
	value, kept := b.unknownOutcomePasses.Load(instanceID)
	if !kept {
		return nil
	}

	pass, _ := value.(*unknownOutcomePass)

	return pass.lastError
}

// NoTaskInAnswer exposes the error for a mutation the director accepted
// with no task in its answer.
func (b *Broker) NoTaskInAnswer(operation, deploymentName, contextID string) error {
	return b.noTaskInAnswer(operation, deploymentName, contextID)
}

// SetCleanupRetryInterval replaces the interval between passes of the
// sweep of owed cleanups, whatever the settings say, and returns a
// function that restores the previous one.
func SetCleanupRetryInterval(d time.Duration) func() {
	previous := cleanupRetryIntervalOverride.Swap(int64(d))

	return func() { cleanupRetryIntervalOverride.Store(previous) }
}

// OwedRetryBackoff exposes how long the sweep of owed cleanups waits
// after its tries-th try of one cleanup.
func OwedRetryBackoff(interval time.Duration, tries int) time.Duration {
	return owedRetryBackoff(interval, tries)
}

// SetReadoptIntervals replaces the backoff between Vault reads and the
// interval between checks of a re-adopted operation, and returns a
// function that restores the previous ones.
func SetReadoptIntervals(d time.Duration) func() {
	previous := readoptIntervalOverride.Swap(int64(d))

	return func() { readoptIntervalOverride.Store(previous) }
}

// SetReadoptBound replaces the bound of every re-adoption goroutine
// started from now on, and returns a function that restores the previous
// one.
func SetReadoptBound(d time.Duration) func() {
	previous := readoptBoundOverride.Swap(int64(d))

	return func() { readoptBoundOverride.Store(previous) }
}

// ReadoptStore is the part of Vault the first phase of re-adoption reads.
type ReadoptStore = readoptStore

// SetReadoptStore replaces the store the first phase of re-adoption reads.
func (b *Broker) SetReadoptStore(store ReadoptStore) {
	b.readoptVault = store
}

// YamlGsub exposes the YAML round trip that swaps one string for another in
// a credentials map.
func YamlGsub(obj interface{}, orig, replacement string) (interface{}, error) {
	return yamlGsub(obj, orig, replacement)
}

// YamlErrorSummary exposes the description of a YAML decode error that the
// broker logs and returns, to package broker_test.
func YamlErrorSummary(err error) string { return yamlErrorSummary(err) }

// InstanceStillIndexed exposes the provision's check that its instance is
// still in the index.
func (b *Broker) InstanceStillIndexed(ctx context.Context, instanceID string) bool {
	phase := &provisioningPhase{broker: b, instanceID: instanceID, logger: logger.Get().Named("broker")}

	return phase.instanceStillIndexed(ctx)
}

// CredentialReaders exposes, by a name for the test to report, each broker
// path that reads an instance's credentials from its deployment's VMs:
// bind, the base credentials of a bind, the credentials a provision
// stores, the retry of that store, and the admin credentials of a RabbitMQ
// or Valkey unbind.
func (b *Broker) CredentialReaders() map[string]func(ctx context.Context, instanceID string, plan services.Plan) error {
	brokerLogger := logger.Get().Named("broker")

	return map[string]func(ctx context.Context, instanceID string, plan services.Plan) error{
		"bind": func(ctx context.Context, instanceID string, plan services.Plan) error {
			_, err := b.getInstanceCredentials(ctx, instanceID, &plan, brokerLogger)

			return err
		},
		"base credentials": func(ctx context.Context, instanceID string, plan services.Plan) error {
			_, err := b.getBaseCredentials(ctx, instanceID, plan, brokerLogger)

			return err
		},
		"provision credentials": func(ctx context.Context, instanceID string, plan services.Plan) error {
			return b.processCredentials(ctx, instanceID, osbapi.ProvisionRequest{ServiceID: plan.Service.ID, PlanID: plan.ID}, brokerLogger)
		},
		"credential store retry": func(ctx context.Context, instanceID string, plan services.Plan) error {
			return b.retryCredentialStorage(ctx, instanceID, &plan, brokerLogger)
		},
		"rabbitmq unbind": func(ctx context.Context, instanceID string, plan services.Plan) error {
			_, err := b.getRabbitMQCredentials(ctx, ctx, instanceID, &plan, brokerLogger)

			return err
		},
		"valkey unbind": func(ctx context.Context, instanceID string, plan services.Plan) error {
			_, err := b.getValkeyCredentials(ctx, ctx, instanceID, &plan, brokerLogger)

			return err
		},
	}
}

// SettleReadoptedTask exposes the settling of a re-adopted operation of an
// indexed instance whose record names a task, as re-adoption runs it
// after a restart.
func (b *Broker) SettleReadoptedTask(ctx context.Context, instanceID, deploymentName string, current RecordedOperation) (osbapi.LastOperationResponse, error) {
	operation := readoptOperation{instanceID: instanceID, indexed: true, deploymentName: deploymentName, recorded: current}

	return b.settleReadoptedTask(ctx, operation, current, logger.Get().Named("broker"))
}

// DeploymentNameSites exposes, by a name for the test to report, each
// broker path besides the credential readers that names an indexed
// instance's deployment: the deployment a deprovision reserves, the
// running-task check before a deprovision, the deployment a last_operation
// poll checks, and the re-adoption candidate list. Each returns the name
// it chose, or "" for a path that only asks the director, whose calls the
// test reads.
func (b *Broker) DeploymentNameSites() map[string]func(ctx context.Context, instanceID, planID string) (string, error) {
	brokerLogger := logger.Get().Named("broker")

	return map[string]func(ctx context.Context, instanceID, planID string) (string, error){
		"deprovision reservation": func(ctx context.Context, instanceID, planID string) (string, error) {
			return b.deprovisionDeployment(ctx, instanceID, planID, brokerLogger)
		},
		"deprovision busy check": func(ctx context.Context, instanceID, planID string) (string, error) {
			_, err := b.rejectIfOperationInProgress(ctx, instanceID, planID, brokerLogger)

			return "", err
		},
		"last_operation poll": func(ctx context.Context, instanceID, _ string) (string, error) {
			_, deploymentName, _, err := b.getInstanceForOperation(ctx, instanceID, brokerLogger)

			return deploymentName, err
		},
		"re-adoption candidates": func(ctx context.Context, instanceID, _ string) (string, error) {
			candidates, _, err := readoptListOnce(ctx, b.Vault)
			if err != nil {
				return "", err
			}

			for _, candidate := range candidates {
				if candidate.instanceID == instanceID {
					return candidate.deploymentName, nil
				}
			}

			return "", nil
		},
	}
}

// SetDeprovisionSettleWait replaces how long a deprovision waits for the
// settle lock before it answers ConcurrencyError, and returns a function
// that restores the previous wait.
func SetDeprovisionSettleWait(d time.Duration) func() {
	previous := deprovisionSettleWaitOverride.Swap(int64(d))

	return func() { deprovisionSettleWaitOverride.Store(previous) }
}

// MintedFor exposes the check that deploymentName is exactly the name the
// broker mints for instanceID under planID.
func MintedFor(deploymentName, instanceID, planID string) bool {
	return mintedFor(deploymentName, instanceID, planID)
}

// MintedShape exposes the check that deploymentName has the shape of a
// name minted for instanceID, which the cleanup makes before it reads the
// request details stored under that name.
func MintedShape(deploymentName, instanceID string) bool {
	return mintedShape(deploymentName, instanceID)
}

// RecordAcceptedDeprovision exposes the write of an accepted deprovision
// under the settle lock, over a record that held no unknown outcome.
func (b *Broker) RecordAcceptedDeprovision(ctx context.Context, instanceID string) (bool, error) {
	return b.recordAcceptedDeprovision(ctx, instanceID, recordedOperation{}, logger.Get().Named("broker"))
}

// SettleLockFree reports whether the settle lock of instanceID can be
// taken within wait, and releases it again when it was.
func (b *Broker) SettleLockFree(instanceID string, wait time.Duration) bool {
	unlock, err := b.lockSettleWithin(context.Background(), instanceID, wait)
	if err != nil {
		return false
	}

	unlock()

	return true
}

// RecordProvisionFailed exposes the write a poll makes when the task of a
// provision failed, which runs no cleanup of its own.
func (b *Broker) RecordProvisionFailed(ctx context.Context, instanceID string, recorded RecordedOperation, description string, taskID int) bool {
	_, landed := b.recordProvisionFailed(ctx, instanceID, recorded, description, taskID, logger.Get().Named("broker"))

	return landed
}

// WriteReadoptedFailure exposes the write re-adoption makes when it fails
// an operation, which runs no cleanup of its own.
func (b *Broker) WriteReadoptedFailure(ctx context.Context, instanceID string, current RecordedOperation, description string) bool {
	return b.writeReadoptedFailure(ctx, instanceID, current, description, logger.Get().Named("readopt"))
}

// SweepOwedCleanupsOnce runs one pass of the sweep of owed cleanups, with
// interval as the interval between passes.
func (b *Broker) SweepOwedCleanupsOnce(ctx context.Context, interval time.Duration) {
	b.sweepOwedCleanupsOnce(ctx, interval, logger.Get().Named("cleanup-sweep"))
}

// NoteCleanupAttention lists instanceID under Attention required for
// deploymentName with reason, as a refused cleanup does.
func (b *Broker) NoteCleanupAttention(instanceID, deploymentName, reason string) {
	b.noteCleanupAttention(instanceID, deploymentName, reason)
}

// SetSweepClock replaces the clock the sweep of owed cleanups reads.
func (b *Broker) SetSweepClock(clock func() time.Time) {
	b.sweep.mu.Lock()
	defer b.sweep.mu.Unlock()

	b.sweep.clock = clock
}

// OwedCleanupSuspects returns the instances a pass of the sweep reads
// between full scans.
func (b *Broker) OwedCleanupSuspects() []string {
	ids, _ := b.sweep.snapshot()

	return ids
}

// JudgeCleanupClaim runs the ownership decision of a cleanup asked for
// asked over the name, plan, and refusal reason the name lookup returned
// for instanceID, and returns what the cleanup would delete.
func (b *Broker) JudgeCleanupClaim(ctx context.Context, instanceID, asked, name, planID, reason string) (string, bool) {
	claim := cleanupName{name: name, planID: planID, refusal: ownership{reason: reason}}

	return b.judgeCleanupClaim(ctx, instanceID, asked, claim, logger.Get().Named("broker"))
}

// ClaimCandidates exposes the IDs of the instances whose records the
// claim check reads for deploymentName.
func ClaimCandidates(deploymentName string) []string {
	return claimCandidates(deploymentName)
}

// RefuseProvisionOverClaimedName runs the check a new provision of
// instanceID under planID makes for another instance that claims the
// deployment name it would mint.
func (b *Broker) RefuseProvisionOverClaimedName(ctx context.Context, instanceID, planID string) error {
	return b.refuseProvisionOverClaimedName(ctx, instanceID, osbapi.ProvisionRequest{PlanID: planID}, logger.Get().Named("broker"))
}

// RefuseProvisionOverUnownedDeployment reads the record of instanceID and
// runs the check a provision of it, which the index does not have, makes
// for a deployment named deploymentName that exists.
func (b *Broker) RefuseProvisionOverUnownedDeployment(ctx context.Context, instanceID, deploymentName string) error {
	recorded, err := b.readRecordedOperation(ctx, instanceID)
	if err != nil {
		return err
	}

	return b.refuseProvisionOverUnownedDeployment(ctx, instanceID, deploymentName, recorded, logger.Get().Named("broker"))
}
