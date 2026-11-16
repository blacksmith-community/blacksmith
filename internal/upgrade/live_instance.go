package upgrade

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"blacksmith/internal/apierr"
	"blacksmith/internal/ownership"
	vaultPkg "blacksmith/pkg/vault"
)

// indexUnreadable is the answer when the service index can't be read, so
// Blacksmith can't tell which instances exist.
func indexUnreadable(err error) error {
	return apierr.New(http.StatusServiceUnavailable, apierr.CodeVaultUnavailable, //nolint:wrapcheck // an API error is the answer as it stands
		fmt.Sprintf("Blacksmith could not read its service index, so it can't tell which instances exist. Try the preview again shortly: %v", err))
}

// previewLiveCommandInstance previews one instance. An ID the index
// doesn't hold live, or that can't name an instance, is listed as blocked
// and never read.
func (m *Manager) previewLiveCommandInstance(ctx context.Context, index map[string]*vaultPkg.Instance, instanceID string) (CommandPreviewInstance, error) {
	if !isLive(index, instanceID) {
		return CommandPreviewInstance{
			InstanceID: instanceID,
			VMs:        []CommandPreviewVM{},
			Blocked:    &Blocked{Code: apierr.CodeInstanceNotFound, Message: "Blacksmith does not know this instance, so it may have been deleted."},
		}, nil
	}

	return m.previewCommandInstance(ctx, instanceID)
}

// readLiveInstance reads an instance's record, but only when the index
// already read holds the instance live. An ID the index doesn't hold, holds
// only as a tombstone, or that can't name an instance reports the record
// missing without reading Vault, and an index that couldn't be read is an
// error.
func (m *Manager) readLiveInstance(ctx context.Context, index map[string]*vaultPkg.Instance, indexErr error, instanceID string, record *map[string]interface{}) (bool, error) {
	if indexErr != nil {
		return false, fmt.Errorf("checking the service index: %w", indexErr)
	}

	_, err := ownership.LiveEntry(index, instanceID)
	if errors.Is(err, ownership.ErrInvalidID) || errors.Is(err, ownership.ErrNotLive) {
		return false, nil
	}

	exists, err := m.vault.Get(ctx, instanceID, record)
	if err != nil {
		return false, fmt.Errorf("reading the instance record: %w", err)
	}

	return exists, nil
}

// isLive reports whether the index already read holds the instance live.
func isLive(index map[string]*vaultPkg.Instance, instanceID string) bool {
	_, err := ownership.LiveEntry(index, instanceID)

	return err == nil
}
