package deployments

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/audit"
	"blacksmith/internal/bosh"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/mutation"
	"blacksmith/pkg/http/response"
)

// The instance actions the VMs tab offers. Each one changes the job state
// of every VM in the instance's deployment.
const (
	instanceActionRestart  = "restart"
	instanceActionRecreate = "recreate"
)

// An instance action stops waiting for the director before the HTTP
// server's write timeout, so the broker always writes its own answer.
const (
	// defaultActionWriteTimeout is the server's write timeout when the
	// broker sets none, which main.go also uses.
	defaultActionWriteTimeout = 120 * time.Second

	// actionDeadlineMargin is how long before the write timeout an action
	// stops waiting, which leaves time to write the answer.
	actionDeadlineMargin = 10 * time.Second

	// shortActionTimeoutDivisor divides the write timeout to give the least
	// time an action may wait, for a write timeout too short to leave much
	// once actionDeadlineMargin is taken off it.
	shortActionTimeoutDivisor = 2
)

// instanceActionPattern matches POST /b/{instance_id}/restart and
// /b/{instance_id}/recreate.
var instanceActionPattern = regexp.MustCompile(`^/b/([^/]+)/(restart|recreate)$`)

// CanHandleInstanceAction reports whether path is an instance action.
func (h *Handler) CanHandleInstanceAction(path string) bool {
	return instanceActionPattern.MatchString(path)
}

// ServeInstanceAction restarts or recreates every VM of a service instance
// the broker owns. The instance must be in the broker's index, so no other
// BOSH deployment, not even the broker's own, can be reached through it. The
// deployment comes from the index entry, never from the request, and the
// request takes no options, so the director gets its defaults: no skipped
// drain, no fix, and no dry run.
//
// The action holds the instance and its deployment in the mutation guard
// while the director accepts the task, so it never runs beside an upgrade,
// a certificate rotation, or a broker operation on the same instance. Under
// the guard it first asks the director for a task already running on the
// deployment, and answers 409 deployment_locked with that task when there
// is one. It answers 202 with the BOSH task the director created. When the
// director's answer is lost after the request went out, the task may have
// started, so it answers 504 outcome_unknown rather than reporting a
// failure. The director calls end before the server's write timeout, and
// an action whose wait runs out answers outcome_unknown too, so the broker
// always writes its own answer.
//
// The broker's basic-auth wrapper puts the authenticated actor in the
// request context, as it does for every /b/ request. Each action that
// reaches the director logs one audit line with that actor, where the
// request came from, the outcome, and the task. An attempt the broker
// refuses before it reaches the director is audited as refused, with the
// running task when that is what refused it.
func (h *Handler) ServeInstanceAction(writer http.ResponseWriter, req *http.Request) {
	matches := instanceActionPattern.FindStringSubmatch(req.URL.Path)
	if matches == nil {
		response.WriteError(writer, http.StatusNotFound, "endpoint not found")

		return
	}

	instanceID, action := matches[1], matches[2]

	if req.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		response.WriteError(writer, http.StatusMethodNotAllowed, "method not allowed")

		return
	}

	logger := h.logger.Named("instance-" + action)
	actor, _ := audit.ActorFrom(req.Context())

	deployment, owned := h.ownedDeployment(req.Context(), writer, logger, instanceID)
	if !owned {
		h.auditInstanceAction(req.Context(), logger, actor, instanceID, "", action, "refused", 0)

		return
	}

	if h.director == nil {
		h.auditInstanceAction(req.Context(), logger, actor, instanceID, deployment, action, "refused", 0)
		apierr.Write(writer, apierr.New(http.StatusServiceUnavailable, apierr.CodeDirectorUnavailable, errDirectorNotConfigured.Error()), http.StatusServiceUnavailable)

		return
	}

	release, reserved := h.reserveInstance(writer, logger, instanceID, deployment, action)
	if !reserved {
		h.auditInstanceAction(req.Context(), logger, actor, instanceID, deployment, action, "refused", 0)

		return
	}
	defer release()

	ctx, cancel := h.instanceActionContext(req.Context())
	defer cancel()

	if runningTask, idle := h.deploymentIsIdle(ctx, writer, logger, instanceID, deployment, action); !idle {
		h.auditInstanceAction(req.Context(), logger, actor, instanceID, deployment, action, "refused", runningTask)

		return
	}

	task, err := h.runInstanceAction(ctx, deployment, action)
	err = outOfTime(ctx, err)
	h.answerInstanceAction(req.Context(), writer, logger, actor, instanceActionProblem{InstanceID: instanceID, Deployment: deployment, Operation: action}, task, err)
}

