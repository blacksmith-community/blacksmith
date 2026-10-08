package broker

import (
	"context"
	"reflect"
	"runtime"
	"strings"
	"time"

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

	_, claimed := b.claimOperation(instanceID, claim)
	if !claimed {
		panic("an operation claim is already held on instance " + instanceID)
	}

	return func() { b.releaseOperation(instanceID, claim) }
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

	return phase.deleteDeploymentWithRetry(ctx)
}

// CleanupFailedDeployment exposes the best-effort delete of a failed deployment.
func (b *Broker) CleanupFailedDeployment(ctx context.Context, deploymentName string) {
	b.cleanupFailedDeployment(ctx, deploymentName, logger.Get().Named("broker"))
}

// TearDownAbandonedDeployment exposes the delete of a deployment whose
// instance was deprovisioned while the deploy was running, for a deploy
// whose task is not known.
func (b *Broker) TearDownAbandonedDeployment(ctx context.Context, instanceID, deploymentName string) {
	b.tearDownAbandonedDeployment(ctx, instanceID, deploymentName, 0, logger.Get().Named("broker"))
}

// TearDownAbandonedDeploymentOfTask exposes the same teardown for a deploy
// that ran as the given create task.
func (b *Broker) TearDownAbandonedDeploymentOfTask(ctx context.Context, instanceID, deploymentName string, createTaskID int) {
	b.tearDownAbandonedDeployment(ctx, instanceID, deploymentName, createTaskID, logger.Get().Named("broker"))
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
