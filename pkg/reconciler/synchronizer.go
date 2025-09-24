package reconciler

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Constants for status values.
const (
	StatusDeleted = "deleted"
)

// Static errors for err113 compliance.
var (
	ErrIndexValidationFailed       = errors.New("index validation failed")
	ErrInvalidDataFormat           = errors.New("invalid data format")
	ErrMissingInvalidRequiredField = errors.New("missing or invalid required field")
	ErrPlanIDMustBeString          = errors.New("plan_id must be a string")
	ErrDeploymentNameMismatch      = errors.New("deployment name mismatch")
	ErrVaultInterfaceNotAvailable  = errors.New("vault interface not available in synchronizer")
)

type IndexSynchronizer struct {
	vault  interface{} // VaultInterface expected
	logger Logger
}

// NewIndexSynchronizer creates a new index synchronizer.
func NewIndexSynchronizer(vault interface{}, logger Logger) *IndexSynchronizer {
	return &IndexSynchronizer{
		vault:  vault,
		logger: logger,
	}
}

// CleanupDeletedInstances removes instances marked as cleanup_eligible from the index.
// This should only be called by administrators or scheduled processes after careful consideration.
func (s *IndexSynchronizer) CleanupDeletedInstances(ctx context.Context, dryRun bool) (int, error) {
	s.logger.Infof("Starting cleanup of deleted instances (dryRun=%v)", dryRun)

	idx, err := s.GetVaultIndex()
	if err != nil {
		return 0, fmt.Errorf("failed to get index for cleanup: %w", err)
	}

	var toDelete []string
	for instanceID, data := range idx {
		dataMap, validMap := data.(map[string]interface{})
		if !validMap {
			continue
		}

		// Only cleanup instances that are explicitly marked as cleanup_eligible
		if eligible, ok := dataMap["cleanup_eligible"].(bool); ok && eligible {
			toDelete = append(toDelete, instanceID)
		}
	}

	if len(toDelete) == 0 {
		s.logger.Infof("No instances eligible for cleanup")

		return 0, nil
	}

	s.logger.Infof("Found %d instances eligible for cleanup", len(toDelete))

	if dryRun {
		for _, id := range toDelete {
			if data, ok := idx[id].(map[string]interface{}); ok {
				s.logger.Infof("Would delete: %s (deleted_at: %v, days_deleted: %v)",
					id, data["deleted_at"], data["days_deleted"])
			}
		}

		return len(toDelete), nil
	}

	// Actually delete the instances
	deletedCount := 0

	for _, id := range toDelete {
		delete(idx, id)

		deletedCount++

		s.logger.Infof("Removed instance %s from index", id)
	}

	// Save the updated index
	if deletedCount > 0 {
		err = s.SaveVaultIndex(idx)
		if err != nil {
			return 0, fmt.Errorf("failed to save index after cleanup: %w", err)
		}
	}

	s.logger.Infof("Successfully cleaned up %d deleted instances", deletedCount)

	return deletedCount, nil
}

// SyncIndex synchronizes the vault index with reconciled instances.
func (s *IndexSynchronizer) SyncIndex(ctx context.Context, instances []InstanceData) error {
	s.logger.Debugf("Synchronizing index with %d instances", len(instances))

	idx, err := s.GetVaultIndex()
	if err != nil {
		return fmt.Errorf("failed to get index: %w", err)
	}

	startCount := len(idx)
	s.logger.Debugf("Current index has %d entries", startCount)

	// Clean up any nil entries first
	nilCount := s.cleanupNilEntries(idx)
	if nilCount > 0 {
		s.logger.Warningf("Cleaned up %d nil entries from index", nilCount)
	}

	reconciledMap := s.buildReconciledMap(instances)
	stats := s.updateIndexWithInstances(idx, instances)
	stats.orphanCount = s.markOrphanedInstances(idx, reconciledMap)
	stats.cleanupCount = s.markStaleOrphans(idx)

	err = s.SaveVaultIndex(idx)
	if err != nil {
		return fmt.Errorf("failed to save index: %w", err)
	}

	s.logger.Infof("Index synchronized successfully: initial=%d, nil-cleaned=%d, updated=%d, added=%d, orphaned=%d, stale=%d, final=%d",
		startCount, nilCount, stats.updateCount, stats.addCount, stats.orphanCount, stats.cleanupCount, len(idx))

	err = s.validateIndexInternal(idx)
	if err != nil {
		s.logger.Errorf("Index validation failed after sync: %s", err)
	}

	return nil
}

