package graft

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/fivetwenty-io/graft/internal/oploc"
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
	return ev.runPhase(p, ev.RunOpsParallel)
}

// RunOpsParallel executes a pre-sorted list of operations using the
// parallel scheduler. Falls back to sequential if no pool is available.
// Like RunOps, it stops once the evaluator's context is done: an
// operator already running finishes, no other one starts, and the
// cancellation comes back alone.
func (ev *Evaluator) RunOpsParallel(ops []*Opcall) error {
	if len(ops) == 0 {
		return nil
	}

	pool := getWorkerPool(ev)
	if pool == nil {
		log.DEBUG("parallel: no worker pool, falling back to sequential")
		return ev.RunOps(ops)
	}

	return ev.runOpsWithScheduler(ev.context(), pool, ops)
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
//
// ctx stops the run between operators. It is checked before each wave
// and before each operator starts, and once it is done the run returns
// the cancellation alone, after every operator already running has
// finished, so nothing touches ev.Tree after this returns.
func (ev *Evaluator) runOpsWithScheduler(ctx context.Context, pool *parallel.WorkerPool, ops []*Opcall) error {
	parallelStats.totalPhases.Add(1)

	defer ev.startPending(ops)()

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

		if len(wave) > 0 {
			if err := opCanceled(ctx, opByID[wave[0].ID]); err != nil {
				return err
			}
		}

		sequential, concurrent := partitionWave(wave, opByID)

		if err := ev.runSequentialTasks(ctx, pool, &treeMu, sequential, opByID, &errors); err != nil {
			return err
		}

		if err := ev.runConcurrentTasks(ctx, pool, &treeMu, concurrent, opByID, &errors); err != nil {
			return err
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
// The locations are in canonical form, as the sequential engine hands
// them to Dependencies, so an operator that compares them with a
// canonical path finds an op under a list entry named by its name.
func indexOps(ops []*Opcall) (allLocs []*tree.Cursor, opIDMap map[string]string, opByID map[string]*Opcall) {
	allLocs = make([]*tree.Cursor, 0, len(ops))
	opIDMap = make(map[string]string, len(ops))
	opByID = make(map[string]*Opcall, len(ops))

	for _, op := range ops {
		if op.Where() == nil {
			continue
		}

		id := op.Where().String()
		opIDMap[id] = id
		opByID[id] = op
		if op.canonical != nil {
			opIDMap[op.canonical.String()] = id
			allLocs = append(allLocs, op.canonical)
		} else {
			allLocs = append(allLocs, op.Where())
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
	ev.locIndex = oploc.New(allLocs, ev.Tree)
	defer func() {
		ev.Here = savedHere
		ev.locIndex = nil
	}()

	// ops arrive in DataFlow's order, which already honors every soft
	// dependency the sequential sort kept. A soft dependency on an op
	// that comes earlier in that order is one the sort kept, and one on
	// an op that comes later is one it dropped to avoid a cycle, so each
	// task waits on its barriers' members that come before it. Each op
	// is taken once, at its first place in the order.
	tasks := make([]*schedTask, 0, len(ops))
	place := make(map[string]int, len(ops))
	barriers := make(map[string]*schedBarrier)
	memberOf := make(map[string][]*schedBarrier)
	for _, op := range ops {
		if op.Where() == nil {
			continue
		}
		taskID := op.Where().String()
		if _, dup := place[taskID]; dup {
			log.DEBUG("parallel: skipping duplicate task %s", taskID)
			continue
		}
		place[taskID] = len(tasks)

		// ev.Here is set to this task's own path before computing its
		// dependencies, mirroring Opcall.Run and
		// dataFlowContext.buildDependencyGraph's sequential-path setup: an
		// operator whose Dependencies() resolves a path relative to
		// ev.Here (op_calc.go's calcBareNameDependencies, for a bare
		// named calc variable that is a sibling of the calc call's own
		// path) needs it to compute the right relative cursor, or the
		// sibling edge is silently never added.
		ev.Here = op.Where()

		task := &schedTask{id: taskID, deps: ev.dependencyIDs(op, taskID, allLocs, opIDMap)}
		if op.op != nil {
			for _, c := range op.SoftDependencies(ev, allLocs) {
				nodes := oploc.Canonical(c, ev.Tree).Nodes
				key := oploc.Key(nodes)
				bar, ok := barriers[key]
				if !ok {
					bar = newSchedBarrier(ev, opIDMap, ev.locIndex.Under(nodes, nil))
					barriers[key] = bar
					for _, id := range bar.members {
						memberOf[id] = append(memberOf[id], bar)
					}
				}
				task.soft = append(task.soft, bar)
			}
		}
		tasks = append(tasks, task)
	}

	for _, task := range finishSchedTasks(tasks, place, memberOf) {
		if err := scheduler.AddTask(&parallel.Task{ID: task.id, Dependencies: task.deps}); err != nil {
			log.DEBUG("parallel: skipping duplicate task %s: %v", task.id, err)
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

// schedTask is one task addSchedulerTasks is building: its ID, its
// dependency IDs, and the soft barriers it waits on.
type schedTask struct {
	id   string
	deps []string
	soft []*schedBarrier
}

// schedBarrier is the scheduler's form of one soft barrier: the task IDs
// of the ops at or under one path, each once. best is the member seen so
// far in the order with the latest wave, and bestWave is that wave.
type schedBarrier struct {
	members  []string
	best     string
	bestWave int
}

// newSchedBarrier returns the barrier whose members are the ops at locs.
func newSchedBarrier(ev *Evaluator, opIDMap map[string]string, locs []*tree.Cursor) *schedBarrier {
	bar := &schedBarrier{members: make([]string, 0, len(locs))}
	seen := make(map[string]bool, len(locs))
	for _, loc := range locs {
		if id, ok := resolveOpIDForDependency(ev, opIDMap, loc); ok && !seen[id] {
			seen[id] = true
			bar.members = append(bar.members, id)
		}
	}
	return bar
}

// finishSchedTasks adds to each task's dependencies the soft ones it
// keeps, which are its barriers' members that come before it in tasks,
// other than itself, and returns tasks.
//
// The scheduler puts each task in the wave after the latest wave among
// its dependencies, so among one barrier's members only the one in the
// latest wave decides where a waiter goes. When every hard dependency
// comes before its task, each task's wave is known by the time it is
// reached, and each waiter gets that one member for each barrier rather
// than all of them. The waves come out the same as with every member
// named, and the edges grow with the tasks rather than with each waiter
// times its members. Otherwise each waiter names every member it keeps.
func finishSchedTasks(tasks []*schedTask, place map[string]int, memberOf map[string][]*schedBarrier) []*schedTask {
	if !hardDepsComeFirst(tasks, place) {
		for i, task := range tasks {
			task.addEveryKeptMember(i, place)
		}
		return tasks
	}
	wave := make(map[string]int, len(tasks))
	for _, task := range tasks {
		w := task.addLatestMembers(wave)
		wave[task.id] = w
		for _, bar := range memberOf[task.id] {
			if bar.best == "" || w > bar.bestWave {
				bar.best, bar.bestWave = task.id, w
			}
		}
	}
	return tasks
}

// hardDepsComeFirst reports whether every task's dependencies come
// before it in tasks, which place gives.
func hardDepsComeFirst(tasks []*schedTask, place map[string]int) bool {
	for i, task := range tasks {
		for _, id := range task.deps {
			if at, ok := place[id]; !ok || at >= i {
				return false
			}
		}
	}
	return true
}

// addEveryKeptMember adds to t's dependencies every member of its
// barriers that comes before it, at i in the order place gives.
func (t *schedTask) addEveryKeptMember(i int, place map[string]int) {
	seen := make(map[string]bool, len(t.deps))
	for _, id := range t.deps {
		seen[id] = true
	}
	for _, bar := range t.soft {
		for _, id := range bar.members {
			if at, ok := place[id]; ok && at < i && !seen[id] {
				seen[id] = true
				t.deps = append(t.deps, id)
			}
		}
	}
}

// addLatestMembers adds to t's dependencies the member of each of its
// barriers with the latest wave so far, and returns t's wave, given the
// wave of every task before it.
func (t *schedTask) addLatestMembers(wave map[string]int) int {
	w := 0
	for _, id := range t.deps {
		w = max(w, wave[id]+1)
	}
	for _, bar := range t.soft {
		if bar.best == "" || slices.Contains(t.deps, bar.best) {
			continue
		}
		t.deps = append(t.deps, bar.best)
		w = max(w, bar.bestWave+1)
	}
	return w
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
//
// Once ctx is done no further task starts, and runSequentialTasks
// returns the cancellation. The pool waits under a context that ctx
// cannot cancel, so a task that has started always finishes before this
// returns, rather than going on to change ev.Tree behind the caller.
func (ev *Evaluator) runSequentialTasks(ctx context.Context, pool *parallel.WorkerPool, treeMu *sync.Mutex, tasks []parallel.Task, opByID map[string]*Opcall, errs *MultiError) error {
	poolCtx := context.WithoutCancel(ctx)
	for _, task := range tasks {
		op := opByID[task.ID]
		if err := opCanceled(ctx, op); err != nil {
			return err
		}
		parallelStats.totalOps.Add(1)
		taskErr := pool.SubmitWaitContext(poolCtx, func(context.Context) error {
			if err := opCanceled(ctx, op); err != nil {
				return err
			}
			treeMu.Lock()
			defer treeMu.Unlock()
			return ev.RunOp(op)
		})
		if isCancel(ctx, taskErr) {
			return taskErr
		}
		if taskErr != nil {
			parallelStats.totalErrors.Add(1)
			errs.Append(taskErr)
		}
	}
	return nil
}

// computeResult is one concurrent task's computeOp outcome, held until
// every task in the wave has finished so the results can be applied to
// the tree serially, in the wave's fixed sorted order.
type computeResult struct {
	op       *Opcall
	resp     *Response
	oldValue interface{}
	err      error

	// stopped is the cancellation that kept this task from starting,
	// or nil when the task ran.
	stopped error
}

// runConcurrentTasks fans computeOp (the potentially slow Vault/AWS/NATS
// I/O) out across the pool's workers, then applies every result serially
// in the order given. It returns a non-nil error when the wave ends the
// phase, in which case no further wave may run: the cancellation alone
// when ctx is done before every task has started, or errs with the
// shutdown added when the pool shuts down mid-wave.
//
// Once ctx is done no further task starts, but every task already
// running finishes and has its result applied before this returns, so
// no worker reads or changes ev.Tree after the caller has moved on. The
// pool runs the tasks under a context that ctx cannot cancel, so a
// queued task always reaches its own check and its wg.Done.
func (ev *Evaluator) runConcurrentTasks(ctx context.Context, pool *parallel.WorkerPool, treeMu *sync.Mutex, tasks []parallel.Task, opByID map[string]*Opcall, errs *MultiError) error {
	if len(tasks) == 0 {
		return nil
	}

	results := make([]computeResult, len(tasks))
	var wg sync.WaitGroup
	wg.Add(len(tasks))
	poolCtx := context.WithoutCancel(ctx)

	for i, task := range tasks {
		idx := i
		op := opByID[task.ID]
		if err := opCanceled(ctx, op); err != nil {
			// No task from here on may start, so each is marked
			// stopped and its wg.Done accounted for here.
			for j := i; j < len(tasks); j++ {
				results[j] = computeResult{op: opByID[tasks[j].ID], stopped: opCanceled(ctx, opByID[tasks[j].ID])}
				wg.Done()
			}
			break
		}
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
		submitErr := pool.SubmitBlocking(poolCtx, func(context.Context) error {
			defer wg.Done()
			if err := opCanceled(ctx, op); err != nil {
				results[idx] = computeResult{op: op, stopped: err}
				return nil
			}
			resp, oldValue, computeErr := ev.computeOp(op)
			results[idx] = computeResult{op: op, resp: resp, oldValue: oldValue, err: computeErr}
			return computeErr
		})
		if submitErr != nil {
			// The pool itself is shut down - the task never ran,
			// so account for its wg.Done() here and record the
			// rejection as that task's error.
			wg.Done()
			results[idx] = computeResult{op: op, err: submitErr}
		}
	}

	// A bare wg.Wait() would block forever if the pool is
	// canceled after a task was already enqueued but before a
	// worker picks it up: workers abandon their current select
	// the instant the pool's internal context is done, without
	// draining taskQueue, so that task's deferred wg.Done() would
	// never fire. Select on the pool's cancellation signal
	// alongside the WaitGroup so a shutdown mid-wave returns a
	// real error instead of hanging - reachable from the library
	// API when a consumer shuts an in-flight engine's pool down
	// concurrently. The caller's ctx is deliberately not in this
	// select: a canceled ctx stops tasks from starting, and the
	// wave still waits for the ones already running.
	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()

	select {
	case <-waitDone:
	case <-pool.Canceled():
		parallelStats.totalErrors.Add(1)
		errs.Append(fmt.Errorf("worker pool shut down while a wave was in flight"))
		return *errs
	}

	var stop error
	for _, r := range results {
		if r.stopped != nil {
			if stop == nil {
				stop = r.stopped
			}
			continue
		}
		parallelStats.totalOps.Add(1)
		if r.err != nil {
			ev.donePending(r.op)
			parallelStats.totalErrors.Add(1)
			errs.Append(r.err)
			continue
		}

		treeMu.Lock()
		applyErr := ev.scrubLiteralError(r.op, ev.applyResponse(r.op, r.resp, r.oldValue))
		treeMu.Unlock()
		ev.donePending(r.op)

		if applyErr != nil {
			parallelStats.totalErrors.Add(1)
			errs.Append(applyErr)
		}
	}

	return stop
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
