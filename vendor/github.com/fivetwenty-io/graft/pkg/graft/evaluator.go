package graft

import (
	"fmt"
	"os"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/fivetwenty-io/graft/internal/literal"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"

	"github.com/fivetwenty-io/graft/internal/errtext"
	"github.com/fivetwenty-io/graft/log"
	"github.com/fivetwenty-io/graft/pkg/graft/interfaces"
	"github.com/fivetwenty-io/graft/pkg/graft/merger"
)

// Evaluator processes YAML documents by evaluating operator expressions.
type Evaluator struct {
	Tree     map[string]interface{}
	Deps     map[string][]tree.Cursor
	SkipEval bool
	Here     *tree.Cursor

	// Target is the "@target" name of the operator call currently running
	// (e.g. "prod" in "(( vault@prod "path:key" ))"), or "" if the call has
	// none. Opcall.Run sets and restores it around each op.Run, mirroring
	// Here, so a target-aware operator's Run method can read it without a
	// change to the Operator interface's signature.
	Target string

	// NoCache is true while an operator call written with the ":nocache"
	// modifier (e.g. "(( vault:nocache "path:key" ))") is running.
	// Opcall.Run sets and restores it around each op.Run exactly like
	// Target, so backend-backed operators can bypass their caches for
	// that one lookup without an Operator interface change.
	NoCache bool

	CheckOps []*Opcall

	Only []string

	// depComputing guards the dependency-computation phase, which
	// mutates the shared Here cursor and therefore must never run
	// concurrently with itself. addSchedulerTasks increments it on
	// entry via atomic ops and panics if another computation is
	// already in flight. A plain int32 (not atomic.Int32) so the
	// deliberate shallow copy in computeOp stays vet-clean.
	depComputing int32

	// holdsLiteral is set when DataFlow finds a literal wrapper in Tree.
	// computeOp then checks each result for text read from inside one.
	holdsLiteral bool

	// Reference to the engine (for accessing registries and state)
	engine Engine

	// DataflowOrder controls the ordering of operations in dataflow output
	// "alphabetical" (default) - sort operations alphabetically by path
	// "insertion" - maintain the order operations were discovered
	DataflowOrder string

	// PriorValues holds, per canonical path string, the value a "(( calc
	// <leading-op> ... ))" value-modification expression overwrote during
	// merge — the "existing value" op_calc.go's leading-operator branch
	// needs but can no longer find at ev.Here once the overlay's operator
	// string has replaced it. Populated only for
	// that one expression shape, by the merge builder, before evaluation
	// begins; nil (or missing the key) for every other document.
	PriorValues map[string]interface{}

	// CherryPickPaths contains the paths to cherry-pick during evaluation.
	// When set, only operators under these paths and their dependencies will be evaluated.
	// This enables selective evaluation, significantly improving performance for large documents
	// when only specific parts are needed.
	//
	// Selective Evaluation Behavior:
	// - Only operators whose paths match or are under cherry-pick paths are evaluated
	// - Dependencies of cherry-picked operators are automatically included (transitive)
	// - Path matching supports both exact indices and named array entries
	// - Empty cherry-pick paths means evaluate everything (default behavior)
	//
	// Example: If cherry-picking "services.web", these operators will be evaluated:
	//   - services.web.port: (( grab defaults.port ))     // Under cherry-pick path
	//   - defaults.port: 8080                             // Dependency of above
	// But this won't be evaluated:
	//   - services.api.port: (( grab defaults.api_port )) // Not under cherry-pick path
	CherryPickPaths []string

	// Sources are the merge inputs this evaluation was built from, in
	// merge order. They are read only on the cycle-error path, to
	// attribute each operator on the cycle to a file and line. Empty is
	// valid: the cycle error then omits its "inputs:" block and every
	// node renders as unresolved.
	Sources []SourceRef

	// Memory tracker for recording evaluation changes
	memory interfaces.MemoryTracker
}

// SetEngine sets the engine for the evaluator.
//
// An Evaluator serves one call. Run reads and writes the operator state
// of the engine set here, so do not hand it an engine that other
// goroutines are using: concurrent calls would see and clear one
// another's prune, sort, and static IP markers. To share one engine
// across goroutines, call Engine.Evaluate, which gives every run its
// own state.
func (ev *Evaluator) SetEngine(engine Engine) {
	ev.engine = engine
}

// SetMemoryTracker sets the memory tracker for the evaluator.
func (ev *Evaluator) SetMemoryTracker(memory interfaces.MemoryTracker) {
	ev.memory = memory
}

func nameOfObj(o interface{}, def string) string {
	for _, field := range tree.NameFields {
		if val, ok := o.(map[string]interface{}); ok {
			if value, found := val[field]; found {
				if s, isString := value.(string); isString {
					return s
				}
			}
		}
	}
	return def
}

// getOperatorName extracts the operator name from an Operator interface.
func getOperatorName(op Operator) string {
	// Use reflection to get the type name
	t := reflect.TypeOf(op)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	name := t.Name()

	// Convert from type name to operator name (e.g., GrabOperator -> grab)
	name = strings.TrimSuffix(name, "Operator")
	return strings.ToLower(name)
}

// dataFlowContext holds the state for a DataFlow operation.
type dataFlowContext struct {
	ev             *Evaluator
	phase          OperatorPhase
	all            map[string]*Opcall
	insertionOrder []string
	locs           []*tree.Cursor
	errors         MultiError
	visited        map[uintptr]bool
	// ambiguous is non-nil while scanning beneath a list entry whose
	// name is shared with another entry in the same list. Any operator
	// found down there is unaddressable, so registerOpcall reports it
	// instead of registering it. See ambiguousEntry.
	ambiguous *ambiguousEntry
}

