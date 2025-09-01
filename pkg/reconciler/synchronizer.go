package reconciler

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type indexSynchronizer struct {
	vault  interface{} // Will be replaced with actual Vault type
	logger Logger
}

// NewIndexSynchronizer creates a new index synchronizer
func NewIndexSynchronizer(vault interface{}, logger Logger) Synchronizer {
	return &indexSynchronizer{
		vault:  vault,
		logger: logger,
	}
}

// SyncIndex synchronizes the vault index with reconciled instances
func (s *indexSynchronizer) SyncIndex(ctx context.Context, instances []InstanceData) error {
	s.logDebug("Synchronizing index with %d instances", len(instances))

	// Get current index
	idx, err := s.getVaultIndex()
	if err != nil {
		return fmt.Errorf("failed to get index: %w", err)
	}

	startCount := len(idx)
	s.logDebug("Current index has %d entries", startCount)

	// Build map of reconciled instances
	reconciledMap := make(map[string]bool)
	for _, inst := range instances {
		reconciledMap[inst.ID] = true
	}

	// Statistics
	updateCount := 0
	addCount := 0
	orphanCount := 0

	// Update index with reconciled instances
	for _, inst := range instances {
		data := map[string]interface{}{
			"deployment_name": inst.Deployment.Name,
			"reconciled":      true,
			"reconciled_at":   time.Now().Format(time.RFC3339),
			"reconciled_by":   "deployment_reconciler",
		}
		// Only set service identifiers when known to avoid validation errors
		if inst.ServiceID != "" {
			data["service_id"] = inst.ServiceID
		}
		if inst.PlanID != "" {
			data["plan_id"] = inst.PlanID
		}

		// Check if instance exists
		if existing, exists := idx[inst.ID]; exists {
			// Merge with existing data
			if existingMap, ok := existing.(map[string]interface{}); ok {
				// Preserve certain fields from existing data
				preserveFields := []string{"created_at", "organization_id", "space_id", "parameters"}
				for _, field := range preserveFields {
					if val, hasField := existingMap[field]; hasField {
						data[field] = val
					}
				}
				updateCount++
				s.logDebug("Updating existing instance %s", inst.ID)
			}
		} else {
			// New instance found
			addCount++
			data["created_at"] = inst.CreatedAt.Format(time.RFC3339)
			data["discovered_at"] = time.Now().Format(time.RFC3339)
			s.logInfo("Adding new instance %s discovered by reconciler", inst.ID)
		}

		// Add updated_at timestamp
		data["updated_at"] = time.Now().Format(time.RFC3339)

		idx[inst.ID] = data
	}

	// Mark orphaned instances (in index but not in BOSH)
	for id, data := range idx {
		if !reconciledMap[id] {
			if dataMap, ok := data.(map[string]interface{}); ok {
				// Check if it's a service instance (has service_id and plan_id)
				if _, hasService := dataMap["service_id"]; hasService {
					if _, hasPlan := dataMap["plan_id"]; hasPlan {
						// Check if already marked as orphaned
						if orphaned, ok := dataMap["orphaned"].(bool); !ok || !orphaned {
							// Mark as potentially orphaned
							dataMap["orphaned"] = true
							dataMap["orphaned_at"] = time.Now().Format(time.RFC3339)
							dataMap["orphaned_reason"] = "no_bosh_deployment"
							idx[id] = dataMap
							orphanCount++

							s.logWarning("Instance %s appears to be orphaned (no BOSH deployment found)", id)

							// Log additional details for debugging
							if deploymentName, ok := dataMap["deployment_name"].(string); ok {
								s.logDebug("Expected deployment name: %s", deploymentName)
							}
						}
					}
				}
			}
		}
	}

	// Mark very old orphaned entries but NEVER delete service instances
	cleanupCount := 0
	for id, data := range idx {
		if dataMap, ok := data.(map[string]interface{}); ok {
			if orphaned, ok := dataMap["orphaned"].(bool); ok && orphaned {
				if orphanedAt, ok := dataMap["orphaned_at"].(string); ok {
					if t, err := time.Parse(time.RFC3339, orphanedAt); err == nil {
						daysSinceOrphaned := int(time.Since(t).Hours() / 24)

						// After 30 days, mark as stale but DO NOT DELETE
						if daysSinceOrphaned > 30 {
							// Check if it's a service instance - these should NEVER be deleted
							if _, hasService := dataMap["service_id"]; hasService {
								if _, hasPlan := dataMap["plan_id"]; hasPlan {
									// Mark as stale but keep in vault for historical purposes
									dataMap["stale"] = true
									dataMap["stale_since"] = orphanedAt
									dataMap["days_orphaned"] = daysSinceOrphaned
									dataMap["preservation_reason"] = "service_instance_historical_data"
									idx[id] = dataMap

									s.logInfo("Instance %s marked as stale (orphaned for %d days) but preserved for historical purposes", id, daysSinceOrphaned)
									cleanupCount++
									continue
								}
							}

							// Only non-service instances can potentially be cleaned up
							// But for now, we'll also preserve these with a warning
							dataMap["stale"] = true
							dataMap["stale_since"] = orphanedAt
							dataMap["days_orphaned"] = daysSinceOrphaned
							idx[id] = dataMap

							s.logWarning("Non-service entry %s has been orphaned for %d days - marked as stale but preserved", id, daysSinceOrphaned)
							cleanupCount++
						}
					}
				}
			}
		}
	}

	// Save updated index
	err = s.saveVaultIndex(idx)
	if err != nil {
		return fmt.Errorf("failed to save index: %w", err)
	}

	s.logInfo("Index synchronized successfully: initial=%d, updated=%d, added=%d, orphaned=%d, stale=%d, final=%d",
		startCount, updateCount, addCount, orphanCount, cleanupCount, len(idx))

	// Validate the index after sync
	if err := s.validateIndexInternal(idx); err != nil {
		s.logError("Index validation failed after sync: %s", err)
		// Don't fail the sync, just log the validation error
	}

	return nil
}

