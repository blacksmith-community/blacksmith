package reconciler_test

import (
	"reflect"
	"testing"

	. "blacksmith/pkg/reconciler"
)

func TestVaultUpdater_UpdateAndGetIndex_CanonicalPath(t *testing.T) {
	t.Parallel()

	vault := NewTestVault(t)
	updater := NewVaultUpdater(vault, NewMockLogger(), BackupConfig{})

	instanceID := "12345678-1234-1234-1234-123456789abc"
	entry := map[string]interface{}{"service_id": "redis", "plan_id": "cache-small"}

	err := updater.UpdateIndex(instanceID, entry)
	if err != nil {
		t.Fatalf("updateIndex error: %v", err)
	}

	// Ensure written to canonical path "db"
	storedIndex, err := vault.Get("db")
	if err != nil {
		t.Fatalf("expected index stored at path 'db', got error: %v", err)
	}

	if got, ok := storedIndex[instanceID]; !ok {
		t.Fatalf("expected instance id present in stored index")
	} else if !reflect.DeepEqual(got, entry) {
		t.Fatalf("stored entry mismatch\nwant: %#v\n got: %#v", entry, got)
	}

	// Now read back via getFromIndex
	gotEntry, err := updater.GetFromIndex(instanceID)
	if err != nil {
		t.Fatalf("getFromIndex error: %v", err)
	}

	if !reflect.DeepEqual(gotEntry, entry) {
		t.Fatalf("round-trip entry mismatch\nwant: %#v\n got: %#v", entry, gotEntry)
	}
}

func TestVaultUpdater_UpdateIndex_DeleteEntry(t *testing.T) {
	t.Parallel()

	vault := NewTestVault(t)
	updater := NewVaultUpdater(vault, NewMockLogger(), BackupConfig{})

	instanceID := "00000000-0000-0000-0000-0000000000ee"
	_ = vault.SetSecret("db", map[string]interface{}{instanceID: map[string]interface{}{"service_id": "redis"}})

	err := updater.UpdateIndex(instanceID, nil)
	if err != nil {
		t.Fatalf("updateIndex delete error: %v", err)
	}

	idx, _ := vault.Get("db")
	if _, exists := idx[instanceID]; exists {
		t.Fatalf("expected instance removed from index after delete")
	}
}