// ambiguousEntry describes a list entry whose name/key/id value is
// shared with at least one sibling.
//
// A name-keyed cursor resolves through listFind (tree/resolver.go),
// which returns the FIRST entry carrying that name. Every operator
// under every entry sharing the name therefore canonicalizes to the
// same path, they overwrite each other in dataFlowContext.all, and one
// survivor writes its value into the first entry's slot while the rest
// keep their raw operator text. Refusing the merge is a deliberate
// divergence from spruce, which produces that corrupt output silently;
// see docs/spruce/known-gaps.md.
type ambiguousEntry struct {
	// name is the shared name/key/id value.
	name string
	// list is the dotted path of the list holding the entries.
	list string
	// paths are the dotted paths of every entry sharing name, in list
	// order.
	paths []string
}

// errorAt reports the operator at opPath as unaddressable, naming the
// shared name and every entry carrying it so the author can see which
// entries to rename. The error is a *PathError against the list, so it
// renders as one " - $.<list>: ..." line like every other per-path
// merge error.
func (a *ambiguousEntry) errorAt(opPath string) error {
	at := strings.Join(a.paths, ", ")
	return &PathError{
		Path: a.list,
		Cause: errtext.WithPlain(
			fmt.Errorf("duplicate name %q at %s makes the operator at $.%s unaddressable; give each entry a unique name",
				a.name, at, opPath),
			fmt.Sprintf("duplicate name at %s makes the operator at $.%s unaddressable; give each entry a unique name",
				at, opPath)),
	}
}

// newDataFlowContext creates a new dataflow context.
func newDataFlowContext(ev *Evaluator, phase OperatorPhase) *dataFlowContext {
	return &dataFlowContext{
		ev:             ev,
		phase:          phase,
		all:            make(map[string]*Opcall),
		insertionOrder: []string{},
		locs:           []*tree.Cursor{},
		errors:         MultiError{Errors: []error{}},
		visited:        make(map[uintptr]bool),
	}
}

// checkValue checks a value for operator expressions.
func (ctx *dataFlowContext) checkValue(v interface{}) {
	s, ok := v.(string)
	if !ok {
		ctx.scanValue(v)
		return
	}

	ctx.logDebugIfNeeded(s)

	op, err := ParseOpcallForEngine(ctx.ev.engine, ctx.phase, s)
	if err != nil {
		ctx.errors.Append(err)
		return
	}
	if op == nil {
		return
	}

	if op.op != nil && op.op.Phase() != ctx.phase {
		return
	}

	ctx.registerOpcall(op)
}

// logDebugIfNeeded logs debug info for certain patterns.
func (ctx *dataFlowContext) logDebugIfNeeded(s string) {
	if strings.HasPrefix(s, "(( grab services") {
		log.DEBUG("evaluator.check: found grab services string in phase %d at %s: %s", ctx.phase, ctx.ev.Here.String(), s)
	}
	if strings.Contains(s, "grab base") {
		log.DEBUG("evaluator.check: found string with 'grab base': %s", s)
	}
	if strings.Contains(s, "services.0") && strings.HasPrefix(s, "((") {
		log.DEBUG("evaluator.check: phase=%d, parsing operator with services.0: %s", ctx.phase, s)
	}
}

// registerOpcall registers an operator call in the context.
func (ctx *dataFlowContext) registerOpcall(op *Opcall) {
	if ctx.ambiguous != nil {
		ctx.errors.Append(ctx.ambiguous.errorAt(ctx.ev.Here.String()))
		return
	}

	op.where = ctx.ev.Here.Copy()
	if canon, err := op.where.Canonical(ctx.ev.Tree); err == nil {
		op.canonical = canon
	} else {
		op.canonical = op.where
	}

	canonStr := op.canonical.String()
	if _, exists := ctx.all[canonStr]; !exists {
		ctx.insertionOrder = append(ctx.insertionOrder, canonStr)
	}
	ctx.all[canonStr] = op
	log.TRACE("found an operation at %s: %s", op.where.String(), op.src)
	log.TRACE("        (canonical at %s)", op.canonical.String())
	ctx.locs = append(ctx.locs, op.canonical)
}

// scanValue recursively scans a value for operators.
func (ctx *dataFlowContext) scanValue(o interface{}) {
	switch v := o.(type) {
	case literal.Value:
		// The scan never looks inside a literal, but the evaluator must
		// know the tree holds one.
		ctx.ev.holdsLiteral = true
	case map[string]interface{}:
		ctx.scanStringMap(v)
	case []interface{}:
		ctx.scanSlice(v)
	}
}

// scanStringMap scans a map[string]interface{}.
func (ctx *dataFlowContext) scanStringMap(v map[string]interface{}) {
	ptr := reflect.ValueOf(v).Pointer()
	if ctx.visited[ptr] {
		return
	}
	ctx.visited[ptr] = true
	defer delete(ctx.visited, ptr)

	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		ctx.ev.Here.Push(k)
		ctx.checkValue(v[k])
		ctx.ev.Here.Pop()
	}
}

// scanSlice scans a []interface{}.
func (ctx *dataFlowContext) scanSlice(v []interface{}) {
	ptr := reflect.ValueOf(v).Pointer()
	if ctx.visited[ptr] {
		return
	}
	ctx.visited[ptr] = true
	defer delete(ctx.visited, ptr)

	listPath := ctx.ev.Here.String()
	shared := sharedNames(v, listPath)

	for i, val := range v {
		idx := fmt.Sprintf("%d", i)
		name := nameOfObj(val, idx)
		op, _ := ParseOpcallForEngine(ctx.ev.engine, ctx.phase, name)

		// A name shared with a sibling cannot address this entry, so
		// push the index and remember why: the index keeps the scan's
		// own paths unique, and registerOpcall reports any operator
		// found beneath rather than letting it collide.
		entry := shared[name]
		if op == nil && entry == nil {
			ctx.ev.Here.Push(name)
		} else {
			ctx.ev.Here.Push(idx)
		}

		outer := ctx.ambiguous
		if entry != nil {
			ctx.ambiguous = entry
		}
		ctx.checkValue(val)
		ctx.ambiguous = outer

		ctx.ev.Here.Pop()
	}
}

