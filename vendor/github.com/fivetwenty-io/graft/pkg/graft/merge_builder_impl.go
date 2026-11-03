package graft

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cppforlife/go-patch/patch"

	"github.com/fivetwenty-io/graft/internal/literal"
	"github.com/fivetwenty-io/graft/pkg/graft/interfaces"
	"github.com/fivetwenty-io/graft/pkg/graft/merger"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// mergeBuilderImpl implements the MergeBuilder interface.
type mergeBuilderImpl struct {
	engine         Engine
	ctx            context.Context
	docs           []Document
	pruneKeys      []string
	cherryPickKeys []string
	postProcessors []PostProcessor
	skipEvaluation bool
	goPatch        bool
	fallbackAppend bool
	trackHistory   bool
	arrayStrategy  ArrayMergeStrategy
	error          error                 // Stores any error from construction
	mergeMetadata  *merger.MergeMetadata // Accumulated metadata from merges

	// basePrune/baseSort remember whether any document that has
	// contributed to the accumulated merge result contained a
	// (( prune )) / (( sort ... )) marker. needsLegacyMerger reads them
	// in place of re-walking the whole accumulated base on every
	// overlay round. They are seeded by prepareFirstDocument and OR-ed
	// with each overlay's own walk in mergeIntoAtPath, so they can only
	// over-approximate (a marker later overwritten leaves its flag
	// set), which routes a merge to the legacy merger unnecessarily -
	// same output, since the legacy merger is the reference
	// implementation the simple path mirrors - and never
	// under-approximates: a marker can only enter the tree from a
	// document, and every contributing document is recorded here.
	basePrune bool
	baseSort  bool

	// priorCalcValues records, per canonical path string, the base value a
	// "(( calc <leading-op> ... ))" value-modification expression
	// overwrote during merge. Populated by
	// mergeValuesAtPath; consumed by applyEvaluation via
	// WithPriorCalcValues. nil for the overwhelming majority of merges that
	// never write this expression shape — no cost for any other document.
	priorCalcValues map[string]interface{}

	// runState is the operator state of the post-processing run in
	// progress: set by applyPostProcessing for a *DefaultEngine and
	// cleared when it returns. It reaches the engine only through the
	// context applyEvaluation hands to Evaluate, never through m.ctx, so
	// no context a post-processor receives carries a finished run's state.
	runState *operatorState
}

// Base sets the base document for the merge - position 0 in the builder's
// document list - replacing whatever previously occupied that slot,
// including a document supplied via engine.Merge(ctx, docs...). Calling
// Base more than once on the same chain replaces the previous base rather
// than accumulating; use Overlay/OverlayFile to add further documents.
//
// Like every other With*-style method on this builder, Base does not
// mutate the receiver: it returns a new builder holding a fresh document
// slice (copied, not aliased, from the receiver's), so two Base calls
// branching off the same starting builder produce independent results.
func (m *mergeBuilderImpl) Base(doc Document) MergeBuilder {
	if m.error != nil {
		return m // Propagate error
	}

	newBuilder := *m
	newDocs := make([]Document, len(m.docs))
	copy(newDocs, m.docs)
	if len(newDocs) == 0 {
		newDocs = append(newDocs, doc)
	} else {
		newDocs[0] = doc
	}
	newBuilder.docs = newDocs
	return &newBuilder
}

// Overlay appends one or more documents to the builder's document list, to
// be merged, in call order, on top of whatever is already queued (the base
// plus any earlier overlays).
//
// Overlay does not mutate the receiver: each call returns a new builder
// holding a fresh document slice sized to its own contents, so branching
// (m.Overlay(a) and m.Overlay(b) off the same m) does not alias.
func (m *mergeBuilderImpl) Overlay(docs ...Document) MergeBuilder {
	if m.error != nil {
		return m // Propagate error
	}
	if len(docs) == 0 {
		return m
	}

	newBuilder := *m
	newDocs := make([]Document, len(m.docs), len(m.docs)+len(docs))
	copy(newDocs, m.docs)
	newDocs = append(newDocs, docs...)
	newBuilder.docs = newDocs
	return &newBuilder
}

// OverlayFile loads each path via the engine's ParseFile - the same
// extension-based YAML/JSON/go-patch auto-detection and "-" == STDIN
// convention ParseFile documents - and appends the resulting documents as
// overlays, in path order.
//
// A load failure for any path does not panic and does not silently return
// an unusable builder: it produces a fresh error-carrying builder, the
// same convention DefaultEngine.MergeFiles/MergeReaders already establish
// for construction-time failures. Every other builder method
// short-circuits on that error, and Execute() reports it first, so
// engine.Merge(ctx, base).OverlayFile("missing.yml").WithPrune(...).Execute()
// surfaces the load failure exactly as if OverlayFile had been the last
// call before Execute().
func (m *mergeBuilderImpl) OverlayFile(paths ...string) MergeBuilder {
	if m.error != nil {
		return m // Propagate error
	}
	if len(paths) == 0 {
		return m
	}
	if m.engine == nil {
		// Defensive: every builder reachable from the public API carries a
		// non-nil engine (Engine.Merge/MergeFiles/MergeReaders always set
		// it). A handful of this package's own tests construct
		// &mergeBuilderImpl{} directly to exercise unrelated helpers, so
		// this path is not reachable through Engine, but it must still not
		// panic if it is ever hit.
		return &mergeBuilderImpl{ctx: m.ctx, error: errors.New("failed to load overlay file: no engine configured")}
	}

	docs := make([]Document, 0, len(paths))
	for _, path := range paths {
		doc, err := m.engine.ParseFile(path)
		if err != nil {
			return &mergeBuilderImpl{engine: m.engine, ctx: m.ctx, error: fmt.Errorf("failed to load overlay file: %w", err)}
		}
		if doc == nil {
			// ParseFile returns (nil, nil) for a blank/null/empty document
			// (its ParseYAML/ParseJSON contract); merge that as an empty
			// map, matching MergeFiles' treatment of the same case.
			doc = NewDocument(make(map[string]interface{}))
		}
		docs = append(docs, doc)
	}

	return m.Overlay(docs...)
}

// WithPrune adds keys to remove from the final output.
func (m *mergeBuilderImpl) WithPrune(keys ...string) MergeBuilder {
	if m.error != nil {
		return m // Propagate error
	}

	newBuilder := *m // Copy the builder
	newBuilder.pruneKeys = append(m.pruneKeys, keys...)
	return &newBuilder
}

// WithCherryPick specifies keys to keep in the final output.
func (m *mergeBuilderImpl) WithCherryPick(keys ...string) MergeBuilder {
	if m.error != nil {
		return m // Propagate error
	}

	newBuilder := *m // Copy the builder
	newBuilder.cherryPickKeys = append(m.cherryPickKeys, keys...)
	return &newBuilder
}

// WithPostProcessors appends processors to run, in Phase-then-Priority
// order, after evaluation, pruning, and cherry-picking (see
// applyPostProcessing and runPostProcessors). It adds to - rather than
// replaces - any processors the engine was constructed with via the
// package-level graft.WithPostProcessors EngineOption; both sets combine
// and are ordered together by Phase-then-Priority, not by which one
// registered a given processor. A nil entry in procs is ignored.
func (m *mergeBuilderImpl) WithPostProcessors(procs ...PostProcessor) MergeBuilder {
	if m.error != nil {
		return m // Propagate error
	}

	newBuilder := *m
	newBuilder.postProcessors = append(append([]PostProcessor(nil), m.postProcessors...), procs...)
	return &newBuilder
}

// SkipEvaluation disables operator evaluation after merging.
func (m *mergeBuilderImpl) SkipEvaluation() MergeBuilder {
	if m.error != nil {
		return m // Propagate error
	}

	newBuilder := *m // Copy the builder
	newBuilder.skipEvaluation = true
	return &newBuilder
}

// EnableGoPatch enables go-patch format parsing.
func (m *mergeBuilderImpl) EnableGoPatch() MergeBuilder {
	if m.error != nil {
		return m // Propagate error
	}

	newBuilder := *m // Copy the builder
	newBuilder.goPatch = true
	return &newBuilder
}

// FallbackAppend uses append instead of inline for arrays by default.
func (m *mergeBuilderImpl) FallbackAppend() MergeBuilder {
	if m.error != nil {
		return m // Propagate error
	}

	newBuilder := *m // Copy the builder
	newBuilder.fallbackAppend = true
	newBuilder.arrayStrategy = AppendArrays
	return &newBuilder
}

// WithArrayMergeStrategy sets how arrays are merged.
func (m *mergeBuilderImpl) WithArrayMergeStrategy(strategy ArrayMergeStrategy) MergeBuilder {
	if m.error != nil {
		return m // Propagate error
	}

	newBuilder := *m // Copy the builder
	newBuilder.arrayStrategy = strategy
	// Update fallbackAppend based on strategy
	if strategy == AppendArrays {
		newBuilder.fallbackAppend = true
	}
	return &newBuilder
}

// TrackHistory activates document-memory tracking for this merge chain.
// See the MergeBuilder interface doc comment (api.go) and the History
// interface doc comment (history.go) for what tracking actually records
// and its engine-wide (not per-merge) scope.
func (m *mergeBuilderImpl) TrackHistory() MergeBuilder {
	if m.error != nil {
		return m // Propagate error
	}

	newBuilder := *m // Copy the builder
	newBuilder.trackHistory = true
	return &newBuilder
}