// instanceActionContext bounds the director calls of an instance action to
// the server's write timeout less actionDeadlineMargin, or to half the write
// timeout when that is longer, as the logs request does.
func (h *Handler) instanceActionContext(parent context.Context) (context.Context, context.CancelFunc) {
	writeTimeout := defaultActionWriteTimeout
	if h.config != nil && h.config.GetBrokerConfig().WriteTimeout > 0 {
		writeTimeout = time.Duration(h.config.GetBrokerConfig().WriteTimeout) * time.Second
	}

	limit := max(writeTimeout-actionDeadlineMargin, writeTimeout/shortActionTimeoutDivisor)
	cause := fmt.Errorf("%w, because the write timeout of %d seconds is near", errInstanceActionOutOfTime, int(writeTimeout/time.Second))

	return context.WithTimeoutCause(parent, limit, cause)
}

// outOfTime marks an action error as an unknown outcome when the action's
// deadline ended the wait, because the request may already have reached
// the director by then. Any other error is returned as it is.
func outOfTime(ctx context.Context, err error) error {
	if err != nil && errors.Is(context.Cause(ctx), errInstanceActionOutOfTime) {
		return fmt.Errorf("%w: %w: %w", bosh.ErrOutcomeUnknown, context.Cause(ctx), err)
	}

	return err
}

// answerInstanceAction answers the director's reply to an instance action
// and audits it. A lost answer is unknown, a refusal failed, and
// a task the director queued accepted.
func (h *Handler) answerInstanceAction(ctx context.Context, writer http.ResponseWriter, logger interfaces.Logger, actor audit.Actor, subject instanceActionProblem, task *bosh.Task, err error) {
	instanceID, deployment, action := subject.InstanceID, subject.Deployment, subject.Operation

	if errors.Is(err, bosh.ErrOutcomeUnknown) {
		logger.Error("The director's answer to the %s of deployment %q of instance %q was lost: %v", action, deployment, instanceID, err)
		h.auditInstanceAction(ctx, logger, actor, instanceID, deployment, action, "unknown", 0)

		subject.Code = apierr.CodeOutcomeUnknown
		subject.Error = fmt.Sprintf("The director's answer was lost or came too late, so the %s of instance %s may have started. Check the tasks of deployment %s before you try again.", action, instanceID, deployment)
		response.JSON(writer, http.StatusGatewayTimeout, subject)

		return
	}

	if err != nil {
		logger.Error("Failed to %s deployment %q of instance %q: %v", action, deployment, instanceID, err)
		h.auditInstanceAction(ctx, logger, actor, instanceID, deployment, action, "failed", 0)
		writeInstanceActionError(writer, deployment, err)

		return
	}

	h.auditInstanceAction(ctx, logger, actor, instanceID, deployment, action, "accepted", task.ID)

	response.JSON(writer, http.StatusAccepted, instanceActionResponse{
		InstanceID: instanceID,
		Deployment: deployment,
		Operation:  action,
		TaskID:     task.ID,
		State:      task.State,
	})
}

// instanceActionResponse is the 202 body of an accepted instance action,
// carrying the task the director queued.
type instanceActionResponse struct {
	InstanceID string `json:"instance_id"`
	Deployment string `json:"deployment"`
	Operation  string `json:"operation"`
	TaskID     int    `json:"task_id"`
	State      string `json:"state"`
}

