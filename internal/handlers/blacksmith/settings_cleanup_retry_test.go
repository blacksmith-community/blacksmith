package blacksmith_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"blacksmith/internal/audit"
	"blacksmith/internal/config"
	"blacksmith/internal/settings"
)

const (
	keyCleanupRetry = "cleanup_retry_interval"
	savedTwenty     = "20m0s"
)

// Saving cleanup_retry_interval keeps it in the Vault settings beside the
// other saved settings, answers with the saved interval, records the
// change, and makes the settings answer report it from Vault.
func TestUpdateSettingsSavesTheCleanupRetryInterval(t *testing.T) {
	t.Setenv(config.CleanupRetryIntervalEnvKey, "")

	vault := &settingsRecordVault{record: map[string]interface{}{keyErrandTimeout: savedFortyFive}}
	handler, store := errandSettingsHandler(vault)

	recorder := httptest.NewRecorder()
	handler.UpdateSettings(recorder, actorRequest(t, http.MethodPut, "/b/blacksmith/settings", `{"cleanup_retry_interval": "20m"}`))
	store.Wait()

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}

	if vault.record[keyCleanupRetry] != savedTwenty || vault.record[keyErrandTimeout] != savedFortyFive {
		t.Errorf("saved record = %v, want the new interval and the old errand timeout", vault.record)
	}

	records, err := store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil || len(records) != 1 || records[0].Key != keyCleanupRetry || records[0].Old != "15m0s" || records[0].New != savedTwenty {
		t.Errorf("records = %+v (%v), want one cleanup_retry_interval change from 15m0s to 20m0s", records, err)
	}

	recorder = httptest.NewRecorder()
	handler.GetSettings(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/blacksmith/settings", nil))

	var body map[string]interface{}

	_ = json.Unmarshal(recorder.Body.Bytes(), &body)

	if body[keyCleanupRetry] != savedTwenty || body[keyCleanupRetry+"_source"] != settings.SourceVault {
		t.Errorf("answer = %v, want 20m0s from the Vault settings", body)
	}
}

// A value under a minute or over six hours, a bare number, or anything
// that is not a duration is refused with a 400, and nothing is saved.
func TestUpdateSettingsRefusesABadCleanupRetryInterval(t *testing.T) {
	t.Setenv(config.CleanupRetryIntervalEnvKey, "")

	for _, bad := range []string{"59s", "0s", "-5m", "600", "eventually", "", "6h1m", "24h"} {
		vault := &settingsRecordVault{}
		handler, _ := errandSettingsHandler(vault)

		recorder := httptest.NewRecorder()
		handler.UpdateSettings(recorder, actorRequest(t, http.MethodPut, "/b/blacksmith/settings", `{"cleanup_retry_interval": "`+bad+`"}`))

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400: %s", bad, recorder.Code, recorder.Body.String())
		}

		if vault.record != nil {
			t.Errorf("%q: the refused value was saved: %v", bad, vault.record)
		}
	}
}

// With nothing saved, the settings answer reports the interval from the
// environment variable.
func TestGetSettingsReportsTheCleanupRetryIntervalFromTheEnvironment(t *testing.T) {
	t.Setenv(config.CleanupRetryIntervalEnvKey, "40m")

	handler, _ := errandSettingsHandler(&settingsRecordVault{})

	recorder := httptest.NewRecorder()
	handler.GetSettings(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/blacksmith/settings", nil))

	var body map[string]interface{}

	_ = json.Unmarshal(recorder.Body.Bytes(), &body)

	if body[keyCleanupRetry] != "40m0s" || body[keyCleanupRetry+"_source"] != config.CleanupRetryIntervalEnvKey {
		t.Errorf("answer = %v, want 40m0s from %s", body, config.CleanupRetryIntervalEnvKey)
	}
}
