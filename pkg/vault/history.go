package vault

import (
	"time"
)

// FilterHistoryByRetention filters history entries based on retention policy.
// Removes entries older than retentionDays and caps total entries at maxEntries.
// Returns the filtered history slice.
func FilterHistoryByRetention(history []map[string]interface{}, retentionDays, maxEntries int) []map[string]interface{} {
	if len(history) == 0 {
		return history
	}

	if retentionDays <= 0 && maxEntries <= 0 {
		return history
	}

	now := time.Now()
	cutoffTime := now.AddDate(0, 0, -retentionDays).Unix()

	filtered := make([]map[string]interface{}, 0, len(history))

	for _, entry := range history {
		timestamp, ok := entry["timestamp"]
		if !ok {
			// Debug: log entries without timestamp
			// fmt.Printf("DEBUG: History entry %d has no timestamp, skipping: %+v\n", idx, entry)
			continue
		}

		var timestampValue int64
		switch typedTimestamp := timestamp.(type) {
		case int64:
			timestampValue = typedTimestamp
		case float64:
			timestampValue = int64(typedTimestamp)
		case int:
			timestampValue = int64(typedTimestamp)
		default:
			continue
		}

		if retentionDays > 0 && timestampValue < cutoffTime {
			// fmt.Printf("DEBUG: History entry %d with timestamp %d is older than cutoff %d, skipping\n", idx, timestampValue, cutoffTime)
			continue
		}

		// fmt.Printf("DEBUG: Adding history entry %d to filtered list\n", idx)
		filtered = append(filtered, entry)
	}

	if maxEntries > 0 && len(filtered) > maxEntries {
		filtered = filtered[len(filtered)-maxEntries:]
	}

	return filtered
}
