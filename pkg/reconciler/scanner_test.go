package reconciler_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"blacksmith/bosh"
	. "blacksmith/pkg/reconciler"
)

// Static errors for scanner test err113 compliance.
var (
	ErrScannerNotImplemented = errors.New("scanner not implemented")
	ErrDirectorUnavailable   = errors.New("director unavailable")
	errDeploymentNotFound    = errors.New("deployment not found")
)

// Mock BOSH Director for testing.
type mockDirector struct {
	deployments []bosh.Deployment
	details     map[string]*bosh.DeploymentDetail
	vms         map[string][]bosh.VM
	getErr      error
	detailErr   error
	vmErr       error
}

func (d *mockDirector) GetInfo() (*bosh.Info, error) {
	return &bosh.Info{Name: "test-director", UUID: "test-uuid"}, nil
}

func (d *mockDirector) GetDeployments() ([]bosh.Deployment, error) {
	if d.getErr != nil {
		return nil, d.getErr
	}

	return d.deployments, nil
}

func (d *mockDirector) GetDeployment(name string) (*bosh.DeploymentDetail, error) {
	if d.detailErr != nil {
		return nil, d.detailErr
	}

	if detail, ok := d.details[name]; ok {
		return detail, nil
	}

	return nil, fmt.Errorf("%w: %s", errDeploymentNotFound, name)
}

func (d *mockDirector) GetDeploymentVMs(deployment string) ([]bosh.VM, error) {
	if d.vmErr != nil {
		return nil, d.vmErr
	}

	if vms, ok := d.vms[deployment]; ok {
		return vms, nil
	}

	return []bosh.VM{}, nil
}

func (d *mockDirector) GetConfig(configType, configName string) (interface{}, error) {
	return nil, nil
}

func (d *mockDirector) CreateDeployment(manifest string) (*bosh.Task, error) {
	return nil, ErrScannerNotImplemented
}

func (d *mockDirector) DeleteDeployment(name string) (*bosh.Task, error) {
	return nil, ErrScannerNotImplemented
}

func (d *mockDirector) GetReleases() ([]bosh.Release, error) {
	return nil, ErrScannerNotImplemented
}

func (d *mockDirector) UploadRelease(url, sha1 string) (*bosh.Task, error) {
	return nil, ErrScannerNotImplemented
}

func (d *mockDirector) GetStemcells() ([]bosh.Stemcell, error) {
	return nil, ErrScannerNotImplemented
}

func (d *mockDirector) UploadStemcell(url, sha1 string) (*bosh.Task, error) {
	return nil, ErrScannerNotImplemented
}

func (d *mockDirector) GetTask(id int) (*bosh.Task, error) {
	return nil, ErrScannerNotImplemented
}

func (d *mockDirector) GetTaskOutput(id int, outputType string) (string, error) {
	return "", ErrScannerNotImplemented
}

func (d *mockDirector) GetTaskEvents(id int) ([]bosh.TaskEvent, error) {
	return nil, ErrScannerNotImplemented
}

func (d *mockDirector) GetEvents(deployment string) ([]bosh.Event, error) {
	return nil, ErrScannerNotImplemented
}

func (d *mockDirector) FetchLogs(deployment string, jobName string, jobIndex string) (string, error) {
	return "", ErrScannerNotImplemented
}

func (d *mockDirector) UpdateCloudConfig(config string) error {
	return ErrScannerNotImplemented
}

func (d *mockDirector) GetCloudConfig() (string, error) {
	return "", ErrScannerNotImplemented
}

func (d *mockDirector) Cleanup(removeAll bool) (*bosh.Task, error) {
	return nil, ErrScannerNotImplemented
}

func (d *mockDirector) SSHCommand(deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error) {
	return "", ErrScannerNotImplemented
}

func (d *mockDirector) SSHSession(deployment, instance string, index int, options map[string]interface{}) (interface{}, error) {
	return nil, ErrScannerNotImplemented
}
func (d *mockDirector) EnableResurrection(deployment string, enabled bool) error {
	return ErrScannerNotImplemented
}