// ensureHistoryTracking activates the engine's DocumentMemory before this
// merge's own recording sites (mergeDocuments, performSimpleMergeAtPath,
// performLegacyMerge) check tracker.IsEnabled(), so a merge chain that
// only calls TrackHistory() - without engine-level WithHistoryTracking/
// WithMemoryConfig - still gets recorded. Idempotent: DefaultEngine.
// EnableMemoryTracking either creates a new, enabled DocumentMemory or
// re-enables the existing one, so calling this on every Execute() is
// safe whether or not tracking is already active. Only *DefaultEngine
// exposes EnableMemoryTracking; a MergeBuilder built against any other
// Engine implementation leaves TrackHistory() unable to lazily activate
// tracking (the resulting Document's History() falls back to
// emptyHistory{} unless that Engine implementation happens to already
// have tracking enabled some other way).
func (m *mergeBuilderImpl) ensureHistoryTracking() {
	de, ok := m.engine.(*DefaultEngine)
	if !ok {
		return
	}
	de.EnableMemoryTracking()
}

// currentDocumentMemory returns the concrete *DocumentMemory behind the
// engine's memory tracker, or nil if there is no engine, no tracker, or
// the tracker is not a *DocumentMemory (interfaces.MemoryTracker's only
// implementation in this package, but GetMemoryTracker's return type is
// the interface, not the concrete type - see memory_interface.go).
func (m *mergeBuilderImpl) currentDocumentMemory() *DocumentMemory {
	if m.engine == nil {
		return nil
	}
	tracker := m.engine.GetMemoryTracker()
	if tracker == nil {
		return nil
	}
	dm, ok := tracker.(*DocumentMemory)
	if !ok {
		return nil
	}
	return dm
}

// attachHistory sets doc's history field to a live view over the
// engine's DocumentMemory, if tracking is active and doc is a *document
// (every Execute() return path in this file produces one; go-patch
// documents never reach here - see mergeDocuments' handling of
// IsGoPatchDocument). A no-op otherwise, matching Document.History()'s
// contract of returning emptyHistory{} rather than requiring every
// caller to nil-check first. Attachment happens regardless of whether
// this specific merge called TrackHistory(): DocumentMemory recording at
// merge_builder_impl.go's other call sites (performSimpleMergeAtPath,
// performLegacyMerge, mergeDocuments) is already gated only on
// tracker.IsEnabled(), not on m.trackHistory, so a document produced on
// an engine with WithHistoryTracking(true)/WithMemoryConfig set at
// construction gets its history attached too, even if this particular
// Execute() call never invoked TrackHistory().
func (m *mergeBuilderImpl) attachHistory(doc Document) {
	dm := m.currentDocumentMemory()
	if dm == nil || !dm.IsEnabled() {
		return
	}
	concrete, ok := doc.(*document)
	if !ok {
		return
	}
	concrete.history = newHistoryFromMemory(dm)
}

// Execute performs the merge operation.
//
//nolint:gocyclo // merge execution handles many edge cases and options
func (m *mergeBuilderImpl) Execute() (Document, error) {
	// Check for construction errors first
	if m.error != nil {
		return nil, m.error
	}

	// Check context cancellation
	select {
	case <-m.ctx.Done():
		return nil, m.ctx.Err()
	default:
	}

	// TrackHistory must activate tracking before any merge or
	// evaluation work starts: every recording site below (single-
	// document merger.Merger.Merge, mergeDocuments, and the evaluation
	// path applyPostProcessing eventually calls) checks the engine's
	// tracker.IsEnabled() as it goes, so enabling it after the fact
	// would silently lose this merge's own changes.
	if m.trackHistory {
		m.ensureHistoryTracking()
	}

	// start anchors ProcessMetadata.StartTime for any WithPostProcessors
	// processors run by applyPostProcessing below, and is the base every
	// mergeDuration passed to it is measured from.
	start := time.Now()

	// An empty document list still goes through applyPostProcessing (with
	// a zero merge duration and no evaluation to run) rather than
	// returning early: WithPostProcessors' doc comment, EngineOptions.
	// PostProcessors' doc comment, and options.md all promise registered
	// processors run "on every merge this engine executes", with no
	// carve-out for zero documents. Returning early here would run no
	// processor and attach no history for engine.Merge(ctx).Execute() or
	// engine.MergeFiles(ctx).Execute() with no paths, silently breaking
	// that promise and any TrackHistory()/engine-level tracking on such a
	// call.
	if len(m.docs) == 0 {
		return m.applyPostProcessing(NewDocument(make(map[string]interface{})), start, 0)
	}

	// Reject a cyclic document before any of the recursive tree walks below
	// run: hasArrayOperators/hasArraysWithMaps/hasPruneOperators/
	// hasSortOperators (called on every document just past this point, for
	// both the single- and multi-document paths) and deepCopyMap/
	// deepCopyValue (called during the merge itself) all recurse over
	// map[string]interface{}/[]interface{} without their own visited
	// tracking cost paid on every call; a single up-front walk here catches
	// a self-referencing document — buildable through the library API via
	// NewDocument(map[string]interface{}) with no validation of its own —
	// before any of them can overflow the stack on it.
	for i, doc := range m.docs {
		if IsGoPatchDocument(doc) {
			continue // go-patch documents carry patch.Ops, not raw map data
		}
		if data, ok := treeOf(doc).(map[string]interface{}); ok {
			if err := detectCyclicValue(data); err != nil {
				return nil, fmt.Errorf("document %d: %w", i, err)
			}
		}
	}

	// Handle single document case. A lone go-patch document (no base to
	// patch) falls through to mergeDocuments below, which patches an empty
	// map at that document's position, matching spruce's behavior for a
	// single-file go-patch invocation.
	if len(m.docs) == 1 && !IsGoPatchDocument(m.docs[0]) {
		// For single documents, we need to validate arrays even without merging
		// to match legacy behavior for Issue #172
		data, ok := treeOf(m.docs[0]).(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("document data is not a map")
		}

		// Check if we need to use the merger:
		// 1. If there are array-merge markers (append/prepend/merge/insert/delete/...)
		//    These are merge-phase constructs and must always be resolved, even
		//    under --skip-eval (which only skips the evaluator phase).
		// 2. If there are arrays with maps (for default key-merge/validation) -
		//    also a merge-phase concern, independent of --skip-eval.
		// 3. If there are prune operators AND we're not skipping evaluation -
		//    (( prune )) on a lone, never-overwritten value is resolved by the
		//    evaluator, so it must stay literal when evaluation is skipped.
		useArrayOperators := m.hasArrayOperators(data)
		hasArraysWithMaps := m.hasArraysWithMaps(data)
		hasPruneOps := m.hasPruneOperators(data) && !m.skipEvaluation

		if useArrayOperators || hasArraysWithMaps || hasPruneOps {
			// Process through merger for validation and/or array operators
			mergerInstance := &merger.Merger{
				AppendByDefault: m.fallbackAppend,
			}

			// Set memory tracker if available from the engine. Routed
			// through GetMemoryTracker() (which returns a true nil, not an
			// interface wrapping a nil *DocumentMemory) rather than a
			// *DefaultEngine type assertion, so this matches the two other
			// tracker-discovery sites in this file (prepareFirstDocument,
			// performLegacyMerge) instead of duplicating the lookup.
			if m.engine != nil {
				if tracker := m.engine.GetMemoryTracker(); tracker != nil && tracker.IsEnabled() {
					mergerInstance.SetMemoryTracker(tracker)
				}
			}

			// Create an empty base and merge our document into it
			// This triggers the array validation logic
			base := make(map[string]interface{})
			err := mergerInstance.Merge(base, data)
			if err != nil {
				// Convert merger.MultiError to graft.MultiError for consistent error formatting
				var mergerMultiErr merger.MultiError
				if errors.As(err, &mergerMultiErr) {
					graftMultiErr := &MultiError{}
					for _, e := range mergerMultiErr.Errors {
						graftMultiErr.Append(e)
					}
					return nil, graftMultiErr
				}
				return nil, err
			}

			return m.applyPostProcessing(NewDocument(base), start, time.Since(start))
		}

		// No special processing needed, just clone
		result := m.docs[0].Clone()
		return m.applyPostProcessing(result, start, time.Since(start))
	}

	// Merge multiple documents
	result, err := m.mergeDocuments()
	if err != nil {
		return nil, err
	}

	return m.applyPostProcessing(result, start, time.Since(start))
}

