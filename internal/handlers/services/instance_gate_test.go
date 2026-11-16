package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"blacksmith/internal/apierr"
	"blacksmith/internal/handlers/services"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
)

const gatePlan = "tiny"

var errGateIndexDown = errors.New("vault is down")

// downIndexVault fails every read.
type downIndexVault struct{ *testutil.InstanceVault }

func (downIndexVault) Get(context.Context, string, interface{}) (bool, error) {
	return false, errGateIndexDown
}

func gateVault() *testutil.InstanceVault {
	return &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
		"live": {ServiceID: logsServiceID, PlanID: gatePlan},
		"gone": {Status: vaultPkg.StatusDeleted, ServiceID: logsServiceID, PlanID: gatePlan},
	}}
}

func serveGate(t *testing.T, vault interfaces.Vault, path string) *httptest.ResponseRecorder {
	t.Helper()

	handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: vault})
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))

	return recorder
}

func gateCode(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Code string `json:"code"`
	}

	_ = json.Unmarshal(recorder.Body.Bytes(), &body)

	return body.Code
}

// A read route answers only for an instance the index holds live, so a
// deprovisioned or unknown ID never reaches the instance's own Vault paths.
func TestInstanceReadRoutesNeedALiveInstance(t *testing.T) {
	t.Parallel()

	operations := []string{"vms", "logs", "events", "manifest", "details", "task/log", "task/debug"}

	for _, operation := range operations {
		for _, id := range []string{"gone", "unknown", "a..b", ".."} {
			t.Run(operation+"/"+id, func(t *testing.T) {
				t.Parallel()

				recorder := serveGate(t, gateVault(), "/b/"+id+"/"+operation)
				if recorder.Code != http.StatusNotFound || gateCode(t, recorder) != apierr.CodeInstanceNotFound {
					t.Fatalf("status %d code %q, want 404 instance_not_found: %s", recorder.Code, gateCode(t, recorder), recorder.Body.String())
				}
			})
		}
	}
}

func TestInstanceReadRoutesRefuseWhenTheIndexIsUnreadable(t *testing.T) {
	t.Parallel()

	for name, vault := range map[string]interfaces.Vault{"broken": downIndexVault{gateVault()}, "none": nil} {
		recorder := serveGate(t, vault, "/b/live/vms")
		if recorder.Code != http.StatusServiceUnavailable || gateCode(t, recorder) != apierr.CodeVaultUnavailable {
			t.Errorf("%s: status %d code %q, want 503 vault_unavailable", name, recorder.Code, gateCode(t, recorder))
		}
	}
}

func TestInstanceReadRoutesServeALiveInstance(t *testing.T) {
	t.Parallel()

	recorder := serveGate(t, gateVault(), "/b/live/vms")
	if recorder.Code == http.StatusNotFound || recorder.Code == http.StatusServiceUnavailable {
		t.Fatalf("a live instance was refused: %d %s", recorder.Code, recorder.Body.String())
	}
}
