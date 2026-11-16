package vault

// VaultCreds represents vault credentials for initialization and unsealing.
type VaultCreds struct {
	SealKey   string `json:"seal_key"`
	RootToken string `json:"root_token"`
}

// Instance represents a service instance stored in Vault.
type Instance struct {
	ID        string `json:"instance_id,omitempty"`
	ServiceID string `json:"service_id,omitempty"`
	PlanID    string `json:"plan_id,omitempty"`
	// DeploymentName is recorded at provision time from the requested plan ID.
	// It is authoritative: PlanID can later be rewritten by the reconciler, so
	// recomputing the deployment name from it can point at a deployment that
	// does not exist.
	DeploymentName string `json:"deployment_name,omitempty"`
	// Status and Deleted are the two marks the reconciler writes on an
	// entry for an instance that is gone, which leaves a tombstone.
	Status  string `json:"status,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
}

// StatusDeleted is the status of a tombstone.
const StatusDeleted = "deleted"

// IsTombstone reports whether the entry is a tombstone, which is marked
// deleted by its status or its deleted flag. A tombstone owns nothing.
func (instance *Instance) IsTombstone() bool {
	return isTombstone(instance.Status, instance.Deleted)
}

// EntryIsTombstone reports whether a raw index entry is a tombstone, by the
// same two marks as Instance.IsTombstone. Anything that isn't a mapping
// carries no mark.
func EntryIsTombstone(entry interface{}) bool {
	fields, isMap := entry.(map[string]interface{})
	if !isMap {
		return false
	}

	status, _ := fields["status"].(string)
	deleted, _ := fields["deleted"].(bool)

	return isTombstone(status, deleted)
}

func isTombstone(status string, deleted bool) bool {
	return status == StatusDeleted || deleted
}

// DeploymentNameFor returns the deployment name the instance's entry
// records, and builds it from the plan ID and instanceID only when the entry
// records none, as an entry indexed before the field existed does.
func (instance *Instance) DeploymentNameFor(instanceID string) string {
	if instance.DeploymentName != "" {
		return instance.DeploymentName
	}

	return instance.PlanID + "-" + instanceID
}
