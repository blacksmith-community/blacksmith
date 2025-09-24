package deployments

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"blacksmith/internal/bosh"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"
	"gopkg.in/yaml.v2"
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
	errInvalidManifest    = errors.New("invalid manifest")
	errDeploymentNotFound = errors.New("deployment not found")
	errTaskCreationFailed = errors.New("failed to create task")
	errErrandNotFound     = errors.New("errand not found")
	errInvalidOptions     = errors.New("invalid operation options")
)

// Handler handles deployment-related endpoints.
type Handler struct {
	logger   interfaces.Logger
	config   interfaces.Config
	vault    interfaces.Vault
	director interfaces.Director
}

// Dependencies contains all dependencies needed by the Deployments handler.
type Dependencies struct {
	Logger   interfaces.Logger
	Config   interfaces.Config
	Vault    interfaces.Vault
	Director interfaces.Director
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
		logger:   deps.Logger,
		config:   deps.Config,
		vault:    deps.Vault,
		director: deps.Director,
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

	// Get basic deployment info from the deployments list
	deployments, err := h.director.GetDeployments()
	if err != nil {
		logger.Error("Failed to get deployments list: %v", err)
		writer.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(writer, nil, err)
		return
	}

	// Find our specific deployment
	var deployment *bosh.Deployment
	for i, dep := range deployments {
		if dep.Name == deploymentName {
			deployment = &deployments[i]
			break
		}
	}

	if deployment == nil {
		logger.Error("Deployment not found: %s", deploymentName)
		writer.WriteHeader(http.StatusNotFound)
		response.HandleJSON(writer, nil, fmt.Errorf("deployment not found: %s", deploymentName))
		return
	}

	// Convert to response format
	result := Deployment{
		Name:      deployment.Name,
		Releases:  convertReleases(deployment.Releases),
		Stemcells: convertStemcells(deployment.Stemcells),
		Teams:     deployment.Teams,
	}

	response.HandleJSON(writer, result, nil)
}

// DeleteDeployment deletes a deployment.
func (h *Handler) DeleteDeployment(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-delete")
	logger.Info("Deleting deployment: %s", deploymentName)

	task, err := h.director.DeleteDeployment(deploymentName)
	if err != nil {
		logger.Error("Failed to delete deployment %s: %v", deploymentName, err)
		writer.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(writer, nil, err)
		return
	}

	result := map[string]interface{}{
		"deployment": deploymentName,
		"deleted":    true,
		"task_id":    task.ID,
		"state":      task.State,
	}

	response.HandleJSON(writer, result, nil)
}

// GetManifest returns the manifest for a deployment.
func (h *Handler) GetManifest(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-manifest")
	logger.Debug("Getting manifest for deployment: %s", deploymentName)

	response.HandleJSON(writer, h.buildManifestPayload(deploymentName), nil)
}

// GetManifestDetails returns a structured manifest payload suitable for UI consumption.
func (h *Handler) GetManifestDetails(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-manifest-details")
	logger.Debug("Getting manifest details for deployment: %s", deploymentName)

	response.HandleJSON(writer, h.buildManifestPayload(deploymentName), nil)
}

