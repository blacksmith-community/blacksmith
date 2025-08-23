package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// TasksClient implements the capi.TasksClient interface
type TasksClient struct {
	httpClient *http.Client
}

// NewTasksClient creates a new TasksClient
func NewTasksClient(httpClient *http.Client) *TasksClient {
	return &TasksClient{
		httpClient: httpClient,
	}
}

// Create creates a new task for an app
func (c *TasksClient) Create(ctx context.Context, appGUID string, request *capi.TaskCreateRequest) (*capi.Task, error) {
	path := fmt.Sprintf("/v3/apps/%s/tasks", appGUID)

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating task: %w", err)
	}

	var task capi.Task
	if err := json.Unmarshal(resp.Body, &task); err != nil {
		return nil, fmt.Errorf("parsing task response: %w", err)
	}

	return &task, nil
}

// Get retrieves a specific task
func (c *TasksClient) Get(ctx context.Context, guid string) (*capi.Task, error) {
	path := fmt.Sprintf("/v3/tasks/%s", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting task: %w", err)
	}

	var task capi.Task
	if err := json.Unmarshal(resp.Body, &task); err != nil {
		return nil, fmt.Errorf("parsing task response: %w", err)
	}

	return &task, nil
}

// List lists all tasks
func (c *TasksClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.Task], error) {
	path := "/v3/tasks"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}

	var result capi.ListResponse[capi.Task]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing tasks list response: %w", err)
	}

	return &result, nil
}

// Update updates a task's metadata
func (c *TasksClient) Update(ctx context.Context, guid string, request *capi.TaskUpdateRequest) (*capi.Task, error) {
	path := fmt.Sprintf("/v3/tasks/%s", guid)

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating task: %w", err)
	}

	var task capi.Task
	if err := json.Unmarshal(resp.Body, &task); err != nil {
		return nil, fmt.Errorf("parsing task response: %w", err)
	}

	return &task, nil
}

// Cancel cancels a running task
func (c *TasksClient) Cancel(ctx context.Context, guid string) (*capi.Task, error) {
	path := fmt.Sprintf("/v3/tasks/%s/actions/cancel", guid)

	resp, err := c.httpClient.Post(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("canceling task: %w", err)
	}

	var task capi.Task
	if err := json.Unmarshal(resp.Body, &task); err != nil {
		return nil, fmt.Errorf("parsing task response: %w", err)
	}

	return &task, nil
}