func (d *mockDirector) DeleteResurrectionConfig(deployment string) error {
	return ErrScannerNotImplemented
}

// New task methods added for Tasks feature.
func (d *mockDirector) GetTasks(taskType string, limit int, states []string, team string) ([]bosh.Task, error) {
	return nil, ErrScannerNotImplemented
}

func (d *mockDirector) GetAllTasks(limit int) ([]bosh.Task, error) {
	return nil, ErrScannerNotImplemented
}

func (d *mockDirector) CancelTask(taskID int) error {
	return ErrScannerNotImplemented
}

func (d *mockDirector) GetConfigs(limit int, configTypes []string) ([]bosh.BoshConfig, error) {
	return nil, nil
}

func (d *mockDirector) GetConfigVersions(configType, name string, limit int) ([]bosh.BoshConfig, error) {
	return nil, nil
}

func (d *mockDirector) GetConfigByID(configID string) (*bosh.BoshConfigDetail, error) {
	return nil, nil
}

func (d *mockDirector) GetConfigContent(configID string) (string, error) {
	return "", nil
}

func (d *mockDirector) ComputeConfigDiff(fromID, toID string) (*bosh.ConfigDiff, error) {
	return nil, nil
}

func TestBOSHScanner_ScanDeployments(t *testing.T) {
	t.Parallel()

	director := &mockDirector{
		deployments: []bosh.Deployment{
			{
				Name:        "redis-deployment",
				CloudConfig: "v1",
				Releases:    []string{"redis/1.0.0", "bpm/1.1.0"},
				Stemcells:   []string{"ubuntu-xenial/456.789"},
				Teams:       []string{"team1"},
			},
			{
				Name:        "postgres-deployment",
				CloudConfig: "v1",
				Releases:    []string{"postgres/42.0.0"},
				Stemcells:   []string{"ubuntu-bionic/621.0"},
				Teams:       []string{"team2"},
			},
		},
	}

	logger := NewMockLogger()
	scanner := NewBOSHScanner(director, logger)

	ctx := context.Background()

	deployments, err := scanner.ScanDeployments(ctx)
	if err != nil {
		t.Fatalf("Failed to scan deployments: %v", err)
	}

	if len(deployments) != 2 {
		t.Errorf("Expected 2 deployments, got %d", len(deployments))
	}

	// Check first deployment
	if deployments[0].Name != "redis-deployment" {
		t.Errorf("Expected name redis-deployment, got %s", deployments[0].Name)
	}

	if len(deployments[0].Releases) != 2 {
		t.Errorf("Expected 2 releases, got %d", len(deployments[0].Releases))
	}

	if deployments[0].Releases[0] != "redis/1.0.0" {
		t.Errorf("Expected release redis/1.0.0, got %s", deployments[0].Releases[0])
	}

	// Check stemcell parsing
	if len(deployments[0].Stemcells) != 1 {
		t.Errorf("Expected 1 stemcell, got %d", len(deployments[0].Stemcells))
	}

	if deployments[0].Stemcells[0] != "ubuntu-xenial/456.789" {
		t.Errorf("Expected stemcell ubuntu-xenial/456.789, got %s", deployments[0].Stemcells[0])
	}
}

func TestBOSHScanner_ScanDeployments_Error(t *testing.T) {
	t.Parallel()

	director := &mockDirector{
		getErr: ErrDirectorUnavailable,
	}

	logger := NewMockLogger()
	scanner := NewBOSHScanner(director, logger)

	ctx := context.Background()

	_, err := scanner.ScanDeployments(ctx)
	if err == nil {
		t.Error("Expected error but got nil")
	}
}

