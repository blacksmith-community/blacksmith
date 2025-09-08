package reconciler_test

import (
	"reflect"
	"testing"

	. "blacksmith/pkg/reconciler"
)

func TestVaultUpdater_UpdateAndGetIndex_CanonicalPath(t *testing.T) {
	t.Parallel()

	v := newMemVault()
	u := NewVaultUpdater(v, NewMockLogger(), BackupConfig{})

	instanceID := "12345678-1234-1234-1234-123456789abc"
	entry := map[string]interface{}{"service_id": "redis", "plan_id": "cache-small"}

	if err := u.UpdateIndex(instanceID, entry); err != nil {
		t.Fatalf("updateIndex error: %v", err)
	}

	// Ensure written to canonical path "db"
	storedIndex := v.store["db"]
	if storedIndex == nil {
		t.Fatalf("expected index stored at path 'db', not found")
	}

	if got, ok := storedIndex[instanceID]; !ok {
		t.Fatalf("expected instance id present in stored index")
	} else if !reflect.DeepEqual(got, entry) {
		t.Fatalf("stored entry mismatch\nwant: %#v\n got: %#v", entry, got)
	}

	// Now read back via getFromIndex
	gotEntry, err := u.GetFromIndex(instanceID)
	if err != nil {
		t.Fatalf("getFromIndex error: %v", err)
	}

	if !reflect.DeepEqual(gotEntry, entry) {
		t.Fatalf("round-trip entry mismatch\nwant: %#v\n got: %#v", entry, gotEntry)
	}
}

func TestVaultUpdater_UpdateIndex_DeleteEntry(t *testing.T) {
	t.Parallel()

	v := newMemVault()
	u := NewVaultUpdater(v, NewMockLogger(), BackupConfig{})

	instanceID := "00000000-0000-0000-0000-0000000000ee"
	v.store["db"] = map[string]interface{}{instanceID: map[string]interface{}{"service_id": "redis"}}

	err := u.UpdateIndex(instanceID, nil)
	if err != nil {
		t.Fatalf("updateIndex delete error: %v", err)
	}

	if _, exists := v.store["db"][instanceID]; exists {
		t.Fatalf("expected instance removed from index after delete")
	}
}
