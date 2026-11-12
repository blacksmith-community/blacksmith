package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"blacksmith/internal/apierr"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/config"
	"blacksmith/internal/credhub"
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
	// commands reads the commands configuration, which may be nil.
	commands func() config.CommandsConfig
}

// taskManager is the part of the upgrade manager that the handlers call.
type taskManager interface {
	CreateTask(ctx context.Context, req upgrade.CreateTaskRequest) (*upgrade.UpgradeTask, error)
	PreviewUpgrade(ctx context.Context, req upgrade.CreateTaskRequest) (*upgrade.UpgradePreview, error)
	CreateCertificateTask(ctx context.Context, req upgrade.CreateCertificateTaskRequest) (*upgrade.UpgradeTask, bool, error)
	PreviewCommand(ctx context.Context, req upgrade.CommandPreviewRequest) (*upgrade.CommandPreview, error)
	CreateCommandTask(ctx context.Context, req upgrade.CommandCreateRequest) (*upgrade.UpgradeTask, bool, error)
	DiscardCommandRequest(ctx context.Context, req upgrade.CommandDiscardRequest) (upgrade.CommandDiscardResult, error)
	ReadCommandOutput(ctx context.Context, taskID, instanceID, vm string) (upgrade.CommandOutput, error)
	GetTask(taskID string) (*upgrade.UpgradeTask, error)
	DecisionTaskKind(taskID string) (upgrade.TaskKind, error)
	ListTasks() []*upgrade.UpgradeTask
	PauseTask(ctx context.Context, taskID string) error
	ResumeTaskGated(ctx context.Context, taskID string, gate func(context.Context) error) error
	CancelTask(ctx context.Context, taskID string) error
	RepreviewForDecision(ctx context.Context, taskID, instanceID string) (*upgrade.Preview, error)
	RefreshCandidates(ctx context.Context, taskID, instanceID string) error
	ReconcileRegeneration(ctx context.Context, taskID, instanceID string, req upgrade.ReconcileRequest) error
	ApproveChangedEffects(ctx context.Context, taskID, instanceID string, req upgrade.ApproveRequest) error
	ResolveDeployOutcome(ctx context.Context, taskID, instanceID string, req upgrade.ResolveRequest) error
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
	// CredHub is the director's CredHub, or nil when the broker has none.
	CredHub upgrade.CredHubAPI
	// CredHubState reports whether CredHub is usable. It may be nil.
	CredHubState func() (credhub.CapabilityState, string)
	// Certificates is the certificate inventory. It may be nil.
	Certificates *certinventory.Service
	// Switches returns the certificate configuration switches as they stand
	// now. When nil, rotation counts as off.
	Switches func() config.CertificatesConfig
	// Previews holds the rotation previews. The manager keeps its own
	// store when it is nil.
	Previews *upgrade.PreviewStore
	// UpgradePreviews holds the previews of upgrades that include a
	// diverged instance. The manager keeps its own store when it is nil.
	UpgradePreviews *upgrade.UpgradePreviewStore
	// Prober reads the leaf certificate a listener serves, which a
	// redeploy checks once its deploy is done.
	Prober certinventory.Prober
	// PatchRunner opens the SSH sessions patch mode runs its scripts
	// through. It may be nil, and then no instance can be patched.
	PatchRunner func(ctx context.Context, deployment, group string, indices []int) (upgrade.PatchSession, error)
	// CommandRunner opens the SSH sessions batch commands run through,
	// which never log what a command prints. It may be nil, and then no
	// command can run.
	CommandRunner func(ctx context.Context, deployment, group string, indices []int) (upgrade.PatchSession, error)
	// Commands returns the commands configuration as it stands now. When
	// nil, commands count as off.
	Commands func() config.CommandsConfig
	// BOSH returns the BOSH configuration as it stands now, which tells a
	// command preview how many director workers a parallel run may use.
	BOSH func() config.BOSHConfig
}

