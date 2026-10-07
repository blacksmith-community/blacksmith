package vmmonitor_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/vmmonitor"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
)

// indexVault serves a fixed index to the monitor and records the instances
// the monitor marks deleted.
type indexVault struct {
	entries map[string]interface{}

	mu     sync.Mutex
	marked []string
}

func (v *indexVault) GetIndex(context.Context, string) (*vaultPkg.Index, error) {
	data := make(map[string]interface{}, len(v.entries))
	for id, entry := range v.entries {
		data[id] = entry
	}

	return &vaultPkg.Index{Data: data}, nil
}

func (v *indexVault) Get(context.Context, string, interface{}) (bool, error) {
	return false, nil
}

func (v *indexVault) Put(context.Context, string, interface{}) error {
	return nil
}

func (v *indexVault) UpdateIndexEntry(_ context.Context, instanceID string, updates map[string]interface{}) error {
	if status, _ := updates["status"].(string); status == "deleted" {
		v.record(instanceID)
	}

	return nil
}

func (v *indexVault) MarkInstanceDeleted(_ context.Context, instanceID string) error {
	v.record(instanceID)

	return nil
}

func (v *indexVault) record(instanceID string) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.marked = append(v.marked, instanceID)
}

func (v *indexVault) markedDeleted() []string {
	v.mu.Lock()
	defer v.mu.Unlock()

	return append([]string(nil), v.marked...)
}

// The monitor marks an instance deleted when the director has no deployment
// by the name it checks. The reconciler can rewrite an entry's plan ID after
// the deployment was created, so the monitor checks the deployment the entry
// records, and builds the name from the plan ID only for an entry that
// records none.
func TestMonitorChecksTheDeploymentAnIndexEntryRecords(t *testing.T) {
	t.Parallel()

	discoveries := []struct {
		name     string
		discover func(ctx context.Context, monitor *vmmonitor.Monitor) error
	}{
		{"scan", func(ctx context.Context, monitor *vmmonitor.Monitor) error { return monitor.ScanAllServices(ctx) }},
		{"rescan", func(ctx context.Context, monitor *vmmonitor.Monitor) error {
			monitor.RescanServices(ctx)

			return nil
		}},
	}

	for _, discovery := range discoveries {
		t.Run(discovery.name, func(t *testing.T) {
			t.Parallel()

			const (
				adoptedID  = "adopted"
				adoptedRun = "standalone-classic-adopted"
				legacyID   = "legacy"
				legacyRun  = "standalone-legacy"
			)

			vault := &indexVault{entries: map[string]interface{}{
				adoptedID: map[string]interface{}{"service_id": "valkey", "plan_id": "standalone", "deployment_name": adoptedRun},
				legacyID:  map[string]interface{}{"service_id": "valkey", "plan_id": "standalone"},
			}}

			director := testutil.NewScriptedBOSHDirector()
			director.GetDeploymentVMsFn = func(name string) ([]bosh.VM, error) {
				if name == adoptedRun || name == legacyRun {
					return []bosh.VM{{ID: name + "-vm", JobState: "running"}}, nil
				}

				return nil, fmt.Errorf("deployment %s: %w", name, bosh.ErrDeploymentNotFound)
			}

			monitor := vmmonitor.NewWithVault(vault, director)

			err := discovery.discover(t.Context(), monitor)
			if err != nil {
				t.Fatalf("the %s failed: %v", discovery.name, err)
			}

			for id, want := range map[string]string{adoptedID: adoptedRun, legacyID: legacyRun} {
				got, monitored := monitor.MonitoredDeployment(id)
				if !monitored || got != want {
					t.Errorf("instance %s is monitored as deployment %q (monitored %t), want %q", id, got, monitored, want)
				}
			}

			monitor.CheckScheduledServices(t.Context())

			marked := vault.markedDeleted()
			if len(marked) != 0 {
				t.Fatalf("the monitor marked live instances deleted: %v (it asked for %v)", marked, director.Calls("GetDeploymentVMs"))
			}
		})
	}
}
