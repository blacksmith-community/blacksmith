package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"blacksmith/internal/bosh"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"
)

// Error variables for err113 compliance.
var (
	errTaskEndpointNotFound = errors.New("task endpoint not found")
	errInvalidOffset        = errors.New("invalid offset")
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
	ID          int        `json:"id"`
	State       string     `json:"state"`
	Description string     `json:"description"`
	Timestamp   int64      `json:"timestamp"`
	StartedAt   time.Time  `json:"started_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	Result      string     `json:"result,omitempty"`
	Error       string     `json:"error,omitempty"`
	User        string     `json:"user,omitempty"`
	Deployment  string     `json:"deployment,omitempty"`
	ContextID   string     `json:"context_id,omitempty"`
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
	ctx := req.Context()

	logger := h.logger.Named("task-list")
	logger.Debug("Listing BOSH tasks")

	query := req.URL.Query()

	stateFilter := strings.TrimSpace(query.Get("state"))

	taskType := strings.TrimSpace(query.Get("type"))
	if taskType == "" {
		taskType = "recent"
	}

	limit := 50

	if limitStr := strings.TrimSpace(query.Get("limit")); limitStr != "" {
		parsedLimit, parseErr := strconv.Atoi(limitStr)
		if parseErr == nil && parsedLimit > 0 && parsedLimit <= maxTaskWindow {
			limit = parsedLimit
		} else {
			logger.Debug("Invalid limit parameter '%s', using default", limitStr)
		}
	}

	offset := 0

	if offsetStr := strings.TrimSpace(query.Get("offset")); offsetStr != "" {
		parsedOffset, parseErr := strconv.Atoi(offsetStr)
		if parseErr != nil || parsedOffset < 0 || parsedOffset > maxTaskOffset {
			writer.WriteHeader(http.StatusBadRequest)
			response.HandleJSON(writer, nil, fmt.Errorf("%w: offset must be a number from 0 to %d", errInvalidOffset, maxTaskOffset))

			return
		}

		offset = parsedOffset
	}

	var states []string

	if statesParam := strings.TrimSpace(query.Get("states")); statesParam != "" {
		for _, state := range strings.Split(statesParam, ",") {
			if trimmed := strings.TrimSpace(state); trimmed != "" {
				states = append(states, trimmed)
			}
		}
	}

	team := strings.TrimSpace(query.Get("team"))
	if team == "" {
		team = "blacksmith"
	}

	wantCounts := query.Get("counts") == "1"
	wantObject := wantCounts || query.Get("paged") == "1"

	// A single state with no states list is an exact filter too, so the
	// director applies it before the page is cut.
	if len(states) == 0 && stateFilter != "" && !wantCounts {
		states, stateFilter = []string{strings.ToLower(stateFilter)}, ""
	}

	logger.Debug("Fetching tasks from director", "type", taskType, "limit", limit, "offset", offset, "states", states, "team", team)

	// A page is read as the first offset+limit rows plus one more, so the
	// answer can say whether a later page exists. With counts and no
	// states, those rows are both counted and returned. With states as
	// well, the director is asked for its whole window unfiltered so every
	// state can still be counted, and the chosen states are applied here.
	asked := offset + limit
	fetchStates := states

	if wantCounts {
		fetchStates = nil

		if len(states) > 0 {
			asked = max(asked, maxTaskWindow)
		}
	}

	listing, err := bosh.ListTasksFrom(ctx, h.director, taskType, asked, fetchStates, team)
	if err != nil {
		logger.Error("Failed to fetch tasks from director: %v", err)

		response.HandleJSON(writer, nil, fmt.Errorf("failed to fetch tasks: %w", err))

		return
	}

	scanned := listing.Tasks

	var stateCounts map[string]int

	if wantCounts {
		stateCounts = map[string]int{"processing": 0, "queued": 0, "done": 0, "error": 0, "cancelled": 0}
		for _, directorTask := range scanned {
			stateCounts[directorTask.State]++
		}
	}

	matching := make([]Task, 0, len(scanned))

	for _, directorTask := range scanned {
		if wantCounts && len(states) > 0 && !slices.Contains(states, directorTask.State) {
			continue
		}

		hTask := convertTask(directorTask)
		if stateFilter != "" && !strings.EqualFold(hTask.State, stateFilter) {
			continue
		}

		matching = append(matching, hTask)
	}

	result := matching[min(offset, len(matching)):]
	hasMore := listing.More

	if len(result) > limit {
		result, hasMore = result[:limit], true
	}

	writer.Header().Set(headerHasMore, strconv.FormatBool(hasMore))
	writer.Header().Set(headerExact, strconv.FormatBool(listing.Exact))

	if !wantObject {
		response.HandleJSON(writer, result, nil)

		return
	}

	page := taskPage{Tasks: result, Offset: offset, Limit: limit, HasMore: hasMore, Exact: listing.Exact}

	if wantCounts {
		window := len(scanned)
		page.StateCounts, page.Window = stateCounts, &window
	}

	response.HandleJSON(writer, page, nil)
}

const (
	// maxTaskWindow is the most tasks one page returns.
	maxTaskWindow = 200

	// maxTaskOffset is the deepest offset a page can start at, so that a
	// page's offset plus the largest page is within what the director
	// adapter will list.
	maxTaskOffset = 1800

	// headerHasMore and headerExact carry the paging answer on the bare
	// array form, whose body has no room for it.
	headerHasMore = "X-Blacksmith-Has-More"
	headerExact   = "X-Blacksmith-Exact"
)

// taskPage is the answer to a list request that asked for the object form,
// with counts=1, paged=1, or both. The plain answer stays a bare array, so
// existing callers are unaffected.
//
// HasMore says that a task beyond this page matches the request. Exact is
// false when the listing stopped before it could tell, in which case HasMore
// is true. StateCounts and Window appear only when counts were asked for, and
// they describe the rows from the newest through the end of this page, which
// are the rows a caller that has loaded each page in turn holds.
type taskPage struct {
	Tasks       []Task         `json:"tasks"`
	Offset      int            `json:"offset"`
	Limit       int            `json:"limit"`
	HasMore     bool           `json:"has_more"`
	Exact       bool           `json:"exact"`
	StateCounts map[string]int `json:"state_counts,omitempty"`
	Window      *int           `json:"window,omitempty"`
}

func convertTask(task bosh.Task) Task {
	converted := Task{
		ID:          task.ID,
		State:       task.State,
		Description: task.Description,
		Timestamp:   task.Timestamp,
		StartedAt:   task.StartedAt,
		EndedAt:     task.EndedAt,
		Result:      task.Result,
		User:        task.User,
		Deployment:  task.Deployment,
		ContextID:   task.ContextID,
	}

	if converted.Timestamp == 0 && !converted.StartedAt.IsZero() {
		converted.Timestamp = converted.StartedAt.Unix()
	}

	if task.State == "error" && strings.TrimSpace(task.Result) != "" {
		converted.Error = task.Result
	}

	return converted
}

func parseResultOutputToEvents(resultOutput string) []bosh.TaskEvent {
	events := make([]bosh.TaskEvent, 0)
	if strings.TrimSpace(resultOutput) == "" {
		return events
	}

	lines := strings.Split(resultOutput, "\n")
	for _, line := range lines {
		if event, ok := parseSingleEventLine(line); ok {
			events = append(events, event)
		}
	}

	return events
}

func parseSingleEventLine(line string) (bosh.TaskEvent, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return bosh.TaskEvent{}, false
	}

	boshEvent, err := unmarshalBOSHEvent(line)
	if err != nil {
		return bosh.TaskEvent{}, false
	}

	return buildTaskEvent(boshEvent), true
}

func unmarshalBOSHEvent(line string) (boshEventData, error) {
	var boshEvent boshEventData

	err := json.Unmarshal([]byte(line), &boshEvent)
	if err != nil {
		return boshEvent, fmt.Errorf("failed to unmarshal BOSH event: %w", err)
	}

	return boshEvent, nil
}

type boshEventData struct {
	Time     int64    `json:"time"`
	Stage    string   `json:"stage"`
	Tags     []string `json:"tags"`
	Total    int      `json:"total"`
	Task     string   `json:"task"`
	Index    int      `json:"index"`
	State    string   `json:"state"`
	Progress int      `json:"progress"`
	Data     struct {
		Status string `json:"status"`
	} `json:"data,omitempty"`
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func buildTaskEvent(boshEvent boshEventData) bosh.TaskEvent {
	event := bosh.TaskEvent{
		Time:     time.Unix(boshEvent.Time, 0),
		Stage:    boshEvent.Stage,
		Tags:     boshEvent.Tags,
		Total:    boshEvent.Total,
		Task:     boshEvent.Task,
		Index:    boshEvent.Index,
		State:    boshEvent.State,
		Progress: boshEvent.Progress,
	}

	if boshEvent.Data.Status != "" {
		event.Data = map[string]interface{}{"status": boshEvent.Data.Status}
	}

	if boshEvent.Error.Message != "" {
		event.Error = &bosh.TaskEventError{
			Code:    boshEvent.Error.Code,
			Message: boshEvent.Error.Message,
		}
	}

	return event
}

// GetTaskDetails returns details for a specific task.
func (h *Handler) GetTaskDetails(writer http.ResponseWriter, req *http.Request, taskID int) {
	ctx := req.Context()

	logger := h.logger.Named("task-details")
	logger.Debug("Getting details for task %d", taskID)

	directorTask, err := h.director.GetTask(ctx, taskID)
	if err != nil {
		logger.Error("Failed to fetch task %d: %v", taskID, err)
		response.WriteError(writer, http.StatusNotFound, fmt.Sprintf("task %d not found", taskID))

		return
	}

	events, err := h.director.GetTaskEvents(ctx, taskID)
	if err != nil {
		logger.Debug("Unable to fetch events for task %d: %v", taskID, err)

		events = []bosh.TaskEvent{}
	}

	payload := struct {
		Task   Task             `json:"task"`
		Events []bosh.TaskEvent `json:"events"`
	}{
		Task:   convertTask(*directorTask),
		Events: events,
	}

	response.HandleJSON(writer, payload, nil)
}

// GetTaskOutput returns the output/logs for a specific task.
func (h *Handler) GetTaskOutput(writer http.ResponseWriter, req *http.Request, taskID int) {
	ctx := req.Context()

	logger := h.logger.Named("task-output")
	logger.Debug("Getting output for task %d", taskID)

	outputType := strings.TrimSpace(req.URL.Query().Get("type"))
	if outputType == "" {
		outputType = "task"
	}

	logger.Debug("Fetching task output", "task_id", taskID, "type", outputType)

	output, err := h.director.GetTaskOutput(ctx, taskID, outputType)
	if err != nil {
		logger.Error("Failed to fetch task output for %d (type=%s): %v", taskID, outputType, err)
		response.WriteError(writer, http.StatusNotFound, fmt.Sprintf("task %d output not found", taskID))

		return
	}

	if outputType == "result" || outputType == "event" {
		events := parseResultOutputToEvents(output)
		response.HandleJSON(writer, events, nil)

		return
	}

	response.HandleJSON(writer, map[string]interface{}{
		"task_id": taskID,
		"type":    outputType,
		"output":  output,
	}, nil)
}

// CancelTask cancels a running task.
func (h *Handler) CancelTask(writer http.ResponseWriter, req *http.Request, taskID int) {
	ctx := req.Context()

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

	err = h.director.CancelTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, bosh.ErrTaskCannotBeCancelled) {
			logger.Warn("Task %d cannot be cancelled: %v", taskID, err)
			response.WriteError(writer, http.StatusConflict, fmt.Sprintf("task %d cannot be cancelled", taskID))

			return
		}

		logger.Error("Failed to cancel task %d: %v", taskID, err)
		response.WriteError(writer, http.StatusBadRequest, fmt.Sprintf("failed to cancel task %d", taskID))

		return
	}

	logger.Info("Successfully cancelled task %d", taskID)

	result := map[string]interface{}{
		"task_id":   taskID,
		"cancelled": true,
		"force":     cancelRequest.Force,
		"message":   fmt.Sprintf("Task %d cancelled successfully", taskID),
	}

	response.HandleJSON(writer, result, nil)
}
