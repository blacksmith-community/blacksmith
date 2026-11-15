package reconciler_test

import (
	"testing"
	"time"

	. "blacksmith/pkg/reconciler"
)

const (
	removalAt     = "orphaned_at"
	removalCount  = "orphan_check_count"
	removalMarked = "orphaned"
	removalStatus = "status"
)

func removalEntry(ago time.Duration, now time.Time, extra map[string]interface{}) map[string]interface{} {
	data := map[string]interface{}{removalMarked: true, removalAt: now.Add(-ago).Format(time.RFC3339)}
	for key, value := range extra {
		data[key] = value
	}

	return data
}

// The rule is the sweep's: more than 24 hours old, or 24 checks, whichever
// comes first, and the checks only count while the count still grows.
func TestOrphanRemovalFor(t *testing.T) {
	t.Parallel()

	now := time.Now()
	five := 5 * time.Minute

	tests := []struct {
		name      string
		data      map[string]interface{}
		interval  time.Duration
		orphaned  bool
		due       bool
		basis     string
		remaining time.Duration
		known     bool
	}{
		{name: "not marked", data: map[string]interface{}{removalMarked: false}},
		{name: "past the clock", data: removalEntry(25*time.Hour, now, nil), interval: five, orphaned: true, due: true, basis: OrphanRemovalByAge, known: true},
		{name: "just inside 24 hours", data: removalEntry(24*time.Hour-time.Minute, now, nil), orphaned: true, basis: OrphanRemovalByAge, remaining: time.Minute, known: true},
		{name: "at the check limit", data: removalEntry(time.Hour, now, map[string]interface{}{removalCount: 24}), orphaned: true, due: true, basis: OrphanRemovalByChecks, known: true},
		{name: "int count", data: removalEntry(time.Hour, now, map[string]interface{}{removalCount: 30}), orphaned: true, due: true, basis: OrphanRemovalByChecks, known: true},
		{name: "checks first", data: removalEntry(time.Hour, now, map[string]interface{}{removalCount: float64(22)}), interval: five, orphaned: true, basis: OrphanRemovalByChecks, remaining: 10 * time.Minute, known: true},
		{name: "clock first", data: removalEntry(23*time.Hour, now, nil), interval: five, orphaned: true, basis: OrphanRemovalByAge, remaining: time.Hour, known: true},
		{name: "no interval leaves the clock", data: removalEntry(2*time.Hour, now, map[string]interface{}{removalCount: 22}), orphaned: true, basis: OrphanRemovalByAge, remaining: 22 * time.Hour, known: true},
		{name: "tombstone ignores the checks span", data: removalEntry(2*time.Hour, now, map[string]interface{}{removalCount: 22, removalStatus: StatusDeleted}), interval: five, orphaned: true, basis: OrphanRemovalByAge, remaining: 22 * time.Hour, known: true},
		{name: "no usable mark and no interval", data: map[string]interface{}{removalMarked: true, removalAt: "later"}, orphaned: true},
		{name: "no usable mark, timed by checks", data: map[string]interface{}{removalMarked: true, removalCount: 20}, interval: five, orphaned: true, basis: OrphanRemovalByChecks, remaining: 20 * time.Minute, known: true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := OrphanRemovalFor(testCase.data, now, testCase.interval)
			if got.Orphaned != testCase.orphaned || got.Due != testCase.due || got.Basis != testCase.basis || got.Known != testCase.known {
				t.Fatalf("got %+v", got)
			}

			if diff := got.Remaining - testCase.remaining; diff < -2*time.Second || diff > 2*time.Second {
				t.Errorf("remaining %v, want %v", got.Remaining, testCase.remaining)
			}
		})
	}
}
