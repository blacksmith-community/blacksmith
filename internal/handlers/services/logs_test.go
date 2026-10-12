package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/handlers/services"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

const (
	logsInstanceID = "instance-1"
	logsDeployment = "valkey-instance-1"
	logsServiceID  = "valkey"
	logsPlanID     = "standalone"
	mebibyte       = 1024 * 1024

	// The instances logsVMs lists, as <job>/<instance ID>, and the one file
	// the scripted fetches return for each.
	logsNodeA   = "node/id-node-a"
	logsNodeB   = "node/id-node-b"
	logsProxy   = "proxy/id-proxy-1"
	logsNodeLog = "node/node.log"

	// The job and instance IDs of the two node instances.
	logsNodeJob = "node"
	logsNodeAID = "id-node-a"
	logsNodeBID = "id-node-b"
)

// logsVault answers the two reads the logs endpoint makes. Every other
// method panics on the nil embedded Vault, so a test sees a call it did not
// script.
type logsVault struct {
	interfaces.Vault

	values map[string]interface{}
}

func (v *logsVault) Get(_ context.Context, path string, result interface{}) (bool, error) {
	value, found := v.values[path]
	if !found {
		return false, nil
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return false, err //nolint:wrapcheck // a test fixture
	}

	return true, json.Unmarshal(encoded, result) //nolint:wrapcheck // a test fixture
}

// newLogsVault holds the index entry of logsInstanceID and, when manifest
// is not empty, its manifest.
func newLogsVault(manifest string) *logsVault {
	vault := &logsVault{values: map[string]interface{}{
		logsInstanceID: map[string]interface{}{
			"service_id": logsServiceID, "plan_id": logsPlanID, "deployment_name": logsDeployment,
		},
	}}

	if manifest != "" {
		vault.values[logsInstanceID+"/manifest"] = map[string]interface{}{"manifest": manifest}
	}

	return vault
}

// logsVMs are the VMs the director lists for the deployment. Their indexes
// and IDs are the ones a deployment that was scaled and recreated has.
func logsVMs() []bosh.VM {
	return []bosh.VM{
		{Job: "proxy", ID: "id-proxy-1", Index: 1},
		{Job: "node", ID: "id-node-b", Index: 2},
		{Job: "node", ID: "id-node-a", Index: 0},
	}
}

// requestLogs asks the handler for the logs of logsInstanceID.
func requestLogs(t *testing.T, handler *services.Handler) map[string]interface{} {
	t.Helper()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/"+logsInstanceID+"/logs", nil)

	handler.GetInstanceLogs(recorder, request, logsInstanceID)

	var body map[string]interface{}

	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("response body is not JSON: %v: %q", err, recorder.Body.String())
	}

	return body
}

// budgetRecorder is a scripted director's log fetch that records the
// budget each call received and takes a share of it, as a fetch that keeps
// files does.
type budgetRecorder struct {
	mu      sync.Mutex
	seen    []*bosh.LogBudget
	before  []int64
	take    int64
	callers []string
}

func (r *budgetRecorder) fetch(_ context.Context, _, job, instanceID string, budget *bosh.LogBudget) (*bosh.LogBundle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seen = append(r.seen, budget)
	r.callers = append(r.callers, job+"/"+instanceID)

	if budget == nil {
		r.before = append(r.before, -1)
	} else {
		r.before = append(r.before, budget.Remaining)
		budget.Remaining -= r.take
	}

	return &bosh.LogBundle{Files: map[string]string{"node/node.log": "from " + job + "/" + instanceID}}, nil
}

