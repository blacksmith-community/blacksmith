package graft

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fivetwenty-io/graft/internal/cache"
	"github.com/fivetwenty-io/graft/internal/config"
	"github.com/fivetwenty-io/graft/internal/features"
	"github.com/fivetwenty-io/graft/internal/metrics"
	"github.com/fivetwenty-io/graft/internal/parallel"
	"github.com/fivetwenty-io/graft/internal/yamlcodec"
	"github.com/fivetwenty-io/graft/log"
	"github.com/fivetwenty-io/graft/pkg/graft/interfaces"

	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// DefaultEngine is the default implementation of the Engine interface
// It provides all the core functionality needed by graft.
type DefaultEngine struct {
	// Configuration
	opts EngineOptions

	// Operator registry (clone of DefaultRegistry, plus engine-local overrides)
	registry       *UnifiedOperatorRegistry
	localOperators map[string]bool // tracks operators registered on this engine (not inherited)
	opMutex        sync.RWMutex

	// Operator state, accessed through the OperatorState interface
	operatorState

	// Metrics and monitoring
	metrics *EngineMetrics

	// Document memory for tracking history. memoryMutex guards the
	// documentMemory field itself (assigning/reading the pointer), not
	// DocumentMemory's own internals, which carry their own mu. Every
	// merge on a shared engine reaches EnableMemoryTracking via
	// mergeBuilderImpl.ensureHistoryTracking on Execute()'s hot path (not
	// only at construction, like every other engine field this package
	// guards with its own per-concern mutex - see opMutex, vaultMutex,
	// ipMutex, pruneMutex, sortMutex above), so two goroutines merging on
	// one engine can race on this field without it.
	documentMemory *DocumentMemory
	memoryMutex    sync.RWMutex

	// New infrastructure systems (all optional)

	// Features controls feature flags for the engine
	Features *features.FeatureFlags

	// Cache provides optional caching for operator results (nil if disabled)
	Cache cache.Cache

	// MetricsRegistry provides optional metrics collection (nil if disabled)
	MetricsRegistry *metrics.Registry

	// Config provides unified configuration management (nil uses defaults)
	Config *config.Config

	// Pool provides optional worker pool for parallel evaluation (nil if disabled)
	Pool *parallel.WorkerPool

	// backends is the custom-backend registry (C7, see backend.go). Only
	// explicitly registered backends live here - never eager adapters
	// for internal/backends/{vault,aws,nats}, see
	// DefaultEngine.RegisterBackend's design note. Guarded by opMutex,
	// the same lock RegisterOperator/GetOperator already take, rather
	// than adding a sixth per-concern mutex.
	backends      map[string]Backend
	backendRetry  map[string]RetryConfig
	backendCaches map[string]BackendCache
	auditLogger   AuditLogger
}

// EngineMetrics tracks engine performance metrics.
type EngineMetrics struct {
	OperatorCalls map[string]int64
	CacheHits     int64
	CacheMisses   int64
	VaultCalls    int64
	AWSCalls      int64
}

// defaultEngineOpts returns default EngineOptions for NewDefaultEngine.
func defaultEngineOpts() EngineOptions {
	return EngineOptions{
		EnableCache:    true,
		CacheSize:      10000,
		EnableParallel: false,
		MaxConcurrency: 4,
		DataflowOrder:  "alphabetical",
	}
}

// NewDefaultEngine creates a new default engine with default configuration.
func NewDefaultEngine() *DefaultEngine {
	opts := defaultEngineOpts()
	e := newEngineFromOptions(&opts)
	return e
}

// RegisterOperator registers a custom operator in the engine's registry clone.
// Returns an error if the operator was already explicitly registered on this engine
// (i.e. not just inherited from DefaultRegistry). Custom operators can shadow
// built-in operators that were inherited from DefaultRegistry.
func (e *DefaultEngine) RegisterOperator(name string, op Operator) error {
	e.opMutex.Lock()
	defer e.opMutex.Unlock()

	// Fail if operator was already locally registered on this engine
	if e.localOperators[name] {
		return fmt.Errorf("operator %s already registered", name)
	}

	// Preserve existing metadata if available (from DefaultRegistry clone); override implementation.
	var entry *UnifiedOperatorEntry
	if existing, exists := e.registry.Get(name); exists {
		entryCopy := *existing
		entryCopy.Implementation = op
		entry = &entryCopy
	} else {
		entry = &UnifiedOperatorEntry{
			Name:           name,
			Precedence:     PrecedenceCall,
			MinArgs:        0,
			MaxArgs:        -1,
			Phase:          EvalPhase,
			Implementation: op,
		}
	}
	if err := e.registry.Register(entry); err != nil {
		return err
	}
	e.localOperators[name] = true
	return nil
}

// GetOperator retrieves an operator by name from the engine's registry clone.
func (e *DefaultEngine) GetOperator(name string) (Operator, bool) {
	e.opMutex.RLock()
	defer e.opMutex.RUnlock()

	return e.registry.GetImplementation(name)
}

// Internal methods

func (e *DefaultEngine) createEvaluator(t map[string]interface{}) *Evaluator {
	here, _ := tree.ParseCursor("$")
	ev := &Evaluator{
		Tree:          t,
		Deps:          map[string][]tree.Cursor{},
		Here:          here,
		engine:        e,
		DataflowOrder: e.opts.DataflowOrder,
	}

	// Set memory tracker if available
	e.memoryMutex.RLock()
	dm := e.documentMemory
	e.memoryMutex.RUnlock()
	if dm != nil {
		ev.SetMemoryTracker(dm)
	}

	return ev
}

// GetMemoryTracker returns the memory tracker interface.
func (e *DefaultEngine) GetMemoryTracker() interfaces.MemoryTracker {
	e.memoryMutex.RLock()
	defer e.memoryMutex.RUnlock()
	if e.documentMemory == nil {
		return nil
	}
	return e.documentMemory
}

