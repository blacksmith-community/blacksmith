package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeVMVault implements the minimal vmVault for VMMonitor tests.
type fakeVMVault struct {
	index map[string]interface{}
	puts  []string
}

func (f *fakeVMVault) GetIndex(ctx context.Context, name string) (*VaultIndex, error) {
	if f.index == nil {
		f.index = make(map[string]interface{})
	}

	return &VaultIndex{parent: nil, path: name, Data: f.index}, nil
}

func (f *fakeVMVault) Put(ctx context.Context, path string, data interface{}) error {
	f.puts = append(f.puts, path)
	if f.index == nil {
		f.index = make(map[string]interface{})
	}

	if path == "db" {
		if m, ok := data.(map[string]interface{}); ok {
			f.index = m

			return nil
		}

		return ErrUnexpectedDataType
	}
	// ignore non-index writes for this unit test
	return nil
}

func (f *fakeVMVault) Get(ctx context.Context, path string, out interface{}) (bool, error) {
	// Only used by GetServiceVMStatus which is not exercised here
	return false, nil
}

func TestVMMonitor_HandleCheckError_Deletes404(t *testing.T) {
	t.Parallel()

	fakeVault := &fakeVMVault{index: map[string]interface{}{}}
	m := &VMMonitor{
		vault:          fakeVault,
		boshDirector:   nil,
		config:         &Config{},
		normalInterval: time.Minute,
		failedInterval: time.Minute,
		services:       make(map[string]*ServiceMonitor),
	}

	svcID := "00000000-0000-0000-0000-0000000000dd"
	dep := "redis-cache-small-00000000-0000-0000-0000-0000000000dd"
	m.services[svcID] = &ServiceMonitor{ServiceID: svcID, DeploymentName: dep}

	// simulate BOSH 404
	err := ErrDirectorNonSuccessfulStatus
	m.handleCheckError(context.Background(), m.services[svcID], err)

	// Assert index marked deleted
	entry, ok := fakeVault.index[svcID].(map[string]interface{})
	if !ok {
		t.Fatalf("expected index entry for service ID; got index: %#v", fakeVault.index)
	}

	if del, _ := entry["deleted"].(bool); !del {
		t.Fatalf("expected deleted=true in index entry: %#v", entry)
	}

	if entry["deployment_name"] != dep {
		t.Fatalf("expected deployment_name to be set")
	}

	// Service should be removed from monitoring
	if _, exists := m.services[svcID]; exists {
		t.Fatalf("expected service to be removed from monitor after 404")
	}
}
