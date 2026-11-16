package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/api"
)

// A broker with no VM monitor answers the documented not-configured body.
func TestVMMonitorRouteAnswersWhenNoMonitorIsConfigured(t *testing.T) {
	t.Parallel()

	router := newSSHGateAPI(t, api.Dependencies{Config: &mockConfig{}})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/bosh/vm-monitor", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "VM monitor not configured") {
		t.Fatalf("got %d %s, want 200 with the not-configured body", rec.Code, rec.Body.String())
	}
}