// syncStats tracks synchronization statistics.
type syncStats struct {
	updateCount, addCount, orphanCount, cleanupCount int
}

// ValidateIndex validates the consistency of the vault index.
func (s *IndexSynchronizer) ValidateIndex(ctx context.Context) error {
	s.logger.Debugf("Validating index consistency")

	idx, err := s.GetVaultIndex()
	if err != nil {
		return fmt.Errorf("failed to get index for validation: %w", err)
	}

	return s.validateIndexInternal(idx)
}

// checkEntryWarnings checks for potential issues that aren't errors.
func (s *IndexSynchronizer) CheckEntryWarnings(id string, data interface{}) string {
	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return ""
	}

	// Check if orphaned
	if orphaned, ok := dataMap["orphaned"].(bool); ok && orphaned {
		if orphanedAt, ok := dataMap["orphaned_at"].(string); ok {
			t, err := time.Parse(time.RFC3339, orphanedAt)
			if err == nil {
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
		t, err := time.Parse(time.RFC3339, reconciledAt)
		if err == nil {
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

// isLegacyDeploymentName checks if a deployment name follows legacy patterns.
func (s *IndexSynchronizer) IsLegacyDeploymentName(deploymentName, planID, instanceID string) bool {
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

// IsValidInstanceID validates instance ID format.
func IsValidInstanceID(id string) bool {
	// Standard UUID format (v1-v5 and nil UUID)
	// Accepts any valid UUID format
	uuidRegex := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

	return uuidRegex.MatchString(id)
}

func (s *IndexSynchronizer) GetVaultIndex() (map[string]interface{}, error) {
	s.logger.Debugf("Getting vault index from Vault")

	v, ok := s.vault.(VaultInterface)
	if !ok || v == nil {
		return nil, ErrVaultInterfaceNotAvailable
	}

	data, err := v.Get("db")
	if err != nil {
		if err.Error() == "vault key not found" {
			s.logger.Infof("Vault index key 'db' does not exist, initializing empty index")

			emptyIndex := make(map[string]interface{})

			err := s.SaveVaultIndex(emptyIndex)
			if err != nil {
				return nil, fmt.Errorf("failed to initialize vault index: %w", err)
			}

			return emptyIndex, nil
		}

		return nil, fmt.Errorf("failed to get index from vault: %w", err)
	}

	if data == nil {
		s.logger.Infof("Vault returned nil data for 'db' key, initializing empty index")

		emptyIndex := make(map[string]interface{})

		err := s.SaveVaultIndex(emptyIndex)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize vault index: %w", err)
		}

		return emptyIndex, nil
	}

	return data, nil
}

func (s *IndexSynchronizer) SaveVaultIndex(idx map[string]interface{}) error {
	s.logger.Debugf("Saving vault index with %d entries", len(idx))

	v, ok := s.vault.(VaultInterface)
	if !ok || v == nil {
		return ErrVaultInterfaceNotAvailable
	}

	err := v.Put("db", idx)
	if err != nil {
		return fmt.Errorf("failed to save index to vault: %w", err)
	}

	return nil
}

// cleanupNilEntries removes nil entries from the index.
func (s *IndexSynchronizer) cleanupNilEntries(idx map[string]interface{}) int {
	count := 0

	for key, value := range idx {
		if value == nil {
			s.logger.Debugf("Removing nil entry %s from index", key)
			delete(idx, key)

			count++
		}
	}

	return count
}

// buildReconciledMap creates a map of reconciled instance IDs.
func (s *IndexSynchronizer) buildReconciledMap(instances []InstanceData) map[string]bool {
	reconciledMap := make(map[string]bool)
	for _, inst := range instances {
		reconciledMap[inst.ID] = true
	}

	return reconciledMap
}

// updateIndexWithInstances updates the index with reconciled instances.
func (s *IndexSynchronizer) updateIndexWithInstances(idx map[string]interface{}, instances []InstanceData) syncStats {
	var stats syncStats

	for _, inst := range instances {
		data := s.buildInstanceData(inst)

		// Check if the instance has been marked for deletion (deployment not found in BOSH)
		if needsDeletion, ok := inst.Metadata["needs_deletion_marking"].(bool); ok && needsDeletion {
			data["status"] = StatusDeleted
			data["deleted_at"] = inst.Metadata["deleted_at"]
			data["deleted_by"] = inst.Metadata["deleted_by"]
			data["deletion_reason"] = inst.Metadata["deletion_reason"]
			s.logger.Infof("Marking instance %s as deleted (deployment not found in BOSH)", inst.ID)
		}

		// Copy status field from metadata if present
		if status, ok := inst.Metadata["status"].(string); ok {
			data["status"] = status
		}

		if existing, exists := idx[inst.ID]; exists {
			s.mergeWithExistingInstance(data, existing)

			stats.updateCount++

			s.logger.Debugf("Updating existing instance %s", inst.ID)
		} else {
			s.addNewInstanceData(data, inst)

			stats.addCount++

			s.logger.Infof("Adding new instance %s discovered by reconciler", inst.ID)
		}

		data["updated_at"] = time.Now().Format(time.RFC3339)
		idx[inst.ID] = data
	}

	return stats
}

// buildInstanceData builds the base data structure for an instance.
func (s *IndexSynchronizer) buildInstanceData(inst InstanceData) map[string]interface{} {
	data := map[string]interface{}{
		"deployment_name": inst.Deployment.Name,
		"reconciled":      true,
		"reconciled_at":   time.Now().Format(time.RFC3339),
		"reconciled_by":   "deployment_reconciler",
	}

	if inst.ServiceID != "" {
		data["service_id"] = inst.ServiceID
	}

	if inst.PlanID != "" {
		data["plan_id"] = inst.PlanID
	}

	return data
}

// mergeWithExistingInstance merges new data with existing instance data.
func (s *IndexSynchronizer) mergeWithExistingInstance(data map[string]interface{}, existing interface{}) {
	if existingMap, ok := existing.(map[string]interface{}); ok {
		// Preserve important fields from existing data
		preserveFields := []string{
			"created_at", "organization_id", "space_id", "parameters",
			"deleted_at", "deleted_by", "deletion_reason", // Preserve deletion info
			"orphaned_at", "orphaned_reason", // Preserve orphan info if exists
		}
		for _, field := range preserveFields {
			if val, hasField := existingMap[field]; hasField {
				data[field] = val
			}
		}

		// Preserve deleted status if already marked
		if status, ok := existingMap["status"].(string); ok && status == StatusDeleted {
			// Don't override deleted status unless explicitly changing it
			if _, hasNewStatus := data["status"]; !hasNewStatus {
				data["status"] = status
			}
		}
	}
}

// addNewInstanceData adds data specific to new instances.
func (s *IndexSynchronizer) addNewInstanceData(data map[string]interface{}, inst InstanceData) {
	data["created_at"] = inst.CreatedAt.Format(time.RFC3339)
	data["discovered_at"] = time.Now().Format(time.RFC3339)
}

// markOrphanedInstances marks instances that are in the index but not in BOSH.
func (s *IndexSynchronizer) markOrphanedInstances(idx map[string]interface{}, reconciledMap map[string]bool) int {
	orphanCount := 0

	for instanceID, data := range idx {
		if !reconciledMap[instanceID] {
			// Check if already marked as deleted (by VM monitor or reconciler)
			if s.isMarkedDeleted(data) {
				s.logger.Debugf("Instance %s already marked as deleted, skipping orphan marking", instanceID)

				continue
			}

			if s.markAsOrphaned(instanceID, data) {
				orphanCount++
			}
		}
	}

	return orphanCount
}

// markAsOrphaned marks a single instance as orphaned if it's a service instance.
func (s *IndexSynchronizer) markAsOrphaned(entryID string, data interface{}) bool {
	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return false
	}

	if !s.isServiceInstance(dataMap) {
		return false
	}

	if orphaned, ok := dataMap["orphaned"].(bool); ok && orphaned {
		return false
	}

	dataMap["orphaned"] = true
	dataMap["orphaned_at"] = time.Now().Format(time.RFC3339)
	dataMap["orphaned_reason"] = "no_bosh_deployment"

	s.logger.Warningf("Instance %s appears to be orphaned (no BOSH deployment found)", entryID)

	if deploymentName, ok := dataMap["deployment_name"].(string); ok {
		s.logger.Debugf("Expected deployment name: %s", deploymentName)
	}

	return true
}

// isServiceInstance checks if the data represents a service instance.
func (s *IndexSynchronizer) isServiceInstance(dataMap map[string]interface{}) bool {
	_, hasService := dataMap["service_id"]
	_, hasPlan := dataMap["plan_id"]

	return hasService && hasPlan
}

// isMarkedDeleted checks if an instance has been marked as deleted.
func (s *IndexSynchronizer) isMarkedDeleted(data interface{}) bool {
	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return false
	}

	// Check for deleted status
	if status, ok := dataMap["status"].(string); ok && status == StatusDeleted {
		return true
	}

	// Also check for deleted flag (backward compatibility)
	if deleted, ok := dataMap["deleted"].(bool); ok && deleted {
		return true
	}

	return false
}

// markStaleOrphans marks old orphaned entries as stale and old deleted entries for cleanup.
func (s *IndexSynchronizer) markStaleOrphans(idx map[string]interface{}) int {
	cleanupCount := 0

	for instanceID, data := range idx {
		// Process orphaned entries
		if s.processOrphanedEntry(instanceID, data) {
			cleanupCount++
		}

		// Process deleted entries that are very old
		if s.processDeletedEntry(instanceID, data) {
			cleanupCount++
		}
	}

	return cleanupCount
}

// processOrphanedEntry processes a single orphaned entry for staleness marking.
func (s *IndexSynchronizer) processOrphanedEntry(entryID string, data interface{}) bool {
	dataMap, exists := data.(map[string]interface{})
	if !exists {
		return false
	}

	orphaned, exists := dataMap["orphaned"].(bool)
	if !exists || !orphaned {
		return false
	}

	orphanedAt, ok := dataMap["orphaned_at"].(string)
	if !ok {
		return false
	}

	t, err := time.Parse(time.RFC3339, orphanedAt)
	if err != nil {
		return false
	}

	daysSinceOrphaned := int(time.Since(t).Hours() / HoursPerDay)
	if daysSinceOrphaned <= DaysBeforeStale {
		return false
	}

	return s.markAsStale(entryID, dataMap, orphanedAt, daysSinceOrphaned)
}

// markAsStale marks an entry as stale while preserving it.
func (s *IndexSynchronizer) markAsStale(entryID string, dataMap map[string]interface{}, orphanedAt string, daysSinceOrphaned int) bool {
	dataMap["stale"] = true
	dataMap["stale_since"] = orphanedAt
	dataMap["days_orphaned"] = daysSinceOrphaned

	if s.isServiceInstance(dataMap) {
		dataMap["preservation_reason"] = "service_instance_historical_data"

		s.logger.Infof("Instance %s marked as stale (orphaned for %d days) but preserved for historical purposes", entryID, daysSinceOrphaned)
	} else {
		s.logger.Warningf("Non-service entry %s has been orphaned for %d days - marked as stale but preserved", entryID, daysSinceOrphaned)
	}

	return true
}

// processDeletedEntry processes deleted entries that are very old.
func (s *IndexSynchronizer) processDeletedEntry(entryID string, data interface{}) bool {
	dataMap, validMap := data.(map[string]interface{})
	if !validMap {
		return false
	}

	// Check if it's marked as deleted
	status, hasStatus := dataMap["status"].(string)
	if !hasStatus || status != StatusDeleted {
		return false
	}

	// Check how long it's been deleted
	deletedAt, ok := dataMap["deleted_at"].(string)
	if !ok {
		return false
	}

	t, err := time.Parse(time.RFC3339, deletedAt)
	if err != nil {
		return false
	}

	daysSinceDeleted := int(time.Since(t).Hours() / HoursPerDay)

	// Mark for cleanup if deleted more than 60 days ago
	const DaysBeforeCleanup = 60
	if daysSinceDeleted > DaysBeforeCleanup {
		dataMap["cleanup_eligible"] = true
		dataMap["cleanup_eligible_since"] = time.Now().Format(time.RFC3339)
		dataMap["days_deleted"] = daysSinceDeleted

		s.logger.Infof("Instance %s has been deleted for %d days, marked as cleanup eligible", entryID, daysSinceDeleted)

		return true
	}

	return false
}

// validateIndexInternal performs the actual validation.
func (s *IndexSynchronizer) validateIndexInternal(idx map[string]interface{}) error {
	validCount := 0
	invalidCount := 0
	warningCount := 0

	var errors []string

	for entryID, data := range idx {
		// Validate entry structure
		err := s.validateEntry(entryID, data)
		if err != nil {
			s.logger.Errorf("Invalid index entry %s: %s", entryID, err)
			errors = append(errors, fmt.Sprintf("%s: %s", entryID, err.Error()))
			invalidCount++

			continue
		}

		// Check for warnings
		if warning := s.CheckEntryWarnings(entryID, data); warning != "" {
			s.logger.Warningf("Index entry %s: %s", entryID, warning)

			warningCount++
		}

		validCount++
	}

	s.logger.Infof("Index validation complete: valid=%d, invalid=%d, warnings=%d",
		validCount, invalidCount, warningCount)

	if invalidCount > 0 {
		return fmt.Errorf("%w: %d invalid entries out of %d total: %s",
			ErrIndexValidationFailed, invalidCount, validCount+invalidCount, strings.Join(errors, "; "))
	}

	return nil
}

// validateEntry validates a single index entry.
func (s *IndexSynchronizer) validateEntry(instanceID string, data interface{}) error {
	// Skip nil entries - these can occur during deletion or cleanup
	if data == nil {
		s.logger.Debugf("Skipping validation for nil entry %s (likely being deleted)", instanceID)

		return nil
	}

	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return fmt.Errorf("%w: expected map, got %T", ErrInvalidDataFormat, data)
	}

	// Check required fields for service instances
	if _, hasService := dataMap["service_id"]; hasService {
		return s.validateServiceInstance(instanceID, dataMap)
	}

	return nil
}

// validateServiceInstance validates service instance specific fields.
func (s *IndexSynchronizer) validateServiceInstance(instanceID string, dataMap map[string]interface{}) error {
	// Validate required fields
	requiredFields := []string{"service_id", "plan_id"}
	for _, field := range requiredFields {
		if val, ok := dataMap[field].(string); !ok || val == "" {
			return fmt.Errorf("%w: %s", ErrMissingInvalidRequiredField, field)
		}
	}

	// Validate UUID format
	if !IsValidInstanceID(instanceID) {
		return ErrInvalidInstanceIDFormat
	}

	// Validate deployment name if present
	return s.validateDeploymentName(instanceID, dataMap)
}

// validateDeploymentName validates the deployment name format.
func (s *IndexSynchronizer) validateDeploymentName(instanceID string, dataMap map[string]interface{}) error {
	deploymentName, exists := dataMap["deployment_name"].(string)
	if !exists || deploymentName == "" {
		return nil
	}

	// Check deployment name format (should be planID-instanceID)
	expectedPattern := fmt.Sprintf("%s-%s", dataMap["plan_id"], instanceID)
	if deploymentName == expectedPattern {
		return nil
	}

	// Some flexibility for legacy deployments
	planID, ok := dataMap["plan_id"].(string)
	if !ok {
		return ErrPlanIDMustBeString
	}

	if !s.IsLegacyDeploymentName(deploymentName, planID, instanceID) {
		return fmt.Errorf("%w: expected pattern %s, got %s",
			ErrDeploymentNameMismatch, expectedPattern, deploymentName)
	}

	return nil
}
