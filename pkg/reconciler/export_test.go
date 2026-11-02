package reconciler

// TaskRecordInFlight exposes the orphan sweep's check of an instance's
// task record.
func (r *ReconcilerManager) TaskRecordInFlight(synchronizer *IndexSynchronizer, instanceID string, tombstone bool) bool {
	return r.taskRecordInFlight(synchronizer, instanceID, tombstone)
}

// ParseManifestData exposes the release and stemcell lists a scan reads from
// a deployment's manifest.
func (s *BoshScanner) ParseManifestData(manifest string) *DeploymentDetail {
	detail := &DeploymentDetail{}
	s.parseManifestData(detail, manifest)

	return detail
}

// ExtractCredentialsFromManifest exposes the credential extraction that
// follows the manifest decode in getDeploymentManifest.
func (cp *CredentialPopulator) ExtractCredentialsFromManifest(manifest map[string]interface{}, deploymentName string) (map[string]interface{}, error) {
	return cp.extractCredentialsFromManifest(manifest, deploymentName)
}