// NewHandler creates a new upgrade handler. It applies the persisted
// max_batch_jobs before it starts the manager, so no deploy that the resume
// of an upgrade sends runs above the persisted limit.
func NewHandler(deps Dependencies) *Handler {
	manager := upgrade.NewManager(deps.Logger, deps.Director, deps.Vault)
	manager.SetGuard(deps.Guard)
	configureManager(manager, deps)

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

		commands: deps.Commands,
	}
}

// configureManager hands the manager what its certificate tasks need. It
// runs before Start, which loads the stored certificate tasks, so no loaded
// rotation runs without its checks.
func configureManager(manager *upgrade.Manager, deps Dependencies) {
	d := upgrade.Deps{
		CredHub:         deps.CredHub,
		CredHubState:    deps.CredHubState,
		Switches:        deps.Switches,
		Previews:        deps.Previews,
		UpgradePreviews: deps.UpgradePreviews,
		Prober:          deps.Prober,
		PatchRunner:     deps.PatchRunner,
		CommandRunner:   deps.CommandRunner,
		Commands:        deps.Commands,
		BOSH:            deps.BOSH,
	}

	if deps.Certificates != nil {
		d.ScanInstance = deps.Certificates.ScanInstance
		d.Settings = deps.Certificates.Settings
		d.Divergence = deps.Certificates.Divergence()

		if d.CredHubState == nil {
			d.CredHubState = deps.Certificates.CredHubState
		}
	}

	err := manager.Configure(d)
	if err != nil {
		deps.Logger.Error("Failed to configure the upgrade manager for certificate tasks: %v", err)
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

	// The decision routes are matched before the task routes, so none of
	// them is taken for a task action.
	if r.Method == http.MethodPost {
		taskID, instanceID, action, ok := decisionRoute(path)
		if ok {
			h.Decide(w, r, taskID, instanceID, action)

			return
		}
	}

	switch {
	case path == "/tasks" && r.Method == http.MethodPost:
		h.CreateTask(w, r)
	case path == "/preview" && r.Method == http.MethodPost:
		h.PreviewUpgrade(w, r)
	case path == "/commands/preview" && r.Method == http.MethodPost:
		h.PreviewCommand(w, r)
	case path == "/commands/availability" && r.Method == http.MethodGet:
		h.CommandAvailability(w, r)
	case path == "/commands/discard" && r.Method == http.MethodPost:
		h.DiscardCommand(w, r)
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
	case strings.HasPrefix(path, "/tasks/") && strings.HasSuffix(path, "/output") && r.Method == http.MethodGet:
		taskID := strings.TrimPrefix(strings.TrimSuffix(path, "/output"), "/tasks/")
		h.GetCommandOutput(w, r, taskID)
	case strings.HasPrefix(path, "/tasks/") && r.Method == http.MethodGet:
		taskID := strings.TrimPrefix(path, "/tasks/")
		h.GetTask(w, r, taskID)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("upgrade endpoint not found"))
	}
}

// The instance decision routes under /tasks/{id}/instances/{instance-id}/.
const (
	decisionReconcile = "reconcile"
	decisionApprove   = "approve"
	decisionPreview   = "preview"
	decisionResolve   = "resolve"
	// decisionCandidates lists a reconcile decision's candidates again.
	decisionCandidates = "candidates"
)

// decisionRoute splits /tasks/{id}/instances/{instance-id}/{action} into
// its parts, and reports whether the path is a decision route.
func decisionRoute(path string) (string, string, string, bool) {
	rest, found := strings.CutPrefix(path, "/tasks/")
	if !found {
		return "", "", "", false
	}

	parts := strings.Split(rest, "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] != "instances" || parts[2] == "" {
		return "", "", "", false
	}

	switch parts[3] {
	case decisionReconcile, decisionApprove, decisionPreview, decisionResolve, decisionCandidates:
		return parts[0], parts[2], parts[3], true
	default:
		return "", "", "", false
	}
}

