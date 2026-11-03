package graft

import (
	"bytes"
	"compress/gzip"
	"encoding/gob"
	"fmt"
	"reflect"
	"sync"
	"time"
	"unsafe"
)

// ChangeOperation represents the type of change made to a node.
type ChangeOperation int

// ChangeOperation constants.
const (
	// OpSet represents a direct value assignment.
	OpSet ChangeOperation = iota
	// OpMerge represents a value merged from another document.
	OpMerge
	// OpDelete represents a deleted value.
	OpDelete
	// OpTransform represents a value transformed by operator.
	OpTransform
	// OpPrune represents a pruned value.
	OpPrune
	// OpReplace represents a replaced value.
	OpReplace
)

// ChangePhase represents when the change occurred.
type ChangePhase int

// ChangePhase constants.
const (
	// PhaseInitial represents the initial document load.
	PhaseInitial ChangePhase = iota
	// PhaseMerge represents a change during merge operation.
	PhaseMerge
	// PhaseEval represents a change during evaluation.
	PhaseEval
	// PhaseManual represents a manual modification.
	PhaseManual
)

// NodeVersion represents a single version of a node's value.
type NodeVersion struct {
	Version     int                    // Version number (incrementing)
	Value       interface{}            // The actual value at this version
	Timestamp   time.Time              // When the change occurred
	Phase       ChangePhase            // Which phase made the change
	Operation   ChangeOperation        // Type of change
	Source      string                 // File or operator that made the change
	Metadata    map[string]interface{} // Additional metadata
	PrevVersion *int                   // Previous version number (nil for first)
}

// NodeHistory tracks all versions of a specific path.
type NodeHistory struct {
	Path         string        // The path in the document
	Versions     []NodeVersion // All versions of this node
	VersionIndex map[int]int   // Map version number to slice index
	Current      int           // Current version number
}

// ChangeEvent represents a change in the document timeline.
type ChangeEvent struct {
	Path      string          // Path that changed
	Version   int             // Version number
	Timestamp time.Time       // When it happened
	Phase     ChangePhase     // Phase of change
	Operation ChangeOperation // Type of change
	OldValue  interface{}     // Previous value (nil if new)
	NewValue  interface{}     // New value (nil if deleted)
	Source    string          // Source of change
}

// VersionDiff represents the difference between two versions.
type VersionDiff struct {
	Path        string
	FromVersion int
	ToVersion   int
	FromValue   interface{}
	ToValue     interface{}
	Changes     []ChangeEvent // All changes between versions
}

// HistoryFilter for querying history.
type HistoryFilter struct {
	Path      string           // Filter by path: exact match, PathMatches wildcards (*, **, [0], [*], [key=value]), or segment-aware prefix ("db" matches "db.host" but not "dbextra"). Empty disables the filter.
	Phase     *ChangePhase     // Filter by phase
	Operation *ChangeOperation // Filter by operation
	Source    string           // Filter by source
	After     *time.Time       // Changes after this time
	Before    *time.Time       // Changes before this time
	Limit     int              // Maximum entries to return, 0 for unlimited. Applied last, after every other filter, keeping the first Limit matches in timeline (chronological) order - not the most recent Limit.
}

// DocumentMemory manages the complete history of a document.
type DocumentMemory struct {
	mu        sync.RWMutex
	histories map[string]*NodeHistory // Path -> History
	timeline  []ChangeEvent           // Chronological list of all changes
	enabled   bool                    // Whether tracking is enabled
	config    MemoryConfig            // Configuration settings

	// Memory management
	totalVersions int          // Total number of versions across all nodes
	memoryUsage   int64        // Estimated memory usage in bytes
	lastCleanup   time.Time    // Last time cleanup was run
	cleanupTicker *time.Ticker // Ticker for periodic cleanup
	cleanupStop   chan bool    // Channel to stop cleanup

	// Compression
	compressedData map[string][]byte // Compressed old versions
}