// forceParallel, when true, makes every phase use ev.RunPhaseParallel
// regardless of e's FeatureParallelEvaluation flag - used by
// EvaluateParallel (depgraph.go), which has already verified e.Pool is
// non-nil before calling this, so RunPhaseParallel's own "no pool: fall
// back to sequential" branch never triggers here. The default entry
// point, Evaluate, passes false and keeps today's flag-gated behavior
// unchanged.
//
//nolint:gocyclo // evaluation pipeline with multiple phases and post-processing is inherently complex
func (e *DefaultEngine) evaluate(ctx context.Context, ev *Evaluator, forceParallel bool) error {
	// Every run works on its own operator state, so runs sharing this
	// engine never see or clear one another's prune, sort, and static IP
	// markers. A merge passes the state it already seeded through the
	// context it hands this call alone (see mergeBuilderImpl's
	// applyEvaluation); any other Evaluate call gets a fresh one here.
	// The deferred finish runs on every exit path, including a panic. It
	// publishes the run's vault references, vault placeholders, deferred
	// paths, and pruned paths to the engine, and then clears the run's own
	// markers so a merge reading them back after this returns finds them
	// consumed, as before.
	run, finish := e.startRun(runStateFrom(ctx))
	rs := run.operatorState
	prunesRecorded := false
	defer func() { finish(prunesRecorded) }()

	// Operators reach their state through GetEngine(ev), so the evaluator
	// sees a wrapper that answers with the run's state.
	ev.engine = run

	// Check context cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// A non-empty REDACT environment variable forces vault, AWS, and NATS
	// operators to return the literal string "REDACTED" instead of making a
	// backend call, matching spruce's evaluator.go REDACT semantics. This
	// check must live on the DefaultEngine.Evaluate path (not only on the
	// legacy Evaluator.Run path) because Evaluate is what the CLI merge
	// path (MergeBuilder) actually calls. SetRedactMode(true) is
	// unconditional here, overriding whatever WithRedact/EngineOptions
	// constructed the engine with, same as the three SetSkip* calls below
	// already did before redact mode existed - this is what keeps
	// REDACT=1 byte-for-byte regardless of any --skip-<backend> flag also
	// given (those flags alone leave redactMode false, deferring instead;
	// see op_skip_defer.go). Only this run's copy of the settings
	// changes; the engine's own settings stay as constructed.
	if os.Getenv("REDACT") != "" {
		rs.SetSkipVault(true)
		rs.SetSkipAws(true)
		rs.SetSkipNats(true)
		rs.SetRedactMode(true)
	}

	// Record evaluation start time if metrics are enabled
	var startTime time.Time
	if e.IsFeatureEnabled(features.FeatureMetrics) && e.MetricsRegistry != nil {
		startTime = time.Now()
	}

	// Run evaluation phases. MergePhase errors are accumulated rather than
	// returned immediately, so ParamPhase and EvalPhase still run and their
	// errors are combined with any MergePhase errors into a single report -
	// this matches spruce's evaluator.go Run(), which appends MergePhase
	// errors to a running MultiError instead of short-circuiting on them.
	// ParamPhase errors still abort evaluation before EvalPhase runs and are
	// returned on their own (dropping any accumulated MergePhase errors),
	// matching spruce: once a required param is unresolved, downstream
	// EvalPhase operators can't be trusted to evaluate meaningfully, so
	// spruce doesn't bother combining prior merge errors into that report.
	var mergeErrs MultiError
	for _, phase := range []OperatorPhase{MergePhase, ParamPhase, EvalPhase} {
		// Check context cancellation before each phase
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var phaseErr error
		if forceParallel || (e.IsFeatureEnabled(features.FeatureParallelEvaluation) && e.Pool != nil) {
			phaseErr = ev.RunPhaseParallel(phase)
		} else {
			phaseErr = ev.RunPhase(phase)
		}
		if phaseErr != nil {
			// Record failure metric if enabled
			if e.IsFeatureEnabled(features.FeatureMetrics) && e.MetricsRegistry != nil {
				phaseName := phaseToString(phase)
				counter := e.MetricsRegistry.GetOrCreateCounter("graft_evaluation_errors_total", metrics.Labels{"phase": phaseName})
				counter.Inc()
			}

			switch phase {
			case MergePhase:
				mergeErrs.Append(phaseErr)
				continue
			case ParamPhase:
				return phaseErr
			case EvalPhase:
				mergeErrs.Append(phaseErr)
				return mergeErrs
			default:
				mergeErrs.Append(phaseErr)
				return mergeErrs
			}
		}
	}

	if len(mergeErrs.Errors) > 0 {
		return mergeErrs
	}

	// Record successful evaluation metrics if enabled
	if e.IsFeatureEnabled(features.FeatureMetrics) && e.MetricsRegistry != nil {
		duration := time.Since(startTime)
		counter := e.MetricsRegistry.GetOrCreateCounter("graft_evaluations_total", nil)
		counter.Inc()
		// Record duration as gauge (simplified histogram)
		gauge := e.MetricsRegistry.GetOrCreateGauge("graft_evaluation_duration_seconds", nil)
		gauge.Set(duration.Seconds())
	}

	// Post-processing: apply operator-level pruning
	prunePaths := rs.GetKeysToPrune()
	log.DEBUG("Engine: Found %d prune paths to process: %v", len(prunePaths), prunePaths)
	if len(prunePaths) > 0 {
		// Convert tree paths to Document paths and remove them
		doc := NewDocument(ev.Tree)
		cleanedPaths := make([]string, 0, len(prunePaths))
		for _, path := range prunePaths {
			// Remove the "$." prefix if present
			cleanPath := strings.TrimPrefix(path, "$.")
			log.DEBUG("Engine: Pruning path '%s' (cleaned: '%s')", path, cleanPath)
			doc = doc.Prune(cleanPath)
			cleanedPaths = append(cleanedPaths, cleanPath)
		}
		// Update the evaluator tree with the pruned document
		if pruned, ok := treeOf(doc).(map[string]interface{}); ok {
			ev.Tree = pruned
		}

		// Stash the paths this run actually pruned, in the same "$."-free
		// dotted form buildMergeHistorySteps' own flattened path keys use,
		// so history reporting (cmd/graft/merge_history.go) can tell a
		// path an operator (( prune )) removed from a path that merely
		// evaluated to an explicit YAML null - both otherwise leave the
		// evaluated tree with that key simply absent or nil respectively.
		// The run's prune markers are cleared by resetPerRunState's
		// defer before this function returns, so this snapshot, which
		// absorb publishes to the engine, is the only way a caller
		// after Evaluate() returns can see it. See
		// GetLastEvaluatedPrunedPaths.
		rs.pruneMutex.Lock()
		rs.lastEvaluatedPrunedPaths = cleanedPaths
		rs.pruneMutex.Unlock()
	} else {
		rs.pruneMutex.Lock()
		rs.lastEvaluatedPrunedPaths = nil
		rs.pruneMutex.Unlock()
	}
	prunesRecorded = true

	// Queued (( sort by X )) markers are NOT applied here: spruce sorts
	// after ALL pruning (operator markers and --prune flags alike) and
	// before cherry-picking, and the --prune flags are only applied by the
	// merge builder after this function returns. applyPostProcessing
	// (merge_builder_impl.go) captures the queued paths before evaluation
	// and applies them at the spruce-equivalent point in both evaluation
	// modes, treating every failure as fatal the way spruce does.

	return nil
}

// Implement Engine interface methods

