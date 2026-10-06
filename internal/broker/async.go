package broker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/manifest"
	"blacksmith/internal/services"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/utils"
	vaultPkg "blacksmith/pkg/vault"
)

// Constants for async operations.
const (
	// Retry backoff multiplier.
	retryBackoffMultiplier = 5 * time.Second

	// Default polling interval for the background delete-task monitor.
	defaultTaskPollInterval = 10 * time.Second

	// Task monitoring timeout duration.
	taskMonitorTimeout = 2 * time.Hour

	// provisionTimeout bounds the provision goroutine: the manifest work, the
	// release uploads and the deploy itself while bosh-cli still runs it.
	provisionTimeout = 60 * time.Minute

	// failedDeploymentCleanupTimeout bounds the best-effort delete of a
	// deployment whose create task failed.
	failedDeploymentCleanupTimeout = 5 * time.Minute

	// recordWriteTimeout bounds a failure or progress record written to the
	// vault, so the write still happens after the context it follows ended.
	recordWriteTimeout = 10 * time.Second
)

// serviceContext returns the context that ends when the broker shuts down. A
// broker built without one, as many tests do, never sees a shutdown.
func (b *Broker) serviceContext() context.Context {
	if b.ServiceContext == nil {
		return context.Background()
	}

	return b.ServiceContext
}

// recordContext returns a context for writing a record. It keeps the values
// of ctx but not its cancellation or deadline, and bounds the write by
// recordWriteTimeout.
func recordContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), recordTimeout())
}

// recordTimeoutOverride, when positive, replaces recordWriteTimeout in
// nanoseconds.
//
//nolint:gochecknoglobals // a test seam, set only through export_test.go
var recordTimeoutOverride atomic.Int64

// recordTimeout returns how long a record write may take.
func recordTimeout() time.Duration {
	override := recordTimeoutOverride.Load()
	if override > 0 {
		return time.Duration(override)
	}

	return recordWriteTimeout
}

// taskPollOverride, when positive, replaces defaultTaskPollInterval in
// nanoseconds. It is atomic because monitors started earlier may read it
// while a test changes it.
//
//nolint:gochecknoglobals // a test seam, set only through export_test.go
var taskPollOverride atomic.Int64

// taskPollInterval returns how often the background monitor polls a task.
func taskPollInterval() time.Duration {
	override := taskPollOverride.Load()
	if override > 0 {
		return time.Duration(override)
	}

	return defaultTaskPollInterval
}

// Static errors for err113 compliance.
var (
	ErrFailedToDeleteDeployment = errors.New("failed to delete deployment after maximum attempts")
)

// Helper methods for provisionAsync refactoring

// trackProgress is a helper to track progress with error logging.
func (b *Broker) trackProgress(ctx context.Context, instanceID, operation, message string, _ int, params map[interface{}]interface{}, l logger.Logger) {
	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	err := b.Vault.TrackProgress(recordCtx, instanceID, operation, message, 0, params)
	if err != nil {
		l.Error("failed to track progress (%s): %s", message, err)
	}
}

// failWithTracking tracks a failure.
func (b *Broker) failWithTracking(ctx context.Context, instanceID, operation, message string, params map[interface{}]interface{}, l logger.Logger) {
	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	err := b.Vault.TrackProgress(recordCtx, instanceID, operation, message, -1, params)
	if err != nil {
		l.Error("failed to track progress (%s): %s", message, err)
	}
}

// stoppedProvisionDescription describes a provision whose own context ended
// before the failing step finished, so an operator reads the cause and not a
// failure of the step. It returns "" while the context is still live.
func (b *Broker) stoppedProvisionDescription(ctx context.Context, err error) string {
	if ctx.Err() == nil {
		return ""
	}

	if b.serviceContext().Err() != nil {
		return fmt.Sprintf("Provision stopped by broker shutdown: %s", err)
	}

	return fmt.Sprintf("Provision stopped at its %s time limit: %s", provisionTimeout, err)
}

// provisioningPhase represents a phase in the provisioning process.
type provisioningPhase struct {
	broker     *Broker
	instanceID string
	logger     logger.Logger
	params     map[interface{}]interface{}
}

// failStep records the provision as failed at the step named by summary. When
// the provision context has ended, the record says the broker stopped it.
func (p *provisioningPhase) failStep(ctx context.Context, summary string, err error) {
	description := p.broker.stoppedProvisionDescription(ctx, err)
	if description == "" {
		description = fmt.Sprintf("%s: %s", summary, err)
	}

	p.broker.failWithTracking(ctx, p.instanceID, "provision", description, p.params, p.logger)
}

