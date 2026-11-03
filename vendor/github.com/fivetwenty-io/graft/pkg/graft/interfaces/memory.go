package interfaces

// MemoryTracker interface for tracking document changes.
type MemoryTracker interface {
	// RecordMergeChange records a change during merge phase
	RecordMergeChange(path string, oldValue, newValue interface{}, source string) error

	// RecordEvalChange records a change during evaluation phase
	RecordEvalChange(path string, oldValue, newValue interface{}, operator string) error

	// IsEnabled returns whether tracking is enabled
	IsEnabled() bool
}