// ParseYAML parses YAML data into a Document.
func (e *DefaultEngine) ParseYAML(data []byte) (Document, error) {
	// Handle empty data
	if len(data) == 0 {
		return nil, nil
	}

	// Keep only the first document, and fail one nested past 10,000
	// levels, before the control-flow expander or yamlprep's placeholder
	// rewrite tokenizes it, and before goccy parses it. See
	// FirstMergeDocument.
	data, err := FirstMergeDocument(data)
	if err != nil {
		var parseErr *GraftError
		if errors.As(err, &parseErr) {
			return nil, err
		}
		return nil, NewParseError(err.Error(), err)
	}

	// Expand (( if )), (( for )), (( while )), (( case )) control-flow
	// constructs into plain YAML before anything else touches the bytes.
	// A document with no control-flow markers is returned unchanged by
	// the expander, so this is a no-op for the overwhelming majority of
	// callers. See pkg/graft/controlflow_hook.go for why this is a
	// function-pointer hook rather than a direct import.
	// The expander evaluates operators best-effort while it expands, so
	// it gets its own run state like any other run, and absorb publishes
	// what those operators record once it finishes.
	if ControlFlowExpander != nil {
		expanded, cfErr := e.expandControlFlow(data)
		if cfErr != nil {
			return nil, NewParseError(fmt.Sprintf("control flow expansion failed: %s", cfErr.Error()), cfErr)
		}
		data = expanded
	}

	// Prepare and parse the document, then apply the YAML 1.1 boolean
	// conversions (yes/no/on/off -> bool) and strip the quoted-bool
	// protection markers in one walk. pkg/yaml decodes through the same
	// helper, so both read a document alike.
	genericResult, err := yamlcodec.DecodeGeneric(data, e.yamlCompat(), false)
	if err != nil {
		// Fold goccy's own error text (it carries line/column detail, the
		// same information spruce's underlying library reports) into the
		// message instead of dropping it, so a human debugging kit YAML
		// gets a usable location, not just "failed to parse YAML".
		// The message's plain text describes the failure without
		// goccy's source lines, and its detailed text keeps them.
		cause := plainYAMLError(err)
		return nil, NewParseError(fmt.Sprintf("failed to parse YAML: %s", cause.Error()), cause)
	}

	if genericResult == nil {
		return nil, nil
	}

	// Check that root is a map/hash. goccy decodes a mapping into
	// map[string]interface{}, even when its keys are not strings.
	switch result := genericResult.(type) {
	case map[string]interface{}:
		return NewDocument(result), nil
	case map[interface{}]interface{}:
		// goccy does not produce this itself, but a value that came from
		// another decoder can carry it, so its keys are stringified here.
		converted := make(map[string]interface{}, len(result))
		for k, v := range result {
			converted[fmt.Sprintf("%v", k)] = v
		}
		return NewDocument(converted), nil
	default:
		// The wording is spruce's, which prints the failed type assertion
		// from its own decoder for every root that is not a mapping.
		return nil, fmt.Errorf("Root of YAML document is not a hash/map: type assertion to map[interface]interface{} failed") //nolint:staticcheck // ST1005: spruce's capitalised wording
	}
}

// expandControlFlow runs ControlFlowExpander over data on its own run
// state, so the operators it evaluates neither read nor leave markers on
// the engine's shared state, and publishes what they record through
// absorb. The caller checks that ControlFlowExpander is set.
func (e *DefaultEngine) expandControlFlow(data []byte) ([]byte, error) {
	run, finish := e.startRun(nil)
	defer finish(false)
	return ControlFlowExpander(run, data)
}

// yamlCompat returns the YAML 1.1 compatibility settings ParseYAML applies:
// the engine's configured YAMLCompat (see WithYAMLCompat) if one was
// supplied, otherwise DefaultYAMLCompat().
func (e *DefaultEngine) yamlCompat() *YAMLCompat {
	if e.opts.YAMLCompat != nil {
		return e.opts.YAMLCompat
	}
	return DefaultYAMLCompat()
}

// ParseMultiDocYAML splits multi-document YAML (separated by "\n---\n") and
// parses each document. An empty leading document produced by a file that
// begins with the separator line is silently discarded.
func (e *DefaultEngine) ParseMultiDocYAML(data []byte) ([]Document, error) {
	rawDocs := bytes.Split(data, []byte("\n---\n"))

	// Strip empty leading doc if the file starts with a --- separator
	if len(rawDocs) > 0 && len(bytes.TrimSpace(rawDocs[0])) == 0 {
		rawDocs = rawDocs[1:]
	}

	docs := make([]Document, 0, len(rawDocs))
	for _, docBytes := range rawDocs {
		doc, err := e.ParseYAML(docBytes)
		if err != nil {
			return nil, err
		}
		if doc != nil {
			docs = append(docs, doc)
		}
	}
	return docs, nil
}

// ParseJSON parses JSON data into a Document.
func (e *DefaultEngine) ParseJSON(data []byte) (Document, error) {
	// Handle empty data
	if len(data) == 0 {
		return nil, nil
	}

	var result map[string]interface{}
	err := json.Unmarshal(data, &result)
	if err != nil {
		// See the matching comment in ParseYAML: fold the underlying
		// decode error into the message so its detail is not dropped.
		return nil, NewParseError(fmt.Sprintf("failed to parse JSON: %s", err.Error()), err)
	}

	if result == nil {
		return nil, nil
	}

	return NewDocument(result), nil
}

// ParseFile and ParseReader are implemented in parse_file.go.

// Merge creates a new merge builder for combining documents. Any
// PostProcessors this engine was constructed with (graft.WithPostProcessors)
// are copied onto the returned builder up front, so
// MergeBuilder.WithPostProcessors can add further processors for this one
// merge chain without mutating the engine's own configured set.
func (e *DefaultEngine) Merge(ctx context.Context, docs ...Document) MergeBuilder {
	if ctx == nil {
		ctx = context.Background()
	}

	return &mergeBuilderImpl{
		engine:         e,
		ctx:            ctx,
		docs:           docs,
		postProcessors: append([]PostProcessor(nil), e.opts.PostProcessors...),
	}
}

// MergeFiles loads paths via ParseFile and returns a MergeBuilder over the
// resulting documents, in path order (first is base, rest are overlays -
// same convention as Merge). A load failure (missing file, unreadable
// file, parse error) does not panic and does not abort immediately: it is
// captured on the returned builder's error field, which every With*
// method short-circuits on and Execute() reports first, so
// engine.MergeFiles(ctx, paths...).WithPrune(...).Execute() surfaces the
// load failure exactly as if it had happened inside Execute() itself.
func (e *DefaultEngine) MergeFiles(ctx context.Context, paths ...string) MergeBuilder {
	if ctx == nil {
		ctx = context.Background()
	}

	docs := make([]Document, 0, len(paths))
	for _, path := range paths {
		doc, err := e.ParseFile(path)
		if err != nil {
			return &mergeBuilderImpl{engine: e, ctx: ctx, error: fmt.Errorf("failed to load merge file %s: %w", path, err)}
		}
		if doc == nil {
			// ParseFile returns (nil, nil) for a blank/null/empty
			// document (its ParseYAML/ParseJSON contract); merge that as
			// an empty map, matching Merge's own treatment of a nil
			// Document and mergeAllDocs' equivalent CLI behavior.
			doc = NewDocument(make(map[string]interface{}))
		}
		docs = append(docs, doc)
	}

	return e.Merge(ctx, docs...)
}