// mergeDocuments performs the actual document merging.
//
// Documents are processed in file order, exactly matching spruce's own merge
// loop: a regular (YAML) document merges onto the running result, and a
// go-patch document replaces the running result with ops.Apply(result) at
// the position it appears. A patch positioned between two regular overlays
// therefore only affects documents merged before it — later overlays keep
// merging on top of the patched result, instead of every patch being hoisted
// to the end of the whole file sequence.
func (m *mergeBuilderImpl) mergeDocuments() (Document, error) {
	var result map[string]interface{}
	haveResult := false

	// Fresh flags per merge chain: a builder can Execute more than once.
	m.basePrune = false
	m.baseSort = false

	for _, doc := range m.docs {
		// Check context cancellation during merge
		select {
		case <-m.ctx.Done():
			return nil, m.ctx.Err()
		default:
		}

		if IsGoPatchDocument(doc) {
			ops, ok := GetGoPatchOps(doc)
			if !ok {
				continue
			}
			if !haveResult {
				// A leading go-patch document (no base merged yet) patches an
				// empty map, matching spruce's `root := make(map[...]...)`
				// starting point.
				result = make(map[string]interface{})
				haveResult = true
			}
			patched, err := applyGoPatchToMap(result, ops)
			if err != nil {
				return nil, err
			}
			result = patched
			// A go-patch value can insert a literal "(( prune ))" /
			// "(( sort ... ))" string the flag bookkeeping (which only
			// sees merged documents) would otherwise miss, so re-walk
			// the patched result - only while the flags are still
			// false, and only for go-patch documents.
			if !m.basePrune {
				m.basePrune = m.hasPruneOperators(result)
			}
			if !m.baseSort {
				m.baseSort = m.hasSortOperators(result)
			}
			continue
		}

		overlayData, ok := treeOf(doc).(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("document data is not a map")
		}

		if !haveResult {
			baseResult, err := m.prepareFirstDocument(overlayData)
			if err != nil {
				return nil, err
			}
			result = baseResult
			haveResult = true
			continue
		}

		if err := m.mergeInto(result, overlayData); err != nil {
			// Check if this is a detailed merger error that should be preserved
			if isMergerError(err) {
				return nil, err
			}
			return nil, NewMergeError("failed to merge documents", err)
		}
	}

	if !haveResult {
		return NewDocument(make(map[string]interface{})), nil
	}

	return NewDocument(result), nil
}

// prepareFirstDocument resolves array-merge markers, arrays-with-maps
// default key-merge validation, and (( prune )) ghost-tracking on the first
// document that contributes to the merge result. This mirrors the
// merge-phase processing mergeInto applies to every later overlay, run once
// against an empty base since there is nothing yet to merge onto — the same
// treatment as spruce's initial `m.Merge(root, doc)` call against an empty
// root. Array-merge markers and arrays-with-maps are merge-phase constructs
// and must be resolved regardless of --skip-eval; prune operators are only
// ghost-tracked here when evaluation will run (a never-overwritten
// (( prune )) on a lone document is resolved by the evaluator, not by
// merge-phase ghosting).
func (m *mergeBuilderImpl) prepareFirstDocument(baseData map[string]interface{}) (map[string]interface{}, error) {
	// Record this document's prune/sort markers for needsLegacyMerger's
	// accumulated-base flags (see basePrune/baseSort).
	prune := m.hasPruneOperators(baseData)
	m.basePrune = m.basePrune || prune
	m.baseSort = m.baseSort || m.hasSortOperators(baseData)

	needsProcessing := m.hasArrayOperators(baseData) ||
		m.hasArraysWithMaps(baseData) ||
		m.hasMapValueDeleteMarkers(baseData) ||
		(prune && !m.skipEvaluation)
	if !needsProcessing {
		return deepCopyMap(baseData)
	}

	mergerInstance := &merger.Merger{
		AppendByDefault: m.fallbackAppend,
	}
	if m.engine != nil {
		if tracker := m.engine.GetMemoryTracker(); tracker != nil && tracker.IsEnabled() {
			mergerInstance.SetMemoryTracker(tracker)
		}
	}

	// Copy the first document before the merger stores its subtrees by
	// reference into the result: later overlay rounds and the evaluator
	// mutate the result in place, and those mutations must not reach
	// the caller's document. The marker-free path above copies for the
	// same reason.
	baseCopy, err := deepCopyMap(baseData)
	if err != nil {
		return nil, err
	}

	emptyBase := make(map[string]interface{})
	if err := mergerInstance.Merge(emptyBase, baseCopy); err != nil {
		return nil, m.convertMergerError(err)
	}

	m.collectMergeMetadata(mergerInstance)

	return emptyBase, nil
}

// needsLegacyMerger determines if the legacy merger should be used.
//
// Array-merge markers, arrays-with-maps default key-merge validation, and
// (( sort by X )) ghost-tracking are all merge-phase constructs that must be
// resolved identically whether or not --skip-eval is set (skip-eval only
// disables the evaluator phase, not the merger). Prune ghost-tracking is
// intentionally left unconditional here too: once a (( prune )) marker has
// been overwritten by a later document it must stay queued for removal
// regardless of --skip-eval, matching spruce's merge.go behavior.
//
// The base side of the decision - "does the accumulated result contain a
// prune or sort marker?" - is answered by the basePrune/baseSort flags
// rather than two full walks of the (ever-growing) result per overlay
// round. The flags OR in every contributing document's markers as it
// arrives, so they can over-approximate after a marker is overwritten -
// which only routes a merge to the legacy merger the simple path
// mirrors, never the other way around. As before, this records prior
// calc values and merges the whole pair through the legacy merger; the
// overlay's own markers are walked here, on the (small) overlay only.
func (m *mergeBuilderImpl) needsLegacyMerger(overlay map[string]interface{}) bool {
	overlayPrune := m.hasPruneOperators(overlay)
	m.basePrune = m.basePrune || overlayPrune
	overlaySort := m.hasSortOperators(overlay)
	m.baseSort = m.baseSort || overlaySort

	return m.hasArrayOperators(overlay) ||
		m.hasArraysWithMaps(overlay) ||
		m.hasMapValueDeleteMarkers(overlay) ||
		m.basePrune ||
		m.baseSort
}

// performLegacyMerge uses the legacy merger for complex merge operations.
func (m *mergeBuilderImpl) performLegacyMerge(base, overlay map[string]interface{}) error {
	mergerInstance := &merger.Merger{
		AppendByDefault: m.fallbackAppend,
	}

	if m.engine != nil {
		if tracker := m.engine.GetMemoryTracker(); tracker != nil && tracker.IsEnabled() {
			mergerInstance.SetMemoryTracker(tracker)
		}
	}

	// Merge into base in place, but hand the merger a deep copy of the
	// overlay: the merger stores overlay subtrees by reference into the
	// tree it builds, and later rounds (or the evaluator) mutate that
	// tree in place, so an uncopied overlay would let those mutations
	// reach the caller's overlay document. Copying the overlay instead
	// of the base flips the per-round cost from O(accumulated result) -
	// which this method used to deep-copy on every overlay - to
	// O(overlay). On error the half-merged base is discarded by every
	// caller, so in-place mutation needs no rollback copy.
	overlayCopy, err := deepCopyMap(overlay)
	if err != nil {
		return err
	}

	err = mergerInstance.Merge(base, overlayCopy)
	if err != nil {
		return m.convertMergerError(err)
	}

	m.collectMergeMetadata(mergerInstance)
	return nil
}

// convertMergerError converts merger errors to graft errors.
func (m *mergeBuilderImpl) convertMergerError(err error) error {
	var mergerMultiErr merger.MultiError
	if errors.As(err, &mergerMultiErr) {
		graftMultiErr := &MultiError{}
		for _, e := range mergerMultiErr.Errors {
			graftMultiErr.Append(e)
		}
		return graftMultiErr
	}
	return err
}

// collectMergeMetadata collects metadata from a merge operation.
func (m *mergeBuilderImpl) collectMergeMetadata(mergerInstance *merger.Merger) {
	metadata := mergerInstance.GetMetadata()
	if metadata == nil || (len(metadata.PrunePaths) == 0 && len(metadata.SortPaths) == 0) {
		return
	}

	if m.mergeMetadata == nil {
		m.mergeMetadata = &merger.MergeMetadata{
			SortPaths: make(map[string]string),
		}
	}
	m.mergeMetadata.PrunePaths = append(m.mergeMetadata.PrunePaths, metadata.PrunePaths...)
	for k, v := range metadata.SortPaths {
		m.mergeMetadata.SortPaths[k] = v
	}
}

// mergeInto merges overlay data into base data using legacy merger when needed.
func (m *mergeBuilderImpl) mergeInto(base, overlay map[string]interface{}) error {
	return m.mergeIntoAtPath(base, overlay, nil)
}

// mergeIntoAtPath is mergeInto with the canonical path (as a segment slice,
// root-relative) of `base`/`overlay` within the overall document. The path
// is threaded through purely to let mergeValuesAtPath record calc
// prior-values at the correct key; it changes no
// merge decision.
func (m *mergeBuilderImpl) mergeIntoAtPath(base, overlay map[string]interface{}, path []string) error {
	if m.needsLegacyMerger(overlay) {
		m.recordPriorCalcValuesUnder(base, overlay, path)
		return m.performLegacyMerge(base, overlay)
	}

	return m.performSimpleMergeAtPath(base, overlay, path)
}

