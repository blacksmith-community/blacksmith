package tasks_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/handlers/tasks"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/testutil"
)

var (
	errFakeNotFound     = errors.New("not found")
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

func (f *fakeDirector) GetTasks(taskType string, limit int, states []string, team string) ([]bosh.Task, error) {
	if f.getTasksErr != nil {
		return nil, f.getTasksErr
	}

	result := make([]bosh.Task, len(f.tasks))
	copy(result, f.tasks)

	return result, nil
}

func (f *fakeDirector) GetTask(taskID int) (*bosh.Task, error) {
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

func (f *fakeDirector) GetTaskEvents(taskID int) ([]bosh.TaskEvent, error) {
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

func (f *fakeDirector) GetTaskOutput(taskID int, outputType string) (string, error) {
	key := outputKey{id: taskID, typ: outputType}

	if err, ok := f.taskOutputErr[key]; ok {
		return "", err
	}

	if output, ok := f.taskOutputs[key]; ok {
		return output, nil
	}

	return "", errFakeNotFound
}

func (f *fakeDirector) CancelTask(taskID int) error {
	if err, ok := f.cancelTaskErr[taskID]; ok {
		return err
	}

	f.cancelledTasks = append(f.cancelledTasks, taskID)

	return nil
}

func (f *fakeDirector) FindRunningTaskForDeployment(deploymentName string) (*bosh.Task, error) {
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
		Deployment:  "example",
	}
	fakeDir.taskEvents[42] = []bosh.TaskEvent{{Stage: "Running", Task: "update", State: "started"}}

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir})

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

	if payload.Task.ID != 42 || payload.Task.User != "admin" || payload.Task.Deployment != "example" {
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
	fakeDir.taskOutputs[outputKey{id: 8, typ: "result"}] = "{\"time\":1700002000,\"stage\":\"Running\",\"task\":\"step\",\"state\":\"started\",\"total\":2,\"index\":1,\"progress\":50}"

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir})

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
	fakeDir.taskOutputs[outputKey{id: 7, typ: "debug"}] = "line one\nline two"

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir})

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
	fakeDir.taskOutputErr[outputKey{id: 6, typ: "task"}] = errFakeNotFound

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir})

	req := httptest.NewRequest(http.MethodGet, "/b/tasks/6/output", nil)
	recorder := httptest.NewRecorder()

	handler.GetTaskOutput(recorder, req, 6)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", recorder.Code)
	}
}

func TestCancelTaskSuccess(t *testing.T) {
	t.Parallel()

	fakeDir := newFakeDirector()

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir})

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
	fakeDir.cancelTaskErr[15] = bosh.ErrTaskCannotBeCancelled

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir})

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
	fakeDir.cancelTaskErr[16] = errTestBoom

	log, err := logger.New(logger.Config{Level: "debug", Format: "json"})
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	handler := tasks.NewHandler(tasks.Dependencies{Logger: log, Director: fakeDir})

	req := httptest.NewRequest(http.MethodPost, "/b/tasks/16/cancel", nil)
	recorder := httptest.NewRecorder()

	handler.CancelTask(recorder, req, 16)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", recorder.Code)
	}
}
