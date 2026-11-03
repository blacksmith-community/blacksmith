// Package postprocess provides post-processing handlers for graft documents.
package postprocess

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// Phase indicates when a post-processor should run.
type Phase int

const (
	// PhaseEarly runs immediately after evaluation.
	PhaseEarly Phase = iota

	// PhaseNormal runs during standard post-processing.
	PhaseNormal

	// PhaseLate runs just before output.
	PhaseLate
)

// Processor name constants.
const (
	procNamePrune      = "prune"
	procNameInject     = "inject"
	procNameCherryPick = "cherry-pick"
)

// defaultPruneMarker is the value an evaluated (( prune )) leaves behind
// for this pipeline to remove.
const defaultPruneMarker = "(( prune ))"

// String returns a string representation of the phase.
func (p Phase) String() string {
	switch p {
	case PhaseEarly:
		return "early"
	case PhaseNormal:
		return "normal"
	case PhaseLate:
		return "late"
	default:
		return fmt.Sprintf("Phase(%d)", p)
	}
}

// Metadata provides context for post-processing.
type Metadata struct {
	// Sources lists the input file sources.
	Sources []string

	// MergeCount is the number of documents that were merged.
	MergeCount int

	// EvalCount is the number of operators that were evaluated.
	EvalCount int

	// StartTime is when processing began.
	StartTime time.Time

	// Duration is how long processing took.
	Duration time.Duration
}

// =============================================================================
// Marker Type Detection (avoids import cycle with operators package)
// =============================================================================

// isPruneMarkerType checks if a value is a PruneMarker by inspecting its type name.
// This avoids importing the operators package which would cause an import cycle.
func isPruneMarkerType(v interface{}) bool {
	if v == nil {
		return false
	}
	t := reflect.TypeOf(v)
	return t.Name() == "PruneMarker"
}

// getInjectMarkerSource extracts the Source field from an InjectMarker if present.
// This avoids importing the operators package which would cause an import cycle.
func getInjectMarkerSource(v interface{}) interface{} {
	if v == nil {
		return nil
	}

	// Check if it's a pointer
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}

	t := rv.Type()
	if t.Name() != "InjectMarker" {
		return nil
	}

	// Get the Source field
	sourceField := rv.FieldByName("Source")
	if !sourceField.IsValid() {
		return nil
	}

	return sourceField.Interface()
}

// PriorityProcessor extends Processor with a Priority method.
// This interface is used by the pipeline to determine execution order.
type PriorityProcessor interface {
	Processor
	// Priority returns the execution priority (lower runs first).
	Priority() int
}

// Processor defines the interface for post-processing operations.
// This is a simpler interface for raw value processing without Document wrapper.
type Processor interface {
	// Name returns the processor's name for identification.
	Name() string

	// Phase returns when this processor should run.
	Phase() Phase

	// Process performs the post-processing operation on the document.
	// The doc parameter is a map[string]interface{} representing the document.
	Process(ctx context.Context, doc interface{}, meta *Metadata) (interface{}, error)
}

// Pipeline manages the execution of multiple post-processors.
type Pipeline struct {
	mu         sync.RWMutex
	processors []Processor
}

// NewPipeline creates a new post-processing pipeline.
func NewPipeline() *Pipeline {
	return &Pipeline{
		processors: make([]Processor, 0),
	}
}

// Add adds a processor to the pipeline.
func (p *Pipeline) Add(proc Processor) {
	if proc == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.processors = append(p.processors, proc)
}

// Remove removes a processor by name.
func (p *Pipeline) Remove(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i, proc := range p.processors {
		if proc.Name() == name {
			p.processors = append(p.processors[:i], p.processors[i+1:]...)
			return true
		}
	}
	return false
}

// Get retrieves a processor by name.
func (p *Pipeline) Get(name string) (Processor, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, proc := range p.processors {
		if proc.Name() == name {
			return proc, true
		}
	}
	return nil, false
}

// Clear removes all processors from the pipeline.
func (p *Pipeline) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.processors = make([]Processor, 0)
}

