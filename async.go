package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"blacksmith/bosh"
	"blacksmith/pkg/logger"
	"gopkg.in/yaml.v2"
)

// Constants for async operations.
const (
	// Retry backoff multiplier.
	retryBackoffMultiplier = 5 * time.Second
)

// Static errors for err113 compliance.
var (
	ErrFailedToDeleteDeployment = errors.New("failed to delete deployment after maximum attempts")
)

// Helper methods for provisionAsync refactoring

// trackProgress is a helper to track progress with error logging.
func (b *Broker) trackProgress(ctx context.Context, instanceID, operation, message string, taskID int, params map[interface{}]interface{}, l logger.Logger) {
	if err := b.Vault.TrackProgress(ctx, instanceID, operation, message, taskID, params); err != nil {
		l.Error("failed to track progress (%s): %s", message, err)
	}
}

// failWithTracking tracks a failure and returns false.
func (b *Broker) failWithTracking(ctx context.Context, instanceID, operation, message string, params map[interface{}]interface{}, l logger.Logger) bool {
	if err := b.Vault.TrackProgress(ctx, instanceID, operation, message, -1, params); err != nil {
		l.Error("failed to track progress (%s): %s", message, err)
	}

	return false
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
func (p *provisioningPhase) setupBOSHDefaults(ctx context.Context, plan Plan) (map[interface{}]interface{}, bool) {
	p.broker.trackProgress(ctx, p.instanceID, "provision", "Setting up deployment defaults", 0, p.params, p.logger)

	defaults := make(map[interface{}]interface{})
	defaults["name"] = plan.ID + "-" + p.instanceID
	p.params["instance_id"] = p.instanceID

	// Get BOSH director UUID
	p.logger.Debug("querying BOSH director for director UUID")
	p.broker.trackProgress(ctx, p.instanceID, "provision", "Connecting to BOSH director", 0, p.params, p.logger)

	info, err := p.broker.BOSH.GetInfo()
	if err != nil {
		p.logger.Error("failed to get information about BOSH director: %s", err)
		p.broker.failWithTracking(ctx, p.instanceID, "provision", fmt.Sprintf("Failed to connect to BOSH: %s", err), p.params, p.logger)

		return nil, false
	}

	defaults["director_uuid"] = info.UUID

	return defaults, true
}

// initializeService runs the service initialization script.
func (p *provisioningPhase) initializeService(ctx context.Context, plan Plan) bool {
	// Set credentials environment variable
	if err := os.Setenv("CREDENTIALS", "secret/"+p.instanceID); err != nil {
		p.logger.Error("failed to set CREDENTIALS environment variable: %s", err)
		p.broker.failWithTracking(ctx, p.instanceID, "provision", "Failed to set environment variables", p.params, p.logger)

		return false
	}

	// Run init script
	p.logger.Debug("running service init script")
	p.broker.trackProgress(ctx, p.instanceID, "provision", "Initializing service deployment", 0, p.params, p.logger)

	err := InitManifest(ctx, plan, p.instanceID)
	if err != nil {
		p.logger.Error("service deployment initialization script failed: %s", err)
		p.broker.failWithTracking(ctx, p.instanceID, "provision", fmt.Sprintf("Service initialization failed: %s", err), p.params, p.logger)

		return false
	}

	// Store init script output in vault if script was run
	if _, err := os.Stat(plan.InitScriptPath); err == nil {
		p.logger.Debug("storing init script in vault at %s/init", p.instanceID)

		initScriptContent, readErr := os.ReadFile(plan.InitScriptPath)
		if readErr == nil {
			err = p.broker.Vault.Put(ctx, p.instanceID+"/init", map[string]interface{}{
				"script":      string(initScriptContent),
				"executed_at": time.Now().Format(time.RFC3339),
			})
			if err != nil {
				p.logger.Error("failed to store init script in vault: %s", err)
				// Continue anyway as this is non-fatal
			}
		}
	}

	return true
}

// generateAndStoreManifest generates the BOSH manifest and stores it in Vault.
func (p *provisioningPhase) generateAndStoreManifest(ctx context.Context, plan Plan, defaults map[interface{}]interface{}) (string, bool) {
	p.logger.Info("Generating BOSH deployment manifest for %s", defaults["name"])
	p.broker.trackProgress(ctx, p.instanceID, "provision", "Generating BOSH deployment manifest", 0, p.params, p.logger)

	manifest, err := GenManifest(plan, defaults, wrap("meta.params", p.params))
	if err != nil {
		p.logger.Error("Failed to generate service deployment manifest: %s", err)
		p.broker.failWithTracking(ctx, p.instanceID, "provision", fmt.Sprintf("Manifest generation failed: %s", err), p.params, p.logger)

		return "", false
	}

	// Store manifest in vault
	p.broker.trackProgress(ctx, p.instanceID, "provision", "Storing deployment manifest in Vault", 0, p.params, p.logger)

	err = p.broker.Vault.Put(ctx, p.instanceID+"/manifest", map[string]interface{}{
		"manifest": manifest,
	})
	if err != nil {
		p.logger.Error("failed to store manifest in the vault: %s", err)
		// Continue anyway as this is non-fatal
	}

	return manifest, true
}

// uploadReleases uploads necessary releases to BOSH director.
func (p *provisioningPhase) uploadReleases(ctx context.Context, manifest string) bool {
	p.logger.Debug("uploading releases (if necessary) to BOSH director")
	p.broker.trackProgress(ctx, p.instanceID, "provision", "Uploading BOSH releases", 0, p.params, p.logger)

	err := UploadReleasesFromManifest(manifest, p.broker.BOSH, p.logger)
	if err != nil {
		p.logger.Error("failed to upload service deployment releases: %s", err)
		p.broker.failWithTracking(ctx, p.instanceID, "provision", fmt.Sprintf("Release upload failed: %s", err), p.params, p.logger)

		return false
	}

	return true
}

// createDeployment creates the BOSH deployment and handles task ID resolution.
func (p *provisioningPhase) createDeployment(ctx context.Context, plan Plan, manifest string) bool {
	p.logger.Info("Deploying service instance to BOSH director")
	p.broker.trackProgress(ctx, p.instanceID, "provision", "Creating BOSH deployment", 0, p.params, p.logger)

	task, err := p.broker.BOSH.CreateDeployment(manifest)
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
func (b *Broker) provisionAsync(ctx context.Context, instanceID string, details interface{}, plan Plan) {
	logger := logger.Get().Named("async " + instanceID)
	logger.Info("Starting async provisioning for instance %s", instanceID)

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

	// Execute provisioning phases
	if !phase.parseParameters(ctx, details) {
		return
	}

	defaults, ok := phase.setupBOSHDefaults(ctx, plan)
	if !ok {
		return
	}

	if !phase.initializeService(ctx, plan) {
		return
	}

	manifest, ok := phase.generateAndStoreManifest(ctx, plan, defaults)
	if !ok {
		return
	}

	if !phase.uploadReleases(ctx, manifest) {
		return
	}

	phase.createDeployment(ctx, plan, manifest)
}

// deprovisionAsync handles the actual deprovisioning work in a background goroutine.
func (b *Broker) deprovisionAsync(ctx context.Context, instanceID string, instance *Instance) {
	logger := logger.Get().Named("async " + instanceID)
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
	exists, ok := phase.checkDeploymentExists(ctx)
	if !ok {
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
}

// retryDeleteDeployment attempts to delete a BOSH deployment with retry logic.
func (b *Broker) retryDeleteDeployment(ctx context.Context, deploymentName string, instanceID string, maxRetries int) (*bosh.Task, error) {
	logger := logger.Get().Named("retryDeleteDeployment " + deploymentName)

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
