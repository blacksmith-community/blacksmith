package tasks_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/handlers/tasks"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

var (
	errDirectorDown     = errors.New("dial tcp: connection refused")
	errFakeNotFound     = &boshapi.APIError{StatusCode: http.StatusNotFound, Code: boshapi.ErrorCodeTaskNotFound, Description: "task not found"}
	errTestBoom         = errors.New("boom")
	errTestEventFailure = errors.New("event failure")
)

type outputKey struct {
	id  int
	typ string
}

type fakeDirector struct {
	*testutil.MockBOSHDirector

	tasks          []bosh.Task
	getTasksErr    error
	gotLimit       int
	gotStates      []string
	taskDetails    map[int]bosh.Task
	taskDetailErr  map[int]error
	taskEvents     map[int][]bosh.TaskEvent
	taskEventsErr  map[int]error
	taskOutputs    map[outputKey]string
	taskOutputErr  map[outputKey]error
	cancelledTasks []int
	cancelTaskErr  map[int]error
}

func newFakeDirector() *fakeDirector {
	return &fakeDirector{
		MockBOSHDirector: testutil.NewMockBOSHDirector(),
		taskDetails:      make(map[int]bosh.Task),
		taskDetailErr:    make(map[int]error),
		taskEvents:       make(map[int][]bosh.TaskEvent),
		taskEventsErr:    make(map[int]error),
		taskOutputs:      make(map[outputKey]string),
		taskOutputErr:    make(map[outputKey]error),
		cancelledTasks:   make([]int, 0),
		cancelTaskErr:    make(map[int]error),
	}
}

func (f *fakeDirector) GetTasks(_ context.Context, taskType string, limit int, states []string, team string) ([]bosh.Task, error) {
	if f.getTasksErr != nil {
		return nil, f.getTasksErr
	}

	f.gotLimit = limit
	f.gotStates = states

	result := make([]bosh.Task, 0, len(f.tasks))

	for _, task := range f.tasks {
		if len(states) > 0 && !slices.Contains(states, task.State) {
			continue
		}

		result = append(result, task)
	}

	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}

	return result, nil
}

// ListTasks is the exact listing the handler reads. It records one more than
// the limit, as the adapter asks the director for, and applies the states.
func (f *fakeDirector) ListTasks(ctx context.Context, taskType string, limit int, states []string, team string) (bosh.TaskListing, error) {
	tasks, err := f.GetTasks(ctx, taskType, limit+1, states, team)
	if err != nil {
		return bosh.TaskListing{}, err
	}

	listing := bosh.TaskListing{Tasks: tasks, More: len(tasks) > limit, Exact: true}
	if listing.More {
		listing.Tasks = tasks[:limit]
	}

	return listing, nil
}

func (f *fakeDirector) GetTask(_ context.Context, taskID int) (*bosh.Task, error) {
	if err, ok := f.taskDetailErr[taskID]; ok {
		return nil, err
	}

	task, ok := f.taskDetails[taskID]
	if !ok {
		return nil, errFakeNotFound
	}

	taskCopy := task

	return &taskCopy, nil
}

func (f *fakeDirector) GetTaskEvents(_ context.Context, taskID int) ([]bosh.TaskEvent, error) {
	if err, ok := f.taskEventsErr[taskID]; ok {
		return nil, err
	}

	events, ok := f.taskEvents[taskID]
	if !ok {
		return []bosh.TaskEvent{}, nil
	}

	result := make([]bosh.TaskEvent, len(events))
	copy(result, events)

	return result, nil
}

func (f *fakeDirector) GetTaskOutput(_ context.Context, taskID int, outputType string) (string, error) {
	key := outputKey{id: taskID, typ: outputType}

	if err, ok := f.taskOutputErr[key]; ok {
		return "", err
	}

	if output, ok := f.taskOutputs[key]; ok {
		return output, nil
	}

	return "", errFakeNotFound
}

func (f *fakeDirector) CancelTask(_ context.Context, taskID int) error {
	if err, ok := f.cancelTaskErr[taskID]; ok {
		return err
	}

	f.cancelledTasks = append(f.cancelledTasks, taskID)

	return nil
}

func (f *fakeDirector) FindRunningTaskForDeployment(_ context.Context, deploymentName string) (*bosh.Task, error) {
	return nil, nil
}