// Decide handles POST /b/upgrade/tasks/{id}/instances/{instance-id}/
// reconcile, approve, resolve, candidates, and preview. A decision answers
// 200 with the task as it stands after it, candidates takes no body and
// answers 200 with the task once the decision's candidates are listed
// again, and preview answers 200 with the bound preview. An error answers
// with its own status and code.
func (h *Handler) Decide(w http.ResponseWriter, r *http.Request, taskID, instanceID, action string) {
	h.logger.Info("Decision %s on instance %s of task %s", action, instanceID, taskID)

	err := h.gateCommandDecision(r, taskID)
	if err != nil {
		h.logger.Info("Refused decision %s on command task %s: %v", action, taskID, err)
		writeCreateError(w, err)

		return
	}

	if action == decisionPreview {
		preview, err := h.tasks.RepreviewForDecision(r.Context(), taskID, instanceID)
		if err != nil {
			h.logger.Error("Failed to preview instance %s of task %s for its decision: %v", instanceID, taskID, err)
			apierr.Write(w, err, http.StatusInternalServerError)

			return
		}

		response.JSON(w, http.StatusOK, preview)

		return
	}

	if action == decisionCandidates {
		err = h.tasks.RefreshCandidates(r.Context(), taskID, instanceID)
	} else {
		var body []byte

		body, err = readDecision(w, r)
		if err == nil {
			err = h.decide(r.Context(), taskID, instanceID, action, body)
		}
	}

	if err != nil {
		h.logger.Error("Failed to apply decision %s on instance %s of task %s: %v", action, instanceID, taskID, err)
		apierr.Write(w, err, http.StatusInternalServerError)

		return
	}

	task, err := h.tasks.GetTask(taskID)
	if err != nil {
		apierr.Write(w, err, http.StatusInternalServerError)

		return
	}

	response.JSON(w, http.StatusOK, task)
}

// gateCommandDecision gives a decision on a command task the read gate the
// task detail takes, because the answer is the task and a command task
// carries its command text. It runs before the decision is applied, so a
// caller the gate refuses changes nothing. A task the gate cannot read,
// while the stored tasks load or because it is missing, is turned away
// with the error the decision would give, and the decision never runs.
func (h *Handler) gateCommandDecision(r *http.Request, taskID string) error {
	kind, err := h.tasks.DecisionTaskKind(taskID)
	if err != nil {
		return err
	}

	if kind != upgrade.TaskKindCommand {
		return nil
	}

	return h.manager.Authorizer().AuthorizeRead(r.Context(), r)
}

// decide decodes the body of the decision and hands it to the manager. A
// body that does not decode is a bad request.
func (h *Handler) decide(ctx context.Context, taskID, instanceID, action string, body []byte) error {
	var err error

	switch action {
	case decisionReconcile:
		var req upgrade.ReconcileRequest

		err = decodeDecision(body, &req)
		if err == nil {
			err = h.tasks.ReconcileRegeneration(ctx, taskID, instanceID, req)
		}
	case decisionApprove:
		var req upgrade.ApproveRequest

		err = decodeDecision(body, &req)
		if err == nil {
			err = h.tasks.ApproveChangedEffects(ctx, taskID, instanceID, req)
		}
	default:
		var req upgrade.ResolveRequest

		err = decodeDecision(body, &req)
		if err == nil {
			err = h.tasks.ResolveDeployOutcome(ctx, taskID, instanceID, req)
		}
	}

	return err //nolint:wrapcheck // the manager answers with the API error
}

// readDecision reads a decision body of at most maxCreateBody bytes, and
// answers 400 invalid_request for one that is too large or can't be read.
func readDecision(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCreateBody))
	if err == nil {
		return body, nil
	}

	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return nil, apierr.New(http.StatusBadRequest, apierr.CodeInvalidRequest,
			fmt.Sprintf("The decision request is larger than %d bytes.", tooLarge.Limit))
	}

	return nil, apierr.New(http.StatusBadRequest, apierr.CodeInvalidRequest, "Blacksmith could not read the decision request: "+err.Error())
}

// decodeDecision decodes a decision body, and answers 400 invalid_request
// for one that is not valid JSON.
func decodeDecision(body []byte, req any) error {
	err := json.Unmarshal(body, req)
	if err != nil {
		return apierr.New(http.StatusBadRequest, apierr.CodeInvalidRequest, "The decision request is not valid JSON: "+err.Error())
	}

	return nil
}