// Count returns the number of processors in the pipeline.
func (p *Pipeline) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.processors)
}

// Len returns the number of processors in the pipeline (alias for Count).
func (p *Pipeline) Len() int {
	return p.Count()
}

// List returns the names of all processors in order.
func (p *Pipeline) List() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	names := make([]string, len(p.processors))
	for i, proc := range p.processors {
		names[i] = proc.Name()
	}
	return names
}

// Process runs all processors on the document in phase order.
func (p *Pipeline) Process(doc interface{}) (interface{}, error) {
	return p.ProcessWithContext(context.Background(), doc, nil)
}

// ProcessWithContext runs all processors with context and metadata.
func (p *Pipeline) ProcessWithContext(ctx context.Context, doc interface{}, meta *Metadata) (interface{}, error) {
	p.mu.RLock()
	processors := make([]Processor, len(p.processors))
	copy(processors, p.processors)
	p.mu.RUnlock()

	if len(processors) == 0 {
		return doc, nil
	}

	// Sort processors by phase and priority
	sorted := sortProcessorsByPriority(processors)

	// Run each processor in order
	result := doc
	for _, proc := range sorted {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		var err error
		result, err = proc.Process(ctx, result, meta)
		if err != nil {
			return result, fmt.Errorf("post-processor %q failed: %w", proc.Name(), err)
		}
	}

	return result, nil
}

// sortProcessorsByPriority sorts processors by phase and priority.
func sortProcessorsByPriority(processors []Processor) []Processor {
	sorted := make([]Processor, len(processors))
	copy(sorted, processors)

	sort.SliceStable(sorted, func(i, j int) bool {
		// First sort by phase
		if sorted[i].Phase() != sorted[j].Phase() {
			return sorted[i].Phase() < sorted[j].Phase()
		}

		// Then by priority if available
		pi := getProcessorPriority(sorted[i])
		pj := getProcessorPriority(sorted[j])
		return pi < pj
	})

	return sorted
}

// getProcessorPriority returns the priority of a processor.
func getProcessorPriority(proc Processor) int {
	if pp, ok := proc.(interface{ Priority() int }); ok {
		return pp.Priority()
	}
	// Default priority based on phase
	switch proc.Phase() {
	case PhaseEarly:
		return 0
	case PhaseNormal:
		return 50
	case PhaseLate:
		return 100
	default:
		return 50
	}
}

// DefaultPipeline creates a pipeline with the default processors.
// By default, it includes inject and prune processors.
func DefaultPipeline() *Pipeline {
	p := NewPipeline()
	p.Add(NewInjectProcessor())
	p.Add(NewPruneProcessor())
	return p
}

// FullPipeline creates a pipeline with all standard processors.
// This includes inject, prune, cherry-pick (empty), and sort (disabled).
func FullPipeline() *Pipeline {
	p := NewPipeline()
	p.Add(NewInjectProcessor())
	p.Add(NewPruneProcessor())
	p.Add(NewCherryPickProcessor(nil))
	p.Add(NewKeySorter(false))
	return p
}

// =============================================================================
// Built-in Processors
// =============================================================================

// PruneProcessor removes fields marked with (( prune )) or PruneMarker.
type PruneProcessor struct {
	// PruneMarker is the string that marks a value for pruning.
	// Default is "(( prune ))".
	PruneMarker string
}

// NewPruneProcessor creates a new PruneProcessor.
func NewPruneProcessor() *PruneProcessor {
	return &PruneProcessor{
		PruneMarker: defaultPruneMarker,
	}
}

// Name returns the processor name.
func (p *PruneProcessor) Name() string {
	return procNamePrune
}

// Phase returns when the processor should run.
func (p *PruneProcessor) Phase() Phase {
	return PhaseEarly
}

// Priority returns the execution priority (lower runs first).
func (p *PruneProcessor) Priority() int {
	return 10
}