// ValidateIndex validates the consistency of the vault index
func (s *indexSynchronizer) ValidateIndex(ctx context.Context) error {
	s.logDebug("Validating index consistency")

	idx, err := s.getVaultIndex()
	if err != nil {
		return fmt.Errorf("failed to get index for validation: %w", err)
	}

	return s.validateIndexInternal(idx)
}

// validateIndexInternal performs the actual validation
func (s *indexSynchronizer) validateIndexInternal(idx map[string]interface{}) error {
	validCount := 0
	invalidCount := 0
	warningCount := 0

	var errors []string

	for id, data := range idx {
		// Validate entry structure
		if err := s.validateEntry(id, data); err != nil {
			s.logError("Invalid index entry %s: %s", id, err)
			errors = append(errors, fmt.Sprintf("%s: %s", id, err.Error()))
			invalidCount++
			continue
		}

		// Check for warnings
		if warning := s.checkEntryWarnings(id, data); warning != "" {
			s.logWarning("Index entry %s: %s", id, warning)
			warningCount++
		}

		validCount++
	}

	s.logInfo("Index validation complete: valid=%d, invalid=%d, warnings=%d",
		validCount, invalidCount, warningCount)

	if invalidCount > 0 {
		return fmt.Errorf("index validation failed: %d invalid entries out of %d total: %s",
			invalidCount, validCount+invalidCount, strings.Join(errors, "; "))
	}

	return nil
}

// validateEntry validates a single index entry
func (s *indexSynchronizer) validateEntry(id string, data interface{}) error {
	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return fmt.Errorf("invalid data format: expected map, got %T", data)
	}

	// Check required fields for service instances
	if _, hasService := dataMap["service_id"]; hasService {
		// This is a service instance, validate required fields
		requiredFields := []string{"service_id", "plan_id"}
		for _, field := range requiredFields {
			if val, ok := dataMap[field].(string); !ok || val == "" {
				return fmt.Errorf("missing or invalid required field: %s", field)
			}
		}

		// Validate UUID format
		if !isValidInstanceID(id) {
			return fmt.Errorf("invalid instance ID format")
		}

		// Validate deployment name if present
		if deploymentName, ok := dataMap["deployment_name"].(string); ok && deploymentName != "" {
			// Check deployment name format (should be planID-instanceID)
			expectedPattern := fmt.Sprintf("%s-%s", dataMap["plan_id"], id)
			if deploymentName != expectedPattern {
				// Some flexibility for legacy deployments
				if !s.isLegacyDeploymentName(deploymentName, dataMap["plan_id"].(string), id) {
					return fmt.Errorf("deployment name mismatch: expected pattern %s, got %s",
						expectedPattern, deploymentName)
				}
			}
		}
	}

	return nil
}