// MemoryConfig configures the document memory behavior.
type MemoryConfig struct {
	Enabled              bool          // Enable history tracking
	MaxVersionsPerNode   int           // Max versions to keep per node
	MaxTotalVersions     int           // Max total versions across all nodes
	MaxMemoryMB          int           // Max memory usage in MB
	CompressAfter        time.Duration // Compress versions older than this
	CleanupInterval      time.Duration // How often to run cleanup
	TrackMergePhase      bool          // Track changes during merge
	TrackEvalPhase       bool          // Track changes during evaluation
	IncludeOperatorState bool          // Include operator state in metadata
	EnableCompression    bool          // Enable compression for old versions
	CompressThreshold    int           // Number of versions before compression kicks in
}

// NewDocumentMemory creates a new document memory instance.
func NewDocumentMemory(config MemoryConfig) *DocumentMemory {
	dm := &DocumentMemory{
		histories:      make(map[string]*NodeHistory),
		timeline:       make([]ChangeEvent, 0),
		enabled:        config.Enabled,
		config:         config,
		compressedData: make(map[string][]byte),
		lastCleanup:    time.Now(),
	}

	// Start background cleanup if enabled
	if config.Enabled && config.CleanupInterval > 0 {
		dm.startBackgroundCleanup()
	}

	return dm
}

// RecordChange records a change to a node.
func (dm *DocumentMemory) RecordChange(path string, oldValue, newValue interface{}, phase ChangePhase, operation ChangeOperation, source string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if !dm.enabled {
		return nil
	}

	// Get or create history for this path
	history, exists := dm.histories[path]
	if !exists {
		history = &NodeHistory{
			Path:         path,
			Versions:     make([]NodeVersion, 0),
			VersionIndex: make(map[int]int),
			Current:      0,
		}
		dm.histories[path] = history
	}

	// Create new version
	version := NodeVersion{
		Version:   history.Current + 1,
		Value:     newValue,
		Timestamp: time.Now(),
		Phase:     phase,
		Operation: operation,
		Source:    source,
		Metadata:  make(map[string]interface{}),
	}

	if len(history.Versions) > 0 {
		prevVersion := history.Current
		version.PrevVersion = &prevVersion
	}

	// Add to history
	history.VersionIndex[version.Version] = len(history.Versions)
	history.Versions = append(history.Versions, version)
	history.Current = version.Version

	// Prune per-node if configured
	if dm.config.MaxVersionsPerNode > 0 {
		dm.pruneOldVersions(history)
	}

	// Add to timeline
	event := ChangeEvent{
		Path:      path,
		Version:   version.Version,
		Timestamp: version.Timestamp,
		Phase:     phase,
		Operation: operation,
		OldValue:  oldValue,
		NewValue:  newValue,
		Source:    source,
	}
	dm.timeline = append(dm.timeline, event)

	// Update memory tracking
	dm.totalVersions++
	dm.memoryUsage += estimateSize(newValue)

	// Check if cleanup needed
	if dm.shouldRunCleanup() {
		dm.performCleanup()
	}

	return nil
}

// GetHistory returns the complete history for a path.
func (dm *DocumentMemory) GetHistory(path string) (*NodeHistory, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	history, exists := dm.histories[path]
	if !exists {
		return nil, fmt.Errorf("no history found for path: %s", path)
	}

	// Return a copy to prevent external modification
	historyCopy := &NodeHistory{
		Path:         history.Path,
		Versions:     make([]NodeVersion, len(history.Versions)),
		VersionIndex: make(map[int]int),
		Current:      history.Current,
	}
	copy(historyCopy.Versions, history.Versions)
	for k, v := range history.VersionIndex {
		historyCopy.VersionIndex[k] = v
	}

	return historyCopy, nil
}

