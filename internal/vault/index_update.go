package vault

import (
	"context"
	"errors"
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
// The broker makes one attempt at the read and one at the save, with no
// auto-unseal of their own. The whole read-modify-write runs under
// withAutoUnseal instead, so when Vault is sealed, indexMu is released before
// the auto-unseal wait, and the retry starts again from a fresh read. A retry
// never saves an index that was read before the unseal.
//
// The one-attempt rule covers the broker's attempts only. The Vault API
// client underneath still retries a 5xx answer, a sealed 503 included, or a
// dropped connection on its own, as its MaxRetries setting allows, and it
// does so while indexMu is held. With the client's defaults of two retries
// and waits of 1 to 1.5 seconds, a writer against a sealed Vault holds the
// lock for about 2.5 seconds before it releases it for the unseal wait, and
// every queued writer then does the same in turn. Against a connection that
// hangs, each attempt can last up to the client timeout. Waiting for the lock
// also ignores ctx.
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

// ErrNilIndexChange is returned by UpdateIndex when it is given no change.
var ErrNilIndexChange = errors.New("index change is nil")

// UpdateIndex runs change on the entries of the "db" index and saves the index
// when change reports that it changed something. It takes the same lock as
// every other index change made through this Vault, from the read to the
// save, so a caller outside this package, such as the reconciler, cannot lose
// a change made by the broker at the same moment, or have its own lost.
//
// change may run more than once, each time on a fresh read, when Vault is
// found sealed and auto-unsealed. It must keep no state between runs, and it
// must not call another index method of this Vault, because the lock is not
// reentrant. An error from change is returned as it is, with nothing saved.
// An index that change leaves empty is deleted, as it is by Index.
func (vault *Vault) UpdateIndex(ctx context.Context, change func(entries map[string]interface{}) (bool, error)) error {
	if change == nil {
		return ErrNilIndexChange
	}

	return vault.updateIndex(ctx, func(idx *vaultPkg.Index) (bool, error) {
		if idx.Data == nil {
			idx.Data = make(map[string]interface{})
		}

		return change(idx.Data)
	})
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
