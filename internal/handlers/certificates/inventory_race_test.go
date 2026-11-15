package certificates_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/audit"
	"blacksmith/internal/audit/audittest"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/handlers/certificates"
	"blacksmith/pkg/logger"
)

const (
	keyWarningDays = "warning_days"
	warningDays45  = `{"warning_days":45}`
)

// racingInventory takes concurrent saves, and its settings read waits for
// a second save to read them too.
type racingInventory struct {
	*fakeInventory

	mu       sync.Mutex
	settings certinventory.Settings
	overlap  audittest.Overlap
}

func (r *racingInventory) Settings() certinventory.Settings {
	r.mu.Lock()
	settings := r.settings
	r.mu.Unlock()

	r.overlap.Read()

	return settings
}

func (r *racingInventory) SaveSettings(_ context.Context, settings certinventory.Settings) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.settings = settings

	return nil
}

// Two saves at once each record the warning days they replaced, so the
// trail reads 30 to one value and then that value to the other.
func TestConcurrentSettingsSavesRecordTheirOwnOldValue(t *testing.T) {
	t.Parallel()

	svc := &racingInventory{fakeInventory: &fakeInventory{enabled: true}, settings: certinventory.DefaultSettings()}
	store := audit.NewChangeStore(audittest.NewKV(), &audittest.Logger{}, audit.WithRetryDelay(time.Millisecond))
	handler := certificates.NewInventoryHandler(svc, nil, logger.NewNoOpLogger())
	handler.SetChanges(store)

	var saves sync.WaitGroup

	for _, body := range []string{warningDays45, `{"warning_days":60}`} {
		req := httptest.NewRequestWithContext(t.Context(), "PUT", "/b/certificates/settings", strings.NewReader(body))
		req = req.WithContext(audit.WithActor(req.Context(), audit.ActorFromRequest(req, "operator")))

		saves.Go(func() { handler.ServeHTTP(httptest.NewRecorder(), req) })
	}

	saves.Wait()
	store.Wait()

	records, err := store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	var changes [][2]string

	for _, rec := range records {
		if rec.Key == keyWarningDays {
			changes = append(changes, [2]string{rec.Old, rec.New})
		}
	}

	if len(changes) != 2 || !audittest.Chained("30", changes[0], changes[1]) {
		t.Fatalf("records = %+v, want 30 to one value and that value to the other", records)
	}
}
