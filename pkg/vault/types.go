package vault

// VaultCreds represents vault credentials for initialization and unsealing.
type VaultCreds struct {
	SealKey   string `json:"seal_key"`
	RootToken string `json:"root_token"`
}

// Instance represents a service instance stored in Vault.
type Instance struct {
	ID        string
	ServiceID string
	PlanID    string
}
