package services_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/handlers/services"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

// historyVault answers the instance record and the metadata record that
// holds the instance's operation history.
const instanceID = "inst-1"

type historyVault struct {
	testutil.InstanceVault

	history []interface{}
}

func (v *historyVault) Get(_ context.Context, path string, out interface{}) (bool, error) {
	var record interface{}

	switch path {
	case instanceID:
		record = map[string]interface{}{"service_id": "valkey", "plan_id": "small", "deployment_name": "valkey-small-inst-1", "instance_name": "cache"}
	case instanceID + "/metadata":
		record = map[string]interface{}{"history": v.history}
	default:
		return false, nil
	}

	body, err := json.Marshal(record)
	if err != nil {
		return false, err //nolint:wrapcheck // a test double
	}

	return true, json.Unmarshal(body, out) //nolint:wrapcheck // a test double
}

func detailsHandler(history []interface{}, deployment *bosh.DeploymentDetail, deploymentErr error) *services.Handler {
	director := testutil.NewScriptedBOSHDirector()
	director.GetDeploymentFn = func(string) (*bosh.DeploymentDetail, error) { return deployment, deploymentErr }

	return services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Director: director, Vault: &historyVault{history: history}})
}

func sampleHistory() []interface{} {
	return []interface{}{
		map[string]interface{}{"timestamp": 1700000000, "action": "provision", "description": "Parsing service parameters", "state": "in_progress"},
		map[string]interface{}{"timestamp": 1700000060, "action": "provision", "description": "Deploying: <b>boom</b>", "state": "failed"},
	}
}

func TestInstanceDetailsIncludeTheOperationHistory(t *testing.T) {
	t.Parallel()

	handler := detailsHandler(sampleHistory(), &bosh.DeploymentDetail{Manifest: "name: x"}, nil)
	recorder := httptest.NewRecorder()

	handler.GetInstanceDetails(recorder, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/b/inst-1/details", nil), instanceID)

	history, _ := decodeJSON(t, recorder)["history"].([]interface{})
	if len(history) != 2 {
		t.Fatalf("history has %d entries, want 2: %s", len(history), recorder.Body.String())
	}

	last, _ := history[1].(map[string]interface{})
	if last["description"] != "Deploying: <b>boom</b>" || last["state"] != "failed" {
		t.Fatalf("the failure entry was altered: %v", last)
	}
}

// A provision that failed before it created a deployment still has history.
func TestInstanceDetailsKeepTheHistoryWhenTheDeploymentIsGone(t *testing.T) {
	t.Parallel()

	handler := detailsHandler(sampleHistory(), nil, bosh.ErrDeploymentNotFound)
	recorder := httptest.NewRecorder()

	handler.GetInstanceDetails(recorder, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/b/inst-1/details", nil), instanceID)

	body := decodeJSON(t, recorder)
	if body["deleted"] != true {
		t.Fatalf("the deleted answer changed: %s", recorder.Body.String())
	}

	if history, _ := body["history"].([]interface{}); len(history) != 2 {
		t.Fatalf("history missing from the deleted answer: %s", recorder.Body.String())
	}
}

func decodeJSON(t *testing.T, recorder *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()

	var body map[string]interface{}

	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("response body is not JSON: %v: %q", err, recorder.Body.String())
	}

	return body
}