// sharedNames maps each name that more than one entry of v carries to
// the ambiguousEntry describing that collision. Names belonging to a
// single entry are absent, so the common case allocates one empty map
// and nothing else.
//
// Entries are keyed on the name nameOfObj yields, which is what would
// become the path segment. That deliberately catches a collision
// between different source fields (one entry's "name" against another's
// "key"), and between an explicit name and the positional default it
// shadows, because either one produces the same ambiguous cursor.
func sharedNames(v []interface{}, listPath string) map[string]*ambiguousEntry {
	order := make([]string, 0, len(v))
	byName := make(map[string][]string, len(v))
	for i, val := range v {
		idx := fmt.Sprintf("%d", i)
		name := nameOfObj(val, idx)
		if _, seen := byName[name]; !seen {
			order = append(order, name)
		}
		byName[name] = append(byName[name], joinPath(listPath, idx))
	}

	shared := map[string]*ambiguousEntry{}
	for _, name := range order {
		paths := byName[name]
		if len(paths) < 2 {
			continue
		}
		shared[name] = &ambiguousEntry{name: name, list: listPath, paths: paths}
	}
	return shared
}

// joinPath appends one segment to a dotted path, tolerating the empty
// path a root-level list scans under.
func joinPath(base, segment string) string {
	if base == "" {
		return segment
	}
	return base + "." + segment
}

// buildDependencyGraph builds the dependency graph from all operators.
//
// ctx.ev.Here is set to each opcall's own path before calling its
// Dependencies(), mirroring Opcall.Run's own "ev.Here = op.where" during
// the later Run pass. Without it, ev.Here stays wherever the preceding
// tree scan left it (the root, since scanValue's Push/Pop calls are
// balanced) for every single call in this loop, not that opcall's own
// position — an operator whose Dependencies() implementation resolves a
// path relative to ev.Here (op_calc.go's calcBareNameDependencies, for a
// bare named variable that is a sibling of the calc call's own path) then
// always computes the wrong relative cursor, silently drops the one
// dependency edge that would have ordered it correctly, and lets it run
// before the sibling it actually needs.
func (ctx *dataFlowContext) buildDependencyGraph() [][]*Opcall {
	var g [][]*Opcall
	savedHere := ctx.ev.Here
	for _, a := range ctx.all {
		ctx.ev.Here = a.where
		for _, path := range a.Dependencies(ctx.ev, ctx.locs) {
			if b := ctx.findDependency(path); b != nil {
				g = append(g, []*Opcall{b, a})
			}
		}
	}
	ctx.ev.Here = savedHere
	return g
}

// findDependency finds the dependency for a path in the operators map.
func (ctx *dataFlowContext) findDependency(path *tree.Cursor) *Opcall {
	// Try path as-is
	if b, found := ctx.all[path.String()]; found {
		return b
	}

	// Try canonical path
	if canon, err := path.Canonical(ctx.ev.Tree); err == nil {
		if b, found := ctx.all[canon.String()]; found {
			return b
		}
	}

	// Check parent paths
	return ctx.findParentDependency(path)
}

// findParentDependency checks parent paths for operators.
func (ctx *dataFlowContext) findParentDependency(path *tree.Cursor) *Opcall {
	parent := path.Copy()
	for len(parent.Nodes) > 0 {
		parent.Pop()
		if len(parent.Nodes) == 0 {
			break
		}

		if b, found := ctx.all[parent.String()]; found {
			return b
		}

		if canon, err := parent.Canonical(ctx.ev.Tree); err == nil {
			if b, found := ctx.all[canon.String()]; found {
				return b
			}
		}
	}
	return nil
}

// dataFlowGraph is the phase-scoped operator set and raw dependency edges
// computed before Kahn's-algorithm sorting collapses them into DataFlow's
// flat topological order. computeDataFlowGraph is the single place both
// DataFlow's sequential sort and the public DependencyGraph projection
// (depgraph.go) get this data from, so the two can never derive two
// different dependency graphs for the same document and phase - see the
// "three incompatible forms" note on Wave C's C9a plan section for why
// that matters.
type dataFlowGraph struct {
	ctx   *dataFlowContext
	edges [][]*Opcall // each element is {dependency, dependent}: edges[i][0] must run before edges[i][1]
}

// computeDataFlowGraph scans ev.Tree for phase's operator calls and builds
// the raw dependency edge list: everything DataFlow does before handing
// off to kahnSort. Extracted out of DataFlow (which now just calls this
// and sorts the result) so depgraph.go's DependencyGraph can consume the
// identical operator set and edges DataFlow's own sequential sort uses,
// rather than re-deriving them with a second, possibly drifting, tree
// walk.
func (ev *Evaluator) computeDataFlowGraph(phase OperatorPhase) (*dataFlowGraph, error) {
	ev.Here = &tree.Cursor{}
	log.DEBUG("DataFlow: starting phase %v", phase)

	ctx := newDataFlowContext(ev, phase)

	log.DEBUG("DataFlow: scanning tree for phase %d", phase)
	ctx.scanValue(ev.Tree)
	log.DEBUG("DataFlow: found %d operators after scan", len(ctx.all))

	if len(ev.CherryPickPaths) > 0 {
		log.DEBUG("DataFlow: Filtering operators for cherry-pick paths: %v", ev.CherryPickPaths)
		ctx.all = ev.filterOperatorsForCherryPick(ctx.all)
	}

	g := ctx.buildDependencyGraph()

	if len(ev.Only) > 0 {
		var err error
		g, err = ev.applyCherryPickFilter(g, ctx)
		if err != nil {
			return nil, err
		}
	}

	for i, node := range g {
		log.TRACE("data flow -- g[%d] is { %s:%s, %s:%s }\n", i, node[0].where, node[0].src, node[1].where, node[1].src)
	}

	return &dataFlowGraph{ctx: ctx, edges: g}, nil
}