// One request fetches the logs of every instance, so the instances share one
// budget. A cap on each call would let five instances hold five times the
// limit. A second request starts with a full budget of its own.
func TestFetchLogsSharesOneBudgetAcrossInstances(t *testing.T) {
	t.Parallel()

	recorder := &budgetRecorder{take: 20 * mebibyte}

	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) { return logsVMs(), nil }
	director.FetchLogsFn = recorder.fetch

	handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: newLogsVault(""), Director: director})

	body := requestLogs(t, handler)
	if body["error"] != nil {
		t.Fatalf("the logs response holds an error: %v", body["error"])
	}

	if len(recorder.seen) != 3 {
		t.Fatalf("the director was asked for %d instances' logs, want 3: %v", len(recorder.seen), recorder.callers)
	}

	for index, budget := range recorder.seen {
		if budget == nil {
			t.Fatalf("call %d got no budget, so its instance had no limit but its own file limit", index)
		}

		if budget != recorder.seen[0] {
			t.Errorf("call %d got a budget of its own, want the one budget of the request", index)
		}
	}

	if want := []int64{64 * mebibyte, 44 * mebibyte, 24 * mebibyte}; recorder.before[0] != want[0] || recorder.before[1] != want[1] || recorder.before[2] != want[2] {
		t.Errorf("the calls saw budgets of %v bytes, want the request's 64 MiB less what each earlier call took: %v", recorder.before, want)
	}

	logs, ok := body["logs"].(map[string]interface{})
	if !ok || len(logs) != 3 {
		t.Fatalf("logs = %v, want one entry for each of 3 instances", body["logs"])
	}

	// A later request does not inherit what this one used.
	_ = requestLogs(t, handler)

	if len(recorder.seen) != 6 || recorder.seen[3] == recorder.seen[0] || recorder.before[3] != 64*mebibyte {
		t.Errorf("a second request must start with its own full budget, saw %v bytes before its first call", recorder.before[3:])
	}
}

// The logs endpoint lists the deployment's instances with the director and
// fetches logs for each one by its instance ID. A manifest that still says
// 5 instances after a scale-down or a recreate asks for no index that does
// not exist, and the response keys are <job>/<id>.
func TestInstanceLogsFetchesEachInstanceByID(t *testing.T) {
	t.Parallel()

	manifest := "name: " + logsDeployment + "\ninstance_groups:\n- name: node\n  instances: 5\n"
	recorder := &budgetRecorder{}

	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) { return logsVMs(), nil }
	director.FetchLogsFn = recorder.fetch

	handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: newLogsVault(manifest), Director: director})

	body := requestLogs(t, handler)
	if body["error"] != nil {
		t.Fatalf("the logs response holds an error: %v", body["error"])
	}

	if body["deployment"] != logsDeployment {
		t.Errorf("deployment = %v, want %s", body["deployment"], logsDeployment)
	}

	if got := director.Calls("GetDeploymentVMs"); len(got) != 1 || got[0] != logsDeployment {
		t.Errorf("the director listed VMs %v, want one listing of %s", got, logsDeployment)
	}

	// The instances are fetched in a fixed order, by job and then index.
	want := []string{"node/id-node-a", "node/id-node-b", "proxy/id-proxy-1"}

	if got := director.Calls("FetchLogs"); !slices.Equal(got, want) {
		t.Fatalf("logs were fetched for %v, want %v", got, want)
	}

	logs, ok := body["logs"].(map[string]interface{})
	if !ok || len(logs) != len(want) {
		t.Fatalf("logs = %v, want one entry for each of %v", body["logs"], want)
	}

	for _, key := range want {
		files, ok := logs[key].(map[string]interface{})
		if !ok || files["node/node.log"] != "from "+key {
			t.Errorf("logs[%q] = %v, want the files fetched for it", key, logs[key])
		}
	}
}

// active and inactive are the values a VM row's Active points to.
func active() *bool {
	value := true

	return &value
}

func inactive() *bool {
	value := false

	return &value
}

