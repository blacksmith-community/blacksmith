package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"blacksmith/internal/bosh"
)

// OrphanSweepMinimumAge is how old an index entry must be before the sweep
// may remove it. A provision writes its index entry before the deployment
// exists on the director, and release uploads can keep it in that state for a
// while, so a younger entry with no deployment is treated as a provision that
// has not reached BOSH yet.
const OrphanSweepMinimumAge = 2 * time.Hour

// sweepOrphanedIndexEntries removes index entries whose deployment the
// director confirms does not exist and for which no provision is in flight.
//
// The regular orphan handling only marks such entries and waits 24 hours or
// 24 cycles before removing them. An entry left behind after its deployment
// was deleted by hand therefore lingers as a live instance in the broker for
// a day. Existence is confirmed with a direct GetDeployment that returns
// bosh.ErrDeploymentNotFound (a real 404), never from the deployment list
// alone or from an empty manifest, and a running BOSH task or a recent
// provision task record keeps the entry.
//
// Only the index entry is removed. The instance's secrets under
// secret/<instance-id>/ are left in place, matching what a normal deprovision
// does: handleSuccessfulDeprovision removes the index entry and deliberately
// skips Vault.Clear so the credentials survive for auditing. Sweeping those
// secrets here would delete data that a completed deprovision keeps.
//
// It returns the number of entries removed.
func (r *ReconcilerManager) sweepOrphanedIndexEntries(_ context.Context, reconciled []InstanceData, deploymentNames map[string]bool) int {
	if r.bosh == nil {
		r.logger.Debugf("BOSH director not available, skipping orphan index sweep")

		return 0
	}

	synchronizer, ok := r.Synchronizer.(*IndexSynchronizer)
	if !ok || synchronizer == nil {
		r.logger.Debugf("Synchronizer does not expose the index, skipping orphan index sweep")

		return 0
	}

	// An empty scan is more likely a failed scan than an empty director.
	if len(deploymentNames) == 0 {
		r.logger.Debugf("BOSH deployment scan returned nothing, skipping orphan index sweep")

		return 0
	}

	idx, err := synchronizer.GetVaultIndex()
	if err != nil {
		r.logger.Errorf("Failed to get vault index for orphan sweep: %v", err)

		return 0
	}

	reconciledIDs := make(map[string]bool, len(reconciled))
	for _, inst := range reconciled {
		reconciledIDs[inst.ID] = true
	}

	removed := 0

	for instanceID, data := range idx {
		if !r.shouldSweepIndexEntry(synchronizer, instanceID, data, reconciledIDs, deploymentNames) {
			continue
		}

		delete(idx, instanceID)

		removed++
	}

	if removed == 0 {
		return 0
	}

	err = synchronizer.SaveVaultIndex(idx)
	if err != nil {
		r.logger.Errorf("Failed to save vault index after orphan sweep: %v", err)

		return 0
	}

	r.logger.Infof("Orphan cleanup: removed %d index entries whose deployments the director confirmed absent", removed)

	return removed
}

// shouldSweepIndexEntry decides whether one index entry is a confirmed orphan.
//
// A tombstone (status deleted) is the vm-monitor's record that the director
// already answered 404 for the deployment. When it still names a deployment
// the director is asked again; when it carries no deployment name at all there
// is nothing left to confirm and its age alone decides.
func (r *ReconcilerManager) shouldSweepIndexEntry(synchronizer *IndexSynchronizer, instanceID string, data interface{}, reconciledIDs, deploymentNames map[string]bool) bool {
	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return false
	}

	if reconciledIDs[instanceID] {
		return false
	}

	tombstone := synchronizer.isMarkedDeleted(dataMap)

	age, known := indexEntryAge(dataMap)
	if !known || age < OrphanSweepMinimumAge {
		r.logger.Debugf("Orphan sweep: keeping %s, index entry is too young or undated to rule out a provision in flight", instanceID)

		return false
	}

	deploymentName := indexEntryDeploymentName(instanceID, dataMap)
	if deploymentName == "" {
		if tombstone {
			r.logger.Infof("Orphan cleanup: removing tombstone index entry %s, it names no deployment and was marked deleted %s ago", instanceID, age.Round(time.Minute))

			return true
		}

		return false
	}

	if deploymentNames[deploymentName] {
		return false
	}

	if !tombstone && r.provisionRecordInFlight(synchronizer, instanceID) {
		r.logger.Debugf("Orphan sweep: keeping %s, its provision task record is still in progress", instanceID)

		return false
	}

	_, err := r.bosh.GetDeployment(deploymentName)
	if err == nil {
		r.logger.Debugf("Orphan sweep: keeping %s, deployment %s exists on the director", instanceID, deploymentName)

		return false
	}

	if !errors.Is(err, bosh.ErrDeploymentNotFound) {
		r.logger.Debugf("Orphan sweep: keeping %s, could not confirm deployment %s is absent: %v", instanceID, deploymentName, err)

		return false
	}

	task, err := r.bosh.FindRunningTaskForDeployment(deploymentName)
	if err != nil {
		r.logger.Debugf("Orphan sweep: keeping %s, could not check for a running task on %s: %v", instanceID, deploymentName, err)

		return false
	}

	if task != nil {
		r.logger.Debugf("Orphan sweep: keeping %s, task %d (%s) is %s", instanceID, task.ID, task.Description, task.State)

		return false
	}

	if tombstone {
		r.logger.Infof("Orphan cleanup: removing tombstone index entry %s, the director confirmed deployment %s does not exist and no task is in flight", instanceID, deploymentName)
	} else {
		r.logger.Infof("Orphan cleanup: removing index entry %s, the director confirmed deployment %s does not exist and no task is in flight", instanceID, deploymentName)
	}

	return true
}