// recordPriorCalcValuesUnder records the calc value-modification prior
// values for a subtree the legacy merger (pkg/graft/merger) is about to
// merge. That merger has no recording hook of its own, and it handles every
// document containing an array-merge marker, an array of maps, or a prune /
// sort marker — the shape most real manifests take — so without this pass
// the leading-operator form would silently evaluate against nothing and
// yield 0 for exactly those documents. It reads both maps and writes only
// m.priorCalcValues, so it changes no merge decision.
//
// Values inside lists are deliberately not recorded: their post-merge
// indices depend on merge decisions (append vs. merge-by-key) this pass runs
// before, so no key it could write would be certain to match ev.Here.
func (m *mergeBuilderImpl) recordPriorCalcValuesUnder(base, overlay map[string]interface{}, path []string) {
	for key, overlayValue := range overlay {
		baseValue, inBase := base[key]
		if !inBase {
			continue
		}

		childPath := make([]string, len(path)+1)
		copy(childPath, path)
		childPath[len(path)] = key

		switch ov := overlayValue.(type) {
		case map[string]interface{}:
			if bv, ok := baseValue.(map[string]interface{}); ok {
				m.recordPriorCalcValuesUnder(bv, ov, childPath)
			}
		case string:
			if isCalcModificationExpression(ov) {
				m.recordPriorCalcValue(childPath, baseValue)
			}
		}
	}
}

// performSimpleMergeAtPath merges overlay into base without the legacy
// merger, carrying the path context mergeIntoAtPath threads through; see
// its doc comment.
//
// path is the canonical no-"$" dotted history vocabulary (P0-2, matching
// pkg/graft/tree.Cursor.String()) of base/overlay's own location, one
// segment per ancestor map key, root-relative (nil at the top level).
// Recording calls join path with each iteration's own key rather than
// recording the bare key alone: this function recurses into nested maps via
// mergeValuesAtPath/mergeIntoAtPath, so without the prefix a change three
// levels deep would be recorded under its bare leaf name (e.g. "host"),
// indistinguishable from an unrelated sibling subtree's own "host" key, and
// would never match the same path's eval-phase entry (which always carries
// its full dotted location).
func (m *mergeBuilderImpl) performSimpleMergeAtPath(base, overlay map[string]interface{}, path []string) error {
	var memTracker interfaces.MemoryTracker
	if m.engine != nil {
		if tracker := m.engine.GetMemoryTracker(); tracker != nil && tracker.IsEnabled() {
			memTracker = tracker
		}
	}
	pathPrefix := strings.Join(path, ".")

	for key, overlayValue := range overlay {
		baseValue, exists := base[key]
		keyStr := key
		recordPath := keyStr
		if pathPrefix != "" {
			recordPath = pathPrefix + "." + keyStr
		}

		if !exists {
			copied, err := deepCopyValue(overlayValue)
			if err != nil {
				return err
			}
			base[key] = copied
			if memTracker != nil {
				_ = memTracker.RecordMergeChange(recordPath, nil, overlayValue, "add")
			}
			continue
		}

		// A fresh copy per key: path is shared across sibling iterations of
		// this loop, and append may otherwise reuse (and silently
		// overwrite) the same backing array across them.
		childPath := make([]string, len(path)+1)
		copy(childPath, path)
		childPath[len(path)] = keyStr

		merged, err := m.mergeValuesAtPath(baseValue, overlayValue, childPath)
		if err != nil {
			return err
		}
		base[key] = merged
		// Record the merge if value changed
		if memTracker != nil && !valuesEqual(baseValue, merged) {
			_ = memTracker.RecordMergeChange(recordPath, baseValue, merged, "merge")
		}
	}

	return nil
}

// mergeValuesAtPath merges two values based on their types, carrying the
// canonical path of base/overlay within the overall document; see
// mergeIntoAtPath's doc comment.
func (m *mergeBuilderImpl) mergeValuesAtPath(base, overlay interface{}, path []string) (interface{}, error) {
	// A nil overlay is a null VALUE to store, never a key deletion —
	// matching spruce and the legacy merger (merger/merge.go mergeMap).
	if overlay == nil {
		return nil, nil
	}

	// If base is nil, use overlay
	if base == nil {
		return deepCopyValue(overlay)
	}

	// Handle map merging
	baseMap, baseIsMap := base.(map[string]interface{})
	overlayMap, overlayIsMap := overlay.(map[string]interface{})

	if baseIsMap && overlayIsMap {
		result, err := deepCopyMap(baseMap)
		if err != nil {
			return nil, err
		}
		err = m.mergeIntoAtPath(result, overlayMap, path)
		if err != nil {
			return nil, err
		}
		return result, nil
	}

	// Handle array merging
	baseArray, baseIsArray := base.([]interface{})
	overlayArray, overlayIsArray := overlay.([]interface{})

	if baseIsArray && overlayIsArray {
		// NOTE: arrays containing merge markers (append/prepend/merge/insert/
		// delete/replace/inline) never reach this branch: needsLegacyMerger
		// recursively detects them in the enclosing map and routes the whole
		// base/overlay pair through performLegacyMerge (the real merger),
		// which resolves markers identically with or without --skip-eval.
		switch m.arrayStrategy {
		case AppendArrays:
			// Append arrays
			result := make([]interface{}, len(baseArray)+len(overlayArray))
			copy(result, baseArray)
			copy(result[len(baseArray):], overlayArray)
			return result, nil
		case PrependArrays:
			// Prepend arrays
			result := make([]interface{}, len(overlayArray)+len(baseArray))
			copy(result, overlayArray)
			copy(result[len(overlayArray):], baseArray)
			return result, nil
		case ReplaceArrays:
			return deepCopyValue(overlayArray)
		case InlineArrays:
			return m.mergeArraysInlineAtPath(baseArray, overlayArray, path)
		default:
			// InlineArrays is the zero value, so an unset strategy lands in
			// the InlineArrays case above; any future strategy value falls
			// back to the same spruce-default positional merge.
			return m.mergeArraysInlineAtPath(baseArray, overlayArray, path)
		}
	}

	// For different types or scalars, overlay replaces base. If the
	// incoming scalar is a "(( calc <leading-op> ... ))" value-modification
	// expression, the base value it is about to overwrite is the "existing
	// value" that expression needs at evaluation time and can no longer
	// find at ev.Here once this replacement happens — record it (spec
	// cluster A5 §5.3).
	if overlayStr, ok := overlay.(string); ok && isCalcModificationExpression(overlayStr) {
		m.recordPriorCalcValue(path, base)
	}

	return deepCopyValue(overlay)
}

// mergeArraysInlineAtPath merges two arrays positionally, matching spruce's
// default (inline) list merge: overlay elements merge over base elements
// index by index, base elements beyond the overlay's length are kept, and
// overlay elements beyond the base's length are appended. Only arrays free
// of merge markers, maps, prune, and sort reach this path — anything else
// routes through the legacy merger (see needsLegacyMerger) — so elements
// here are scalars or nested plain arrays.
func (m *mergeBuilderImpl) mergeArraysInlineAtPath(baseArray, overlayArray []interface{}, path []string) ([]interface{}, error) {
	merged := make([]interface{}, 0, max(len(baseArray), len(overlayArray)))
	for i := range overlayArray {
		elemPath := append(append([]string{}, path...), strconv.Itoa(i))
		if i < len(baseArray) {
			elem, err := m.mergeValuesAtPath(baseArray[i], overlayArray[i], elemPath)
			if err != nil {
				return nil, err
			}
			merged = append(merged, elem)
		} else {
			copied, err := deepCopyValue(overlayArray[i])
			if err != nil {
				return nil, err
			}
			merged = append(merged, copied)
		}
	}
	for i := len(overlayArray); i < len(baseArray); i++ {
		copied, err := deepCopyValue(baseArray[i])
		if err != nil {
			return nil, err
		}
		merged = append(merged, copied)
	}
	return merged, nil
}

// isCalcModificationExpression reports whether s is an unevaluated "(( calc
// <leading-op> ... ))" value-modification expression — either the raw form
// ("(( calc * 2 ))") or the quoted form the parser normalizes it to
// ("(( calc "* 2" ))") — as opposed to an ordinary calc expression like
// "(( calc "1 + 2" ))", which needs no prior value. A leading +, -, *, /, %,
// or ^ character (op_calc.go's own leading-operator set, mirrored by the
// parser's raw-substring capture — spec §5.2) immediately after "calc"
// identifies the shape.
func isCalcModificationExpression(s string) bool {
	return calcModificationPattern.MatchString(strings.TrimSpace(s))
}

var calcModificationPattern = regexp.MustCompile(`^\(\(\s*calc\s+"?[*/+%^-]`)

// recordPriorCalcValue records value as the prior value at the canonical
// path (dot-joined, matching tree.Cursor.String()) about to be overwritten
// by a calc value-modification expression. A root-level path (path
// unset — should not occur, since calc's leading-operator form always
// targets a specific key) is ignored rather than recorded under an empty
// key.
func (m *mergeBuilderImpl) recordPriorCalcValue(path []string, value interface{}) {
	if len(path) == 0 {
		return
	}
	if m.priorCalcValues == nil {
		m.priorCalcValues = make(map[string]interface{})
	}
	m.priorCalcValues[strings.Join(path, ".")] = value
}

// hasArrayOperators checks if a map contains arrays with merge operators.
func (m *mergeBuilderImpl) hasArrayOperators(data map[string]interface{}) bool {
	for _, value := range data {
		if m.valueHasArrayOperators(value) {
			return true
		}
	}
	return false
}

