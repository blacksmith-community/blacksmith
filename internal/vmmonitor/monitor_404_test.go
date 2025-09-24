package vmmonitor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	vaultPkg "blacksmith/pkg/vault"
)

// TestVMMonitor_HandleDeploymentNotFound tests that the monitor properly handles 404 errors
func TestVMMonitor_HandleDeploymentNotFound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		boshError           error
		expectMarkDeleted   bool
		expectStopMonitoring bool
	}{
		{
			name:                "ErrDeploymentNotFound error",
			boshError:           bosh.ErrDeploymentNotFound,
			expectMarkDeleted:   true,
			expectStopMonitoring: true,
		},
		{
			name:                "404 status code in error",
			boshError:           fmt.Errorf("Director responded with non-successful status code '404' response"),
			expectMarkDeleted:   true,
			expectStopMonitoring: true,
		},
		{
			name:                "deployment doesn't exist message",
			boshError:           fmt.Errorf("Deployment 'test' doesn't exist"),
			expectMarkDeleted:   true,
			expectStopMonitoring: true,
		},
		{
			name:                "wrapped deployment not found error",
			boshError:           fmt.Errorf("failed to get VMs: %w", bosh.ErrDeploymentNotFound),
			expectMarkDeleted:   true,
			expectStopMonitoring: true,
		},
		{
			name:                "other error",
			boshError:           fmt.Errorf("network timeout"),
			expectMarkDeleted:   false,
			expectStopMonitoring: false,
		},
		{
			name:                "nil error",
			boshError:           nil,
			expectMarkDeleted:   false,
			expectStopMonitoring: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			logger := &mockLogger{logs: []string{}}
			vault := &mockVault{
				index: &vaultPkg.Index{
					Data: map[string]interface{}{
						"test-instance": map[string]interface{}{
							"service_id":     "test-instance",
							"deployment":     "test-deployment",
							"plan_id":        "test-plan",
						},
					},
				},
				data: make(map[string]map[string]interface{}),
			}
			director := &mockDirector{
				vmsError: tt.boshError,
			}

			monitor := &Monitor{
				vault:        vault,
				boshDirector: director,
				services: map[string]*ServiceMonitor{
					"test-instance": &ServiceMonitor{
						ServiceID:      "test-instance",
						DeploymentName: "test-deployment",
					},
				},
			}

			// Check VMs which should trigger the error handling
			monitor.checkScheduledServices(ctx)

			// Check if instance was marked as deleted
			if tt.expectMarkDeleted {
				if !vault.markedDeleted["test-instance"] {
					t.Errorf("expected instance to be marked as deleted")
				}

				// Check if appropriate log was written
				foundLog := false
				for _, log := range logger.logs {
					if contains(log, "not found") && contains(log, "404") {
						foundLog = true
						break
					}
				}
				if !foundLog {
					t.Errorf("expected log about deployment not found")
				}
			} else {
				if vault.markedDeleted["test-instance"] {
					t.Errorf("instance should not be marked as deleted")
				}
			}

			// Check if service was removed from monitoring
			if tt.expectStopMonitoring {
				if _, exists := monitor.services["test-instance"]; exists {
					t.Errorf("expected service to be removed from monitoring")
				}
			} else if tt.boshError == nil {
				if _, exists := monitor.services["test-instance"]; !exists {
					t.Errorf("service should still be monitored")
				}
			}
		})
	}
}

