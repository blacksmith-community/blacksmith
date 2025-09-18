package deployments

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"
)

// Constants for deployment test data.
const (
	// Test task IDs for various operations.
	testTaskIDCreate   = 999
	testTaskIDUpdate   = 1000
	testTaskIDRestart  = 1001
	testTaskIDRecreate = 1002
	testTaskIDStop     = 1003
	testTaskIDStart    = 1004
	testTaskIDDelete   = 1005

	// Path depth for deployment actions.
	minPathDepth = 2
)

// Error variables for err113 compliance.
var (
	errInvalidManifest = errors.New("invalid manifest")
)

// Handler handles deployment-related endpoints.
type Handler struct {
	logger interfaces.Logger
	config interfaces.Config
	vault  interfaces.Vault
}

// Dependencies contains all dependencies needed by the Deployments handler.
type Dependencies struct {
	Logger interfaces.Logger
	Config interfaces.Config
	Vault  interfaces.Vault
}

// Deployment represents a BOSH deployment.
type Deployment struct {
	Name        string                 `json:"name"`
	Manifest    map[string]interface{} `json:"manifest,omitempty"`
	Releases    []Release              `json:"releases,omitempty"`
	Stemcells   []Stemcell             `json:"stemcells,omitempty"`
	Teams       []string               `json:"teams,omitempty"`
	CloudConfig map[string]interface{} `json:"cloud_config,omitempty"`
}

// Release represents a BOSH release.
type Release struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Stemcell represents a BOSH stemcell.
type Stemcell struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	OS      string `json:"os"`
}

// NewHandler creates a new Deployments handler.
func NewHandler(deps Dependencies) *Handler {
	return &Handler{
		logger: deps.Logger,
		config: deps.Config,
		vault:  deps.Vault,
	}
}

// ServeHTTP handles deployment-related endpoints with pattern matching.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	if !h.isDeploymentEndpoint(req.URL.Path) {
		writer.WriteHeader(http.StatusNotFound)

		return
	}

	deploymentName, pathParts := h.parseDeploymentPath(req.URL.Path)
	if deploymentName == "" {
		writer.WriteHeader(http.StatusNotFound)

		return
	}

	if len(pathParts) == 1 {
		h.handleRootDeploymentOperations(writer, req, deploymentName)

		return
	}

	h.handleSubpathOperations(writer, req, deploymentName, pathParts)
}

// GetDeployment returns details about a specific deployment.
func (h *Handler) GetDeployment(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-get")
	logger.Debug("Getting deployment: %s", deploymentName)

	// TODO: Implement actual BOSH deployment fetching
	deployment := Deployment{
		Name: deploymentName,
		Releases: []Release{
			{Name: "blacksmith", Version: "1.0.0"},
		},
		Stemcells: []Stemcell{
			{Name: "bosh-warden-boshlite-ubuntu-jammy-go_agent", Version: "1.1", OS: "ubuntu-jammy"},
		},
		Teams: []string{"blacksmith"},
	}

	response.HandleJSON(writer, deployment, nil)
}

// DeleteDeployment deletes a deployment.
func (h *Handler) DeleteDeployment(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-delete")
	logger.Info("Deleting deployment: %s", deploymentName)

	// TODO: Implement actual BOSH deployment deletion
	result := map[string]interface{}{
		"deployment": deploymentName,
		"deleted":    true,
		"task_id":    testTaskIDCreate,
	}

	response.HandleJSON(writer, result, nil)
}

// GetManifest returns the manifest for a deployment.
func (h *Handler) GetManifest(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-manifest")
	logger.Debug("Getting manifest for deployment: %s", deploymentName)

	// TODO: Implement actual manifest fetching
	manifest := map[string]interface{}{
		"name": deploymentName,
		"releases": []map[string]string{
			{"name": "blacksmith", "version": "latest"},
		},
		"instance_groups": []map[string]interface{}{
			{
				"name":      "blacksmith",
				"instances": 1,
				"vm_type":   "default",
			},
		},
	}

	response.HandleJSON(writer, manifest, nil)
}

