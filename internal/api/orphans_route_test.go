package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/api"
)

// /b/orphans reaches the orphan listing and /b/cleanup the stub, and neither
// is taken for an instance id.
func TestOrphanRoutes(t *testing.T) {
	t.Parallel()

	apiHandler := api.NewInternalAPI(createTestDependencies(t))
	t.Cleanup(apiHandler.UpgradeHandler().Stop)

	serve := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
		recorder := httptest.NewRecorder()
		apiHandler.ServeHTTP(recorder, req)

		return recorder
	}

	recorder := serve(http.MethodGet, "/b/orphans")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"orphans"`) {
		t.Errorf("GET /b/orphans answered %d %s, want 200 with an orphans list", recorder.Code, recorder.Body.String())
	}

	recorder = serve(http.MethodPost, "/b/orphans")
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /b/orphans answered %d, want 405", recorder.Code)
	}

	recorder = serve(http.MethodPost, "/b/cleanup")
	if recorder.Code != http.StatusNotImplemented {
		t.Errorf("POST /b/cleanup answered %d %s, want 501", recorder.Code, recorder.Body.String())
	}
}
