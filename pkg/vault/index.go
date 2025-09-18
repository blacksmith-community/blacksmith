package vault

import (
	"context"
	"encoding/json"
	"fmt"
)

// Index represents a vault index for managing indexed data.
type Index struct {
	parent VaultOperations
	path   string
	Data   map[string]interface{}
}

// VaultOperations defines the interface for basic vault operations.
type VaultOperations interface {
	Get(ctx context.Context, path string, out interface{}) (bool, error)
	Put(ctx context.Context, path string, data interface{}) error
	Delete(ctx context.Context, path string) error
}

// NewIndex creates a new VaultIndex.
func NewIndex(parent VaultOperations, path string, ctx context.Context) (*Index, error) {
	data := map[string]interface{}{}

	exists, err := parent.Get(ctx, path, &data)
	if err != nil {
		return nil, fmt.Errorf("failed to get vault data for index: %w", err)
	}

	if !exists {
		data = make(map[string]interface{})
	}

	return &Index{
		parent: parent,
		path:   path,
		Data:   data,
	}, nil
}

// Lookup retrieves a value from the index by key.
func (vi *Index) Lookup(key string) (interface{}, error) {
	v, ok := vi.Data[key]
	if !ok {
		return nil, fmt.Errorf("%w: '%s'", ErrKeyNotFoundInIndex, key)
	}

	return v, nil
}

// Save persists the index to vault.
func (vi *Index) Save(ctx context.Context) error {
	if len(vi.Data) == 0 {
		err := vi.parent.Delete(ctx, vi.path)
		if err != nil {
			return fmt.Errorf("failed to delete empty index: %w", err)
		}

		return nil
	}

	err := vi.parent.Put(ctx, vi.path, vi.Data)
	if err != nil {
		return fmt.Errorf("failed to save index to vault: %w", err)
	}

	return nil
}

// JSON returns the index data as a JSON string.
func (vi *Index) JSON() string {
	b, err := json.Marshal(vi.Data)
	if err != nil {
		return `{"error":"json failed"}`
	}

	return string(b)
}