// UpdateManifest updates the manifest for a deployment.
func (h *Handler) UpdateManifest(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-manifest-update")
	logger.Info("Updating manifest for deployment: %s", deploymentName)

	var manifest map[string]interface{}

	err := json.NewDecoder(req.Body).Decode(&manifest)
	if err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		response.HandleJSON(writer, nil, fmt.Errorf("%w: %w", errInvalidManifest, err))

		return
	}

	// TODO: Implement actual manifest update
	result := map[string]interface{}{
		"deployment": deploymentName,
		"updated":    true,
		"task_id":    testTaskIDUpdate,
	}

	response.HandleJSON(writer, result, nil)
}

// GetVMs returns the VMs for a deployment.
func (h *Handler) GetVMs(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-vms")
	logger.Debug("Getting VMs for deployment: %s", deploymentName)

	// TODO: Implement actual VM fetching
	vms := []map[string]interface{}{
		{
			"instance":   "blacksmith/0",
			"state":      "running",
			"vm_cid":     "vm-1234",
			"vm_type":    "default",
			"ips":        []string{"10.0.0.1"},
			"deployment": deploymentName,
		},
	}

	response.HandleJSON(writer, map[string]interface{}{
		"deployment": deploymentName,
		"vms":        vms,
	}, nil)
}

// GetInstances returns the instances for a deployment.
func (h *Handler) GetInstances(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-instances")
	logger.Debug("Getting instances for deployment: %s", deploymentName)

	// TODO: Implement actual instance fetching
	instances := []map[string]interface{}{
		{
			"instance":   "blacksmith/0",
			"state":      "running",
			"vm_cid":     "vm-1234",
			"process":    []string{"blacksmith"},
			"deployment": deploymentName,
		},
	}

	response.HandleJSON(writer, map[string]interface{}{
		"deployment": deploymentName,
		"instances":  instances,
	}, nil)
}

// ListErrands lists available errands for a deployment.
func (h *Handler) ListErrands(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-errands-list")
	logger.Debug("Listing errands for deployment: %s", deploymentName)

	// TODO: Implement actual errand listing
	errands := []string{"smoke-tests", "cleanup"}

	response.HandleJSON(writer, map[string]interface{}{
		"deployment": deploymentName,
		"errands":    errands,
	}, nil)
}

// RunErrand runs a specific errand.
func (h *Handler) RunErrand(writer http.ResponseWriter, req *http.Request, deploymentName string, errandName string) {
	logger := h.logger.Named("deployment-errand-run")
	logger.Info("Running errand %s for deployment: %s", errandName, deploymentName)

	// TODO: Implement actual errand execution
	result := map[string]interface{}{
		"deployment": deploymentName,
		"errand":     errandName,
		"task_id":    testTaskIDRestart,
		"started":    true,
	}

	response.HandleJSON(writer, result, nil)
}

// RecreateDeployment recreates all VMs in a deployment.
func (h *Handler) RecreateDeployment(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-recreate")
	logger.Info("Recreating deployment: %s", deploymentName)

	// TODO: Implement actual deployment recreation
	result := map[string]interface{}{
		"deployment": deploymentName,
		"task_id":    testTaskIDRecreate,
		"operation":  "recreate",
	}

	response.HandleJSON(writer, result, nil)
}

// RestartDeployment restarts all VMs in a deployment.
func (h *Handler) RestartDeployment(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-restart")
	logger.Info("Restarting deployment: %s", deploymentName)

	// TODO: Implement actual deployment restart
	result := map[string]interface{}{
		"deployment": deploymentName,
		"task_id":    testTaskIDStop,
		"operation":  "restart",
	}

	response.HandleJSON(writer, result, nil)
}

// StopDeployment stops all VMs in a deployment.
func (h *Handler) StopDeployment(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-stop")
	logger.Info("Stopping deployment: %s", deploymentName)

	// TODO: Implement actual deployment stop
	result := map[string]interface{}{
		"deployment": deploymentName,
		"task_id":    testTaskIDStart,
		"operation":  "stop",
	}

	response.HandleJSON(writer, result, nil)
}