// deploymentIsIdle asks the director whether it runs a task on the
// deployment, as the certificate preflight does. A queued, processing, or
// cancelling task holds the deployment's lock, so the director would fail
// a new task, and the action answers 409 deployment_locked with the task
// instead. A task list the director can't give refuses the action too,
// because nobody then knows whether the deployment is locked. The caller
// holds the guard, so no broker operation can start in between. It returns
// the running task, which is zero when there is none or the list failed.
func (h *Handler) deploymentIsIdle(ctx context.Context, writer http.ResponseWriter, logger interfaces.Logger, instanceID, deployment, action string) (int, bool) {
	running, err := h.director.FindRunningTasksForDeployment(ctx, deployment)
	if err != nil {
		logger.Error("Failed to list the running tasks of deployment %q before the %s of instance %q: %v", deployment, action, instanceID, err)
		apierr.Write(writer, apierr.New(http.StatusBadGateway, apierr.CodeInternal,
			fmt.Sprintf("The director did not list the tasks of deployment %s, so the %s of instance %s didn't start: %v", deployment, action, instanceID, err)), http.StatusBadGateway)

		return 0, false
	}

	if len(running) == 0 {
		return 0, true
	}

	taskID := running[0].ID
	logger.Warning("Refusing the %s of instance %q: the director is running task %d on deployment %q", action, instanceID, taskID, deployment)
	response.JSON(writer, http.StatusConflict, instanceActionProblem{
		Code:       apierr.CodeDeploymentLocked,
		Error:      fmt.Sprintf("The director is running task %d on deployment %s, so the %s of instance %s didn't start. Wait for that task to finish.", taskID, deployment, action, instanceID),
		InstanceID: instanceID,
		Deployment: deployment,
		Operation:  action,
		TaskID:     taskID,
	})

	return taskID, false
}

// instanceActionProblem is the body of an instance action that did not
// start, or whose start can't be confirmed. It carries the error code and
// message every broker error has, plus what the UI needs to point the
// operator at the right deployment or task.
type instanceActionProblem struct {
	Success    bool   `json:"success"`
	Code       string `json:"code"`
	Error      string `json:"error"`
	InstanceID string `json:"instance_id"`
	Deployment string `json:"deployment"`
	Operation  string `json:"operation"`
	TaskID     int    `json:"task_id,omitempty"`
}

// ownedDeployment looks the instance up in the broker's index and returns
// the deployment its entry records. An instance the index doesn't hold
// answers 404, and an index that can't be read answers 503, because then
// nobody knows whether the broker owns the instance. Both log the ID from
// the path quoted, as every log line of the handler does, because Go's
// regexp lets the path segment hold a decoded newline.
func (h *Handler) ownedDeployment(ctx context.Context, writer http.ResponseWriter, logger interfaces.Logger, instanceID string) (string, bool) {
	if h.vault == nil {
		apierr.Write(writer, apierr.New(http.StatusServiceUnavailable, apierr.CodeVaultUnavailable, errVaultNotConfigured.Error()), http.StatusServiceUnavailable)

		return "", false
	}

	inst, found, err := h.vault.FindInstance(ctx, instanceID)
	if err != nil {
		logger.Error("Unable to read the service index for instance %q: %v", instanceID, err)
		apierr.Write(writer, apierr.New(http.StatusServiceUnavailable, apierr.CodeVaultUnavailable,
			"the service index could not be read, so the broker can't tell whether it owns instance "+instanceID), http.StatusServiceUnavailable)

		return "", false
	}

	if !found || inst == nil {
		logger.Warning("Refusing an action on %q, which is not a service instance this broker owns", instanceID)
		apierr.Write(writer, apierr.New(http.StatusNotFound, apierr.CodeInstanceNotFound,
			"no service instance "+instanceID+" is provisioned by this broker"), http.StatusNotFound)

		return "", false
	}

	return inst.DeploymentNameFor(instanceID), true
}

