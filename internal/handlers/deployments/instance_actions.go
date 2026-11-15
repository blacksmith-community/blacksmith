package deployments

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"

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
// a certificate rotation, or a broker operation on the same instance. It
// answers 202 with the BOSH task the director created.
//
// The broker's basic-auth wrapper puts the authenticated actor in the
// request context, as it does for every /b/ request. Each action that
// reaches the director logs one audit line with that actor, where the
// request came from, the outcome, and the task.
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

	deployment, owned := h.ownedDeployment(req.Context(), writer, logger, instanceID)
	if !owned {
		return
	}

	if h.director == nil {
		apierr.Write(writer, apierr.New(http.StatusServiceUnavailable, apierr.CodeInternal, errDirectorNotConfigured.Error()), http.StatusServiceUnavailable)

		return
	}

	release, reserved := h.reserveInstance(writer, logger, instanceID, deployment, action)
	if !reserved {
		return
	}
	defer release()

	actor, _ := audit.ActorFrom(req.Context())

	task, err := h.runInstanceAction(req.Context(), deployment, action)
	if err != nil {
		logger.Error("Failed to %s deployment %s of instance %s: %v", action, deployment, instanceID, err)
		logInstanceActionAudit(logger, actor, instanceID, deployment, action, "failed", 0)
		writeInstanceActionError(writer, deployment, err)

		return
	}

	logInstanceActionAudit(logger, actor, instanceID, deployment, action, "accepted", task.ID)

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

// ownedDeployment looks the instance up in the broker's index and returns
// the deployment its entry records. An instance the index doesn't hold
// answers 404, and an index that can't be read answers 503, because then
// nobody knows whether the broker owns the instance.
func (h *Handler) ownedDeployment(ctx context.Context, writer http.ResponseWriter, logger interfaces.Logger, instanceID string) (string, bool) {
	if h.vault == nil {
		apierr.Write(writer, apierr.New(http.StatusServiceUnavailable, apierr.CodeVaultUnavailable, errVaultNotConfigured.Error()), http.StatusServiceUnavailable)

		return "", false
	}

	inst, found, err := h.vault.FindInstance(ctx, instanceID)
	if err != nil {
		logger.Error("Unable to read the service index for instance %s: %v", instanceID, err)
		apierr.Write(writer, apierr.New(http.StatusServiceUnavailable, apierr.CodeVaultUnavailable,
			"the service index could not be read, so the broker can't tell whether it owns instance "+instanceID), http.StatusServiceUnavailable)

		return "", false
	}

	if !found || inst == nil {
		logger.Warning("Refusing an action on %s, which is not a service instance this broker owns", instanceID)
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
		logger.Warning("Refusing the %s of instance %s: %s", action, instanceID, message)
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
		logger.Warning("Refusing the %s of instance %s: %s", action, instanceID, message)
		apierr.Write(writer, apierr.New(http.StatusConflict, apierr.CodeInstanceBusy, message), http.StatusConflict)

		return nil, false
	}

	logger.Error("Failed to reserve instance %s for the %s: %v", instanceID, action, err)
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

// logInstanceActionAudit logs the audit line of an instance action. The
// actor and its address are quoted, because the forwarded-for part is
// whatever the client sent.
func logInstanceActionAudit(logger interfaces.Logger, actor audit.Actor, instanceID, deployment, action, outcome string, taskID int) {
	logger.Info("audit=instance-action instance=%s deployment=%s operation=%s actor=%q from=%q outcome=%q task=%d",
		instanceID, deployment, action, actor.Username, actor.From(), outcome, taskID)
}