// StartDeployment starts all VMs in a deployment.
func (h *Handler) StartDeployment(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-start")
	logger.Info("Starting deployment: %s", deploymentName)

	// TODO: Implement actual deployment start
	result := map[string]interface{}{
		"deployment": deploymentName,
		"task_id":    testTaskIDDelete,
		"operation":  "start",
	}

	response.HandleJSON(writer, result, nil)
}

// isDeploymentEndpoint checks if the path is a deployment-specific endpoint.
func (h *Handler) isDeploymentEndpoint(path string) bool {
	return strings.HasPrefix(path, "/b/deployments/")
}

// parseDeploymentPath extracts the deployment name and path parts from the URL path.
func (h *Handler) parseDeploymentPath(path string) (string, []string) {
	pathParts := strings.Split(strings.TrimPrefix(path, "/b/deployments/"), "/")
	if len(pathParts) < 1 || pathParts[0] == "" {
		return "", nil
	}

	return pathParts[0], pathParts
}

// handleRootDeploymentOperations handles operations on the deployment itself.
func (h *Handler) handleRootDeploymentOperations(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	switch req.Method {
	case http.MethodGet:
		h.GetDeployment(writer, req, deploymentName)
	case http.MethodDelete:
		h.DeleteDeployment(writer, req, deploymentName)
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleSubpathOperations handles operations on deployment subresources.
func (h *Handler) handleSubpathOperations(writer http.ResponseWriter, req *http.Request, deploymentName string, pathParts []string) {
	operation := pathParts[1]

	switch operation {
	case "manifest":
		h.handleManifestOperations(writer, req, deploymentName)
	case "vms":
		h.handleVMsOperations(writer, req, deploymentName)
	case "instances":
		h.handleInstancesOperations(writer, req, deploymentName)
	case "errands":
		h.handleErrandsOperations(writer, req, deploymentName, pathParts)
	case "recreate":
		h.handleLifecycleOperation(writer, req, deploymentName, h.RecreateDeployment)
	case "restart":
		h.handleLifecycleOperation(writer, req, deploymentName, h.RestartDeployment)
	case "stop":
		h.handleLifecycleOperation(writer, req, deploymentName, h.StopDeployment)
	case "start":
		h.handleLifecycleOperation(writer, req, deploymentName, h.StartDeployment)
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

// handleManifestOperations handles manifest-related operations.
func (h *Handler) handleManifestOperations(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	switch req.Method {
	case http.MethodGet:
		h.GetManifest(writer, req, deploymentName)
	case http.MethodPost:
		h.UpdateManifest(writer, req, deploymentName)
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleVMsOperations handles VMs-related operations.
func (h *Handler) handleVMsOperations(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	if req.Method == http.MethodGet {
		h.GetVMs(writer, req, deploymentName)
	} else {
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleInstancesOperations handles instances-related operations.
func (h *Handler) handleInstancesOperations(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	if req.Method == http.MethodGet {
		h.GetInstances(writer, req, deploymentName)
	} else {
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleErrandsOperations handles errands-related operations.
func (h *Handler) handleErrandsOperations(writer http.ResponseWriter, req *http.Request, deploymentName string, pathParts []string) {
	if len(pathParts) > minPathDepth {
		h.handleSpecificErrand(writer, req, deploymentName, pathParts[2])
	} else {
		h.handleErrandsList(writer, req, deploymentName)
	}
}

// handleSpecificErrand handles operations on a specific errand.
func (h *Handler) handleSpecificErrand(writer http.ResponseWriter, req *http.Request, deploymentName, errandName string) {
	if req.Method == http.MethodPost {
		h.RunErrand(writer, req, deploymentName, errandName)
	} else {
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleErrandsList handles operations on the errands list.
func (h *Handler) handleErrandsList(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	if req.Method == http.MethodGet {
		h.ListErrands(writer, req, deploymentName)
	} else {
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleLifecycleOperation handles lifecycle operations (recreate, restart, stop, start).
func (h *Handler) handleLifecycleOperation(writer http.ResponseWriter, req *http.Request, deploymentName string, operation func(http.ResponseWriter, *http.Request, string)) {
	if req.Method == http.MethodPost {
		operation(writer, req, deploymentName)
	} else {
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}