// CanHandle returns true if this handler can handle the given path.
func (h *Handler) CanHandle(path string) bool {
	return strings.HasPrefix(path, "/b/upgrade/")
}

// maxCreateBody bounds the body of a create request.
const maxCreateBody = 1 << 20

// writeInvalidRequest answers 400 invalid_request, as spec 9.4 has a body
// that is unreadable, too large, or not valid JSON answer.
func writeInvalidRequest(w http.ResponseWriter, message string) {
	response.WriteErrorCode(w, http.StatusBadRequest, apierr.CodeInvalidRequest, message)
}

// CreateTask handles POST /b/upgrade/tasks, which creates an upgrade task,
// or a certificate task when the body's kind is certificates.
func (h *Handler) CreateTask(w http.ResponseWriter, r *http.Request) {
	h.logger.Debug("Create upgrade task request")

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCreateBody))
	if err != nil {
		h.logger.Error("Failed to read request body: %v", err)
		writeInvalidRequest(w, "invalid upgrade request: "+err.Error())

		return
	}

	var kind struct {
		Kind upgrade.TaskKind `json:"kind"`
	}

	err = json.Unmarshal(body, &kind)
	if err != nil {
		h.logger.Error("Failed to decode request body: %v", err)
		writeInvalidRequest(w, "invalid upgrade request: "+err.Error())

		return
	}

	if kind.Kind == upgrade.TaskKindCertificates {
		h.createCertificateTask(w, r, body)

		return
	}

	if kind.Kind == upgrade.TaskKindCommand {
		h.createCommandTask(w, r, body)

		return
	}

	var req upgrade.CreateTaskRequest

	err = json.Unmarshal(body, &req)
	if err != nil {
		h.logger.Error("Failed to decode request body: %v", err)
		writeInvalidRequest(w, "invalid upgrade request: "+err.Error())

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

// PreviewUpgrade handles POST /b/upgrade/preview. It takes the body of an
// upgrade create request and answers 200 with the upgrade preview, which
// lists each diverged instance and what its deploy would apply. A failure
// answers as a failed create does.
func (h *Handler) PreviewUpgrade(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCreateBody))
	if err != nil {
		apierr.Write(w, apierr.New(http.StatusBadRequest, apierr.CodeInvalidRequest,
			"Blacksmith could not read the upgrade preview request: "+err.Error()), http.StatusBadRequest)

		return
	}

	var req upgrade.CreateTaskRequest

	err = json.Unmarshal(body, &req)
	if err != nil {
		apierr.Write(w, apierr.New(http.StatusBadRequest, apierr.CodeInvalidRequest,
			"The upgrade preview request is not valid JSON: "+err.Error()), http.StatusBadRequest)

		return
	}

	h.logger.Info("Previewing an upgrade of %d instances, targets: %s", len(req.InstanceIDs), req.DescribeTargets())

	preview, err := h.tasks.PreviewUpgrade(r.Context(), req)
	if err != nil {
		h.logger.Error("Failed to preview the upgrade: %v", err)
		writeCreateError(w, err)

		return
	}

	h.logger.Info("Upgrade preview %s lists %d diverged instances", preview.ID, len(preview.Diverged))
	response.JSON(w, http.StatusOK, preview)
}

// createCertificateTask creates a certificate task from the preview the
// body names. A new task answers 201 and a retry that found the task its
// request ID already made answers 200, both with the task. Any created_from
// in the body is ignored, because the request type has no such field.
func (h *Handler) createCertificateTask(w http.ResponseWriter, r *http.Request, body []byte) {
	var req upgrade.CreateCertificateTaskRequest

	err := json.Unmarshal(body, &req)
	if err != nil {
		h.logger.Error("Failed to decode certificate task request: %v", err)
		writeInvalidRequest(w, "invalid certificate task request: "+err.Error())

		return
	}

	h.logger.Info("Creating certificate task from preview %s", req.PreviewID)

	task, existing, err := h.tasks.CreateCertificateTask(r.Context(), req)
	if err != nil {
		h.logger.Error("Failed to create certificate task: %v", err)

		if errors.Is(err, upgrade.ErrTasksLoading) {
			writeCreateError(w, err)

			return
		}

		// Every other failure carries its own status and code, and one that
		// doesn't is an internal error.
		apierr.Write(w, err, http.StatusInternalServerError)

		return
	}

	status := http.StatusCreated
	if existing {
		status = http.StatusOK
	}

	h.logger.Info("Answered certificate task %s, existing: %t", task.ID, existing)
	response.JSON(w, status, task)
}

