package reconciler

import (
	"time"
)

// Which limit the removal of an orphaned entry comes from.
const (
	OrphanRemovalByAge    = "age"
	OrphanRemovalByChecks = "checks"
)

// OrphanRemoval says when the reconciler removes one orphan-marked index entry.
// The sweep and the orphans endpoint both read it, so they cannot disagree.
type OrphanRemoval struct {
	// Orphaned is false for an entry the sweep never removes.
	Orphaned bool
	// Due means the sweep removes the entry on its next pass.
	Due bool
	// Basis names the limit that made the entry due, or the one that comes
	// first when it is not due yet. It is empty when no limit can be timed.
	Basis string
	// Remaining is the time before the entry is due. It is meaningful when
	// Known is true, and it is an estimate, since a run happens only once per
	// interval and the interval can change.
	Remaining time.Duration
	Known     bool
	// Count and OrphanedAt are the values the decision read.
	Count      int
	OrphanedAt string
}

// OrphanRemovalFor applies the sweep's rule to one index entry. An entry
// marked orphaned is removed when its orphaned_at is more than
// OrphanRemovalAfter old, or when orphan_check_count has reached
// OrphanRemovalChecks, whichever comes first. A tombstone is judged the same
// way, but its count stops growing, so only the clock can still bring it due.
//
// The interval is the reconciler's current run interval, which turns the
// checks still owed into a time. A zero interval leaves that span out.
func OrphanRemovalFor(data map[string]interface{}, now time.Time, interval time.Duration) OrphanRemoval {
	removal := OrphanRemoval{}

	orphaned, _ := data["orphaned"].(bool)
	if !orphaned {
		return removal
	}

	removal.Orphaned = true
	removal.OrphanedAt, _ = data["orphaned_at"].(string)

	if c, ok := data["orphan_check_count"].(int); ok {
		removal.Count = c
	} else if c, ok := data["orphan_check_count"].(float64); ok {
		removal.Count = int(c)
	}

	ageLeft, hasAge := time.Duration(0), false

	marked, err := time.Parse(time.RFC3339, removal.OrphanedAt)
	if err == nil {
		age := now.Sub(marked)
		if age > OrphanRemovalAfter {
			removal.Due, removal.Basis, removal.Known = true, OrphanRemovalByAge, true

			return removal
		}

		ageLeft, hasAge = OrphanRemovalAfter-age, true
	}

	if removal.Count >= OrphanRemovalChecks {
		removal.Due, removal.Basis, removal.Known = true, OrphanRemovalByChecks, true

		return removal
	}

	// A tombstone's count no longer grows, so the checks span says nothing.
	tombstone := isTombstoneData(data)
	checksLeft, hasChecks := time.Duration(OrphanRemovalChecks-removal.Count)*interval, interval > 0 && !tombstone

	switch {
	case hasAge && hasChecks && checksLeft < ageLeft:
		removal.Basis, removal.Remaining, removal.Known = OrphanRemovalByChecks, checksLeft, true
	case hasAge:
		removal.Basis, removal.Remaining, removal.Known = OrphanRemovalByAge, ageLeft, true
	case hasChecks:
		removal.Basis, removal.Remaining, removal.Known = OrphanRemovalByChecks, checksLeft, true
	}

	return removal
}

// isTombstoneData reports whether an index entry is marked deleted, with the
// same two tests the synchronizer uses.
func isTombstoneData(data map[string]interface{}) bool {
	if status, ok := data["status"].(string); ok && status == StatusDeleted {
		return true
	}

	deleted, _ := data["deleted"].(bool)

	return deleted
}
