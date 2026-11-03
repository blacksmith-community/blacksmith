package graft

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/fivetwenty-io/graft/internal/parallel"
	"github.com/fivetwenty-io/graft/log"
	"github.com/fivetwenty-io/graft/pkg/graft/tree"
)

// parallelStats tracks global statistics for parallel execution.
var parallelStats struct {
	totalOps     atomic.Int64
	totalPhases  atomic.Int64
	totalErrors  atomic.Int64
	schedulerRun atomic.Int64
}

// resetParallelStats resets all parallel execution statistics to zero.
func resetParallelStats() {
	parallelStats.totalOps.Store(0)
	parallelStats.totalPhases.Store(0)
	parallelStats.totalErrors.Store(0)
	parallelStats.schedulerRun.Store(0)
}

// RunPhaseParallel runs a phase using DAG-based parallel scheduling.
//
// It computes the dataflow dependency graph for the phase and builds a
// parallel.Scheduler with one task per operator. Tasks that share no
// dependencies are placed in the same wave by the scheduler; within a
// wave, each operator's computeOp (including any Vault/AWS/NATS I/O it
// performs) runs concurrently on the engine's WorkerPool - see
// runOpsWithScheduler.
//
// Tree mutations (applyResponse) are serialized with a mutex because the
// underlying map is not safe for concurrent writes, and are applied in a
// fixed task-ID-sorted order per wave rather than completion order, so
// output is identical regardless of goroutine scheduling. Operators
// implementing OrderSensitive are dispatched one at a time instead of
// concurrently.
//
// If no WorkerPool is available on the engine, execution falls back to
// the sequential RunOps path.
func (ev *Evaluator) RunPhaseParallel(p OperatorPhase) error {
	err := SetupOperators(p)
	if err != nil {
		return err
	}

	ops, err := ev.DataFlow(p)
	if err != nil {
		return err
	}

	if len(ops) == 0 {
		return nil
	}

	// Retrieve the worker pool from the engine
	pool := getWorkerPool(ev)
	if pool == nil {
		log.DEBUG("parallel: no worker pool, falling back to sequential")
		return ev.RunOps(ops)
	}

	return ev.runOpsWithScheduler(context.Background(), pool, ops)
}

// RunOpsParallel executes a pre-sorted list of operations using the
// parallel scheduler. Falls back to sequential if no pool is available.
func (ev *Evaluator) RunOpsParallel(ops []*Opcall) error {
	if len(ops) == 0 {
		return nil
	}

	pool := getWorkerPool(ev)
	if pool == nil {
		log.DEBUG("parallel: no worker pool, falling back to sequential")
		return ev.RunOps(ops)
	}

	return ev.runOpsWithScheduler(context.Background(), pool, ops)
}

// resolveOpIDForDependency maps a dependency cursor to the task ID of the
// operator that produces it, mirroring dataFlowContext.findDependency /
// findParentDependency (evaluator.go) exactly: try the path as-is, then its
// canonical form, then walk parent prefixes (also trying each parent's
// canonical form) until a producing operator is found. This parent-path
// fallback is required because a dependency can reference a subtree of an
// operator's result (e.g. `grab meta.vm.small` where `meta.vm` is itself
// `(( grab ... ))`) - the operator's own path (`meta.vm`) never equals the
// dependency path (`meta.vm.small`), so an exact-match-only lookup misses
// the edge. Sequential and parallel evaluation must derive identical
// dependency graphs; this keeps the two lookups in lockstep.
func resolveOpIDForDependency(ev *Evaluator, opIDMap map[string]string, path *tree.Cursor) (string, bool) {
	if id, found := opIDMap[path.String()]; found {
		return id, true
	}

	if canon, err := path.Canonical(ev.Tree); err == nil {
		if id, found := opIDMap[canon.String()]; found {
			return id, true
		}
	}

	parent := path.Copy()
	for len(parent.Nodes) > 0 {
		parent.Pop()
		if len(parent.Nodes) == 0 {
			break
		}

		if id, found := opIDMap[parent.String()]; found {
			return id, true
		}

		if canon, err := parent.Canonical(ev.Tree); err == nil {
			if id, found := opIDMap[canon.String()]; found {
				return id, true
			}
		}
	}

	return "", false
}

