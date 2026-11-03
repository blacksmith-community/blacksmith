package graft

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fivetwenty-io/graft/internal/parallel"
)

// ErrNoWorkerPool is returned by EvaluateParallel when the engine has no
// WorkerPool configured (see DefaultEngine.GetWorkerPool). RunPhaseParallel
// silently falls back to sequential execution when it can't find a pool
// (evaluator_parallel.go); EvaluateParallel refuses instead of inheriting
// that fallback, since a caller that asked for parallel evaluation and
// silently got sequential execution is exactly the kind of doc-vs-code gap
// this API exists to close. Configure the engine with WithParallel(true)
// or WithWorkerPool before calling EvaluateParallel.
var ErrNoWorkerPool = errors.New("graft: EvaluateParallel: engine has no WorkerPool configured")

// ErrInvalidEvalPlan is returned by EvaluateParallel when a non-empty
// waves argument does not describe a dependency-respecting ordering of
// doc's operator calls: some operator in waves is positioned at or before
// a dependency that DependencyGraph (built fresh from doc, the same way
// BuildDependencyGraph does) says must run first. Every phase is checked,
// regardless of what the plan's own OperatorRef.Phase fields say.
var ErrInvalidEvalPlan = errors.New("graft: EvaluateParallel: waves is not a valid evaluation plan for doc")

// ErrDependencyCycle is returned by DependencyGraph.TopologicalSort when
// the graph contains a cycle. It wraps the same class of failure
// Evaluator.DataFlow reports via kahnSort's "cycle detected in operator
// data-flow graph" error, so a cyclic document surfaces a clear, typed
// error from the public API too, not a hang or a panic.
var ErrDependencyCycle = errors.New("graft: dependency graph has a cycle")

// OperatorRef describes one operator call found in a document: where it
// is, which operator it calls, its arguments, and the phase it runs in.
// It is a read-only snapshot - mutating Args does not affect evaluation.
type OperatorRef struct {
	// Path is the operator call's canonical cursor path (e.g. "jobs.0.name").
	Path string
	// Operator is the operator name as written in the call (e.g. "grab", "vault").
	Operator string
	// Args are the call's parsed argument expressions.
	Args []*Expr
	// Phase is when this operator call runs (MergePhase, ParamPhase, or EvalPhase).
	Phase OperatorPhase
}

// DependencyGraph is a read-only snapshot of the operator calls found in a
// document for one OperatorPhase, and the dependency edges between them.
// Build one with (*DefaultEngine).BuildDependencyGraph.
//
// A DependencyGraph reflects doc's operator calls as they exist at the
// moment it was built; it does not run any operator, and it does not
// update itself if doc changes afterward (call BuildDependencyGraph again
// for a fresh snapshot - e.g. after Evaluate, if a "(( grab ... ))" result
// turned out to be itself-unevaluated YAML with more operator calls).
type DependencyGraph struct {
	phase      OperatorPhase
	order      []string // canonical paths, sorted, for stable Nodes() order
	nodes      map[string]OperatorRef
	deps       map[string][]string // path -> paths it depends on (must run first), sorted
	dependents map[string][]string // path -> paths that depend on it, sorted
}

// newDependencyGraph builds a DependencyGraph from a dataFlowGraph -
// Evaluator.computeDataFlowGraph's output - the exact operator set and
// edges DataFlow's sequential kahnSort also consumes. This is the only
// place a DependencyGraph is constructed, so it can never diverge from
// what the live sequential path saw for the same document and phase.
func newDependencyGraph(phase OperatorPhase, dfg *dataFlowGraph) *DependencyGraph {
	g := &DependencyGraph{
		phase:      phase,
		nodes:      make(map[string]OperatorRef, len(dfg.ctx.all)),
		deps:       make(map[string][]string),
		dependents: make(map[string][]string),
	}

	for path, op := range dfg.ctx.all {
		ref := OperatorRef{Path: path, Operator: op.name, Args: op.args}
		if op.op != nil {
			ref.Phase = op.op.Phase()
		} else {
			ref.Phase = phase
		}
		g.nodes[path] = ref
	}

	g.order = make([]string, 0, len(g.nodes))
	for path := range g.nodes {
		g.order = append(g.order, path)
	}
	sort.Strings(g.order)

	// buildDependencyGraph can report the same (dependency, dependent)
	// pair more than once - e.g. an operator whose two arguments both
	// resolve to the same producing opcall. Dedup so Dependencies() and
	// Dependents() each report a path once.
	seen := make(map[string]bool)
	for _, pair := range dfg.edges {
		depPath := pair[0].canonical.String()
		dependentPath := pair[1].canonical.String()
		key := depPath + "\x00" + dependentPath
		if seen[key] {
			continue
		}
		seen[key] = true

		g.deps[dependentPath] = append(g.deps[dependentPath], depPath)
		g.dependents[depPath] = append(g.dependents[depPath], dependentPath)
	}

	for path := range g.deps {
		sort.Strings(g.deps[path])
	}
	for path := range g.dependents {
		sort.Strings(g.dependents[path])
	}

	return g
}

