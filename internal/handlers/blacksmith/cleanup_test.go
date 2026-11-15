package blacksmith_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	handlerpkg "blacksmith/internal/handlers/blacksmith"
	"blacksmith/pkg/logger"
)

// Cleanup has no job behind it yet, so it must say so. It used to answer 200
// with status completed and zero counts, which read as a clean sweep.
func TestCleanupReportsNotImplemented(t *testing.T) {
	t.Parallel()

	handler := handlerpkg.NewHandler(handlerpkg.Dependencies{
		Logger: logger.Get().Named("test"),
		Config: createTestConfig(),
	})

	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/b/cleanup", strings.NewReader(`{"dry_run":false,"target":"all"}`))
	response := httptest.NewRecorder()

	handler.Cleanup(response, request)

	if response.Code != http.StatusNotImplemented {
		t.Fatalf("status %d, want %d: %s", response.Code, http.StatusNotImplemented, response.Body.String())
	}

	var body map[string]interface{}

	err := json.Unmarshal(response.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, response.Body.String())
	}

	if body["success"] != false {
		t.Errorf("success = %v, want false", body["success"])
	}

	message, _ := body["error"].(string)
	if !strings.Contains(message, "not implemented") {
		t.Errorf("error %q does not say the cleanup is not implemented", message)
	}

	if _, claimed := body["status"]; claimed {
		t.Errorf("body still carries a status field: %s", response.Body.String())
	}

	if !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
		t.Errorf("content type %q, want JSON", response.Header().Get("Content-Type"))
	}
}

// Only POST reaches the cleanup, as before.
func TestCleanupRejectsOtherMethods(t *testing.T) {
	t.Parallel()

	handler := handlerpkg.NewHandler(handlerpkg.Dependencies{
		Logger: logger.Get().Named("test"),
		Config: createTestConfig(),
	})

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/cleanup", nil)
	response := httptest.NewRecorder()

	handler.Cleanup(response, request)

	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
}
