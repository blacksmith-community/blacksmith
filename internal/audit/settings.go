package audit

import (
	"context"
	"fmt"
)

// RecordDiff keeps one record for each key whose value differs between
// old and current. Every record copies base, the scope, target, and kind
// of change, and takes its key and values from the difference.
func (s *ChangeStore) RecordDiff(ctx context.Context, base ChangeRecord, old, current map[string]string) {
	if s == nil {
		return
	}

	s.RecordChanges(ctx, base, Diff(old, current))
}

// RecordChanges keeps one record for each change, copying base, and keeps
// no more than MaxRecordsPerChange of them. Past that, the first 31 are
// kept and the rest fold into one record that counts them, so one request
// can't flood the trail or the log.
func (s *ChangeStore) RecordChanges(ctx context.Context, base ChangeRecord, changes []KeyChange) {
	if s == nil || len(changes) == 0 {
		return
	}

	changes = capChanges(changes)
	records := make([]ChangeRecord, len(changes))

	for index, change := range changes {
		rec := base
		rec.Key, rec.Old, rec.New = change.Key, change.Old, change.New
		records[index] = rec
	}

	s.Record(ctx, records...)
}

// RecordSettings keeps a settings record for each JSON key a save changed
// between old and current, under target. Settings that can't be read as
// JSON are logged by type and recorded as nothing.
func (s *ChangeStore) RecordSettings(ctx context.Context, target string, old, current interface{}) {
	if s == nil {
		return
	}

	before, err := FlattenJSON(old)
	if err == nil {
		var after map[string]string

		after, err = FlattenJSON(current)
		if err == nil {
			s.RecordDiff(ctx, ChangeRecord{Scope: ScopeSettings, Target: target, Change: ChangeUpdate}, before, after)

			return
		}
	}

	s.logError("audit=change target=%q the old and new settings could not be compared, so no record was kept: %T to %T", truncate(clean(target), maxChangeText), old, current)
}

// capChanges keeps the first MaxRecordsPerChange-1 changes and folds the
// rest into one change that counts them, with both its values hidden.
func capChanges(changes []KeyChange) []KeyChange {
	if len(changes) <= MaxRecordsPerChange {
		return changes
	}

	kept := make([]KeyChange, MaxRecordsPerChange-1, MaxRecordsPerChange)
	copy(kept, changes)

	return append(kept, KeyChange{Key: fmt.Sprintf("and %d more keys", len(changes)-len(kept)), Old: Changed, New: Changed})
}