func (h *Handler) buildManifestPayload(deploymentName string) map[string]interface{} {
	manifestText := fmt.Sprintf("name: %s\nreleases:\n- name: blacksmith\n  version: latest\ninstance_groups:\n- name: blacksmith\n  instances: 1\n  vm_type: default\n", deploymentName)

	parsed := map[string]interface{}{
		"name":          deploymentName,
		"director_uuid": "placeholder-director-uuid",
		"releases": []map[string]string{
			{"name": "blacksmith", "version": "latest"},
		},
		"stemcells": []map[string]string{
			{"name": "bosh-warden-boshlite-ubuntu-jammy-go_agent", "version": "1.1", "os": "ubuntu-jammy"},
		},
		"instance_groups": []map[string]interface{}{
			{
				"name":      "blacksmith",
				"instances": 1,
				"vm_type":   "default",
				"azs":       []string{"z1"},
			},
		},
		"features": map[string]bool{
			"use_dns_addresses": true,
		},
		"update": map[string]interface{}{
			"canaries":          1,
			"max_in_flight":     1,
			"canary_watch_time": "1000-30000",
			"update_watch_time": "1000-30000",
		},
		"variables": []map[string]string{
			{"name": "blacksmith_admin_password", "type": "password"},
		},
	}

	return map[string]interface{}{
		"text":   manifestText,
		"parsed": parsed,
	}
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

	// Convert manifest to YAML string
	manifestBytes, err := yaml.Marshal(manifest)
	if err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		response.HandleJSON(writer, nil, fmt.Errorf("failed to marshal manifest: %w", err))
		return
	}

	task, err := h.director.UpdateDeployment(deploymentName, string(manifestBytes))
	if err != nil {
		logger.Error("Failed to update deployment %s: %v", deploymentName, err)
		writer.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(writer, nil, err)
		return
	}

	result := map[string]interface{}{
		"deployment": deploymentName,
		"updated":    true,
		"task_id":    task.ID,
		"state":      task.State,
	}

	response.HandleJSON(writer, result, nil)
}

// GetVMs returns the VMs for a deployment.
func (h *Handler) GetVMs(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-vms")
	logger.Debug("Getting VMs for deployment: %s", deploymentName)

	vms, err := h.director.GetDeploymentVMs(deploymentName)
	if err != nil {
		logger.Error("Failed to get VMs for deployment %s: %v", deploymentName, err)
		writer.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(writer, nil, err)
		return
	}

	// Convert VM structs to response format
	vmList := make([]map[string]interface{}, len(vms))
	for i, vm := range vms {
		vmList[i] = map[string]interface{}{
			"instance":   fmt.Sprintf("%s/%d", vm.Job, vm.Index),
			"state":      vm.State,
			"vm_cid":     vm.CID,
			"vm_type":    vm.VMType,
			"ips":        vm.IPs,
			"deployment": deploymentName,
			"az":         vm.AZ,
			"active":     vm.Active,
		}
	}

	response.HandleJSON(writer, map[string]interface{}{
		"deployment": deploymentName,
		"vms":        vmList,
	}, nil)
}

// GetInstances returns the instances for a deployment.
func (h *Handler) GetInstances(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-instances")
	logger.Debug("Getting instances for deployment: %s", deploymentName)

	instances, err := h.director.GetInstances(deploymentName)
	if err != nil {
		logger.Error("Failed to get instances for deployment %s: %v", deploymentName, err)
		writer.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(writer, nil, err)
		return
	}

	// Convert to response format
	instanceList := make([]map[string]interface{}, len(instances))
	for i, instance := range instances {
		instanceList[i] = map[string]interface{}{
			"instance":      fmt.Sprintf("%s/%s", instance.Group, instance.Index),
			"state":         instance.State,
			"process_state": instance.ProcessState,
			"ips":           instance.IPs,
			"deployment":    deploymentName,
		}
	}

	response.HandleJSON(writer, map[string]interface{}{
		"deployment": deploymentName,
		"instances":  instanceList,
	}, nil)
}

// ListErrands lists available errands for a deployment.
func (h *Handler) ListErrands(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-errands-list")
	logger.Debug("Listing errands for deployment: %s", deploymentName)

	errands, err := h.director.ListErrands(deploymentName)
	if err != nil {
		logger.Error("Failed to list errands for deployment %s: %v", deploymentName, err)
		writer.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(writer, nil, err)
		return
	}

	// Extract errand names
	errandNames := make([]string, len(errands))
	for i, errand := range errands {
		errandNames[i] = errand.Name
	}

	response.HandleJSON(writer, map[string]interface{}{
		"deployment": deploymentName,
		"errands":    errandNames,
	}, nil)
}

