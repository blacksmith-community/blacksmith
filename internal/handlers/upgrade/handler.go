package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"blacksmith/internal/apierr"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/mutation"
	"blacksmith/internal/upgrade"
	"blacksmith/pkg/http/response"
)

// Handler handles upgrade-related endpoints.
type Handler struct {
	logger  interfaces.Logger
	manager *upgrade.Manager
	tasks   taskManager
}

// taskManager is the part of the upgrade manager that the handlers call.
type taskManager interface {
	CreateTask(ctx context.Context, req upgrade.CreateTaskRequest) (*upgrade.UpgradeTask, error)
	GetTask(taskID string) (*upgrade.UpgradeTask, error)
	ListTasks() []*upgrade.UpgradeTask
	PauseTask(ctx context.Context, taskID string) error
	ResumeTask(ctx context.Context, taskID string) error
	CancelTask(ctx context.Context, taskID string) error
	GetSettings() upgrade.Settings
	UpdateSettings(ctx context.Context, settings upgrade.Settings) error
}

// Dependencies contains all dependencies needed by the upgrade handler.
type Dependencies struct {
	Logger                interfaces.Logger
	Director              interfaces.Director
	Vault                 interfaces.Vault
	OnMaxBatchJobsChanged func(maxJobs int) // Callback to update BatchDirector pool size
	// Guard is the broker's one mutation guard. The manager reserves each
	// upgrade's instances in it, and keeps a private guard when it is nil.
	Guard *mutation.Guard
}

// NewHandler creates a new upgrade handler. It applies the persisted
// max_batch_jobs before it starts the manager, so no deploy that the resume
// of an upgrade sends runs above the persisted limit.
func NewHandler(deps Dependencies) *Handler {
	manager := upgrade.NewManager(deps.Logger, deps.Director, deps.Vault)
	manager.SetGuard(deps.Guard)

	// Set callback to sync BatchDirector pool with max_batch_jobs setting
	if deps.OnMaxBatchJobsChanged != nil {
		manager.SetOnMaxBatchJobsChanged(deps.OnMaxBatchJobsChanged)

		// Sync BatchDirector pool with settings loaded from Vault at
		// startup. A limit of 0 restores the configured one.
		deps.OnMaxBatchJobsChanged(manager.GetSettings().MaxBatchJobs)
	}

	manager.Start()

	return &Handler{
		logger:  deps.Logger.Named("upgrade-handler"),
		manager: manager,
		tasks:   manager,
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
	case path == "/settings" && r.Method == http.MethodGet:
		h.GetSettings(w, r)
	case path == "/settings" && r.Method == http.MethodPut:
		h.UpdateSettings(w, r)
	case strings.HasSuffix(path, "/pause") && r.Method == http.MethodPost:
		taskID := strings.TrimPrefix(strings.TrimSuffix(path, "/pause"), "/tasks/")
		h.PauseTask(w, r, taskID)
	case strings.HasSuffix(path, "/resume") && r.Method == http.MethodPost:
		taskID := strings.TrimPrefix(strings.TrimSuffix(path, "/resume"), "/tasks/")
		h.ResumeTask(w, r, taskID)
	case strings.HasSuffix(path, "/cancel") && r.Method == http.MethodPost:
		taskID := strings.TrimPrefix(strings.TrimSuffix(path, "/cancel"), "/tasks/")
		h.CancelTask(w, r, taskID)
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
		response.WriteError(w, http.StatusBadRequest, "invalid upgrade request: "+err.Error())

		return
	}

	h.logger.Info("Creating upgrade task for %d instances, targets: %s", len(req.InstanceIDs), req.DescribeTargets())

	task, err := h.tasks.CreateTask(r.Context(), req)
	if err != nil {
		h.logger.Error("Failed to create upgrade task: %v", err)
		writeCreateError(w, err)

		return
	}

	h.logger.Info("Created upgrade task %s", task.ID)
	response.HandleJSON(w, task, nil)
}

// ListTasks handles GET /b/upgrade/tasks - returns all upgrade tasks.
func (h *Handler) ListTasks(w http.ResponseWriter, r *http.Request) {
	h.logger.Debug("List upgrade tasks request")

	tasks := h.tasks.ListTasks()

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

	task, err := h.tasks.GetTask(taskID)
	if err != nil {
		h.logger.Error("Failed to get upgrade task %s: %v", taskID, err)
		response.WriteErrorCode(w, http.StatusNotFound, apierr.CodeInstanceNotFound, "task not found")

		return
	}

	response.HandleJSON(w, task, nil)
}