// MergeReaders loads readers via ParseReader and returns a MergeBuilder
// over the resulting documents, in reader order. Error handling mirrors
// MergeFiles: a load failure is captured on the returned builder rather
// than panicking or returning early with a bare nil interface.
func (e *DefaultEngine) MergeReaders(ctx context.Context, readers ...io.Reader) MergeBuilder {
	if ctx == nil {
		ctx = context.Background()
	}

	docs := make([]Document, 0, len(readers))
	for i, r := range readers {
		doc, err := e.ParseReader(r)
		if err != nil {
			return &mergeBuilderImpl{engine: e, ctx: ctx, error: fmt.Errorf("failed to load merge reader %d: %w", i, err)}
		}
		if doc == nil {
			doc = NewDocument(make(map[string]interface{}))
		}
		docs = append(docs, doc)
	}

	return e.Merge(ctx, docs...)
}

// logDebugf reports msg to the engine's configured Logger (see WithLogger)
// at debug level. It is a no-op when no Logger was configured, which is
// the default.
func (e *DefaultEngine) logDebugf(format string, args ...interface{}) {
	if e.opts.Logger != nil {
		e.opts.Logger.Debug(fmt.Sprintf(format, args...))
	}
}

// Evaluate processes operators in a document. It resolves them in doc
// itself, so doc must not be used by another call while Evaluate runs.
// Each call works on an operator state of its own, so concurrent calls on
// one engine do not disturb one another, and when the call finishes its
// vault references, vault placeholders, deferred paths, and pruned paths
// are published to the engine's own state.
func (e *DefaultEngine) Evaluate(ctx context.Context, doc Document) (Document, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	e.logDebugf("Evaluate: starting evaluation")

	// Get the raw data
	data, ok := treeOf(doc).(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("document data is not a map")
	}

	// Create evaluator
	ev := e.createEvaluator(data)

	// Extract cherry-pick paths from context if present
	if cherryPickPaths := GetCherryPickPaths(ctx); len(cherryPickPaths) > 0 {
		ev.CherryPickPaths = cherryPickPaths
		ev.Only = cherryPickPaths // Also set the original field for backward compatibility
	}

	// Extract merge input references from context if present. Used only
	// to attribute an operator data-flow cycle to files and lines.
	if refs := GetSourceRefs(ctx); len(refs) > 0 {
		ev.Sources = refs
	}

	// Extract calc-modification prior values recorded during merge, if any.
	if priorValues := GetPriorCalcValues(ctx); len(priorValues) > 0 {
		ev.PriorValues = priorValues
	}

	// Run evaluation
	err := e.evaluate(ctx, ev, false)
	if err != nil {
		// ev.Tree is always populated by this point (createEvaluator set
		// it before evaluate() ran, and evaluate() only ever accumulates
		// errors onto it - it never discards or resets the tree itself),
		// so it is always the best partial result available for this
		// failure: every operator that already ran is resolved, and
		// every operator that failed still carries its own "(( ... ))"
		// text. See PartialEvaluationError's doc comment for why
		// wrapping here is safe for every existing caller.
		return nil, &PartialEvaluationError{Err: err, Tree: NewDocument(ev.Tree)}
	}

	// Return evaluated document
	return NewDocument(ev.Tree), nil
}

// Diff computes the differences between a and b using
// DefaultDiffOptions(). See DiffWithOptions.
func (e *DefaultEngine) Diff(a, b Document) DiffResult {
	return e.DiffWithOptions(a, b, DefaultDiffOptions())
}

// DiffWithOptions computes the differences between a and b using opts
// (nil selects DefaultDiffOptions()), via the package-level DiffDocuments
// (diff_changes.go). a and b are always graft Documents, whose RawData()
// is always a map[string]interface{}, so DiffDocuments' error return
// (surfaced only by Diff's own "diff not implemented for this type"
// default branch and a nil-document guard, diff.go/diff_changes.go) is
// unreachable for two non-nil Documents; the Engine method signatures
// (api.go) match the plan's target library API and stay error-free. A nil
// a or b -- a caller error, not a comparison result -- degrades to an
// empty DiffResult rather than panicking, consistent with this method
// having no error return to report it through.
func (e *DefaultEngine) DiffWithOptions(a, b Document, opts *DiffOptions) DiffResult {
	result, err := DiffDocuments(a, b, opts)
	if err != nil {
		return &diffResult{}
	}
	return result
}

// ToYAML evaluates doc's operators (see Evaluate) and converts the
// resulting document to YAML bytes via Document.ToYAML. Unlike
// Document.ToYAML, which serializes doc as-is, this always evaluates
// first - see Document.ToJSONIndent's doc comment (api.go) for the
// Document-level/Engine-level distinction that also applies here. Returns
// an error if doc is nil or evaluation fails; a nil doc's error does not
// come from Evaluate (which would otherwise panic dereferencing a nil
// Document), it is checked explicitly first.
//
// Mutates doc: like Evaluate, whose in-place behavior this inherits, doc
// is resolved in place, not left untouched with only a new Document
// returned. A caller that still needs doc's pre-evaluation state - e.g.
// to call ToYAML/ToJSON/ToJSONIndent more than once and compare, or to
// keep the original alongside the evaluated form - should pass
// doc.Clone() (a genuine deep copy) instead of doc.
func (e *DefaultEngine) ToYAML(doc Document) ([]byte, error) {
	if doc == nil {
		return nil, NewValidationError("ToYAML: document must be non-nil")
	}
	evaluated, err := e.Evaluate(context.Background(), doc)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate document: %w", err)
	}
	return evaluated.ToYAML()
}

// ToJSON evaluates doc's operators (see Evaluate) and converts the
// resulting document to JSON bytes via Document.ToJSON. See ToYAML's doc
// comment for the evaluate-then-serialize contract, the nil handling, and
// the doc-mutation caveat (pass doc.Clone() to avoid it) this shares with
// ToJSON/ToJSONIndent.
func (e *DefaultEngine) ToJSON(doc Document) ([]byte, error) {
	if doc == nil {
		return nil, NewValidationError("ToJSON: document must be non-nil")
	}
	evaluated, err := e.Evaluate(context.Background(), doc)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate document: %w", err)
	}
	return evaluated.ToJSON()
}