// CommandAvailability handles GET /b/upgrade/commands/availability. It
// answers whether commands are on and whether they may run as root, read from
// the configuration the authorizer reads, so the console learns the switches
// without sending a preview that the browser would log as a failure. It takes
// no input, runs nothing, and answers the two switches and the most nodes a
// parallel run may use, which is the limit a preview reports from the same
// worker count, and nothing else. The answer follows live configuration, so
// it is marked as not storable.
func (h *Handler) CommandAvailability(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	response.JSON(w, http.StatusOK, commandAvailabilityAnswer{
		CommandAvailability: h.manager.Authorizer().Availability(),
		MaxInFlightLimit:    h.manager.CommandMaxInFlightLimit(r.Context()),
	})
}

// commandAvailabilityAnswer is the body of the availability answer.
type commandAvailabilityAnswer struct {
	upgrade.CommandAvailability
	MaxInFlightLimit int `json:"max_in_flight_limit"`
}

// PreviewCommand handles POST /b/upgrade/commands/preview. It takes the
// instances, the command, and its settings, and answers 200 with the stored
// preview, which holds the command's digest and length and never its text.
// The request is authorized right after its body is decoded, because the
// authorizer needs run_as, and before any instance is read, so a caller with
// no second credential learns nothing about the instances.
func (h *Handler) PreviewCommand(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCreateBody))
	if err != nil {
		writeInvalidRequest(w, "invalid command preview request: "+err.Error())

		return
	}

	var req upgrade.CommandPreviewRequest

	err = json.Unmarshal(body, &req)
	if err != nil {
		writeInvalidRequest(w, "invalid command preview request: "+err.Error())

		return
	}

	err = h.manager.Authorizer().AuthorizeRequest(r.Context(), r, req.RunAs)
	if err != nil {
		h.logger.Info("Refused a command preview: %v", err)
		writeCreateError(w, err)

		return
	}

	h.logger.Info("Previewing a command of %d bytes on %d instances", len(req.Command), len(req.InstanceIDs))

	preview, err := h.tasks.PreviewCommand(r.Context(), req)
	if err != nil {
		h.logger.Error("Failed to preview the command: %v", err)
		writeCreateError(w, err)

		return
	}

	h.logger.Info("Command preview %s lists %d instances", preview.ID, len(preview.Instances))
	response.JSON(w, http.StatusOK, preview)
}

// DiscardCommand handles POST /b/upgrade/commands/discard. It takes the
// client request ID and the preview ID of a create that may still be on its
// way, and answers 200 with whether the discard stopped it. The feature
// switch applies, but the second credential does not, because a discard can
// only stop a run and never start one.
func (h *Handler) DiscardCommand(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCreateBody))
	if err != nil {
		writeInvalidRequest(w, "invalid command discard request: "+err.Error())

		return
	}

	var req upgrade.CommandDiscardRequest

	err = json.Unmarshal(body, &req)
	if err != nil {
		writeInvalidRequest(w, "invalid command discard request: "+err.Error())

		return
	}

	// A stored check applies the feature switch and, for the vcap user,
	// nothing else, and it reads no credential from the request.
	err = h.manager.Authorizer().AuthorizeStored(r.Context(), upgrade.CommandTaskSpec{RunAs: "vcap"})
	if err != nil {
		h.logger.Info("Refused a command discard: %v", err)
		writeCreateError(w, err)

		return
	}

	result, err := h.tasks.DiscardCommandRequest(r.Context(), req)
	if err != nil {
		h.logger.Error("Failed to discard the command request: %v", err)
		writeCreateError(w, err)

		return
	}

	response.JSON(w, http.StatusOK, result)
}

