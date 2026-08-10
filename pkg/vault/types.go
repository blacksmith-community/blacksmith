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
}