// ToJSONIndent evaluates doc's operators (see Evaluate) and converts the
// resulting document to indented JSON bytes via Document.ToJSONIndent,
// using indent as the per-level indentation string. See ToYAML's doc
// comment for the evaluate-then-serialize contract, the nil handling, and
// the doc-mutation caveat (pass doc.Clone() to avoid it) this shares with
// ToYAML/ToJSON.
func (e *DefaultEngine) ToJSONIndent(doc Document, indent string) ([]byte, error) {
	if doc == nil {
		return nil, NewValidationError("ToJSONIndent: document must be non-nil")
	}
	evaluated, err := e.Evaluate(context.Background(), doc)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate document: %w", err)
	}
	return evaluated.ToJSONIndent(indent)
}

// UnregisterOperator removes an operator from the engine's registry clone.
// Note: this removes from the clone only, not from DefaultRegistry.
func (e *DefaultEngine) UnregisterOperator(name string) error {
	e.opMutex.Lock()
	defer e.opMutex.Unlock()

	e.registry.mu.Lock()
	delete(e.registry.operators, name)
	e.registry.mu.Unlock()
	delete(e.localOperators, name)
	return nil
}

// ListOperators returns all available operators from the engine's registry clone.
func (e *DefaultEngine) ListOperators() []string {
	e.opMutex.RLock()
	defer e.opMutex.RUnlock()

	return e.registry.ListOperators()
}

// WithLogger sets a new logger (returns new engine instance).
func (e *DefaultEngine) WithLogger(logger Logger) Engine {
	// For now, return self as logging is not implemented yet
	return e
}

// WithVaultClient sets a new vault client (returns new engine instance).
func (e *DefaultEngine) WithVaultClient(client VaultClient) Engine {
	// For now, return self as custom vault client is not implemented yet
	return e
}

// WithAWSConfig sets AWS configuration (returns new engine instance).
func (e *DefaultEngine) WithAWSConfig(_ AWSConfig) Engine {
	// For now, return self as AWS config is not fully implemented yet
	return e
}

// UpdateOptions replaces the engine options wholesale and re-applies the
// skip flags derived from them. Any field not set on opts reverts to its
// zero value, including fields the engine was originally constructed with
// - unlike Configure, which applies opts as an incremental change over the
// engine's current configuration.
func (e *DefaultEngine) UpdateOptions(opts EngineOptions) {
	e.opts = opts
	e.SetSkipVault(opts.SkipVault)
	e.SetSkipAws(opts.SkipAws)
	e.SetSkipNats(opts.SkipNats)
}

// Configure applies opts as an incremental change over the engine's
// current configuration - a copy of the engine's existing EngineOptions
// with opts applied on top, so any field opts doesn't touch keeps its
// current value - validates the result, and, if valid, makes it the
// engine's new configuration.
//
// Configure validates fully before mutating any engine state, so a failed
// call leaves the engine's configuration exactly as it was: an invalid
// MaxConcurrency (negative) returns a *GraftError (see
// NewConfigurationError), and one or more invalid pending custom-operator
// registrations (an empty name, or a nil Operator - the same checks
// UnifiedOperatorRegistry.Register performs) return that check's plain
// (non-*GraftError) error, for the first invalid registration in
// sorted-name order - deterministically, rather than whichever pending
// operator Go's randomized map iteration happens to reach first.
//
// Once validation passes, Configure re-derives the engine's cache from the
// resulting configuration (rebuilding it with the new size/TTL, or
// removing it, as EnableCache/CacheSize/CacheTTL/CacheInstance dictate -
// skipped entirely if none of those, nor the FeatureCaching flag that
// gates EnableCache, changed), re-applies any WithTraceOutput/
// WithTraceLevel/WithDebugLogging change (see WithTraceOutput for the
// process-wide-sink caveat that also applies here), registers every
// pending custom operator (WithOperators/WithCustomOperator) not already
// registered on this engine, in the same sorted-name order used for
// validation, and keeps skipVault/skipAws/skipNats in sync with the
// resulting SkipVault/SkipAws/SkipNats fields. A previously built cache's
// contents are discarded when the cache is rebuilt: Configure does not
// attempt to carry cache entries forward across a resize. Because pending
// operators were already validated above, registration is expected to
// always succeed; if it somehow does not (its error is still returned),
// the rest of the configuration applied by this call has already taken
// effect - registration is the one step Configure cannot roll back.
//
// Configure rewrites configuration that every merge and evaluation reads,
// so it must not run while another call on this engine is in progress.
// Configure the engine before sharing it across goroutines.
func (e *DefaultEngine) Configure(opts ...Option) error {
	// Captured *before* the option loop runs (F14): some options (e.g.
	// WithCaching) mutate their *features.FeatureFlags in place via Set,
	// and when a Configure call supplies no WithFeatureFlags option,
	// newOpts.FeatureFlags below is carried forward as the *same pointer*
	// as e.Features - so reading e.IsFeatureEnabled after the loop would
	// see the option's own mutation already applied, making a
	// FeatureCaching flip invisible to the cache-affecting check further
	// down. wasCachingEnabled, a plain bool, has no such aliasing problem.
	wasCachingEnabled := e.IsFeatureEnabled(features.FeatureCaching)

	newOpts := e.opts
	// Carry the engine's already-resolved feature flags forward so a
	// Configure call that never touches WithFeatureFlags doesn't silently
	// reset them to nil (createEngineFromOptions only ever writes a
	// resolved *features.FeatureFlags to e.Features, not back to
	// e.opts.FeatureFlags, when the caller didn't supply one at
	// construction).
	if newOpts.FeatureFlags == nil {
		newOpts.FeatureFlags = e.Features
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&newOpts)
		}
	}

	if newOpts.MaxConcurrency < 0 {
		return NewConfigurationError("concurrency must be non-negative")
	}

	// Validate every pending custom-operator registration - in
	// deterministic sorted-name order (F12) - before mutating any engine
	// state (F4). A Configure call that combines a valid option (e.g.
	// WithSkipVault) with an invalid pending operator must not apply the
	// valid part and then fail: it must leave the engine's configuration
	// completely untouched.
	pendingOperatorNames := pendingOperatorNames(newOpts.CustomOperators, e.localOperators)
	if err := validatePendingOperators(pendingOperatorNames, newOpts.CustomOperators); err != nil {
		return err
	}

	// Validate every pending backend registration (WithBackend, and
	// transitively WithVault/WithVaultTarget/WithAWS/WithAWSTarget, all of
	// which populate newOpts.Backends) before mutating any engine state,
	// the same discipline applied to pending operators above. RegisterBackend
	// performs these same two checks itself, but only at registration time -
	// too late for Configure's all-or-nothing contract, since the feature
	// flag, retry, cache, and audit-logger fields below would already be
	// mutated by the time a bad backend surfaced.
	pendingBackendNames := backendRegistrationNames(newOpts.Backends)
	if err := validatePendingBackends(pendingBackendNames, newOpts.Backends); err != nil {
		return err
	}

	// Asked *before* engine state is mutated, while the outgoing values
	// are still readable; skipping the rebuild when nothing
	// cache-affecting changed avoids creating (and leaking) a cache we
	// don't need to.
	cacheAffectingChanged := e.cacheConfigChanged(&newOpts, wasCachingEnabled)

	e.opts = newOpts
	e.SetSkipVault(newOpts.SkipVault)
	e.SetSkipAws(newOpts.SkipAws)
	e.SetSkipNats(newOpts.SkipNats)

	if newOpts.FeatureFlags != nil {
		e.Features = newOpts.FeatureFlags
	}

	applyLogging(&newOpts)

	if cacheAffectingChanged {
		e.rederiveCache(&newOpts)
	}

	for _, name := range pendingOperatorNames {
		if err := e.RegisterOperator(name, newOpts.CustomOperators[name]); err != nil {
			return err
		}
	}

	return e.applyBackendOptions(&newOpts, pendingBackendNames)
}

