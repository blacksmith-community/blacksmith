package vmmonitor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/vmmonitor"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
)

// indexVault serves an index and fixed records to the monitor, and records
// the instances the monitor marks deleted, how many index writes it saved,
// and the last VM status it stores for each instance. A test can make later
// reads of the index or of a record fail, make index writes fail, and run a
// hook at the start of each index write.
type indexVault struct {
	entries map[string]interface{}

	// records holds the secrets Get serves, by path.
	records map[string]map[string]interface{}

	mu         sync.Mutex
	marked     []string
	saves      int
	statuses   map[string]interface{}
	indexErr   error
	getErr     error
	writeErr   error
	beforeEach func()
}

func (v *indexVault) GetIndex(context.Context, string) (*vaultPkg.Index, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.indexErr != nil {
		return nil, v.indexErr
	}

	return &vaultPkg.Index{Data: copyEntries(v.entries)}, nil
}

// UpdateIndex runs change on a fresh copy of the entries and saves the copy
// when change reports that it changed something, as the vault does under
// its index lock. It first runs the hook beforeIndexWrite set, which stands
// for whatever another goroutine does after the monitor's checks and before
// its write reads the index. An entry whose status the save turns to
// "deleted" is recorded as marked.
func (v *indexVault) UpdateIndex(_ context.Context, change func(entries map[string]interface{}) (bool, error)) error {
	v.mu.Lock()
	hook, writeErr := v.beforeEach, v.writeErr
	v.mu.Unlock()

	if hook != nil {
		hook()

		v.mu.Lock()
		writeErr = v.writeErr
		v.mu.Unlock()
	}

	if writeErr != nil {
		return writeErr
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	entries := copyEntries(v.entries)

	changed, err := change(entries)
	if err != nil || !changed {
		return err
	}

	for instanceID, entry := range entries {
		if entryStatus(entry) == statusDeleted && entryStatus(v.entries[instanceID]) != statusDeleted {
			v.marked = append(v.marked, instanceID)
		}
	}

	v.entries = entries
	v.saves++

	return nil
}

// copyEntries copies an index and each entry in it, the way a fresh read
// decodes new maps, so no caller shares an entry with the stored index.
func copyEntries(entries map[string]interface{}) map[string]interface{} {
	copied := make(map[string]interface{}, len(entries))

	for instanceID, entry := range entries {
		fields, isMap := entry.(map[string]interface{})
		if !isMap {
			copied[instanceID] = entry

			continue
		}

		copiedFields := make(map[string]interface{}, len(fields))
		for key, value := range fields {
			copiedFields[key] = value
		}

		copied[instanceID] = copiedFields
	}

	return copied
}

// entryStatus returns the status an index entry records, or "".
func entryStatus(entry interface{}) string {
	fields, _ := entry.(map[string]interface{})
	status, _ := fields["status"].(string)

	return status
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

// beforeIndexWrite sets a hook that runs at the start of every later index
// write, before the write reads the index.
func (v *indexVault) beforeIndexWrite(hook func()) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.beforeEach = hook
}

// failIndexWrites makes every later index write fail with err.
func (v *indexVault) failIndexWrites(err error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.writeErr = err
}

// removeEntry removes an instance from the index, as a finished
// deprovision would.
func (v *indexVault) removeEntry(instanceID string) {
	v.mu.Lock()
	defer v.mu.Unlock()

	delete(v.entries, instanceID)
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

// entry returns an instance's index entry as it is stored now.
func (v *indexVault) entry(instanceID string) interface{} {
	v.mu.Lock()
	defer v.mu.Unlock()

	return v.entries[instanceID]
}

// savedWrites returns how many index writes the monitor saved.
func (v *indexVault) savedWrites() int {
	v.mu.Lock()
	defer v.mu.Unlock()

	return v.saves
}

// markEntryDeleted marks an instance's index entry deleted at a time, as
// another check of the monitor would have.
func (v *indexVault) markEntryDeleted(instanceID string, deletedAt time.Time) {
	v.mu.Lock()
	defer v.mu.Unlock()

	entry, _ := v.entries[instanceID].(map[string]interface{})

	marked := make(map[string]interface{}, len(entry)+4)
	for key, existing := range entry {
		marked[key] = existing
	}

	marked["status"] = statusDeleted
	marked["deleted_at"] = deletedAt.Format(time.RFC3339)
	marked["deleted_by"] = deletedByMonitor
	marked["deletion_reason"] = "deployment not found in BOSH director"
	v.entries[instanceID] = marked
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

// The broker hands the monitor the deployment name an instance's index entry
// records when its provision completes. The monitor watches that name as it
// is, even when it does not end in plan_id-instance_id, because a name
// rebuilt from the plan ID would name a deployment the director does not
// have.
func TestMonitorWatchesTheStoredDeploymentName(t *testing.T) {
	t.Parallel()

	t.Run("the monitor watches the stored deployment name", func(t *testing.T) {
		t.Parallel()

		const (
			instanceID = "renamed"
			stored     = "valkey-blue-7f3a"
		)

		vault := &indexVault{entries: map[string]interface{}{
			instanceID: map[string]interface{}{serviceKey: serviceID, planKey: "standalone", nameKey: stored},
		}}

		director := testutil.NewScriptedBOSHDirector()
		director.GetDeploymentVMsFn = func(name string) ([]bosh.VM, error) {
			if name == stored {
				return []bosh.VM{{ID: name + "-vm", JobState: "running"}}, nil
			}

			return nil, fmt.Errorf("deployment %s: %w", name, bosh.ErrDeploymentNotFound)
		}
		director.GetDeploymentFn = func(name string) (*bosh.DeploymentDetail, error) {
			if name == stored {
				return &bosh.DeploymentDetail{Name: name}, nil
			}

			return deploymentNotFound(name)
		}
		director.FindRunningTaskForDeploymentFn = noRunningTask

		monitor := vmmonitor.NewWithVault(vault, director, noClaims{})

		err := monitor.AddService(t.Context(), instanceID, stored)
		if err != nil {
			t.Fatalf("adding the instance failed: %v", err)
		}

		got, monitored := monitor.MonitoredDeployment(instanceID)
		if !monitored || got != stored {
			t.Errorf("instance %s is monitored as deployment %q (monitored %t), want %q", instanceID, got, monitored, stored)
		}

		monitor.CheckScheduledServices(t.Context())

		listed := director.Calls("GetDeploymentVMs")
		marked := vault.markedDeleted()
		status := vault.storedStatus(instanceID)

		if len(listed) != 1 || listed[0] != stored || len(marked) != 0 || status != vmStatusRunning {
			t.Errorf("the monitor listed the VMs of %v, marked %v deleted, and stored status %q; want only %q listed, nothing marked, and running",
				listed, marked, status, stored)
		}
	})
}
