package vault

import (
	"context"
	"fmt"

	"blacksmith/pkg/logger"
	vaultPkg "blacksmith/pkg/vault"
)

// updateIndex reads the "db" index, passes it to change, and saves it when
// change reports that it changed something. It holds indexMu from the read to
// the save, so two callers sharing this Vault cannot both read the same
// index and have the second save drop the first one's change. Callers using
// another Vault value, in this process or another, are not covered.
//
// The read and the save make one attempt each, with no auto-unseal of their
// own. The whole read-modify-write runs under withAutoUnseal instead, so when
// Vault is sealed, indexMu is released before the auto-unseal wait, and the
// retry starts again from a fresh read. A retry never saves an index that was
// read before the unseal.
//
// An error from change is returned as it is and never starts an unseal.
func (vault *Vault) updateIndex(ctx context.Context, change func(*vaultPkg.Index) (bool, error)) error {
	err := vault.ensureClient()
	if err != nil {
		return err
	}

	var changeErr error

	err = vault.withAutoUnseal(ctx, func() error {
		changeErr = nil

		vault.indexMu.Lock()
		defer vault.indexMu.Unlock()

		idx, readErr := vaultPkg.NewIndex(directIndexStore{vault: vault}, "db", ctx)
		if readErr != nil {
			return fmt.Errorf("failed to create vault index: %w", readErr)
		}

		changed, mutateErr := change(idx)
		if mutateErr != nil {
			changeErr = mutateErr

			return nil
		}

		if !changed {
			return nil
		}

		saveErr := idx.Save(ctx)
		if saveErr != nil {
			return fmt.Errorf("failed to save index: %w", saveErr)
		}

		return nil
	})
	if err != nil {
		return err
	}

	return changeErr
}

// directIndexStore gives an Index one attempt per Vault call and no
// auto-unseal, for use while indexMu is held. Waiting on an auto-unseal with
// indexMu held would stall every other index change in the process behind
// it. The caller has called ensureClient.
type directIndexStore struct {
	vault *Vault
}

// Get reads the secret at path once and decodes it into out.
func (store directIndexStore) Get(ctx context.Context, path string, out interface{}) (bool, error) {
	data, exists, err := store.vault.readSecret(ctx, path)
	if err != nil {
		return false, err
	}

	return decodeSecret(logger.Get().Named("vault get"), path, data, exists, out)
}

// Put writes data to path once.
func (store directIndexStore) Put(ctx context.Context, path string, data interface{}) error {
	dataMap, err := convertToMap(data)
	if err != nil {
		return fmt.Errorf("failed to convert data: %w", err)
	}

	err = store.vault.client.PutSecret(ctx, path, dataMap)
	if err != nil {
		return fmt.Errorf("failed to put secret: %w", err)
	}

	return nil
}

// Delete deletes the secret at path once.
func (store directIndexStore) Delete(ctx context.Context, path string) error {
	err := store.vault.client.DeleteSecret(ctx, path)
	if err != nil {
		return fmt.Errorf("failed to delete secret: %w", err)
	}

	return nil
}
