package main

import (
	"context"
	"encoding/json"
	"fmt"
)

type VaultIndex struct {
	parent *Vault
	path   string
	Data   map[string]interface{}
}

func (v *Vault) GetIndex(ctx context.Context, path string) (*VaultIndex, error) {
	Data := map[string]interface{}{}

	exists, err := v.Get(ctx, path, &Data)
	if err != nil {
		return nil, err
	}

	if !exists {
		Data = make(map[string]interface{})
	}

	return &VaultIndex{
		parent: v,
		path:   path,
		Data:   Data,
	}, nil
}

func (vi *VaultIndex) Lookup(key string) (interface{}, error) {
	v, ok := vi.Data[key]
	if !ok {
		return nil, fmt.Errorf("%w: '%s'", ErrKeyNotFoundInIndex, key)
	}

	return v, nil
}

func (vi *VaultIndex) Save(ctx context.Context) error {
	if len(vi.Data) == 0 {
		return vi.parent.Delete(ctx, vi.path)
	}

	return vi.parent.Put(ctx, vi.path, vi.Data)
}

func (vi *VaultIndex) JSON() string {
	b, err := json.Marshal(vi.Data)
	if err != nil {
		return `{"error":"json failed"}`
	}

	return string(b)
}