// reserveInstance holds the instance and its deployment in the guard for the
// action, as reserveDeployment does for a deployment alone. A stored task
// that waits for either one owns it, so the action is refused then too.
func (h *Handler) reserveInstance(writer http.ResponseWriter, logger interfaces.Logger, instanceID, deployment, action string) (func(), bool) {
	if !h.tasksLoaded() {
		message := fmt.Sprintf("the stored upgrade and certificate tasks are still loading, so the %s of instance %s can't start yet", action, instanceID)
		logger.Warning("Refusing the %s of instance %q: the stored upgrade and certificate tasks are still loading", action, instanceID)
		apierr.Write(writer, apierr.New(http.StatusServiceUnavailable, apierr.CodeTasksLoading, message), http.StatusServiceUnavailable)

		return nil, false
	}

	if h.guard == nil {
		return func() {}, true
	}

	release, err := h.acquireInstance(instanceID, deployment, action)
	if err == nil {
		return release, true
	}

	var busy *mutation.BusyError
	if errors.As(err, &busy) {
		message := fmt.Sprintf("instance %s is busy with %s", instanceID, busy.Holder.Description)
		logger.Warning("Refusing the %s of instance %q: it is busy with %q", action, instanceID, busy.Holder.Description)
		apierr.Write(writer, apierr.New(http.StatusConflict, apierr.CodeInstanceBusy, message), http.StatusConflict)

		return nil, false
	}

	logger.Error("Failed to reserve instance %q for the %s: %v", instanceID, action, err)
	apierr.Write(writer, err, http.StatusInternalServerError)

	return nil, false
}

func (h *Handler) acquireInstance(instanceID, deployment, action string) (func(), error) {
	if h.waiters != nil {
		if holder, waiting := h.waiters.WaitingFor(instanceID, deployment); waiting {
			return nil, &mutation.BusyError{Holder: holder}
		}
	}

	release, err := h.guard.Acquire(instanceID, deployment, mutation.Holder{
		Kind:        mutation.KindDeploymentAction,
		Description: action + " of " + instanceID + " from the UI",
	})
	if err != nil {
		return nil, fmt.Errorf("reserving instance %s: %w", instanceID, err)
	}

	return release, nil
}

func (h *Handler) runInstanceAction(ctx context.Context, deployment, action string) (*bosh.Task, error) {
	var (
		task *bosh.Task
		err  error
	)

	switch action {
	case instanceActionRestart:
		task, err = h.director.RestartDeployment(ctx, deployment, bosh.RestartOpts{})
	case instanceActionRecreate:
		task, err = h.director.RecreateDeployment(ctx, deployment, bosh.RecreateOpts{})
	default:
		return nil, fmt.Errorf("%w: %s", errUnknownInstanceAction, action)
	}

	if err != nil {
		return nil, fmt.Errorf("%s of deployment %s: %w", action, deployment, err)
	}

	if task == nil {
		return nil, fmt.Errorf("%s of deployment %s: %w", action, deployment, errDirectorReturnedNoTask)
	}

	return task, nil
}

// writeInstanceActionError answers 404 when the director has no such
// deployment, and 502 for any other refusal or failure of the director.
func writeInstanceActionError(writer http.ResponseWriter, deployment string, err error) {
	if errors.Is(err, bosh.ErrDeploymentNotFound) {
		apierr.Write(writer, apierr.New(http.StatusNotFound, apierr.CodeNotFound,
			"the BOSH director has no deployment "+deployment), http.StatusNotFound)

		return
	}

	apierr.Write(writer, apierr.Wrap(http.StatusBadGateway, apierr.CodeInternal, err), http.StatusBadGateway)
}

// auditInstanceAction logs the audit line of an instance action and keeps
// its change record. The store writes the record in the background, so a
// slow or failing Vault never changes the action's answer, and the store
// logs a record it could not save.
func (h *Handler) auditInstanceAction(ctx context.Context, logger interfaces.Logger, actor audit.Actor, instanceID, deployment, action, outcome string, taskID int) {
	logInstanceActionAudit(logger, actor, instanceID, deployment, action, outcome, taskID)

	h.changes.Record(ctx, audit.ChangeRecord{
		Scope: audit.ScopeInstances, Target: instanceID, Instance: instanceID, Deployment: deployment,
		Action: action, Outcome: outcome, TaskID: taskID,
	})
}

// logInstanceActionAudit logs the audit line of an instance action. The
// instance, deployment, actor, and address are quoted, because the instance
// comes from the request path and the forwarded-for part is whatever the
// client sent, so neither can forge a second line.
func logInstanceActionAudit(logger interfaces.Logger, actor audit.Actor, instanceID, deployment, action, outcome string, taskID int) {
	logger.Info("audit=instance-action instance=%q deployment=%q operation=%s actor=%q from=%q outcome=%q task=%d",
		instanceID, deployment, action, actor.Username, actor.From(), outcome, taskID)
}
