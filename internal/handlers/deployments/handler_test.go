package deployments_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	errTestDirectorUnreachable          = errors.New("director unreachable")
)

type testVaultAdapter struct {
	client *vault.Client
	server *testutil.VaultDevServer
}

func (v *testVaultAdapter) Get(ctx context.Context, path string, result interface{}) (bool, error) {
	data, exists, err := v.client.GetSecret(ctx, path)
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

	err := v.client.PutSecret(ctx, path, dataMap)
	if err != nil {
		return fmt.Errorf("failed to put secret to vault path %q: %w", path, err)
	}

	return nil
}

func (v *testVaultAdapter) Delete(ctx context.Context, path string) error {
	err := v.client.DeleteSecret(ctx, path)
	if err != nil {
		return fmt.Errorf("failed to delete secret from vault path %q: %w", path, err)
	}

	return nil
}

func (v *testVaultAdapter) FindInstance(ctx context.Context, instanceID string) (*vault.Instance, bool, error) {
	indexData, exists, err := v.client.GetSecret(ctx, "db")
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

func (v *testVaultAdapter) SetKVMaxVersions(context.Context, int) error {
	return nil
}

func (v *testVaultAdapter) GetKVMaxVersions(context.Context) (int, error) {
	return 0, nil
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

func (f *fakeDirector) UpdateDeployment(_ context.Context, name, manifest string) (*bosh.Task, error) {
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

func (f *fakeDirector) FindRunningTaskForDeployment(_ context.Context, deploymentName string) (*bosh.Task, error) {
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

// errandDirector runs every errand with the result it holds.
type errandDirector struct {
	*testutil.MockBOSHDirector

	result *bosh.ErrandResult
	err    error
}

func (d *errandDirector) RunErrand(context.Context, string, string, bosh.ErrandOpts) (*bosh.ErrandResult, error) {
	return d.result, d.err
}

// runErrandThroughHandler posts an errand run to the handler and returns
// the decoded answer.
func runErrandThroughHandler(t *testing.T, result *bosh.ErrandResult) map[string]interface{} {
	t.Helper()

	director := &errandDirector{MockBOSHDirector: testutil.NewMockBOSHDirector(), result: result}
	handler := deployments.NewHandler(deployments.Dependencies{Logger: logger.NewNoOpLogger(), Director: director})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/b/deployments/valkey-1/errands/smoke-tests?when_changed=true", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var body map[string]interface{}

	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("decoding the answer %q: %v", recorder.Body.String(), err)
	}

	return body
}

// The handler blames the caller with 400 for an invalid errand instance
// and keeps 500 for every other failure, both with a JSON error body.
func TestErrandsHandlerAnswersForRunFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		code int
	}{
		{"invalid instance", fmt.Errorf("instance /abc: %w", bosh.ErrInvalidErrandInstance), http.StatusBadRequest},
		{"other failure", errors.New("director unreachable"), http.StatusInternalServerError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			director := &errandDirector{MockBOSHDirector: testutil.NewMockBOSHDirector(), err: test.err}
			handler := deployments.NewHandler(deployments.Dependencies{Logger: logger.NewNoOpLogger(), Director: director})

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/b/deployments/valkey-1/errands/smoke-tests", nil)
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			if recorder.Code != test.code {
				t.Fatalf("expected status %d, got %d: %s", test.code, recorder.Code, recorder.Body.String())
			}

			if !strings.Contains(recorder.Body.String(), test.err.Error()) {
				t.Errorf("expected the body to carry %q, got %s", test.err.Error(), recorder.Body.String())
			}
		})
	}
}

