// Package inflight decides whether an index entry whose deployment the
// director does not have may still belong to work in flight. The broker's
// orphan check and the vm-monitor both apply it before they remove or
// tombstone an entry, so they keep the same entries.
package inflight

import (
	"encoding/json"
	"fmt"
	"time"
)

// MinimumAge is how old an index entry must be before it may be removed or
// tombstoned for want of a deployment. An accepted provision has no
// deployment until its deploy task starts, so a younger entry may belong to
// one. It matches the reconciler's orphan sweep.
const MinimumAge = 2 * time.Hour

const (
	// actionProvision is the action a task record names for a provision.
	actionProvision = "provision"

	// stateFailed is the state a task record holds once its operation failed.
	stateFailed = "failed"
)

// TaskRecordReader reads the task record stored at <instance>/task. It
// reports false when there is none.
type TaskRecordReader func() (map[string]interface{}, bool, error)

// Reason returns why an index entry with no deployment may belong to work in
// flight, or "" when nothing shows that it does. The entry is in flight when
// the newest time it carries is less than MinimumAge old, or when its task
// record shows a provision in progress. An entry that carries no time is
// judged by its task record alone, and the record is read only then. A task
// record that cannot be read counts as in flight, so the entry is kept.
func Reason(entry map[string]interface{}, readTaskRecord TaskRecordReader) string {
	newest, dated := NewestTime(entry)
	if dated && time.Since(newest) < MinimumAge {
		return fmt.Sprintf("its newest time is %s, less than %s ago", newest.Format(time.RFC3339), MinimumAge)
	}

	task, exists, err := readTaskRecord()
	if err != nil {
		return fmt.Sprintf("its task record could not be read (%s)", err)
	}

	if !exists {
		return ""
	}

	action, _ := task["action"].(string)
	state, _ := task["state"].(string)

	if action != actionProvision || state == stateFailed {
		return ""
	}

	updatedAt, known := UnixTimestamp(task["updated_at"])
	if !known {
		return "its task record shows a provision in progress with no update time"
	}

	if time.Since(time.Unix(updatedAt, 0)) < MinimumAge {
		return fmt.Sprintf("its task record shows a provision in progress, updated less than %s ago", MinimumAge)
	}

	return ""
}

// NewestTime returns the newest of the times an index entry carries, and
// false when it carries none. They are the RFC 3339 times that the broker,
// the reconciler, and the vm-monitor write on an entry, and its created, a
// Unix time. The reconciler's orphan sweep dates an entry from the same
// fields.
func NewestTime(entry map[string]interface{}) (time.Time, bool) {
	var newest time.Time

	for _, field := range []string{"requested_at", "created_at", "reconciled_at", "discovered_at", "updated_at", "unorphaned_at", "deprovision_requested_at", "deleted_at"} {
		raw, _ := entry[field].(string)

		parsed, err := time.Parse(time.RFC3339, raw)
		if err == nil && parsed.After(newest) {
			newest = parsed
		}
	}

	created, known := UnixTimestamp(entry["created"])
	if known && time.Unix(created, 0).After(newest) {
		newest = time.Unix(created, 0)
	}

	return newest, !newest.IsZero()
}

// UnixTimestamp reads a Unix time that was decoded from JSON as a float64 or
// a json.Number, or stored as an integer.
func UnixTimestamp(value interface{}) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return parsed, true
		}

		asFloat, err := typed.Float64()
		if err != nil {
			return 0, false
		}

		return int64(asFloat), true
	}

	return 0, false
}
