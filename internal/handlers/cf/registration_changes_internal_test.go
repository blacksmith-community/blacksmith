package cf

import (
	"context"
	"fmt"
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
	whereVault     = "Vault"
	whereLog       = "the log"
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

	for where, text := range map[string]string{whereVault: r.kv.Dump(), whereLog: r.log.Text()} {
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

const urlLeak = "url-s3cret-4d1b"

// An API URL pasted with a password in it keeps its username and host in
// the record and loses the password, so the change record, the Vault
// write, the log, and the list never hold it.
func TestCreateRegistrationStripsAPasswordFromTheAPIURL(t *testing.T) {
	t.Parallel()

	changes := newRegistrationChanges()
	changes.serve(t, http.MethodPost, "/b/cf/registrations",
		`{"id":"reg-1","name":"lab-cf","api_url":"https://admin:`+urlLeak+`@api.cf.example.com","username":"admin","broker_name":"blacksmith"}`)

	rec, ok := changes.records(t)[audit.ChangeCreate+" "+keyAPIURL]
	if !ok || rec.New != "https://admin:(changed)@api.cf.example.com" { //nolint:gosec // "(changed)" stands where the password was
		t.Fatalf("api_url record = %+v, want the URL with its password replaced", rec)
	}

	listed, err := changes.store.List(t.Context(), audit.ScopeEndpoints, audit.MaxChangeList)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	for where, text := range map[string]string{whereVault: changes.kv.Dump(), whereLog: changes.log.Text(), "the list": strings.Join(recordValues(listed), "\n")} {
		if strings.Contains(text, urlLeak) {
			t.Fatalf("the URL's password reached %s", where)
		}
	}
}

// recordValues returns every old and new value of the records.
func recordValues(records []audit.ChangeRecord) []string {
	values := make([]string, 0, 2*len(records))
	for _, rec := range records {
		values = append(values, rec.Old, rec.New)
	}

	return values
}

const unknownFieldValue = "unknown-field-raw-62b0"

// A field the registration form does not have is recorded once, under its
// top-level key, with both values hidden, however deep it goes. The form's
// own fields keep their values.
func TestRegistrationRecordsAnUnknownFieldAsChanged(t *testing.T) {
	t.Parallel()

	changes := newRegistrationChanges()
	changes.serve(t, http.MethodPost, "/b/cf/registrations",
		`{"id":"reg-1","name":"lab-cf","api_url":"https://api.cf.example.com","auto_register":true,`+
			`"note":"`+unknownFieldValue+`","labels":{"team":"`+unknownFieldValue+`","tier":{"level":"`+unknownFieldValue+`"}}}`)

	got := changes.records(t)
	for key, want := range map[string]string{keyName: changeRegName, keyAPIURL: changeAPIURL, "auto_register": "true"} {
		if rec := got[audit.ChangeCreate+" "+key]; rec.New != want {
			t.Errorf("create record for %s = %+v, want %q", key, rec, want)
		}
	}

	for _, key := range []string{"note", "labels"} {
		rec, ok := got[audit.ChangeCreate+" "+key]
		if !ok || rec.Old != audit.Changed || rec.New != audit.Changed {
			t.Errorf("create record for %s = %+v, want one record with both values hidden", key, rec)
		}
	}

	for key := range got {
		if strings.Contains(key, ".") {
			t.Errorf("an unknown field was recorded by its nested key %q", key)
		}
	}

	for where, text := range map[string]string{whereVault: changes.kv.Dump(), whereLog: changes.log.Text()} {
		if strings.Contains(text, unknownFieldValue) {
			t.Fatalf("an unknown field's value reached %s", where)
		}
	}
}

// An update that sends the same unknown field again records nothing for
// it, and one that changes it records it once.
func TestRegistrationRecordsAnUnknownFieldOnlyWhenItChanges(t *testing.T) {
	t.Parallel()

	changes := newRegistrationChanges()
	changes.serve(t, http.MethodPost, "/b/cf/registrations", `{"id":"reg-1","name":"lab-cf","labels":{"team":"a"}}`)
	changes.serve(t, http.MethodPut, "/b/cf/registrations/reg-1", `{"id":"reg-1","name":"lab-cf","labels":{"team":"a"}}`)

	if rec, ok := changes.records(t)[audit.ChangeUpdate+" labels"]; ok {
		t.Fatalf("an unchanged unknown field was recorded: %+v", rec)
	}

	changes.serve(t, http.MethodPut, "/b/cf/registrations/reg-1", `{"id":"reg-1","name":"lab-cf","labels":{"team":"b"}}`)

	if rec, ok := changes.records(t)[audit.ChangeUpdate+" labels"]; !ok || rec.New != audit.Changed {
		t.Fatalf("a changed unknown field recorded %+v, want one hidden record", rec)
	}
}

// One registration body with many fields leaves at most 32 records and 32
// log lines, the last of which counts the keys it folded.
func TestARegistrationChangeKeepsAtMost32Records(t *testing.T) {
	t.Parallel()

	fields := make([]string, 0, 100)
	for i := range 100 {
		fields = append(fields, fmt.Sprintf(`"extra_%03d":"x"`, i))
	}

	changes := newRegistrationChanges()
	changes.serve(t, http.MethodPost, "/b/cf/registrations", `{"id":"reg-1","name":"lab-cf",`+strings.Join(fields, ",")+`}`)

	records, err := changes.store.List(t.Context(), audit.ScopeEndpoints, audit.MaxChangeList)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(records) != audit.MaxRecordsPerChange {
		t.Fatalf("one change left %d records, want %d", len(records), audit.MaxRecordsPerChange)
	}

	folded := 0

	for _, rec := range records {
		if rec.Key == "and 70 more keys" {
			folded++

			if rec.Old != audit.Changed || rec.New != audit.Changed {
				t.Errorf("the folded record shows values: %+v", rec)
			}
		}
	}

	if folded != 1 {
		t.Fatalf("records = %+v, want one that folds the last 70 keys", records)
	}

	if lines := len(changes.log.Lines()); lines > audit.MaxRecordsPerChange+1 {
		t.Fatalf("one change logged %d lines, want at most %d", lines, audit.MaxRecordsPerChange+1)
	}
}

// A registration body larger than the test-connection limit is refused
// before it is read whole, and leaves no record.
func TestARegistrationBodyIsBounded(t *testing.T) {
	t.Parallel()

	big := `{"id":"reg-1","name":"lab-cf","note":"` + strings.Repeat("x", testConnectionBodyLimit) + `"}`

	for _, request := range []struct{ method, path string }{
		{http.MethodPost, "/b/cf/registrations"},
		{http.MethodPut, "/b/cf/registrations/reg-1"},
	} {
		changes := newRegistrationChanges()
		changes.vault.registrations[changeRegID] = map[string]interface{}{"id": changeRegID, keyName: changeRegName}

		req := httptest.NewRequestWithContext(t.Context(), request.method, request.path, strings.NewReader(big))
		req.Header.Set("Content-Type", "application/json")

		recorder := httptest.NewRecorder()
		changes.handler.ServeHTTP(recorder, req)
		changes.store.Wait()

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("%s %s with a large body answered %d, want 400", request.method, request.path, recorder.Code)
		}

		if records, _ := changes.store.List(t.Context(), audit.ScopeEndpoints, audit.MaxChangeList); len(records) != 0 {
			t.Errorf("%s %s with a large body left %d records", request.method, request.path, len(records))
		}
	}
}

// nestedFieldValue is a value sent nested under a form field, which no
// record, Vault write, or log line may hold.
const nestedFieldValue = "nested-field-raw-4d19"

// A form field that holds a map or a list is recorded like an unknown
// key, as one record whose values are both hidden, so a value nested
// under it never reaches the trail.
func TestRegistrationHidesAValueNestedUnderAFormField(t *testing.T) {
	t.Parallel()

	changes := newRegistrationChanges()
	changes.serve(t, http.MethodPost, "/b/cf/registrations",
		`{"id":"reg-1","name":{"note":"`+nestedFieldValue+`"},"username":["admin","`+nestedFieldValue+`"],"api_url":"https://api.cf.example.com"}`)
	changes.serve(t, http.MethodPut, "/b/cf/registrations/reg-1",
		`{"id":"reg-1","name":"lab-cf","username":"admin","api_url":{"host":"`+nestedFieldValue+`"}}`)

	records, err := changes.store.List(t.Context(), audit.ScopeEndpoints, audit.MaxChangeList)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	seen := map[string]int{}

	for _, rec := range records {
		seen[rec.Change+" "+rec.Key]++

		if strings.Contains(rec.Key, ".") {
			t.Errorf("a form field was recorded by its nested key %q", rec.Key)
		}

		if strings.Contains(rec.Old+rec.New, nestedFieldValue) {
			t.Errorf("record %s %s holds the nested value", rec.Change, rec.Key)
		}
	}

	for _, key := range []string{audit.ChangeCreate + " " + keyName, audit.ChangeCreate + " " + keyUsername, audit.ChangeUpdate + " " + keyName, audit.ChangeUpdate + " " + keyUsername, audit.ChangeUpdate + " " + keyAPIURL} {
		if seen[key] != 1 {
			t.Errorf("%d records for %q, want one", seen[key], key)
		}
	}

	for _, rec := range records {
		key := rec.Change + " " + rec.Key
		if key == audit.ChangeCreate+" "+keyAPIURL {
			if rec.New != changeAPIURL {
				t.Errorf("a scalar form field lost its value: %+v", rec)
			}

			continue
		}

		if rec.Old != audit.Changed || rec.New != audit.Changed {
			t.Errorf("record %s = %+v, want both values hidden", key, rec)
		}
	}

	for where, text := range map[string]string{whereVault: changes.kv.Dump(), whereLog: changes.log.Text()} {
		if strings.Contains(text, nestedFieldValue) {
			t.Fatalf("a value nested under a form field reached %s", where)
		}
	}
}