// A when_changed run the director skipped succeeded, so the handler
// answers 200 and says the run was skipped.
func TestErrandsHandlerReportsASkippedRun(t *testing.T) {
	t.Parallel()

	body := runErrandThroughHandler(t, &bosh.ErrandResult{Skipped: true})

	if skipped, ok := body["skipped"].(bool); !ok || !skipped {
		t.Errorf("expected skipped to be true, got %v", body)
	}

	if code, ok := body["exit_code"].(float64); !ok || code != 0 {
		t.Errorf("expected exit code 0, got %v", body)
	}

	// A skipped run has an empty results array, never null or a missing key.
	if results, ok := body["results"].([]interface{}); !ok || len(results) != 0 {
		t.Errorf("expected an empty results array, got %#v", body["results"])
	}
}

// errandAnswer is one errand result in the errand handler's answer.
type errandAnswer struct {
	Instance   string `json:"instance"`
	InstanceID string `json:"instance_id"`
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
}

// The instance group and IDs the every-result handler test reports.
const (
	errandGroup     = "node"
	errandPassingID = "uuid-0"
	errandFailingID = "uuid-1"
)

// The handler keeps the worst result in its top-level fields and adds
// every instance's result in a results array.
func TestErrandsHandlerAddsEveryResult(t *testing.T) {
	t.Parallel()

	passing := errandAnswer{Instance: errandGroup, InstanceID: errandPassingID, ExitCode: 0, Stdout: "ok"}
	failing := errandAnswer{Instance: errandGroup, InstanceID: errandFailingID, ExitCode: 1, Stdout: "failed", Stderr: "boom"}

	asResult := func(answer errandAnswer) bosh.ErrandResult {
		return bosh.ErrandResult{
			InstanceGroup: answer.Instance, InstanceID: answer.InstanceID,
			ExitCode: answer.ExitCode, Stdout: answer.Stdout, Stderr: answer.Stderr,
		}
	}

	worst := asResult(failing)
	worst.Results = []bosh.ErrandResult{asResult(passing), asResult(failing)}

	encoded, err := json.Marshal(runErrandThroughHandler(t, &worst))
	if err != nil {
		t.Fatalf("encoding the answer: %v", err)
	}

	var answer struct {
		errandAnswer

		Results []errandAnswer `json:"results"`
	}

	var keys struct {
		Results []map[string]interface{} `json:"results"`
	}

	err = errors.Join(json.Unmarshal(encoded, &answer), json.Unmarshal(encoded, &keys))
	if err != nil {
		t.Fatalf("decoding the answer %s: %v", encoded, err)
	}

	if answer.errandAnswer != failing {
		t.Errorf("expected the top level to be the failing result %+v, got %+v", failing, answer.errandAnswer)
	}

	want := []errandAnswer{passing, failing}
	if len(answer.Results) != len(want) || len(keys.Results) != len(want) {
		t.Fatalf("expected %d results, got %s", len(want), encoded)
	}

	for index, entry := range answer.Results {
		if entry != want[index] || len(keys.Results[index]) != 5 {
			t.Errorf("result %d is %v, want %+v and no other keys", index, keys.Results[index], want[index])
		}
	}
}

// manifestDirector answers GetDeployment with the detail and error it
// holds, and records any deploy that reaches it.
type manifestDirector struct {
	*testutil.MockBOSHDirector

	detail *bosh.DeploymentDetail
	err    error

	deployCalled bool
}

func (d *manifestDirector) GetDeployment(context.Context, string) (*bosh.DeploymentDetail, error) {
	return d.detail, d.err
}

func (d *manifestDirector) UpdateDeployment(context.Context, string, string) (*bosh.Task, error) {
	d.deployCalled = true

	return &bosh.Task{ID: 1}, nil
}

// manifestDeployment is the deployment the manifest tests read and update.
const manifestDeployment = "valkey-1"

// manifestRoutes are the two routes that read a deployment's manifest.
func manifestRoutes() []string {
	return []string{"manifest", "manifest-details"}
}

// placeholderMarkers are strings from the stand-in manifest the routes
// once answered with when the director could not give the real one.
func placeholderMarkers() []string {
	return []string{
		"placeholder-director-uuid",
		"blacksmith_admin_password",
		"bosh-warden-boshlite",
		"instance_groups",
	}
}

