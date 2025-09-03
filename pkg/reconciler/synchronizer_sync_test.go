package reconciler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func newSync(logger Logger) (*indexSynchronizer, *memVault) {
	v := newMemVault()
	s := NewIndexSynchronizer(v, logger).(*indexSynchronizer)
	return s, v
}

func TestIndexSynchronizer_SyncIndex_AddAndUpdate(t *testing.T) {
	logger := newMockLogger()
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

	if err := s.SyncIndex(context.TODO(), []InstanceData{inst}); err != nil {
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
	if entry["reconciled"].(bool) != true {
		t.Fatalf("reconciled flag not set true")
	}

	// Update same instance; preserve created_at and parameters
	if idx[inst.ID] == nil {
		t.Fatalf("instance missing after first sync")
	}
	// seed preserved fields
	seeded := idx[inst.ID].(map[string]interface{})
	seeded["parameters"] = map[string]interface{}{"foo": "bar"}
	seeded["created_at"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
	v.store["db"][inst.ID] = seeded

	if err := s.SyncIndex(context.TODO(), []InstanceData{inst}); err != nil {
		t.Fatalf("second SyncIndex error: %v", err)
	}
	entry = v.store["db"][inst.ID].(map[string]interface{})
	if _, ok := entry["parameters"].(map[string]interface{}); !ok {
		t.Fatalf("expected parameters to be preserved: %#v", entry)
	}
	if _, ok := entry["created_at"].(string); !ok {
		t.Fatalf("expected created_at to be preserved: %#v", entry)
	}
}

func TestIndexSynchronizer_SyncIndex_MarksOrphans(t *testing.T) {
	logger := newMockLogger()
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

	if err := s.SyncIndex(context.TODO(), []InstanceData{inst}); err != nil {
		t.Fatalf("SyncIndex error: %v", err)
	}

	orphan := v.store["db"]["00000000-0000-0000-0000-0000000000aa"].(map[string]interface{})
	if orphan["orphaned"] != true {
		t.Fatalf("expected orphaned=true, got %#v", orphan)
	}
}

func TestIndexSynchronizer_SyncIndex_GetError(t *testing.T) {
	logger := newMockLogger()
	s, v := newSync(logger)
	v.getErr = errors.New("boom-get")

	err := s.SyncIndex(context.TODO(), []InstanceData{})
	if err == nil || !strings.Contains(err.Error(), "failed to get index") {
		t.Fatalf("expected get error propagated, got: %v", err)
	}
}

func TestIndexSynchronizer_SyncIndex_SaveError(t *testing.T) {
	logger := newMockLogger()
	s, v := newSync(logger)
	v.putErr = errors.New("boom-put")

	inst := InstanceData{ID: "00000000-0000-0000-0000-0000000000cc"}
	err := s.SyncIndex(context.TODO(), []InstanceData{inst})
	if err == nil || !strings.Contains(err.Error(), "failed to save index") {
		t.Fatalf("expected save error propagated, got: %v", err)
	}
}
