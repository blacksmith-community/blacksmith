package vault

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"blacksmith/internal/bosh/sshconn"
	vaultPkg "blacksmith/pkg/vault"
)

// sshCleanupsFolder holds one record for each throwaway SSH user the
// broker may still have on a VM, keyed by the user's name. It sits under
// the secret/ folder beside the CF registrations, and ListOperationRecordIDs
// skips that folder, so re-adoption never takes a record for an instance.
const sshCleanupsFolder = "secret/blacksmith/ssh-cleanups/"

// ErrSSHCleanupUnreadable means ListSSHCleanups found a record it could not
// read or could not use, and left it out.
var ErrSSHCleanupUnreadable = errors.New("an SSH cleanup record could not be used")

// RecordSSHCleanup writes the record for cleanup.User, replacing any
// record the user already has. A record the sweep could never send, such
// as one whose target names no instance, is refused before anything is
// written.
func (vault *Vault) RecordSSHCleanup(ctx context.Context, cleanup sshconn.PendingCleanup) error {
	err := cleanup.Validate()
	if err != nil {
		return fmt.Errorf("refused to record the SSH cleanup: %w", err)
	}

	err = vault.Put(ctx, sshCleanupsFolder+cleanup.User, cleanup)
	if err != nil {
		return fmt.Errorf("failed to record the SSH cleanup of user %s at %s: %w", cleanup.User, sshCleanupsFolder+cleanup.User, err)
	}

	return nil
}

// ClearSSHCleanup removes the record for user, every version of it, so the
// folder no longer lists the user. A record that is already gone is not an
// error.
func (vault *Vault) ClearSSHCleanup(ctx context.Context, user string) error {
	err := sshconn.CheckUser(user)
	if err != nil {
		return fmt.Errorf("refused to clear the SSH cleanup: %w", err)
	}

	err = vault.ensureClient()
	if err != nil {
		return err
	}

	path := sshCleanupsFolder + user

	err = vault.withAutoUnseal(ctx, func() error {
		return vault.client.DestroySecret(ctx, path)
	})
	if err != nil {
		return fmt.Errorf("failed to clear the SSH cleanup of user %s at %s: %w", user, path, err)
	}

	return nil
}

// ListSSHCleanups returns every record, oldest first. A failed listing, or
// a failed read of any record, returns no records and the error. A record
// that reads but cannot be used, because it does not decode, names a user
// other than its key, or fails Validate, is left out, and the records that
// can be used come back with an error wrapping ErrSSHCleanupUnreadable that
// names each one left out, so the caller can still sweep the rest.
func (vault *Vault) ListSSHCleanups(ctx context.Context) ([]sshconn.PendingCleanup, error) {
	err := vault.ensureClient()
	if err != nil {
		return nil, err
	}

	var keys []string

	err = vault.withAutoUnseal(ctx, func() error {
		var listErr error

		keys, listErr = vault.client.ListSecrets(ctx, sshCleanupsFolder)

		return listErr //nolint:wrapcheck // wrapped below once withAutoUnseal has seen it
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list the SSH cleanups at %s: %w", sshCleanupsFolder, err)
	}

	records := make([]sshconn.PendingCleanup, 0, len(keys))

	var unusable []string

	for _, key := range keys {
		// A key that ends in "/" is a folder, which no record is.
		if strings.HasSuffix(key, "/") {
			continue
		}

		record, found, problem, err := vault.readSSHCleanup(ctx, key)

		switch {
		case err != nil:
			return nil, err
		case problem != "":
			unusable = append(unusable, fmt.Sprintf("%s (%s)", sshCleanupsFolder+key, problem))
		case found:
			records = append(records, record)
		}
	}

	slices.SortStableFunc(records, func(a, b sshconn.PendingCleanup) int { return a.CreatedAt.Compare(b.CreatedAt) })

	if len(unusable) > 0 {
		return records, fmt.Errorf("%w: %s. Remove each one once its user is gone from the VM",
			ErrSSHCleanupUnreadable, strings.Join(unusable, "; "))
	}

	return records, nil
}

// readSSHCleanup reads the record at key. It returns found false for a
// record that is gone, a problem for one that cannot be used, and an error
// only when Vault could not be read.
func (vault *Vault) readSSHCleanup(ctx context.Context, key string) (sshconn.PendingCleanup, bool, string, error) {
	var record sshconn.PendingCleanup

	path := sshCleanupsFolder + key

	found, err := vault.Get(ctx, path, &record)

	switch {
	case errors.Is(err, vaultPkg.ErrCouldNotRemarshalData):
		return record, false, "it does not decode: " + err.Error(), nil
	case err != nil:
		return record, false, "", fmt.Errorf("failed to read the SSH cleanup at %s: %w", path, err)
	case !found:
		return record, false, "", nil
	case record.User != key:
		return record, false, fmt.Sprintf("it names user %q", record.User), nil
	}

	err = record.Validate()
	if err != nil {
		return record, false, err.Error(), nil //nolint:nilerr // a record that cannot be used is a problem to name, not a failed read
	}

	return record, true, "", nil
}