// getManifestThroughHandler reads one manifest route for the test
// deployment and
// returns the answer.
func getManifestThroughHandler(t *testing.T, director *manifestDirector, route string) *httptest.ResponseRecorder {
	t.Helper()

	handler := deployments.NewHandler(deployments.Dependencies{Logger: logger.NewNoOpLogger(), Director: director})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/deployments/"+manifestDeployment+"/"+route, nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	return recorder
}

// assertManifestError checks an error answer from a manifest route. It
// carries the wanted status, the handler's JSON error shape, an error
// that names the deployment and the failure, and no stand-in manifest.
func assertManifestError(t *testing.T, recorder *httptest.ResponseRecorder, wantCode int, wantText string) {
	t.Helper()

	if recorder.Code != wantCode {
		t.Fatalf("expected status %d, got %d: %s", wantCode, recorder.Code, recorder.Body.String())
	}

	for _, marker := range placeholderMarkers() {
		if strings.Contains(recorder.Body.String(), marker) {
			t.Errorf("the answer carries the stand-in manifest's %q: %s", marker, recorder.Body.String())
		}
	}

	var body struct {
		Success *bool  `json:"success"`
		Error   string `json:"error"`
		Text    string `json:"text"`
	}

	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("decoding the answer %q: %v", recorder.Body.String(), err)
	}

	if body.Success == nil || *body.Success {
		t.Errorf("expected success to be false, got %s", recorder.Body.String())
	}

	if body.Text != "" {
		t.Errorf("expected no manifest text, got %q", body.Text)
	}

	for _, want := range []string{manifestDeployment, wantText} {
		if !strings.Contains(body.Error, want) {
			t.Errorf("expected the error to carry %q, got %q", want, body.Error)
		}
	}
}

// A deployment the director does not know gets 404 and an error, never a
// stand-in manifest that a caller could deploy over the real one.
func TestManifestRoutesAnswer404ForAnUnknownDeployment(t *testing.T) {
	t.Parallel()

	for _, route := range manifestRoutes() {
		t.Run(route, func(t *testing.T) {
			t.Parallel()

			director := &manifestDirector{
				MockBOSHDirector: testutil.NewMockBOSHDirector(),
				err:              fmt.Errorf("%w: %s: director code 70000", bosh.ErrDeploymentNotFound, manifestDeployment),
			}

			recorder := getManifestThroughHandler(t, director, route)

			assertManifestError(t, recorder, http.StatusNotFound, "deployment not found")
		})
	}
}

// Any other director failure gets the 500 the sibling routes answer with,
// and an error, never a stand-in manifest.
func TestManifestRoutesAnswer500WhenTheDirectorFails(t *testing.T) {
	t.Parallel()

	for _, route := range manifestRoutes() {
		t.Run(route, func(t *testing.T) {
			t.Parallel()

			director := &manifestDirector{
				MockBOSHDirector: testutil.NewMockBOSHDirector(),
				err:              errTestDirectorUnreachable,
			}

			recorder := getManifestThroughHandler(t, director, route)

			assertManifestError(t, recorder, http.StatusInternalServerError, errTestDirectorUnreachable.Error())
		})
	}
}