// checkEntryWarnings checks for potential issues that aren't errors
func (s *indexSynchronizer) checkEntryWarnings(id string, data interface{}) string {
	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return ""
	}

	// Check if orphaned
	if orphaned, ok := dataMap["orphaned"].(bool); ok && orphaned {
		if orphanedAt, ok := dataMap["orphaned_at"].(string); ok {
			if t, err := time.Parse(time.RFC3339, orphanedAt); err == nil {
				duration := time.Since(t)
				if duration > 7*24*time.Hour {
					return fmt.Sprintf("orphaned for %v", duration.Round(time.Hour))
				}
			}
		}
		return "marked as orphaned"
	}

	// Check if not recently reconciled
	if reconciledAt, ok := dataMap["reconciled_at"].(string); ok {
		if t, err := time.Parse(time.RFC3339, reconciledAt); err == nil {
			if time.Since(t) > 24*time.Hour {
				return fmt.Sprintf("not reconciled for %v", time.Since(t).Round(time.Hour))
			}
		}
	} else if reconciled, ok := dataMap["reconciled"].(bool); !ok || !reconciled {
		return "never reconciled"
	}

	// Check for missing deployment name
	if _, hasService := dataMap["service_id"]; hasService {
		if deploymentName, ok := dataMap["deployment_name"].(string); !ok || deploymentName == "" {
			return "missing deployment name"
		}
	}

	return ""
}

// isLegacyDeploymentName checks if a deployment name follows legacy patterns
func (s *indexSynchronizer) isLegacyDeploymentName(deploymentName, planID, instanceID string) bool {
	// Check various legacy patterns
	patterns := []string{
		fmt.Sprintf("^%s[-_]%s$", regexp.QuoteMeta(planID), regexp.QuoteMeta(instanceID)),
		fmt.Sprintf("^blacksmith[-_]%s[-_]%s$", regexp.QuoteMeta(planID), regexp.QuoteMeta(instanceID)),
		fmt.Sprintf("^service[-_]%s[-_]%s$", regexp.QuoteMeta(planID), regexp.QuoteMeta(instanceID)),
	}

	for _, pattern := range patterns {
		if matched, _ := regexp.MatchString(pattern, deploymentName); matched {
			return true
		}
	}

	return false
}

// isValidInstanceID validates instance ID format
func isValidInstanceID(id string) bool {
	// Standard UUID format (v1-v5 and nil UUID)
	// Accepts any valid UUID format
	uuidRegex := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	return uuidRegex.MatchString(id)
}

// Vault interaction methods - these will be replaced with actual vault calls

func (s *indexSynchronizer) getVaultIndex() (map[string]interface{}, error) {
	// This will be replaced with actual vault index retrieval
	// idx, err := vault.GetIndex("db")
	// return idx.Data, err
	s.logDebug("Getting vault index")
	return make(map[string]interface{}), nil
}

func (s *indexSynchronizer) saveVaultIndex(idx map[string]interface{}) error {
	// This will be replaced with actual vault index save
	// vaultIdx, err := vault.GetIndex("db")
	// vaultIdx.Data = idx
	// return vaultIdx.Save()
	s.logDebug("Saving vault index with %d entries", len(idx))
	return nil
}

// Logging helper methods - these will be replaced with actual logger calls
func (s *indexSynchronizer) logDebug(format string, args ...interface{}) {
	// Will be replaced with actual logger call
	fmt.Printf("[DEBUG] synchronizer: "+format+"\n", args...)
}

func (s *indexSynchronizer) logInfo(format string, args ...interface{}) {
	// Will be replaced with actual logger call
	fmt.Printf("[INFO] synchronizer: "+format+"\n", args...)
}

func (s *indexSynchronizer) logWarning(format string, args ...interface{}) {
	// Will be replaced with actual logger call
	fmt.Printf("[WARN] synchronizer: "+format+"\n", args...)
}

func (s *indexSynchronizer) logError(format string, args ...interface{}) {
	// Will be replaced with actual logger call
	fmt.Printf("[ERROR] synchronizer: "+format+"\n", args...)
}