// DataFlow computes the dependency-ordered list of operators for a given phase.
func (ev *Evaluator) DataFlow(phase OperatorPhase) ([]*Opcall, error) {
	dfg, err := ev.computeDataFlowGraph(phase)
	if err != nil {
		return nil, err
	}

	sortedKeys := ev.getSortedKeys(dfg.ctx.all, dfg.ctx.insertionOrder)
	ops, err := ev.kahnSort(dfg.edges, dfg.ctx.all, sortedKeys)
	if err != nil {
		return nil, err
	}

	if len(dfg.ctx.errors.Errors) > 0 {
		return nil, dfg.ctx.errors
	}
	return ops, nil
}

// applyCherryPickFilter applies cherry-pick filtering to the dependency graph.
func (ev *Evaluator) applyCherryPickFilter(g [][]*Opcall, ctx *dataFlowContext) ([][]*Opcall, error) {
	picks := make([]*tree.Cursor, len(ev.Only))
	for i, s := range ev.Only {
		c, err := tree.ParseCursor(s)
		if err != nil {
			return nil, errtext.Colorf("@*{invalid --cherry-pick path '%s': %s}", s, err)
		}
		picks[i] = c
	}

	g = ev.filterFirstOps(g, picks)

	newAll, err := ev.collectCherryPickedOps(ctx.all)
	if err != nil {
		return nil, err
	}
	ctx.all = newAll

	ev.addGraphDependencies(g, ctx)
	return g, nil
}

// filterFirstOps returns ops related to cherry-picked paths.
func (ev *Evaluator) filterFirstOps(ops [][]*Opcall, picks []*tree.Cursor) [][]*Opcall {
	final := make([][]*Opcall, 0)
	for i, op := range ops {
		for _, pick := range picks {
			if pick.Contains(op[1].canonical) {
				final = append(final, op)
				ops[i] = nil
				log.TRACE("data flow - adding [%s: %s, %s: %s] to data flow set (it matched --cherry-pick %s)",
					op[0].canonical, op[0].src, op[1].canonical, op[1].src, pick)
				break
			}
		}
	}

	for ev.filterCherryPickDeps(&final, &ops) > 0 {
	}
	return final
}

// filterCherryPickDeps migrates dependencies from in to out.
func (ev *Evaluator) filterCherryPickDeps(out, in *[][]*Opcall) int {
	l := make([][]*Opcall, 0)
	for i, candidate := range *in {
		if candidate == nil {
			continue
		}
		for _, op := range *out {
			if candidate[1] == op[0] {
				log.TRACE("data flow - adding [%s: %s, %s: %s] to data flow set (it matched {%s})",
					candidate[0].canonical, candidate[0].src,
					candidate[1].canonical, candidate[1].src, op[0].canonical)
				l = append(l, candidate)
				(*in)[i] = nil
				break
			}
		}
	}
	*out = append(*out, l...)
	return len(l)
}

// collectCherryPickedOps collects ops under cherry-picked paths.
func (ev *Evaluator) collectCherryPickedOps(all map[string]*Opcall) (map[string]*Opcall, error) {
	newAll := map[string]*Opcall{}
	for path, op := range all {
		for _, pickedPath := range ev.Only {
			cursor, err := tree.ParseCursor(pickedPath)
			if err != nil {
				return nil, errtext.Colorf("@*{invalid --cherry-pick path '%s': %s}", pickedPath, err)
			}
			if cursor.Contains(op.canonical) {
				newAll[path] = op
			}
		}
	}
	return newAll, nil
}

// addGraphDependencies adds dependencies from the graph to the context.
func (ev *Evaluator) addGraphDependencies(g [][]*Opcall, ctx *dataFlowContext) {
	for _, ops := range g {
		if _, exists := ctx.all[ops[0].canonical.String()]; !exists {
			ctx.insertionOrder = append(ctx.insertionOrder, ops[0].canonical.String())
		}
		ctx.all[ops[0].canonical.String()] = ops[0]
		if _, exists := ctx.all[ops[1].canonical.String()]; !exists {
			ctx.insertionOrder = append(ctx.insertionOrder, ops[1].canonical.String())
		}
		ctx.all[ops[1].canonical.String()] = ops[1]
	}
}

