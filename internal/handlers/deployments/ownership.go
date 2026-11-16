package deployments

import (
	"context"
	"fmt"
	"net/http"

	"blacksmith/internal/apierr"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/ownership"
	"blacksmith/internal/ownname"
)

// The operation segments of the deployment routes.
const (
	routeManifest = "manifest"
	routeRecreate = "recreate"
	routeRestart  = "restart"
	routeStop     = "stop"
	routeStart    = "start"
	routeErrands  = "errands"
)

// ownDeployment returns the name of the broker's own deployment, from the
// instance file BOSH writes or else the configured environment plus
// -blacksmith, and false when neither names it.
func (h *Handler) ownDeployment() (string, bool) {
	environment := ""
	if h.config != nil {
		environment = h.config.GetEnvironment()
	}

	return ownname.Resolve(h.ownFile, environment)
}

// permitDeployment reports false, after it answers, when the request is
// not allowed on the deployment. A change needs a deployment of one of the
// broker's service instances. A read needs the same, except that the
// broker's own deployment, matched exactly, can be read and never
// changed, because the UI shows it. A change to the own deployment is
// refused before the index is read, whatever the index records.
func (h *Handler) permitDeployment(ctx context.Context, writer http.ResponseWriter, logger interfaces.Logger, method string, pathParts []string, deploymentName string) bool {
	changes := changesDeployment(method, pathParts)

	if own, ok := h.ownDeployment(); ok && deploymentName == own {
		if !changes {
			return true
		}

		logger.Warning("Refusing to change deployment %q, which is the broker's own deployment", deploymentName)
		apierr.Write(writer, apierr.New(http.StatusForbidden, apierr.CodeDeploymentNotManaged,
			"deployment "+deploymentName+" is the broker's own deployment, which can be shown and never changed"), http.StatusForbidden)

		return false
	}

	return h.refuseUnmanaged(ctx, writer, logger, deploymentName, changes)
}

// changesDeployment reports whether the request changes the deployment,
// by restarting, recreating, stopping, starting, deleting, or updating
// it, or by running an errand on it. A read, and any request no route
// answers, changes nothing.
func changesDeployment(method string, pathParts []string) bool {
	if len(pathParts) == 1 {
		return method == http.MethodDelete
	}

	switch pathParts[1] {
	case routeManifest:
		return method == http.MethodPost
	case routeRecreate, routeRestart, routeStop, routeStart:
		return method == http.MethodPost
	case routeErrands:
		return len(pathParts) > minPathDepth && method == http.MethodPost
	default:
		return false
	}
}

// refuseUnmanaged reports false, after it answers, when the broker owns no
// service instance whose deployment is deploymentName. The changes flag
// only words the refusal. The broker's index
// is the only authority. A name that merely looks like a service
// instance's deployment is another deployment, and it belongs to someone
// else. A vault that is missing or an index that can't be read answers 503
// with the code vault_unavailable, because then nobody knows whether the
// broker owns the deployment, and the action must not run.
func (h *Handler) refuseUnmanaged(ctx context.Context, writer http.ResponseWriter, logger interfaces.Logger, deploymentName string, changes bool) bool {
	if h.vault == nil {
		apierr.Write(writer, apierr.New(http.StatusServiceUnavailable, apierr.CodeVaultUnavailable, errVaultNotConfigured.Error()), http.StatusServiceUnavailable)

		return false
	}

	managed, err := h.isManagedDeployment(ctx, deploymentName)
	if err != nil {
		logger.Error("Unable to read the service index to check deployment %q: %v", deploymentName, err)
		apierr.Write(writer, apierr.New(http.StatusServiceUnavailable, apierr.CodeVaultUnavailable,
			"the service index could not be read, so the broker can't tell whether it manages deployment "+deploymentName), http.StatusServiceUnavailable)

		return false
	}

	if !managed {
		verb := "show"
		if changes {
			verb = "change"
		}

		logger.Warning("Refusing to %s deployment %q, which is not the deployment of a service instance this broker owns", verb, deploymentName)
		apierr.Write(writer, apierr.New(http.StatusForbidden, apierr.CodeDeploymentNotManaged,
			"deployment "+deploymentName+" is not a deployment of one of this broker's service instances, so the broker will not "+verb+" it"), http.StatusForbidden)

		return false
	}

	return true
}

// isManagedDeployment reports whether the broker's index records
// deploymentName as the deployment of a live service instance.
func (h *Handler) isManagedDeployment(ctx context.Context, deploymentName string) (bool, error) {
	managed, err := ownership.ManagedDeployment(ctx, h.vault, deploymentName)
	if err != nil {
		return false, fmt.Errorf("checking deployment %q: %w", deploymentName, err)
	}

	return managed, nil
}
