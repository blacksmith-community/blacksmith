package reconciler_test

import (
	"reflect"
	"testing"

	. "blacksmith/pkg/reconciler"
)

func TestIndexSynchronizer_GetVaultIndex_Empty(t *testing.T) {
	t.Parallel()

	v := NewTestVault(t)
	s := NewIndexSynchronizer(v, NewMockLogger())

	// Ensure canonical index path exists but empty
	_ = v.SetSecret("db", map[string]interface{}{})

	idx, err := s.GetVaultIndex()
	if err != nil {
		t.Fatalf("getVaultIndex returned error: %v", err)
	}

	if len(idx) != 0 {
		t.Fatalf("expected empty index, got: %#v", idx)
	}
}

func TestIndexSynchronizer_GetAndSaveVaultIndex_RoundTrip(t *testing.T) {
	t.Parallel()

	vault := NewTestVault(t)
	// Ensure canonical index path exists
	_ = vault.SetSecret("db", map[string]interface{}{})
	// Seed existing index under canonical path "db"
	err := vault.SetSecret("db", map[string]interface{}{"inst1": map[string]interface{}{"service_id": "redis"}})
	if err != nil {
		t.Fatalf("failed to set secret: %v", err)
	}

	synchronizer := NewIndexSynchronizer(vault, NewMockLogger())

	idx, err := synchronizer.GetVaultIndex()
	if err != nil {
		t.Fatalf("getVaultIndex error: %v", err)
	}

	if _, exists := idx["inst1"]; !exists {
		t.Fatalf("expected inst1 present, got: %#v", idx)
	}

	// Save new index and verify stored
	newIdx := map[string]interface{}{"inst2": map[string]interface{}{"plan_id": "cache-small"}}

	err = synchronizer.SaveVaultIndex(newIdx)
	if err != nil {
		t.Fatalf("saveVaultIndex error: %v", err)
	}

	got, err := vault.Get("db")
	if err != nil {
		t.Fatalf("failed to read stored index: %v", err)
	}

	if !reflect.DeepEqual(got, newIdx) {
		t.Fatalf("vault stored index mismatch\nwant: %#v\n got: %#v", newIdx, got)
	}
}
