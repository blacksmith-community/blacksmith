package vmmonitor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/vmmonitor"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
)

// indexVault serves a fixed index and fixed records to the monitor, and
// records the instances the monitor marks deleted and the last VM status it
// stores for each. A test can make later reads of the index or of a record
// fail.
type indexVault struct {
	entries map[string]interface{}

	// records holds the secrets Get serves, by path.
	records map[string]map[string]interface{}

	mu       sync.Mutex
	marked   []string
	statuses map[string]interface{}
	indexErr error
	getErr   error
}

func (v *indexVault) GetIndex(context.Context, string) (*vaultPkg.Index, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.indexErr != nil {
		return nil, v.indexErr
	}

	data := make(map[string]interface{}, len(v.entries))
	for id, entry := range v.entries {
		data[id] = entry
	}

	return &vaultPkg.Index{Data: data}, nil
}

// Get decodes the record at path into out the way the vault client does,
// through JSON, so numbers arrive as float64.
func (v *indexVault) Get(_ context.Context, path string, out interface{}) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.getErr != nil {
		return false, v.getErr
	}

	record, exists := v.records[path]
	if !exists {
		return false, nil
	}

	encoded, err := json.Marshal(record)
	if err != nil {
		return false, fmt.Errorf("encode %s: %w", path, err)
	}

	err = json.Unmarshal(encoded, out)
	if err != nil {
		return false, fmt.Errorf("decode %s: %w", path, err)
	}

	return true, nil
}

func (v *indexVault) Put(_ context.Context, path string, data interface{}) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.statuses == nil {
		v.statuses = map[string]interface{}{}
	}

	v.statuses[path] = data

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

// failIndexReads makes every later read of the index fail with err.
func (v *indexVault) failIndexReads(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.indexErr = err
}

// failRecordReads makes every later read of a record fail with err.
func (v *indexVault) failRecordReads(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.getErr = err
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

// storedStatus returns the status of the last VM status the monitor stored
// for an instance, or "" when it stored none.
func (v *indexVault) storedStatus(instanceID string) string {
	v.mu.Lock()
	defer v.mu.Unlock()

	stored, _ := v.statuses[instanceID+"/vm_status"].(map[string]interface{})
	status, _ := stored["status"].(string)

	return status
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
			director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
				if name == adoptedRun || name == legacyRun {
					return &bosh.DeploymentDetail{Name: name}, nil
				}

				return deploymentNotFound(name)
			}
			director.FindRunningTaskForDeploymentFn = noRunningTask

			monitor := vmmonitor.NewWithVault(vault, director, noClaims{})

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

// setEntryTime sets one time on an instance's index entry, as a later write
// to the index would.
func (v *indexVault) setEntryTime(instanceID, field, value string) {
	v.mu.Lock()
	defer v.mu.Unlock()

	entry, _ := v.entries[instanceID].(map[string]interface{})

	updated := make(map[string]interface{}, len(entry)+1)
	for key, existing := range entry {
		updated[key] = existing
	}

	updated[field] = value
	v.entries[instanceID] = updated
}