// GetVersion returns the value at a specific version.
func (dm *DocumentMemory) GetVersion(path string, version int) (interface{}, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	history, exists := dm.histories[path]
	if !exists {
		return nil, fmt.Errorf("no history found for path: %s", path)
	}

	idx, exists := history.VersionIndex[version]
	if !exists {
		return nil, fmt.Errorf("version %d not found for path: %s", version, path)
	}

	return history.Versions[idx].Value, nil
}

// GetCurrentValue returns the current value for a path.
func (dm *DocumentMemory) GetCurrentValue(path string) (interface{}, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	history, exists := dm.histories[path]
	if !exists {
		return nil, fmt.Errorf("no history found for path: %s", path)
	}

	if len(history.Versions) == 0 {
		return nil, fmt.Errorf("no versions found for path: %s", path)
	}

	return history.Versions[len(history.Versions)-1].Value, nil
}

// Compare compares two versions of a node.
func (dm *DocumentMemory) Compare(path string, v1, v2 int) (*VersionDiff, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	history, exists := dm.histories[path]
	if !exists {
		return nil, fmt.Errorf("no history found for path: %s", path)
	}

	idx1, exists1 := history.VersionIndex[v1]
	idx2, exists2 := history.VersionIndex[v2]

	if !exists1 || !exists2 {
		return nil, fmt.Errorf("one or both versions not found")
	}

	diff := &VersionDiff{
		Path:        path,
		FromVersion: v1,
		ToVersion:   v2,
		FromValue:   history.Versions[idx1].Value,
		ToValue:     history.Versions[idx2].Value,
		Changes:     make([]ChangeEvent, 0),
	}

	// Collect all changes between versions
	startIdx := idx1
	endIdx := idx2
	if startIdx > endIdx {
		startIdx, endIdx = endIdx, startIdx
	}

	for i := startIdx; i <= endIdx; i++ {
		v := &history.Versions[i]
		var oldValue interface{}
		if v.PrevVersion != nil {
			if prevIdx, ok := history.VersionIndex[*v.PrevVersion]; ok {
				oldValue = history.Versions[prevIdx].Value
			}
		}

		diff.Changes = append(diff.Changes, ChangeEvent{
			Path:      path,
			Version:   v.Version,
			Timestamp: v.Timestamp,
			Phase:     v.Phase,
			Operation: v.Operation,
			OldValue:  oldValue,
			NewValue:  v.Value,
			Source:    v.Source,
		})
	}

	return diff, nil
}

// GetTimeline returns the complete timeline of changes.
func (dm *DocumentMemory) GetTimeline() []ChangeEvent {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	// Return a copy
	timeline := make([]ChangeEvent, len(dm.timeline))
	copy(timeline, dm.timeline)
	return timeline
}

// selects reports whether event satisfies every set field of f. Limit is
// not consulted here: it bounds how many selected events a caller keeps,
// not whether any one of them matches.
func (f HistoryFilter) selects(event *ChangeEvent) bool {
	switch {
	// Path matches exactly, by wildcard, or by segment prefix.
	case f.Path != "" && !matchPath(event.Path, f.Path):
		return false
	case f.Phase != nil && event.Phase != *f.Phase:
		return false
	case f.Operation != nil && event.Operation != *f.Operation:
		return false
	case f.Source != "" && event.Source != f.Source:
		return false
	case f.After != nil && event.Timestamp.Before(*f.After):
		return false
	case f.Before != nil && event.Timestamp.After(*f.Before):
		return false
	}
	return true
}

// Query returns changes matching the filter.
func (dm *DocumentMemory) Query(filter HistoryFilter) []ChangeEvent {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	results := make([]ChangeEvent, 0)

	for i := range dm.timeline {
		event := &dm.timeline[i]
		if !filter.selects(event) {
			continue
		}

		results = append(results, *event)

		// Limit stops collection as soon as it is satisfied, so it caps
		// the earliest Limit matches in timeline order rather than
		// requiring a full scan followed by a truncation.
		if filter.Limit > 0 && len(results) >= filter.Limit {
			break
		}
	}

	return results
}

