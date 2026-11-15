package cf

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/audit"
	"blacksmith/internal/audit/audittest"
)

const (
	changeOperator = "operator"
	changeRegName  = "lab-cf"
	changeAPIURL   = "https://api.cf.example.com"
	changeRegID    = "reg-1"
	changeUser     = "admin"
	statusActive   = "active"
	statusFailed   = "failed"
	keyAPIURL      = "api_url"
	keyPassword    = "password"
	keyName        = "name"
	keyUsername    = "username"
	keyBrokerName  = "broker_name"
	brokerName     = "blacksmith"
)

// deletingVault is a memoryVault that can also delete a registration.
type deletingVault struct {
	*memoryVault
}

func (v deletingVault) DeleteCFRegistration(_ context.Context, registrationID string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	delete(v.registrations, registrationID)

	return nil
}

type registrationChanges struct {
	handler *Handler
	vault   deletingVault
	store   *audit.ChangeStore
	kv      *audittest.KV
	log     *audittest.Logger
}

func newRegistrationChanges() registrationChanges {
	vaultKV, log := audittest.NewKV(), &audittest.Logger{}
	store := audit.NewChangeStore(vaultKV, log, audit.WithRetryDelay(time.Millisecond))
	vault := deletingVault{memoryVault: &memoryVault{registrations: map[string]map[string]interface{}{}}}

	handler, _ := registerHandler(vault.memoryVault)
	handler.vault = vault
	handler.SetChanges(store)

	return registrationChanges{handler: handler, vault: vault, store: store, kv: vaultKV, log: log}
}

func (r registrationChanges) serve(t *testing.T, method, path, body string) {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.0.2.10:51234"
	req = req.WithContext(audit.WithActor(req.Context(), audit.ActorFromRequest(req, changeOperator)))

	recorder := httptest.NewRecorder()
	r.handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("%s %s answered %d: %s", method, path, recorder.Code, recorder.Body.String())
	}

	r.store.Wait()
}

func (r registrationChanges) records(t *testing.T) map[string]audit.ChangeRecord {
	t.Helper()

	records, err := r.store.List(t.Context(), audit.ScopeEndpoints, audit.MaxChangeList)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	byKey := map[string]audit.ChangeRecord{}
	for _, rec := range records {
		byKey[rec.Change+" "+rec.Key] = rec
	}

	return byKey
}

// assertNoPassword checks the password reached no record, no Vault write,
// and no log line.
func (r registrationChanges) assertNoPassword(t *testing.T) {
	t.Helper()

	records, err := r.store.List(t.Context(), audit.ScopeEndpoints, audit.MaxChangeList)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	for _, rec := range records {
		if rec.Old == registrationPassword || rec.New == registrationPassword {
			t.Fatalf("record %+v holds the password", rec)
		}
	}

	for where, text := range map[string]string{"Vault": r.kv.Dump(), "the log": r.log.Text()} {
		if strings.Contains(text, registrationPassword) {
			t.Fatalf("the password reached %s", where)
		}
	}
}

const createdRegistration = `{"id":"reg-1","name":"lab-cf","api_url":"https://api.cf.example.com","username":"admin","password":"` +
	registrationPassword + `","broker_name":"blacksmith","created_at":"2026-11-14T09:00:00Z"}`

// A new registration leaves a record of each field the operator set, under
// the registration's name, and none for the broker's own bookkeeping.
func TestCreateRegistrationRecordsEachField(t *testing.T) {
	t.Parallel()

	changes := newRegistrationChanges()
	changes.serve(t, http.MethodPost, "/b/cf/registrations", createdRegistration)

	got := changes.records(t)
	for key, want := range map[string]string{keyName: changeRegName, keyAPIURL: changeAPIURL, keyUsername: changeUser, keyBrokerName: brokerName} {
		rec, ok := got[audit.ChangeCreate+" "+key]
		if !ok || rec.Old != "" || rec.New != want || rec.Target != changeRegName || rec.Actor != changeOperator {
			t.Errorf("create record for %s = %+v, want %q set by the operator", key, rec, want)
		}
	}

	if len(got) != 4 {
		t.Fatalf("records = %+v, want the four fields and no id, created_at, or password", got)
	}

	changes.assertNoPassword(t)
}

// An edit records only the fields it changed. One that moves the API drops
// a stored password, and that record says only that it changed.
func TestUpdateRegistrationRecordsTheChangedFields(t *testing.T) {
	t.Parallel()

	changes := newRegistrationChanges()
	changes.vault.registrations[changeRegID] = map[string]interface{}{
		"id": changeRegID, keyName: changeRegName, keyAPIURL: changeAPIURL, keyUsername: changeUser, keyBrokerName: brokerName,
		keyPassword: registrationPassword, registrationKeyStatus: statusActive, registrationKeyUpdatedAt: "2026-11-14T08:00:00Z",
	}

	changes.serve(t, http.MethodPut, "/b/cf/registrations/reg-1",
		`{"id":"reg-1","name":"lab-cf","api_url":"https://api.sys.example.com","username":"admin","broker_name":"blacksmith","status":"active","updated_at":"2026-11-14T09:00:00Z"}`)

	got := changes.records(t)

	if rec := got[audit.ChangeUpdate+" "+keyAPIURL]; rec.Old != changeAPIURL || rec.New != "https://api.sys.example.com" || rec.Target != changeRegName {
		t.Errorf("api_url record = %+v", rec)
	}

	if rec := got[audit.ChangeUpdate+" "+keyPassword]; rec.Old != audit.Changed || rec.New != audit.Changed {
		t.Errorf("password record = %+v, want both values hidden", rec)
	}

	if len(got) != 2 {
		t.Fatalf("records = %+v, want api_url and password only", got)
	}

	changes.assertNoPassword(t)
}

// A deleted registration leaves a record of each field it had.
func TestDeleteRegistrationRecordsWhatWasRemoved(t *testing.T) {
	t.Parallel()

	changes := newRegistrationChanges()
	changes.vault.registrations[changeRegID] = map[string]interface{}{"id": changeRegID, keyName: changeRegName, keyAPIURL: changeAPIURL, registrationKeyStatus: statusActive}

	changes.serve(t, http.MethodDelete, "/b/cf/registrations/reg-1", "")

	got := changes.records(t)

	if rec := got[audit.ChangeDelete+" "+keyName]; rec.Old != changeRegName || rec.New != "" || rec.Target != changeRegName || rec.Actor != changeOperator {
		t.Errorf("name record = %+v", rec)
	}

	if len(got) != 2 || got[audit.ChangeDelete+" "+keyAPIURL].Old != changeAPIURL {
		t.Fatalf("records = %+v, want name and api_url", got)
	}
}

// Starting a registration records its status moving to registering, and
// the password the request carries reaches no record.
func TestStartRegistrationRecordsTheAttempt(t *testing.T) {
	t.Parallel()

	changes := newRegistrationChanges()
	changes.vault.registrations[changeRegID] = map[string]interface{}{"id": changeRegID, keyName: changeRegName, keyAPIURL: changeAPIURL, keyUsername: changeUser, registrationKeyStatus: statusFailed}

	changes.serve(t, http.MethodPost, "/b/cf/registrations/reg-1/register", `{"password":"`+registrationPassword+`"}`)

	got := changes.records(t)

	rec, ok := got[audit.ChangeRegister+" "+registrationKeyStatus]
	if !ok || rec.Old != statusFailed || rec.New != statusRegistering || rec.Target != changeRegName || rec.Actor != changeOperator || len(got) != 1 {
		t.Fatalf("records = %+v, want one status record from failed to registering", got)
	}

	changes.assertNoPassword(t)
}