// cacheConfigChanged reports whether this Configure call touches anything
// the cache is derived from. cache.NewCache starts a background
// cleanupLoop goroutine (internal/cache/shard.go) whenever CleanupInterval
// > 0 (the default); rebuilding on every Configure call - even ones that
// only touch a skip flag - orphaned that goroutine on every call, since
// the outgoing cache.Cache was never closed (F2). It must be called
// before e.opts is replaced, while the old values are still readable.
func (e *DefaultEngine) cacheConfigChanged(newOpts *EngineOptions, wasCachingEnabled bool) bool {
	return e.opts.CacheInstance != newOpts.CacheInstance ||
		e.opts.EnableCache != newOpts.EnableCache ||
		e.opts.CacheSize != newOpts.CacheSize ||
		e.opts.CacheTTL != newOpts.CacheTTL ||
		wasCachingEnabled != featureCachingEnabled(newOpts.FeatureFlags)
}

// rederiveCache rebuilds e.Cache from newOpts and closes the outgoing one
// if nothing else is using it. See the feature-flag-shadowing comment in
// createEngineFromOptions: EnableCache alone does not win over a disabled
// FeatureCaching flag, matching construction-time behavior.
func (e *DefaultEngine) rederiveCache(newOpts *EngineOptions) {
	outgoing := e.Cache

	switch {
	case newOpts.CacheInstance != nil:
		e.Cache = newOpts.CacheInstance
	case newOpts.EnableCache && e.IsFeatureEnabled(features.FeatureCaching):
		cacheOpts := []cache.Option{cache.WithMaxSize(newOpts.CacheSize)}
		if newOpts.CacheTTL > 0 {
			cacheOpts = append(cacheOpts, cache.WithTTL(newOpts.CacheTTL))
		}
		e.Cache = cache.NewCache(cacheOpts...)
	default:
		e.Cache = nil
	}

	closeOutgoingCache(outgoing, e.Cache)
}

// applyBackendOptions applies the backend registry fields, in the same
// order createEngineFromOptions applies them at construction: the feature
// flag, then retry/cache/audit-logger configuration, then the backends
// themselves - RegisterBackend reads e.backendRetry/e.backendCaches/
// e.auditLogger at registration time to build each backend's wrapper
// (registerBackendLocked), so those three must already reflect this
// call's values before any backend in names registers. Configure has
// already validated them, so RegisterBackend cannot fail here for the
// reasons validatePendingBackends checks; its error is still returned
// defensively rather than ignored.
func (e *DefaultEngine) applyBackendOptions(newOpts *EngineOptions, names []string) error {
	if newOpts.backendRegistryEnabled != nil {
		e.Features.Set(features.FeatureBackendRegistry, *newOpts.backendRegistryEnabled)
	}
	if newOpts.BackendRetryConfigs != nil {
		e.backendRetry = newOpts.BackendRetryConfigs
	}
	if newOpts.BackendCaches != nil {
		e.backendCaches = newOpts.BackendCaches
	}
	if newOpts.AuditLoggerInstance != nil {
		e.auditLogger = newOpts.AuditLoggerInstance
	}

	for _, name := range names {
		if err := e.RegisterBackend(newOpts.Backends[name]); err != nil {
			return err
		}
	}
	return nil
}

