package reconciler_test

import (
	"fmt"
	"testing"
	"time"

	. "blacksmith/pkg/reconciler"
)

const (
	statusActive = "active"
)

// mockBroker implements BrokerInterface for reconstruction tests.
type reconMockBroker struct {
	creds *BindingCredentials
}

func (m *reconMockBroker) GetServices() []Service { return nil }
func (m *reconMockBroker) GetBindingCredentials(instanceID, bindingID string) (*BindingCredentials, error) {
	return m.creds, nil
}

func TestReconstructBindingWithBroker_StoresCredentialsAndMetadata(t *testing.T) {
	t.Parallel()

	vault := NewTestVault(t)
	updater := NewVaultUpdater(vault, NewMockLogger(), BackupConfig{})

	instanceID := "00000000-0000-0000-0000-00000000beef"
	bindingID := "bind-abc"

	setupBrokerReconstructTest(vault, instanceID)

	mb := createMockBroker()

	err := updater.ReconstructBindingWithBroker(instanceID, bindingID, mb)
	if err != nil {
		t.Fatalf("ReconstructBindingWithBroker error: %v", err)
	}

	verifyCredentialsStored(t, vault, instanceID, bindingID)
	verifyMetadataStored(t, vault, instanceID, bindingID)
	verifyBindingsIndex(t, vault, instanceID, bindingID)
}

// setupBrokerReconstructTest sets up test data for broker reconstruction.
func setupBrokerReconstructTest(vault *RealTestVault, instanceID string) {
	_ = vault.SetSecret("db", map[string]interface{}{
		instanceID: map[string]interface{}{
			"service_id": "rabbitmq",
			"plan_id":    "single-node",
		},
	})
}

// createMockBroker creates a mock broker for testing.
func createMockBroker() *reconMockBroker {
	return &reconMockBroker{creds: &BindingCredentials{
		Host:            "rmq.local",
		Port:            5672,
		Username:        "userX",
		Password:        "passX",
		CredentialType:  "dynamic",
		ReconstructedAt: time.Now().Format(time.RFC3339),
		Raw: map[string]interface{}{
			"api_url": "https://rmq-api",
			"vhost":   "/",
		},
	}}
}

// verifyCredentialsStored verifies that credentials are properly stored in vault.
func verifyCredentialsStored(t *testing.T, vault *RealTestVault, instanceID, bindingID string) {
	t.Helper()

	credPath := instanceID + "/bindings/" + bindingID + "/credentials"

	creds, err := vault.Get(credPath)
	if err != nil {
		t.Fatalf("expected credentials stored at %s: %v", credPath, err)
	}

	if creds["host"] != "rmq.local" || fmt.Sprint(creds["port"]) != "5672" {
		t.Fatalf("unexpected credentials content: %#v", creds)
	}

	if creds["username"] != "userX" || creds["password"] != "passX" {
		t.Fatalf("missing username/password in creds: %#v", creds)
	}

	if creds["credential_type"] != "dynamic" {
		t.Fatalf("expected credential_type=dynamic in creds: %#v", creds)
	}

	if creds["api_url"] != "https://rmq-api" || creds["vhost"] != "/" {
		t.Fatalf("expected broker Raw fields merged into creds: %#v", creds)
	}
}

// verifyMetadataStored verifies that metadata is properly stored in vault.
func verifyMetadataStored(t *testing.T, vault *RealTestVault, instanceID, bindingID string) {
	t.Helper()

	metaPath := instanceID + "/bindings/" + bindingID + "/metadata"

	meta, err := vault.Get(metaPath)
	if err != nil {
		t.Fatalf("expected metadata stored at %s: %v", metaPath, err)
	}

	if meta["binding_id"] != bindingID || meta["instance_id"] != instanceID {
		t.Fatalf("binding identifiers missing in metadata: %#v", meta)
	}

	if meta["credential_type"] != "dynamic" || meta["reconstruction_source"] != "broker" {
		t.Fatalf("expected reconstruction markers in metadata: %#v", meta)
	}

	if meta["service_id"] != "rabbitmq" || meta["plan_id"] != "single-node" {
		t.Fatalf("service/plan enrichment missing in metadata: %#v", meta)
	}
}