// PauseTask handles POST /b/upgrade/tasks/{id}/pause - pauses a running task.
func (h *Handler) PauseTask(w http.ResponseWriter, r *http.Request, taskID string) {
	h.logger.Info("Pause upgrade task request: %s", taskID)

	err := h.tasks.PauseTask(r.Context(), taskID)
	if err != nil {
		h.logger.Error("Failed to pause upgrade task %s: %v", taskID, err)
		writeTaskActionError(w, err)

		return
	}

	response.HandleJSON(w, map[string]interface{}{"success": true, "message": "task paused"}, nil)
}

// ResumeTask handles POST /b/upgrade/tasks/{id}/resume - resumes a paused task.
func (h *Handler) ResumeTask(w http.ResponseWriter, r *http.Request, taskID string) {
	h.logger.Info("Resume upgrade task request: %s", taskID)

	err := h.tasks.ResumeTask(r.Context(), taskID)
	if err != nil {
		h.logger.Error("Failed to resume upgrade task %s: %v", taskID, err)
		writeTaskActionError(w, err)

		return
	}

	response.HandleJSON(w, map[string]interface{}{"success": true, "message": "task resumed"}, nil)
}

// CancelTask handles POST /b/upgrade/tasks/{id}/cancel - cancels a running or paused task.
func (h *Handler) CancelTask(w http.ResponseWriter, r *http.Request, taskID string) {
	h.logger.Info("Cancel upgrade task request: %s", taskID)

	err := h.tasks.CancelTask(r.Context(), taskID)
	if err != nil {
		h.logger.Error("Failed to cancel upgrade task %s: %v", taskID, err)
		writeTaskActionError(w, err)

		return
	}

	response.HandleJSON(w, map[string]interface{}{"success": true, "message": "task cancelled"}, nil)
}

// GetSettings handles GET /b/upgrade/settings - returns current upgrade settings.
func (h *Handler) GetSettings(w http.ResponseWriter, r *http.Request) {
	h.logger.Debug("Get upgrade settings request")

	settings := h.tasks.GetSettings()
	response.HandleJSON(w, upgrade.SettingsResponse(settings), nil)
}

// UpdateSettings handles PUT /b/upgrade/settings - updates upgrade settings.
func (h *Handler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	h.logger.Debug("Update upgrade settings request")

	var req upgrade.UpdateSettingsRequest

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.Error("Failed to decode settings request body: %v", err)
		response.HandleJSON(w, nil, err)

		return
	}

	settings := upgrade.Settings(req)

	if err := h.tasks.UpdateSettings(r.Context(), settings); err != nil {
		h.logger.Error("Failed to update settings: %v", err)
		response.HandleJSON(w, nil, err)

		return
	}

	h.logger.Info("Updated upgrade settings: max_batch_jobs=%d", settings.MaxBatchJobs)
	response.HandleJSON(w, map[string]interface{}{"success": true, "max_batch_jobs": settings.MaxBatchJobs}, nil)
}

// writeCreateError answers a failed CreateTask. A request that names no
// instance, no valid target, a release version the director lacks, or a
// kind this endpoint does not create is a bad request. An instance another upgrade holds is a conflict, and a
// manager still loading the stored upgrades is not ready yet, so a client
// can tell those from a failure.
func writeCreateError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, upgrade.ErrNoInstances),
		errors.Is(err, upgrade.ErrNoUpgradeTarget),
		errors.Is(err, upgrade.ErrInvalidStemcell),
		errors.Is(err, upgrade.ErrInvalidRelease),
		errors.Is(err, upgrade.ErrReleaseNotOnDirector),
		errors.Is(err, upgrade.ErrInvalidTaskKind),
		errors.Is(err, upgrade.ErrCertificateTaskKind):
		response.WriteError(writer, http.StatusBadRequest, err.Error())
	case errors.Is(err, upgrade.ErrInstanceBusy):
		// The guard's error names the operation that holds the instance.
		apierr.Write(writer, apierr.Wrap(http.StatusConflict, apierr.CodeInstanceBusy, err), http.StatusConflict)
	case errors.Is(err, upgrade.ErrTasksLoading):
		apierr.Write(writer, apierr.Wrap(http.StatusServiceUnavailable, apierr.CodeTasksLoading, err), http.StatusServiceUnavailable)
	default:
		response.HandleJSON(writer, nil, err)
	}
}

// writeTaskActionError answers a failed pause, resume, or cancel. An unknown
// task is not found, a task whose state does not allow the action is a
// conflict, and anything else is an internal error.
func writeTaskActionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, upgrade.ErrTaskNotFound):
		response.WriteErrorCode(w, http.StatusNotFound, apierr.CodeInstanceNotFound, err.Error())
	case errors.Is(err, upgrade.ErrTaskNotRunning), errors.Is(err, upgrade.ErrTaskNotPaused):
		response.WriteErrorCode(w, http.StatusConflict, apierr.CodeInvalidTaskState, err.Error())
	default:
		response.WriteErrorCode(w, http.StatusInternalServerError, apierr.CodeInternal, err.Error())
	}
}
