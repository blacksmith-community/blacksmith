package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/api"
	"blacksmith/internal/audit/audittest"
	"blacksmith/internal/interfaces"
)

// keepingVault keeps what is written to it, which the shared test adapter
// does not, and leaves every other call to the wrapped Vault.
type keepingVault struct {
	interfaces.Vault

	kv *audittest.KV
}

func (v keepingVault) Get(ctx context.Context, path string, out interface{}) (bool, error) {
	return v.kv.Get(ctx, path, out) //nolint:wrapcheck // the fake answers as Vault would
}

func (v keepingVault) Put(ctx context.Context, path string, data interface{}) error {
	return v.kv.Put(ctx, path, data) //nolint:wrapcheck // the fake answers as Vault would
}

// stillReconciler is a reconciler whose interval only a settings save moves.
type stillReconciler struct {
	interval time.Duration
}

func (r *stillReconciler) UpdateInterval(_ context.Context, interval time.Duration) error {
	r.interval = interval

	return nil
}

func (r *stillReconciler) GetInterval() time.Duration { return r.interval }

// A reconciler-interval save through the router leaves a settings record
// that GET /b/blacksmith/changes then lists.
func TestChangesRouteListsASettingsSave(t *testing.T) {
	t.Parallel()

	deps := createTestDependencies(t)
	deps.Vault = keepingVault{Vault: deps.Vault, kv: audittest.NewKV()}
	deps.Reconciler = &stillReconciler{interval: time.Hour}
	apiHandler := api.NewInternalAPI(deps)

	save := httptest.NewRecorder()
	apiHandler.ServeHTTP(save, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/b/blacksmith/settings", strings.NewReader(`{"reconciler_interval": "30m"}`)))
	validateResponse(t, save, http.StatusOK)

	read := httptest.NewRecorder()
	apiHandler.ServeHTTP(read, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/blacksmith/changes?scope=settings", nil))
	validateResponse(t, read, http.StatusOK)

	body := read.Body.String()
	if !strings.Contains(body, `"key":"reconciler_interval"`) || !strings.Contains(body, `"old":"1h0m0s"`) || !strings.Contains(body, `"new":"30m0s"`) {
		t.Fatalf("the changes route does not list the save: %s", body)
	}
}
