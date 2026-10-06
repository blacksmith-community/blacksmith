package broker

import (
	"context"
	"time"

	"blacksmith/internal/services"
	"blacksmith/pkg/logger"
	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
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

// SetCredentialSweepPassTimeout replaces the deadline of one sweep pass.
func (b *Broker) SetCredentialSweepPassTimeout(timeout time.Duration) {
	b.credentialSweep.mu.Lock()
	defer b.credentialSweep.mu.Unlock()

	b.credentialSweep.passTimeout = timeout
}

// ProvisionActive reports whether the provisionAsync goroutine for instanceID
// is still running in this process.
func (b *Broker) ProvisionActive(instanceID string) bool {
	_, running := b.activeProvisions.Load(instanceID)

	return running
}

// DeprovisionActive reports whether the deprovisionAsync goroutine for
// instanceID is still running in this process.
func (b *Broker) DeprovisionActive(instanceID string) bool {
	_, running := b.activeDeprovisions.Load(instanceID)

	return running
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

// RetryDeleteDeployment exposes the deprovision delete retry loop.
func (b *Broker) RetryDeleteDeployment(ctx context.Context, deploymentName, instanceID string, maxRetries int) error {
	_, err := b.retryDeleteDeployment(ctx, deploymentName, instanceID, maxRetries)

	return err
}

// CleanupFailedDeployment exposes the best-effort delete of a failed deployment.
func (b *Broker) CleanupFailedDeployment(ctx context.Context, deploymentName string) {
	b.cleanupFailedDeployment(ctx, deploymentName, logger.Get().Named("broker"))
}

// TearDownAbandonedDeployment exposes the delete of a deployment whose
// instance was deprovisioned while the deploy was running.
func (b *Broker) TearDownAbandonedDeployment(ctx context.Context, instanceID, deploymentName string) {
	b.tearDownAbandonedDeployment(ctx, instanceID, deploymentName, logger.Get().Named("broker"))
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

// RecordDeprovisionRequest exposes the index write that Deprovision makes
// before it answers.
func (b *Broker) RecordDeprovisionRequest(ctx context.Context, instanceID string, details osbapi.DeprovisionRequest) error {
	return b.recordDeprovisionRequest(ctx, instanceID, details, logger.Get().Named("broker"))
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