// Process removes pruned fields from the document.
func (p *PruneProcessor) Process(ctx context.Context, doc interface{}, meta *Metadata) (interface{}, error) {
	return p.prune(doc), nil
}

// PruneValue processes a single value and returns the pruned result.
func (p *PruneProcessor) PruneValue(v interface{}) (interface{}, error) {
	return p.prune(v), nil
}

// prune recursively removes pruned values.
//
//nolint:gocyclo // prune handles maps, arrays, and various marker types
func (p *PruneProcessor) prune(v interface{}) interface{} {
	// Check for PruneMarker from operators package (by type name to avoid import cycle)
	if isPruneMarkerType(v) {
		return nil
	}

	switch val := v.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{})
		for k, v := range val {
			if p.isPruned(v) || isPruneMarkerType(v) {
				continue
			}
			pruned := p.prune(v)
			// Skip if recursion returned nil for a prune marker
			if pruned != nil || (!p.isPruned(v) && !isPruneMarkerType(v)) {
				result[k] = pruned
			}
		}
		return result

	case []interface{}:
		result := make([]interface{}, 0, len(val))
		for _, elem := range val {
			if p.isPruned(elem) || isPruneMarkerType(elem) {
				continue
			}
			pruned := p.prune(elem)
			if pruned != nil || (!p.isPruned(elem) && !isPruneMarkerType(elem)) {
				result = append(result, pruned)
			}
		}
		return result

	default:
		return v
	}
}

// isPruned checks if a value is marked for pruning by string marker.
func (p *PruneProcessor) isPruned(v interface{}) bool {
	if s, ok := v.(string); ok {
		marker := p.PruneMarker
		if marker == "" {
			marker = defaultPruneMarker
		}
		return strings.TrimSpace(s) == marker
	}
	return false
}

// InjectProcessor expands inject responses into parent maps.
type InjectProcessor struct{}

// NewInjectProcessor creates a new InjectProcessor.
func NewInjectProcessor() *InjectProcessor {
	return &InjectProcessor{}
}

// Name returns the processor name.
func (p *InjectProcessor) Name() string {
	return procNameInject
}

// Phase returns when the processor should run.
func (p *InjectProcessor) Phase() Phase {
	return PhaseEarly
}

// Priority returns the execution priority (lower runs first).
func (p *InjectProcessor) Priority() int {
	return 5
}

// Process handles injection markers in the document.
func (p *InjectProcessor) Process(ctx context.Context, doc interface{}, meta *Metadata) (interface{}, error) {
	return p.processInject(doc), nil
}

// InjectValue processes a single value and returns the injected result.
func (p *InjectProcessor) InjectValue(v interface{}) (interface{}, error) {
	return p.processInject(v), nil
}

// processInject recursively processes inject markers.
func (p *InjectProcessor) processInject(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{})

		// First pass: collect all regular keys
		for k, v := range val {
			processed := p.processInject(v)

			// Check for InjectMarker from operators package (by type name to avoid import cycle)
			if source := getInjectMarkerSource(v); source != nil {
				// Merge source map into result
				if src, ok := source.(map[string]interface{}); ok {
					for sk, sv := range src {
						procSv := p.processInject(sv)
						result[sk] = procSv
					}
				}
				continue
			}

			// Check if this is an inject marker by key prefix
			if strings.HasPrefix(k, "<<") {
				// This key should have its value (a map) merged into parent
				if injectMap, ok := processed.(map[string]interface{}); ok {
					for ik, iv := range injectMap {
						result[ik] = iv
					}
				}
			} else {
				result[k] = processed
			}
		}

		return result

	case []interface{}:
		result := make([]interface{}, len(val))
		for i, elem := range val {
			result[i] = p.processInject(elem)
		}
		return result

	default:
		return v
	}
}

// KeySorter sorts map keys alphabetically.
type KeySorter struct {
	// Recursive indicates whether to sort keys in nested maps.
	Recursive bool
	// Enabled indicates whether sorting is active.
	Enabled bool
}

