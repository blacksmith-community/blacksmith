package broker

import (
	"context"
	"time"

	"blacksmith/pkg/logger"
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
