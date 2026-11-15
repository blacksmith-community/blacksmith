package blacksmith_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/audit"
	"blacksmith/internal/audit/audittest"
	handlerpkg "blacksmith/internal/handlers/blacksmith"
	"blacksmith/pkg/logger"
)

// racingReconciler takes concurrent interval saves, and its interval read
// waits for a second save to read it too.
type racingReconciler struct {
	mu       sync.Mutex
	interval time.Duration
	overlap  audittest.Overlap
}

func (r *racingReconciler) UpdateInterval(_ context.Context, interval time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.interval = interval

	return nil
}

func (r *racingReconciler) GetInterval() time.Duration {
	r.mu.Lock()
	interval := r.interval
	r.mu.Unlock()

	r.overlap.Read()

	return interval
}

// Two saves at once each record the interval they replaced, so the trail
// reads 1h to one interval and then that interval to the other.
func TestConcurrentSettingsSavesRecordTheirOwnOldInterval(t *testing.T) {
	t.Parallel()

	store := audit.NewChangeStore(audittest.NewKV(), &audittest.Logger{}, audit.WithRetryDelay(time.Millisecond))
	handler := handlerpkg.NewHandler(handlerpkg.Dependencies{
		Logger: logger.Get().Named("test"), Config: createTestConfig(), Reconciler: &racingReconciler{interval: time.Hour}, Changes: store,
	})

	var saves sync.WaitGroup

	for _, body := range []string{`{"reconciler_interval": "30m"}`, `{"reconciler_interval": "2h"}`} {
		req := actorRequest(t, http.MethodPost, "/b/blacksmith/settings", body)

		saves.Go(func() { handler.UpdateSettings(httptest.NewRecorder(), req) })
	}

	saves.Wait()
	store.Wait()

	records, err := store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(records) != 2 || !audittest.Chained("1h0m0s", [2]string{records[0].Old, records[0].New}, [2]string{records[1].Old, records[1].New}) {
		t.Fatalf("records = %+v, want 1h0m0s to one interval and that interval to the other", records)
	}
}