func TestBOSHScanner_GetDeploymentDetails(t *testing.T) {
	t.Parallel()

	manifest := `
name: redis-deployment
releases:
  - name: redis
    version: 1.0.0
    url: https://example.com/redis.tgz
    sha1: abc123
  - name: bpm
    version: 1.1.0
stemcells:
  - alias: default
    os: ubuntu-xenial
    version: "456.789"
instance_groups:
  - name: redis
    instances: 3
properties:
  redis:
    password: secret
  blacksmith:
    service_id: redis-cache
    plan_id: small
    instance_id: inst-123
`

	director := &mockDirector{
		details: map[string]*bosh.DeploymentDetail{
			"redis-deployment": {
				Name:     "redis-deployment",
				Manifest: manifest,
			},
		},
		vms: map[string][]bosh.VM{
			"redis-deployment": {
				{
					ID:           "vm-1",
					CID:          "i-abc123",
					Job:          "redis",
					Index:        0,
					State:        "running",
					IPs:          []string{"10.0.0.1"},
					AZ:           "az1",
					ResourcePool: "small",
				},
				{
					ID:           "vm-2",
					CID:          "i-def456",
					Job:          "redis",
					Index:        1,
					State:        "running",
					IPs:          []string{"10.0.0.2"},
					AZ:           "az2",
					ResourcePool: "small",
				},
			},
		},
	}

	logger := NewMockLogger()
	scanner := NewBOSHScanner(director, logger)

	ctx := context.Background()

	detail, err := scanner.GetDeploymentDetails(ctx, "redis-deployment")
	if err != nil {
		t.Fatalf("Failed to get deployment details: %v", err)
	}

	if detail.Name != "redis-deployment" {
		t.Errorf("Expected name redis-deployment, got %s", detail.Name)
	}

	// Check releases were parsed from manifest
	if len(detail.Releases) != 2 {
		t.Errorf("Expected 2 releases, got %d", len(detail.Releases))
	}

	if len(detail.Releases) > 0 {
		if !strings.HasPrefix(detail.Releases[0], "redis/") {
			t.Errorf("Expected release to start with redis/, got %s", detail.Releases[0])
		}
	}

	// Check stemcells were parsed (now include alias prefix if present)
	if len(detail.Stemcells) != 1 {
		t.Errorf("Expected 1 stemcell, got %d", len(detail.Stemcells))
	}

	if len(detail.Stemcells) > 0 {
		if !strings.HasPrefix(detail.Stemcells[0], "default/ubuntu-xenial/") {
			t.Errorf("Expected stemcell to start with default/ubuntu-xenial/, got %s", detail.Stemcells[0])
		}
	}

	// Check VMs (Name now maps to VM ID)
	if len(detail.VMs) != 2 {
		t.Errorf("Expected 2 VMs, got %d", len(detail.VMs))
	}

	if len(detail.VMs) > 0 {
		if detail.VMs[0].Name != "vm-1" {
			t.Errorf("Expected VM Name vm-1, got %s", detail.VMs[0].Name)
		}
	}

	// Check properties were extracted
	if detail.Properties["blacksmith"] == nil {
		t.Error("Expected blacksmith properties to be extracted")
	}
}

func TestBOSHScanner_GetDeploymentDetails_Cache(t *testing.T) {
	t.Parallel()

	director := &mockDirector{
		details: map[string]*bosh.DeploymentDetail{
			"cached-deployment": {
				Name:     "cached-deployment",
				Manifest: "name: cached-deployment",
			},
		},
	}

	logger := NewMockLogger()
	scanner := NewBOSHScanner(director, logger)

	ctx := context.Background()

	// First call should fetch from director
	detail1, err := scanner.GetDeploymentDetails(ctx, "cached-deployment")
	if err != nil {
		t.Fatalf("Failed to get deployment details: %v", err)
	}

	// Modify director response
	director.details["cached-deployment"].Manifest = "name: modified"

	// Second call should use cache
	detail2, err := scanner.GetDeploymentDetails(ctx, "cached-deployment")
	if err != nil {
		t.Fatalf("Failed to get cached deployment details: %v", err)
	}

	// Should get same (cached) result
	if detail1.Manifest != detail2.Manifest {
		t.Error("Expected cached result, got different manifest")
	}

	if detail2.Manifest == "name: modified" {
		t.Error("Got modified manifest, cache not working")
	}
}

