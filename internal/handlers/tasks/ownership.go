package tasks

import (
	"context"
	"fmt"
	"net/http"

	"blacksmith/internal/apierr"
	"blacksmith/internal/bosh"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/ownership"
	"blacksmith/internal/ownname"
	"blacksmith/pkg/http/response"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// writeDirectorReadFailure answers a failed read of a task from the
// director. It answers 404 only when the director says the task does not
// exist, because an operator reads a 404 as a task that is gone. Any other
// failure, such as an outage, a timeout, or a refusal, answers 502 with
// internal_error, as the broker answers a director failure elsewhere.
func writeDirectorReadFailure(writer http.ResponseWriter, err error, taskID int, what string) {
	if boshapi.IsNotFound(err) {
		response.WriteError(writer, http.StatusNotFound, fmt.Sprintf("task %d %s not found", taskID, what))

		return
	}

	apierr.Write(writer, apierr.New(http.StatusBadGateway, apierr.CodeInternal,
		fmt.Sprintf("the director could not give %s of task %d, so nothing is known about it: %v", what, taskID, err)), http.StatusBadGateway)
}

// permitTask fetches the task from the director and reports it, and true,
// when the request may go on. Otherwise it has answered and returns false.
//
// A task is the broker's to show or cancel when its deployment is the
// deployment of a live service instance in the broker's index. A task of a
// deployment the index holds only as a tombstone, which the broker managed
// and then deleted, can still be shown, and never cancelled. The broker's
// own deployment can be read and never cancelled, as its routes do. A task
// with no deployment belongs to the director, so it can be read and never
// cancelled. A task that is not the broker's is refused with 403 and the code
// deployment_not_managed. An index that can't be read, or no Vault, answers
// 503 with vault_unavailable, because then nobody knows whether the broker
// owns the deployment. The caller's team parameter is never read.
func (h *Handler) permitTask(ctx context.Context, writer http.ResponseWriter, logger interfaces.Logger, taskID int, cancel bool) (*bosh.Task, bool) {
	task, err := h.director.GetTask(ctx, taskID)
	if err != nil {
		logger.Error("Failed to fetch task %d: %v", taskID, err)
		writeDirectorReadFailure(writer, err, taskID, "task")

		return nil, false
	}

	deployment := task.Deployment

	if deployment == "" {
		return h.readOnlyTask(writer, logger, task, cancel, "task %d belongs to the director and not to a deployment, so the broker will not cancel it")
	}

	if own, ok := h.ownDeployment(); ok && deployment == own {
		return h.readOnlyTask(writer, logger, task, cancel, "task %d belongs to the broker's own deployment, which can be shown and never changed")
	}

	if h.vault == nil {
		apierr.Write(writer, apierr.New(http.StatusServiceUnavailable, apierr.CodeVaultUnavailable,
			"the broker has no Vault, so it can't tell whether it manages deployment "+deployment), http.StatusServiceUnavailable)

		return nil, false
	}

	managed, err := ownership.ManagedDeployment(ctx, h.vault, deployment)
	if err != nil {
		logger.Error("Unable to read the service index to check task %d: %v", taskID, err)
		apierr.Write(writer, apierr.New(http.StatusServiceUnavailable, apierr.CodeVaultUnavailable,
			"the service index could not be read, so the broker can't tell whether it manages deployment "+deployment), http.StatusServiceUnavailable)

		return nil, false
	}

	if !managed && !cancel {
		managed, err = h.retired(ctx, writer, logger, taskID, deployment)
		if err != nil {
			return nil, false
		}
	}

	if !managed {
		verb := "show"
		if cancel {
			verb = "cancel"
		}

		logger.Warning("Refusing to %s task %d, whose deployment %q is not the deployment of a service instance this broker owns", verb, taskID, deployment)
		apierr.Write(writer, apierr.New(http.StatusForbidden, apierr.CodeDeploymentNotManaged,
			fmt.Sprintf("task %d belongs to deployment %s, which is not a deployment of one of this broker's service instances, so the broker will not %s it", taskID, deployment, verb)),
			http.StatusForbidden)

		return nil, false
	}

	return task, true
}

// retired reports whether the index holds a tombstone for the deployment,
// so a read of its task is allowed. It has answered 503 when the index can't
// be read, and then returns an error.
func (h *Handler) retired(ctx context.Context, writer http.ResponseWriter, logger interfaces.Logger, taskID int, deployment string) (bool, error) {
	retired, err := ownership.RetiredDeployment(ctx, h.vault, deployment)
	if err != nil {
		logger.Error("Unable to read the service index to check task %d: %v", taskID, err)
		apierr.Write(writer, apierr.New(http.StatusServiceUnavailable, apierr.CodeVaultUnavailable,
			"the service index could not be read, so the broker can't tell whether it managed deployment "+deployment), http.StatusServiceUnavailable)

		return false, fmt.Errorf("reading the service index: %w", err)
	}

	return retired, nil
}

// readOnlyTask lets a read of a task through and refuses a cancel.
func (h *Handler) readOnlyTask(writer http.ResponseWriter, logger interfaces.Logger, task *bosh.Task, cancel bool, refusal string) (*bosh.Task, bool) {
	if !cancel {
		return task, true
	}

	logger.Warning("Refusing to cancel task %d", task.ID)
	apierr.Write(writer, apierr.New(http.StatusForbidden, apierr.CodeDeploymentNotManaged, fmt.Sprintf(refusal, task.ID)), http.StatusForbidden)

	return nil, false
}

// ownDeployment returns the name of the broker's own deployment, and false
// when nothing names it.
func (h *Handler) ownDeployment() (string, bool) {
	environment := ""
	if h.config != nil {
		environment = h.config.GetEnvironment()
	}

	return ownname.Resolve(h.ownFile, environment)
}