// NewKeySorter creates a new KeySorter.
func NewKeySorter(enabled bool) *KeySorter {
	return &KeySorter{
		Recursive: true,
		Enabled:   enabled,
	}
}

// Name returns the processor name.
func (p *KeySorter) Name() string {
	return "key-sorter"
}

// Phase returns when the processor should run.
func (p *KeySorter) Phase() Phase {
	return PhaseLate
}

// Priority returns the execution priority (lower runs first).
func (p *KeySorter) Priority() int {
	return 100
}

// Process sorts map keys alphabetically.
func (p *KeySorter) Process(ctx context.Context, doc interface{}, meta *Metadata) (interface{}, error) {
	if !p.Enabled {
		return doc, nil
	}
	// Note: Go maps don't maintain order. goccy's own encoder sorts map
	// keys lexicographically, and graft's encoder wrapper applies
	// spruce's key order instead.
	// We return a SortedMap wrapper that can be detected during serialization
	return p.sortKeys(doc), nil
}

// SortValue processes a single value and returns the sorted result.
func (p *KeySorter) SortValue(v interface{}) (interface{}, error) {
	if !p.Enabled {
		return v, nil
	}
	return p.sortKeys(v), nil
}

// sortKeys recursively processes the document to mark maps for sorted output.
func (p *KeySorter) sortKeys(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{})
		for k, v := range val {
			if p.Recursive {
				result[k] = p.sortKeys(v)
			} else {
				result[k] = v
			}
		}
		return &SortedMap{Data: result}

	case *SortedMap:
		if p.Recursive {
			result := make(map[string]interface{})
			for k, v := range val.Data {
				result[k] = p.sortKeys(v)
			}
			return &SortedMap{Data: result}
		}
		return val

	case []interface{}:
		result := make([]interface{}, len(val))
		for i, elem := range val {
			if p.Recursive {
				result[i] = p.sortKeys(elem)
			} else {
				result[i] = elem
			}
		}
		return result

	default:
		return v
	}
}

// SortedMap is a wrapper that indicates keys should be sorted during serialization.
type SortedMap struct {
	Data map[string]interface{}
}

