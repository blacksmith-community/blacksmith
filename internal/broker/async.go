package broker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/manifest"
	"blacksmith/internal/services"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/utils"
	vaultPkg "blacksmith/pkg/vault"
	"gopkg.in/yaml.v2"
)

// Constants for async operations.
const (
	// Retry backoff multiplier.
	retryBackoffMultiplier = 5 * time.Second

	// Task monitoring polling interval.
	taskPollInterval = 10 * time.Second

	// Task monitoring timeout duration.
	taskMonitorTimeout = 2 * time.Hour
)

// Static errors for err113 compliance.
var (
	ErrFailedToDeleteDeployment = errors.New("failed to delete deployment after maximum attempts")
)

// Helper methods for provisionAsync refactoring

// trackProgress is a helper to track progress with error logging.
func (b *Broker) trackProgress(ctx context.Context, instanceID, operation, message string, _ int, params map[interface{}]interface{}, l logger.Logger) {
	err := b.Vault.TrackProgress(ctx, instanceID, operation, message, 0, params)
	if err != nil {
		l.Error("failed to track progress (%s): %s", message, err)
	}
}

// failWithTracking tracks a failure.
func (b *Broker) failWithTracking(ctx context.Context, instanceID, operation, message string, params map[interface{}]interface{}, l logger.Logger) {
	err := b.Vault.TrackProgress(ctx, instanceID, operation, message, -1, params)
	if err != nil {
		l.Error("failed to track progress (%s): %s", message, err)
	}
}

// provisioningPhase represents a phase in the provisioning process.
type provisioningPhase struct {
	broker     *Broker
	instanceID string
	logger     logger.Logger
	params     map[interface{}]interface{}
}