// runOpsWithScheduler builds a parallel.Scheduler from the given ops,
// computes inter-op dependencies, and executes via the pool.
//
// Within a wave (operators with no dependency edge between them per the
// DAG), computeOp for each operator - which may perform Vault/AWS/NATS
// I/O - runs truly concurrently on the pool. applyResponse (the ev.Tree
// mutation) is always serialized and applied in a fixed, sorted-by-path
// order so the final tree and any recorded history are identical
// regardless of which goroutine's I/O finished first - see
// scripts/check-parallel-determinism.sh. Operators implementing
// OrderSensitive (currently only static_ips) are dispatched one at a time
// instead, since their observable behavior depends on relative order
// among themselves in ways no dependency edge captures.
func (ev *Evaluator) runOpsWithScheduler(ctx context.Context, pool *parallel.WorkerPool, ops []*Opcall) error {
	parallelStats.totalPhases.Add(1)

	scheduler := parallel.NewScheduler()
	allLocs, opIDMap, opByID := indexOps(ops)
	ev.addSchedulerTasks(scheduler, ops, allLocs, opIDMap)

	parallelStats.schedulerRun.Add(1)

	// Compute dependency-ordered waves. Tasks within a wave have no
	// dependency relationship to each other per the DAG. scheduler.Schedule
	// builds each wave from Go map iteration, which is randomized per run,
	// so the wave slice's element order is not itself deterministic - it is
	// re-sorted by task ID immediately below.
	waves, err := scheduler.Schedule()
	if err != nil {
		parallelStats.totalErrors.Add(1)
		return err
	}

	// treeMu serializes every ev.Tree mutation: the sequential
	// (order-sensitive) group's fused RunOp calls, and the concurrent
	// group's applyResponse calls after their computeOp phase completes.
	var treeMu sync.Mutex

	errors := MultiError{Errors: []error{}}
	for _, wave := range waves {
		// Sort the wave by task ID (the operator's cursor path, e.g.
		// "jobs.static_z1...") before dispatch, independent of whatever
		// order scheduler.Schedule produced it in, so partitioning and
		// dispatch order below are reproducible across runs.
		sort.Slice(wave, func(i, j int) bool { return wave[i].ID < wave[j].ID })

		sequential, concurrent := partitionWave(wave, opByID)

		ev.runSequentialTasks(ctx, pool, &treeMu, sequential, opByID, &errors)

		if ev.runConcurrentTasks(ctx, pool, &treeMu, concurrent, opByID, &errors) {
			return errors
		}
	}

	if len(errors.Errors) > 0 {
		return errors
	}
	return nil
}

// indexOps builds the lookups the scheduler needs from the ops of one
// evaluation phase: every op location for dependency computation, a
// cursor-string to task ID map (a canonical path maps to the same task ID
// as the path it canonicalizes), and a task ID back to its *Opcall, since
// the scheduler's Task carries only an ID string plus arbitrary Data.
func indexOps(ops []*Opcall) (allLocs []*tree.Cursor, opIDMap map[string]string, opByID map[string]*Opcall) {
	allLocs = make([]*tree.Cursor, 0, len(ops))
	opIDMap = make(map[string]string, len(ops))
	opByID = make(map[string]*Opcall, len(ops))

	for _, op := range ops {
		if op.Where() == nil {
			continue
		}

		allLocs = append(allLocs, op.Where())

		id := op.Where().String()
		opIDMap[id] = id
		opByID[id] = op
		if op.canonical != nil {
			opIDMap[op.canonical.String()] = id
		}
	}

	return allLocs, opIDMap, opByID
}

