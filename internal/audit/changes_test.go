package audit_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/audit"
	"blacksmith/internal/audit/audittest"
)

const (
	oldSecret     = "old-s3cr3t-value-7f1c"
	newSecret     = "new-s3cr3t-value-7f1c"
	keyName       = "name"
	keyNestedTok  = "metadata.token"
	halfHour      = "30m0s"
	keyInterval   = "reconciler_interval"
	keyVersions   = "vault_max_versions"
	keyBatchJobs  = "max_batch_jobs"
	operatorActor = "operator"
)

var errSealed = errors.New("vault is sealed")

// actorContext is a request context as the broker's basic-auth wrapper
// leaves it.
func actorContext(t *testing.T) context.Context {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), "POST", "/b/blacksmith/settings", nil)
	req.RemoteAddr = "192.0.2.10:51234"
	req.Header.Set("X-Forwarded-For", "198.51.100.7")

	return audit.WithActor(req.Context(), audit.ActorFromRequest(req, operatorActor))
}

func newStore(kv audit.KV, log *audittest.Logger, at time.Time) *audit.ChangeStore {
	return audit.NewChangeStore(kv, log, audit.WithRetryDelay(time.Millisecond), audit.WithClock(func() time.Time { return at }))
}

func TestSecretKeyDecidesByName(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"password", "cf_password", "PASSWORD", "client_secret", "secret", "access_token", "token",
		"api_key", "apiKey", "key", "private_key", "privateKey", "tls.private_key", "certificate_private_key",
		"credentials", "credential", keyNestedTok, "auth.pass", "unseal_keys.0", "passphrase",
	} {
		if !audit.SecretKey(key) {
			t.Errorf("SecretKey(%q) = false, want true", key)
		}
	}

	for _, key := range []string{
		keyVersions, keyInterval, keyBatchJobs, "username", "api_url", keyName,
		"broker_name", "auto_register", "warning_days", "probe_enabled", "monkey", "keyboard", "keep_versions", "",
	} {
		if audit.SecretKey(key) {
			t.Errorf("SecretKey(%q) = true, want false", key)
		}
	}
}

// A record carries the actor and address from the request context, the
// time, and the key with its old and new values, in Vault and in List.
func TestRecordSavesTheChangeWithTheActor(t *testing.T) {
	t.Parallel()

	vault := audittest.NewKV()
	log := &audittest.Logger{}
	stamp := time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)
	store := newStore(vault, log, stamp)

	store.Record(actorContext(t), audit.ChangeRecord{Scope: audit.ScopeSettings, Target: "broker", Change: audit.ChangeUpdate, Key: keyInterval, Old: "1h0m0s", New: halfHour})
	store.Wait()

	records, err := store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("List returned %d records, want 1: %+v", len(records), records)
	}

	got := records[0]
	want := audit.ChangeRecord{
		ID: got.ID, At: stamp, Scope: audit.ScopeSettings, Actor: operatorActor, From: "192.0.2.10:51234 via 198.51.100.7",
		Target: "broker", Change: audit.ChangeUpdate, Key: keyInterval, Old: "1h0m0s", New: halfHour,
	}

	if got.ID == "" || got != want {
		t.Fatalf("record = %+v, want %+v", got, want)
	}

	if paths := vault.Paths(); len(paths) != 2 || paths[0] != audit.ChangeVaultPath || paths[1] != audit.ChangeVaultPath+"/"+got.ID {
		t.Fatalf("Vault paths = %v, want the index and the record", paths)
	}

	assertLogNamesKeyOnly(t, log.Lines())
}

// assertLogNamesKeyOnly checks the one log line names the key and the
// actor and never either value.
func assertLogNamesKeyOnly(t *testing.T, lines []string) {
	t.Helper()

	if len(lines) != 1 || !strings.HasPrefix(lines[0], "audit=change ") || !strings.Contains(lines[0], `key="reconciler_interval"`) || !strings.Contains(lines[0], `actor="operator"`) {
		t.Fatalf("log = %q", lines)
	}

	if strings.Contains(lines[0], halfHour) || strings.Contains(lines[0], "1h0m0s") {
		t.Fatalf("log line holds a value: %q", lines[0])
	}
}