func TestListTasksSuccess(t *testing.T) {
	t.Parallel()

	fakeDir := newFakeDirector()
	fakeDir.tasks = []bosh.Task{
		{
			ID:          1,
			State:       "done",
			Description: "create deployment",
			Timestamp:   1700000000,
			StartedAt:   time.Unix(1700000000, 0),
			Result:      "success",
		},
		{
			ID:          2,
			State:       "processing",
			Description: "update deployment",
			Timestamp:   1700000600,
			StartedAt:   time.Unix(1700000600, 0),
		},
	}

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir})

	req := httptest.NewRequest(http.MethodGet, "/b/tasks?type=all&team=blacksmith", nil)
	recorder := httptest.NewRecorder()

	handler.ListTasks(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var payload []tasks.Task

	err = json.Unmarshal(recorder.Body.Bytes(), &payload)
	if err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(payload) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(payload))
	}

	if payload[0].ID != 1 || payload[1].ID != 2 {
		t.Fatalf("unexpected task ids: %+v", payload)
	}
}

func TestListTasksFiltersByState(t *testing.T) {
	t.Parallel()

	fakeDir := newFakeDirector()
	fakeDir.tasks = []bosh.Task{
		{ID: 1, State: "done"},
		{ID: 2, State: "processing"},
	}

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir})

	req := httptest.NewRequest(http.MethodGet, "/b/tasks?state=done", nil)
	recorder := httptest.NewRecorder()

	handler.ListTasks(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var payload []tasks.Task

	err = json.Unmarshal(recorder.Body.Bytes(), &payload)
	if err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(payload) != 1 || payload[0].ID != 1 {
		t.Fatalf("expected only task 1, got %+v", payload)
	}
}

const errorState = "error"

func TestListTasksCountsEveryStateInTheWindow(t *testing.T) {
	t.Parallel()

	fakeDir := newFakeDirector()
	fakeDir.tasks = []bosh.Task{
		{ID: 1, State: "done"}, {ID: 2, State: "done"}, {ID: 3, State: "done"},
		{ID: 4, State: errorState}, {ID: 5, State: errorState}, {ID: 6, State: errorState},
		{ID: 7, State: "processing"},
	}

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/tasks?counts=1&states=error&limit=2", nil)
	recorder := httptest.NewRecorder()

	handler.ListTasks(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var payload struct {
		Tasks       []tasks.Task   `json:"tasks"`
		StateCounts map[string]int `json:"state_counts"`
		Window      int            `json:"window"`
	}

	err = json.Unmarshal(recorder.Body.Bytes(), &payload)
	if err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(payload.Tasks) != 2 || payload.Tasks[0].State != errorState || payload.Tasks[1].State != errorState {
		t.Fatalf("expected the two newest error tasks, got %+v", payload.Tasks)
	}

	want := map[string]int{"processing": 1, "queued": 0, "done": 3, errorState: 3, "cancelled": 0}
	for state, count := range want {
		if payload.StateCounts[state] != count {
			t.Fatalf("state_counts[%s] = %d, want %d (all: %v)", state, payload.StateCounts[state], count, payload.StateCounts)
		}
	}

	if payload.Window != 7 {
		t.Fatalf("window = %d, want 7", payload.Window)
	}
}

func TestListTasksCountsWithoutStatesAsksForOneWindow(t *testing.T) {
	t.Parallel()

	fakeDir := newFakeDirector()
	fakeDir.tasks = []bosh.Task{
		{ID: 1, State: "done"}, {ID: 2, State: errorState}, {ID: 3, State: "done"}, {ID: 4, State: "done"},
	}

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/tasks?counts=1&limit=3", nil)
	recorder := httptest.NewRecorder()

	handler.ListTasks(recorder, req)

	var payload struct {
		Tasks       []tasks.Task   `json:"tasks"`
		StateCounts map[string]int `json:"state_counts"`
		Window      int            `json:"window"`
	}

	err = json.Unmarshal(recorder.Body.Bytes(), &payload)
	if err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if fakeDir.gotLimit != 4 || len(fakeDir.gotStates) != 0 {
		t.Fatalf("director asked with limit %d and states %v, want the requested limit and one more, and no states", fakeDir.gotLimit, fakeDir.gotStates)
	}

	if len(payload.Tasks) != 3 || payload.Window != 3 || payload.StateCounts["done"] != 2 || payload.StateCounts[errorState] != 1 {
		t.Fatalf("rows and counts must describe the same window, got %d rows, window %d, counts %v", len(payload.Tasks), payload.Window, payload.StateCounts)
	}
}

func TestListTasksWithoutCountsStaysAnArray(t *testing.T) {
	t.Parallel()

	fakeDir := newFakeDirector()
	fakeDir.tasks = []bosh.Task{{ID: 1, State: "done"}, {ID: 2, State: errorState}}

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/b/tasks?states=error", nil)
	recorder := httptest.NewRecorder()

	handler.ListTasks(recorder, req)

	var payload []tasks.Task

	err = json.Unmarshal(recorder.Body.Bytes(), &payload)
	if err != nil {
		t.Fatalf("the default response must stay a bare array: %v", err)
	}

	if len(payload) != 1 || payload[0].ID != 2 || fakeDir.gotStates[0] != "error" {
		t.Fatalf("states=error must still reach the director and filter, got %+v", payload)
	}
}

func TestListTasksDirectorError(t *testing.T) {
	t.Parallel()

	fakeDir := newFakeDirector()
	fakeDir.getTasksErr = errTestBoom

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir})

	req := httptest.NewRequest(http.MethodGet, "/b/tasks", nil)
	recorder := httptest.NewRecorder()

	handler.ListTasks(recorder, req)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", recorder.Code)
	}
}