// parseParameters extracts and parses parameters from the details.
func (p *provisioningPhase) parseParameters(ctx context.Context, details interface{}) bool {
	p.broker.trackProgress(ctx, p.instanceID, "provision", "Parsing service parameters", 0, p.params, p.logger)

	if detailsMap, ok := details.(map[string]interface{}); ok {
		if params, ok := detailsMap["parameters"].(map[string]interface{}); ok {
			for k, v := range params {
				p.params[k] = v
			}
		}
	}

	return true
}

// setupBOSHDefaults sets up BOSH director connection and defaults.
func (p *provisioningPhase) setupBOSHDefaults(ctx context.Context, plan services.Plan) (map[interface{}]interface{}, bool) {
	startTime := time.Now()

	p.broker.trackProgress(ctx, p.instanceID, "provision", "Setting up deployment defaults", 0, p.params, p.logger)

	defaults := make(map[interface{}]interface{})
	defaults["name"] = plan.ID + "-" + p.instanceID
	p.params["instance_id"] = p.instanceID

	// Get BOSH director UUID
	p.logger.Debug("Querying BOSH director for director UUID")
	p.broker.trackProgress(ctx, p.instanceID, "provision", "Connecting to BOSH director", 0, p.params, p.logger)

	boshStartTime := time.Now()

	info, err := p.broker.BOSH.GetInfo(ctx)
	if err != nil {
		p.logger.Error("Failed to get information about BOSH director: %s", err)
		p.failStep(ctx, "Failed to connect to BOSH", err)

		return nil, false
	}

	p.logger.Debug("BOSH director query completed in %v (UUID: %s)", time.Since(boshStartTime), info.UUID)
	defaults["director_uuid"] = info.UUID

	p.logger.Debug("Setup BOSH defaults completed in %v", time.Since(startTime))

	return defaults, true
}

// initializeService runs the service initialization script.
func (p *provisioningPhase) initializeService(ctx context.Context, plan services.Plan) bool {
	startTime := time.Now()

	// Set credentials environment variable
	err := os.Setenv("CREDENTIALS", "secret/"+p.instanceID)
	if err != nil {
		p.logger.Error("Failed to set CREDENTIALS environment variable: %s", err)
		p.broker.failWithTracking(ctx, p.instanceID, "provision", "Failed to set environment variables", p.params, p.logger)

		return false
	}

	// Run init script
	p.logger.Info("Running service init script for plan %s", plan.ID)
	p.broker.trackProgress(ctx, p.instanceID, "provision", "Initializing service deployment", 0, p.params, p.logger)

	initStartTime := time.Now()

	err = manifest.InitManifest(ctx, plan, p.instanceID)
	if err != nil {
		p.logger.Error("Service deployment initialization script failed: %s", err)
		p.failStep(ctx, "Service initialization failed", err)

		return false
	}

	p.logger.Debug("Init script execution completed in %v", time.Since(initStartTime))

	// Store init script output in vault if script was run
	_, statErr := os.Stat(plan.InitScriptPath)
	if statErr == nil {
		p.logger.Debug("Storing init script in vault at %s/init", p.instanceID)

		initScriptContent, readErr := os.ReadFile(plan.InitScriptPath)
		if readErr == nil {
			recordCtx, cancelRecord := recordContext(ctx)
			err = p.broker.Vault.Put(recordCtx, p.instanceID+"/init", map[string]interface{}{
				"script":      string(initScriptContent),
				"executed_at": time.Now().Format(time.RFC3339),
			})

			cancelRecord()

			if err != nil {
				p.logger.Error("Failed to store init script in vault: %s", err)
				// Continue anyway as this is non-fatal
			}
		}
	}

	p.logger.Debug("Service initialization completed in %v", time.Since(startTime))

	return true
}

