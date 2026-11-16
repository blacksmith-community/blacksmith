// Package ownership answers from the broker's service index which
// deployments and instances belong to the broker, so every route that
// takes a name or an ID from a caller applies the same rule.
package ownership

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

// ErrInvalidID says an instance ID can't name an index entry, because it
// is empty or holds a path separator, a parent reference, or an empty
// segment. Such an ID would reach other Vault paths when a handler joins
// it into one.
var ErrInvalidID = errors.New("invalid instance ID")

// ErrNotLive says the index holds no live entry for an instance ID. The
// instance was never created, or it was deprovisioned and only its
// tombstone remains.
var ErrNotLive = errors.New("no live instance with that ID")

// ValidID reports whether id can name an instance. It refuses an empty ID
// and any ID that holds a slash, a backslash, or "..".
func ValidID(id string) bool {
	return id != "" && !strings.ContainsAny(id, `/\`) && !strings.Contains(id, "..")
}

// LiveInstance returns the live index entry for instanceID. It answers
// ErrInvalidID for an ID that can't name an instance, ErrNotLive for an ID
// the index doesn't hold or holds only as a tombstone, and a wrapped error
// when the index can't be read.
func LiveInstance(ctx context.Context, store Store, instanceID string) (*vaultPkg.Instance, error) {
	if !ValidID(instanceID) {
		return nil, ErrInvalidID
	}

	index, err := Index(ctx, store)
	if err != nil {
		return nil, err
	}

	return LiveEntry(index, instanceID)
}

// LiveEntry returns the live entry of an index already read, with the same
// answers as LiveInstance, so a caller that checks many IDs reads the
// index once.
func LiveEntry(index map[string]*vaultPkg.Instance, instanceID string) (*vaultPkg.Instance, error) {
	if !ValidID(instanceID) {
		return nil, ErrInvalidID
	}

	instance := index[instanceID]
	if instance == nil || instance.IsTombstone() {
		return nil, ErrNotLive
	}

	return instance, nil
}