// TestVMMonitor_MarkInstanceDeleted tests the markInstanceDeleted method
func TestVMMonitor_MarkInstanceDeleted(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	logger := &mockLogger{logs: []string{}}
	vault := &mockVault{
		index: &vaultPkg.Index{
			Data: map[string]interface{}{
				"test-instance": map[string]interface{}{
					"service_id": "test-instance",
					"deployment": "test-deployment",
					"plan_id":    "test-plan",
				},
			},
		},
		data: make(map[string]map[string]interface{}),
	}

	monitor := &Monitor{
		vault:  vault,
		services: map[string]*ServiceMonitor{
			"test-instance": &ServiceMonitor{
				ServiceID:      "test-instance",
				DeploymentName: "test-deployment",
			},
		},
	}

	// Mark instance as deleted
	monitor.markInstanceDeleted(ctx, monitor.services["test-instance"])

	// Verify MarkInstanceDeleted was called on vault
	if !vault.markedDeleted["test-instance"] {
		t.Errorf("expected MarkInstanceDeleted to be called on vault")
	}

	// Verify service was removed from monitoring
	if _, exists := monitor.services["test-instance"]; exists {
		t.Errorf("expected service to be removed from monitoring")
	}

	// Verify appropriate logs
	foundLog := false
	for _, log := range logger.logs {
		if contains(log, "Marked instance") && contains(log, "deleted") {
			foundLog = true
			break
		}
	}
	if !foundLog {
		t.Errorf("expected log about marking instance as deleted")
	}
}

// Mock implementations for testing

type mockVault struct {
	index         *vaultPkg.Index
	data          map[string]map[string]interface{}
	markedDeleted map[string]bool
}

func (v *mockVault) GetIndex(ctx context.Context, name string) (*vaultPkg.Index, error) {
	if name != "db" {
		return nil, fmt.Errorf("unknown index: %s", name)
	}
	return v.index, nil
}

func (v *mockVault) Get(ctx context.Context, path string, out interface{}) (bool, error) {
	data, exists := v.data[path]
	if !exists {
		return false, nil
	}
	// Simulate copying data to out parameter
	if outMap, ok := out.(*map[string]interface{}); ok {
		*outMap = data
	}
	return true, nil
}

func (v *mockVault) Put(ctx context.Context, path string, data interface{}) error {
	if dataMap, ok := data.(map[string]interface{}); ok {
		v.data[path] = dataMap
	}
	return nil
}

func (v *mockVault) UpdateIndexEntry(ctx context.Context, instanceID string, updates map[string]interface{}) error {
	if v.index == nil {
		return errors.New("no index")
	}
	entry, exists := v.index.Data[instanceID]
	if !exists {
		v.index.Data[instanceID] = updates
	} else if entryMap, ok := entry.(map[string]interface{}); ok {
		for k, v := range updates {
			entryMap[k] = v
		}
		v.index.Data[instanceID] = entryMap
	}
	return nil
}

func (v *mockVault) MarkInstanceDeleted(ctx context.Context, instanceID string) error {
	if v.markedDeleted == nil {
		v.markedDeleted = make(map[string]bool)
	}
	v.markedDeleted[instanceID] = true

	// Also update the index entry
	return v.UpdateIndexEntry(ctx, instanceID, map[string]interface{}{
		"status":     "deleted",
		"deleted_at": time.Now().Format(time.RFC3339),
	})
}

type mockDirector struct {
	vms      []bosh.VM
	vmsError error
}

func (d *mockDirector) GetDeploymentVMs(deployment string) ([]bosh.VM, error) {
	if d.vmsError != nil {
		return nil, d.vmsError
	}
	return d.vms, nil
}

// Implement other required methods for bosh.Director interface
func (d *mockDirector) GetInfo() (*bosh.Info, error) { return nil, nil }
func (d *mockDirector) GetDeployments() ([]bosh.Deployment, error) { return nil, nil }
func (d *mockDirector) GetDeployment(name string) (*bosh.DeploymentDetail, error) { return nil, nil }
func (d *mockDirector) CreateDeployment(manifest string) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) DeleteDeployment(name string) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) GetReleases() ([]bosh.Release, error) { return nil, nil }
func (d *mockDirector) UploadRelease(url, sha1 string) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) GetStemcells() ([]bosh.Stemcell, error) { return nil, nil }
func (d *mockDirector) UploadStemcell(url, sha1 string) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) GetTask(id int) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) CancelTask(taskID int) error { return nil }
func (d *mockDirector) WaitForTask(taskID int, pollInterval time.Duration) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) GetManifest(deployment string) (string, error) { return "", nil }
func (d *mockDirector) GetCloudConfig() (string, error) { return "", nil }
func (d *mockDirector) UpdateRuntimeConfig(yaml string) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) GetRuntimeConfig() (string, error) { return "", nil }
func (d *mockDirector) GetSnapshots(deployment string) ([]interface{}, error) { return nil, nil }
func (d *mockDirector) TakeSnapshot(deployment string) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) DeleteSnapshot(deployment string, cid string) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) RestartVM(deployment, cid string) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) StopVM(deployment, cid string) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) StartVM(deployment, cid string) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) RecreateVM(deployment, cid string) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) ComputeConfigDiff(fromID, toID string) (*bosh.ConfigDiff, error) { return nil, nil }

