// Package vmmonitortest holds helpers for tests that race other index
// writers against the vm-monitor's tombstone. No production code imports
// it, so it never ships in the broker.
package vmmonitortest

import (
	"context"
	"fmt"

	"blacksmith/internal/vmmonitor"
)

// IndexUpdater is the locked read-modify-write of the index that the vault
// offers and the vm-monitor's tombstone uses.
type IndexUpdater interface {
	UpdateIndex(ctx context.Context, change func(entries map[string]interface{}) (bool, error)) error
}

// MarkDeleted marks an indexed instance deleted in one locked index write,
// and writes the entry vmmonitor.DeletedEntry builds, which is the entry the
// vm-monitor's tombstone writes. It writes nothing for an instance the index
// does not hold, as the monitor does. It skips the monitor's checks for work
// in flight, so a test can race the mark against another writer.
func MarkDeleted(ctx context.Context, index IndexUpdater, instanceID string) error {
	err := index.UpdateIndex(ctx, func(entries map[string]interface{}) (bool, error) {
		current, indexed := entries[instanceID]
		if !indexed {
			return false, nil
		}

		entries[instanceID] = vmmonitor.DeletedEntry(current)

		return true, nil
	})
	if err != nil {
		return fmt.Errorf("mark %s deleted: %w", instanceID, err)
	}

	return nil
}