// createCommandTask creates a command task from the preview the body names.
// It authorizes the request before the preview is read, so a caller without
// the second credential can't spend a preview. A new task answers 201 and a
// retry that found the task its request ID already made answers 200, both
// with the task, which carries the command text and never any output. A body
// with pause_on_failure answers 400 whatever its value, null included, since
// a command task always pauses after a failure. Nothing here logs the
// command.
func (h *Handler) createCommandTask(w http.ResponseWriter, r *http.Request, body []byte) {
	var req upgrade.CommandCreateRequest

	err := json.Unmarshal(body, &req)
	if err != nil {
		writeInvalidRequest(w, "invalid command task request: "+err.Error())

		return
	}

	// A command task always pauses after a failed node, so a request that
	// sends the field at all, whatever its value, is refused rather than
	// quietly ignored.
	var fields map[string]json.RawMessage

	err = json.Unmarshal(body, &fields)
	if _, sent := fields["pause_on_failure"]; err == nil && sent {
		writeInvalidRequest(w, "a command task always pauses after a failure, so it takes no pause_on_failure")

		return
	}

	err = h.manager.Authorizer().AuthorizeRequest(r.Context(), r, req.RunAs)
	if err != nil {
		h.logger.Info("Refused a command task: %v", err)
		writeCreateError(w, err)

		return
	}

	// The authorizer has already proved a configured key matched the header,
	// so the flag says a configured credential was checked, and a header
	// sent to a deployment with no key does not set it.
	req.CredentialPresented = h.commandKeyConfigured() && r.Header.Get(upgrade.CommandKeyHeader) != ""

	h.logger.Info("Creating a command task of %d bytes from preview %s", len(req.Command), req.PreviewID)

	task, existing, err := h.tasks.CreateCommandTask(r.Context(), req)
	if err != nil {
		h.logger.Error("Failed to create the command task: %v", err)
		writeCreateError(w, err)

		return
	}

	status := http.StatusCreated
	if existing {
		status = http.StatusOK
	}

	h.logger.Info("Answered command task %s, existing: %t", task.ID, existing)
	response.JSON(w, status, task)
}

// commandKeyConfigured reports whether a second credential is configured.
func (h *Handler) commandKeyConfigured() bool {
	return h.commands != nil && h.commands().CredentialKey() != ""
}

