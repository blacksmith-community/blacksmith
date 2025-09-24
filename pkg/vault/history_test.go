package vault_test

import (
	"testing"
	"time"

	"blacksmith/pkg/vault"
)

func TestFilterHistoryByRetention(t *testing.T) {
	t.Parallel()

	testCases := buildHistoryRetentionTestCases()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := vault.FilterHistoryByRetention(testCase.history, testCase.retentionDays, testCase.maxEntries)
			if len(result) != testCase.expectedLen {
				t.Errorf("expected %d entries, got %d", testCase.expectedLen, len(result))
			}
		})
	}
}

type historyRetentionTestCase struct {
	name          string
	history       []map[string]interface{}
	retentionDays int
	maxEntries    int
	expectedLen   int
}

func buildHistoryRetentionTestCases() []historyRetentionTestCase {
	now := time.Now()
	cutoffTime := now.AddDate(0, 0, -30).Unix()
	oldTime := now.AddDate(0, 0, -40).Unix()
	recentTime := now.AddDate(0, 0, -10).Unix()

	times := testTimes{cutoffTime: cutoffTime, oldTime: oldTime, recentTime: recentTime}

	return []historyRetentionTestCase{
		buildEmptyHistoryTestCase(),
		buildFilterOldEntriesTestCase(times),
		buildKeepEntriesTestCase(times),
		buildCapMaxEntriesTestCase(times),
		buildNoRetentionPolicyTestCase(times),
		buildEntryWithoutTimestampTestCase(times),
		buildTimestampAsFloat64TestCase(times),
		buildTimestampAsIntTestCase(times),
	}
}

type testTimes struct {
	cutoffTime int64
	oldTime    int64
	recentTime int64
}

func buildEmptyHistoryTestCase() historyRetentionTestCase {
	return historyRetentionTestCase{
		name:          "empty history",
		history:       []map[string]interface{}{},
		retentionDays: 30,
		maxEntries:    100,
		expectedLen:   0,
	}
}

func buildFilterOldEntriesTestCase(times testTimes) historyRetentionTestCase {
	return historyRetentionTestCase{
		name: "filter old entries",
		history: []map[string]interface{}{
			{"timestamp": times.oldTime, "action": "old"},
			{"timestamp": times.recentTime, "action": "recent"},
		},
		retentionDays: 30,
		maxEntries:    100,
		expectedLen:   1,
	}
}

func buildKeepEntriesTestCase(times testTimes) historyRetentionTestCase {
	return historyRetentionTestCase{
		name: "keep entries within retention",
		history: []map[string]interface{}{
			{"timestamp": times.cutoffTime + 1, "action": "keep1"},
			{"timestamp": times.recentTime, "action": "keep2"},
		},
		retentionDays: 30,
		maxEntries:    100,
		expectedLen:   2,
	}
}

func buildCapMaxEntriesTestCase(times testTimes) historyRetentionTestCase {
	return historyRetentionTestCase{
		name: "cap at max entries",
		history: []map[string]interface{}{
			{"timestamp": times.recentTime, "action": "1"},
			{"timestamp": times.recentTime, "action": "2"},
			{"timestamp": times.recentTime, "action": "3"},
			{"timestamp": times.recentTime, "action": "4"},
			{"timestamp": times.recentTime, "action": "5"},
		},
		retentionDays: 30,
		maxEntries:    3,
		expectedLen:   3,
	}
}

func buildNoRetentionPolicyTestCase(times testTimes) historyRetentionTestCase {
	return historyRetentionTestCase{
		name: "no retention policy",
		history: []map[string]interface{}{
			{"timestamp": times.oldTime, "action": "old"},
			{"timestamp": times.recentTime, "action": "recent"},
		},
		retentionDays: 0,
		maxEntries:    0,
		expectedLen:   2,
	}
}

func buildEntryWithoutTimestampTestCase(times testTimes) historyRetentionTestCase {
	return historyRetentionTestCase{
		name: "entry without timestamp",
		history: []map[string]interface{}{
			{"action": "no-timestamp"},
			{"timestamp": times.recentTime, "action": "recent"},
		},
		retentionDays: 30,
		maxEntries:    100,
		expectedLen:   1,
	}
}

func buildTimestampAsFloat64TestCase(times testTimes) historyRetentionTestCase {
	return historyRetentionTestCase{
		name: "timestamp as float64",
		history: []map[string]interface{}{
			{"timestamp": float64(times.recentTime), "action": "float"},
		},
		retentionDays: 30,
		maxEntries:    100,
		expectedLen:   1,
	}
}

func buildTimestampAsIntTestCase(times testTimes) historyRetentionTestCase {
	return historyRetentionTestCase{
		name: "timestamp as int",
		history: []map[string]interface{}{
			{"timestamp": int(times.recentTime), "action": "int"},
		},
		retentionDays: 30,
		maxEntries:    100,
		expectedLen:   1,
	}
}

func TestFilterHistoryByRetention_PreservesOrder(t *testing.T) {
	t.Parallel()

	now := time.Now()
	history := []map[string]interface{}{
		{"timestamp": now.AddDate(0, 0, -5).Unix(), "action": "first"},
		{"timestamp": now.AddDate(0, 0, -4).Unix(), "action": "second"},
		{"timestamp": now.AddDate(0, 0, -3).Unix(), "action": "third"},
	}

	result := vault.FilterHistoryByRetention(history, 30, 100)

	if len(result) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(result))
	}

	if result[0]["action"] != "first" {
		t.Errorf("expected first entry to be 'first', got %v", result[0]["action"])
	}

	if result[1]["action"] != "second" {
		t.Errorf("expected second entry to be 'second', got %v", result[1]["action"])
	}

	if result[2]["action"] != "third" {
		t.Errorf("expected third entry to be 'third', got %v", result[2]["action"])
	}
}

func TestFilterHistoryByRetention_MaxEntriesKeepsLatest(t *testing.T) {
	t.Parallel()

	now := time.Now()
	history := []map[string]interface{}{
		{"timestamp": now.AddDate(0, 0, -5).Unix(), "action": "old1"},
		{"timestamp": now.AddDate(0, 0, -4).Unix(), "action": "old2"},
		{"timestamp": now.AddDate(0, 0, -3).Unix(), "action": "keep1"},
		{"timestamp": now.AddDate(0, 0, -2).Unix(), "action": "keep2"},
		{"timestamp": now.AddDate(0, 0, -1).Unix(), "action": "keep3"},
	}

	result := vault.FilterHistoryByRetention(history, 30, 3)

	if len(result) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(result))
	}

	if result[0]["action"] != "keep1" {
		t.Errorf("expected first kept entry to be 'keep1', got %v", result[0]["action"])
	}

	if result[2]["action"] != "keep3" {
		t.Errorf("expected last kept entry to be 'keep3', got %v", result[2]["action"])
	}
}
