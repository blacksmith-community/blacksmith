package testutil

import (
	"context"

	"blacksmith/internal/interfaces"
	vaultPkg "blacksmith/pkg/vault"
)

// InstanceVault is an interfaces.Vault that holds index entries for
// FindInstance and no other secrets, so Get reports every path missing. Any
// other method panics on the nil embedded Vault, so a test sees a call it did
// not script.
type InstanceVault struct {
	interfaces.Vault

	Instances map[string]vaultPkg.Instance
}

// FindInstance returns the entry held for instanceID, and false when there is
// none.
func (v *InstanceVault) FindInstance(_ context.Context, instanceID string) (*vaultPkg.Instance, bool, error) {
	instance, found := v.Instances[instanceID]
	if !found {
		return nil, false, nil
	}

	return &instance, true, nil
}

// Get reports every path missing.
func (v *InstanceVault) Get(context.Context, string, interface{}) (bool, error) {
	return false, nil
}
