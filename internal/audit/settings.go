package audit

import "context"

// RecordDiff keeps one record for each key whose value differs between
// old and current. Every record copies base, the scope, target, and kind
// of change, and takes its key and values from the difference.
func (s *ChangeStore) RecordDiff(ctx context.Context, base ChangeRecord, old, current map[string]string) {
	if s == nil {
		return
	}

	changes := Diff(old, current)
	if len(changes) == 0 {
		return
	}

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

	s.logger.Error("audit=change target=%q the old and new settings could not be compared, so no record was kept: %T to %T", truncate(clean(target), maxChangeText), old, current)
}