// commandOutputResponse is what one node printed. The streams are decoded
// text, and the counts and flags say how much was kept of how much the node
// sent. Merged is true when stdout holds the command's stderr too, in the
// order it was written, and stderr is empty. An entry saved before output
// was merged answers false, with both streams as they were saved.
type commandOutputResponse struct {
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	StdoutKept      int64  `json:"stdout_kept"`
	StdoutReceived  int64  `json:"stdout_received"`
	StderrKept      int64  `json:"stderr_kept"`
	StderrReceived  int64  `json:"stderr_received"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
	Merged          bool   `json:"merged"`
}

// GetCommandOutput handles GET /b/upgrade/tasks/{id}/output?vm=<group>-<index>
// and answers with what that node printed. The read is authorized for the
// feature switch and the second credential and never for the user the run
// used, so a finished root run stays readable while root runs are off. The
// optional instance parameter names the instance when more than one has the
// node. The path of the stored entry comes from the task's own records.
func (h *Handler) GetCommandOutput(w http.ResponseWriter, r *http.Request, taskID string) {
	err := h.manager.Authorizer().AuthorizeRead(r.Context(), r)
	if err != nil {
		h.logger.Info("Refused a command output read of task %s: %v", taskID, err)
		writeCreateError(w, err)

		return
	}

	query := r.URL.Query()

	output, err := h.tasks.ReadCommandOutput(r.Context(), taskID, query.Get("instance"), query.Get("vm"))
	if err != nil {
		h.logger.Info("Could not read command output of task %s: %v", taskID, err)
		writeCreateError(w, err)

		return
	}

	response.JSON(w, http.StatusOK, commandOutputResponse{
		Stdout:          string(output.Stdout),
		Stderr:          string(output.Stderr),
		StdoutKept:      output.StdoutKept,
		StdoutReceived:  output.StdoutReceived,
		StderrKept:      output.StderrKept,
		StderrReceived:  output.StderrReceived,
		StdoutTruncated: output.StdoutTruncated,
		StderrTruncated: output.StderrTruncated,
		Merged:          output.Merged,
	})
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

	// The detail of a command task carries the full command text, so it
	// needs the gate the output read has.
	if task.KindOrDefault() == upgrade.TaskKindCommand {
		err = h.manager.Authorizer().AuthorizeRead(r.Context(), r)
		if err != nil {
			h.logger.Info("Refused the detail of command task %s: %v", taskID, err)
			writeCreateError(w, err)

			return
		}
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

	// A resume can send a stored command to VMs, so it takes the second
	// credential the output read does. The manager runs the gate, because
	// only it knows under its lock whether the resume sends a command. A
	// resume that only cleans a cancelled task never asks. Pause and cancel
	// send nothing.
	var gateErr error

	gate := func(ctx context.Context) error {
		gateErr = h.manager.Authorizer().AuthorizeRead(ctx, r)

		return gateErr //nolint:wrapcheck // the authorizer answers with the API error
	}

	err := h.tasks.ResumeTaskGated(r.Context(), taskID, gate)
	if err != nil {
		if gateErr != nil && errors.Is(err, gateErr) {
			h.logger.Info("Refused the resume of command task %s: %v", taskID, err)
			writeCreateError(w, err)

			return
		}

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

// writeCreateError answers a failed CreateTask. An error that carries its
// own status and code answers with them. A request that names no
// instance, no valid target, a release version the director lacks, or a
// kind this endpoint does not create is a bad request. An instance another
// upgrade holds is a conflict, and a manager still loading the stored
// upgrades is not ready yet, so a client can tell those from a failure.
func writeCreateError(writer http.ResponseWriter, err error) {
	var apiErr *apierr.Error

	switch {
	case errors.As(err, &apiErr):
		apierr.Write(writer, err, http.StatusInternalServerError)
	case errors.Is(err, upgrade.ErrNoInstances),
		errors.Is(err, upgrade.ErrNoUpgradeTarget),
		errors.Is(err, upgrade.ErrInvalidStemcell),
		errors.Is(err, upgrade.ErrInvalidRelease),
		errors.Is(err, upgrade.ErrReleaseNotOnDirector),
		errors.Is(err, upgrade.ErrInvalidTaskKind),
		errors.Is(err, upgrade.ErrCertificateTaskKind),
		errors.Is(err, upgrade.ErrCommandTaskKind):
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

// writeTaskActionError answers a failed pause, resume, or cancel. An error
// that carries its own status and code answers with them, an unknown task
// is not found, a task whose state does not allow the action is a
// conflict, a manager still loading the stored tasks is not ready yet, and
// anything else is an internal error.
func writeTaskActionError(w http.ResponseWriter, err error) {
	var apiErr *apierr.Error

	switch {
	case errors.As(err, &apiErr):
		// A refusal that carries its own status and code, such as a
		// certificate task's resume while rotation is off, keeps them.
		apierr.Write(w, err, http.StatusInternalServerError)
	case errors.Is(err, upgrade.ErrTaskNotFound):
		response.WriteErrorCode(w, http.StatusNotFound, apierr.CodeInstanceNotFound, err.Error())
	case errors.Is(err, upgrade.ErrTaskNotRunning), errors.Is(err, upgrade.ErrTaskNotPaused):
		response.WriteErrorCode(w, http.StatusConflict, apierr.CodeInvalidTaskState, err.Error())
	case errors.Is(err, upgrade.ErrTasksLoading):
		apierr.Write(w, apierr.Wrap(http.StatusServiceUnavailable, apierr.CodeTasksLoading, err), http.StatusServiceUnavailable)
	case errors.Is(err, upgrade.ErrCleanupNotStarted):
		apierr.Write(w, apierr.Wrap(http.StatusServiceUnavailable, apierr.CodeCleanupNotStarted, err), http.StatusServiceUnavailable)
	default:
		response.WriteErrorCode(w, http.StatusInternalServerError, apierr.CodeInternal, err.Error())
	}
}