// RunErrand runs a specific errand.
func (h *Handler) RunErrand(writer http.ResponseWriter, req *http.Request, deploymentName string, errandName string) {
	logger := h.logger.Named("deployment-errand-run")
	logger.Info("Running errand %s for deployment: %s", errandName, deploymentName)

	opts := parseErrandOpts(req)
	result, err := h.director.RunErrand(deploymentName, errandName, opts)
	if err != nil {
		logger.Error("Failed to run errand %s for deployment %s: %v", errandName, deploymentName, err)
		writer.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(writer, nil, err)
		return
	}

	response.HandleJSON(writer, map[string]interface{}{
		"deployment":   deploymentName,
		"errand":       errandName,
		"exit_code":    result.ExitCode,
		"stdout":       result.Stdout,
		"stderr":       result.Stderr,
		"instance":     result.InstanceGroup,
		"instance_id":  result.InstanceID,
	}, nil)
}

// RecreateDeployment recreates all VMs in a deployment.
func (h *Handler) RecreateDeployment(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-recreate")
	logger.Info("Recreating deployment: %s", deploymentName)

	opts := parseRecreateOpts(req)
	task, err := h.director.RecreateDeployment(deploymentName, opts)
	if err != nil {
		logger.Error("Failed to recreate deployment %s: %v", deploymentName, err)
		writer.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(writer, nil, err)
		return
	}

	result := map[string]interface{}{
		"deployment": deploymentName,
		"task_id":    task.ID,
		"operation":  "recreate",
		"state":      task.State,
	}

	response.HandleJSON(writer, result, nil)
}

// RestartDeployment restarts all VMs in a deployment.
func (h *Handler) RestartDeployment(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-restart")
	logger.Info("Restarting deployment: %s", deploymentName)

	opts := parseRestartOpts(req)
	task, err := h.director.RestartDeployment(deploymentName, opts)
	if err != nil {
		logger.Error("Failed to restart deployment %s: %v", deploymentName, err)
		writer.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(writer, nil, err)
		return
	}

	result := map[string]interface{}{
		"deployment": deploymentName,
		"task_id":    task.ID,
		"operation":  "restart",
		"state":      task.State,
	}

	response.HandleJSON(writer, result, nil)
}

// StopDeployment stops all VMs in a deployment.
func (h *Handler) StopDeployment(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-stop")
	logger.Info("Stopping deployment: %s", deploymentName)

	opts := parseStopOpts(req)
	task, err := h.director.StopDeployment(deploymentName, opts)
	if err != nil {
		logger.Error("Failed to stop deployment %s: %v", deploymentName, err)
		writer.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(writer, nil, err)
		return
	}

	result := map[string]interface{}{
		"deployment": deploymentName,
		"task_id":    task.ID,
		"operation":  "stop",
		"state":      task.State,
	}

	response.HandleJSON(writer, result, nil)
}

