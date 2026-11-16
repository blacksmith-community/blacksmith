package deployments

import (
	"context"
	"fmt"
	"net/http"

	"blacksmith/internal/apierr"
	"blacksmith/internal/interfaces"
	vaultPkg "blacksmith/pkg/vault"
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

// changesDeployment reports whether the request changes the deployment,
// by restarting, recreating, stopping, starting, deleting, or updating
// it, or by running an errand on it. A read, and any request no route
// answers, changes nothing, so the ownership check leaves it to the
// route.
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
// service instance whose deployment is deploymentName. The broker's index
// is the only authority. A name that merely looks like a service
// instance's deployment is another deployment, and it belongs to someone
// else. A vault that is missing or an index that can't be read answers 503
// with the code vault_unavailable, because then nobody knows whether the
// broker owns the deployment, and the action must not run.
func (h *Handler) refuseUnmanaged(ctx context.Context, writer http.ResponseWriter, logger interfaces.Logger, deploymentName string) bool {
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
		logger.Warning("Refusing to change deployment %q, which is not the deployment of a service instance this broker owns", deploymentName)
		apierr.Write(writer, apierr.New(http.StatusForbidden, apierr.CodeDeploymentNotManaged,
			"deployment "+deploymentName+" is not a deployment of one of this broker's service instances, so the broker will not change it"), http.StatusForbidden)

		return false
	}

	return true
}

// isManagedDeployment reports whether an entry of the broker's index
// records deploymentName as its deployment. An entry whose data is nil is
// an instance being deleted, and it owns nothing.
func (h *Handler) isManagedDeployment(ctx context.Context, deploymentName string) (bool, error) {
	var index map[string]*vaultPkg.Instance

	exists, err := h.vault.Get(ctx, "db", &index)
	if err != nil {
		return false, fmt.Errorf("reading the service index: %w", err)
	}

	if !exists {
		return false, nil
	}

	for instanceID, instance := range index {
		if instanceID == "" || instance == nil {
			continue
		}

		if instance.DeploymentNameFor(instanceID) == deploymentName {
			return true, nil
		}
	}

	return false, nil
}