// A key that names a secret has both values replaced in Vault, in List,
// and in the log, and the secret is never written anywhere.
func TestRecordHidesASecretByItsKeyName(t *testing.T) {
	t.Parallel()

	vault := audittest.NewKV()
	log := &audittest.Logger{}
	store := newStore(vault, log, time.Now())

	store.Record(actorContext(t),
		audit.ChangeRecord{Scope: audit.ScopeEndpoints, Target: "reg-1", Change: audit.ChangeUpdate, Key: "client_secret", Old: oldSecret, New: newSecret},
		audit.ChangeRecord{Scope: audit.ScopeEndpoints, Target: "reg-1", Change: audit.ChangeUpdate, Key: keyNestedTok, Old: "", New: newSecret},
	)
	store.Wait()

	records, err := store.List(t.Context(), audit.ScopeEndpoints, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(records) != 2 {
		t.Fatalf("List returned %d records, want 2", len(records))
	}

	for _, rec := range records {
		if rec.Old != audit.Changed || rec.New != audit.Changed {
			t.Errorf("record %s holds old %q new %q, want %q for both", rec.Key, rec.Old, rec.New, audit.Changed)
		}
	}

	for where, text := range map[string]string{"Vault": vault.Dump(), "the log": log.Text()} {
		if strings.Contains(text, oldSecret) || strings.Contains(text, newSecret) {
			t.Errorf("a secret value reached %s: %s", where, text)
		}
	}
}

// A record someone wrote by hand, or an older build wrote, with a secret
// value under a secret key is hidden again when it is listed.
func TestListHidesASecretItReads(t *testing.T) {
	t.Parallel()

	vault := audittest.NewKV()
	ctx := t.Context()
	stamp := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)

	err := vault.Put(ctx, audit.ChangeVaultPath+"/hand", audit.ChangeRecord{ID: "hand", At: stamp, Scope: audit.ScopeSettings, Key: "api_token", Old: oldSecret, New: newSecret})
	if err != nil {
		t.Fatal(err)
	}

	err = vault.Put(ctx, audit.ChangeVaultPath, map[string]interface{}{"hand": map[string]string{"at": stamp.Format(time.RFC3339Nano), "scope": audit.ScopeSettings}})
	if err != nil {
		t.Fatal(err)
	}

	records, err := newStore(vault, &audittest.Logger{}, stamp).List(ctx, audit.ScopeSettings, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(records) != 1 || records[0].Old != audit.Changed || records[0].New != audit.Changed {
		t.Fatalf("records = %+v, want one record with both values hidden", records)
	}
}

// List keeps to its scope, returns the newest first, and stops at limit.
// The keys of one change read in key order.
func TestListReturnsTheNewestOfTheScope(t *testing.T) {
	t.Parallel()

	vault := audittest.NewKV()
	clock := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	store := audit.NewChangeStore(vault, &audittest.Logger{}, audit.WithClock(func() time.Time { return clock }))
	ctx := actorContext(t)

	for minute := range 12 {
		clock = clock.Add(time.Minute)

		store.Record(ctx, audit.ChangeRecord{Scope: audit.ScopeSettings, Key: keyVersions, New: string(rune('a' + minute))})
		store.Wait()
	}

	clock = clock.Add(time.Minute)

	store.Record(ctx,
		audit.ChangeRecord{Scope: audit.ScopeSettings, Key: "z_last"},
		audit.ChangeRecord{Scope: audit.ScopeSettings, Key: "a_first"},
		audit.ChangeRecord{Scope: audit.ScopeEndpoints, Key: keyName},
	)
	store.Wait()

	records, err := store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(records) != 10 {
		t.Fatalf("List returned %d records, want 10", len(records))
	}

	if records[0].Key != "a_first" || records[1].Key != "z_last" || records[2].New != "l" || records[9].New != "e" {
		t.Fatalf("order = %+v", records)
	}

	for _, rec := range records {
		if rec.Scope != audit.ScopeSettings {
			t.Fatalf("List of settings returned a %s record", rec.Scope)
		}
	}
}

// gatedKV holds every record write until the gate opens, as a slow Vault
// does.
type gatedKV struct {
	*audittest.KV

	gate chan struct{}
}

func (g *gatedKV) Put(ctx context.Context, path string, data interface{}) error {
	if path != audit.ChangeVaultPath {
		<-g.gate
	}

	return g.KV.Put(ctx, path, data) //nolint:wrapcheck // the fake passes the store's own error through
}

// Record never waits on Vault, and a record still on its way to Vault is
// listed already, so a page that reloads right after a save shows it.
func TestRecordDoesNotWaitAndListShowsAPendingRecord(t *testing.T) {
	t.Parallel()

	vault := &gatedKV{KV: audittest.NewKV(), gate: make(chan struct{})}
	store := newStore(vault, &audittest.Logger{}, time.Now())

	done := make(chan struct{})

	go func() {
		store.Record(actorContext(t), audit.ChangeRecord{Scope: audit.ScopeSettings, Key: "max_batch_jobs", Old: "5", New: "10"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record waited on Vault")
	}

	records, err := store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(records) != 1 || records[0].New != "10" {
		t.Fatalf("records = %+v, want the pending record", records)
	}

	close(vault.gate)
	store.Wait()

	records, err = store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil || len(records) != 1 {
		t.Fatalf("after the write: records = %+v, err = %v, want the one record once", records, err)
	}
}

// A Vault that refuses the write is tried three times, every failure is
// logged, and the record is then given up with an error line.
func TestAFailedWriteIsRetriedAndLogged(t *testing.T) {
	t.Parallel()

	vault := audittest.NewKV()
	vault.Fail(errSealed)

	log := &audittest.Logger{}
	store := newStore(vault, log, time.Now())

	store.Record(actorContext(t), audit.ChangeRecord{Scope: audit.ScopeSettings, Key: keyVersions, Old: "5", New: "10"})
	store.Wait()

	tries, givenUp := 0, 0

	for _, line := range log.Lines() {
		switch {
		case strings.Contains(line, "could not be saved on try"):
			tries++
		case strings.Contains(line, "was never saved to Vault"):
			givenUp++
		}

		if !strings.Contains(line, "audit=change") {
			t.Errorf("line %q does not name the change trail", line)
		}
	}

	if tries != 3 || givenUp != 1 {
		t.Fatalf("logged %d failed tries and %d give-ups, want 3 and 1: %q", tries, givenUp, log.Lines())
	}

	_, err := store.List(t.Context(), audit.ScopeSettings, 10)
	if !errors.Is(err, errSealed) {
		t.Fatalf("List error = %v, want the Vault error", err)
	}
}

// A key or value with a line break can't forge a second log line.
func TestRecordCleansControlCharacters(t *testing.T) {
	t.Parallel()

	vault := audittest.NewKV()
	log := &audittest.Logger{}
	store := newStore(vault, log, time.Now())

	store.Record(actorContext(t), audit.ChangeRecord{Scope: audit.ScopeEndpoints, Target: "reg\n1", Key: "name\naudit=change forged", Old: "a\r\nb", New: "c"})
	store.Wait()

	records, err := store.List(t.Context(), audit.ScopeEndpoints, 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if records[0].Key != "nameaudit=change forged" || records[0].Old != "ab" || records[0].Target != "reg1" {
		t.Fatalf("record = %+v", records[0])
	}

	if lines := log.Lines(); len(lines) != 1 || strings.Contains(lines[0], "\n") {
		t.Fatalf("log = %q", lines)
	}
}

// An instance action logs its own line, so its record adds none.
func TestAnInstanceRecordLogsNoSecondLine(t *testing.T) {
	t.Parallel()

	vault := audittest.NewKV()
	log := &audittest.Logger{}
	store := newStore(vault, log, time.Now())

	store.Record(actorContext(t), audit.ChangeRecord{Scope: audit.ScopeInstances, Instance: "7a1b", Deployment: "redis-7a1b", Action: "restart", Outcome: "accepted", TaskID: 42})
	store.Wait()

	if lines := log.Lines(); len(lines) != 0 {
		t.Fatalf("log = %q, want nothing", lines)
	}

	records, err := store.List(t.Context(), audit.ScopeInstances, 10)
	if err != nil || len(records) != 1 || records[0].TaskID != 42 || records[0].Actor != operatorActor {
		t.Fatalf("records = %+v, err = %v", records, err)
	}
}

// A nil store records nothing and lists nothing, so a broker without
// Vault still answers.
func TestANilStoreDoesNothing(t *testing.T) {
	t.Parallel()

	var store *audit.ChangeStore

	store.Record(t.Context(), audit.ChangeRecord{Scope: audit.ScopeSettings, Key: "x"})
	store.Wait()

	records, err := store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil || len(records) != 0 {
		t.Fatalf("records = %+v, err = %v", records, err)
	}
}