// addSchedulerTasks registers one task per op, with the dependency edges
// its operator reports. This runs single-threaded, before any wave
// dispatch starts goroutines, so mutating ev.Here here is safe. A
// duplicate task ID (which canonical/alias paths can produce) is skipped
// rather than treated as an error.
func (ev *Evaluator) addSchedulerTasks(scheduler *parallel.Scheduler, ops []*Opcall, allLocs []*tree.Cursor, opIDMap map[string]string) {
	// Enforce the single-threaded promise above rather than trusting
	// it: overlapping computations would corrupt each other's ev.Here
	// and produce wrong dependency edges silently. Panicking here puts
	// the failure at the bug instead of in a mis-merged document.
	if atomic.AddInt32(&ev.depComputing, 1) != 1 {
		panic("graft: concurrent dependency computation on one Evaluator; ev.Here mutation requires single-threaded scheduling")
	}
	defer atomic.AddInt32(&ev.depComputing, -1)

	savedHere := ev.Here
	defer func() { ev.Here = savedHere }()

	for _, op := range ops {
		taskOp := op // capture for closure
		taskID := taskOp.Where().String()

		// ev.Here is set to this task's own path before computing its
		// dependencies, mirroring Opcall.Run and
		// dataFlowContext.buildDependencyGraph's sequential-path setup: an
		// operator whose Dependencies() resolves a path relative to
		// ev.Here (op_calc.go's calcBareNameDependencies, for a bare
		// named calc variable that is a sibling of the calc call's own
		// path) needs it to compute the right relative cursor, or the
		// sibling edge is silently never added.
		ev.Here = taskOp.Where()

		task := &parallel.Task{
			ID:           taskID,
			Dependencies: ev.dependencyIDs(taskOp, taskID, allLocs, opIDMap),
		}

		if err := scheduler.AddTask(task); err != nil {
			log.DEBUG("parallel: skipping duplicate task %s: %v", taskID, err)
			continue
		}
	}
}

// dependencyIDs maps the cursors op depends on to the task IDs of the ops
// that produce them, dropping self-edges and duplicates. ev.Here must
// already be set to op's own path - see addSchedulerTasks.
func (ev *Evaluator) dependencyIDs(op *Opcall, taskID string, allLocs []*tree.Cursor, opIDMap map[string]string) []string {
	if op.op == nil {
		return nil
	}

	var depIDs []string
	seen := make(map[string]bool)
	for _, depCursor := range op.Dependencies(ev, allLocs) {
		if id, ok := resolveOpIDForDependency(ev, opIDMap, depCursor); ok && id != taskID && !seen[id] {
			depIDs = append(depIDs, id)
			seen[id] = true
		}
	}
	return depIDs
}

// partitionWave splits an already-sorted wave into the operators that
// must run one at a time relative to each other (OrderSensitive) and the
// operators eligible for true concurrent computeOp dispatch. Order within
// each group is preserved from the caller's sort.
func partitionWave(wave []parallel.Task, opByID map[string]*Opcall) (sequential, concurrent []parallel.Task) {
	for _, task := range wave {
		op := opByID[task.ID]
		if op != nil && isOrderSensitiveOp(op) {
			sequential = append(sequential, task)
		} else {
			concurrent = append(concurrent, task)
		}
	}
	return sequential, concurrent
}

// runSequentialTasks dispatches the order-sensitive group one task at a
// time, compute and apply fused under treeMu, identical to pre-Wave-D
// behavior for these operators. Evaluation errors are collected in errs
// rather than aborting the wave.
func (ev *Evaluator) runSequentialTasks(ctx context.Context, pool *parallel.WorkerPool, treeMu *sync.Mutex, tasks []parallel.Task, opByID map[string]*Opcall, errs *MultiError) {
	for _, task := range tasks {
		op := opByID[task.ID]
		parallelStats.totalOps.Add(1)
		taskErr := pool.SubmitWaitContext(ctx, func(context.Context) error {
			treeMu.Lock()
			defer treeMu.Unlock()
			return ev.RunOp(op)
		})
		if taskErr != nil {
			parallelStats.totalErrors.Add(1)
			errs.Append(taskErr)
		}
	}
}

// computeResult is one concurrent task's computeOp outcome, held until
// every task in the wave has finished so the results can be applied to
// the tree serially, in the wave's fixed sorted order.
type computeResult struct {
	op       *Opcall
	resp     *Response
	oldValue interface{}
	err      error
}