// BuildDependencyGraph scans doc for phase's operator calls and returns a
// DependencyGraph snapshot of their dependency relationships.
//
// It is built from the same tree walk and edge computation DataFlow's
// sequential sort uses (Evaluator.computeDataFlowGraph) rather than a
// second, independently maintained graph implementation, so it cannot
// silently drift out of agreement with what the sequential path actually
// does.
//
// doc must be non-nil with a map[string]interface{} RawData (the shape
// every graft.Document produced by ParseYAML/ParseJSON/NewDocument has).
// Operator registration is engine-local (see WithCustomOperator), which is
// why this is a method on the engine rather than a free function: a graph
// built from one engine's registry can report a different Operator set
// than the same document built from another's.
func (e *DefaultEngine) BuildDependencyGraph(doc Document, phase OperatorPhase) (*DependencyGraph, error) {
	if doc == nil {
		return nil, fmt.Errorf("graft: BuildDependencyGraph: doc is nil")
	}
	data, ok := doc.RawData().(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("graft: BuildDependencyGraph: document data is not a map")
	}

	ev := e.createEvaluator(data)
	dfg, err := ev.computeDataFlowGraph(phase)
	if err != nil {
		return nil, err
	}
	if len(dfg.ctx.errors.Errors) > 0 {
		return nil, dfg.ctx.errors
	}

	return newDependencyGraph(phase, dfg), nil
}

// Nodes returns every operator call in the graph, ordered by canonical path.
func (g *DependencyGraph) Nodes() []OperatorRef {
	if g == nil {
		return nil
	}
	refs := make([]OperatorRef, len(g.order))
	for i, path := range g.order {
		refs[i] = g.nodes[path]
	}
	return refs
}

// Dependencies returns the canonical paths of operator calls that path's
// operator call depends on (must run before it), sorted. Returns nil if
// path has no node in this graph or has no dependencies.
func (g *DependencyGraph) Dependencies(path string) []string {
	if g == nil {
		return nil
	}
	return append([]string(nil), g.deps[path]...)
}

// Dependents returns the canonical paths of operator calls that depend on
// path's operator call (must run after it), sorted. Returns nil if path
// has no node in this graph or has no dependents.
func (g *DependencyGraph) Dependents(path string) []string {
	if g == nil {
		return nil
	}
	return append([]string(nil), g.dependents[path]...)
}

// DetectCycles returns every elementary cycle in the graph, each as an
// ordered slice of canonical paths (path[i+1] depends on path[i] running
// first... and path[0] depends on the last element, closing the cycle;
// the closing edge is not repeated in the slice). Returns nil if the
// graph is acyclic. The same cycle may be reported more than once if more
// than one node on it is also reachable from outside the cycle - callers
// that need a deduplicated set can dedupe by the sorted set of paths each
// cycle slice contains.
func (g *DependencyGraph) DetectCycles() [][]string {
	if g == nil {
		return nil
	}
	return findCycles(g.dependents, g.order)
}