func TestGetTaskDetailsSuccess(t *testing.T) {
	t.Parallel()

	fakeDir := newFakeDirector()
	fakeDir.taskDetails[42] = bosh.Task{
		ID:          42,
		State:       "done",
		Description: "deployment update",
		Timestamp:   1700001000,
		StartedAt:   time.Unix(1700001000, 0),
		Result:      "ok",
		User:        "admin",
		Deployment:  managedDeployment,
	}
	fakeDir.taskEvents[42] = []bosh.TaskEvent{{Stage: "Running", Task: "update", State: "started"}}

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir, Vault: managingTaskVault()})

	req := httptest.NewRequest(http.MethodGet, "/b/tasks/42", nil)
	recorder := httptest.NewRecorder()

	handler.GetTaskDetails(recorder, req, 42)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var payload struct {
		Task   tasks.Task       `json:"task"`
		Events []bosh.TaskEvent `json:"events"`
	}

	err = json.Unmarshal(recorder.Body.Bytes(), &payload)
	if err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if payload.Task.ID != 42 || payload.Task.User != "admin" || payload.Task.Deployment != managedDeployment {
		t.Fatalf("unexpected task payload: %+v", payload.Task)
	}

	if len(payload.Events) != 1 || payload.Events[0].Stage != "Running" {
		t.Fatalf("unexpected events payload: %+v", payload.Events)
	}
}

func TestGetTaskDetailsNotFound(t *testing.T) {
	t.Parallel()

	fakeDir := newFakeDirector()
	fakeDir.taskDetailErr[99] = errFakeNotFound

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir})

	req := httptest.NewRequest(http.MethodGet, "/b/tasks/99", nil)
	recorder := httptest.NewRecorder()

	handler.GetTaskDetails(recorder, req, 99)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", recorder.Code)
	}

	assertTaskNotFoundCode(t, recorder)
}

func TestGetTaskDetailsEventsFallback(t *testing.T) {
	t.Parallel()

	fakeDir := newFakeDirector()
	fakeDir.taskDetails[10] = bosh.Task{ID: 10, State: "done"}
	fakeDir.taskEventsErr[10] = errTestEventFailure

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir})

	req := httptest.NewRequest(http.MethodGet, "/b/tasks/10", nil)
	recorder := httptest.NewRecorder()

	handler.GetTaskDetails(recorder, req, 10)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var payload struct {
		Events []bosh.TaskEvent `json:"events"`
	}

	err = json.Unmarshal(recorder.Body.Bytes(), &payload)
	if err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(payload.Events) != 0 {
		t.Fatalf("expected empty events, got %d", len(payload.Events))
	}
}

func TestGetTaskOutputResultEvents(t *testing.T) {
	t.Parallel()

	fakeDir := newFakeDirector()
	fakeDir.taskDetails[8] = bosh.Task{ID: 8, Deployment: managedDeployment}
	fakeDir.taskOutputs[outputKey{id: 8, typ: "result"}] = "{\"time\":1700002000,\"stage\":\"Running\",\"task\":\"step\",\"state\":\"started\",\"total\":2,\"index\":1,\"progress\":50}"

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir, Vault: managingTaskVault()})

	req := httptest.NewRequest(http.MethodGet, "/b/tasks/8/output?type=result", nil)
	recorder := httptest.NewRecorder()

	handler.GetTaskOutput(recorder, req, 8)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var events []bosh.TaskEvent

	err = json.Unmarshal(recorder.Body.Bytes(), &events)
	if err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(events) != 1 || events[0].Stage != "Running" || events[0].Task != "step" {
		t.Fatalf("unexpected events payload: %+v", events)
	}
}