// provisionRecordInFlight reports whether the instance's vault task record
// describes a provision that is still running: action "provision", a state
// other than failed, and an update within OrphanSweepMinimumAge. A missing
// record means no provision is in flight; any other vault error is treated as
// in flight so the entry is kept.
func (r *ReconcilerManager) provisionRecordInFlight(synchronizer *IndexSynchronizer, instanceID string) bool {
	vaultClient, isVault := synchronizer.vault.(VaultInterface)
	if !isVault || vaultClient == nil {
		return true
	}

	task, err := vaultClient.Get(instanceID + "/task")
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false
		}

		r.logger.Debugf("Orphan sweep: could not read task record for %s: %v", instanceID, err)

		return true
	}

	if task == nil {
		return false
	}

	action, _ := task["action"].(string)
	state, _ := task["state"].(string)

	if action != "provision" || state == "failed" {
		return false
	}

	updatedAt, ok := numericTimestamp(task["updated_at"])
	if !ok {
		return true
	}

	return time.Since(time.Unix(updatedAt, 0)) < OrphanSweepMinimumAge
}

// indexEntryDeploymentName returns the deployment name recorded on the entry,
// falling back to the name the vm-monitor records on a tombstone and then to
// the plan ID prefix used before the field existed.
func indexEntryDeploymentName(instanceID string, dataMap map[string]interface{}) string {
	if name, ok := dataMap["deployment_name"].(string); ok && name != "" {
		return name
	}

	if name, ok := dataMap["last_deployment"].(string); ok && name != "" {
		return name
	}

	if planID, ok := dataMap["plan_id"].(string); ok && planID != "" {
		return planID + "-" + instanceID
	}

	return ""
}

// indexEntryAge returns the time since the entry was last written by the
// broker, the reconciler, or the vm-monitor, using the newest timestamp it
// carries. The second result is false when the entry has no usable timestamp.
func indexEntryAge(dataMap map[string]interface{}) (time.Duration, bool) {
	var newest time.Time

	for _, field := range []string{"requested_at", "created_at", fieldReconciledAt, "discovered_at", "updated_at", "unorphaned_at", "deprovision_requested_at", "deleted_at"} {
		value, ok := dataMap[field].(string)
		if !ok {
			continue
		}

		parsed, err := time.Parse(time.RFC3339, value)
		if err == nil && parsed.After(newest) {
			newest = parsed
		}
	}

	if created, ok := numericTimestamp(dataMap["created"]); ok {
		if parsed := time.Unix(created, 0); parsed.After(newest) {
			newest = parsed
		}
	}

	if newest.IsZero() {
		return 0, false
	}

	return time.Since(newest), true
}

// numericTimestamp reads a unix timestamp that may have been decoded from
// JSON as a json.Number (the Vault API client) or a float64, or stored as an int.
func numericTimestamp(value interface{}) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			asFloat, floatErr := typed.Float64()
			if floatErr != nil {
				return 0, false
			}

			return int64(asFloat), true
		}

		return parsed, true
	default:
		return 0, false
	}
}
