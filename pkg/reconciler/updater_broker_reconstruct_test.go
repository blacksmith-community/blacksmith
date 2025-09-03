package reconciler

import (
	"testing"
	"time"
)

// mockBroker implements BrokerInterface for reconstruction tests
type reconMockBroker struct {
	creds *BindingCredentials
}

func (m *reconMockBroker) GetServices() []Service { return nil }
func (m *reconMockBroker) GetBindingCredentials(instanceID, bindingID string) (*BindingCredentials, error) {
	return m.creds, nil
}

func TestReconstructBindingWithBroker_StoresCredentialsAndMetadata(t *testing.T) {
	v := newMemVault()
	u := &vaultUpdater{vault: v, logger: newMockLogger()}

	instanceID := "00000000-0000-0000-0000-00000000beef"
	bindingID := "bind-abc"

	// Seed index with service/plan so metadata enrichment can pick it up
	v.store["db"] = map[string]interface{}{
		instanceID: map[string]interface{}{
			"service_id": "rabbitmq",
			"plan_id":    "single-node",
		},
	}

	// Broker returns credentials consistent with broker logic
	mb := &reconMockBroker{creds: &BindingCredentials{
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

	if err := u.ReconstructBindingWithBroker(instanceID, bindingID, mb); err != nil {
		t.Fatalf("ReconstructBindingWithBroker error: %v", err)
	}

	// Verify credentials stored
	credPath := instanceID + "/bindings/" + bindingID + "/credentials"
	creds := v.store[credPath]
	if creds == nil {
		t.Fatalf("expected credentials stored at %s", credPath)
	}
	if creds["host"] != "rmq.local" || int(creds["port"].(int)) != 5672 {
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

	// Verify metadata stored
	metaPath := instanceID + "/bindings/" + bindingID + "/metadata"
	meta := v.store[metaPath]
	if meta == nil {
		t.Fatalf("expected metadata stored at %s", metaPath)
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

	// Verify bindings index updated
	idxPath := instanceID + "/bindings/index"
	idx := v.store[idxPath]
	if idx == nil {
		t.Fatalf("expected bindings index at %s", idxPath)
	}
	if entry, ok := idx[bindingID].(map[string]interface{}); !ok || entry["status"] != "active" {
		t.Fatalf("expected index entry with status active, got: %#v", idx)
	}
}

func TestReconstructBindingWithBroker_BrokerError(t *testing.T) {
	v := newMemVault()
	u := &vaultUpdater{vault: v, logger: newMockLogger()}

	instanceID := "00000000-0000-0000-0000-00000000dead"
	bindingID := "bind-fail"

	// broker that returns error
	bad := &reconMockBroker{creds: nil}

	// Override method via embedding not available; simulate by calling with nil creds and expecting error
	// The updater checks for nil and returns error without writing
	if err := u.ReconstructBindingWithBroker(instanceID, bindingID, bad); err == nil {
		t.Fatalf("expected error from ReconstructBindingWithBroker when broker returns nil creds")
	}

	credPath := instanceID + "/bindings/" + bindingID + "/credentials"
	if v.store[credPath] != nil {
		t.Fatalf("credentials should not be stored on broker error")
	}
}

func TestStoreBindingCredentials_PreservesHistory_And_IndexTransitions(t *testing.T) {
	v := newMemVault()
	u := &vaultUpdater{vault: v, logger: newMockLogger()}

	instanceID := "00000000-0000-0000-0000-00000000cafe"
	bindingID := "bind-idx"

	// Seed existing binding metadata with history
	metaPath := instanceID + "/bindings/" + bindingID + "/metadata"
	v.store[metaPath] = map[string]interface{}{
		"history": []interface{}{map[string]interface{}{"event": "created"}},
	}

	creds := map[string]interface{}{
		"host": "db.local", "port": 5432, "username": "u", "password": "p",
	}
	metadata := map[string]interface{}{"note": "reconstructed"}

	if err := u.StoreBindingCredentials(instanceID, bindingID, creds, metadata); err != nil {
		t.Fatalf("StoreBindingCredentials error: %v", err)
	}

	// Metadata should still have history
	meta := v.store[metaPath]
	if meta == nil {
		t.Fatalf("expected metadata persisted at %s", metaPath)
	}
	if _, ok := meta["history"].([]interface{}); !ok {
		t.Fatalf("expected history preserved in metadata: %#v", meta)
	}

	// Index should be created and status active
	idxPath := instanceID + "/bindings/index"
	idx := v.store[idxPath]
	if idx == nil {
		t.Fatalf("expected bindings index at %s", idxPath)
	}
	if entry, ok := idx[bindingID].(map[string]interface{}); !ok || entry["status"] != "active" {
		t.Fatalf("expected index entry with status active, got: %#v", idx)
	}

	// Now mark binding removed and verify index transition to deleted
	if err := u.RemoveBinding(instanceID, bindingID); err != nil {
		t.Fatalf("RemoveBinding error: %v", err)
	}
	idx = v.store[idxPath]
	if entry, ok := idx[bindingID].(map[string]interface{}); !ok || entry["status"] != "deleted" {
		t.Fatalf("expected index status deleted after removal, got: %#v", idx)
	}
}
