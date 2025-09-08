package reconciler

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
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

// SyncIndex synchronizes the vault index with reconciled instances.
func (s *IndexSynchronizer) SyncIndex(ctx context.Context, instances []InstanceData) error {
	s.logger.Debugf("Synchronizing index with %d instances", len(instances))

	idx, err := s.GetVaultIndex()
	if err != nil {
		return fmt.Errorf("failed to get index: %w", err)
	}

	startCount := len(idx)
	s.logger.Debugf("Current index has %d entries", startCount)

	reconciledMap := s.buildReconciledMap(instances)
	stats := s.updateIndexWithInstances(idx, instances)
	stats.orphanCount = s.markOrphanedInstances(idx, reconciledMap)
	stats.cleanupCount = s.markStaleOrphans(idx)

	if err := s.SaveVaultIndex(idx); err != nil {
		return fmt.Errorf("failed to save index: %w", err)
	}

	s.logger.Infof("Index synchronized successfully: initial=%d, updated=%d, added=%d, orphaned=%d, stale=%d, final=%d",
		startCount, stats.updateCount, stats.addCount, stats.orphanCount, stats.cleanupCount, len(idx))

	if err := s.validateIndexInternal(idx); err != nil {
		s.logger.Errorf("Index validation failed after sync: %s", err)
	}

	return nil
}

// syncStats tracks synchronization statistics.
type syncStats struct {
	updateCount, addCount, orphanCount, cleanupCount int
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
		preserveFields := []string{"created_at", "organization_id", "space_id", "parameters"}
		for _, field := range preserveFields {
			if val, hasField := existingMap[field]; hasField {
				data[field] = val
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

	for id, data := range idx {
		if !reconciledMap[id] {
			if s.markAsOrphaned(id, data) {
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

// markStaleOrphans marks old orphaned entries as stale but preserves them.
func (s *IndexSynchronizer) markStaleOrphans(idx map[string]interface{}) int {
	cleanupCount := 0

	for id, data := range idx {
		if s.processOrphanedEntry(id, data) {
			cleanupCount++
		}
	}

	return cleanupCount
}

// processOrphanedEntry processes a single orphaned entry for staleness marking.
func (s *IndexSynchronizer) processOrphanedEntry(entryID string, data interface{}) bool {
	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return false
	}

	orphaned, ok := dataMap["orphaned"].(bool)
	if !ok || !orphaned {
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

// ValidateIndex validates the consistency of the vault index.
func (s *IndexSynchronizer) ValidateIndex(ctx context.Context) error {
	s.logger.Debugf("Validating index consistency")

	idx, err := s.GetVaultIndex()
	if err != nil {
		return fmt.Errorf("failed to get index for validation: %w", err)
	}

	return s.validateIndexInternal(idx)
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
func (s *IndexSynchronizer) validateEntry(id string, data interface{}) error {
	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return fmt.Errorf("%w: expected map, got %T", ErrInvalidDataFormat, data)
	}

	// Check required fields for service instances
	if _, hasService := dataMap["service_id"]; hasService {
		return s.validateServiceInstance(id, dataMap)
	}

	return nil
}

// validateServiceInstance validates service instance specific fields.
func (s *IndexSynchronizer) validateServiceInstance(id string, dataMap map[string]interface{}) error {
	// Validate required fields
	requiredFields := []string{"service_id", "plan_id"}
	for _, field := range requiredFields {
		if val, ok := dataMap[field].(string); !ok || val == "" {
			return fmt.Errorf("%w: %s", ErrMissingInvalidRequiredField, field)
		}
	}

	// Validate UUID format
	if !IsValidInstanceID(id) {
		return ErrInvalidInstanceIDFormat
	}

	// Validate deployment name if present
	return s.validateDeploymentName(id, dataMap)
}

// validateDeploymentName validates the deployment name format.
func (s *IndexSynchronizer) validateDeploymentName(id string, dataMap map[string]interface{}) error {
	deploymentName, ok := dataMap["deployment_name"].(string)
	if !ok || deploymentName == "" {
		return nil
	}

	// Check deployment name format (should be planID-instanceID)
	expectedPattern := fmt.Sprintf("%s-%s", dataMap["plan_id"], id)
	if deploymentName == expectedPattern {
		return nil
	}

	// Some flexibility for legacy deployments
	planID, ok := dataMap["plan_id"].(string)
	if !ok {
		return ErrPlanIDMustBeString
	}

	if !s.IsLegacyDeploymentName(deploymentName, planID, id) {
		return fmt.Errorf("%w: expected pattern %s, got %s",
			ErrDeploymentNameMismatch, expectedPattern, deploymentName)
	}

	return nil
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

// Vault interaction methods - these will be replaced with actual vault calls

func (s *IndexSynchronizer) GetVaultIndex() (map[string]interface{}, error) {
	s.logger.Debugf("Getting vault index from Vault")

	v, ok := s.vault.(VaultInterface)
	if !ok || v == nil {
		return nil, ErrVaultInterfaceNotAvailable
	}

	data, err := v.Get("db")
	if err != nil {
		return nil, fmt.Errorf("failed to get index from vault: %w", err)
	}

	if data == nil {
		return make(map[string]interface{}), nil
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
