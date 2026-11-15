package upgrade

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/audit"
	"blacksmith/internal/audit/audittest"
	"blacksmith/internal/upgrade"
	"blacksmith/pkg/logger"
)

// racingTasks is a taskManager that takes concurrent saves, and whose
// settings read waits for a second save to read them too.
type racingTasks struct {
	taskManager

	mu       sync.Mutex
	settings upgrade.Settings
	overlap  audittest.Overlap
}

func (r *racingTasks) GetSettings() upgrade.Settings {
	r.mu.Lock()
	settings := r.settings
	r.mu.Unlock()

	r.overlap.Read()

	return settings
}

func (r *racingTasks) UpdateSettings(_ context.Context, settings upgrade.Settings) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.settings = settings

	return nil
}

// Two saves at once each record the limit they replaced, so the trail
// reads 5 to one limit and then that limit to the other.
func TestConcurrentSettingsSavesRecordTheirOwnOldLimit(t *testing.T) {
	t.Parallel()

	tasks := &racingTasks{settings: upgrade.Settings{MaxBatchJobs: 5}}
	store := audit.NewChangeStore(audittest.NewKV(), &audittest.Logger{}, audit.WithRetryDelay(time.Millisecond))
	handler := &Handler{logger: logger.NewNoOpLogger(), tasks: tasks, changes: store}

	var saves sync.WaitGroup

	for _, body := range []string{`{"max_batch_jobs": 7}`, `{"max_batch_jobs": 9}`} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/b/upgrade/settings", strings.NewReader(body))
		req = req.WithContext(audit.WithActor(req.Context(), audit.ActorFromRequest(req, "operator")))

		saves.Go(func() { handler.UpdateSettings(httptest.NewRecorder(), req) })
	}

	saves.Wait()
	store.Wait()

	records, err := store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(records) != 2 || !audittest.Chained("5", [2]string{records[0].Old, records[0].New}, [2]string{records[1].Old, records[1].New}) {
		t.Fatalf("records = %+v, want 5 to one limit and that limit to the other", records)
	}
}