// A deploy that creates a VM before it deletes the old one leaves an
// inactive row beside the active one under the same instance ID. The
// inactive row is not fetched, a repeated row is fetched once, and an
// instance the director lists only with an inactive VM says why it has no
// logs.
func TestInstanceLogsFetchEachActiveInstanceOnce(t *testing.T) {
	t.Parallel()

	recorder := &budgetRecorder{}

	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) {
		return []bosh.VM{
			{Job: logsNodeJob, ID: logsNodeAID, Index: 0, Active: inactive()},
			{Job: logsNodeJob, ID: logsNodeAID, Index: 0, Active: active()},
			{Job: logsNodeJob, ID: logsNodeBID, Index: 2},
			{Job: logsNodeJob, ID: logsNodeBID, Index: 2},
			{Job: "proxy", ID: "id-proxy-1", Index: 1, Active: inactive()},
		}, nil
	}
	director.FetchLogsFn = recorder.fetch

	handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: newLogsVault(""), Director: director})

	body := requestLogs(t, handler)

	if got, want := director.Calls("FetchLogs"), []string{logsNodeA, logsNodeB}; !slices.Equal(got, want) {
		t.Errorf("logs were fetched for %v, want each active instance once: %v", got, want)
	}

	logs, _ := body["logs"].(map[string]interface{})

	for _, key := range []string{logsNodeA, logsNodeB} {
		if files, _ := logs[key].(map[string]interface{}); files[logsNodeLog] != "from "+key {
			t.Errorf("logs[%q] = %v, want the files fetched for it", key, logs[key])
		}
	}

	entry, _ := logs[logsProxy].(map[string]interface{})
	if message, _ := entry["error"].(string); !strings.Contains(message, "only an inactive VM") {
		t.Errorf("logs[%q] = %v, want an error that says the director lists only an inactive VM", logsProxy, logs[logsProxy])
	}
}

// Logs are fetched by job name and instance ID, so a row that lacks either
// is not fetched, and the response says so under the row's key.
func TestInstanceLogsReportRowsWithNoJobOrID(t *testing.T) {
	t.Parallel()

	recorder := &budgetRecorder{}

	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) {
		return []bosh.VM{
			{Job: logsNodeJob, ID: "", Index: 3},
			{Job: "", ID: "id-orphan", Index: 0},
			{Job: logsNodeJob, ID: logsNodeAID, Index: 0},
		}, nil
	}
	director.FetchLogsFn = recorder.fetch

	handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: newLogsVault(""), Director: director})

	body := requestLogs(t, handler)

	if got, want := director.Calls("FetchLogs"), []string{logsNodeA}; !slices.Equal(got, want) {
		t.Errorf("logs were fetched for %v, want only %v", got, want)
	}

	logs, _ := body["logs"].(map[string]interface{})
	if len(logs) != 3 {
		t.Errorf("logs = %v, want an entry for each of the 3 rows", logs)
	}

	for _, key := range []string{"node/", "/id-orphan"} {
		entry, _ := logs[key].(map[string]interface{})
		if message, _ := entry["error"].(string); !strings.Contains(message, "without a job name or an instance ID") {
			t.Errorf("logs[%q] = %v, want an error that says the row has no job name or instance ID", key, logs[key])
		}
	}
}

// A listing the director fails answers with an error, and fetches nothing.
func TestInstanceLogsReportAFailedInstanceListing(t *testing.T) {
	t.Parallel()

	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentVMsFn = func(string) ([]bosh.VM, error) { return nil, errListingFailed }
	director.FetchLogsFn = (&budgetRecorder{}).fetch

	handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: newLogsVault(""), Director: director})

	body := requestLogs(t, handler)

	message, _ := body["error"].(string)
	if !strings.Contains(message, errListingFailed.Error()) {
		t.Errorf("error = %v, want one that carries %q", body["error"], errListingFailed)
	}

	if got := director.Calls("FetchLogs"); len(got) != 0 {
		t.Errorf("logs were fetched for %v after the listing failed", got)
	}
}

var errListingFailed = errors.New("the director could not list the instances")