// verifyBindingsIndex verifies that the bindings index is properly updated.
func verifyBindingsIndex(t *testing.T, vault *RealTestVault, instanceID, bindingID string) {
	t.Helper()

	idxPath := instanceID + "/bindings/index"

	idx, err := vault.Get(idxPath)
	if err != nil {
		t.Fatalf("expected bindings index at %s: %v", idxPath, err)
	}

	if entry, ok := idx[bindingID].(map[string]interface{}); !ok || entry["status"] != statusActive {
		t.Fatalf("expected index entry with status active, got: %#v", idx)
	}
}

func TestReconstructBindingWithBroker_BrokerError(t *testing.T) {
	t.Parallel()

	vault := NewTestVault(t)
	updater := NewVaultUpdater(vault, NewMockLogger(), BackupConfig{})

	instanceID := "00000000-0000-0000-0000-00000000dead"
	bindingID := "bind-fail"

	// broker that returns error
	bad := &reconMockBroker{creds: nil}

	// Override method via embedding not available; simulate by calling with nil creds and expecting error
	// The updater checks for nil and returns error without writing
	err := updater.ReconstructBindingWithBroker(instanceID, bindingID, bad)
	if err == nil {
		t.Fatalf("expected error from ReconstructBindingWithBroker when broker returns nil creds")
	}

	credPath := instanceID + "/bindings/" + bindingID + "/credentials"

	_, err = vault.Get(credPath)
	if err == nil {
		t.Fatalf("credentials should not be stored on broker error")
	}
}

func TestStoreBindingCredentials_PreservesHistory_And_IndexTransitions(t *testing.T) {
	t.Parallel()

	vault := NewTestVault(t)
	updater := NewVaultUpdater(vault, NewMockLogger(), BackupConfig{})

	instanceID := "00000000-0000-0000-0000-00000000cafe"
	bindingID := "bind-idx"

	// Seed existing binding metadata with history
	metaPath := instanceID + "/bindings/" + bindingID + "/metadata"
	_ = vault.SetSecret(metaPath, map[string]interface{}{
		"history": []interface{}{map[string]interface{}{"event": "created"}},
	})

	creds := map[string]interface{}{
		"host": "db.local", "port": 5432, "username": "u", "password": "p",
	}
	metadata := map[string]interface{}{"note": "reconstructed"}

	err := updater.StoreBindingCredentials(instanceID, bindingID, creds, metadata)
	if err != nil {
		t.Fatalf("StoreBindingCredentials error: %v", err)
	}

	// Metadata should still have history
	meta, err := vault.Get(metaPath)
	if err != nil {
		t.Fatalf("expected metadata persisted at %s: %v", metaPath, err)
	}

	if _, ok := meta["history"].([]interface{}); !ok {
		t.Fatalf("expected history preserved in metadata: %#v", meta)
	}

	// Index should be created and status active
	idxPath := instanceID + "/bindings/index"

	idx, err := vault.Get(idxPath)
	if err != nil {
		t.Fatalf("expected bindings index at %s: %v", idxPath, err)
	}

	if entry, ok := idx[bindingID].(map[string]interface{}); !ok || entry["status"] != statusActive {
		t.Fatalf("expected index entry with status active, got: %#v", idx)
	}

	// Now mark binding removed and verify index transition to deleted
	err = updater.RemoveBinding(instanceID, bindingID)
	if err != nil {
		t.Fatalf("RemoveBinding error: %v", err)
	}

	idx, _ = vault.Get(idxPath)
	if entry, ok := idx[bindingID].(map[string]interface{}); !ok || entry["status"] != "deleted" {
		t.Fatalf("expected index status deleted after removal, got: %#v", idx)
	}
}
