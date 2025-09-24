package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"
)

// Error variables for err113 compliance.
var (
	errTaskEndpointNotFound = errors.New("task endpoint not found")
)

// Handler handles BOSH task-related endpoints.
type Handler struct {
	logger   interfaces.Logger
	config   interfaces.Config
	director interfaces.Director
}

// Dependencies contains all dependencies needed by the Tasks handler.
type Dependencies struct {
	Logger   interfaces.Logger
	Config   interfaces.Config
	Director interfaces.Director
}

// Task represents a BOSH task.
type Task struct {
	ID          int       `json:"id"`
	State       string    `json:"state"`
	Description string    `json:"description"`
	Timestamp   int64     `json:"timestamp"`
	StartedAt   time.Time `json:"started_at"`
	Result      string    `json:"result,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// NewHandler creates a new Tasks handler.
func NewHandler(deps Dependencies) *Handler {
	return &Handler{
		logger:   deps.Logger,
		config:   deps.Config,
		director: deps.Director,
	}
}

// ServeHTTP handles task-related endpoints with pattern matching.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	// List all tasks - GET /b/tasks
	if req.URL.Path == "/b/tasks" && req.Method == http.MethodGet {
		h.ListTasks(writer, req)

		return
	}

	// Task details - GET /b/tasks/{id}
	detailPattern := regexp.MustCompile(`^/b/tasks/([0-9]+)$`)
	if m := detailPattern.FindStringSubmatch(req.URL.Path); m != nil && req.Method == http.MethodGet {
		taskID, _ := strconv.Atoi(m[1])
		h.GetTaskDetails(writer, req, taskID)

		return
	}

	// Task output - GET /b/tasks/{id}/output
	outputPattern := regexp.MustCompile(`^/b/tasks/([0-9]+)/output$`)
	if m := outputPattern.FindStringSubmatch(req.URL.Path); m != nil && req.Method == http.MethodGet {
		taskID, _ := strconv.Atoi(m[1])
		h.GetTaskOutput(writer, req, taskID)

		return
	}

	// Cancel task - POST /b/tasks/{id}/cancel
	cancelPattern := regexp.MustCompile(`^/b/tasks/([0-9]+)/cancel$`)
	if m := cancelPattern.FindStringSubmatch(req.URL.Path); m != nil && req.Method == http.MethodPost {
		taskID, _ := strconv.Atoi(m[1])
		h.CancelTask(writer, req, taskID)

		return
	}

	// No matching endpoint
	writer.WriteHeader(http.StatusNotFound)
	response.HandleJSON(writer, nil, errTaskEndpointNotFound)
}

// ListTasks returns a list of all BOSH tasks.
func (h *Handler) ListTasks(writer http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("task-list")
	logger.Debug("Listing BOSH tasks")

	// Parse query parameters
	state := req.URL.Query().Get("state")

	limit := req.URL.Query().Get("limit")
	if limit == "" {
		limit = "50"
	}

	// TODO: Implement actual BOSH task fetching
	// For now, return sample tasks
	tasks := []Task{
		{
			ID:          1,
			State:       "done",
			Description: "create deployment",
			Timestamp:   time.Now().Unix(),
			StartedAt:   time.Now().Add(-1 * time.Hour),
			Result:      "success",
		},
		{
			ID:          TaskIDProcessing,
			State:       "processing",
			Description: "update deployment",
			Timestamp:   time.Now().Unix(),
			StartedAt:   time.Now().Add(-10 * time.Minute),
		},
	}

	// Filter by state if provided
	if state != "" {
		var filtered []Task

		for _, task := range tasks {
			if task.State == state {
				filtered = append(filtered, task)
			}
		}

		tasks = filtered
	}

	response.HandleJSON(writer, map[string]interface{}{
		"tasks": tasks,
		"count": len(tasks),
		"limit": limit,
	}, nil)
}

// GetTaskDetails returns details for a specific task.
func (h *Handler) GetTaskDetails(writer http.ResponseWriter, req *http.Request, taskID int) {
	logger := h.logger.Named("task-details")
	logger.Debug("Getting details for task %d", taskID)

	// TODO: Implement actual BOSH task detail fetching
	// For now, return sample task
	task := Task{
		ID:          taskID,
		State:       "done",
		Description: fmt.Sprintf("task %d operation", taskID),
		Timestamp:   time.Now().Unix(),
		StartedAt:   time.Now().Add(-30 * time.Minute),
		Result:      "success",
	}

	response.HandleJSON(writer, task, nil)
}

// GetTaskOutput returns the output/logs for a specific task.
func (h *Handler) GetTaskOutput(writer http.ResponseWriter, req *http.Request, taskID int) {
	logger := h.logger.Named("task-output")
	logger.Debug("Getting output for task %d", taskID)

	// Parse query parameters
	outputType := req.URL.Query().Get("type")
	if outputType == "" {
		outputType = "result"
	}

	// TODO: Implement actual BOSH task output fetching
	// For now, return sample output
	output := map[string]interface{}{
		"task_id": taskID,
		"type":    outputType,
		"output":  fmt.Sprintf("Sample output for task %d\nOperation completed successfully", taskID),
	}

	response.HandleJSON(writer, output, nil)
}

// CancelTask cancels a running task.
func (h *Handler) CancelTask(writer http.ResponseWriter, req *http.Request, taskID int) {
	logger := h.logger.Named("task-cancel")

	// Parse optional request body
	var cancelRequest struct {
		Force bool `json:"force"`
	}

	// Try to decode body, but it's optional
	err := json.NewDecoder(req.Body).Decode(&cancelRequest)
	if err != nil && err.Error() != "EOF" {
		logger.Debug("Could not decode cancel request body: %v", err)
	}

	logger.Info("Cancelling task %d (force: %v)", taskID, cancelRequest.Force)

	// TODO: Implement actual BOSH task cancellation
	// For now, return success response
	result := map[string]interface{}{
		"task_id":   taskID,
		"cancelled": true,
		"force":     cancelRequest.Force,
		"message":   fmt.Sprintf("Task %d cancellation initiated", taskID),
	}

	response.HandleJSON(writer, result, nil)
}
