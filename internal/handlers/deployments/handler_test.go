package deployments_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/handlers/deployments"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
	"blacksmith/pkg/vault"
	"gopkg.in/yaml.v3"
)

// Test error definitions.
var (
	errTestDataMustBeMapStringInterface = errors.New("data must be map[string]interface{}")
)

type testVaultAdapter struct {
	client *vault.Client
	server *testutil.VaultDevServer
}

func (v *testVaultAdapter) Get(ctx context.Context, path string, result interface{}) (bool, error) {
	data, exists, err := v.client.GetSecret(path)
	if err != nil {
		return false, fmt.Errorf("failed to get secret from vault path %q: %w", path, err)
	}

	if !exists {
		return false, nil
	}

	payload, err := json.Marshal(data)
	if err != nil {
		return false, fmt.Errorf("failed to marshal vault data: %w", err)
	}

	err = json.Unmarshal(payload, result)
	if err != nil {
		return false, fmt.Errorf("failed to unmarshal vault data: %w", err)
	}

	return true, nil
}

func (v *testVaultAdapter) Put(ctx context.Context, path string, data interface{}) error {
	dataMap, ok := data.(map[string]interface{})
	if !ok {
		return errTestDataMustBeMapStringInterface
	}

	err := v.client.PutSecret(path, dataMap)
	if err != nil {
		return fmt.Errorf("failed to put secret to vault path %q: %w", path, err)
	}

	return nil
}

func (v *testVaultAdapter) Delete(ctx context.Context, path string) error {
	err := v.client.DeleteSecret(path)
	if err != nil {
		return fmt.Errorf("failed to delete secret from vault path %q: %w", path, err)
	}

	return nil
}

func (v *testVaultAdapter) FindInstance(ctx context.Context, instanceID string) (*vault.Instance, bool, error) {
	indexData, exists, err := v.client.GetSecret("db")
	if err != nil {
		return nil, false, fmt.Errorf("failed to find instance: %w", err)
	}

	if !exists {
		return nil, false, nil
	}

	instData, exists := indexData[instanceID]
	if !exists {
		return nil, false, nil
	}

	instMap, ok := instData.(map[string]interface{})
	if !ok {
		return nil, false, nil
	}

	inst := &vault.Instance{}
	if serviceID, ok := instMap["service_id"].(string); ok {
		inst.ServiceID = serviceID
	}

	if planID, ok := instMap["plan_id"].(string); ok {
		inst.PlanID = planID
	}

	return inst, true, nil
}

func (v *testVaultAdapter) ListCFRegistrations(context.Context) ([]map[string]interface{}, error) {
	return []map[string]interface{}{}, nil
}

func (v *testVaultAdapter) SaveCFRegistration(context.Context, map[string]interface{}) error {
	return nil
}

func (v *testVaultAdapter) GetCFRegistration(context.Context, string, interface{}) (bool, error) {
	return false, nil
}

func (v *testVaultAdapter) DeleteCFRegistration(context.Context, string) error {
	return nil
}

func (v *testVaultAdapter) UpdateCFRegistrationStatus(context.Context, string, string, string) error {
	return nil
}

func (v *testVaultAdapter) SaveCFRegistrationProgress(context.Context, string, map[string]interface{}) error {
	return nil
}

// fakeDirector embeds the shared testutil.MockBOSHDirector to satisfy the Director interface.
type fakeDirector struct {
	*testutil.MockBOSHDirector

	updateDeploymentManifest string
	updateDeploymentCalled   bool
	updateDeploymentTask     *bosh.Task
	updateDeploymentErr      error
}

func newFakeDirector() *fakeDirector {
	return &fakeDirector{MockBOSHDirector: testutil.NewMockBOSHDirector()}
}

func (f *fakeDirector) UpdateDeployment(name, manifest string) (*bosh.Task, error) {
	f.updateDeploymentCalled = true
	f.updateDeploymentManifest = manifest

	if f.updateDeploymentErr != nil {
		return nil, f.updateDeploymentErr
	}

	if f.updateDeploymentTask != nil {
		return f.updateDeploymentTask, nil
	}

	return &bosh.Task{ID: 1, State: "done"}, nil
}

func (f *fakeDirector) FindRunningTaskForDeployment(deploymentName string) (*bosh.Task, error) {
	return nil, nil
}

func TestUpdateManifestCachesToVault(t *testing.T) {
	t.Parallel()

	vaultAdapter := setupVaultForManifestTest(t)
	director, instanceID, deploymentName := setupDirectorForManifestTest()
	seedVaultWithTestData(t, vaultAdapter.server, instanceID, deploymentName)
	handler := createHandlerForManifestTest(vaultAdapter, director)

	manifest := map[string]interface{}{"name": deploymentName}
	_, recorder := executeManifestUpdate(t, handler, deploymentName, manifest)

	verifyManifestUpdateResponse(t, recorder, director, manifest)
	verifyManifestCachedInVault(t, vaultAdapter, instanceID, director.updateDeploymentManifest)
}

