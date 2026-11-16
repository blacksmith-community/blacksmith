// Package ownership answers from the broker's service index which
// deployments and instances belong to the broker, so every route that
// takes a name or an ID from a caller applies the same rule.
package ownership

import (
	"context"
	"fmt"

	vaultPkg "blacksmith/pkg/vault"
)

// IndexPath is where the service index lives in Vault, keyed by instance ID.
const IndexPath = "db"

// Store is the part of Vault the index needs.
type Store interface {
	Get(ctx context.Context, path string, result interface{}) (bool, error)
}

// Index reads the service index. A missing index reads as empty. An index
// that can't be read is an error, because then nobody knows what the
// broker owns.
func Index(ctx context.Context, store Store) (map[string]*vaultPkg.Instance, error) {
	var index map[string]*vaultPkg.Instance

	exists, err := store.Get(ctx, IndexPath, &index)
	if err != nil {
		return nil, fmt.Errorf("reading the service index: %w", err)
	}

	if !exists {
		return nil, nil //nolint:nilnil // a missing index is an empty one
	}

	return index, nil
}

// ManagedDeployment reports whether a live entry of the index records
// deploymentName as its deployment. An entry whose data is nil, and a
// tombstone marked deleted, is an instance that is gone, and it owns
// nothing.
func ManagedDeployment(ctx context.Context, store Store, deploymentName string) (bool, error) {
	index, err := Index(ctx, store)
	if err != nil {
		return false, err
	}

	for instanceID, instance := range index {
		if instanceID == "" || instance == nil || instance.IsTombstone() {
			continue
		}

		if instance.DeploymentNameFor(instanceID) == deploymentName {
			return true, nil
		}
	}

	return false, nil
}
