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
	// DeploymentName is recorded at provision time from the requested plan
	// ID, and the deployment keeps that name for life. PlanID names the plan
	// the instance is on now, which a plan change moves away from the name,
	// so a name built from PlanID can point at a deployment that does not
	// exist. Read the name through DeploymentNameFor.
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
// records, and mints it from the plan ID and instanceID only when the entry
// records none, as an entry indexed before the field existed does.
func (instance *Instance) DeploymentNameFor(instanceID string) string {
	if instance.DeploymentName != "" {
		return instance.DeploymentName
	}

	return MintDeploymentName(instance.PlanID, instanceID)
}

// EntryDeploymentName returns the deployment name a raw index entry
// records, and mints it from the entry's plan ID and instanceID only when
// the entry records none, by the same rule as Instance.DeploymentNameFor.
func EntryDeploymentName(instanceID string, entry map[string]interface{}) string {
	if name, _ := entry["deployment_name"].(string); name != "" {
		return name
	}

	planID, _ := entry["plan_id"].(string)

	return MintDeploymentName(planID, instanceID)
}

// MintDeploymentName names the deployment a provision of instanceID under
// planID creates. A deployment keeps the name it was created under for
// life, and BOSH can't rename one, so after a plan change the name still
// starts with the old plan. Anything that looks an existing deployment up
// reads the name the instance's index entry records, and mints one only
// for an entry that records none or for an instance the index doesn't
// have, where the plan is the only thing that can name it.
func MintDeploymentName(planID, instanceID string) string {
	return planID + "-" + instanceID
}