// Enable enables history tracking.
func (dm *DocumentMemory) Enable() {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	dm.enabled = true
}

// Disable disables history tracking.
func (dm *DocumentMemory) Disable() {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	dm.enabled = false
}

// IsEnabled returns whether history tracking is enabled.
func (dm *DocumentMemory) IsEnabled() bool {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	return dm.enabled
}

// Clear clears all history.
func (dm *DocumentMemory) Clear() {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	dm.histories = make(map[string]*NodeHistory)
	dm.timeline = make([]ChangeEvent, 0)
	dm.compressedData = make(map[string][]byte)
	dm.totalVersions = 0
	dm.memoryUsage = 0
}

// pruneOldVersions removes old versions to maintain size limits.
func (dm *DocumentMemory) pruneOldVersions(history *NodeHistory) {
	if len(history.Versions) <= dm.config.MaxVersionsPerNode {
		return
	}

	// Keep only the most recent versions
	toKeep := dm.config.MaxVersionsPerNode
	newVersions := history.Versions[len(history.Versions)-toKeep:]

	// Rebuild version index
	history.Versions = newVersions
	history.VersionIndex = make(map[int]int)
	for i, v := range history.Versions {
		history.VersionIndex[v.Version] = i
	}
}

// matchPath checks if a recorded history path matches a filter pattern. A
// path matches when it equals the pattern, when PathMatches accepts it
// (*, **, [0], [*], [key=value] wildcards - the same grammar diff filtering
// uses), or when the pattern is a segment-aware path prefix of it, so
// "db" matches "db.host" but not "dbextra" (the previous raw byte-prefix
// check wrongly matched both). Recorded paths use the unified dotted no-$
// form, which ParsePath handles directly.
func matchPath(path, pattern string) bool {
	if path == pattern {
		return true
	}
	return PathMatches(path, pattern) || PathHasPrefix(path, pattern)
}

// String returns a string representation of a ChangeOperation.
func (op ChangeOperation) String() string {
	switch op {
	case OpSet:
		return "SET"
	case OpMerge:
		return "MERGE"
	case OpDelete:
		return "DELETE"
	case OpTransform:
		return "TRANSFORM"
	case OpPrune:
		return "PRUNE"
	case OpReplace:
		return "REPLACE"
	default:
		return "UNKNOWN"
	}
}

// String returns a string representation of a ChangePhase.
func (phase ChangePhase) String() string {
	switch phase {
	case PhaseInitial:
		return "INITIAL"
	case PhaseMerge:
		return "MERGE"
	case PhaseEval:
		return "EVAL"
	case PhaseManual:
		return "MANUAL"
	default:
		return "UNKNOWN"
	}
}

// startBackgroundCleanup starts the background cleanup goroutine.
func (dm *DocumentMemory) startBackgroundCleanup() {
	dm.cleanupTicker = time.NewTicker(dm.config.CleanupInterval)
	dm.cleanupStop = make(chan bool)

	go func() {
		for {
			select {
			case <-dm.cleanupTicker.C:
				dm.mu.Lock()
				dm.performCleanup()
				dm.mu.Unlock()
			case <-dm.cleanupStop:
				return
			}
		}
	}()
}

// StopBackgroundCleanup stops the background cleanup goroutine.
func (dm *DocumentMemory) StopBackgroundCleanup() {
	if dm.cleanupTicker != nil {
		dm.cleanupTicker.Stop()
	}
	if dm.cleanupStop != nil {
		close(dm.cleanupStop)
	}
}

