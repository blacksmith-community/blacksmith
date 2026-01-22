package upgrade

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"blacksmith/internal/interfaces"
	"blacksmith/internal/upgrade"
	"blacksmith/pkg/http/response"
)

// Handler handles upgrade-related endpoints.
type Handler struct {
	logger  interfaces.Logger
	manager *upgrade.Manager
}

// Dependencies contains all dependencies needed by the upgrade handler.
type Dependencies struct {
	Logger   interfaces.Logger
	Director interfaces.Director
	Vault    interfaces.Vault
}

// NewHandler creates a new upgrade handler.
func NewHandler(deps Dependencies) *Handler {
	manager := upgrade.NewManager(deps.Logger, deps.Director, deps.Vault)

	return &Handler{
		logger:  deps.Logger.Named("upgrade-handler"),
		manager: manager,
	}
}

// Stop stops the handler and its manager.
func (h *Handler) Stop() {
	h.manager.Stop()
}

// GetManager returns the upgrade manager.
func (h *Handler) GetManager() *upgrade.Manager {
	return h.manager
}

// ServeHTTP implements http.Handler for routing upgrade requests.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/b/upgrade")

	switch {
	case path == "/tasks" && r.Method == http.MethodPost:
		h.CreateTask(w, r)
	case path == "/tasks" && r.Method == http.MethodGet:
		h.ListTasks(w, r)
	case strings.HasPrefix(path, "/tasks/") && r.Method == http.MethodGet:
		taskID := strings.TrimPrefix(path, "/tasks/")
		h.GetTask(w, r, taskID)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("upgrade endpoint not found"))
	}
}

// CanHandle returns true if this handler can handle the given path.
func (h *Handler) CanHandle(path string) bool {
	return strings.HasPrefix(path, "/b/upgrade/")
}

// CreateTask handles POST /b/upgrade/tasks - creates a new upgrade task.
func (h *Handler) CreateTask(w http.ResponseWriter, r *http.Request) {
	h.logger.Debug("Create upgrade task request")

	var req upgrade.CreateTaskRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.Error("Failed to decode request body: %v", err)
		response.HandleJSON(w, nil, err)

		return
	}

	h.logger.Info("Creating upgrade task for %d instances, target: %s/%s",
		len(req.InstanceIDs), req.TargetStemcell.OS, req.TargetStemcell.Version)

	task, err := h.manager.CreateTask(r.Context(), req)
	if err != nil {
		h.logger.Error("Failed to create upgrade task: %v", err)
		response.HandleJSON(w, nil, err)

		return
	}

	h.logger.Info("Created upgrade task %s", task.ID)
	response.HandleJSON(w, task, nil)
}

// ListTasks handles GET /b/upgrade/tasks - returns all upgrade tasks.
func (h *Handler) ListTasks(w http.ResponseWriter, r *http.Request) {
	h.logger.Debug("List upgrade tasks request")

	tasks := h.manager.ListTasks()

	// Sort by created_at descending (newest first)
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.After(tasks[j].CreatedAt)
	})

	// Convert to summaries for list view
	summaries := make([]upgrade.TaskSummary, len(tasks))
	for i, task := range tasks {
		summaries[i] = task.ToSummary()
	}

	response.HandleJSON(w, summaries, nil)
}

// GetTask handles GET /b/upgrade/tasks/{id} - returns a specific upgrade task.
func (h *Handler) GetTask(w http.ResponseWriter, r *http.Request, taskID string) {
	h.logger.Debug("Get upgrade task request: %s", taskID)

	task, err := h.manager.GetTask(taskID)
	if err != nil {
		h.logger.Error("Failed to get upgrade task %s: %v", taskID, err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success": false, "error": "task not found"}`))

		return
	}

	response.HandleJSON(w, task, nil)
}
