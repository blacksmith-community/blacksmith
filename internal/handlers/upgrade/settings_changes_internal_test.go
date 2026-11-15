package upgrade

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/audit"
	"blacksmith/internal/audit/audittest"
	"blacksmith/internal/upgrade"
	"blacksmith/pkg/logger"
)

var errSettingsUnsaved = errors.New("vault refused the settings")

// settingsTasks is a taskManager that holds one batch setting.
type settingsTasks struct {
	taskManager

	settings upgrade.Settings
	err      error
}

func (s *settingsTasks) GetSettings() upgrade.Settings { return s.settings }

func (s *settingsTasks) UpdateSettings(_ context.Context, settings upgrade.Settings) error {
	if s.err != nil {
		return s.err
	}

	s.settings = settings

	return nil
}

func saveBatchSettings(t *testing.T, tasks *settingsTasks, body string) (*httptest.ResponseRecorder, []audit.ChangeRecord) {
	t.Helper()

	store := audit.NewChangeStore(audittest.NewKV(), &audittest.Logger{}, audit.WithRetryDelay(time.Millisecond))
	handler := &Handler{logger: logger.NewNoOpLogger(), tasks: tasks, changes: store}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/b/upgrade/settings", strings.NewReader(body))
	req.RemoteAddr = "192.0.2.10:51234"
	req = req.WithContext(audit.WithActor(req.Context(), audit.ActorFromRequest(req, "operator")))

	recorder := httptest.NewRecorder()
	handler.UpdateSettings(recorder, req)
	store.Wait()

	records, err := store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	return recorder, records
}

// A max_batch_jobs save leaves a record of the old and new limit, under
// the batch settings, naming the operator.
func TestUpdateSettingsRecordsTheBatchLimit(t *testing.T) {
	t.Parallel()

	recorder, records := saveBatchSettings(t, &settingsTasks{settings: upgrade.Settings{MaxBatchJobs: 5}}, `{"max_batch_jobs": 10}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}

	if len(records) != 1 {
		t.Fatalf("records = %+v, want one", records)
	}

	if rec := records[0]; rec.Key != "max_batch_jobs" || rec.Old != "5" || rec.New != "10" || rec.Target != "batch" || rec.Actor != "operator" {
		t.Fatalf("record = %+v", rec)
	}
}

// A save the manager could not keep, or one that changes nothing, leaves
// no record.
func TestUpdateSettingsRecordsNoUnsavedOrUnchangedLimit(t *testing.T) {
	t.Parallel()

	_, records := saveBatchSettings(t, &settingsTasks{settings: upgrade.Settings{MaxBatchJobs: 5}, err: errSettingsUnsaved}, `{"max_batch_jobs": 10}`)
	if len(records) != 0 {
		t.Fatalf("an unsaved limit left records %+v", records)
	}

	_, records = saveBatchSettings(t, &settingsTasks{settings: upgrade.Settings{MaxBatchJobs: 5}}, `{"max_batch_jobs": 5}`)
	if len(records) != 0 {
		t.Fatalf("an unchanged limit left records %+v", records)
	}
}
