package services_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func newLogsVault(manifest string) *logsVault {
	return &logsVault{values: map[string]interface{}{
		logsInstanceID: map[string]interface{}{
			"service_id": logsServiceID, "plan_id": logsPlanID, "deployment_name": logsDeployment,
		},
		logsInstanceID + "/manifest": map[string]interface{}{"manifest": manifest},
	}}
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

	manifest := "name: " + logsDeployment + "\ninstance_groups:\n- name: node\n  instances: 3\n"
	recorder := &budgetRecorder{take: 20 * mebibyte}

	director := testutil.NewScriptedBOSHDirector()
	director.FetchLogsFn = recorder.fetch

	handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: newLogsVault(manifest), Director: director})

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