func TestGetTaskOutputRaw(t *testing.T) {
	t.Parallel()

	fakeDir := newFakeDirector()
	fakeDir.taskDetails[7] = bosh.Task{ID: 7, Deployment: managedDeployment}
	fakeDir.taskOutputs[outputKey{id: 7, typ: "debug"}] = "line one\nline two"

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir, Vault: managingTaskVault()})

	req := httptest.NewRequest(http.MethodGet, "/b/tasks/7/output?type=debug", nil)
	recorder := httptest.NewRecorder()

	handler.GetTaskOutput(recorder, req, 7)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var payload struct {
		TaskID int    `json:"task_id"`
		Type   string `json:"type"`
		Output string `json:"output"`
	}

	err = json.Unmarshal(recorder.Body.Bytes(), &payload)
	if err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if payload.Type != "debug" || payload.Output != "line one\nline two" {
		t.Fatalf("unexpected payload: %+v", payload)
	}

	if payload.TaskID != 7 {
		t.Fatalf("unexpected task id: %d", payload.TaskID)
	}
}

func TestGetTaskOutputNotFound(t *testing.T) {
	t.Parallel()

	fakeDir := newFakeDirector()
	fakeDir.taskDetails[6] = bosh.Task{ID: 6, Deployment: managedDeployment}
	fakeDir.taskOutputErr[outputKey{id: 6, typ: "task"}] = errFakeNotFound

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir, Vault: managingTaskVault()})

	req := httptest.NewRequest(http.MethodGet, "/b/tasks/6/output", nil)
	recorder := httptest.NewRecorder()

	handler.GetTaskOutput(recorder, req, 6)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", recorder.Code)
	}

	assertTaskNotFoundCode(t, recorder)
}

func TestCancelTaskSuccess(t *testing.T) {
	t.Parallel()

	fakeDir := newFakeDirector()
	fakeDir.taskDetails[12] = bosh.Task{ID: 12, Deployment: managedDeployment}

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir, Vault: managingTaskVault()})

	req := httptest.NewRequest(http.MethodPost, "/b/tasks/12/cancel", strings.NewReader(`{"force":true}`))
	recorder := httptest.NewRecorder()

	handler.CancelTask(recorder, req, 12)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}

	var payload struct {
		TaskID    int  `json:"task_id"`
		Cancelled bool `json:"cancelled"`
		Force     bool `json:"force"`
	}

	err = json.Unmarshal(recorder.Body.Bytes(), &payload)
	if err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if !payload.Cancelled || !payload.Force || payload.TaskID != 12 {
		t.Fatalf("unexpected payload: %+v", payload)
	}

	if len(fakeDir.cancelledTasks) != 1 || fakeDir.cancelledTasks[0] != 12 {
		t.Fatalf("expected task 12 to be cancelled, got %+v", fakeDir.cancelledTasks)
	}
}

func TestCancelTaskConflict(t *testing.T) {
	t.Parallel()

	fakeDir := newFakeDirector()
	fakeDir.taskDetails[15] = bosh.Task{ID: 15, Deployment: managedDeployment}
	fakeDir.cancelTaskErr[15] = bosh.ErrTaskCannotBeCancelled

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir, Vault: managingTaskVault()})

	req := httptest.NewRequest(http.MethodPost, "/b/tasks/15/cancel", nil)
	recorder := httptest.NewRecorder()

	handler.CancelTask(recorder, req, 15)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d", recorder.Code)
	}
}

func TestCancelTaskFailure(t *testing.T) {
	t.Parallel()

	fakeDir := newFakeDirector()
	fakeDir.taskDetails[16] = bosh.Task{ID: 16, Deployment: managedDeployment}
	fakeDir.cancelTaskErr[16] = errTestBoom

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir, Vault: managingTaskVault()})

	req := httptest.NewRequest(http.MethodPost, "/b/tasks/16/cancel", nil)
	recorder := httptest.NewRecorder()

	handler.CancelTask(recorder, req, 16)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", recorder.Code)
	}
}

func assertTaskNotFoundCode(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()

	var body struct {
		Code string `json:"code"`
	}

	err := json.Unmarshal(recorder.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("the 404 body is not JSON: %v", err)
	}

	if body.Code != "task_not_found" {
		t.Fatalf("expected code task_not_found, got %q in %s", body.Code, recorder.Body.String())
	}
}