// Keys returns the sorted keys.
func (m *SortedMap) Keys() []string {
	keys := make([]string, 0, len(m.Data))
	for k := range m.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Get retrieves a value by key.
func (m *SortedMap) Get(key string) (interface{}, bool) {
	v, ok := m.Data[key]
	return v, ok
}

// Range iterates over the map in sorted key order.
func (m *SortedMap) Range(fn func(key string, value interface{}) bool) {
	for _, k := range m.Keys() {
		if !fn(k, m.Data[k]) {
			break
		}
	}
}

// CherryPickProcessor extracts specific paths from the document.
type CherryPickProcessor struct {
	// Paths is the list of paths to extract.
	Paths []string
}

// NewCherryPickProcessor creates a new CherryPickProcessor.
func NewCherryPickProcessor(paths []string) *CherryPickProcessor {
	return &CherryPickProcessor{
		Paths: paths,
	}
}

// Name returns the processor name.
func (p *CherryPickProcessor) Name() string {
	return procNameCherryPick
}

// Phase returns when the processor should run.
func (p *CherryPickProcessor) Phase() Phase {
	return PhaseLate
}

// Priority returns the execution priority (lower runs first).
func (p *CherryPickProcessor) Priority() int {
	return 50
}

// Process extracts the specified paths from the document.
func (p *CherryPickProcessor) Process(ctx context.Context, doc interface{}, meta *Metadata) (interface{}, error) {
	if len(p.Paths) == 0 {
		return doc, nil
	}

	result := make(map[string]interface{})

	for _, path := range p.Paths {
		value := getPath(doc, path)
		if value != nil {
			setPath(result, path, value)
		}
	}

	return result, nil
}

// CherryPickValue extracts specified paths from a value.
func (p *CherryPickProcessor) CherryPickValue(v interface{}) (interface{}, error) {
	if len(p.Paths) == 0 {
		return v, nil
	}

	result := make(map[string]interface{})

	for _, path := range p.Paths {
		value := getPath(v, path)
		if value != nil {
			setPath(result, path, value)
		}
	}

	return result, nil
}

// SetPaths updates the paths to extract.
func (p *CherryPickProcessor) SetPaths(paths []string) {
	p.Paths = paths
}

// AddPath adds a path to extract.
func (p *CherryPickProcessor) AddPath(path string) {
	p.Paths = append(p.Paths, path)
}

// getPath retrieves a value at a dot-separated path.
func getPath(doc interface{}, path string) interface{} {
	if path == "" {
		return doc
	}

	parts := splitPath(path)
	current := doc

	for _, part := range parts {
		switch val := current.(type) {
		case map[string]interface{}:
			v, ok := val[part]
			if !ok {
				return nil
			}
			current = v

		case *SortedMap:
			v, ok := val.Data[part]
			if !ok {
				return nil
			}
			current = v

		case []interface{}:
			var idx int
			if _, err := fmt.Sscanf(part, "%d", &idx); err != nil {
				return nil
			}
			if idx < 0 || idx >= len(val) {
				return nil
			}
			current = val[idx]

		default:
			return nil
		}
	}

	return current
}

// setPath sets a value at a dot-separated path, creating intermediate maps as needed.
func setPath(doc map[string]interface{}, path string, value interface{}) {
	parts := splitPath(path)
	if len(parts) == 0 {
		return
	}

	current := doc
	for i := 0; i < len(parts)-1; i++ {
		part := parts[i]
		if _, ok := current[part]; !ok {
			current[part] = make(map[string]interface{})
		}
		if next, ok := current[part].(map[string]interface{}); ok {
			current = next
		} else {
			// Cannot traverse further
			return
		}
	}

	current[parts[len(parts)-1]] = value
}

// splitPath splits a dot-separated path into segments.
func splitPath(path string) []string {
	if path == "" {
		return nil
	}

	var result []string
	var current strings.Builder
	inBracket := false

	for _, r := range path {
		switch r {
		case '.':
			if !inBracket {
				if current.Len() > 0 {
					result = append(result, current.String())
					current.Reset()
				}
			} else {
				current.WriteRune(r)
			}
		case '[':
			if current.Len() > 0 {
				result = append(result, current.String())
				current.Reset()
			}
			inBracket = true
		case ']':
			if current.Len() > 0 {
				result = append(result, current.String())
				current.Reset()
			}
			inBracket = false
		default:
			current.WriteRune(r)
		}
	}

	if current.Len() > 0 {
		result = append(result, current.String())
	}

	return result
}

// PathPruner removes specific paths from the document.
type PathPruner struct {
	// Paths is the list of paths to remove.
	Paths []string
}

// NewPathPruner creates a new PathPruner.
func NewPathPruner(paths []string) *PathPruner {
	return &PathPruner{
		Paths: paths,
	}
}

// Name returns the processor name.
func (p *PathPruner) Name() string {
	return "path-pruner"
}

// Phase returns when the processor should run.
func (p *PathPruner) Phase() Phase {
	return PhaseLate
}

// Process removes the specified paths from the document.
func (p *PathPruner) Process(ctx context.Context, doc interface{}, meta *Metadata) (interface{}, error) {
	if len(p.Paths) == 0 {
		return doc, nil
	}

	result := deepCopy(doc)

	for _, path := range p.Paths {
		result = deletePath(result, path)
	}

	return result, nil
}

// deepCopy creates a deep copy of a value.
func deepCopy(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{})
		for k, v := range val {
			result[k] = deepCopy(v)
		}
		return result

	case []interface{}:
		result := make([]interface{}, len(val))
		for i, elem := range val {
			result[i] = deepCopy(elem)
		}
		return result

	default:
		return v
	}
}

// deletePath removes a value at a dot-separated path.
func deletePath(doc interface{}, path string) interface{} {
	parts := splitPath(path)
	if len(parts) == 0 {
		return doc
	}

	return deletePathRecursive(doc, parts)
}