// generateAndStoreManifest generates the BOSH manifest and stores it in Vault.
func (p *provisioningPhase) generateAndStoreManifest(ctx context.Context, plan services.Plan, defaults map[interface{}]interface{}) (string, bool) {
	startTime := time.Now()

	p.logger.Info("Generating BOSH deployment manifest for %s", defaults["name"])
	p.broker.trackProgress(ctx, p.instanceID, "provision", "Generating BOSH deployment manifest", 0, p.params, p.logger)

	manifestStr, err := manifest.GenManifest(plan, defaults, utils.Wrap("meta.params", p.params))
	if err != nil {
		p.logger.Error("Failed to generate service deployment manifest: %s", err)
		p.broker.failWithTracking(ctx, p.instanceID, "provision", fmt.Sprintf("Manifest generation failed: %s", err), p.params, p.logger)

		return "", false
	}

	genDuration := time.Since(startTime)
	p.logger.Debug("Manifest generated in %v (size: %d bytes)", genDuration, len(manifestStr))

	// Store manifest in vault immediately - this is critical for UI visibility
	p.logger.Info("Storing deployment manifest in Vault at %s/manifest", p.instanceID)
	p.broker.trackProgress(ctx, p.instanceID, "provision", "Storing deployment manifest in Vault", 0, p.params, p.logger)

	storeStartTime := time.Now()

	recordCtx, cancelRecord := recordContext(ctx)
	err = p.broker.Vault.Put(recordCtx, p.instanceID+"/manifest", map[string]interface{}{
		"manifest":  manifestStr,
		"stored_at": time.Now().Format(time.RFC3339),
	})

	cancelRecord()

	if err != nil {
		p.logger.Error("FATAL: Failed to store manifest in Vault at %s/manifest: %s", p.instanceID, err)
		p.broker.failWithTracking(ctx, p.instanceID, "provision", fmt.Sprintf("Failed to store manifest in Vault: %s", err), p.params, p.logger)

		return "", false
	}

	storeDuration := time.Since(storeStartTime)
	p.logger.Info("Manifest successfully stored in Vault in %v (total manifest generation+storage: %v)", storeDuration, time.Since(startTime))

	return manifestStr, true
}

// uploadReleases uploads necessary releases to BOSH director.
func (p *provisioningPhase) uploadReleases(ctx context.Context, manifestStr string) bool {
	p.logger.Debug("uploading releases (if necessary) to BOSH director")
	p.broker.trackProgress(ctx, p.instanceID, "provision", "Uploading BOSH releases", 0, p.params, p.logger)

	err := manifest.UploadReleasesFromManifest(ctx, manifestStr, p.broker.BOSH, p.logger)
	if err != nil {
		p.logger.Error("failed to upload service deployment releases: %s", err)
		p.failStep(ctx, "Release upload failed", err)

		return false
	}

	return true
}

// createDeployment creates the BOSH deployment and handles task ID resolution.
func (p *provisioningPhase) createDeployment(ctx context.Context, plan services.Plan, manifestStr string) bool {
	p.logger.Info("Deploying service instance to BOSH director")
	p.broker.trackProgress(ctx, p.instanceID, "provision", "Creating BOSH deployment", 0, p.params, p.logger)

	deploymentName := plan.ID + "-" + p.instanceID

	task, err := p.broker.BOSH.CreateDeployment(ctx, manifestStr)
	if err != nil {
		p.logger.Error("Failed to create service deployment: %s", err)

		// bosh-cli reports a deploy task that failed as an error. When BOSH
		// started the task, record it, so LastOperation answers from the
		// task's own state and cleans up the failed deployment.
		//
		// The lookup runs under its own bound, because the provision context
		// may be what ended the deploy. Reading the events on it would fail,
		// and the instance would be recorded as failed without a task, which
		// LastOperation cannot recover from.
		lookupCtx, cancelLookup := recordContext(ctx)
		createTaskID := p.broker.resolveDeploymentTask(lookupCtx, deploymentName, boshEventActionCreate, p.logger)

		cancelLookup()

		if createTaskID > 0 {
			p.broker.recordTask(ctx, p.instanceID, operationTypeProvision, fmt.Sprintf("BOSH deployment task %d did not succeed: %s", createTaskID, err), createTaskID, p.logger)

			return false
		}

		p.failStep(ctx, "Deployment creation failed", err)

		return false
	}

	p.logger.Info("Deployment finished on the director, reported task ID: %d", task.ID)

	// A deprovision may have removed the instance while the deploy was running.
	// Recording the task now would resurrect the index entry for a service
	// instance Cloud Foundry no longer has, so tear the deployment down instead.
	if !p.instanceStillIndexed(ctx) {
		p.logger.Warning("Instance %s was deprovisioned while deployment %s was being created; deleting the deployment instead of recording it", p.instanceID, deploymentName)
		p.broker.tearDownAbandonedDeployment(ctx, p.instanceID, deploymentName, p.logger)

		return false
	}

	// The adapter reports the deployment's newest task, which can be one
	// someone ran on the deployment while bosh-cli waited on the deploy. The
	// deployment-level create event names the deploy task itself. When no
	// event names it yet, zero is recorded and LastOperation looks again.
	actualTaskID := p.broker.resolveDeploymentTask(ctx, deploymentName, boshEventActionCreate, p.logger)
	if actualTaskID != task.ID {
		p.logger.Info("Recording create deployment task %d from BOSH events for %s (the director adapter reported task %d)", actualTaskID, deploymentName, task.ID)
	}

	// Update tracking with deployment status - store the actual task ID
	p.logger.Debug("about to store task ID %d in vault for instance %s", actualTaskID, p.instanceID)

	description := fmt.Sprintf("BOSH deployment in progress (task %d)", actualTaskID)
	if actualTaskID == 0 {
		description = "BOSH deployment returned; waiting for BOSH events to name the deploy task"
	}

	recordCtx, cancelRecord := recordContext(ctx)
	defer cancelRecord()

	err = p.broker.Vault.TrackProgress(recordCtx, p.instanceID, "provision", description, actualTaskID, p.params)
	if err != nil {
		p.logger.Error("CRITICAL: failed to store service status in the vault: %s", err)
	} else {
		p.logger.Info("Successfully stored task ID %d in vault for instance %s", actualTaskID, p.instanceID)
	}

	p.logger.Info("Async provisioning handed off to BOSH for instance %s", p.instanceID)

	return true
}