func indexOfString(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// TopologicalSort returns the graph's operator calls in an order that
// respects every dependency edge (each OperatorRef appears after every
// OperatorRef it depends on), computed by flattening BuildEvalPlan's
// waves - the same wave computation the live parallel scheduler uses (see
// BuildEvalPlan) - rather than a separate sort implementation. Returns
// ErrDependencyCycle, wrapping the detected cycle(s), if the graph is
// cyclic.
func (g *DependencyGraph) TopologicalSort() ([]OperatorRef, error) {
	if g == nil || len(g.order) == 0 {
		return nil, nil
	}

	waves := BuildEvalPlan(g)
	if len(waves) == 0 {
		cycles := g.DetectCycles()
		return nil, fmt.Errorf("%w: %v", ErrDependencyCycle, cycles)
	}

	refs := make([]OperatorRef, 0, len(g.order))
	for _, wave := range waves {
		refs = append(refs, wave.Operators...)
	}
	return refs, nil
}

// ToDOT renders the graph in Graphviz DOT format. An edge "A -> B" means A
// must be evaluated before B (A produces a value B's operator call reads).
func (g *DependencyGraph) ToDOT() string {
	var b strings.Builder
	b.WriteString("digraph DependencyGraph {\n")

	if g != nil {
		for _, path := range g.order {
			ref := g.nodes[path]
			fmt.Fprintf(&b, "  %q [label=%q];\n", path, fmt.Sprintf("%s: %s", path, ref.Operator))
		}
		for _, from := range g.order {
			for _, to := range g.dependents[from] {
				fmt.Fprintf(&b, "  %q -> %q;\n", from, to)
			}
		}
	}

	b.WriteString("}\n")
	return b.String()
}

// EvalWave is a set of operator calls with no dependency relationship to
// each other and every dependency already satisfied by an earlier wave -
// the unit RunPhaseParallel's scheduler dispatches concurrently.
type EvalWave struct {
	Operators []OperatorRef
}

// BuildEvalPlan groups g's operator calls into dependency-respecting
// waves by feeding g's edges into internal/parallel.Scheduler - the exact
// scheduler type Evaluator.runOpsWithScheduler builds from live operator
// calls for RunPhaseParallel (evaluator_parallel.go) - rather than a
// separate wave-computation algorithm. Two operator calls land in the
// same wave here under precisely the condition RunPhaseParallel would
// place them in the same wave: neither depends, directly or transitively,
// on the other.
//
// Each wave's Operators are sorted by Path for a reproducible, distinct-
// from-goroutine-scheduling result across calls - runOpsWithScheduler
// applies the same tie-break for tree-mutation order (see its doc
// comment), so this also matches which order a real RunPhaseParallel call
// would apply that wave's results in.
//
// Returns nil if g is empty or nil, or if g contains a cycle (a cycle
// makes Scheduler.Schedule return an error; BuildEvalPlan reports that as
// no plan rather than panicking or hanging - use DetectCycles or
// TopologicalSort for cycle detail).
func BuildEvalPlan(g *DependencyGraph) []EvalWave {
	if g == nil || len(g.order) == 0 {
		return nil
	}

	scheduler := parallel.NewScheduler()
	for _, path := range g.order {
		deps := append([]string(nil), g.deps[path]...)
		// AddTask only fails on a nil task or empty ID, neither possible
		// here: path comes from g.order, which is derived from g.nodes'
		// own non-empty keys.
		_ = scheduler.AddTask(&parallel.Task{ID: path, Dependencies: deps})
	}

	waves, err := scheduler.Schedule()
	if err != nil {
		return nil
	}

	result := make([]EvalWave, len(waves))
	for i, wave := range waves {
		sort.Slice(wave, func(a, b int) bool { return wave[a].ID < wave[b].ID })
		ops := make([]OperatorRef, len(wave))
		for j, task := range wave {
			ops[j] = g.nodes[task.ID]
		}
		result[i] = EvalWave{Operators: ops}
	}
	return result
}

// validateEvalPlan checks that waves does not order any operator call at
// or before a dependency that a fresh DependencyGraph says must run
// first - the "never orders X before Y when the live path requires Y
// first" half of EvaluateParallel's contract. It does not require waves
// to mention every live operator call (a partial/subset plan is allowed -
// RunPhaseParallel honors every live edge regardless of what waves said),
// and it does not validate wave grouping itself (RunPhaseParallel
// computes its own waves unconditionally; waves' grouping is
// documentation, not instruction).
//
// Every phase is checked, not just the ones waves' own OperatorRef.Phase
// fields name: MergePhase is OperatorPhase's zero value, so deriving the
// set of phases to check from the caller's refs would silently skip
// EvalPhase for a hand-built plan that left Phase unset - accepting an
// ordering this function exists to reject.
func (e *DefaultEngine) validateEvalPlan(data map[string]interface{}, waves []EvalWave) error {
	position := make(map[string]int)
	for i, wave := range waves {
		for _, ref := range wave.Operators {
			if ref.Path == "" {
				return fmt.Errorf("%w: wave %d has an operator ref with an empty Path", ErrInvalidEvalPlan, i)
			}
			if prev, exists := position[ref.Path]; exists {
				return fmt.Errorf("%w: %s appears in more than one wave (%d and %d)", ErrInvalidEvalPlan, ref.Path, prev, i)
			}
			position[ref.Path] = i
		}
	}

	for _, phase := range []OperatorPhase{MergePhase, EvalPhase, ParamPhase} {
		liveEv := e.createEvaluator(data)
		dfg, err := liveEv.computeDataFlowGraph(phase)
		if err != nil {
			return fmt.Errorf("%w: could not compute live dependency graph for phase %v: %w", ErrInvalidEvalPlan, phase, err)
		}

		for _, pair := range dfg.edges {
			depPath := pair[0].canonical.String()
			dependentPath := pair[1].canonical.String()

			depPos, depKnown := position[depPath]
			dependentPos, dependentKnown := position[dependentPath]
			if !depKnown || !dependentKnown {
				// waves did not mention one side of this live edge - not
				// this function's concern; see the doc comment above.
				continue
			}
			if depPos >= dependentPos {
				return fmt.Errorf("%w: %s (wave %d) is not scheduled strictly before its dependent %s (wave %d)",
					ErrInvalidEvalPlan, depPath, depPos, dependentPath, dependentPos)
			}
		}
	}

	return nil
}

// EvaluateParallel evaluates doc using the engine's real parallel
// execution path - Evaluator.RunPhaseParallel, the same method
// DefaultEngine.Evaluate calls when FeatureParallelEvaluation and a
// WorkerPool are both configured - forced on for every phase, rather than
// only when the engine happens to be configured that way.
//
// waves documents the caller's expected evaluation plan, typically
// BuildDependencyGraph(doc, phase) piped through BuildEvalPlan, and is
// validated against doc's live dependency graph before anything runs: if
// it orders any operator call at or before a dependency the live path
// requires to run first, EvaluateParallel returns ErrInvalidEvalPlan
// without evaluating doc. A nil or empty waves skips validation.
//
// waves' own wave grouping does not drive execution - RunPhaseParallel
// computes and schedules its own waves internally, exactly as it does for
// engine.Evaluate. This keeps EvaluateParallel a verified, read-only
// projection onto the one live execution path, rather than a second,
// independently maintained one.
//
// Returns ErrNoWorkerPool if the engine has no WorkerPool: RunPhaseParallel
// silently falls back to sequential execution when it can't find one, and
// EvaluateParallel treats that as an error rather than repeating the
// "documented as parallel, secretly sequential" gap this API exists to
// close. Configure the engine with WithParallel(true) or WithWorkerPool
// first.
func (e *DefaultEngine) EvaluateParallel(ctx context.Context, doc Document, waves []EvalWave) (Document, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if doc == nil {
		return nil, fmt.Errorf("graft: EvaluateParallel: doc is nil")
	}
	if e.Pool == nil {
		return nil, ErrNoWorkerPool
	}

	data, ok := doc.RawData().(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("graft: EvaluateParallel: document data is not a map")
	}

	if len(waves) > 0 {
		if err := e.validateEvalPlan(data, waves); err != nil {
			return nil, err
		}
	}

	e.logDebugf("EvaluateParallel: starting evaluation")

	ev := e.createEvaluator(data)

	if cherryPickPaths := GetCherryPickPaths(ctx); len(cherryPickPaths) > 0 {
		ev.CherryPickPaths = cherryPickPaths
		ev.Only = cherryPickPaths
	}
	if priorValues := GetPriorCalcValues(ctx); len(priorValues) > 0 {
		ev.PriorValues = priorValues
	}

	if err := e.evaluate(ctx, ev, true); err != nil {
		return nil, err
	}

	return NewDocument(ev.Tree), nil
}
