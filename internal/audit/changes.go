package audit

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ChangeVaultPath is the index of the change records, and each record is
// saved under it by its ID, beside the certificate-audit and
// command-audit records.
const ChangeVaultPath = "change-audit"

// The scopes a change record belongs to. A settings record names a broker
// setting, an endpoint record a CF endpoint registration, and an instance
// record a Restart or Recreate of a service instance.
const (
	ScopeSettings  = "settings"
	ScopeEndpoints = "endpoints"
	ScopeInstances = "instances"
)

// The kinds of change a settings or endpoint record names.
const (
	ChangeUpdate   = "update"
	ChangeCreate   = "create"
	ChangeDelete   = "delete"
	ChangeRegister = "register"
)

const (
	// changeAttempts is how many times a record's write is tried.
	changeAttempts = 3
	// changeRetryDefault is the wait between two tries.
	changeRetryDefault = 500 * time.Millisecond
	// changeWriteTimeout bounds one try, and one read of the records.
	changeWriteTimeout = 10 * time.Second
	// maxChangeText bounds each text field of a record, in bytes.
	maxChangeText = 1024
	// MaxChangeList is the most records one List returns.
	MaxChangeList = 100
)

// ChangeRecord is one entry of the change audit trail. A settings or
// endpoint record holds one key that changed, with its old and new values,
// and a key that names a secret holds Changed in place of both. An
// instance record holds one Restart or Recreate attempt with its outcome
// and the BOSH task the director made, when it made one.
type ChangeRecord struct {
	ID    string    `json:"id"`
	At    time.Time `json:"at"`
	Scope string    `json:"scope"`
	// Actor is the user who made the change, and From is where the request
	// came from as the client reported it. Both come from the request
	// context and are for the record alone.
	Actor string `json:"actor,omitempty"`
	From  string `json:"from,omitempty"`
	// Target names what changed, such as the group of settings or the
	// registration, and Change says how.
	Target string `json:"target,omitempty"`
	Change string `json:"change,omitempty"`
	Key    string `json:"key,omitempty"`
	Old    string `json:"old,omitempty"`
	New    string `json:"new,omitempty"`
	// Instance, Deployment, Action, Outcome, and TaskID describe an
	// instance action.
	Instance   string `json:"instance,omitempty"`
	Deployment string `json:"deployment,omitempty"`
	Action     string `json:"action,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
	TaskID     int    `json:"task_id,omitempty"`
}

// KV is the part of Vault the store reads and writes.
type KV interface {
	Get(ctx context.Context, path string, out interface{}) (bool, error)
	Put(ctx context.Context, path string, data interface{}) error
}

// Logger is the part of the broker's logger the store writes to.
type Logger interface {
	Info(msg string, args ...interface{})
	Error(msg string, args ...interface{})
}

// ChangeStoreOption changes how a store works, for tests.
type ChangeStoreOption func(*ChangeStore)

// WithRetryDelay sets the wait between two tries of a write.
func WithRetryDelay(delay time.Duration) ChangeStoreOption {
	return func(store *ChangeStore) { store.retry = delay }
}

// WithClock sets the clock that stamps each record.
func WithClock(now func() time.Time) ChangeStoreOption {
	return func(store *ChangeStore) { store.now = now }
}

// changeIndexEntry is what the index says about one record.
type changeIndexEntry struct {
	At    string `json:"at"`
	Scope string `json:"scope"`
}

// ChangeStore saves change records to Vault in the background, in the
// order they were made, so a request never waits on Vault and a Vault that
// fails never changes what the request answers. Every method of a nil
// store does nothing, so a broker without Vault records nothing.
type ChangeStore struct {
	kv     KV
	logger Logger
	now    func() time.Time
	retry  time.Duration

	// mu guards pending and running. A record stays in pending until its
	// write ends, so List can show it while Vault is still taking it.
	mu      sync.Mutex
	idle    *sync.Cond
	pending []ChangeRecord
	running bool

	// write serialises the read, extend, and write of the index.
	write sync.Mutex
}

// NewChangeStore returns a store that saves to kv and logs to logger.
func NewChangeStore(kv KV, logger Logger, options ...ChangeStoreOption) *ChangeStore {
	store := &ChangeStore{kv: kv, logger: logger, now: time.Now, retry: changeRetryDefault}
	store.idle = sync.NewCond(&store.mu)

	for _, option := range options {
		option(store)
	}

	return store
}

// Record stamps each record with an ID, the time, and the actor and
// address the request context carries, hides the values of a key that
// names a secret, logs it without its values, and queues it to be saved.
// It never waits on Vault.
func (s *ChangeStore) Record(ctx context.Context, records ...ChangeRecord) {
	if s == nil || len(records) == 0 {
		return
	}

	actor, hasActor := ActorFrom(ctx)
	now := s.now().UTC()
	prepared := make([]ChangeRecord, 0, len(records))

	for _, rec := range records {
		rec.ID = uuid.NewString()
		rec.At = now

		if hasActor {
			rec.Actor = actor.Username
			rec.From = actor.From()
		}

		rec = SanitizeChange(rec)
		s.logChange(rec)
		prepared = append(prepared, rec)
	}

	s.mu.Lock()
	s.pending = append(s.pending, prepared...)
	start := !s.running
	s.running = true
	s.mu.Unlock()

	if start {
		go s.drain() //nolint:contextcheck // the writes outlive the request, so a finished request never cancels them
	}
}

// Wait returns once every queued record has been written or given up.
func (s *ChangeStore) Wait() {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for s.running {
		s.idle.Wait()
	}
}

// List returns the newest records of the scope, newest first, up to limit
// of them, or MaxChangeList when limit is out of range. A record still
// waiting to be written is listed too. Each record's values are hidden
// again by key name as they are read, so a record written by hand or by
// an older build still never shows a secret.
func (s *ChangeStore) List(ctx context.Context, scope string, limit int) ([]ChangeRecord, error) {
	if s == nil {
		return []ChangeRecord{}, nil
	}

	if limit <= 0 || limit > MaxChangeList {
		limit = MaxChangeList
	}

	ctx, cancel := context.WithTimeout(ctx, changeWriteTimeout)
	defer cancel()

	var index map[string]changeIndexEntry

	_, err := s.kv.Get(ctx, ChangeVaultPath, &index)
	if err != nil {
		return nil, fmt.Errorf("reading the change index: %w", err)
	}

	listed := make(map[string]bool)
	records := s.pendingOf(scope, listed)

	for _, recordID := range newestIDs(index, scope, limit) {
		if listed[recordID] {
			continue
		}

		var rec ChangeRecord

		found, err := s.kv.Get(ctx, ChangeVaultPath+"/"+recordID, &rec)
		if err != nil {
			return nil, fmt.Errorf("reading change record %s: %w", recordID, err)
		}

		if found && rec.Scope == scope {
			listed[recordID] = true

			records = append(records, SanitizeChange(rec))
		}
	}

	sortNewestFirst(records)

	if len(records) > limit {
		records = records[:limit]
	}

	return records, nil
}

// pendingOf returns the queued records of the scope and marks them listed.
func (s *ChangeStore) pendingOf(scope string, listed map[string]bool) []ChangeRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	var records []ChangeRecord

	for _, rec := range s.pending {
		if rec.Scope == scope {
			listed[rec.ID] = true

			records = append(records, rec)
		}
	}

	return records
}

// newestIDs returns the IDs of the scope's limit newest records the index
// names, newest first.
func newestIDs(index map[string]changeIndexEntry, scope string, limit int) []string {
	type stamped struct {
		id string
		at time.Time
	}

	var entries []stamped

	for recordID, entry := range index {
		if entry.Scope != scope {
			continue
		}

		at, _ := time.Parse(time.RFC3339Nano, entry.At)
		entries = append(entries, stamped{id: recordID, at: at})
	}

	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].at.Equal(entries[j].at) {
			return entries[i].at.After(entries[j].at)
		}

		return entries[i].id < entries[j].id
	})

	if len(entries) > limit {
		entries = entries[:limit]
	}

	ids := make([]string, len(entries))
	for i, entry := range entries {
		ids[i] = entry.id
	}

	return ids
}

// sortNewestFirst orders records newest first, and records made at the
// same moment by key, so the keys of one change read in order.
func sortNewestFirst(records []ChangeRecord) {
	sort.SliceStable(records, func(left, right int) bool {
		if !records[left].At.Equal(records[right].At) {
			return records[left].At.After(records[right].At)
		}

		if records[left].Key != records[right].Key {
			return records[left].Key < records[right].Key
		}

		return records[left].ID < records[right].ID
	})
}

// SanitizeChange hides both values of a key that names a secret, decided
// by the key's name alone, drops control characters from every text
// field so no value can forge a log line, and cuts each field to 1024
// bytes.
func SanitizeChange(rec ChangeRecord) ChangeRecord {
	if SecretKey(rec.Key) {
		rec.Old = Changed
		rec.New = Changed
	}

	for _, field := range []*string{
		&rec.ID, &rec.Scope, &rec.Actor, &rec.From, &rec.Target, &rec.Change, &rec.Key,
		&rec.Old, &rec.New, &rec.Instance, &rec.Deployment, &rec.Action, &rec.Outcome,
	} {
		*field = truncate(clean(*field), maxChangeText)
	}

	return rec
}

// logChange writes the log line of a settings or endpoint record. The line
// names the key and never its values, so the log never holds a setting,
// and Vault holds what the values were. An instance action logs its own
// line, so its record logs none here.
func (s *ChangeStore) logChange(rec ChangeRecord) {
	if rec.Scope == ScopeInstances || s.logger == nil {
		return
	}

	s.logger.Info("audit=change id=%s scope=%s target=%q change=%s key=%q secret=%t actor=%q from=%q",
		rec.ID, rec.Scope, rec.Target, rec.Change, rec.Key, SecretKey(rec.Key), rec.Actor, rec.From)
}

// drain writes the queued records until none is left.
func (s *ChangeStore) drain() {
	for {
		s.mu.Lock()

		if len(s.pending) == 0 {
			s.running = false
			s.idle.Broadcast()
			s.mu.Unlock()

			return
		}

		rec := s.pending[0]
		s.mu.Unlock()

		s.save(rec)

		s.mu.Lock()
		s.pending = s.pending[1:]
		s.mu.Unlock()
	}
}

// save tries the record's write up to three times, and logs each failure.
// A record that never lands is logged as an error and dropped, and its log
// line is all that is left of it.
func (s *ChangeStore) save(rec ChangeRecord) {
	var err error

	for attempt := 1; attempt <= changeAttempts; attempt++ {
		err = s.writeChange(rec)
		if err == nil {
			return
		}

		s.logError("audit=change id=%s scope=%s key=%q could not be saved on try %d of %d: %v", rec.ID, rec.Scope, rec.Key, attempt, changeAttempts, err)

		if attempt < changeAttempts {
			time.Sleep(s.retry)
		}
	}

	s.logError("audit=change id=%s scope=%s key=%q was never saved to Vault: %v", rec.ID, rec.Scope, rec.Key, err)
}

func (s *ChangeStore) logError(msg string, args ...interface{}) {
	if s.logger != nil {
		s.logger.Error(msg, args...)
	}
}

// writeChange makes one try at saving the record and its index entry. The
// index is read, extended, and written back, and never has an ID removed
// or rewritten. A record the index already names was written whole, and a
// body that landed without its index entry is written again.
func (s *ChangeStore) writeChange(rec ChangeRecord) error {
	s.write.Lock()
	defer s.write.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), changeWriteTimeout)
	defer cancel()

	var index map[string]changeIndexEntry

	_, err := s.kv.Get(ctx, ChangeVaultPath, &index)
	if err != nil {
		return fmt.Errorf("reading the change index: %w", err)
	}

	if _, named := index[rec.ID]; named {
		return nil
	}

	err = s.kv.Put(ctx, ChangeVaultPath+"/"+rec.ID, rec)
	if err != nil {
		return fmt.Errorf("writing the change record: %w", err)
	}

	extended := make(map[string]changeIndexEntry, len(index)+1)
	for id, entry := range index {
		extended[id] = entry
	}

	extended[rec.ID] = changeIndexEntry{At: rec.At.UTC().Format(time.RFC3339Nano), Scope: rec.Scope}

	err = s.kv.Put(ctx, ChangeVaultPath, extended)
	if err != nil {
		return fmt.Errorf("writing the change index: %w", err)
	}

	return nil
}