// StartDeployment starts all VMs in a deployment.
func (h *Handler) StartDeployment(writer http.ResponseWriter, req *http.Request, deploymentName string) {
	logger := h.logger.Named("deployment-start")
	logger.Info("Starting deployment: %s", deploymentName)

	opts := parseStartOpts(req)
	task, err := h.director.StartDeployment(deploymentName, opts)
	if err != nil {
		logger.Error("Failed to start deployment %s: %v", deploymentName, err)
		writer.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(writer, nil, err)
		return
	}

	result := map[string]interface{}{
		"deployment": deploymentName,
		"task_id":    task.ID,
		"operation":  "start",
		"state":      task.State,
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
	case "manifest-details":
		if req.Method == http.MethodGet {
			h.GetManifestDetails(writer, req, deploymentName)
		} else {
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
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

// Helper conversion functions
func convertReleases(releases []string) []Release {
	result := make([]Release, len(releases))
	for i, release := range releases {
		// Parse release string format: "name/version"
		parts := strings.SplitN(release, "/", 2)
		if len(parts) == 2 {
			result[i] = Release{
				Name:    parts[0],
				Version: parts[1],
			}
		} else {
			result[i] = Release{
				Name:    release,
				Version: "unknown",
			}
		}
	}
	return result
}

func convertStemcells(stemcells []string) []Stemcell {
	result := make([]Stemcell, len(stemcells))
	for i, stemcell := range stemcells {
		// Parse stemcell string format: "name/version"
		parts := strings.SplitN(stemcell, "/", 2)
		if len(parts) == 2 {
			result[i] = Stemcell{
				Name:    parts[0],
				Version: parts[1],
				OS:      extractOSFromStemcell(parts[0]),
			}
		} else {
			result[i] = Stemcell{
				Name:    stemcell,
				Version: "unknown",
				OS:      extractOSFromStemcell(stemcell),
			}
		}
	}
	return result
}

func extractOSFromStemcell(name string) string {
	// Extract OS from stemcell name (e.g., "bosh-warden-boshlite-ubuntu-jammy-go_agent" -> "ubuntu-jammy")
	if strings.Contains(name, "ubuntu") {
		parts := strings.Split(name, "-")
		for i, part := range parts {
			if part == "ubuntu" && i+1 < len(parts) {
				return fmt.Sprintf("ubuntu-%s", parts[i+1])
			}
		}
		return "ubuntu"
	}
	return "linux"
}

// Request parsing helper functions
func parseRestartOpts(req *http.Request) bosh.RestartOpts {
	opts := bosh.RestartOpts{
		Converge: true, // Default to converged operations
	}

	query := req.URL.Query()
	if v := query.Get("skip_drain"); v != "" {
		opts.SkipDrain = v == "true"
	}
	if v := query.Get("force"); v != "" {
		opts.Force = v == "true"
	}
	if v := query.Get("canaries"); v != "" {
		opts.Canaries = v
	}
	if v := query.Get("max_in_flight"); v != "" {
		opts.MaxInFlight = v
	}

	return opts
}

func parseStopOpts(req *http.Request) bosh.StopOpts {
	opts := bosh.StopOpts{
		Converge: true,
	}

	query := req.URL.Query()
	if v := query.Get("force"); v != "" {
		opts.Force = v == "true"
	}
	if v := query.Get("skip_drain"); v != "" {
		opts.SkipDrain = v == "true"
	}
	if v := query.Get("hard"); v != "" {
		opts.Hard = v == "true"
	}
	if v := query.Get("canaries"); v != "" {
		opts.Canaries = v
	}
	if v := query.Get("max_in_flight"); v != "" {
		opts.MaxInFlight = v
	}

	return opts
}

func parseStartOpts(req *http.Request) bosh.StartOpts {
	opts := bosh.StartOpts{
		Converge: true,
	}

	query := req.URL.Query()
	if v := query.Get("canaries"); v != "" {
		opts.Canaries = v
	}
	if v := query.Get("max_in_flight"); v != "" {
		opts.MaxInFlight = v
	}

	return opts
}

func parseRecreateOpts(req *http.Request) bosh.RecreateOpts {
	opts := bosh.RecreateOpts{
		Converge: true,
	}

	query := req.URL.Query()
	if v := query.Get("skip_drain"); v != "" {
		opts.SkipDrain = v == "true"
	}
	if v := query.Get("force"); v != "" {
		opts.Force = v == "true"
	}
	if v := query.Get("fix"); v != "" {
		opts.Fix = v == "true"
	}
	if v := query.Get("dry_run"); v != "" {
		opts.DryRun = v == "true"
	}
	if v := query.Get("canaries"); v != "" {
		opts.Canaries = v
	}
	if v := query.Get("max_in_flight"); v != "" {
		opts.MaxInFlight = v
	}

	return opts
}

func parseErrandOpts(req *http.Request) bosh.ErrandOpts {
	opts := bosh.ErrandOpts{}

	query := req.URL.Query()
	if v := query.Get("keep_alive"); v != "" {
		opts.KeepAlive = v == "true"
	}
	if v := query.Get("when_changed"); v != "" {
		opts.WhenChanged = v == "true"
	}
	if v := query.Get("instances"); v != "" {
		opts.Instances = strings.Split(v, ",")
	}

	return opts
}