// Missing methods from current bosh.Director interface
func (d *mockDirector) GetTasks(taskType string, limit int, states []string, team string) ([]bosh.Task, error) { return nil, nil }
func (d *mockDirector) GetAllTasks(limit int) ([]bosh.Task, error) { return nil, nil }
func (d *mockDirector) GetTaskOutput(id int, outputType string) (string, error) { return "", nil }
func (d *mockDirector) GetTaskEvents(id int) ([]bosh.TaskEvent, error) { return nil, nil }
func (d *mockDirector) GetEvents(deployment string) ([]bosh.Event, error) { return nil, nil }
func (d *mockDirector) FetchLogs(deployment string, jobName string, jobIndex string) (string, error) { return "", nil }
func (d *mockDirector) UpdateCloudConfig(config string) error { return nil }
func (d *mockDirector) GetConfig(configType, configName string) (interface{}, error) { return nil, nil }
func (d *mockDirector) GetConfigs(limit int, configTypes []string) ([]bosh.BoshConfig, error) { return nil, nil }
func (d *mockDirector) GetConfigVersions(configType, name string, limit int) ([]bosh.BoshConfig, error) { return nil, nil }
func (d *mockDirector) GetConfigByID(configID string) (*bosh.BoshConfigDetail, error) { return nil, nil }
func (d *mockDirector) GetConfigContent(configID string) (string, error) { return "", nil }
func (d *mockDirector) Cleanup(removeAll bool) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) SSHCommand(deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) { return "", nil }
func (d *mockDirector) SSHSession(deployment, instance string, index int, options map[string]interface{}) (interface{}, error) { return nil, nil }
func (d *mockDirector) EnableResurrection(deployment string, enabled bool) error { return nil }
func (d *mockDirector) DeleteResurrectionConfig(deployment string) error { return nil }
func (d *mockDirector) RestartDeployment(name string, opts bosh.RestartOpts) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) StopDeployment(name string, opts bosh.StopOpts) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) StartDeployment(name string, opts bosh.StartOpts) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) RecreateDeployment(name string, opts bosh.RecreateOpts) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) ListErrands(deployment string) ([]bosh.Errand, error) { return nil, nil }
func (d *mockDirector) RunErrand(deployment, errand string, opts bosh.ErrandOpts) (*bosh.ErrandResult, error) { return nil, nil }
func (d *mockDirector) GetInstances(deployment string) ([]bosh.Instance, error) { return nil, nil }
func (d *mockDirector) UpdateDeployment(name, manifest string) (*bosh.Task, error) { return nil, nil }
func (d *mockDirector) GetPoolStats() (*bosh.PoolStats, error) { return nil, nil }

type mockLogger struct {
	logs []string
}

func (l *mockLogger) Info(format string, args ...interface{}) {
	l.logs = append(l.logs, fmt.Sprintf(format, args...))
}

func (l *mockLogger) Debug(format string, args ...interface{}) {
	l.logs = append(l.logs, fmt.Sprintf(format, args...))
}

func (l *mockLogger) Error(format string, args ...interface{}) {
	l.logs = append(l.logs, fmt.Sprintf(format, args...))
}

func (l *mockLogger) Named(name string) *mockLogger {
	return l
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && s[len(s)-len(substr):] == substr ||
		len(substr) > 0 && len(s) > len(substr) && s[:len(substr)] == substr ||
		len(s) > len(substr) && findSubstring(s, substr)
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}