// valueHasArrayOperators checks if a value (or its children) contains array operators.
func (m *mergeBuilderImpl) valueHasArrayOperators(value interface{}) bool {
	switch v := value.(type) {
	case []interface{}:
		if m.arrayHasOperators(v) {
			return true
		}
	case map[string]interface{}:
		if m.hasArrayOperators(v) {
			return true
		}
	case map[interface{}]interface{}:
		// goccy decodes every mapping to map[string]interface{}, but values
		// from other decoders (yaml.v2 via go-patch, for one) can carry
		// non-string keys in this type, so the walk handles it too.
		for _, val := range v {
			if m.valueHasArrayOperators(val) {
				return true
			}
		}
	}
	return false
}

// arrayHasOperators checks if an array contains merge operators.
func (m *mergeBuilderImpl) arrayHasOperators(array []interface{}) bool {
	for _, item := range array {
		if str, ok := item.(string); ok {
			// Check for any array modification operators
			if strings.Contains(str, "(( append ))") ||
				strings.Contains(str, "(( prepend ))") ||
				strings.Contains(str, "(( replace ))") ||
				strings.Contains(str, "(( inline ))") ||
				strings.Contains(str, "(( merge") || // matches (( merge )) and (( merge on key ))
				strings.Contains(str, "(( insert") || // matches various insert forms
				strings.Contains(str, "(( delete") { // matches various delete forms
				return true
			}
		}
	}
	return false
}

// hasMapValueDeleteMarkers reports whether any map VALUE in data (at any
// map-nesting depth) is an argument-bearing (( delete ... )) marker. Such a
// document must route through the legacy merger so mergeMap's guard rejects
// it — the marker only has meaning as a list entry, and the simple path
// would otherwise copy it into the output as literal data. List elements
// are deliberately not scanned: delete markers there are the legal form
// (and lists containing maps already route to the legacy merger via
// hasArraysWithMaps).
func (m *mergeBuilderImpl) hasMapValueDeleteMarkers(data map[string]interface{}) bool {
	for _, value := range data {
		switch v := value.(type) {
		case string:
			if merger.IsDeleteMarkerWithArgs(v) {
				return true
			}
		case map[string]interface{}:
			if m.hasMapValueDeleteMarkers(v) {
				return true
			}
		}
	}
	return false
}

// hasArraysWithMaps checks if a map contains arrays with map elements (for merge-by-key detection).
func (m *mergeBuilderImpl) hasArraysWithMaps(data map[string]interface{}) bool {
	for _, value := range data {
		switch v := value.(type) {
		case []interface{}:
			for _, item := range v {
				if _, isMap := item.(map[string]interface{}); isMap {
					return true
				}
			}
		case map[string]interface{}:
			// Recursively check nested maps
			if m.hasArraysWithMaps(v) {
				return true
			}
		}
	}
	return false
}

// hasPruneOperators checks if a map contains prune operators.
func (m *mergeBuilderImpl) hasPruneOperators(data map[string]interface{}) bool {
	for _, value := range data {
		switch v := value.(type) {
		case string:
			// Check if it's a prune operator
			if strings.TrimSpace(v) == "(( prune ))" {
				return true
			}
		case map[string]interface{}:
			// Recursively check nested maps
			if m.hasPruneOperators(v) {
				return true
			}
		case []interface{}:
			// Check arrays for prune operators
			for _, item := range v {
				if str, ok := item.(string); ok && strings.TrimSpace(str) == "(( prune ))" {
					return true
				}
				// Also check if array contains maps with prune operators
				if mapItem, ok := item.(map[string]interface{}); ok {
					if m.hasPruneOperators(mapItem) {
						return true
					}
				}
			}
		}
	}
	return false
}

// hasSortOperators checks if a map contains sort operators.
func (m *mergeBuilderImpl) hasSortOperators(data map[string]interface{}) bool {
	for _, value := range data {
		switch v := value.(type) {
		case string:
			// Check if it's a sort operator
			trimmed := strings.TrimSpace(v)
			if strings.HasPrefix(trimmed, "(( sort") && strings.HasSuffix(trimmed, "))") {
				return true
			}
		case map[string]interface{}:
			// Recursively check nested maps
			if m.hasSortOperators(v) {
				return true
			}
		}
	}
	return false
}

// isMergerError checks if an error originated from the legacy merger
// (performLegacyMerge/convertMergerError), which always reports detailed,
// per-path failures (bad key-merge, out-of-bounds insert/delete index,
// duplicate insert entry, missing modification point, etc.) as a *MultiError.
// Those messages must reach the CLI unwrapped, matching spruce's own
// "N error(s) detected: - $.path: ..." formatting, rather than being
// flattened into a generic "failed to merge documents" wrapper.
func isMergerError(err error) bool {
	if err == nil {
		return false
	}
	var multiErr *MultiError
	return errors.As(err, &multiErr)
}

// applyPostProcessing applies evaluation, pruning, cherry-picking, and
// finally any WithPostProcessors processors, in that order. startTime and
// mergeDuration come from Execute's timing of the merge phase that ran
// before this call; both flow into the ProcessMetadata runPostProcessors
// builds for user-supplied processors.
func (m *mergeBuilderImpl) applyPostProcessing(doc Document, startTime time.Time, mergeDuration time.Duration) (Document, error) {
	result := doc

	// go-patch operations are applied inline, at their position in the file
	// sequence, by mergeDocuments; nothing left to do for them here.

	// This run's operator markers live in a run state of its own, which
	// applyEvaluation passes to the engine's Evaluate so the evaluation
	// continues this run rather than starting another. A fresh state per
	// call keeps a second Execute on the same builder from republishing
	// the first call's outputs. The state stays out of m.ctx: Evaluate
	// absorbs it into the engine before the post-processors run, and a
	// post-processor that calls Evaluate with the context it was given
	// must start a run of its own rather than continue this finished one.
	if de, ok := m.engine.(*DefaultEngine); ok {
		m.runState = de.forRun()
		defer func() { m.runState = nil }()
	}

	sortPaths := m.seedEngineState()

	// Apply evaluation if not skipped
	var evalDuration time.Duration
	if !m.skipEvaluation {
		evalStart := time.Now()
		evaluated, err := m.applyEvaluation(result)
		evalDuration = time.Since(evalStart)
		if err != nil {
			return nil, err
		}
		result = evaluated
	}

	allPruneKeys := m.collectPruneKeys(result)

	// Apply pruning AFTER evaluation so that grab operators can reference values before they're pruned
	if len(allPruneKeys) > 0 {
		// Temporarily set m.pruneKeys to all keys for the applyPruning method
		originalPruneKeys := m.pruneKeys
		m.pruneKeys = allPruneKeys
		var pruneErr error
		result, pruneErr = m.applyPruning(result)
		m.pruneKeys = originalPruneKeys // Restore original
		if pruneErr != nil {
			return nil, pruneErr
		}
	}

	// Apply queued (( sort by X )) list ordering AFTER all pruning and
	// BEFORE cherry-picking, matching spruce's post-processing order
	// (prune, then sort, then cherry-pick). This runs in both evaluation
	// modes: --skip-eval only disables the evaluator phases, not
	// merge-phase list ordering. Every failure of a queued sort is fatal
	// in spruce - an unresolvable (e.g. pruned-away) path, a non-list
	// value, and an unsortable list all abort the merge - so the error
	// propagates instead of being skipped.
	if len(sortPaths) > 0 {
		sorted, err := m.applySortPaths(result, sortPaths)
		if err != nil {
			return nil, err
		}
		result = sorted
	}

	// Apply cherry-picking AFTER evaluation and pruning
	if len(m.cherryPickKeys) > 0 {
		cherryPicked, err := m.applyCherryPicking(result)
		if err != nil {
			return nil, err
		}
		result = cherryPicked
	}

	// User-supplied post-processors (WithPostProcessors, either
	// EngineOption- or MergeBuilder-registered) run last, after
	// evaluation, pruning, and cherry-picking have all finished.
	processed, err := m.runPostProcessors(result, startTime, mergeDuration, evalDuration)
	if err != nil {
		return nil, err
	}

	// History is attached last, after post-processors have had their
	// chance to replace the Document entirely (runPostProcessors
	// rebuilds a fresh *document via NewDocumentFromInterface whenever
	// m.postProcessors is non-empty) - attaching any earlier would be
	// silently discarded by that rebuild.
	m.attachHistory(processed)

	return processed, nil
}

// seedEngineState hands the merge metadata's prune and sort paths to the
// run's operator state (see opState) and returns the queued (( sort by X ))
// paths. Those paths are captured and cleared here, before evaluation, because
// evaluation's deferred per-run state reset would wipe them before the
// post-prune sort runs.
func (m *mergeBuilderImpl) seedEngineState() map[string]string {
	if m.engine == nil {
		return nil
	}

	state := m.opState()

	if m.mergeMetadata != nil {
		for _, path := range m.mergeMetadata.PrunePaths {
			state.AddKeyToPrune(path)
		}
		for path, order := range m.mergeMetadata.SortPaths {
			state.AddPathToSort(path, order)
		}
	}

	sortPaths := state.GetPathsToSort()
	state.ResetPathsToSort()
	return sortPaths
}

