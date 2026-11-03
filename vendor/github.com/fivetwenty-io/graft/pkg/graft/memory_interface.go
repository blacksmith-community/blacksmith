package graft

import "github.com/fivetwenty-io/graft/pkg/graft/interfaces"

// Ensure DocumentMemory implements MemoryTracker.
var _ interfaces.MemoryTracker = (*DocumentMemory)(nil)

// RecordMergeChange implements MemoryTracker.
func (dm *DocumentMemory) RecordMergeChange(path string, oldValue, newValue interface{}, source string) error {
	return dm.RecordChange(path, oldValue, newValue, PhaseMerge, OpMerge, source)
}

// RecordEvalChange implements MemoryTracker.
func (dm *DocumentMemory) RecordEvalChange(path string, oldValue, newValue interface{}, operator string) error {
	return dm.RecordChange(path, oldValue, newValue, PhaseEval, OpTransform, operator)
}
