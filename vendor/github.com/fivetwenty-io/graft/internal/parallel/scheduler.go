package parallel

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Common scheduler errors.
var (
	ErrCycleDetected = errors.New("cycle detected in task dependencies")
	ErrTaskNotFound  = errors.New("task not found")
	ErrDuplicateTask = errors.New("duplicate task ID")
)

// Task represents a unit of work with dependencies.
type Task struct {
	// ID uniquely identifies this task
	ID string

	// Dependencies are IDs of tasks that must complete before this task
	Dependencies []string

	// Execute is the function to run for this task
	Execute func(ctx context.Context) error

	// Priority is an optional priority hint (higher = run sooner within a wave)
	Priority int

	// Data holds arbitrary metadata
	Data interface{}
}

// TaskResult contains the result of executing a task.
type TaskResult struct {
	ID    string
	Error error
}

// Scheduler orders and executes tasks based on dependencies.
type Scheduler struct {
	tasks map[string]*Task
	mu    sync.RWMutex
}

// NewScheduler creates a new task scheduler.
func NewScheduler() *Scheduler {
	return &Scheduler{
		tasks: make(map[string]*Task),
	}
}

// AddTask adds a task to the scheduler.
func (s *Scheduler) AddTask(task *Task) error {
	if task == nil || task.ID == "" {
		return errors.New("invalid task: ID required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.tasks[task.ID]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateTask, task.ID)
	}

	s.tasks[task.ID] = task
	return nil
}

// AddTasks adds multiple tasks to the scheduler.
func (s *Scheduler) AddTasks(tasks []*Task) error {
	for _, task := range tasks {
		if err := s.AddTask(task); err != nil {
			return err
		}
	}
	return nil
}

// RemoveTask removes a task from the scheduler.
func (s *Scheduler) RemoveTask(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tasks, id)
}

// GetTask returns a task by ID.
func (s *Scheduler) GetTask(id string) (*Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	task, ok := s.tasks[id]
	return task, ok
}

// Clear removes all tasks from the scheduler.
func (s *Scheduler) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks = make(map[string]*Task)
}

// Schedule returns tasks grouped into execution waves.
// Each wave contains tasks that can be executed concurrently.
// Waves must be executed in order.
func (s *Scheduler) Schedule() ([][]Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.tasks) == 0 {
		return nil, nil
	}

	// Build in-degree map and validate dependencies
	inDegree := make(map[string]int)
	for id, task := range s.tasks {
		if _, exists := inDegree[id]; !exists {
			inDegree[id] = 0
		}

		for _, dep := range task.Dependencies {
			if _, exists := s.tasks[dep]; !exists {
				return nil, fmt.Errorf("%w: %s (dependency of %s)", ErrTaskNotFound, dep, id)
			}
			inDegree[id]++
		}
	}

	// Build reverse dependency map
	dependents := make(map[string][]string)
	for id, task := range s.tasks {
		for _, dep := range task.Dependencies {
			dependents[dep] = append(dependents[dep], id)
		}
	}

	// Process in waves
	waves := make([][]Task, 0)
	processed := make(map[string]bool)

	for len(processed) < len(s.tasks) {
		// Find all tasks with no remaining dependencies
		wave := make([]Task, 0)
		for id, degree := range inDegree {
			if degree == 0 && !processed[id] {
				wave = append(wave, *s.tasks[id])
			}
		}

		if len(wave) == 0 {
			// Find the cycle
			cycle := s.findCycle(processed)
			return nil, fmt.Errorf("%w: %v", ErrCycleDetected, cycle)
		}

		// Sort wave by priority (higher priority first)
		sort.Slice(wave, func(i, j int) bool {
			return wave[i].Priority > wave[j].Priority
		})

		// Mark tasks as processed and update in-degrees
		for _, task := range wave {
			processed[task.ID] = true
			for _, depID := range dependents[task.ID] {
				inDegree[depID]--
			}
		}

		waves = append(waves, wave)
	}

	return waves, nil
}