// backendRegistrationNames returns the names in backends, sorted, so
// Configure applies (and validates) pending backend registrations in a
// deterministic order - the same reasoning pendingOperatorNames documents
// for pending custom-operator registrations.
func backendRegistrationNames(backends map[string]Backend) []string {
	names := make([]string, 0, len(backends))
	for name := range backends {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// validatePendingBackends validates every backend in backends, in names
// order, mirroring the two checks Engine.RegisterBackend performs (nil
// backend, empty Name()) plus one Configure-specific check: a backend
// whose Name() disagrees with the map key it is stored under would
// silently register under a different name than the one Configure
// validated, so that mismatch is rejected too. A map key is not itself an
// input a caller directly controls when going through WithBackend (which
// always keys by b.Name()), but EngineOptions.Backends is an exported
// field, so a hand-built EngineOptions is possible.
func validatePendingBackends(names []string, backends map[string]Backend) error {
	for _, name := range names {
		b := backends[name]
		if b == nil {
			return fmt.Errorf("backend must not be nil")
		}
		if b.Name() == "" {
			return fmt.Errorf("backend Name() must not be empty")
		}
		if b.Name() != name {
			return fmt.Errorf("backend registered under key %q reports Name() %q", name, b.Name())
		}
	}
	return nil
}

// pendingOperatorNames returns the names in customOperators not already
// present in alreadyRegistered, sorted. Configure uses this both to
// validate pending operator registrations and, once validation passes, to
// apply them, in the same deterministic order (F12) - iterating
// customOperators (a Go map) directly gave a different, randomized order
// on every call, so which of several invalid registrations Configure
// reported varied run to run.
func pendingOperatorNames(customOperators map[string]Operator, alreadyRegistered map[string]bool) []string {
	names := make([]string, 0, len(customOperators))
	for name := range customOperators {
		if alreadyRegistered[name] {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// validatePendingOperators replicates UnifiedOperatorRegistry.Register's
// validation (empty name, nil Operator) for each name in names, against
// customOperators, without registering anything. Configure calls this
// before mutating any engine state (F4): a Configure call with one or
// more invalid pending operators must leave the engine's configuration
// exactly as it was, not partially applied up to the first failure.
// names is expected to already be sorted (see pendingOperatorNames) so
// the first error returned is deterministic.
func validatePendingOperators(names []string, customOperators map[string]Operator) error {
	for _, name := range names {
		if name == "" {
			return fmt.Errorf("operator name cannot be empty")
		}
		if customOperators[name] == nil {
			return fmt.Errorf("operator implementation cannot be nil")
		}
	}
	return nil
}

// featureCachingEnabled reports whether ff has features.FeatureCaching
// enabled, mirroring DefaultEngine.IsFeatureEnabled's nil-safety (a nil
// *features.FeatureFlags means every flag reads as disabled) without
// requiring an engine receiver - Configure needs this to evaluate the
// *pending* (not-yet-applied) feature flags alongside the engine's current
// ones, to decide whether a Configure call changes anything cache-affecting.
func featureCachingEnabled(ff *features.FeatureFlags) bool {
	if ff == nil {
		return false
	}
	return ff.IsEnabled(features.FeatureCaching)
}

// closeOutgoingCache closes outgoing if it is being replaced by a
// different cache instance (including a nil one) and it implements an
// optional Close() method. This stops resources like ShardedCache's
// background cleanupLoop goroutine (internal/cache/shard.go) rather than
// leaking it every time Configure rebuilds the cache. cache.Cache does not
// itself declare Close() - DiskCache and HierarchicalCache expose
// Close() error while ShardedCache (the type cache.NewCache actually
// returns) exposes Close() with no return value - so this type-asserts to
// the narrower, no-error shape rather than widening the shared interface.
// A cache with no matching Close method, or one identical to the
// replacement (a Configure call that resolves to the same instance), is
// left alone.
func closeOutgoingCache(outgoing, replacement cache.Cache) {
	if outgoing == nil || outgoing == replacement {
		return
	}
	if closer, ok := outgoing.(interface{ Close() }); ok {
		closer.Close()
	}
}

// GetOperatorState returns the engine's own operator state. A run never
// writes its working markers here: it works on a state of its own and,
// when it finishes, publishes its vault references, vault placeholders,
// deferred paths, and pruned paths here, so this state holds the outputs
// of every finished run. During evaluation an operator's
// GetEngine(ev).GetOperatorState() returns the run's state instead.
func (e *DefaultEngine) GetOperatorState() OperatorState {
	// The engine itself implements OperatorState
	return e
}

// GetDocumentMemory returns the document memory tracker.
func (e *DefaultEngine) GetDocumentMemory() *DocumentMemory {
	e.memoryMutex.RLock()
	defer e.memoryMutex.RUnlock()
	return e.documentMemory
}

// EnableMemoryTracking enables document memory tracking. Guarded by
// memoryMutex end to end (not just around the field read/write) so two
// goroutines calling this concurrently on a shared engine - the case
// ensureHistoryTracking hits on every TrackHistory() Execute() - cannot
// both observe a nil documentMemory and each construct their own
// DocumentMemory, silently dropping one: the second caller through the
// lock always sees the first caller's already-assigned instance and calls
// Enable() on it instead.
func (e *DefaultEngine) EnableMemoryTracking() {
	e.memoryMutex.Lock()
	defer e.memoryMutex.Unlock()

	if e.documentMemory == nil {
		// Create with default config if not already created
		var memConfig MemoryConfig
		if e.opts.MemoryConfig != nil {
			memConfig = *e.opts.MemoryConfig
		}
		memConfig.Enabled = true
		e.documentMemory = NewDocumentMemory(memConfig)
	} else {
		e.documentMemory.Enable()
	}
}

// DisableMemoryTracking disables document memory tracking.
func (e *DefaultEngine) DisableMemoryTracking() {
	e.memoryMutex.RLock()
	dm := e.documentMemory
	e.memoryMutex.RUnlock()
	if dm != nil {
		dm.Disable()
	}
}

// GetMemoryStats returns memory tracking statistics.
func (e *DefaultEngine) GetMemoryStats() map[string]interface{} {
	e.memoryMutex.RLock()
	dm := e.documentMemory
	e.memoryMutex.RUnlock()
	if dm == nil {
		return map[string]interface{}{
			"enabled": false,
		}
	}
	return dm.GetMemoryStats()
}

// ClearMemoryHistory clears all tracked history.
func (e *DefaultEngine) ClearMemoryHistory() {
	e.memoryMutex.RLock()
	dm := e.documentMemory
	e.memoryMutex.RUnlock()
	if dm != nil {
		dm.Clear()
	}
}

// GetNodeHistory returns the history for a specific path.
func (e *DefaultEngine) GetNodeHistory(path string) (*NodeHistory, error) {
	e.memoryMutex.RLock()
	dm := e.documentMemory
	e.memoryMutex.RUnlock()
	if dm == nil {
		return nil, fmt.Errorf("memory tracking is not enabled")
	}
	return dm.GetHistory(path)
}

// QueryMemoryHistory queries the history with filters.
func (e *DefaultEngine) QueryMemoryHistory(filter HistoryFilter) []ChangeEvent {
	e.memoryMutex.RLock()
	dm := e.documentMemory
	e.memoryMutex.RUnlock()
	if dm == nil {
		return []ChangeEvent{}
	}
	return dm.Query(filter)
}

// Infrastructure system accessors

// GetFeatureFlags returns the feature flags for this engine.
func (e *DefaultEngine) GetFeatureFlags() *features.FeatureFlags {
	return e.Features
}

// SetFeatureFlags sets the feature flags for this engine.
func (e *DefaultEngine) SetFeatureFlags(ff *features.FeatureFlags) {
	e.Features = ff
}

// IsFeatureEnabled checks if a feature flag is enabled.
func (e *DefaultEngine) IsFeatureEnabled(flag string) bool {
	if e.Features == nil {
		return false
	}
	return e.Features.IsEnabled(flag)
}

// GetCache returns the cache for this engine (may be nil).
func (e *DefaultEngine) GetCache() cache.Cache {
	return e.Cache
}

// SetCache sets the cache for this engine.
func (e *DefaultEngine) SetCache(c cache.Cache) {
	e.Cache = c
}

// GetMetricsRegistry returns the metrics registry for this engine (may be nil).
func (e *DefaultEngine) GetMetricsRegistry() *metrics.Registry {
	return e.MetricsRegistry
}

// SetMetricsRegistry sets the metrics registry for this engine.
func (e *DefaultEngine) SetMetricsRegistry(r *metrics.Registry) {
	e.MetricsRegistry = r
}

// GetConfig returns the configuration for this engine (may be nil).
func (e *DefaultEngine) GetConfig() *config.Config {
	return e.Config
}

// SetConfig sets the configuration for this engine.
func (e *DefaultEngine) SetConfig(cfg *config.Config) {
	e.Config = cfg
}

// GetWorkerPool returns the worker pool for this engine (may be nil).
func (e *DefaultEngine) GetWorkerPool() *parallel.WorkerPool {
	return e.Pool
}

// SetWorkerPool sets the worker pool for this engine.
func (e *DefaultEngine) SetWorkerPool(pool *parallel.WorkerPool) {
	e.Pool = pool
}

// newEngineFromOptions builds a *DefaultEngine directly from *EngineOptions.
// It does not validate options; callers that need validation should call
// createEngineFromOptions instead.
func newEngineFromOptions(opts *EngineOptions) *DefaultEngine {
	e := &DefaultEngine{
		opts:           *opts,
		registry:       DefaultRegistry.Clone(),
		localOperators: make(map[string]bool),
		operatorState: operatorState{
			vaultRefs:         make(map[string][]string),
			vaultPlaceholders: make(map[string]string),
			usedIPs:           make(map[string]string),
			pathsToSort:       make(map[string]string),
			skipVault:         opts.SkipVault,
			skipAws:           opts.SkipAws,
			skipNats:          opts.SkipNats,
			redactMode:        opts.Redact,
		},
		backends: make(map[string]Backend),
		metrics: &EngineMetrics{
			OperatorCalls: make(map[string]int64),
		},
	}

	// Initialize document memory if a config was provided and enabled
	if opts.MemoryConfig != nil && opts.MemoryConfig.Enabled {
		e.documentMemory = NewDocumentMemory(*opts.MemoryConfig)
	}

	return e
}

// createEngineFromOptions creates an engine from EngineOptions (used by NewEngine in api.go).
//
//nolint:gocyclo // engine configuration requires handling many optional settings
func createEngineFromOptions(opts *EngineOptions) (Engine, error) {
	// Validate options
	if opts.MaxConcurrency < 0 {
		return nil, NewConfigurationError("concurrency must be non-negative")
	}

	// Build the engine directly from opts
	engine := newEngineFromOptions(opts)

	// Register custom operators if any
	if opts.CustomOperators != nil {
		for name, op := range opts.CustomOperators {
			if err := engine.RegisterOperator(name, op); err != nil {
				return nil, err
			}
		}
	}

	// Enable memory tracking if requested (without a MemoryConfig)
	if opts.EnableMemoryTracking {
		engine.EnableMemoryTracking()
	}

	// Apply new infrastructure systems if provided

	// Set feature flags (use provided or create defaults)
	if opts.FeatureFlags != nil {
		engine.Features = opts.FeatureFlags
	} else {
		engine.Features = features.DefaultFlags()
	}

	// Apply caching options.
	//
	// EnableCache alone does not win over a disabled FeatureCaching flag:
	// the CLI unconditionally requests WithCache(true, 1000) on every
	// invocation (cmd/graft/main.go buildEngineAndDocs) and relies on
	// GRAFT_FEATURE_CACHE=false (which clears FeatureCaching, see
	// internal/features/env.go) suppressing the cache anyway - see
	// cmd/graft/main_test.go TestConfigEngineOptsWiresFeatureFlags, which
	// asserts exactly that even though WithCache(true, ..) was requested.
	// A CacheInstance bypasses this gate entirely (below), which is the
	// asymmetry: supplying a pre-built cache always wins, but the EnableCache
	// boolean only wins when the feature flag also allows it.
	if opts.CacheInstance != nil {
		engine.Cache = opts.CacheInstance
	} else if opts.EnableCache && engine.IsFeatureEnabled(features.FeatureCaching) {
		// Create default cache if caching is enabled. CacheTTL, when set
		// (WithCacheTTL), gives entries a default expiration; internal/cache's
		// ShardedCache (built by both NewCache and NewTTLCache) already applies
		// Options.TTL to every Set call, so no separate TTL-capable
		// constructor is needed here.
		cacheOpts := []cache.Option{cache.WithMaxSize(opts.CacheSize)}
		if opts.CacheTTL > 0 {
			cacheOpts = append(cacheOpts, cache.WithTTL(opts.CacheTTL))
		}
		engine.Cache = cache.NewCache(cacheOpts...)
	}

	// Apply metrics registry
	if opts.MetricsRegistry != nil {
		engine.MetricsRegistry = opts.MetricsRegistry
	} else if opts.EnableMetrics && engine.IsFeatureEnabled(features.FeatureMetrics) {
		engine.MetricsRegistry = metrics.NewRegistry()
	}

	// Apply config
	if opts.ConfigInstance != nil {
		engine.Config = opts.ConfigInstance
	}

	// Apply worker pool
	if opts.WorkerPool != nil {
		engine.Pool = opts.WorkerPool
	} else if opts.EnableParallel && engine.IsFeatureEnabled(features.FeatureParallelEvaluation) {
		// Create default worker pool if parallel evaluation is enabled.
		// minWorkers comes from the resolved ConfigInstance's
		// Parallel.MinWorkers when one was supplied (WithConfigInstance),
		// instead of always hardcoding 1, so a config file/env override of
		// GRAFT_PARALLEL_MIN_WORKERS has an observable effect on pool
		// construction.
		minWorkers := 1
		if opts.ConfigInstance != nil && opts.ConfigInstance.Parallel.MinWorkers > 0 {
			minWorkers = opts.ConfigInstance.Parallel.MinWorkers
		}
		pool, err := parallel.NewPool(minWorkers, opts.MaxConcurrency)
		if err != nil {
			log.DEBUG("Warning: Failed to create worker pool: %v", err)
		} else {
			engine.Pool = pool
		}
	}

	// Apply WithTraceOutput/WithTraceLevel/WithDebugLogging, if any were
	// supplied. A no-op when none were (see applyLogging).
	applyLogging(opts)

	// Apply the backend registry (C7, see backend.go). WithBackendRegistry
	// must be applied before the backends are registered (harmless either
	// way for RegisterBackend itself, which does not consult the flag,
	// but keeps the flag's final value settled before anything that might
	// reason about it runs). Retry/cache configuration and the audit
	// logger must also be set before the backends are registered, since
	// RegisterBackend reads them at registration time to build each
	// backend's wrapper.
	if opts.backendRegistryEnabled != nil {
		engine.Features.Set(features.FeatureBackendRegistry, *opts.backendRegistryEnabled)
	}
	if opts.BackendRetryConfigs != nil {
		engine.backendRetry = opts.BackendRetryConfigs
	}
	if opts.BackendCaches != nil {
		engine.backendCaches = opts.BackendCaches
	}
	if opts.AuditLoggerInstance != nil {
		engine.auditLogger = opts.AuditLoggerInstance
	}
	for _, b := range opts.Backends {
		if err := engine.RegisterBackend(b); err != nil {
			return nil, err
		}
	}

	return engine, nil
}

// phaseToString converts an OperatorPhase to a string for metrics labels.
func phaseToString(phase OperatorPhase) string {
	switch phase {
	case MergePhase:
		return "merge"
	case EvalPhase:
		return "eval"
	case ParamPhase:
		return "param"
	default:
		return valueTypeUnknown
	}
}