func setupVaultForManifestTest(t *testing.T) *testVaultAdapter {
	t.Helper()

	vaultServer, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to create vault dev server: %v", err)
	}

	vaultClient, err := vault.NewClient(vaultServer.Addr, vaultServer.RootToken, true)
	if err != nil {
		t.Fatalf("failed to create vault client: %v", err)
	}

	return &testVaultAdapter{
		client: vaultClient,
		server: vaultServer,
	}
}

func setupDirectorForManifestTest() (*fakeDirector, string, string) {
	director := newFakeDirector()
	director.updateDeploymentTask = &bosh.Task{ID: 123, State: "done"}
	instanceID := "instance-123"
	deploymentName := "redis-plan-instance-123"

	return director, instanceID, deploymentName
}

func seedVaultWithTestData(t *testing.T, vaultServer *testutil.VaultDevServer, instanceID, deploymentName string) {
	t.Helper()

	err := vaultServer.WriteSecret("db", map[string]interface{}{instanceID: map[string]interface{}{}})
	if err != nil {
		t.Fatalf("failed to seed index: %v", err)
	}

	err = vaultServer.WriteSecret(instanceID, map[string]interface{}{"deployment_name": deploymentName})
	if err != nil {
		t.Fatalf("failed to seed instance root: %v", err)
	}
}

func createHandlerForManifestTest(vaultAdapter *testVaultAdapter, director *fakeDirector) *deployments.Handler {
	return deployments.NewHandler(deployments.Dependencies{
		Logger:   logger.Get(),
		Vault:    vaultAdapter,
		Director: director,
	})
}

func executeManifestUpdate(t *testing.T, handler *deployments.Handler, deploymentName string, manifest map[string]interface{}) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()

	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("failed to marshal manifest: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/b/deployments/"+deploymentName+"/manifest", bytes.NewReader(body))
	recorder := httptest.NewRecorder()

	handler.UpdateManifest(recorder, req, deploymentName)

	return req, recorder
}

func verifyManifestUpdateResponse(t *testing.T, recorder *httptest.ResponseRecorder, director *fakeDirector, manifest map[string]interface{}) {
	t.Helper()

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	if !director.updateDeploymentCalled {
		t.Fatalf("expected UpdateDeployment to be called")
	}

	yamlBytes, _ := yaml.Marshal(manifest)
	if director.updateDeploymentManifest != string(yamlBytes) {
		t.Fatalf("expected manifest passed to director to match YAML representation")
	}
}

func verifyManifestCachedInVault(t *testing.T, vaultAdapter *testVaultAdapter, instanceID, expectedManifest string) {
	t.Helper()

	var cached struct {
		Manifest string `json:"manifest"`
	}

	exists, err := vaultAdapter.Get(context.Background(), instanceID+"/manifest", &cached)
	if err != nil {
		t.Fatalf("failed to retrieve cached manifest: %v", err)
	}

	if !exists {
		t.Fatalf("expected manifest to be cached in vault")
	}

	if cached.Manifest != expectedManifest {
		t.Fatalf("expected cached manifest %q, got %q", expectedManifest, cached.Manifest)
	}
}

func TestUpdateManifestReturnsErrorWhenInstanceNotFound(t *testing.T) {
	t.Parallel()

	vaultServer, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("failed to create vault dev server: %v", err)
	}

	vaultClient, err := vault.NewClient(vaultServer.Addr, vaultServer.RootToken, true)
	if err != nil {
		t.Fatalf("failed to create vault client: %v", err)
	}

	vaultAdapter := &testVaultAdapter{
		client: vaultClient,
		server: vaultServer,
	}

	director := newFakeDirector()
	director.updateDeploymentTask = &bosh.Task{ID: 456, State: "done"}

	err = vaultServer.WriteSecret("db", map[string]interface{}{"other-instance": map[string]interface{}{}})
	if err != nil {
		t.Fatalf("failed to seed index: %v", err)
	}

	err = vaultServer.WriteSecret("other-instance", map[string]interface{}{"deployment_name": "different"})
	if err != nil {
		t.Fatalf("failed to seed mismatched instance: %v", err)
	}

	handler := deployments.NewHandler(deployments.Dependencies{
		Logger:   logger.Get(),
		Vault:    vaultAdapter,
		Director: director,
	})

	manifest := map[string]interface{}{"name": "example"}

	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("failed to marshal manifest: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/b/deployments/missing-deployment/manifest", bytes.NewReader(body))
	recorder := httptest.NewRecorder()

	handler.UpdateManifest(recorder, req, "missing-deployment")

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", recorder.Code)
	}

	if !director.updateDeploymentCalled {
		t.Fatalf("expected UpdateDeployment to run even when vault lookup fails")
	}
}