// collectPruneKeys gathers every key to prune from result: the --prune
// flags, the keys the (( prune )) operator marked during evaluation, and
// the control-flow preprocessor's bindings.
//
// __graft_loop is a reserved top-level key pkg/graft/controlflow uses to
// materialize for-loop variables so generated bodies can (( grab )) them
// during normal evaluation. It must never reach evaluated output. It is
// added only when actually present (a cheap top-level map lookup, not a
// tree walk) rather than unconditionally: an unconditional append would
// flip applyPruning's "anything to prune?" check to true for every
// document, forcing a prune pass (and, under --skip-eval, its deep
// copy) over documents that never touched
// control flow and have no other reason to be pruned or copied. It is
// also gated on evaluation having run: under --skip-eval the loop bodies
// still hold unresolved (( grab __graft_loop... )) references, and
// dropping the bindings would strand every one of them in an intermediate
// meant to be merged again.
func (m *mergeBuilderImpl) collectPruneKeys(result Document) []string {
	keys := make([]string, len(m.pruneKeys))
	copy(keys, m.pruneKeys)

	if m.engine != nil {
		keys = append(keys, m.opState().GetKeysToPrune()...)
	}

	if m.skipEvaluation {
		return keys
	}

	resultData, ok := treeOf(result).(map[string]interface{})
	if !ok {
		return keys
	}
	if _, present := resultData["__graft_loop"]; present {
		keys = append(keys, "__graft_loop")
	}
	return keys
}

// applyGoPatchToMap applies a single set of go-patch operations to data at
// its position in the merge sequence (see mergeDocuments), converting to and
// from go-patch's map[interface{}]interface{} convention (yaml.v2 style)
// around the call.
func applyGoPatchToMap(data map[string]interface{}, ops patch.Ops) (map[string]interface{}, error) {
	converted := toInterfaceKeyMap(data)

	result, err := ops.Apply(converted)
	if err != nil {
		return nil, err
	}

	// Convert the result back to map[string]interface{} for the rest of the pipeline
	switch typed := result.(type) {
	case map[string]interface{}:
		return NormalizeMap(typed), nil
	case map[interface{}]interface{}:
		out := make(map[string]interface{}, len(typed))
		for k, v := range typed {
			out[fmt.Sprintf("%v", k)] = v
		}
		return NormalizeMap(out), nil
	default:
		return nil, fmt.Errorf("go-patch operations resulted in non-map data")
	}
}

// toInterfaceKeyMap deep-converts map[string]interface{} to
// map[interface{}]interface{} for go-patch compatibility.
func toInterfaceKeyMap(data map[string]interface{}) map[interface{}]interface{} {
	result := make(map[interface{}]interface{}, len(data))
	for k, v := range data {
		result[k] = toInterfaceKeyValue(v)
	}
	return result
}

func toInterfaceKeyValue(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		return toInterfaceKeyMap(val)
	case map[interface{}]interface{}:
		// Already interface keys, but recurse into values
		for k, v := range val {
			val[k] = toInterfaceKeyValue(v)
		}
		return val
	case []interface{}:
		for i, item := range val {
			val[i] = toInterfaceKeyValue(item)
		}
		return val
	default:
		return v
	}
}

// applySortPaths applies queued (( sort by X )) list ordering to the document
// through the same code spruce uses (Evaluator.SortPaths, a byte-exact port):
// the path is resolved with tree.ParseCursor/Resolve - NOT a map-only
// dotted-path walk, which errors on any path crossing a list index and would
// turn valid paths into false failures - and the first bad path aborts with
// spruce's exact error text (NotFoundError for an unresolvable path,
// TypeMismatchError for a map/scalar, the SortList error for an unsortable
// list). spruce reports at most one sort error per run; returning on the
// first failure preserves that.
func (m *mergeBuilderImpl) applySortPaths(doc Document, sortPaths map[string]string) (Document, error) {
	data, ok := treeOf(doc).(map[string]interface{})
	if !ok {
		if len(sortPaths) == 0 {
			return doc, nil
		}
		// A queued sort path can never resolve in a non-map root. No
		// merge-path input is known to reach this (the merged root is
		// always a map), but if one ever does — e.g. a post-processor
		// replacing the document — fail exactly like an unresolvable
		// sort path rather than silently dropping the queued markers,
		// which is the silent skip this function's error handling exists
		// to prevent. Report one path (the lexicographically first, for
		// determinism), matching the one-sort-error-per-run behavior.
		first := ""
		for path := range sortPaths {
			if first == "" || path < first {
				first = path
			}
		}
		return nil, MultiError{Errors: []error{tree.NotFoundError{Path: []string{first}}}}
	}

	if m.skipEvaluation {
		// Under --skip-eval the merge result can still alias the caller's
		// input documents, so sort a deep copy rather than mutating shared
		// slices. The evaluated path already owns its tree and sorts in
		// place. A cyclic document is an error, same as every other
		// deepCopyMap call site.
		copied, err := deepCopyMap(data)
		if err != nil {
			return nil, err
		}
		data = copied
		doc = NewDocument(copied)
	}

	ev := &Evaluator{Tree: data}
	if err := ev.SortPaths(sortPaths); err != nil {
		// Wrap in MultiError so the CLI renders spruce's
		// "N error(s) detected:\n - msg" framing with exit 2 instead of
		// the generic "Merge failed: ..." wrapper.
		return nil, MultiError{Errors: []error{err}}
	}
	return doc, nil
}

// applyPruning removes specified keys from the document.
//
// A prune path that does not resolve to anything (missing key,
// out-of-range index, or a named array-entry lookup at the final path
// segment) is not an error: removeKey returns nil for all of those
// cases, matching spruce's own --prune behavior (verified against the
// spruce binary), where an unresolved path is silently skipped. Any
// non-nil error from removeKey is therefore a genuine failure and is
// propagated rather than swallowed.
func (m *mergeBuilderImpl) applyPruning(doc Document) (Document, error) {
	data, ok := treeOf(doc).(map[string]interface{})
	if !ok {
		return doc, nil // Return unchanged if not a map
	}

	// Same ownership rule as the sort step below: under --skip-eval the
	// merge result can still alias the caller's input documents (e.g.
	// AppendArrays inserts overlay elements by reference), so prune a
	// deep copy. The evaluated path already owns its tree and prunes in
	// place, skipping a full-tree copy on the hot path.
	result := data
	if m.skipEvaluation {
		copied, err := deepCopyMap(data)
		if err != nil {
			return nil, err
		}
		result = copied
	}

	for _, key := range m.pruneKeys {
		m.removeKey(result, key)
	}

	return NewDocument(result), nil
}

// applyCherryPicking keeps only specified keys in the document.
//
//nolint:gocyclo // cherry-picking handles arrays, maps, and nested paths
func (m *mergeBuilderImpl) applyCherryPicking(doc Document) (Document, error) {
	data, ok := treeOf(doc).(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("document data is not a map")
	}
	result := make(map[string]interface{})

	// Group cherry-pick paths by their parent
	type arraySelection struct {
		indices map[int]bool
		names   map[string]bool
	}
	arrayPaths := make(map[string]*arraySelection)
	regularPaths := []string{}

	// First pass: categorize and group paths
	for _, keyPath := range m.cherryPickKeys {
		parts := strings.Split(keyPath, ".")

		if len(parts) >= 2 {
			parentPath := strings.Join(parts[:len(parts)-1], ".")
			lastPart := parts[len(parts)-1]

			parentValue, err := m.extractKey(data, parentPath)
			if err == nil {
				if arr, isArray := parentValue.([]interface{}); isArray {
					// This is an array - check if we can handle it
					if idx, isNum := isNumericIndex(lastPart); isNum {
						if idx >= 0 && idx < len(arr) {
							// Valid numeric index
							if arrayPaths[parentPath] == nil {
								arrayPaths[parentPath] = &arraySelection{
									indices: make(map[int]bool),
									names:   make(map[string]bool),
								}
							}
							arrayPaths[parentPath].indices[idx] = true
							continue
						}
					} else {
						// Try named lookup
						_, found := findNamedArrayEntry(arr, lastPart)
						if found {
							if arrayPaths[parentPath] == nil {
								arrayPaths[parentPath] = &arraySelection{
									indices: make(map[int]bool),
									names:   make(map[string]bool),
								}
							}
							arrayPaths[parentPath].names[lastPart] = true
							continue
						}
					}
				}
			}
		}

		// Not an array path or couldn't handle it
		regularPaths = append(regularPaths, keyPath)
	}

	// Second pass: extract array elements in their original order
	for parentPath, selection := range arrayPaths {
		parentValue, _ := m.extractKey(data, parentPath)
		arr, ok := parentValue.([]interface{})
		if !ok {
			continue // Skip if not an array
		}

		selectedItems := []interface{}{}

		// Iterate through the array in reverse order
		// This matches the expected test behavior where higher indices come first
		for i := len(arr) - 1; i >= 0; i-- {
			item := arr[i]
			// Check if this index is selected
			if selection.indices[i] {
				selectedItems = append(selectedItems, item)
				continue
			}

			// Check if this item has a name that's selected
			if len(selection.names) > 0 {
				for name := range selection.names {
					if entry, found := findNamedArrayEntry([]interface{}{item}, name); found {
						selectedItems = append(selectedItems, entry)
						break
					}
				}
			}
		}

		err := m.setKey(result, parentPath, selectedItems)
		if err != nil {
			return nil, err
		}
	}

	// Handle regular paths
	for _, path := range regularPaths {
		value, err := m.extractKey(data, path)
		if err != nil {
			// Special handling for array access with invalid indices
			parts := strings.Split(path, ".")
			if len(parts) >= 2 {
				parentPath := strings.Join(parts[:len(parts)-1], ".")
				parentValue, parentErr := m.extractKey(data, parentPath)
				if parentErr == nil {
					if _, isArray := parentValue.([]interface{}); isArray {
						// Parent is an array, but we couldn't access the element
						// Create a nested map structure: parent.key = entire array
						lastPart := parts[len(parts)-1]
						mapWithKey := map[string]interface{}{
							lastPart: parentValue,
						}
						err = m.setKey(result, parentPath, mapWithKey)
						if err != nil {
							return nil, err
						}
						continue
					}
				}
			}
			// For other errors, return the error
			return nil, err
		}
		err = m.setKey(result, path, value)
		if err != nil {
			return nil, err
		}
	}

	return NewDocument(result), nil
}