// instanceStillIndexed reports whether the instance is still present in the
// vault index. When the lookup itself fails the instance is assumed to be
// present, so a vault hiccup never turns a finished provision into a delete.
func (p *provisioningPhase) instanceStillIndexed(ctx context.Context) bool {
	_, exists, err := p.broker.Vault.FindInstance(ctx, p.instanceID)
	if err != nil {
		p.logger.Error("could not check whether instance %s is still indexed, assuming it is: %s", p.instanceID, err)

		return true
	}

	return exists
}

// tearDownAbandonedDeployment deletes a deployment whose instance was removed
// from the index while the create deployment task was still running. It runs
// the same delete-with-retry and background monitoring as deprovisionAsync and
// leaves the instance out of the index.
func (b *Broker) tearDownAbandonedDeployment(ctx context.Context, instanceID, deploymentName string, logger logger.Logger) {
	// The provision context may be nearly spent by now, so the teardown keeps
	// its values and sets its own deadline. The delete waits on the director
	// task, so it gets the same bound as a regular deprovision.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskMonitorTimeout)
	defer cancel()

	recordCtx, cancelRecord := recordContext(ctx)
	err := b.Vault.TrackProgress(recordCtx, instanceID, "deprovision", "Deleting deployment that finished after the instance was deprovisioned", 0, nil)

	cancelRecord()

	if err != nil {
		logger.Error("failed to track abandoned deployment cleanup: %s", err)
	}

	phase := &deprovisioningPhase{
		broker:         b,
		instanceID:     instanceID,
		deploymentName: deploymentName,
		logger:         logger,
	}

	task, ok := phase.deleteDeploymentWithRetry(ctx)
	if !ok {
		return
	}

	if task.ID == 0 {
		phase.handleDeploymentCleanedDuringRetry(ctx)

		return
	}

	deleteTask := phase.resolveDeleteTask(ctx)
	if deleteTask == nil {
		return
	}

	phase.trackDeletionProgress(ctx, deleteTask)

	go phase.monitorAndCleanupTask(b.serviceContext(), deleteTask)
}

// deprovisioningPhase represents a phase in the deprovisioning process.
type deprovisioningPhase struct {
	broker         *Broker
	instanceID     string
	deploymentName string
	logger         logger.Logger
}

// checkDeploymentExists checks if deployment exists and handles cleanup if not.
func (d *deprovisioningPhase) checkDeploymentExists(ctx context.Context) (bool, bool) {
	d.broker.trackProgress(ctx, d.instanceID, "deprovision", "Checking BOSH deployment status", 0, nil, d.logger)

	deployment, err := d.broker.BOSH.GetDeployment(ctx, d.deploymentName)
	if err != nil {
		if errors.Is(err, bosh.ErrDeploymentNotFound) {
			d.logger.Info("Deployment %s not found, marking as deleted", d.deploymentName)

			return false, d.handleDeploymentNotFound(ctx)
		}

		// Existence is unknown. Treating this as "not found" would drop the
		// index entry while the deployment keeps running on the director.
		d.logger.Error("Failed to check whether deployment %s exists: %s", d.deploymentName, err)

		// A deprovision that the broker's shutdown or its time bound ended has
		// not failed. The director may be deleting the deployment already, and
		// LastOperation settles the instance from what the director reports.
		if ctx.Err() != nil {
			d.logger.Warning("Deprovision of instance %s stopped before the deployment status was known (%s); LastOperation settles it", d.instanceID, ctx.Err())

			return false, false
		}

		d.broker.failWithTracking(ctx, d.instanceID, "deprovision", fmt.Sprintf("Failed to check deployment status: %s", err), nil, d.logger)

		return false, false
	}

	// An empty manifest means the first deploy has not finished yet. The
	// deployment exists and must be deleted like any other.
	if deployment.Manifest == "" {
		d.logger.Info("Deployment %s exists but has no manifest yet (deploy still in flight); proceeding with deletion", d.deploymentName)
	}

	return true, true
}