// A manifest the director returns comes back as its text and its parsed
// form.
func TestManifestRoutesReturnTheDirectorsManifest(t *testing.T) {
	t.Parallel()

	manifest := "name: " + manifestDeployment + "\ninstance_groups:\n- name: node\n  instances: 3\n"

	for _, route := range manifestRoutes() {
		t.Run(route, func(t *testing.T) {
			t.Parallel()

			director := &manifestDirector{
				MockBOSHDirector: testutil.NewMockBOSHDirector(),
				detail:           &bosh.DeploymentDetail{Name: manifestDeployment, Manifest: manifest},
			}

			recorder := getManifestThroughHandler(t, director, route)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d: %s", recorder.Code, recorder.Body.String())
			}

			var body struct {
				Text   string                 `json:"text"`
				Parsed map[string]interface{} `json:"parsed"`
			}

			err := json.Unmarshal(recorder.Body.Bytes(), &body)
			if err != nil {
				t.Fatalf("decoding the answer %q: %v", recorder.Body.String(), err)
			}

			if body.Text != manifest {
				t.Errorf("expected the director's manifest text %q, got %q", manifest, body.Text)
			}

			if body.Parsed["name"] != manifestDeployment {
				t.Errorf("expected the parsed manifest to name %s, got %v", manifestDeployment, body.Parsed)
			}

			groups, ok := body.Parsed["instance_groups"].([]interface{})
			if !ok || len(groups) != 1 {
				t.Errorf("expected one parsed instance group, got %v", body.Parsed["instance_groups"])
			}
		})
	}
}

// A deployment whose first deploy is still running has no manifest yet.
// The routes say so with empty text, never with a stand-in manifest.
func TestManifestRoutesReturnNoManifestForADeployInFlight(t *testing.T) {
	t.Parallel()

	for _, route := range manifestRoutes() {
		t.Run(route, func(t *testing.T) {
			t.Parallel()

			director := &manifestDirector{
				MockBOSHDirector: testutil.NewMockBOSHDirector(),
				detail:           &bosh.DeploymentDetail{Name: manifestDeployment},
			}

			recorder := getManifestThroughHandler(t, director, route)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d: %s", recorder.Code, recorder.Body.String())
			}

			for _, marker := range placeholderMarkers() {
				if strings.Contains(recorder.Body.String(), marker) {
					t.Errorf("the answer carries the stand-in manifest's %q: %s", marker, recorder.Body.String())
				}
			}

			var body struct {
				Text string `json:"text"`
			}

			err := json.Unmarshal(recorder.Body.Bytes(), &body)
			if err != nil {
				t.Fatalf("decoding the answer %q: %v", recorder.Body.String(), err)
			}

			if body.Text != "" {
				t.Errorf("expected empty manifest text, got %q", body.Text)
			}
		})
	}
}

// An update body that is not a mapping with at least one key is refused
// with 400 and an error that names the deployment, and no deploy reaches
// the director. Each of these bodies once became the manifest "{}" or an
// error that did not say which deployment it was for.
func TestUpdateManifestRefusesABodyThatIsNotAMapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
	}{
		{"empty", ""},
		{"whitespace", " \n\t "},
		{"null", "null"},
		{"empty mapping", "{}"},
		{"empty mapping with whitespace", " { } "},
		{"yaml null", "~"},
		{"string", `"name: valkey-1"`},
		{"number", "42"},
		{"boolean", "true"},
		{"empty list", "[]"},
		{"list of mappings", `[{"name": "valkey-1"}]`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			director := &manifestDirector{MockBOSHDirector: testutil.NewMockBOSHDirector()}
			handler := deployments.NewHandler(deployments.Dependencies{Logger: logger.NewNoOpLogger(), Director: director})

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/b/deployments/"+manifestDeployment+"/manifest", strings.NewReader(test.payload))
			recorder := httptest.NewRecorder()

			handler.ServeHTTP(recorder, req)

			if director.deployCalled {
				t.Errorf("expected no deploy to reach the director for body %q", test.payload)
			}

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("expected status 400 for body %q, got %d: %s", test.payload, recorder.Code, recorder.Body.String())
			}

			var body struct {
				Success *bool  `json:"success"`
				Error   string `json:"error"`
			}

			err := json.Unmarshal(recorder.Body.Bytes(), &body)
			if err != nil {
				t.Fatalf("decoding the answer %q: %v", recorder.Body.String(), err)
			}

			if body.Success == nil || *body.Success {
				t.Errorf("expected success to be false, got %s", recorder.Body.String())
			}

			if !strings.Contains(body.Error, manifestDeployment) {
				t.Errorf("expected the error to name %s, got %q", manifestDeployment, body.Error)
			}
		})
	}
}
