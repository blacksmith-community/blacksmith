package main

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v2"
)

// provisionAsync handles the actual provisioning work in a background goroutine
// provisionAsync handles the actual provisioning work in a background goroutine
func (b *Broker) provisionAsync(instanceID string, details interface{}, plan Plan) {
	l := Logger.Wrap("async %s", instanceID)
	l.Info("Starting async provisioning for instance %s", instanceID)

	// Initialize params for tracking
	params := make(map[interface{}]interface{})

	// Track initial state
	err := b.Vault.TrackProgress(instanceID, "provision", "Starting provisioning process", 0, params)
	if err != nil {
		l.Error("failed to initialize provision tracking: %s", err)
		return
	}

	// Parse parameters if available
	if err := b.Vault.TrackProgress(instanceID, "provision", "Parsing service parameters", 0, params); err != nil {
		l.Error("failed to track progress (parsing parameters): %s", err)
	}
	if detailsMap, ok := details.(map[string]interface{}); ok {
		if rawParams, ok := detailsMap["raw_parameters"].([]byte); ok {
			if err := yaml.Unmarshal(rawParams, &params); err != nil {
				l.Debug("Error unmarshalling params: %s", err)
			}
		}
	}

	// Setup defaults
	if err := b.Vault.TrackProgress(instanceID, "provision", "Setting up deployment defaults", 0, params); err != nil {
		l.Error("failed to track progress (setup defaults): %s", err)
	}
	defaults := make(map[interface{}]interface{})
	defaults["name"] = plan.ID + "-" + instanceID
	params["instance_id"] = instanceID

	// Get BOSH director UUID
	l.Debug("querying BOSH director for director UUID")
	if err := b.Vault.TrackProgress(instanceID, "provision", "Connecting to BOSH director", 0, params); err != nil {
		l.Error("failed to track progress (connecting to BOSH): %s", err)
	}
	info, err := b.BOSH.GetInfo()
	if err != nil {
		l.Error("failed to get information about BOSH director: %s", err)
		// Mark as failed
		if trackErr := b.Vault.TrackProgress(instanceID, "provision", fmt.Sprintf("Failed to connect to BOSH: %s", err), -1, params); trackErr != nil {
			l.Error("failed to track progress (BOSH connection failed): %s", trackErr)
		}
		return
	}
	defaults["director_uuid"] = info.UUID

	// Set credentials environment variable
	if err := os.Setenv("CREDENTIALS", fmt.Sprintf("secret/%s", instanceID)); err != nil {
		l.Error("failed to set CREDENTIALS environment variable: %s", err)
		if trackErr := b.Vault.TrackProgress(instanceID, "provision", "Failed to set environment variables", -1, params); trackErr != nil {
			l.Error("failed to track progress (env vars failed): %s", trackErr)
		}
		return
	}

	// Run init script
	l.Debug("running service init script")
	if err := b.Vault.TrackProgress(instanceID, "provision", "Initializing service deployment", 0, params); err != nil {
		l.Error("failed to track progress (init script): %s", err)
	}
	err = InitManifest(plan, instanceID)
	if err != nil {
		l.Error("service deployment initialization script failed: %s", err)
		if trackErr := b.Vault.TrackProgress(instanceID, "provision", fmt.Sprintf("Service initialization failed: %s", err), -1, params); trackErr != nil {
			l.Error("failed to track progress (init script failed): %s", trackErr)
		}
		return
	}

	// Store init script output in vault if script was run
	if _, err := os.Stat(plan.InitScriptPath); err == nil {
		l.Debug("storing init script in vault at %s/init", instanceID)
		initScriptContent, readErr := os.ReadFile(plan.InitScriptPath)
		if readErr == nil {
			err = b.Vault.Put(fmt.Sprintf("%s/init", instanceID), map[string]interface{}{
				"script":      string(initScriptContent),
				"executed_at": time.Now().Format(time.RFC3339),
			})
			if err != nil {
				l.Error("failed to store init script in vault: %s", err)
				// Continue anyway as this is non-fatal
			}
		}
	}

	// Generate manifest
	l.Info("Generating BOSH deployment manifest for %s", defaults["name"])
	if err := b.Vault.TrackProgress(instanceID, "provision", "Generating BOSH deployment manifest", 0, params); err != nil {
		l.Error("failed to track progress (generating manifest): %s", err)
	}
	manifest, err := GenManifest(plan, defaults, wrap("meta.params", params))
	if err != nil {
		l.Error("Failed to generate service deployment manifest: %s", err)
		if trackErr := b.Vault.TrackProgress(instanceID, "provision", fmt.Sprintf("Manifest generation failed: %s", err), -1, params); trackErr != nil {
			l.Error("failed to track progress (manifest generation failed): %s", trackErr)
		}
		return
	}

	// Store manifest in vault
	if err := b.Vault.TrackProgress(instanceID, "provision", "Storing deployment manifest in Vault", 0, params); err != nil {
		l.Error("failed to track progress (storing manifest): %s", err)
	}
	err = b.Vault.Put(fmt.Sprintf("%s/manifest", instanceID), map[string]interface{}{
		"manifest": manifest,
	})
	if err != nil {
		l.Error("failed to store manifest in the vault: %s", err)
		// Continue anyway as this is non-fatal
	}

	// Upload releases
	l.Debug("uploading releases (if necessary) to BOSH director")
	if err := b.Vault.TrackProgress(instanceID, "provision", "Uploading BOSH releases", 0, params); err != nil {
		l.Error("failed to track progress (uploading releases): %s", err)
	}
	err = UploadReleasesFromManifest(manifest, b.BOSH, l)
	if err != nil {
		l.Error("failed to upload service deployment releases: %s", err)
		if trackErr := b.Vault.TrackProgress(instanceID, "provision", fmt.Sprintf("Release upload failed: %s", err), -1, params); trackErr != nil {
			l.Error("failed to track progress (release upload failed): %s", trackErr)
		}
		return
	}

	// Create deployment
	l.Info("Deploying service instance to BOSH director")
	if err := b.Vault.TrackProgress(instanceID, "provision", "Creating BOSH deployment", 0, params); err != nil {
		l.Error("failed to track progress (creating deployment): %s", err)
	}
	task, err := b.BOSH.CreateDeployment(manifest)
	if err != nil {
		l.Error("Failed to create service deployment: %s", err)
		if trackErr := b.Vault.TrackProgress(instanceID, "provision", fmt.Sprintf("Deployment creation failed: %s", err), -1, params); trackErr != nil {
			l.Error("failed to track progress (deployment creation failed): %s", trackErr)
		}
		return
	}

	l.Info("Deployment started successfully, BOSH task ID: %d", task.ID)

	// Update tracking with deployment status (no longer storing task ID)
	err = b.Vault.TrackProgress(instanceID, "provision", fmt.Sprintf("BOSH deployment in progress (task %d)", task.ID), task.ID, params)
	if err != nil {
		l.Error("failed to store service status in the vault: %s", err)
	}

	l.Info("Async provisioning handed off to BOSH for instance %s", instanceID)
}