// shouldRunCleanup checks if cleanup should be run.
func (dm *DocumentMemory) shouldRunCleanup() bool {
	// Check version limits
	if dm.config.MaxTotalVersions > 0 && dm.totalVersions > dm.config.MaxTotalVersions {
		return true
	}

	// Check memory limits
	if dm.config.MaxMemoryMB > 0 {
		maxBytes := int64(dm.config.MaxMemoryMB) * 1024 * 1024
		if dm.memoryUsage > maxBytes {
			return true
		}
	}

	return false
}

// performCleanup performs memory cleanup.
func (dm *DocumentMemory) performCleanup() {
	now := time.Now()
	dm.lastCleanup = now

	// Compress old versions if enabled
	if dm.config.EnableCompression && dm.config.CompressAfter > 0 {
		compressThreshold := now.Add(-dm.config.CompressAfter)
		dm.compressOldVersions(compressThreshold)
	}

	// Prune excess versions
	if dm.config.MaxTotalVersions > 0 && dm.totalVersions > dm.config.MaxTotalVersions {
		dm.pruneExcessVersions()
	}

	// Prune by memory limit
	if dm.config.MaxMemoryMB > 0 {
		maxBytes := int64(dm.config.MaxMemoryMB) * 1024 * 1024
		if dm.memoryUsage > maxBytes {
			dm.pruneByMemoryLimit(maxBytes)
		}
	}

	// Clean up empty histories
	dm.cleanupEmptyHistories()
}

// compressOldVersions compresses versions older than the threshold.
func (dm *DocumentMemory) compressOldVersions(threshold time.Time) {
	if !dm.config.EnableCompression {
		return
	}

	for path, history := range dm.histories {
		if len(history.Versions) < dm.config.CompressThreshold {
			continue
		}

		// Find versions to compress
		var toCompress []NodeVersion
		for i, v := range history.Versions {
			if v.Timestamp.Before(threshold) && i < len(history.Versions)-1 { // Keep latest uncompressed
				toCompress = append(toCompress, v)
			}
		}

		if len(toCompress) > 0 {
			// Compress the versions
			compressed, err := compressVersions(toCompress)
			if err == nil {
				key := fmt.Sprintf("%s:%d-%d", path, toCompress[0].Version, toCompress[len(toCompress)-1].Version)
				dm.compressedData[key] = compressed

				// Remove compressed versions from history (keep metadata)
				for _, v := range toCompress {
					if idx, ok := history.VersionIndex[v.Version]; ok {
						// Clear the value but keep the version metadata
						history.Versions[idx].Value = nil
						history.Versions[idx].Metadata["compressed"] = true
						history.Versions[idx].Metadata["compressed_key"] = key
					}
				}
			}
		}
	}
}

// pruneExcessVersions removes oldest versions to maintain total version limit.
func (dm *DocumentMemory) pruneExcessVersions() {
	if dm.totalVersions <= dm.config.MaxTotalVersions {
		return
	}

	// Calculate how many versions to remove
	toRemove := dm.totalVersions - dm.config.MaxTotalVersions

	// Sort all versions by timestamp
	type versionInfo struct {
		path      string
		version   int
		timestamp time.Time
	}

	var allVersions []versionInfo
	for path, history := range dm.histories {
		for _, v := range history.Versions {
			allVersions = append(allVersions, versionInfo{
				path:      path,
				version:   v.Version,
				timestamp: v.Timestamp,
			})
		}
	}

	// Sort by timestamp (oldest first)
	for i := 0; i < len(allVersions)-1; i++ {
		for j := i + 1; j < len(allVersions); j++ {
			if allVersions[j].timestamp.Before(allVersions[i].timestamp) {
				allVersions[i], allVersions[j] = allVersions[j], allVersions[i]
			}
		}
	}

	// Remove oldest versions
	for i := 0; i < toRemove && i < len(allVersions); i++ {
		info := allVersions[i]
		if history, ok := dm.histories[info.path]; ok {
			dm.removeVersion(history, info.version)
		}
	}
}

