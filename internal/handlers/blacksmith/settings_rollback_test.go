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
	keyUpdateRollback = "update_rollback"
	savedFiveMinutes  = "5m0s"
)

// getSettingsBody answers a GET of the settings and decodes the answer.
func getSettingsBody(t *testing.T, handler interface {
	GetSettings(writer http.ResponseWriter, request *http.Request)
},
) map[string]interface{} {
	t.Helper()

	recorder := httptest.NewRecorder()
	handler.GetSettings(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/blacksmith/settings", nil))

	var body map[string]interface{}

	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("the settings answer isn't JSON: %v", err)
	}

	return body
}

// update_rollback saves through the settings API into the Vault settings
// beside the other saved settings, which stay as they were. The settings
// answer shows the value in effect and where it came from, and the change
// is recorded.
func TestSettings_UpdateRollbackSavedAndShown(t *testing.T) {
	t.Setenv(config.UpdateRollbackEnvKey, "")
	t.Setenv(config.ErrandTimeoutEnvKey, "")

	vault := &settingsRecordVault{record: map[string]interface{}{keyInterval: savedFiveMinutes, keyErrandTimeout: savedFortyFive}}
	handler, store := errandSettingsHandler(vault)

	before := getSettingsBody(t, handler)
	if before[keyUpdateRollback] != false || before[keyUpdateRollback+"_source"] != settings.SourceDefault {
		t.Errorf("before a save the answer is %v, want update_rollback false from the default", before)
	}

	recorder := httptest.NewRecorder()
	handler.UpdateSettings(recorder, actorRequest(t, http.MethodPut, "/b/blacksmith/settings", `{"update_rollback": true}`))
	store.Wait()

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}

	if vault.record[keyUpdateRollback] != true || vault.record[keyInterval] != savedFiveMinutes || vault.record[keyErrandTimeout] != savedFortyFive {
		t.Errorf("saved record = %v, want the rollback on and the other settings as they were", vault.record)
	}

	after := getSettingsBody(t, handler)
	if after[keyUpdateRollback] != true || after[keyUpdateRollback+"_source"] != settings.SourceVault {
		t.Errorf("after a save the answer is %v, want update_rollback true from the Vault settings", after)
	}

	records, err := store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil || len(records) != 1 || records[0].Key != keyUpdateRollback || records[0].Old != "false" || records[0].New != "true" {
		t.Errorf("records = %+v (%v), want one update_rollback change from false to true", records, err)
	}
}

// A value that isn't a JSON boolean is refused with a 400, and nothing is
// saved.
func TestSettings_UpdateRollbackRefusesANonBoolean(t *testing.T) {
	t.Setenv(config.UpdateRollbackEnvKey, "")

	for _, bad := range []string{`"true"`, `1`, `"yes"`, `null`} {
		vault := &settingsRecordVault{}
		handler, _ := errandSettingsHandler(vault)

		recorder := httptest.NewRecorder()
		handler.UpdateSettings(recorder, actorRequest(t, http.MethodPut, "/b/blacksmith/settings", `{"update_rollback": `+bad+`}`))

		if bad == `null` {
			// A null leaves the setting alone, as it does every other field.
			if recorder.Code != http.StatusOK || vault.record != nil {
				t.Errorf("null: status %d, record %v, want 200 and nothing saved", recorder.Code, vault.record)
			}

			continue
		}

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", bad, recorder.Code, recorder.Body.String())
		}

		if vault.record != nil {
			t.Errorf("%s: the refused value was saved: %v", bad, vault.record)
		}
	}
}
