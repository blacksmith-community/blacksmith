package blacksmith_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"blacksmith/internal/audit"
	"blacksmith/internal/audit/audittest"
	"blacksmith/internal/config"
	handlerpkg "blacksmith/internal/handlers/blacksmith"
	"blacksmith/internal/settings"
	"blacksmith/pkg/logger"
)

const (
	keyErrandTimeout = "errand_timeout"
	savedFortyFive   = "45m0s"
)

// settingsRecordVault holds the saved runtime settings record.
type settingsRecordVault struct {
	versionsVault

	record map[string]interface{}
}

func (v *settingsRecordVault) Get(_ context.Context, path string, out interface{}) (bool, error) {
	if path != settings.Path || v.record == nil {
		return false, nil
	}

	target, _ := out.(*map[string]interface{})
	copied := map[string]interface{}{}

	for key, value := range v.record {
		copied[key] = value
	}

	*target = copied

	return true, nil
}

func (v *settingsRecordVault) Put(_ context.Context, _ string, data interface{}) error {
	v.record, _ = data.(map[string]interface{})

	return nil
}

func errandSettingsHandler(vault *settingsRecordVault) (*handlerpkg.Handler, *audit.ChangeStore) {
	store := audit.NewChangeStore(audittest.NewKV(), &audittest.Logger{}, audit.WithRetryDelay(time.Millisecond))

	return handlerpkg.NewHandler(handlerpkg.Dependencies{
		Logger: logger.Get().Named("test"), Config: createTestConfig(), Vault: vault, Changes: store,
	}), store
}

// Saving errand_timeout keeps it in the Vault settings beside the other
// saved settings, answers with the saved wait, and records the change.
func TestUpdateSettingsSavesTheErrandTimeout(t *testing.T) {
	t.Setenv(config.ErrandTimeoutEnvKey, "")

	vault := &settingsRecordVault{record: map[string]interface{}{keyInterval: "5m0s"}}
	handler, store := errandSettingsHandler(vault)

	recorder := httptest.NewRecorder()
	handler.UpdateSettings(recorder, actorRequest(t, http.MethodPut, "/b/blacksmith/settings", `{"errand_timeout": "45m"}`))
	store.Wait()

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}

	if vault.record[keyErrandTimeout] != savedFortyFive || vault.record[keyInterval] != "5m0s" {
		t.Errorf("saved record = %v, want the new timeout and the old interval", vault.record)
	}

	var body map[string]interface{}

	_ = json.Unmarshal(recorder.Body.Bytes(), &body)

	if body[keyErrandTimeout] != savedFortyFive {
		t.Errorf("answer = %v, want errand_timeout 45m0s", body)
	}

	records, err := store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil || len(records) != 1 || records[0].Key != keyErrandTimeout || records[0].Old != "30m0s" || records[0].New != savedFortyFive {
		t.Errorf("records = %+v (%v), want one errand_timeout change from 30m0s to 45m0s", records, err)
	}
}

// A value of zero or less, a bare number, or anything that is not a
// duration is refused with a 400, and nothing is saved.
func TestUpdateSettingsRefusesABadErrandTimeout(t *testing.T) {
	t.Setenv(config.ErrandTimeoutEnvKey, "")

	for _, bad := range []string{"0s", "-5m", "600", "soon", ""} {
		vault := &settingsRecordVault{}
		handler, _ := errandSettingsHandler(vault)

		recorder := httptest.NewRecorder()
		handler.UpdateSettings(recorder, actorRequest(t, http.MethodPut, "/b/blacksmith/settings", `{"errand_timeout": "`+bad+`"}`))

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400: %s", bad, recorder.Code, recorder.Body.String())
		}

		if vault.record != nil {
			t.Errorf("%q: the refused value was saved: %v", bad, vault.record)
		}
	}
}

// The settings answer reports the wait in effect and where it comes from.
func TestGetSettingsReportsTheErrandTimeoutAndItsSource(t *testing.T) {
	t.Setenv(config.ErrandTimeoutEnvKey, "20m")

	vault := &settingsRecordVault{}
	handler, _ := errandSettingsHandler(vault)

	recorder := httptest.NewRecorder()
	handler.GetSettings(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/blacksmith/settings", nil))

	var body map[string]interface{}

	_ = json.Unmarshal(recorder.Body.Bytes(), &body)

	if body[keyErrandTimeout] != "20m0s" || body["errand_timeout_source"] != settings.SourceEnvironment {
		t.Errorf("answer = %v, want 20m0s from the environment", body)
	}

	vault.record = map[string]interface{}{keyErrandTimeout: "10m0s"}
	recorder = httptest.NewRecorder()
	handler.GetSettings(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/blacksmith/settings", nil))

	body = map[string]interface{}{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)

	if body[keyErrandTimeout] != "10m0s" || body["errand_timeout_source"] != settings.SourceVault {
		t.Errorf("answer = %v, want 10m0s from Vault", body)
	}
}