// deprovisionAsync handles the actual deprovisioning work in a background goroutine
// deprovisionAsync handles the actual deprovisioning work in a background goroutine
func (b *Broker) deprovisionAsync(instanceID string, instance *Instance) {
	l := Logger.Wrap("async %s", instanceID)
	l.Info("Starting async deprovisioning for instance %s", instanceID)

	// Track initial state
	err := b.Vault.TrackProgress(instanceID, "deprovision", "Starting deprovisioning process", 0, nil)
	if err != nil {
		l.Error("failed to initialize deprovision tracking: %s", err)
		return
	}

	deploymentName := instance.PlanID + "-" + instanceID
	l.Info("Deleting BOSH deployment %s", deploymentName)

	// Check if deployment exists
	if err := b.Vault.TrackProgress(instanceID, "deprovision", "Checking BOSH deployment status", 0, nil); err != nil {
		l.Error("failed to track progress (checking deployment): %s", err)
	}
	manifest, err := b.BOSH.GetDeployment(deploymentName)
	if err != nil || manifest.Manifest == "" {
		l.Info("Deployment %s not found, marking as deleted", deploymentName)
		if err := b.Vault.TrackProgress(instanceID, "deprovision", "Deployment not found, cleanup only", 0, nil); err != nil {
			l.Error("failed to track progress (deployment not found): %s", err)
		}

		// Remove from index
		if err := b.Vault.TrackProgress(instanceID, "deprovision", "Removing from service index", 0, nil); err != nil {
			l.Error("failed to track progress (removing from index): %s", err)
		}
		if err := b.Vault.Index(instanceID, nil); err != nil {
			l.Error("failed to remove service from vault index: %s", err)
		}

		// Mark as completed
		if err := b.Vault.TrackProgress(instanceID, "deprovision", "Deprovisioning completed (no deployment)", 0, nil); err != nil {
			l.Error("failed to track progress (completed): %s", err)
		}
		if err := b.Vault.Track(instanceID, "deprovision", 0, nil); err != nil {
			l.Error("failed to track completion: %s", err)
		}
		return
	}

	// Delete deployment
	if err := b.Vault.TrackProgress(instanceID, "deprovision", "Initiating BOSH deployment deletion", 0, nil); err != nil {
		l.Error("failed to track progress (initiating deletion): %s", err)
	}
	task, err := b.BOSH.DeleteDeployment(deploymentName)
	if err != nil {
		l.Error("Failed to delete BOSH deployment %s: %s", deploymentName, err)
		if trackErr := b.Vault.TrackProgress(instanceID, "deprovision", fmt.Sprintf("Deployment deletion failed: %s", err), -1, nil); trackErr != nil {
			l.Error("failed to track progress (deletion failed): %s", trackErr)
		}
		return
	}

	l.Info("Delete operation started successfully, BOSH task ID: %d", task.ID)

	// Update tracking with deployment status (no longer storing task ID)
	err = b.Vault.TrackProgress(instanceID, "deprovision", fmt.Sprintf("BOSH deletion in progress (task %d)", task.ID), task.ID, nil)
	if err != nil {
		l.Error("failed to store deprovision status in the vault: %s", err)
	}

	// Remove from index (will be cleaned up when task completes)
	if err := b.Vault.TrackProgress(instanceID, "deprovision", "Removing from service index", 0, nil); err != nil {
		l.Error("failed to track progress (removing from index): %s", err)
	}
	if err := b.Vault.Index(instanceID, nil); err != nil {
		l.Error("failed to remove service from vault index: %s", err)
	}

	l.Info("Async deprovisioning handed off to BOSH for instance %s", instanceID)
}
