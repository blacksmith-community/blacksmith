package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/apierr"
	"blacksmith/internal/handlers/services"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
	vaultPkg "blacksmith/pkg/vault"
)

const (
	gatePlan    = "tiny"
	goneGateID  = "gone"
	trustedPath = "/certificates/trusted"
)

var errGateIndexDown = errors.New("vault is down")

// downIndexVault fails every read.
type downIndexVault struct{ *testutil.InstanceVault }

func (downIndexVault) Get(context.Context, string, interface{}) (bool, error) {
	return false, errGateIndexDown
}

func gateVault() *testutil.InstanceVault {
	return &testutil.InstanceVault{Instances: map[string]vaultPkg.Instance{
		"live":     {ServiceID: logsServiceID, PlanID: gatePlan},
		goneGateID: {Status: vaultPkg.StatusDeleted, ServiceID: logsServiceID, PlanID: gatePlan},
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
		for _, id := range []string{goneGateID, "unknown", "a..b", ".."} {
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

// The trusted certificate routes write under the instance's own Vault path
// on POST and DELETE, so they answer only for a live instance. A dead ID, an
// unknown one, a path trick, and the UI's internal name, which the router
// sends to the broker's own certificate handler and never here, all answer
// 404 and write nothing.
func TestTrustedCertificateRoutesNeedALiveInstance(t *testing.T) {
	t.Parallel()

	routes := []struct{ method, suffix string }{
		{http.MethodGet, trustedPath},
		{http.MethodPost, trustedPath},
		{http.MethodDelete, trustedPath},
		{http.MethodPost, trustedPath + "/file"},
	}

	for _, route := range routes {
		for _, badID := range []string{goneGateID, "unknown", "a..b", "..", "internal"} {
			t.Run(route.method+route.suffix+"/"+badID, func(t *testing.T) {
				t.Parallel()

				vault := gateVault()
				handler := services.NewHandler(services.Dependencies{Logger: logger.Get().Named("test"), Vault: vault})
				recorder := httptest.NewRecorder()

				handler.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), route.method, "/b/"+badID+route.suffix, strings.NewReader("{}")))

				if recorder.Code != http.StatusNotFound || gateCode(t, recorder) != apierr.CodeInstanceNotFound {
					t.Fatalf("status %d code %q, want 404 instance_not_found: %s", recorder.Code, gateCode(t, recorder), recorder.Body.String())
				}
			})
		}
	}
}

// A live instance still reaches its trusted certificate handlers.
func TestTrustedCertificateRoutesServeALiveInstance(t *testing.T) {
	t.Parallel()

	recorder := serveGate(t, gateVault(), "/b/live"+trustedPath)
	if code := gateCode(t, recorder); code == apierr.CodeInstanceNotFound || code == apierr.CodeVaultUnavailable {
		t.Fatalf("a live instance was refused: %d %s", recorder.Code, recorder.Body.String())
	}
}