// pruneByMemoryLimit removes versions to stay under memory limit.
func (dm *DocumentMemory) pruneByMemoryLimit(maxBytes int64) {
	// Similar to pruneExcessVersions but based on memory usage
	// This is a simplified implementation
	for dm.memoryUsage > maxBytes && dm.totalVersions > 0 {
		// Find oldest version
		var oldestPath string
		var oldestVersion int
		var oldestTime time.Time

		for path, history := range dm.histories {
			if len(history.Versions) > 0 {
				v := history.Versions[0]
				if oldestPath == "" || v.Timestamp.Before(oldestTime) {
					oldestPath = path
					oldestVersion = v.Version
					oldestTime = v.Timestamp
				}
			}
		}

		if oldestPath != "" {
			if history, ok := dm.histories[oldestPath]; ok {
				dm.removeVersion(history, oldestVersion)
			}
		} else {
			break
		}
	}
}

// removeVersion removes a specific version from history.
func (dm *DocumentMemory) removeVersion(history *NodeHistory, version int) {
	idx, ok := history.VersionIndex[version]
	if !ok {
		return
	}

	// Update memory usage
	if idx < len(history.Versions) {
		dm.memoryUsage -= estimateSize(history.Versions[idx].Value)
	}

	// Remove from versions array
	history.Versions = append(history.Versions[:idx], history.Versions[idx+1:]...)

	// Rebuild index
	history.VersionIndex = make(map[int]int)
	for i, v := range history.Versions {
		history.VersionIndex[v.Version] = i
	}

	dm.totalVersions--
}

// cleanupEmptyHistories removes paths with no versions.
func (dm *DocumentMemory) cleanupEmptyHistories() {
	var toDelete []string
	for path, history := range dm.histories {
		if len(history.Versions) == 0 {
			toDelete = append(toDelete, path)
		}
	}

	for _, path := range toDelete {
		delete(dm.histories, path)
	}
}

// compressVersions compresses a slice of versions using gzip.
func compressVersions(versions []NodeVersion) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	enc := gob.NewEncoder(gw)

	if err := enc.Encode(versions); err != nil {
		return nil, err
	}

	if err := gw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// estimateSize estimates the memory size of a value in bytes.
func estimateSize(v interface{}) int64 {
	if v == nil {
		return 0
	}

	// Use reflection to estimate size
	// This is a simplified estimation
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return int64(len(rv.String()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return 8
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return 8
	case reflect.Float32, reflect.Float64:
		return 8
	case reflect.Bool:
		return 1
	case reflect.Slice, reflect.Array:
		size := int64(0)
		for i := 0; i < rv.Len(); i++ {
			size += estimateSize(rv.Index(i).Interface())
		}
		return size + int64(unsafe.Sizeof(v))
	case reflect.Map:
		size := int64(0)
		for _, key := range rv.MapKeys() {
			size += estimateSize(key.Interface())
			size += estimateSize(rv.MapIndex(key).Interface())
		}
		return size + int64(unsafe.Sizeof(v))
	case reflect.Struct:
		return int64(unsafe.Sizeof(v))
	case reflect.Invalid, reflect.Uintptr, reflect.Complex64, reflect.Complex128,
		reflect.Chan, reflect.Func, reflect.Interface, reflect.Pointer, reflect.UnsafePointer:
		return int64(unsafe.Sizeof(v))
	}
	return int64(unsafe.Sizeof(v))
}

// GetMemoryStats returns current memory statistics.
func (dm *DocumentMemory) GetMemoryStats() map[string]interface{} {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	return map[string]interface{}{
		"total_versions":     dm.totalVersions,
		"memory_usage_bytes": dm.memoryUsage,
		"memory_usage_mb":    float64(dm.memoryUsage) / (1024 * 1024),
		"num_paths":          len(dm.histories),
		"num_compressed":     len(dm.compressedData),
		"timeline_events":    len(dm.timeline),
		"last_cleanup":       dm.lastCleanup,
	}
}
