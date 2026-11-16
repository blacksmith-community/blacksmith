// Package settings reads and saves the runtime settings the broker keeps
// in Vault at blacksmith/settings, one record that several settings share.
package settings

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Path is where the runtime settings live in Vault.
const Path = "blacksmith/settings"

// Store is the part of Vault the settings need.
type Store interface {
	Get(ctx context.Context, path string, result interface{}) (bool, error)
	Put(ctx context.Context, path string, data interface{}) error
}

// saves serialises every save, because all settings share one record and a
// save reads the record, changes one key, and writes it back.
var saves sync.Mutex //nolint:gochecknoglobals // every saver shares one record, so one lock guards it

// Read returns the record, and an empty one when none is saved yet.
func Read(ctx context.Context, store Store) (map[string]interface{}, error) {
	var record map[string]interface{}

	exists, err := store.Get(ctx, Path, &record)
	if err != nil {
		return nil, fmt.Errorf("reading the saved settings: %w", err)
	}

	if !exists || record == nil {
		return map[string]interface{}{}, nil
	}

	return record, nil
}

// Save sets one key of the record and keeps every other key as it was. A
// record that can't be read is never written over.
func Save(ctx context.Context, store Store, key string, value interface{}) error {
	saves.Lock()
	defer saves.Unlock()

	record, err := Read(ctx, store)
	if err != nil {
		return err
	}

	record[key] = value

	err = store.Put(ctx, Path, record)
	if err != nil {
		return fmt.Errorf("saving the settings: %w", err)
	}

	return nil
}

// ErrNoStore means there is no Vault to hold the settings.
var ErrNoStore = errors.New("there is no Vault to save the settings in")
