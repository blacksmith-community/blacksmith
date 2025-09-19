package reconciler_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	. "blacksmith/pkg/reconciler"
)

// Static errors for this test file.
var (
	errBoomGet = errors.New("boom-get")
	errBoomPut = errors.New("boom-put")
)

//nolint:ireturn // Test helper function - interface return allows testing different implementations
func newSync(t *testing.T, logger Logger) (Synchronizer, *RealTestVault) {
	t.Helper()
	v := NewTestVault(t)
	s := NewIndexSynchronizer(v, logger)

	return s, v
}

//nolint:funlen // This test function is intentionally long for comprehensive testing
func TestIndexSynchronizer_SyncIndex_AddAndUpdate(t *testing.T) {
	t.Parallel()

	logger := NewMockLogger()
	synchronizer, vault := newSync(t, logger)
	// seed empty index path so GetVaultIndex succeeds
	_ = vault.SetSecret("db", map[string]interface{}{})

	// Start with empty index; add one instance
	inst := InstanceData{
		ID:        "00000000-0000-0000-0000-000000000001",
		ServiceID: "redis",
		PlanID:    "cache-small",
		Deployment: DeploymentDetail{DeploymentInfo: DeploymentInfo{
			Name: "redis-cache-small-00000000-0000-0000-0000-000000000001",
		}},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Metadata:  map[string]interface{}{},
	}

	err := synchronizer.SyncIndex(context.TODO(), []InstanceData{inst})
	if err != nil {
		t.Fatalf("SyncIndex error: %v", err)
	}

	idx, err := vault.Get("db")
	if err != nil {
		t.Fatalf("failed to read index: %v", err)
	}

	entry, exists := idx[inst.ID].(map[string]interface{})
	if !exists {
		t.Fatalf("expected map entry for instance")
	}

	if entry["service_id"] != "redis" || entry["plan_id"] != "cache-small" {
		t.Fatalf("service/plan not set correctly: %#v", entry)
	}

	if entry["deployment_name"] != inst.Deployment.Name {
		t.Fatalf("deployment_name mismatch")
	}

	reconciledVal, exists := entry["reconciled"].(bool)
	if !exists {
		t.Fatalf("reconciled field must be a bool")
	}

	if reconciledVal != true {
		t.Fatalf("reconciled flag not set true")
	}

	// Update same instance; preserve created_at and parameters
	if idx[inst.ID] == nil {
		t.Fatalf("instance missing after first sync")
	}
	// seed preserved fields
	seeded, valid := idx[inst.ID].(map[string]interface{})
	if !valid {
		t.Fatalf("expected idx[inst.ID] to be map[string]interface{}, got %T", idx[inst.ID])
	}

	seeded["parameters"] = map[string]interface{}{"foo": "bar"}
	seeded["created_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
	idx[inst.ID] = seeded
	_ = vault.Put("db", idx)

	err = synchronizer.SyncIndex(context.TODO(), []InstanceData{inst})
	if err != nil {
		t.Fatalf("second SyncIndex error: %v", err)
	}

	idx2, _ := vault.Get("db")

	entry, valid = idx2[inst.ID].(map[string]interface{})
	if !valid {
		t.Fatalf("expected entry to be map[string]interface{}, got %T", idx2[inst.ID])
	}

	if _, ok := entry["parameters"].(map[string]interface{}); !ok {
		t.Fatalf("expected parameters to be preserved: %#v", entry)
	}

	if _, ok := entry["created_at"].(string); !ok {
		t.Fatalf("expected created_at to be preserved: %#v", entry)
	}
}

func TestIndexSynchronizer_SyncIndex_MarksOrphans(t *testing.T) {
	t.Parallel()

	logger := NewMockLogger()
	synchronizer, vault := newSync(t, logger)
	_ = vault.SetSecret("db", map[string]interface{}{})

	// Pre-existing instance not in new reconciled set should be marked orphaned
	_ = vault.SetSecret("db", map[string]interface{}{
		"00000000-0000-0000-0000-0000000000aa": map[string]interface{}{
			"service_id":      "rabbitmq",
			"plan_id":         "single-node",
			"deployment_name": "rabbitmq-single-node-00000000-0000-0000-0000-0000000000aa",
		},
	})

	inst := InstanceData{
		ID:        "00000000-0000-0000-0000-0000000000bb",
		ServiceID: "redis",
		PlanID:    "cache-small",
		Deployment: DeploymentDetail{DeploymentInfo: DeploymentInfo{
			Name: "redis-cache-small-00000000-0000-0000-0000-0000000000bb",
		}},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	err := synchronizer.SyncIndex(context.TODO(), []InstanceData{inst})
	if err != nil {
		t.Fatalf("SyncIndex error: %v", err)
	}

	idxOrphan, _ := vault.Get("db")

	orphan, ok := idxOrphan["00000000-0000-0000-0000-0000000000aa"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected orphan to be map[string]interface{}, got %T", idxOrphan["00000000-0000-0000-0000-0000000000aa"])
	}

	if orphan["orphaned"] != true {
		t.Fatalf("expected orphaned=true, got %#v", orphan)
	}
}

func TestIndexSynchronizer_SyncIndex_GetError(t *testing.T) {
	t.Parallel()

	logger := NewMockLogger()
	synchronizer, vault := newSync(t, logger)
	_ = vault.SetSecret("db", map[string]interface{}{})
	vault.SetError("db", errBoomGet)

	err := synchronizer.SyncIndex(context.TODO(), []InstanceData{})
	if err == nil || !strings.Contains(err.Error(), "failed to get index") {
		t.Fatalf("expected get error propagated, got: %v", err)
	}
}

func TestIndexSynchronizer_SyncIndex_SaveError(t *testing.T) {
	t.Parallel()

	logger := NewMockLogger()
	synchronizer, vault := newSync(t, logger)
	_ = vault.SetSecret("db", map[string]interface{}{})
	vault.SetError("db", errBoomPut)

	inst := InstanceData{ID: "00000000-0000-0000-0000-0000000000cc"}

	err := synchronizer.SyncIndex(context.TODO(), []InstanceData{inst})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}
