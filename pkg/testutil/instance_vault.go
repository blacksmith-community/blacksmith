package testutil

import (
	"context"
	"encoding/json"
	"fmt"

	"blacksmith/internal/interfaces"
	vaultPkg "blacksmith/pkg/vault"
)

// InstanceVault is an interfaces.Vault that holds index entries for
// FindInstance and the index at "db", so Get reports every other path missing. Any
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

// Get serves the index at the path "db", keyed by instance ID, and reports
// every other path missing.
func (v *InstanceVault) Get(_ context.Context, path string, out interface{}) (bool, error) {
	if path != "db" || len(v.Instances) == 0 {
		return false, nil
	}

	payload, err := json.Marshal(v.Instances)
	if err != nil {
		return false, fmt.Errorf("encoding the index: %w", err)
	}

	err = json.Unmarshal(payload, out)
	if err != nil {
		return false, fmt.Errorf("decoding the index: %w", err)
	}

	return true, nil
}