// getSortedKeys returns keys in the appropriate order.
func (ev *Evaluator) getSortedKeys(all map[string]*Opcall, insertionOrder []string) []string {
	if ev.DataflowOrder == "insertion" {
		return insertionOrder
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// kahnSort performs Kahn's topological sort algorithm.
func (ev *Evaluator) kahnSort(g [][]*Opcall, all map[string]*Opcall, sortedKeys []string) ([]*Opcall, error) {
	ops := []*Opcall{}
	wave := 0

	for len(all) > 0 {
		wave++
		free := ev.findFreeNodes(g, all, sortedKeys)
		if len(free) == 0 {
			return nil, ev.newCycleError(g, all)
		}

		for _, node := range free {
			log.TRACE("data flow: [%d] wave %d, op %s: %s", len(ops), wave, node.where, node.src)
			ops = append(ops, node)
			g = ev.removeNodeFromGraph(g, node)
		}
	}
	return ops, nil
}

// newCycleError builds the error reported when kahnSort stalls.
//
// At the stall, findFreeNodes has already deleted every scheduled node
// from all, and removeNodeFromGraph has dropped their edges from g, so
// both hold exactly the blocked remainder. That remainder is not the
// cycle: any node whose dependency chain leads into a cycle stays
// blocked forever without being on it. extractCycle reports only the
// gray-stack slice, so those bystanders are never named.
//
// Everything here runs on the error path only. If extraction somehow
// finds nothing, we fall back to the original one-line message rather
// than report an empty block.
func (ev *Evaluator) newCycleError(g [][]*Opcall, all map[string]*Opcall) error {
	dependents, order := dependentsFromEdges(g)
	paths := extractCycle(dependents, order)
	if len(paths) == 0 {
		return errtext.Colorf("@*{cycle detected in operator data-flow graph}")
	}
	return buildCycleError(ev.Sources, paths, all)
}

// findFreeNodes finds all nodes with no incoming dependencies.
func (ev *Evaluator) findFreeNodes(g [][]*Opcall, all map[string]*Opcall, sortedKeys []string) []*Opcall {
	freeNodeMap := make(map[string]*Opcall)
	for k, node := range all {
		called := false
		for _, pair := range g {
			if pair[1] == node {
				called = true
				break
			}
		}
		if !called {
			freeNodeMap[k] = node
		}
	}

	l := []*Opcall{}
	for _, k := range sortedKeys {
		if node, isFree := freeNodeMap[k]; isFree {
			delete(all, k)
			l = append(l, node)
		}
	}
	return l
}

// removeNodeFromGraph removes all dependencies on a node.
func (ev *Evaluator) removeNodeFromGraph(old [][]*Opcall, n *Opcall) [][]*Opcall {
	l := make([][]*Opcall, 0, len(old))
	for _, pair := range old {
		if pair[0] != n {
			l = append(l, pair)
		}
	}
	return l
}

// RunOps executes a list of operators in order, collecting any errors.
func (ev *Evaluator) RunOps(ops []*Opcall) error {
	log.DEBUG("patching up YAML by evaluating outstanding operators\n")

	errors := MultiError{Errors: []error{}}
	for _, op := range ops {
		err := ev.RunOp(op)
		if err != nil {
			errors.Append(err)
		}
	}

	if len(errors.Errors) > 0 {
		return errors
	}
	return nil
}

// Prune removes specified paths from the evaluated YAML tree.
func (ev *Evaluator) Prune(paths []string) error {
	log.DEBUG("pruning %d paths from the final YAML structure", len(paths))
	for _, path := range paths {
		c, err := tree.ParseCursor(path)
		if err != nil {
			return err
		}

		key := c.Component(-1)
		parent := c.Copy()
		parent.Pop()
		o, err := parent.Resolve(ev.Tree)
		if err != nil {
			continue
		}

		switch v := o.(type) {
		case map[string]interface{}:
			log.DEBUG("  pruning %s", path)
			delete(v, key)

		case []interface{}:
			list := v
			if true {
				if idx, err := strconv.Atoi(key); err == nil {
					// Check if index is valid
					if idx < 0 || idx >= len(list) {
						log.DEBUG("  skipping prune of out-of-bounds index %d in array of length %d", idx, len(list))
						continue
					}

					parent.Pop()
					if s, err := parent.Resolve(ev.Tree); err == nil {
						if reflect.TypeOf(s).Kind() == reflect.Map {
							parentName := c.Component(-2)
							log.DEBUG("  pruning index %d of array '%s'", idx, parentName)

							// Create new slice without the element at idx
							replacement := make([]interface{}, 0, len(list)-1)
							replacement = append(replacement, list[:idx]...)
							if idx+1 < len(list) {
								replacement = append(replacement, list[idx+1:]...)
							}

							if sMap, ok := s.(map[string]interface{}); ok {
								delete(sMap, parentName)
								sMap[parentName] = replacement
							}
						}
					}
				}
			}

		default:
			log.DEBUG("  I don't know how to prune %s\n    value=%v\n", path, o)
		}
	}
	log.DEBUG("")
	return nil
}

// SortPaths sorts all paths (keys in map) using the provided sort-key (respective value).
func (ev *Evaluator) SortPaths(pathKeyMap map[string]string) error {
	log.DEBUG("sorting %d paths in the final YAML structure", len(pathKeyMap))
	for path, sortBy := range pathKeyMap {
		log.DEBUG("  sorting path %s (sort-key %s)", path, sortBy)

		cursor, err := tree.ParseCursor(path)
		if err != nil {
			return err
		}

		value, err := cursor.Resolve(ev.Tree)
		if err != nil {
			return err
		}

		switch value.(type) {
		case []interface{}:
			// no-op, that's what we want ...

		case map[string]interface{}:
			return tree.TypeMismatchError{
				Path:   []string{path},
				Wanted: "a list",
				Got:    "a map",
			}

		default:
			return tree.TypeMismatchError{
				Path:   []string{path},
				Wanted: "a list",
				Got:    "a scalar",
			}
		}

		if valueList, ok := value.([]interface{}); ok {
			if err := SortList(path, valueList, sortBy); err != nil {
				return err
			}
		}
	}

	log.DEBUG("")
	return nil
}

// CherryPick filters the tree to only include the specified paths.
func (ev *Evaluator) CherryPick(paths []string) error {
	log.DEBUG("cherry-picking %d paths from the final YAML structure", len(paths))

	if len(paths) > 0 {
		// This will serve as the replacement tree ...
		replacement := make(map[string]interface{})

		for _, path := range paths {
			cursor, err := tree.ParseCursor(path)
			if err != nil {
				return err
			}

			// These variables will potentially be modified (depending on the structure)
			var cherryName string
			var cherryValue interface{}

			// Resolve the value that needs to be cherry picked
			cherryValue, err = cursor.Resolve(ev.Tree)
			if err != nil {
				return err
			}

			// Name of the parameter of the to-be-picked value
			cherryName = cursor.Nodes[len(cursor.Nodes)-1]

			// Since the cherry can be deep down the structure, we need to go down
			// (or up, depending how you read it) the structure to include the parent
			// names of the respective cherry. The pointer will be reassigned with
			// each level.
			pointer := cursor
			for pointer != nil {
				parent := pointer.Copy()
				parent.Pop()

				if parent.String() == "" {
					// Empty parent string means we reached the root, setting the pointer nil to stop processing ...
					pointer = nil

					// ... create the final cherry wrapped in its container ...
					tmp := make(map[string]interface{})
					tmp[cherryName] = cherryValue

					// ... and add it to the replacement map
					log.DEBUG("Merging '%s' into the replacement tree", path)
					m := &merger.Merger{AppendByDefault: true}
					merged := m.MergeObj(tmp, replacement, path)
					if err := m.Error(); err != nil {
						return err
					}

					if mergedMap, ok := merged.(map[string]interface{}); ok {
						replacement = mergedMap
					}
				} else {
					// Reassign the pointer to the parent and restructre the current cherry value to address the parent structure and name
					pointer = parent

					// Depending on the type of the parent, either a map or a list is created for the new parent of the cherry value
					if obj, err := parent.Resolve(ev.Tree); err == nil {
						switch obj.(type) {
						case map[string]interface{}:
							tmp := make(map[string]interface{})
							tmp[cherryName] = cherryValue

							cherryName = parent.Nodes[len(parent.Nodes)-1]
							cherryValue = tmp

						case []interface{}:
							tmp := []interface{}{cherryValue}

							cherryName = parent.Nodes[len(parent.Nodes)-1]
							cherryValue = tmp

						default:
							return errtext.Colorf("@*{Unsupported type detected, %s is neither a map nor a list}", parent.String())
						}
					} else {
						return err
					}
				}
			}
		}

		// replace the existing tree with a new one that contain the cherry-picks
		ev.Tree = replacement
	}

	log.DEBUG("")
	return nil
}

// CheckForCycles detects self-referencing structures up to maxDepth levels.
func (ev *Evaluator) CheckForCycles(maxDepth int) error {
	log.DEBUG("checking for cycles in final YAML structure")

	var check func(o interface{}, depth int) error
	check = func(o interface{}, depth int) error {
		if depth == 0 {
			return errtext.Colorf("@*{Hit max recursion depth. You seem to have a self-referencing dataset}")
		}

		switch val := o.(type) {
		case literal.Value:
			// A wrapped value can refer to itself as well.
			return check(val.Value(), depth-1)

		case []interface{}:
			for _, v := range val {
				if err := check(v, depth-1); err != nil {
					return err
				}
			}

		case map[string]interface{}:
			for _, v := range val {
				if err := check(v, depth-1); err != nil {
					return err
				}
			}
		}

		return nil
	}

	err := check(ev.Tree, maxDepth)
	if err != nil {
		log.DEBUG("error: %s\n", err)
		return err
	}

	log.DEBUG("no cycles detected.\n")
	return nil
}

// RunOp executes a single operator and applies its result to the tree.
// It is the sequential entry point: computeOp (may perform Vault/AWS/NATS
// I/O, reads ev.Tree) followed immediately by applyResponse (mutates
// ev.Tree). The parallel scheduler (evaluator_parallel.go) calls the two
// halves separately so it can run computeOp for multiple operators
// concurrently and serialize only applyResponse - see runOpsWithScheduler.
func (ev *Evaluator) RunOp(op *Opcall) error {
	resp, oldValue, err := ev.computeOp(op)
	if err != nil {
		return err
	}
	return ev.scrubLiteralError(op, ev.applyResponse(op, resp, oldValue))
}

// computeOp runs the operator itself (op.Run), which may perform Vault,
// AWS, or NATS I/O and reads ev.Tree, but does not mutate it. It also
// captures the tree's current value at op.where for memory/history
// recording, before applyResponse overwrites it. Safe to call concurrently
// for multiple operators within the same dependency-free scheduling wave,
// since DataFlow guarantees no same-wave operator reads a path another
// same-wave operator writes, and no writes happen until applyResponse.
func (ev *Evaluator) computeOp(op *Opcall) (resp *Response, oldValue interface{}, err error) {
	if ev.memory != nil && ev.memory.IsEnabled() && op.where != nil {
		oldValue, _ = op.where.Resolve(ev.Tree)
	}

	// Opcall.Run sets ev.Here/ev.Target as ambient per-call context for the
	// duration of op.op.Run (and restores them after), so a nested operator
	// or a "grab"-style relative-path resolution can read "where am I right
	// now". That is safe for one evaluator running one call at a time, but
	// two concurrent computeOp calls sharing the same *Evaluator would race
	// on those two fields and could observe each other's Here/Target
	// mid-flight, corrupting relative-path resolution, not just tripping
	// the race detector. Give each call its own shallow copy of the
	// evaluator so Here/Target mutations are goroutine-local. Every other
	// field (Tree, Deps, engine, memory, ...) is a reference type or a
	// value set once before evaluation begins, so sharing it across the
	// copy is safe for the read-only access computeOp performs.
	evCopy := *ev
	resp, err = op.Run(&evCopy)
	if err != nil {
		return resp, oldValue, ev.scrubLiteralError(op, err)
	}
	ev.guardLiteralResponse(op, resp)
	return resp, oldValue, nil
}

// applyResponse mutates ev.Tree with the operator's result and records
// history if enabled. Callers running multiple operators' computeOp
// concurrently must still call applyResponse serially (e.g. under a
// mutex) - ev.Tree is a plain map/slice tree, not safe for concurrent
// writes.
//
//nolint:gocyclo // operator response application requires handling multiple response types and parent structures
func (ev *Evaluator) applyResponse(op *Opcall, resp *Response, oldValue interface{}) error {
	switch resp.Type {
	case Replace:
		log.DEBUG("executing a Replace instruction on %s", op.where)
		key := op.where.Component(-1)
		parent := op.where.Copy()
		parent.Pop()

		o, err := parent.Resolve(ev.Tree)
		if err != nil {
			log.DEBUG("  error: %s\n  continuing\n", err)
			return err
		}
		switch val := o.(type) {
		case []interface{}:
			i, err := strconv.ParseUint(key, 10, 0)
			if err != nil {
				log.DEBUG("  error: %s\n  continuing\n", err)
				return err
			}
			val[i] = resp.Value

		case map[string]interface{}:
			val[key] = resp.Value

		default:
			err := tree.TypeMismatchError{
				Path:   parent.Nodes,
				Wanted: "a map or a list",
				Got:    "a scalar",
			}
			log.DEBUG("  error: %s\n  continuing\n", err)
			return err
		}
		log.DEBUG("")

		// Record change if memory tracking is enabled
		if ev.memory != nil && ev.memory.IsEnabled() && op.where != nil {
			opName := "operator"
			if op.op != nil {
				// Get operator name from the operator
				opName = getOperatorName(op.op)
			}
			_ = ev.memory.RecordEvalChange(op.where.String(), oldValue, resp.Value, opName)
		}

	case Inject:
		log.DEBUG("executing an Inject instruction on %s", op.where)
		key := op.where.Component(-1)
		parent := op.where.Copy()
		parent.Pop()

		o, err := parent.Resolve(ev.Tree)
		if err != nil {
			log.DEBUG("  error: %s\n  continuing\n", err)
			return err
		}

		m, ok := o.(map[string]interface{})
		if !ok {
			return fmt.Errorf("inject target is not a map")
		}
		delete(m, key)

		respMap, ok := resp.Value.(map[string]interface{})
		if !ok {
			return fmt.Errorf("inject value is not a map")
		}
		for k, v := range respMap {
			path := fmt.Sprintf("%s.%s", parent, k)
			log.DEBUG("Inject: parent=%s, k=%s, path=%s", parent, k, path)
			_, set := m[k]
			if !set {
				log.DEBUG("  %s is not set, using the injected value", path)
				m[k] = v
			} else {
				// Check type of existing and injected values for proper merging
				// the quoted spelling keeps a dotted parent node or key
				// whole if a prune marker under it is queued
				quoted := fmt.Sprintf("%s.%s", parent.Quoted(), tree.QuoteNode(k))
				mrg := &merger.Merger{AppendByDefault: true}
				merged := mrg.MergeObjQuoted(v, m[k], path, quoted)
				if err := mrg.Error(); err != nil {
					return err
				}
				m[k] = merged
				ev.queuePrunePathsFrom(mrg)
			}
		}
	}
	return nil
}

// queuePrunePathsFrom hands the paths a nested merge queued for pruning to
// the run's prune list. (( inject )) merges the injected map into the
// target with the same merger the merge phase uses, so an injected value
// landing on a (( prune )) marker queues that path against that merger
// rather than against the run. Spruce keeps a single package-level list, so
// its nested merges reach the same place on their own; graft's list belongs
// to the run, and without this hand-off the queued path would be dropped
// and the key would survive into the output.
func (ev *Evaluator) queuePrunePathsFrom(mrg *merger.Merger) {
	metadata := mrg.GetMetadata()
	if metadata == nil || len(metadata.PrunePaths) == 0 {
		return
	}

	state := GetEngine(ev).GetOperatorState()
	queued := state.GetKeysToPrune()
	for _, path := range metadata.PrunePaths {
		if slices.Contains(queued, path) {
			continue
		}
		log.DEBUG("queueing '%s' for pruning, from a merge inside an operator", path)
		state.AddKeyToPrune(path)
		queued = append(queued, path)
	}
}

// RunPhase executes all operators belonging to the specified phase.
func (ev *Evaluator) RunPhase(p OperatorPhase) error {
	err := SetupOperators(p)
	if err != nil {
		return err
	}

	op, err := ev.DataFlow(p)
	if err != nil {
		return err
	}

	return ev.RunOps(op)
}

// Run executes all evaluation phases with optional pruning and cherry-picking.
//
// Run works on the operator state of the engine the evaluator holds (see
// SetEngine), not on a state of its own, so it is not safe on an engine
// shared with concurrent calls. Under a non-empty REDACT it also turns on
// that engine's skip and redact settings for good. An Evaluator is meant
// for one call; to evaluate on a shared engine, use Engine.Evaluate.
func (ev *Evaluator) Run(prune, picks []string) error {
	errors := MultiError{Errors: []error{}}
	paramErrs := MultiError{Errors: []error{}}

	eng := GetEngine(ev)
	state := eng.GetOperatorState()

	if os.Getenv("REDACT") != "" {
		log.DEBUG("Setting vault & aws & nats operators to redact keys")
		state.SetSkipVault(true)
		state.SetSkipAws(true)
		state.SetSkipNats(true)
		state.SetRedactMode(true)
	}

	if !ev.SkipEval {
		ev.Only = picks
		errors.Append(ev.RunPhase(MergePhase))
		paramErrs.Append(ev.RunPhase(ParamPhase))
		if len(paramErrs.Errors) > 0 {
			return paramErrs
		}

		errors.Append(ev.RunPhase(EvalPhase))
	}

	// this is a big failure...
	if err := ev.CheckForCycles(4096); err != nil {
		return err
	}

	// post-processing: prune
	for _, p := range prune {
		state.AddKeyToPrune(p)
	}
	prunePaths := state.GetKeysToPrune()
	state.ResetKeysToPrune()
	log.DEBUG("Final prune list contains %d paths: %v", len(prunePaths), prunePaths)
	errors.Append(ev.Prune(prunePaths))

	// post-processing: sorting
	sortPaths := state.GetPathsToSort()
	state.ResetPathsToSort()
	errors.Append(ev.SortPaths(sortPaths))

	// post-processing: cherry-pick
	errors.Append(ev.CherryPick(picks))

	if len(errors.Errors) > 0 {
		return errors
	}
	return nil
}

// isUnderPath checks if an operator path is under a cherry-pick path.
// This is the core of selective evaluation - it determines whether an operator
// should be evaluated based on cherry-pick paths.
//
// The function handles special cases like:
// - Named array entries (e.g., "jobs.web" matching "jobs.0")
// - Exact path matches
// - Nested paths (e.g., "a.b.c" is under "a.b").
func (ev *Evaluator) isUnderPath(opPath, cherryPath string) bool {
	// Handle empty paths
	if opPath == "" || cherryPath == "" {
		return false
	}

	// Parse both paths into cursors
	opCursor, err := tree.ParseCursor(opPath)
	if err != nil {
		return false
	}
	cherryCursor, err := tree.ParseCursor(cherryPath)
	if err != nil {
		return false
	}

	// Check if either cursor has no nodes
	if len(opCursor.Nodes) == 0 || len(cherryCursor.Nodes) == 0 {
		return false
	}

	// Check if opCursor starts with cherryCursor
	if len(opCursor.Nodes) < len(cherryCursor.Nodes) {
		return false
	}

	// Compare each segment with context
	currentPath := &tree.Cursor{}
	for i, cherryNode := range cherryCursor.Nodes {
		if !ev.segmentsMatchWithContext(opCursor.Nodes[i], cherryNode, currentPath) {
			return false
		}
		// Build up the current path as we go
		currentPath.Push(opCursor.Nodes[i])
	}

	return true
}

// segmentsMatchWithContext compares two path segments with access to the data structure.
//
//nolint:gocyclo // path segment matching with named array entries requires multiple type checks
func (ev *Evaluator) segmentsMatchWithContext(opSegment, cherrySegment string, currentPath *tree.Cursor) bool {
	// Direct string comparison first
	if opSegment == cherrySegment {
		return true
	}

	// Check if both are numeric indices
	opIdx, opErr := strconv.Atoi(opSegment)
	cherryIdx, cherryErr := strconv.Atoi(cherrySegment)

	// Both are numeric - they should match exactly
	if opErr == nil && cherryErr == nil {
		return opIdx == cherryIdx
	}

	// One is numeric and one is not - check if they refer to the same array element
	if (opErr == nil) != (cherryErr == nil) {
		// Try to resolve the current path to get the actual array
		if len(currentPath.Nodes) > 0 {
			obj, err := currentPath.Resolve(ev.Tree)
			if err == nil {
				if arr, ok := obj.([]interface{}); ok {
					// If we have a numeric index and a name, check if they match
					if opErr == nil {
						// opSegment is numeric, cherrySegment is a name
						if opIdx >= 0 && opIdx < len(arr) {
							// Check if the element at this index has the expected name
							if elem, ok := arr[opIdx].(map[string]interface{}); ok {
								// Check common name fields
								for _, nameField := range tree.NameFields {
									if name, exists := elem[nameField]; exists {
										if nameStr, ok := name.(string); ok && nameStr == cherrySegment {
											return true
										}
									}
								}
							}
						}
					} else {
						// cherrySegment is numeric, opSegment is a name
						if cherryIdx >= 0 && cherryIdx < len(arr) {
							// Check if the element at the cherry index has the op name
							if elem, ok := arr[cherryIdx].(map[string]interface{}); ok {
								for _, nameField := range tree.NameFields {
									if name, exists := elem[nameField]; exists {
										if nameStr, ok := name.(string); ok && nameStr == opSegment {
											return true
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	return false
}

// filterOperatorsForCherryPick filters operators to only those needed for cherry-picked paths.
// This implements the selective evaluation strategy:
//
// 1. First identifies all operators under cherry-picked paths
// 2. Then recursively collects all their dependencies (transitive closure)
// 3. Returns only the operators that are needed for evaluation
//
// This significantly reduces the number of operators evaluated in large documents,
// improving performance when only specific sections are needed.
//
//nolint:gocyclo // transitive dependency collection requires multiple iteration passes
func (ev *Evaluator) filterOperatorsForCherryPick(all map[string]*Opcall) map[string]*Opcall {
	if len(ev.CherryPickPaths) == 0 {
		return all // No filtering needed
	}

	needed := make(map[string]bool)
	result := make(map[string]*Opcall)

	log.DEBUG("filterOperatorsForCherryPick: Filtering operators for cherry-pick paths: %v", ev.CherryPickPaths)

	// Step 1: Mark operators under cherry-picked paths
	for path := range all {
		for _, cherryPath := range ev.CherryPickPaths {
			if ev.isUnderPath(path, cherryPath) {
				needed[path] = true
				log.DEBUG("filterOperatorsForCherryPick: Operator at %s is under cherry-pick path %s", path, cherryPath)
				break
			}
		}
	}

	// If no operators were found under cherry-pick paths, include all operators
	// This handles cases where cherry-pick paths don't contain operators directly
	if len(needed) == 0 {
		log.DEBUG("filterOperatorsForCherryPick: No operators found under cherry-pick paths, including all")
		return all
	}

	// Step 2: Collect transitive dependencies - but only check operators in the dependency list
	// We need to look at what the needed operators depend on, not what depends on them
	changed := true
	iterations := 0
	maxIterations := 100 // Prevent infinite loops

	for changed && iterations < maxIterations {
		changed = false
		iterations++

		// Create a snapshot of currently needed paths to iterate over
		currentNeeded := make([]string, 0, len(needed))
		for path := range needed {
			currentNeeded = append(currentNeeded, path)
		}

		// For each needed operator, add its dependencies
		for _, path := range currentNeeded {
			if op, exists := all[path]; exists {
				// Get dependencies for this operator
				deps := op.Dependencies(ev, nil)
				for _, dep := range deps {
					// Try to resolve the dependency to a canonical path
					depPath := dep.String()

					// Check if this dependency corresponds to an operator
					if _, isOp := all[depPath]; isOp && !needed[depPath] {
						needed[depPath] = true
						changed = true
						log.DEBUG("filterOperatorsForCherryPick: Added operator dependency %s for operator at %s", depPath, path)
					}
				}
			}
		}
	}

	if iterations >= maxIterations {
		log.DEBUG("filterOperatorsForCherryPick: Warning - reached maximum iterations while collecting dependencies")
	}

	// Step 3: Build filtered result
	for path, op := range all {
		if needed[path] {
			result[path] = op
		}
	}

	log.DEBUG("filterOperatorsForCherryPick: Filtered from %d to %d operators", len(all), len(result))

	return result
}