// applyEvaluation runs operator evaluation on the document.
func (m *mergeBuilderImpl) applyEvaluation(doc Document) (Document, error) {
	// Use the engine's evaluate method if available
	if m.engine != nil {
		evalCtx := m.ctx
		// Continue this merge's run (see applyPostProcessing). Only this
		// context, which never leaves Evaluate, carries the run state.
		if m.runState != nil {
			evalCtx = withRunState(evalCtx, m.runState)
		}
		// If we have cherry-pick keys, pass them to the engine for evaluation
		if len(m.cherryPickKeys) > 0 {
			evalCtx = WithCherryPickPaths(evalCtx, m.cherryPickKeys)
		}
		// Pass calc value-modification prior values recorded during merge,
		// if any.
		if len(m.priorCalcValues) > 0 {
			evalCtx = WithPriorCalcValues(evalCtx, m.priorCalcValues)
		}
		return m.engine.Evaluate(evalCtx, doc)
	}

	// Fallback: create basic evaluator (this should not happen in practice)
	data, ok := treeOf(doc).(map[string]interface{})
	if !ok {
		return nil, NewEvaluationError("", "document data is not a map", nil)
	}

	// Create evaluator
	evaluator := &Evaluator{
		Tree:        data,
		PriorValues: m.priorCalcValues,
		Sources:     GetSourceRefs(m.ctx),
	}

	// Run evaluation - pass cherry-pick keys as the "picks" parameter
	err := evaluator.Run(nil, m.cherryPickKeys)
	if err != nil {
		return nil, NewEvaluationError("", "failed to evaluate merged document", err)
	}

	return NewDocument(evaluator.Tree), nil
}

// Helper functions for key manipulation

//nolint:gocyclo // removeKey navigates nested maps and arrays with various path formats
func (m *mergeBuilderImpl) removeKey(data map[string]interface{}, keyPath string) {
	// Handle nested paths like "config.enabled" or "meta.list.1"
	if keyPath == "" {
		return
	}

	// Split path by dots, keeping a bracketed dotted node (as the
	// (( prune )) operator queues one) whole
	parts := tree.SplitPath(keyPath)
	if len(parts) == 1 {
		// Simple key
		delete(data, parts[0])
		return
	}

	// Navigate to the parent of the target, tracking the immediate container
	// (map or array) that holds `current` and the key/index used to reach it
	// at every step. Unlike a re-navigation from the root, this walk covers
	// map segments *and* array segments (numeric index or named entry) at
	// every position, so the final-segment array branch below can splice the
	// target array back into whatever holds it — a map field, or a slot in
	// an outer array — for paths like "jobs.0.networks.1" or
	// "jobs.web.networks.1" that pass through an array before the final
	// segment.
	var current interface{} = data
	var parentContainer interface{}
	var parentKey interface{} // string for a map parent, int for an array parent

	for i := 0; i < len(parts)-1; i++ {
		part := parts[i]

		switch v := current.(type) {
		case map[string]interface{}:
			value, exists := v[part]
			if !exists {
				// Path doesn't exist, nothing to remove
				return
			}
			parentContainer, parentKey = v, part
			current = value
		case []interface{}:
			// Handle array index or named entry
			if idx, isNum := isNumericIndex(part); isNum {
				// Numeric index
				if idx < 0 || idx >= len(v) {
					// Index out of bounds, nothing to remove
					return
				}
				parentContainer, parentKey = v, idx
				current = v[idx]
			} else {
				// Named entry lookup
				idx, entry, found := findNamedArrayEntryWithIndex(v, part)
				if !found {
					// Named entry not found, nothing to remove
					return
				}
				parentContainer, parentKey = v, idx
				current = entry
			}
		default:
			// current is a scalar (or other non-container) but the path has
			// more segments to traverse: the target doesn't exist under it.
			// Matches spruce, where tree.Cursor.Resolve returns an error in
			// this situation and the caller silently skips the prune.
			return
		}
	}

	// Now remove the final key/index
	finalPart := parts[len(parts)-1]

	switch parent := current.(type) {
	case map[string]interface{}:
		// Simple map key deletion
		delete(parent, finalPart)
	case []interface{}:
		// Array index deletion. spruce's own --prune only removes array
		// entries addressed by a numeric index at the final path segment;
		// a bare named final segment (e.g. "items.beta") is a no-op there
		// too (verified against the spruce binary: named lookup is only
		// used by spruce when navigating *through* an array to reach a
		// deeper key, never as the final delete target). Mirror that for
		// the bare-name case, which has a real spruce behavior to stay
		// compatible with.
		//
		// A "field=value" predicate final segment (e.g.
		// "servers.name=secondary") is different: spruce has no predicate
		// syntax at all, so there is no spruce
		// behavior to preserve, and the whole point of wiring predicates
		// into --cherry-pick/--prune is that they resolve to a specific
		// array entry — so a predicate final segment does resolve to a
		// delete index, via the same tree.PredicateFind the resolver uses.
		index, isNum := isNumericIndex(finalPart)
		if !isNum {
			if field, value, isPredicate := tree.ParsePredicateSegment(finalPart); isPredicate {
				if _, idx, found := tree.PredicateFind(parent, field, value); found && idx < uint64(len(parent)) {
					index, isNum = int(idx), true // #nosec G115 -- idx < uint64(len(parent)) in the enclosing condition
				}
			}
		}
		if !isNum {
			return
		}
		if index < 0 || index >= len(parent) {
			// Index out of bounds, nothing to remove. The spruce binary
			// panics on an out-of-range --prune index into an array; graft
			// intentionally diverges here and stays graceful, matching its
			// own no-op-on-unresolved-path behavior for every other prune
			// shape rather than crashing the whole merge.
			return
		}

		// Splice the element out and write the shortened array back into the
		// map field that holds it. spruce's own Prune() (evaluator.go) only
		// performs this write-back when the array's own container is a map
		// (its `reflect.TypeOf(s).Kind() == reflect.Map` check); an array
		// nested directly inside another array with no intervening map (e.g.
		// prune path "matrix.0.1" on `matrix: [[10,20,30],[40,50,60]]`) is a
		// silent no-op in spruce too (verified against the binary). Mirror
		// that restriction exactly — parentKey/parentContainer is populated
		// for every intermediate segment (map field, numeric array index, or
		// named array entry) so paths like "jobs.0.networks.1" and
		// "jobs.web.networks.1" reach a map parentContainer correctly; only
		// a bare array-of-arrays final segment stays unsupported, matching
		// spruce rather than silently going further than it does.
		if pc, ok := parentContainer.(map[string]interface{}); ok {
			if key, ok := parentKey.(string); ok {
				newArr := append(append([]interface{}{}, parent[:index]...), parent[index+1:]...)
				pc[key] = newArr
			}
		}
	default:
		// The path navigates down to a scalar (or other non-container)
		// and still expects a final key/index under it: nothing to
		// remove. Matches spruce's default case in Prune(), which logs
		// and moves on without deleting anything or raising an error.
		return
	}
}

// isNumericIndex checks if a string is a valid numeric array index.
func isNumericIndex(s string) (int, bool) {
	idx, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return idx, true
}

// findNamedArrayEntry searches for an entry in an array by checking identifier fields.
// The configured array-merge identifier key (DEFAULT_ARRAY_MERGE_KEY, "name" by
// default) is checked first and is authoritative; "name", "id", and "key" remain
// as fallbacks for entries that don't use the configured key, preserving prior
// behavior for documents that don't set the environment variable.
func findNamedArrayEntry(arr []interface{}, name string) (interface{}, bool) {
	_, entry, found := findNamedArrayEntryWithIndex(arr, name)
	return entry, found
}

