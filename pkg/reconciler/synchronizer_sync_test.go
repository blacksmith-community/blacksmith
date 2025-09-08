package reconciler_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	. "blacksmith/pkg/reconciler"
)

// Static errors for this test file
var (
	errBoomGet = errors.New("boom-get")
	errBoomPut = errors.New("boom-put")
)

//nolint:ireturn // Test helper function - interface return allows testing different implementations
func newSync(logger Logger) (Synchronizer, *memVault) {
	v := newMemVault()
	s := NewIndexSynchronizer(v, logger)

	return s, v
}

func TestIndexSynchronizer_SyncIndex_AddAndUpdate(t *testing.T) {
	t.Parallel()

	logger := NewMockLogger()
	s, v := newSync(logger)

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

	err := s.SyncIndex(context.TODO(), []InstanceData{inst})
	if err != nil {
		t.Fatalf("SyncIndex error: %v", err)
	}

	idx := v.store["db"]
	if idx == nil {
		t.Fatalf("expected index stored at 'db'")
	}

	entry, ok := idx[inst.ID].(map[string]interface{})
	if !ok {
		t.Fatalf("expected map entry for instance")
	}

	if entry["service_id"] != "redis" || entry["plan_id"] != "cache-small" {
		t.Fatalf("service/plan not set correctly: %#v", entry)
	}

	if entry["deployment_name"] != inst.Deployment.Name {
		t.Fatalf("deployment_name mismatch")
	}

	reconciledVal, ok := entry["reconciled"].(bool)
	if !ok {
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
	seeded, ok := idx[inst.ID].(map[string]interface{})
	if !ok {
		t.Fatalf("expected idx[inst.ID] to be map[string]interface{}, got %T", idx[inst.ID])
	}

	seeded["parameters"] = map[string]interface{}{"foo": "bar"}
	seeded["created_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
	v.store["db"][inst.ID] = seeded

	err = s.SyncIndex(context.TODO(), []InstanceData{inst})
	if err != nil {
		t.Fatalf("second SyncIndex error: %v", err)
	}

	entry, ok = v.store["db"][inst.ID].(map[string]interface{})
	if !ok {
		t.Fatalf("expected entry to be map[string]interface{}, got %T", v.store["db"][inst.ID])
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
	s, v := newSync(logger)

	// Pre-existing instance not in new reconciled set should be marked orphaned
	v.store["db"] = map[string]interface{}{
		"00000000-0000-0000-0000-0000000000aa": map[string]interface{}{
			"service_id":      "rabbitmq",
			"plan_id":         "single-node",
			"deployment_name": "rabbitmq-single-node-00000000-0000-0000-0000-0000000000aa",
		},
	}

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

	err := s.SyncIndex(context.TODO(), []InstanceData{inst})
	if err != nil {
		t.Fatalf("SyncIndex error: %v", err)
	}

	orphan, ok := v.store["db"]["00000000-0000-0000-0000-0000000000aa"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected orphan to be map[string]interface{}, got %T", v.store["db"]["00000000-0000-0000-0000-0000000000aa"])
	}

	if orphan["orphaned"] != true {
		t.Fatalf("expected orphaned=true, got %#v", orphan)
	}
}

func TestIndexSynchronizer_SyncIndex_GetError(t *testing.T) {
	t.Parallel()

	logger := NewMockLogger()
	s, v := newSync(logger)
	v.getErr = errBoomGet

	err := s.SyncIndex(context.TODO(), []InstanceData{})
	if err == nil || !strings.Contains(err.Error(), "failed to get index") {
		t.Fatalf("expected get error propagated, got: %v", err)
	}
}

func TestIndexSynchronizer_SyncIndex_SaveError(t *testing.T) {
	t.Parallel()

	logger := NewMockLogger()
	s, v := newSync(logger)
	v.putErr = errBoomPut

	inst := InstanceData{ID: "00000000-0000-0000-0000-0000000000cc"}

	err := s.SyncIndex(context.TODO(), []InstanceData{inst})
	if err == nil || !strings.Contains(err.Error(), "failed to save index") {
		t.Fatalf("expected save error propagated, got: %v", err)
	}
}
