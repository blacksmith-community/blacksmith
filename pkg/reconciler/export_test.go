package reconciler

// TaskRecordInFlight exposes the orphan sweep's check of an instance's
// task record.
func (r *ReconcilerManager) TaskRecordInFlight(synchronizer *IndexSynchronizer, instanceID string, tombstone bool) bool {
	return r.taskRecordInFlight(synchronizer, instanceID, tombstone)
}