// handleDeploymentNotFound handles cleanup when deployment doesn't exist.
func (d *deprovisioningPhase) handleDeploymentNotFound(ctx context.Context) bool {
	d.broker.trackProgress(ctx, d.instanceID, "deprovision", "Deployment not found, cleanup only", 0, nil, d.logger)

	d.broker.cleanupDeploymentCredentials(ctx, d.instanceID, d.deploymentName, d.logger)

	if !d.removeFromIndex(ctx, "Removing from service index") {
		return false
	}

	d.broker.trackProgress(ctx, d.instanceID, "deprovision", "Deprovisioning completed (no deployment)", 0, nil, d.logger)

	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	err := d.broker.Vault.Track(recordCtx, d.instanceID, "deprovision", 1, nil)
	if err != nil {
		d.logger.Error("failed to track completion: %s", err)
	}

	return true
}

// deleteDeploymentWithRetry attempts to delete deployment with retry logic.
func (d *deprovisioningPhase) deleteDeploymentWithRetry(ctx context.Context) (*bosh.Task, bool) {
	d.broker.trackProgress(ctx, d.instanceID, "deprovision", "Initiating BOSH deployment deletion with retry", 0, nil, d.logger)

	task, err := d.broker.retryDeleteDeployment(ctx, d.deploymentName, d.instanceID, defaultDeleteRetryAttempts)
	if err != nil {
		d.logger.Error("Failed to delete BOSH deployment %s: %s", d.deploymentName, err)

		// The director's delete task keeps running when the broker stops, and
		// LastOperation adopts it from the director's events. The retry loop
		// has left a progress record, so no failure is recorded here.
		if ctx.Err() != nil {
			d.logger.Warning("Deprovision of instance %s stopped before the delete finished (%s); LastOperation settles it", d.instanceID, ctx.Err())

			return nil, false
		}

		d.broker.failWithTracking(ctx, d.instanceID, "deprovision", fmt.Sprintf("Deployment deletion failed: %s", err), nil, d.logger)

		return nil, false
	}

	return task, true
}

// handleDeploymentCleanedDuringRetry handles case where deployment was cleaned up during retry.
func (d *deprovisioningPhase) handleDeploymentCleanedDuringRetry(ctx context.Context) bool {
	d.logger.Info("Deployment %s was cleaned up during retry attempts", d.deploymentName)

	d.broker.cleanupDeploymentCredentials(ctx, d.instanceID, d.deploymentName, d.logger)

	if !d.removeFromIndex(ctx, "Removing from service index after cleanup") {
		return false
	}

	d.broker.trackProgress(ctx, d.instanceID, "deprovision", "Deprovisioning completed (cleanup during retry)", 0, nil, d.logger)

	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	err := d.broker.Vault.Track(recordCtx, d.instanceID, "deprovision", 0, nil)
	if err != nil {
		d.logger.Error("failed to track completion: %s", err)
	}

	return true
}

// removeFromIndex removes the instance from the vault index.
func (d *deprovisioningPhase) removeFromIndex(ctx context.Context, message string) bool {
	d.broker.trackProgress(ctx, d.instanceID, "deprovision", message, 0, nil, d.logger)

	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	err := d.broker.Vault.Index(recordCtx, d.instanceID, nil)
	if err != nil {
		d.logger.Error("failed to remove service from vault index: %s", err)

		return false
	}

	return true
}