// deletePathRecursive recursively deletes a path.
//
//nolint:gocyclo // recursively handles maps and arrays for path deletion
func deletePathRecursive(v interface{}, parts []string) interface{} {
	if len(parts) == 0 {
		return v
	}

	part := parts[0]
	remaining := parts[1:]

	switch val := v.(type) {
	case map[string]interface{}:
		if len(remaining) == 0 {
			// This is the final segment - delete it
			result := make(map[string]interface{})
			for k, v := range val {
				if k != part {
					result[k] = v
				}
			}
			return result
		}

		// Need to recurse
		result := make(map[string]interface{})
		for k, v := range val {
			if k == part {
				result[k] = deletePathRecursive(v, remaining)
			} else {
				result[k] = v
			}
		}
		return result

	case []interface{}:
		var idx int
		if _, err := fmt.Sscanf(part, "%d", &idx); err != nil {
			return v
		}
		if idx < 0 || idx >= len(val) {
			return v
		}

		if len(remaining) == 0 {
			// Delete this index
			result := make([]interface{}, 0, len(val)-1)
			for i, elem := range val {
				if i != idx {
					result = append(result, elem)
				}
			}
			return result
		}

		// Recurse into the element
		result := make([]interface{}, len(val))
		for i, elem := range val {
			if i == idx {
				result[i] = deletePathRecursive(elem, remaining)
			} else {
				result[i] = elem
			}
		}
		return result

	default:
		return v
	}
}

// TransformProcessor applies a custom transformation function.
type TransformProcessor struct {
	name      string
	phase     Phase
	transform func(ctx context.Context, doc interface{}, meta *Metadata) (interface{}, error)
}

// NewTransformProcessor creates a new TransformProcessor with a custom function.
func NewTransformProcessor(name string, phase Phase, fn func(ctx context.Context, doc interface{}, meta *Metadata) (interface{}, error)) *TransformProcessor {
	return &TransformProcessor{
		name:      name,
		phase:     phase,
		transform: fn,
	}
}

// Name returns the processor name.
func (p *TransformProcessor) Name() string {
	return p.name
}

// Phase returns when the processor should run.
func (p *TransformProcessor) Phase() Phase {
	return p.phase
}

// Process applies the custom transformation function.
func (p *TransformProcessor) Process(ctx context.Context, doc interface{}, meta *Metadata) (interface{}, error) {
	if p.transform == nil {
		return doc, nil
	}
	return p.transform(ctx, doc, meta)
}

// =============================================================================
// Utility Functions
// =============================================================================

// Flatten converts nested maps to dot-notation keys.
func Flatten(doc interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	flattenRecursive(doc, "", result)
	return result
}

// flattenRecursive recursively flattens a document.
func flattenRecursive(v interface{}, prefix string, result map[string]interface{}) {
	switch val := v.(type) {
	case map[string]interface{}:
		for k, v := range val {
			newKey := k
			if prefix != "" {
				newKey = prefix + "." + k
			}
			flattenRecursive(v, newKey, result)
		}

	case []interface{}:
		for i, elem := range val {
			newKey := fmt.Sprintf("%s[%d]", prefix, i)
			if prefix == "" {
				newKey = fmt.Sprintf("[%d]", i)
			}
			flattenRecursive(elem, newKey, result)
		}

	default:
		if prefix != "" {
			result[prefix] = v
		}
	}
}

// Unflatten converts dot-notation keys back to nested maps.
func Unflatten(flat map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for path, value := range flat {
		setPath(result, path, value)
	}
	return result
}

// NormalizeMapKeys recursively normalizes map keys (no-op for map[string]interface{}).
func NormalizeMapKeys(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{})
		for k, v := range val {
			result[k] = NormalizeMapKeys(v)
		}
		return result

	case []interface{}:
		result := make([]interface{}, len(val))
		for i, elem := range val {
			result[i] = NormalizeMapKeys(elem)
		}
		return result

	default:
		return v
	}
}
