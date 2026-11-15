package blacksmith_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/audit"
	"blacksmith/internal/audit/audittest"
	handlerpkg "blacksmith/internal/handlers/blacksmith"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
)

const (
	changeActor    = "operator"
	settingsTarget = "broker"
	keyMaxVersions = "vault_max_versions"
	keyInterval    = "reconciler_interval"
)

var errMountUnreadable = errors.New("mount tune is unreadable")

// versionsVault is a Vault whose mount holds a max_versions setting.
type versionsVault struct {
	interfaces.Vault

	versions int
	readErr  error
}

func (v *versionsVault) GetKVMaxVersions(context.Context) (int, error) {
	return v.versions, v.readErr
}

func (v *versionsVault) SetKVMaxVersions(_ context.Context, versions int) error {
	v.versions = versions

	return nil
}

type settingsChangesSetup struct {
	handler  *handlerpkg.Handler
	store    *audit.ChangeStore
	vault    *versionsVault
	interval *intervalRecorder
}

func newSettingsChangesSetup(t *testing.T) settingsChangesSetup {
	t.Helper()

	store := audit.NewChangeStore(audittest.NewKV(), &audittest.Logger{}, audit.WithRetryDelay(time.Millisecond))
	vault := &versionsVault{versions: 5}
	interval := &intervalRecorder{interval: time.Hour}

	return settingsChangesSetup{
		handler: handlerpkg.NewHandler(handlerpkg.Dependencies{
			Logger: logger.Get().Named("test"), Config: createTestConfig(), Vault: vault, Reconciler: interval, Changes: store,
		}),
		store: store, vault: vault, interval: interval,
	}
}

func actorRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.10:51234"

	return req.WithContext(audit.WithActor(req.Context(), audit.ActorFromRequest(req, changeActor)))
}

func (s settingsChangesSetup) save(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	s.handler.UpdateSettings(recorder, actorRequest(t, http.MethodPost, "/b/blacksmith/settings", body))
	s.store.Wait()

	return recorder
}

func (s settingsChangesSetup) changes(t *testing.T) []audit.ChangeRecord {
	t.Helper()

	records, err := s.store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	return records
}

// Every setting a save changes gets a record naming the operator, the key,
// and its old and new values, the reconciler interval included.
func TestUpdateSettingsRecordsEachChangedSetting(t *testing.T) {
	t.Parallel()

	setup := newSettingsChangesSetup(t)

	response := setup.save(t, `{"vault_max_versions": 10, "reconciler_interval": "30m"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}

	got := map[string]audit.ChangeRecord{}
	for _, rec := range setup.changes(t) {
		got[rec.Key] = rec
	}

	for key, want := range map[string][2]string{keyMaxVersions: {"5", "10"}, keyInterval: {"1h0m0s", "30m0s"}} {
		rec, ok := got[key]
		if !ok {
			t.Fatalf("no record for %s in %+v", key, got)
		}

		if rec.Old != want[0] || rec.New != want[1] || rec.Actor != changeActor || rec.From != "192.0.2.10:51234" ||
			rec.Target != settingsTarget || rec.Change != audit.ChangeUpdate || rec.At.IsZero() {
			t.Errorf("record for %s = %+v, want %s to %s by the operator", key, rec, want[0], want[1])
		}
	}

	if len(got) != 2 {
		t.Fatalf("records = %+v, want two", got)
	}
}

// A save that leaves a value as it was, or one the broker refuses, records
// nothing.
func TestUpdateSettingsRecordsOnlyRealChanges(t *testing.T) {
	t.Parallel()

	setup := newSettingsChangesSetup(t)

	setup.save(t, `{"vault_max_versions": 5, "reconciler_interval": "1h"}`)
	setup.save(t, `{"vault_max_versions": 5000}`)
	setup.save(t, `{"reconciler_interval": "5s"}`)

	if records := setup.changes(t); len(records) != 0 {
		t.Fatalf("records = %+v, want none", records)
	}
}

// When the mount's old setting can't be read, the record still names the
// change and says the old value is unknown.
func TestUpdateSettingsRecordsAnUnreadableOldValue(t *testing.T) {
	t.Parallel()

	setup := newSettingsChangesSetup(t)
	setup.vault.readErr = errMountUnreadable

	setup.save(t, `{"vault_max_versions": 10}`)

	records := setup.changes(t)
	if len(records) != 1 || records[0].Old != audit.Unknown || records[0].New != "10" {
		t.Fatalf("records = %+v, want one from %s to 10", records, audit.Unknown)
	}
}

func (s settingsChangesSetup) read(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()

	recorder := httptest.NewRecorder()
	s.handler.HandleChanges(recorder, actorRequest(t, http.MethodGet, "/b/blacksmith/changes"+query, ""))

	return recorder
}

// The read endpoint lists the newest changes of a scope, and a secret's
// values never leave it.
func TestHandleChangesListsAScope(t *testing.T) {
	t.Parallel()

	setup := newSettingsChangesSetup(t)
	setup.save(t, `{"reconciler_interval": "30m"}`)
	setup.store.Record(t.Context(), audit.ChangeRecord{Scope: audit.ScopeEndpoints, Target: "reg-1", Key: "admin_password", Old: "old-pw-9d2e", New: "new-pw-9d2e"})
	setup.store.Wait()

	response := setup.read(t, "?scope=endpoints&limit=10")
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}

	if strings.Contains(response.Body.String(), "pw-9d2e") {
		t.Fatalf("a secret value left the endpoint: %s", response.Body.String())
	}

	var body struct {
		Changes []audit.ChangeRecord `json:"changes"`
	}

	err := json.Unmarshal(response.Body.Bytes(), &body)
	if err != nil {
		t.Fatal(err)
	}

	if len(body.Changes) != 1 || body.Changes[0].Old != audit.Changed || body.Changes[0].Key != "admin_password" {
		t.Fatalf("changes = %+v", body.Changes)
	}

	response = setup.read(t, "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"key":"reconciler_interval"`) {
		t.Fatalf("the default scope is settings: %d %s", response.Code, response.Body.String())
	}

	// A scope with no records answers an empty list, not null.
	response = setup.read(t, "?scope=instances")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"changes":[]`) {
		t.Fatalf("an empty scope answered %d %s, want an empty list", response.Code, response.Body.String())
	}
}

func TestHandleChangesRefusesWhatItCannotAnswer(t *testing.T) {
	t.Parallel()

	setup := newSettingsChangesSetup(t)

	for query, want := range map[string]int{"?scope=everything": http.StatusBadRequest, "?limit=lots": http.StatusBadRequest, "?limit=0": http.StatusBadRequest} {
		if response := setup.read(t, query); response.Code != want {
			t.Errorf("%s answered %d, want %d", query, response.Code, want)
		}
	}

	recorder := httptest.NewRecorder()
	setup.handler.HandleChanges(recorder, actorRequest(t, http.MethodPost, "/b/blacksmith/changes", ""))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST answered %d, want 405", recorder.Code)
	}

	noStore := handlerpkg.NewHandler(handlerpkg.Dependencies{Logger: logger.Get().Named("test"), Config: createTestConfig()})
	recorder = httptest.NewRecorder()
	noStore.HandleChanges(recorder, actorRequest(t, http.MethodGet, "/b/blacksmith/changes", ""))

	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "vault_unavailable") {
		t.Errorf("a broker without a store answered %d %s, want 503 vault_unavailable", recorder.Code, recorder.Body.String())
	}
}