// storeDeletedTimestamp stores the deleted_at timestamp in Vault metadata.
func (d *deprovisioningPhase) storeDeletedTimestamp(ctx context.Context) {
	d.logger.Debug("storing deleted_at timestamp in Vault")

	deletedAt := time.Now()

	// Get existing metadata
	var metadata map[string]interface{}

	getCtx, cancelGet := recordContext(ctx)
	exists, err := d.broker.Vault.Get(getCtx, d.instanceID+"/metadata", &metadata)

	cancelGet()

	if err != nil {
		// A fresh map written over metadata that could not be read would
		// erase the history and every timestamp already recorded.
		d.logger.Error("failed to read %s/metadata before recording deleted_at, leaving it unchanged: %s", d.instanceID, err)

		return
	}

	if !exists || metadata == nil {
		metadata = make(map[string]interface{})
	}

	// The first deleted_at is the time the deployment was confirmed gone. A
	// poll that finds the entry still indexed reaches the broker's own write,
	// and neither may move it.
	indexCtx, cancelIndex := recordContext(ctx)
	indexEntry := d.broker.lifecycleIndexEntry(indexCtx, d.instanceID, d.logger)

	cancelIndex()

	if deletionAlreadyRecorded(metadata, indexEntry) {
		d.logger.Debug("deleted_at is already recorded for %s, keeping it", d.instanceID)

		return
	}

	// Add deleted_at
	metadata["deleted_at"] = deletedAt.Format(time.RFC3339)

	// Store updated metadata
	putCtx, cancelPut := recordContext(ctx)
	defer cancelPut()

	err = d.broker.Vault.Put(putCtx, d.instanceID+"/metadata", metadata)
	if err != nil {
		d.logger.Error("failed to store deleted_at timestamp: %s", err)
	}
}

// handleTaskSuccess performs cleanup after successful task completion.
func (d *deprovisioningPhase) handleTaskSuccess(ctx context.Context) {
	// Verify deployment is actually gone
	_, deploymentErr := d.broker.BOSH.GetDeployment(ctx, d.deploymentName)
	if deploymentErr == nil {
		d.logger.Error("Task succeeded but deployment %s still exists, not removing from index", d.deploymentName)

		return
	}

	if !errors.Is(deploymentErr, bosh.ErrDeploymentNotFound) {
		d.logger.Error("Could not confirm that deployment %s is deleted, leaving the index entry for LastOperation: %s", d.deploymentName, deploymentErr)

		return
	}

	d.logger.Info("Deployment %s confirmed deleted, performing cleanup", d.deploymentName)

	d.broker.cleanupDeploymentCredentials(ctx, d.instanceID, d.deploymentName, d.logger)

	// Store deleted_at timestamp
	d.storeDeletedTimestamp(ctx)

	// Remove from index
	if !d.removeFromIndex(ctx, "Removing from service index after confirmed deletion") {
		d.logger.Error("Failed to remove instance from index after successful deletion")

		return
	}

	d.logger.Info("Successfully completed cleanup for instance %s", d.instanceID)
}

// monitorAndCleanupTask monitors BOSH task completion and performs cleanup.
//
// This function implements background monitoring to ensure cleanup happens even if
// Cloud Foundry stops polling LastOperation. It polls the task status every 10 seconds
// and performs cleanup when the task completes successfully.
//
// This provides defense-in-depth alongside the LastOperation cleanup mechanism.
func (d *deprovisioningPhase) monitorAndCleanupTask(ctx context.Context, task *bosh.Task) {
	defer d.broker.noteMonitorExited(d.instanceID)

	d.logger.Info("Monitoring BOSH task %d for completion", task.ID)

	ticker := time.NewTicker(taskPollInterval())
	defer ticker.Stop()

	timeout := time.After(taskMonitorTimeout)

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("Context cancelled, stopping task monitoring for instance %s", d.instanceID)

			return

		case <-timeout:
			d.logger.Warning("Timeout waiting for task %d completion after 2 hours", task.ID)

			return

		case <-ticker.C:
			// Poll BOSH for task status
			boshTask, err := d.broker.BOSH.GetTask(ctx, task.ID)
			if err != nil {
				d.logger.Error("Failed to get task %d status: %s", task.ID, err)

				continue
			}

			d.logger.Debug("Task %d state: %s", task.ID, boshTask.State)

			switch boshTask.State {
			case "done":
				d.logger.Info("Task %d completed successfully, performing cleanup", task.ID)
				d.handleTaskSuccess(ctx)

				return

			case "error", "cancelled", "timeout":
				d.logger.Error("Task %d failed with state: %s", task.ID, boshTask.State)

				return

			default:
				continue
			}
		}
	}
}

// noteMonitorExited reports that a deprovision monitor goroutine has returned
// to the monitorExited hook. It does nothing when no hook is set.
func (b *Broker) noteMonitorExited(instanceID string) {
	if b.monitorExited != nil {
		b.monitorExited(instanceID)
	}
}

// resolveDeleteTask names the delete task the broker started. bosh-cli returns
// once the delete task has finished, and the adapter then reports the
// deployment's newest task, which can be one someone or vm-monitor ran on the
// deployment in the meantime. The deployment-level delete event names the
// delete task itself. When no event names it, nil is returned after recording
// that, and LastOperation looks again on the next poll.
func (d *deprovisioningPhase) resolveDeleteTask(ctx context.Context) *bosh.Task {
	taskID := d.broker.resolveDeploymentTask(ctx, d.deploymentName, boshEventActionDelete, d.logger)
	if taskID == 0 {
		d.broker.trackProgress(ctx, d.instanceID, "deprovision", "BOSH deletion returned; waiting for BOSH events to name the delete task", 0, nil, d.logger)

		return nil
	}

	return &bosh.Task{ID: taskID, Description: "delete deployment " + d.deploymentName}
}

