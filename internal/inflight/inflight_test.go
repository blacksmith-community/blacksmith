package inflight_test

import (
	"strings"
	"testing"
	"time"

	"blacksmith/internal/inflight"
)

// An index entry whose task record holds an unknown outcome is in flight
// whatever the record's action and age, because only a LastOperation poll
// settles the outcome.
func TestReasonKeepsAnUnknownOutcome(t *testing.T) {
	t.Parallel()

	old := time.Now().Add(-3 * inflight.MinimumAge)
	entry := map[string]interface{}{"created": old.Unix()}

	for _, action := range []string{"provision", "deprovision"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()

			record := map[string]interface{}{
				"action": action, "state": "outcome_unknown", "context_id": "5f0c2a52-6a1e-4d0c-9a8e-1f6c7e0b9d11",
				"updated_at": old.Unix(),
			}

			reason := inflight.Reason(entry, func() (map[string]interface{}, bool, error) { return record, true, nil })
			if reason == "" {
				t.Fatalf("an old entry whose %s outcome is unknown was not in flight", action)
			}

			if !strings.Contains(reason, "5f0c2a52-6a1e-4d0c-9a8e-1f6c7e0b9d11") {
				t.Errorf("the reason %q does not name the context ID", reason)
			}
		})
	}
}

// An old entry whose finished deprovision record is old is not in flight,
// so only the unknown state keeps the entry above.
func TestReasonDoesNotKeepAnOldDeprovision(t *testing.T) {
	t.Parallel()

	old := time.Now().Add(-3 * inflight.MinimumAge)
	entry := map[string]interface{}{"created": old.Unix()}
	record := map[string]interface{}{"action": "deprovision", "state": "in_progress", "updated_at": old.Unix()}

	reason := inflight.Reason(entry, func() (map[string]interface{}, bool, error) { return record, true, nil })
	if reason != "" {
		t.Fatalf("an old entry with an old deprovision record was in flight: %s", reason)
	}
}