// runConcurrentTasks fans computeOp (the potentially slow Vault/AWS/NATS
// I/O) out across the pool's workers, then applies every result serially
// in the order given. It reports whether the wave was abandoned - the
// caller's context was canceled or the pool shut down mid-wave - in which
// case no further wave may run.
func (ev *Evaluator) runConcurrentTasks(ctx context.Context, pool *parallel.WorkerPool, treeMu *sync.Mutex, tasks []parallel.Task, opByID map[string]*Opcall, errs *MultiError) bool {
	if len(tasks) == 0 {
		return false
	}

	results := make([]computeResult, len(tasks))
	var wg sync.WaitGroup
	wg.Add(len(tasks))

	for i, task := range tasks {
		idx := i
		op := opByID[task.ID]
		// SubmitBlocking (not SubmitContext): a wave can be far
		// wider than the pool's task queue (large manifests
		// routinely have 1000+ independent grab/vault/concat
		// calls in one wave), and SubmitContext's non-blocking
		// select turns a saturated queue into an immediate
		// ErrPoolFull - which this function would then record as
		// that operator's evaluation error, failing the merge
		// outright. SubmitBlocking blocks until a slot frees
		// instead, exerting backpressure on this loop rather than
		// rejecting work the pool merely hasn't drained yet.
		submitErr := pool.SubmitBlocking(ctx, func(context.Context) error {
			defer wg.Done()
			resp, oldValue, computeErr := ev.computeOp(op)
			results[idx] = computeResult{op: op, resp: resp, oldValue: oldValue, err: computeErr}
			return computeErr
		})
		if submitErr != nil {
			// The pool itself is shut down, or ctx was canceled
			// while blocked waiting for a slot - the task never
			// ran, so account for its wg.Done() here and record
			// the rejection as that task's error.
			wg.Done()
			results[idx] = computeResult{op: op, err: submitErr}
		}
	}

	// A bare wg.Wait() would block forever if the pool is
	// canceled after a task was already enqueued but before a
	// worker picks it up: workers abandon their current select
	// the instant the pool's internal context is done, without
	// draining taskQueue, so that task's deferred wg.Done() would
	// never fire. Select on the pool's cancellation signal (and
	// the caller's ctx) alongside the WaitGroup so a shutdown or
	// cancellation mid-wave returns a real error instead of
	// hanging - reachable from the library API when a consumer
	// shuts an in-flight engine's pool down concurrently.
	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
	case <-ctx.Done():
		parallelStats.totalErrors.Add(1)
		errs.Append(fmt.Errorf("wave evaluation canceled: %w", ctx.Err()))
		return true
	case <-pool.Canceled():
		parallelStats.totalErrors.Add(1)
		errs.Append(fmt.Errorf("worker pool shut down while a wave was in flight"))
		return true
	}

	for _, r := range results {
		parallelStats.totalOps.Add(1)
		if r.err != nil {
			parallelStats.totalErrors.Add(1)
			errs.Append(r.err)
			continue
		}

		treeMu.Lock()
		applyErr := ev.applyResponse(r.op, r.resp, r.oldValue)
		treeMu.Unlock()

		if applyErr != nil {
			parallelStats.totalErrors.Add(1)
			errs.Append(applyErr)
		}
	}

	return false
}

// isOrderSensitiveOp reports whether op's underlying Operator implements
// OrderSensitive and currently returns true for it.
func isOrderSensitiveOp(op *Opcall) bool {
	if op == nil || op.op == nil {
		return false
	}
	sensitive, ok := op.op.(OrderSensitive)
	return ok && sensitive.OrderSensitive()
}

// getWorkerPool extracts the WorkerPool from the evaluator's engine.
func getWorkerPool(ev *Evaluator) *parallel.WorkerPool {
	// During a run the evaluator holds a runEngine wrapping the shared
	// engine (see DefaultEngine.evaluate), and the pool belongs to the
	// engine it wraps.
	switch eng := GetEngine(ev).(type) {
	case *runEngine:
		return eng.GetWorkerPool()
	case *DefaultEngine:
		return eng.GetWorkerPool()
	default:
		return nil
	}
}

// ParallelExecutionStats returns statistics about parallel execution.
func ParallelExecutionStats() map[string]interface{} {
	total := parallelStats.totalOps.Load()
	return map[string]interface{}{
		"enabled":        total > 0,
		"total_ops":      total,
		"total_phases":   parallelStats.totalPhases.Load(),
		"total_errors":   parallelStats.totalErrors.Load(),
		"scheduler_runs": parallelStats.schedulerRun.Load(),
	}
}