// trackDeletionProgress tracks the ongoing deletion task.
func (d *deprovisioningPhase) trackDeletionProgress(ctx context.Context, task *bosh.Task) {
	d.logger.Info("Delete operation started successfully, BOSH task ID: %d", task.ID)

	recordCtx, cancel := recordContext(ctx)
	defer cancel()

	err := d.broker.Vault.TrackProgress(recordCtx, d.instanceID, "deprovision", fmt.Sprintf("BOSH deletion in progress (task %d)", task.ID), task.ID, nil)
	if err != nil {
		d.logger.Error("failed to store deprovision status in the vault: %s", err)
	}

	d.logger.Info("Async deprovisioning handed off to BOSH for instance %s, will be removed from index when deletion completes", d.instanceID)
}

// provisionAsync handles the actual provisioning work in a background goroutine.
func (b *Broker) provisionAsync(ctx context.Context, instanceID string, details interface{}, plan services.Plan) {
	startTime := time.Now()
	logger := logger.Get().Named("broker")
	logger.Info("Starting async provisioning for instance %s (plan: %s)", instanceID, plan.ID)

	// Initialize params for tracking
	params := make(map[interface{}]interface{})

	// Track initial state
	recordCtx, cancelRecord := recordContext(ctx)
	err := b.Vault.TrackProgress(recordCtx, instanceID, "provision", "Starting provisioning process", 0, params)

	cancelRecord()

	if err != nil {
		logger.Error("failed to initialize provision tracking: %s", err)

		return
	}

	// Create provisioning phase helper
	phase := &provisioningPhase{
		broker:     b,
		instanceID: instanceID,
		logger:     logger,
		params:     params,
	}

	// Execute provisioning phases with timing
	phaseStart := time.Now()

	if !phase.parseParameters(ctx, details) {
		return
	}

	logger.Debug("Phase 1/5: Parameter parsing completed in %v", time.Since(phaseStart))

	phaseStart = time.Now()

	defaults, success := phase.setupBOSHDefaults(ctx, plan)
	if !success {
		return
	}

	logger.Debug("Phase 2/5: BOSH defaults setup completed in %v (cumulative: %v)", time.Since(phaseStart), time.Since(startTime))

	phaseStart = time.Now()

	if !phase.initializeService(ctx, plan) {
		return
	}

	logger.Debug("Phase 3/5: Service initialization completed in %v (cumulative: %v)", time.Since(phaseStart), time.Since(startTime))

	phaseStart = time.Now()

	manifest, ok := phase.generateAndStoreManifest(ctx, plan, defaults)
	if !ok {
		return
	}

	logger.Info("Phase 4/5: Manifest generation and storage completed in %v (cumulative: %v) - manifest now available in UI", time.Since(phaseStart), time.Since(startTime))

	phaseStart = time.Now()

	if !phase.uploadReleases(ctx, manifest) {
		return
	}

	logger.Debug("Phase 5/5: Release upload completed in %v (cumulative: %v)", time.Since(phaseStart), time.Since(startTime))

	phaseStart = time.Now()

	phase.createDeployment(ctx, plan, manifest)
	logger.Info("Deployment creation completed in %v (total provisioning time: %v)", time.Since(phaseStart), time.Since(startTime))
}