// ScheduleIDs returns task IDs grouped into execution waves.
func (s *Scheduler) ScheduleIDs() ([][]string, error) {
	waves, err := s.Schedule()
	if err != nil {
		return nil, err
	}

	result := make([][]string, len(waves))
	for i, wave := range waves {
		result[i] = make([]string, len(wave))
		for j, task := range wave {
			result[i][j] = task.ID
		}
	}

	return result, nil
}

// TopologicalOrder returns a linear ordering of tasks respecting dependencies.
func (s *Scheduler) TopologicalOrder() ([]Task, error) {
	waves, err := s.Schedule()
	if err != nil {
		return nil, err
	}

	result := make([]Task, 0, len(s.tasks))
	for _, wave := range waves {
		result = append(result, wave...)
	}

	return result, nil
}

// HasCycle returns true if there's a cycle in the task dependencies.
func (s *Scheduler) HasCycle() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	visited := make(map[string]bool)
	inStack := make(map[string]bool)

	var dfs func(id string) bool
	dfs = func(id string) bool {
		if inStack[id] {
			return true
		}
		if visited[id] {
			return false
		}

		visited[id] = true
		inStack[id] = true

		if task, exists := s.tasks[id]; exists {
			for _, dep := range task.Dependencies {
				if dfs(dep) {
					return true
				}
			}
		}

		inStack[id] = false
		return false
	}

	for id := range s.tasks {
		if !visited[id] {
			if dfs(id) {
				return true
			}
		}
	}

	return false
}

// findCycle finds a cycle in unprocessed tasks.
func (s *Scheduler) findCycle(processed map[string]bool) []string {
	visited := make(map[string]bool)
	inStack := make(map[string]bool)
	path := make([]string, 0)

	var dfs func(id string) bool
	dfs = func(id string) bool {
		if processed[id] {
			return false
		}
		if inStack[id] {
			return true
		}
		if visited[id] {
			return false
		}

		visited[id] = true
		inStack[id] = true
		path = append(path, id)

		if task, exists := s.tasks[id]; exists {
			for _, dep := range task.Dependencies {
				if dfs(dep) {
					return true
				}
			}
		}

		inStack[id] = false
		path = path[:len(path)-1]
		return false
	}

	for id := range s.tasks {
		if !processed[id] && !visited[id] {
			if dfs(id) {
				return path
			}
		}
	}

	return nil
}

// Execute runs all tasks using the provided pool.
// Tasks are executed in waves based on their dependencies.
func (s *Scheduler) Execute(ctx context.Context, pool *WorkerPool) ([]TaskResult, error) {
	waves, err := s.Schedule()
	if err != nil {
		return nil, err
	}

	allResults := make([]TaskResult, 0, len(s.tasks))

	for _, wave := range waves {
		// Execute all tasks in the wave concurrently
		results := s.executeWave(ctx, pool, wave)
		allResults = append(allResults, results...)

		// Note: We continue with other waves even on error.
		// Errors are recorded in the results and returned to the caller.
		_ = results // Errors are tracked in allResults
	}

	return allResults, nil
}

// ExecuteWithFailFast runs tasks but stops on first error.
func (s *Scheduler) ExecuteWithFailFast(ctx context.Context, pool *WorkerPool) ([]TaskResult, error) {
	waves, err := s.Schedule()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	allResults := make([]TaskResult, 0, len(s.tasks))

	for _, wave := range waves {
		results := s.executeWave(ctx, pool, wave)
		allResults = append(allResults, results...)

		// Check for failures and stop
		for _, result := range results {
			if result.Error != nil {
				cancel()
				return allResults, result.Error
			}
		}
	}

	return allResults, nil
}

