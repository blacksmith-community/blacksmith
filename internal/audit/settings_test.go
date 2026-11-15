package audit_test

import (
	"strings"
	"testing"
	"time"

	"blacksmith/internal/audit"
	"blacksmith/internal/audit/audittest"
)

type batchSettings struct {
	MaxBatchJobs int    `json:"max_batch_jobs"`
	Mode         string `json:"mode"`
	APIToken     string `json:"api_token"`
}

// RecordSettings keeps one record for each JSON key a save changed, under
// the target it names, and hides a secret key's values.
func TestRecordSettingsKeepsEachChangedKey(t *testing.T) {
	t.Parallel()

	vault := audittest.NewKV()
	store := newStore(vault, &audittest.Logger{}, time.Now())

	store.RecordSettings(actorContext(t), "batch",
		batchSettings{MaxBatchJobs: 5, Mode: "serial", APIToken: oldSecret},
		batchSettings{MaxBatchJobs: 10, Mode: "serial", APIToken: newSecret},
	)
	store.Wait()

	records, err := store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(records) != 2 || records[0].Key != "api_token" || records[1].Key != keyBatchJobs {
		t.Fatalf("records = %+v, want api_token and max_batch_jobs", records)
	}

	if records[0].Old != audit.Changed || records[0].New != audit.Changed {
		t.Fatalf("api_token record = %+v, want both values hidden", records[0])
	}

	if rec := records[1]; rec.Old != "5" || rec.New != "10" || rec.Target != "batch" || rec.Change != audit.ChangeUpdate || rec.Actor != operatorActor {
		t.Fatalf("max_batch_jobs record = %+v", rec)
	}

	if strings.Contains(vault.Dump(), oldSecret) || strings.Contains(vault.Dump(), newSecret) {
		t.Fatalf("a secret reached Vault: %s", vault.Dump())
	}
}

// A save that changes nothing records nothing, and settings that can't be
// read as JSON are logged and recorded as nothing.
func TestRecordSettingsSkipsNoChangeAndUnreadableSettings(t *testing.T) {
	t.Parallel()

	log := &audittest.Logger{}
	store := newStore(audittest.NewKV(), log, time.Now())

	store.RecordSettings(actorContext(t), "batch", batchSettings{MaxBatchJobs: 5}, batchSettings{MaxBatchJobs: 5})
	store.RecordSettings(actorContext(t), "batch", make(chan int), batchSettings{})
	store.Wait()

	records, err := store.List(t.Context(), audit.ScopeSettings, 10)
	if err != nil || len(records) != 0 {
		t.Fatalf("records = %+v, err = %v, want none", records, err)
	}

	if text := log.Text(); !strings.Contains(text, "could not be compared") {
		t.Fatalf("log = %q, want the unreadable settings named", text)
	}
}