// parseParameters extracts and parses parameters from the details.
func (p *provisioningPhase) parseParameters(ctx context.Context, details interface{}) bool {
	p.broker.trackProgress(ctx, p.instanceID, "provision", "Parsing service parameters", 0, p.params, p.logger)

	if detailsMap, ok := details.(map[string]interface{}); ok {
		if rawParams, ok := detailsMap["raw_parameters"].([]byte); ok {
			err := yaml.Unmarshal(rawParams, &p.params)
			if err != nil {
				p.logger.Debug("Error unmarshalling params: %s", err)
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

	info, err := p.broker.BOSH.GetInfo()
	if err != nil {
		p.logger.Error("Failed to get information about BOSH director: %s", err)
		p.broker.failWithTracking(ctx, p.instanceID, "provision", fmt.Sprintf("Failed to connect to BOSH: %s", err), p.params, p.logger)

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
		p.broker.failWithTracking(ctx, p.instanceID, "provision", fmt.Sprintf("Service initialization failed: %s", err), p.params, p.logger)

		return false
	}

	p.logger.Debug("Init script execution completed in %v", time.Since(initStartTime))

	// Store init script output in vault if script was run
	_, statErr := os.Stat(plan.InitScriptPath)
	if statErr == nil {
		p.logger.Debug("Storing init script in vault at %s/init", p.instanceID)

		initScriptContent, readErr := os.ReadFile(plan.InitScriptPath)
		if readErr == nil {
			err = p.broker.Vault.Put(ctx, p.instanceID+"/init", map[string]interface{}{
				"script":      string(initScriptContent),
				"executed_at": time.Now().Format(time.RFC3339),
			})
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

	err = p.broker.Vault.Put(ctx, p.instanceID+"/manifest", map[string]interface{}{
		"manifest":  manifestStr,
		"stored_at": time.Now().Format(time.RFC3339),
	})
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

	err := manifest.UploadReleasesFromManifest(manifestStr, p.broker.BOSH, p.logger)
	if err != nil {
		p.logger.Error("failed to upload service deployment releases: %s", err)
		p.broker.failWithTracking(ctx, p.instanceID, "provision", fmt.Sprintf("Release upload failed: %s", err), p.params, p.logger)

		return false
	}

	return true
}

// createDeployment creates the BOSH deployment and handles task ID resolution.
func (p *provisioningPhase) createDeployment(ctx context.Context, plan services.Plan, manifestStr string) bool {
	p.logger.Info("Deploying service instance to BOSH director")
	p.broker.trackProgress(ctx, p.instanceID, "provision", "Creating BOSH deployment", 0, p.params, p.logger)

	task, err := p.broker.BOSH.CreateDeployment(manifestStr)
	if err != nil {
		p.logger.Error("Failed to create service deployment: %s", err)
		p.broker.failWithTracking(ctx, p.instanceID, "provision", fmt.Sprintf("Deployment creation failed: %s", err), p.params, p.logger)

		return false
	}

	p.logger.Info("Deployment started successfully, initial task ID: %d", task.ID)

	// Get the actual task ID from BOSH events if the returned task ID is a placeholder
	deploymentName := plan.ID + "-" + p.instanceID
	actualTaskID := task.ID

	if task.ID <= 1 {
		// This is a placeholder task ID (0 or 1), get the real one from BOSH events
		p.logger.Debug("Task ID is placeholder (%d), retrieving real task ID from BOSH events", task.ID)

		latestTask, _, err := p.broker.GetLatestDeploymentTask(deploymentName)
		if err != nil {
			p.logger.Info("Could not get latest task ID immediately, will use placeholder: %s", err)
			// Continue with placeholder ID - the LastOperation will pick up the real one later
		} else {
			actualTaskID = latestTask.ID
			p.logger.Info("Retrieved actual task ID from BOSH events: %d (was %d)", actualTaskID, task.ID)
		}
	}

	// Update tracking with deployment status - store the actual task ID
	p.logger.Debug("about to store task ID %d in vault for instance %s", actualTaskID, p.instanceID)

	err = p.broker.Vault.TrackProgress(ctx, p.instanceID, "provision", fmt.Sprintf("BOSH deployment in progress (task %d)", actualTaskID), actualTaskID, p.params)
	if err != nil {
		p.logger.Error("CRITICAL: failed to store service status in the vault: %s", err)
	} else {
		p.logger.Info("Successfully stored task ID %d in vault for instance %s", actualTaskID, p.instanceID)
	}

	p.logger.Info("Async provisioning handed off to BOSH for instance %s", p.instanceID)

	return true
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

	manifest, err := d.broker.BOSH.GetDeployment(d.deploymentName)
	if err != nil || manifest.Manifest == "" {
		d.logger.Info("Deployment %s not found, marking as deleted", d.deploymentName)

		return false, d.handleDeploymentNotFound(ctx)
	}

	return true, true
}

// handleDeploymentNotFound handles cleanup when deployment doesn't exist.
func (d *deprovisioningPhase) handleDeploymentNotFound(ctx context.Context) bool {
	d.broker.trackProgress(ctx, d.instanceID, "deprovision", "Deployment not found, cleanup only", 0, nil, d.logger)

	if !d.removeFromIndex(ctx, "Removing from service index") {
		return false
	}

	d.broker.trackProgress(ctx, d.instanceID, "deprovision", "Deprovisioning completed (no deployment)", 0, nil, d.logger)

	err := d.broker.Vault.Track(ctx, d.instanceID, "deprovision", 1, nil)
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
		d.logger.Error("Failed to delete BOSH deployment %s after retries: %s", d.deploymentName, err)
		d.broker.failWithTracking(ctx, d.instanceID, "deprovision", fmt.Sprintf("Deployment deletion failed after retries: %s", err), nil, d.logger)

		return nil, false
	}

	return task, true
}

// handleDeploymentCleanedDuringRetry handles case where deployment was cleaned up during retry.
func (d *deprovisioningPhase) handleDeploymentCleanedDuringRetry(ctx context.Context) bool {
	d.logger.Info("Deployment %s was cleaned up during retry attempts", d.deploymentName)

	if !d.removeFromIndex(ctx, "Removing from service index after cleanup") {
		return false
	}

	d.broker.trackProgress(ctx, d.instanceID, "deprovision", "Deprovisioning completed (cleanup during retry)", 0, nil, d.logger)

	err := d.broker.Vault.Track(ctx, d.instanceID, "deprovision", 0, nil)
	if err != nil {
		d.logger.Error("failed to track completion: %s", err)
	}

	return true
}

// removeFromIndex removes the instance from the vault index.
func (d *deprovisioningPhase) removeFromIndex(ctx context.Context, message string) bool {
	d.broker.trackProgress(ctx, d.instanceID, "deprovision", message, 0, nil, d.logger)

	err := d.broker.Vault.Index(ctx, d.instanceID, nil)
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

	exists, err := d.broker.Vault.Get(ctx, d.instanceID+"/metadata", &metadata)
	if err != nil || !exists {
		metadata = make(map[string]interface{})
	}

	// Add deleted_at
	metadata["deleted_at"] = deletedAt.Format(time.RFC3339)

	// Store updated metadata
	err = d.broker.Vault.Put(ctx, d.instanceID+"/metadata", metadata)
	if err != nil {
		d.logger.Error("failed to store deleted_at timestamp: %s", err)
	}
}

// handleTaskSuccess performs cleanup after successful task completion.
func (d *deprovisioningPhase) handleTaskSuccess(ctx context.Context) {
	// Verify deployment is actually gone
	_, deploymentErr := d.broker.BOSH.GetDeployment(d.deploymentName)
	if deploymentErr == nil {
		d.logger.Error("Task succeeded but deployment %s still exists, not removing from index", d.deploymentName)

		return
	}

	d.logger.Info("Deployment %s confirmed deleted, performing cleanup", d.deploymentName)

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
	d.logger.Info("Monitoring BOSH task %d for completion", task.ID)

	ticker := time.NewTicker(taskPollInterval)
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
			boshTask, err := d.broker.BOSH.GetTask(task.ID)
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

// trackDeletionProgress tracks the ongoing deletion task.
func (d *deprovisioningPhase) trackDeletionProgress(ctx context.Context, task *bosh.Task) {
	d.logger.Info("Delete operation started successfully, BOSH task ID: %d", task.ID)

	err := d.broker.Vault.TrackProgress(ctx, d.instanceID, "deprovision", fmt.Sprintf("BOSH deletion in progress (task %d)", task.ID), task.ID, nil)
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
	err := b.Vault.TrackProgress(ctx, instanceID, "provision", "Starting provisioning process", 0, params)
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
	err := b.Vault.TrackProgress(ctx, instanceID, "deprovision", "Starting deprovisioning process", 0, nil)
	if err != nil {
		logger.Error("failed to initialize deprovision tracking: %s", err)

		return
	}

	deploymentName := instance.PlanID + "-" + instanceID
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

	// Track the ongoing deletion
	phase.trackDeletionProgress(ctx, task)

	// Launch background task monitor for cleanup
	// Create a detached context that survives the HTTP request lifecycle
	// This ensures cleanup happens even if the request context is cancelled
	monitorCtx := context.WithoutCancel(ctx)
	go phase.monitorAndCleanupTask(monitorCtx, task)

	logger.Info("Launched background task monitor for instance %s (task %d)", instanceID, task.ID)
}

// retryDeleteDeployment attempts to delete a BOSH deployment with retry logic.
func (b *Broker) retryDeleteDeployment(ctx context.Context, deploymentName string, instanceID string, maxRetries int) (*bosh.Task, error) {
	logger := logger.Get().Named("broker")

	for attempt := 1; attempt <= maxRetries; attempt++ {
		logger.Debug("Deletion attempt %d/%d for deployment %s", attempt, maxRetries, deploymentName)

		// Check if deployment still exists
		_, err := b.BOSH.GetDeployment(deploymentName)
		if err != nil {
			// Deployment not found is success for deletion
			// Note: We treat any GetDeployment error as "not found" for deletion purposes
			logger.Info("Deployment %s no longer exists, deletion successful", deploymentName)

			return &bosh.Task{ID: 0, State: "done"}, nil //nolint:nilerr // Deployment not found is success for deletion
		}

		// Attempt deletion
		task, err := b.BOSH.DeleteDeployment(deploymentName)
		if err == nil {
			logger.Info("Deletion task started on attempt %d for deployment %s (task %d)", attempt, deploymentName, task.ID)

			return task, nil
		}

		logger.Error("Deletion attempt %d failed for deployment %s: %s", attempt, deploymentName, err)

		// Track retry attempt
		trackErr := b.Vault.TrackProgress(ctx, instanceID, "deprovision", fmt.Sprintf("Deletion retry %d/%d failed: %s", attempt, maxRetries, err), -1, nil)
		if trackErr != nil {
			logger.Error("failed to track retry attempt: %s", trackErr)
		}

		// Wait before retry (except on last attempt)
		if attempt < maxRetries {
			waitTime := time.Duration(attempt) * retryBackoffMultiplier
			logger.Debug("Waiting %v before retry attempt %d", waitTime, attempt+1)
			time.Sleep(waitTime)
		}
	}

	return nil, fmt.Errorf("%w: %s after %d attempts", ErrFailedToDeleteDeployment, deploymentName, maxRetries)
}