func TestBOSHScanner_ParseManifestProperties(t *testing.T) {
	t.Parallel()

	scanner := &BoshScanner{}

	manifest := `
name: test-deployment
properties:
  redis:
    password: secret
  blacksmith:
    service_id: redis-service
    plan_id: small
meta:
  environment: production
networks:
  - name: default
    type: manual
instance_groups:
  - name: redis
    instances: 3
`

	props, err := scanner.ParseManifestProperties(manifest)
	if err != nil {
		t.Fatalf("Failed to parse manifest: %v", err)
	}

	// Check properties were extracted
	if props["properties"] == nil {
		t.Error("Expected properties to be extracted")
	}

	if props["meta"] == nil {
		t.Error("Expected meta to be extracted")
	}

	if props["networks"] == nil {
		t.Error("Expected networks to be extracted")
	}

	if props["instance_groups"] == nil {
		t.Error("Expected instance_groups to be extracted")
	}

	if props["blacksmith"] == nil {
		t.Error("Expected blacksmith metadata to be extracted")
	}

	// Check blacksmith metadata
	blacksmith, ok := props["blacksmith"].(map[string]interface{})
	if !ok {
		t.Fatal("Blacksmith metadata not a map")
	}

	if blacksmith["service_id"] != "redis-service" {
		t.Errorf("Expected service_id redis-service, got %v", blacksmith["service_id"])
	}
}

func TestDeploymentCache(t *testing.T) {
	t.Parallel()

	cache := &DeploymentCache{
		Data:   make(map[string]*DeploymentDetail),
		Expiry: make(map[string]time.Time),
		TTL:    100 * time.Millisecond,
	}

	detail := &DeploymentDetail{
		DeploymentInfo: DeploymentInfo{
			Name: "test-deployment",
		},
	}

	// Set item in cache
	cache.Set("test-deployment", detail)

	// Should be able to retrieve it
	cached := cache.Get("test-deployment")
	if cached == nil {
		t.Fatal("Expected to get cached item")
	}

	if cached.Name != "test-deployment" {
		t.Errorf("Expected name test-deployment, got %s", cached.Name)
	}

	// Wait for expiry
	time.Sleep(150 * time.Millisecond)

	// Should be expired now
	cached = cache.Get("test-deployment")
	if cached != nil {
		t.Error("Expected cached item to be expired")
	}
}

func TestDeploymentCache_Cleanup(t *testing.T) {
	t.Parallel()

	cache := &DeploymentCache{
		Data:   make(map[string]*DeploymentDetail),
		Expiry: make(map[string]time.Time),
		TTL:    50 * time.Millisecond,
	}

	// Add multiple items
	for i := range 5 {
		name := fmt.Sprintf("deployment-%d", i)
		cache.Data[name] = &DeploymentDetail{
			DeploymentInfo: DeploymentInfo{Name: name},
		}
		cache.Expiry[name] = time.Now().Add(cache.TTL)
	}

	// Expire some items
	cache.Expiry["deployment-0"] = time.Now().Add(-1 * time.Second)
	cache.Expiry["deployment-1"] = time.Now().Add(-1 * time.Second)

	// Run cleanup
	cache.Cleanup()

	// Should have removed expired items
	if len(cache.Data) != 3 {
		t.Errorf("Expected 3 items after cleanup, got %d", len(cache.Data))
	}

	if _, ok := cache.Data["deployment-0"]; ok {
		t.Error("deployment-0 should have been cleaned up")
	}

	if _, ok := cache.Data["deployment-1"]; ok {
		t.Error("deployment-1 should have been cleaned up")
	}
}