// findNamedArrayEntryWithIndex is findNamedArrayEntry plus the position of
// the matched entry within arr. removeKey needs the index (not just the
// entry) to splice a shortened array back into its parent container when
// navigation passes through a named array entry before reaching the final
// prune target.
//
// A "field=value" path segment (e.g. the "servers.name=primary" of
// --cherry-pick/--prune) is a list predicate, not a name/id/key lookup —
// dispatched to tree.PredicateFind so cherry-pick and prune match the exact
// same first-match, list-containers-only semantics as (( grab ... ))
// predicate resolution, carried into this path
// navigator, which used to hand-roll its own name-field-only search and
// silently fail on predicate syntax).
func findNamedArrayEntryWithIndex(arr []interface{}, name string) (int, interface{}, bool) {
	if field, value, isPredicate := tree.ParsePredicateSegment(name); isPredicate {
		entry, idx, found := tree.PredicateFind(arr, field, value)
		if !found || idx >= uint64(len(arr)) {
			return -1, nil, false
		}
		return int(idx), entry, true // #nosec G115 -- idx >= uint64(len(arr)) returned early above
	}

	configuredKey := merger.GetIdentifierKey()
	identifierKeys := make([]string, 0, 4)
	identifierKeys = append(identifierKeys, configuredKey)
	for _, fallback := range []string{"name", "id", "key"} {
		if fallback != configuredKey {
			identifierKeys = append(identifierKeys, fallback)
		}
	}

	for i, entry := range arr {
		// Only check map entries
		if v, ok := entry.(map[string]interface{}); ok {
			for _, idKey := range identifierKeys {
				if val, exists := v[idKey]; exists && fmt.Sprintf("%v", val) == name {
					return i, entry, true
				}
			}
		}
	}

	return -1, nil, false
}

func (m *mergeBuilderImpl) extractKey(data map[string]interface{}, keyPath string) (interface{}, error) {
	// Handle nested paths like "config.enabled"
	if keyPath == "" {
		return nil, NewValidationError("empty key path")
	}

	// Split path by dots
	parts := strings.Split(keyPath, ".")

	// Navigate through the structure
	var current interface{} = data
	for i, part := range parts {
		switch v := current.(type) {
		case map[string]interface{}:
			value, exists := v[part]
			if !exists {
				return nil, NewValidationError(fmt.Sprintf("key not found: %s (missing segment '%s')", keyPath, part))
			}
			current = value
		case []interface{}:
			// Handle array access
			if idx, isNum := isNumericIndex(part); isNum {
				// Numeric index access
				if idx < 0 || idx >= len(v) {
					return nil, NewValidationError(fmt.Sprintf("array index out of bounds: %s (index %d, array length %d)", keyPath, idx, len(v)))
				}
				current = v[idx]
			} else {
				// Named entry lookup
				entry, found := findNamedArrayEntry(v, part)
				if !found {
					return nil, NewValidationError(fmt.Sprintf("named array entry not found: %s (looking for '%s')", keyPath, part))
				}
				current = entry
			}
		default:
			if i < len(parts)-1 {
				// Still have more path segments but current value is not navigable
				return nil, NewValidationError(fmt.Sprintf("cannot navigate path '%s' at segment %d: '%s' is not a map or array", keyPath, i, parts[i-1]))
			}
		}
	}

	return deepCopyValue(current)
}

func (m *mergeBuilderImpl) setKey(data map[string]interface{}, keyPath string, value interface{}) error {
	// Handle nested paths like "config.enabled"
	if keyPath == "" {
		return NewValidationError("empty key path")
	}

	// Split path by dots
	parts := strings.Split(keyPath, ".")

	// For simple keys, just set directly
	if len(parts) == 1 {
		data[keyPath] = value
		return nil
	}

	// Navigate to the parent map and set the final key
	current := data
	for i := 0; i < len(parts)-1; i++ {
		part := parts[i]

		if next, exists := current[part]; exists {
			switch v := next.(type) {
			case map[string]interface{}:
				current = v
			default:
				return NewValidationError(fmt.Sprintf("cannot set path '%s': segment '%s' is not a map", keyPath, part))
			}
		} else {
			// Create intermediate maps as needed
			newMap := make(map[string]interface{})
			current[part] = newMap
			current = newMap
		}
	}

	// Set the final value
	finalKey := parts[len(parts)-1]
	current[finalKey] = value
	return nil
}

// detectCyclicValue reports whether v — a document's raw
// map[string]interface{} data, or any value nested inside it — contains a
// map or list that (transitively) contains itself. It walks the same two
// container shapes deepCopyMap/deepCopyValue and this file's
// hasArrayOperators/hasArraysWithMaps/hasPruneOperators/hasSortOperators
// walkers recurse into, without allocating a copy, so Execute can reject a
// cyclic document in one pass before any of those unrelated walkers get a
// chance to run on it and overflow the stack themselves. Uses the same
// on-stack (not permanent-seen) pointer tracking as deepCopyMapOnStack for
// the same reason: a diamond-shaped, shared-but-acyclic structure must not
// be misreported as cyclic.
func detectCyclicValue(v interface{}) error {
	return detectCyclicValueOnStack(v, make(map[uintptr]struct{}))
}

func detectCyclicValueOnStack(v interface{}, onStack map[uintptr]struct{}) error {
	switch val := v.(type) {
	case literal.Value:
		// A literal is opaque to merge, but the value it wraps is still
		// copied and written out, so a cycle inside it is just as fatal.
		return detectCyclicValueOnStack(val.Value(), onStack)
	case map[string]interface{}:
		if len(val) == 0 {
			return nil
		}
		ptr := reflect.ValueOf(val).Pointer()
		if _, cyclic := onStack[ptr]; cyclic {
			return fmt.Errorf("cyclic reference detected: a map contains itself")
		}
		onStack[ptr] = struct{}{}
		defer delete(onStack, ptr)
		for _, item := range val {
			if err := detectCyclicValueOnStack(item, onStack); err != nil {
				return err
			}
		}
	case []interface{}:
		if len(val) == 0 {
			return nil
		}
		ptr := reflect.ValueOf(val).Pointer()
		if _, cyclic := onStack[ptr]; cyclic {
			return fmt.Errorf("cyclic reference detected: a list contains itself")
		}
		onStack[ptr] = struct{}{}
		defer delete(onStack, ptr)
		for _, item := range val {
			if err := detectCyclicValueOnStack(item, onStack); err != nil {
				return err
			}
		}
	}
	return nil
}

// Deep copy helpers
//
// Both functions track the map/list pointers currently on the recursion
// stack (not every pointer ever seen) so a self-referencing value — a map
// or list that transitively contains itself — is reported as an error
// instead of recursing forever and overflowing the goroutine stack. Using
// an on-stack set rather than a permanent "seen" set is required for
// correctness, not just style: a diamond-shaped structure (the same map or
// list instance legitimately reachable from two different sibling
// branches) must still copy successfully, and it will revisit that
// instance's pointer after the first branch has already finished with it
// and popped it back off the stack.
//
// Every caller in this file can carry an error (they already return one
// for other merge failures), so a cycle is always surfaced as an error
// here rather than silently truncated.

func deepCopyMap(src map[string]interface{}) (map[string]interface{}, error) {
	return deepCopyMapOnStack(src, make(map[uintptr]struct{}))
}

func deepCopyMapOnStack(src map[string]interface{}, onStack map[uintptr]struct{}) (map[string]interface{}, error) {
	// An empty (or nil) map cannot contain itself: there is nothing to
	// recurse into, so it never needs a stack entry. Skipping it also
	// avoids collisions between distinct empty/nil maps, which is Go
	// pointer arithmetic can share a zero-value representation for.
	if len(src) > 0 {
		ptr := reflect.ValueOf(src).Pointer()
		if _, cyclic := onStack[ptr]; cyclic {
			return nil, fmt.Errorf("cyclic reference detected while copying: a map contains itself")
		}
		onStack[ptr] = struct{}{}
		defer delete(onStack, ptr)
	}

	dst := make(map[string]interface{}, len(src))
	for key, value := range src {
		copied, err := deepCopyValueOnStack(value, onStack)
		if err != nil {
			return nil, err
		}
		dst[key] = copied
	}
	return dst, nil
}

func deepCopyValue(src interface{}) (interface{}, error) {
	return deepCopyValueOnStack(src, make(map[uintptr]struct{}))
}

func deepCopyValueOnStack(src interface{}, onStack map[uintptr]struct{}) (interface{}, error) {
	switch v := src.(type) {
	case map[string]interface{}:
		return deepCopyMapOnStack(v, onStack)
	case []interface{}:
		if len(v) > 0 {
			ptr := reflect.ValueOf(v).Pointer()
			if _, cyclic := onStack[ptr]; cyclic {
				return nil, fmt.Errorf("cyclic reference detected while copying: a list contains itself")
			}
			onStack[ptr] = struct{}{}
			defer delete(onStack, ptr)
		}
		dst := make([]interface{}, len(v))
		for i, item := range v {
			copied, err := deepCopyValueOnStack(item, onStack)
			if err != nil {
				return nil, err
			}
			dst[i] = copied
		}
		return dst, nil
	case literal.Value:
		// A copy of a literal is still a literal, with its own copy of
		// the value it wraps.
		inner, err := deepCopyValueOnStack(v.Value(), onStack)
		if err != nil {
			return nil, err
		}
		return literal.Wrap(inner), nil
	default:
		return v, nil // Primitives are copied by value
	}
}

// valuesEqual performs a simple equality check for values.
func valuesEqual(a, b interface{}) bool {
	// Handle nil cases
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	// For maps and slices, use fmt.Sprintf comparison as a simple deep equality check
	// This is not the most efficient but handles nested structures
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}