// deprovisionAsync handles the actual deprovisioning work in a background goroutine.
//
// Cleanup Strategy:
// This function implements a dual cleanup mechanism for reliability:
//
//  1. Background Task Monitoring: After starting the BOSH delete task, we launch
//     a background goroutine that polls the task status every 10 seconds. When the
//     task completes successfully and the deployment is confirmed deleted, this
//     goroutine performs cleanup (stores deleted_at timestamp and removes instance
//     from Vault index).
//
//  2. LastOperation Polling: Cloud Foundry polls the LastOperation endpoint, which
//     also checks task status. When it detects successful completion, it performs
//     the same cleanup operations.
//
// This dual approach ensures cleanup happens reliably even if:
// - Cloud Foundry stops polling LastOperation
// - The broker process restarts between task completion and next poll
// - There's a timing/race condition in the polling mechanism
//
// Both cleanup paths are idempotent (Index removal and timestamp storage can be
// repeated safely), so there's no harm if both execute.
func (b *Broker) deprovisionAsync(ctx context.Context, instanceID string, instance *vaultPkg.Instance) {
	logger := logger.Get().Named("broker")
	logger.Info("Starting async deprovisioning for instance %s", instanceID)

	// Track initial state
	recordCtx, cancelRecord := recordContext(ctx)
	err := b.Vault.TrackProgress(recordCtx, instanceID, "deprovision", "Starting deprovisioning process", 0, nil)

	cancelRecord()

	if err != nil {
		logger.Error("failed to initialize deprovision tracking: %s", err)

		return
	}

	deploymentName := deploymentNameForInstance(instanceID, instance)
	logger.Info("Deleting BOSH deployment %s", deploymentName)

	// Create deprovisioning phase helper
	phase := &deprovisioningPhase{
		broker:         b,
		instanceID:     instanceID,
		deploymentName: deploymentName,
		logger:         logger,
	}

	// Check if deployment exists
	exists, successful := phase.checkDeploymentExists(ctx)
	if !successful {
		return
	}

	if !exists {
		return // Already handled by checkDeploymentExists
	}

	// Delete deployment with retry logic
	task, ok := phase.deleteDeploymentWithRetry(ctx)
	if !ok {
		return
	}

	if task.ID == 0 {
		// Deployment was already gone during retry
		phase.handleDeploymentCleanedDuringRetry(ctx)

		return
	}

	deleteTask := phase.resolveDeleteTask(ctx)
	if deleteTask == nil {
		return
	}

	// Track the ongoing deletion
	phase.trackDeletionProgress(ctx, deleteTask)

	// Launch background task monitor for cleanup
	// The monitor outlives the request, so it runs under the service context,
	// which a shutdown ends. LastOperation finishes a deprovision whose
	// monitor is gone.
	go phase.monitorAndCleanupTask(b.serviceContext(), deleteTask)

	logger.Info("Launched background task monitor for instance %s (task %d)", instanceID, deleteTask.ID)
}

// retryDeleteDeployment attempts to delete a BOSH deployment with retry logic.
func (b *Broker) retryDeleteDeployment(ctx context.Context, deploymentName string, instanceID string, maxRetries int) (*bosh.Task, error) {
	logger := logger.Get().Named("broker")

	for attempt := 1; attempt <= maxRetries; attempt++ {
		logger.Debug("Deletion attempt %d/%d for deployment %s", attempt, maxRetries, deploymentName)

		// Check if deployment still exists
		_, err := b.BOSH.GetDeployment(ctx, deploymentName)
		if err != nil {
			if errors.Is(err, bosh.ErrDeploymentNotFound) {
				// Deployment not found is success for deletion
				logger.Info("Deployment %s no longer exists, deletion successful", deploymentName)

				return &bosh.Task{ID: 0, State: "done"}, nil
			}

			// Existence is unknown; let the delete attempt decide rather than
			// declaring success on a director error.
			logger.Warning("Could not confirm whether deployment %s exists (%s); attempting deletion anyway", deploymentName, err)
		}

		// Attempt deletion
		task, err := b.BOSH.DeleteDeployment(ctx, deploymentName)
		if err == nil {
			logger.Info("Deletion task started on attempt %d for deployment %s (task %d)", attempt, deploymentName, task.ID)

			return task, nil
		}

		logger.Error("Deletion attempt %d failed for deployment %s: %s", attempt, deploymentName, err)

		// Track the retry attempt without a task, which keeps the operation in
		// progress. Only the caller records the deprovision as failed, once
		// every attempt is spent.
		recordCtx, cancelRecord := recordContext(ctx)
		trackErr := b.Vault.TrackProgress(recordCtx, instanceID, "deprovision", fmt.Sprintf("Deletion retry %d/%d failed: %s", attempt, maxRetries, err), 0, nil)

		cancelRecord()

		if trackErr != nil {
			logger.Error("failed to track retry attempt: %s", trackErr)
		}

		// Wait before retry (except on last attempt)
		if attempt < maxRetries {
			waitTime := time.Duration(attempt) * retryBackoffMultiplier
			logger.Debug("Waiting %v before retry attempt %d", waitTime, attempt+1)

			timer := time.NewTimer(waitTime)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()

				return nil, fmt.Errorf("stopped deleting deployment %s for instance %s after attempt %d of %d because the broker context ended (%w); check whether the broker is shutting down and whether the deployment still exists on the director", deploymentName, instanceID, attempt, maxRetries, ctx.Err())
			}
		}
	}

	return nil, fmt.Errorf("%w: %s after %d attempts", ErrFailedToDeleteDeployment, deploymentName, maxRetries)
}
