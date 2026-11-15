package certificates_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/audit"
	"blacksmith/internal/audit/audittest"
	"blacksmith/internal/handlers/certificates"
	"blacksmith/pkg/logger"
)

var errSettingsStoreDown = errors.New("vault down")

func saveInventorySettings(t *testing.T, svc *fakeInventory, body string) (*httptest.ResponseRecorder, []audit.ChangeRecord) {
	t.Helper()

	store := audit.NewChangeStore(audittest.NewKV(), &audittest.Logger{}, audit.WithRetryDelay(time.Millisecond))
	handler := certificates.NewInventoryHandler(svc, nil, logger.NewNoOpLogger())
	handler.SetChanges(store)

	req := httptest.NewRequestWithContext(context.Background(), "PUT", "/b/certificates/settings", strings.NewReader(body))
	req.RemoteAddr = "192.0.2.10:51234"
	req = req.WithContext(audit.WithActor(req.Context(), audit.ActorFromRequest(req, "operator")))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	store.Wait()

	records, err := store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	return recorder, records
}

// A certificate settings save leaves a record for each setting it changed,
// under the certificate settings, naming the operator.
func TestSaveSettingsRecordsEachChangedSetting(t *testing.T) {
	t.Parallel()

	recorder, records := saveInventorySettings(t, &fakeInventory{enabled: true}, `{"warning_days":45,"critical_days":7,"probe_enabled":false}`)
	if recorder.Code != 200 {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}

	got := map[string]audit.ChangeRecord{}
	for _, rec := range records {
		got[rec.Key] = rec
	}

	if len(got) != 2 {
		t.Fatalf("records = %+v, want warning_days and probe_enabled", records)
	}

	for key, want := range map[string][2]string{"warning_days": {"30", "45"}, "probe_enabled": {"true", "false"}} {
		rec := got[key]
		if rec.Old != want[0] || rec.New != want[1] || rec.Target != "certificates" || rec.Actor != "operator" {
			t.Errorf("record for %s = %+v, want %s to %s", key, rec, want[0], want[1])
		}
	}
}

// A refused or unsaved certificate settings save leaves no record.
func TestSaveSettingsRecordsNoRefusedOrUnsavedSave(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		svc  *fakeInventory
		body string
	}{
		"invalid": {svc: &fakeInventory{enabled: true}, body: `{"warning_days":7,"critical_days":7}`},
		"unsaved": {svc: &fakeInventory{enabled: true, saveErr: errSettingsStoreDown}, body: `{"warning_days":45}`},
		"off":     {svc: &fakeInventory{}, body: `{"warning_days":45}`},
	} {
		if _, records := saveInventorySettings(t, testCase.svc, testCase.body); len(records) != 0 {
			t.Errorf("%s save left records %+v", name, records)
		}
	}
}