// executeWave executes a single wave of tasks.
func (s *Scheduler) executeWave(_ context.Context, pool *WorkerPool, wave []Task) []TaskResult {
	results := make([]TaskResult, len(wave))
	var wg sync.WaitGroup
	var mu sync.Mutex
	resultMap := make(map[string]TaskResult)

	for _, task := range wave {
		wg.Add(1)
		t := task // Capture for closure

		if err := pool.Submit(func(ctx context.Context) error {
			defer wg.Done()

			var err error
			if t.Execute != nil {
				err = t.Execute(ctx)
			}

			mu.Lock()
			resultMap[t.ID] = TaskResult{ID: t.ID, Error: err}
			mu.Unlock()

			return err
		}); err != nil {
			wg.Done() // Balance the wg.Add(1) since task won't run
			mu.Lock()
			resultMap[t.ID] = TaskResult{ID: t.ID, Error: err}
			mu.Unlock()
		}
	}

	wg.Wait()

	// Preserve order
	for i, task := range wave {
		results[i] = resultMap[task.ID]
	}

	return results
}

// ExecuteSequential runs all tasks sequentially (no pool needed).
func (s *Scheduler) ExecuteSequential(ctx context.Context) ([]TaskResult, error) {
	order, err := s.TopologicalOrder()
	if err != nil {
		return nil, err
	}

	results := make([]TaskResult, 0, len(order))

	for _, task := range order {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}

		var taskErr error
		if task.Execute != nil {
			taskErr = task.Execute(ctx)
		}

		results = append(results, TaskResult{
			ID:    task.ID,
			Error: taskErr,
		})
	}

	return results, nil
}

// Validate checks that all dependencies exist and there are no cycles.
func (s *Scheduler) Validate() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check all dependencies exist
	for id, task := range s.tasks {
		for _, dep := range task.Dependencies {
			if _, exists := s.tasks[dep]; !exists {
				return fmt.Errorf("%w: %s (dependency of %s)", ErrTaskNotFound, dep, id)
			}
		}
	}

	// Check for cycles
	if s.HasCycle() {
		return ErrCycleDetected
	}

	return nil
}

// Size returns the number of tasks in the scheduler.
func (s *Scheduler) Size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tasks)
}

// GetDependencies returns the dependencies of a task.
func (s *Scheduler) GetDependencies(id string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if task, exists := s.tasks[id]; exists {
		deps := make([]string, len(task.Dependencies))
		copy(deps, task.Dependencies)
		return deps
	}
	return nil
}

// GetDependents returns tasks that depend on the given task.
func (s *Scheduler) GetDependents(id string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var dependents []string
	for taskID, task := range s.tasks {
		for _, dep := range task.Dependencies {
			if dep == id {
				dependents = append(dependents, taskID)
				break
			}
		}
	}
	return dependents
}

// CriticalPath returns the longest path through the task graph.
// This represents the minimum time needed assuming infinite parallelism.
func (s *Scheduler) CriticalPath() ([]string, error) {
	waves, err := s.Schedule()
	if err != nil {
		return nil, err
	}

	if len(waves) == 0 {
		return nil, nil
	}

	// Build longest path to each task
	longestPath := make(map[string]int)
	predecessor := make(map[string]string)

	for _, wave := range waves {
		for _, task := range wave {
			maxLen := 0
			var maxPred string

			for _, dep := range task.Dependencies {
				if l := longestPath[dep]; l >= maxLen {
					maxLen = l + 1
					maxPred = dep
				}
			}

			longestPath[task.ID] = maxLen
			if maxPred != "" {
				predecessor[task.ID] = maxPred
			}
		}
	}

	// Find the end of the critical path
	var endID string
	maxLen := -1
	for id, length := range longestPath {
		if length > maxLen {
			maxLen = length
			endID = id
		}
	}

	// Reconstruct path
	path := make([]string, 0)
	for id := endID; id != ""; id = predecessor[id] {
		path = append([]string{id}, path...)
	}

	return path, nil
}
