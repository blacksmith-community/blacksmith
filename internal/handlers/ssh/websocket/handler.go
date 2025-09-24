package websocket

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"blacksmith/internal/interfaces"
	"blacksmith/internal/services"
	"blacksmith/pkg/http/response"
	"gopkg.in/yaml.v3"
)

// Constants for SSH websocket handler.
const (
	deploymentNameBlacksmith = "blacksmith"
)

// Error variables for err113 compliance.
var (
	errSSHTerminalUIDisabled           = errors.New("SSH Terminal UI is disabled in configuration")
	errMethodNotAllowedWebSocket       = errors.New("method not allowed: use GET or CONNECT for WebSocket")
	errWebSocketSSHServiceNotAvailable = errors.New("WebSocket SSH service not available")
	errServiceInstanceNotFound         = errors.New("service instance not found")
	errVaultNotConfigured              = errors.New("vault not configured")
	errBOSHDirectorNotConfigured       = errors.New("BOSH director not configured")
	errManifestMissingInstanceGroups   = errors.New("manifest missing instance_groups")
	errFirstInstanceGroupInvalidFormat = errors.New("first instance group has invalid format")
	errFirstInstanceGroupMissingName   = errors.New("first instance group missing name field")
)

// Handler handles SSH WebSocket streaming operations.
type Handler struct {
	logger           interfaces.Logger
	config           interfaces.Config
	webSocketHandler interfaces.WebSocketHandler
	director         interfaces.Director
	vault            interfaces.Vault
	broker           interfaces.Broker
}

// Dependencies contains all dependencies needed by the SSH WebSocket handler.
type Dependencies struct {
	Logger           interfaces.Logger
	Config           interfaces.Config
	WebSocketHandler interfaces.WebSocketHandler
	Director         interfaces.Director
	Vault            interfaces.Vault
	Broker           interfaces.Broker
}

// NewHandler creates a new SSH WebSocket handler.
func NewHandler(deps Dependencies) *Handler {
	return &Handler{
		logger:           deps.Logger,
		config:           deps.Config,
		webSocketHandler: deps.WebSocketHandler,
		director:         deps.Director,
		vault:            deps.Vault,
		broker:           deps.Broker,
	}
}

// CanHandle checks if this handler can handle the given path.
func (h *Handler) CanHandle(path string) bool {
	// Check for service instance SSH stream endpoints
	pattern := regexp.MustCompile(`^/b/[^/]+/ssh/stream$`)

	return pattern.MatchString(path)
}

// ServeHTTP handles SSH WebSocket endpoints.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	// Handle blacksmith deployment SSH WebSocket
	if req.URL.Path == "/b/blacksmith/ssh/stream" {
		h.handleBlacksmithSSH(writer, req)

		return
	}

	// Handle service instance SSH WebSocket
	pattern := regexp.MustCompile(`^/b/([^/]+)/ssh/stream$`)
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		instanceID := m[1]
		h.handleInstanceSSH(writer, req, instanceID)

		return
	}

	// Handle SSH status endpoint
	statusPattern := regexp.MustCompile(`^/b/ssh/status$`)
	if statusPattern.MatchString(req.URL.Path) {
		h.handleSSHStatus(writer, req)

		return
	}

	// No SSH WebSocket endpoint matched
	response.WriteError(writer, http.StatusNotFound, "SSH endpoint not found")
}

// handleBlacksmithSSH handles WebSocket SSH for blacksmith deployment.
func (h *Handler) handleBlacksmithSSH(writer http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("blacksmith-websocket-ssh")
	logger.Debug("WebSocket SSH connection request for blacksmith deployment")

	// Check if SSH UI Terminal is enabled in configuration
	if !h.isSSHUIEnabled() {
		logger.Info("SSH Terminal UI access denied - disabled in configuration")
		writer.WriteHeader(http.StatusForbidden)
		response.HandleJSON(writer, nil, errSSHTerminalUIDisabled)

		return
	}

	// Allow classic HTTP/1.1 Upgrade (GET) and HTTP/2 Extended CONNECT
	if req.Method != http.MethodGet && req.Method != http.MethodConnect {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		response.HandleJSON(writer, nil, errMethodNotAllowedWebSocket)

		return
	}

	deploymentName, instanceName, err := h.resolveBlacksmithDeployment(logger)
	if err != nil {
		logger.Error("Failed to resolve blacksmith deployment info: %v", err)
		writer.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(writer, nil, fmt.Errorf("failed to resolve blacksmith deployment: %w", err))

		return
	}

	query := req.URL.Query()
	if override := strings.TrimSpace(query.Get("instance")); override != "" && override != "-" {
		logger.Debug("Using instance override from query: %s", override)
		instanceName = override
	}

	instanceIndex := 0

	if indexValue := strings.TrimSpace(query.Get("index")); indexValue != "" {
		parsedIndex, parseErr := strconv.Atoi(indexValue)
		if parseErr != nil {
			logger.Error("Invalid instance index '%s', defaulting to 0", indexValue)
		} else {
			instanceIndex = parsedIndex
		}
	}

	logger.Info("Establishing WebSocket SSH connection to blacksmith deployment %s/%s/%d", deploymentName, instanceName, instanceIndex)

	// Handle WebSocket SSH connection
	if h.webSocketHandler != nil {
		h.webSocketHandler.HandleWebSocket(req.Context(), writer, req, deploymentName, instanceName, instanceIndex)
	} else {
		logger.Error("WebSocket handler not available")
		writer.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(writer, nil, errWebSocketSSHServiceNotAvailable)
	}
}

func (h *Handler) resolveBlacksmithDeployment(logger interfaces.Logger) (string, string, error) {
	deploymentName := h.readDeploymentNameFromInstanceFile(logger)
	if deploymentName == "" {
		env := strings.TrimSpace(h.config.GetEnvironment())
		if env != "" {
			deploymentName = env + "-blacksmith"
		} else {
			deploymentName = deploymentNameBlacksmith
		}

		logger.Debug("Using fallback deployment name: %s", deploymentName)
	}

	if h.director == nil {
		logger.Error("BOSH director not configured for blacksmith SSH handler")

		return deploymentName, deploymentNameBlacksmith, errBOSHDirectorNotConfigured
	}

	deployment, err := h.director.GetDeployment(deploymentName)
	if err != nil {
		logger.Error("Failed to fetch deployment manifest for %s: %v", deploymentName, err)

		return deploymentName, "blacksmith", fmt.Errorf("failed to get deployment manifest: %w", err)
	}

	resolvedDeployment, instanceGroup, err := h.extractBlacksmithManifestMetadata(deployment.Manifest, deploymentName, logger)
	if err != nil {
		return deploymentName, deploymentNameBlacksmith, err
	}

	return resolvedDeployment, instanceGroup, nil
}

func (h *Handler) readDeploymentNameFromInstanceFile(logger interfaces.Logger) string {
	data, err := os.ReadFile("/var/vcap/instance/deployment")
	if err != nil {
		logger.Debug("Unable to read deployment name from instance file: %v", err)

		return ""
	}

	deployment := strings.TrimSpace(string(data))
	if deployment == "" {
		logger.Debug("Deployment name file is empty")

		return ""
	}

	logger.Debug("Read deployment name from instance file: %s", deployment)

	return deployment
}

func (h *Handler) extractBlacksmithManifestMetadata(manifestYAML string, fallbackName string, logger interfaces.Logger) (string, string, error) {
	var manifest map[interface{}]interface{}

	err := yaml.Unmarshal([]byte(manifestYAML), &manifest)
	if err != nil {
		logger.Error("Failed to parse deployment manifest YAML: %v", err)

		return fallbackName, deploymentNameBlacksmith, fmt.Errorf("failed to parse deployment manifest: %w", err)
	}

	deploymentName := fallbackName
	if name, ok := manifest["name"].(string); ok && strings.TrimSpace(name) != "" {
		deploymentName = strings.TrimSpace(name)
		logger.Debug("Extracted deployment name from manifest: %s", deploymentName)
	} else {
		logger.Debug("Manifest missing name field, using fallback deployment name %s", fallbackName)
	}

	instanceGroups, ok := manifest["instance_groups"].([]interface{})
	if !ok || len(instanceGroups) == 0 {
		logger.Error("Manifest missing instance_groups")

		return deploymentName, deploymentNameBlacksmith, errManifestMissingInstanceGroups
	}

	firstGroup := instanceGroups[0]

	var nameValue string
	switch group := firstGroup.(type) {
	case map[interface{}]interface{}:
		if value, ok := group["name"].(string); ok {
			nameValue = value
		}
	case map[string]interface{}:
		if value, ok := group["name"].(string); ok {
			nameValue = value
		}
	default:
		logger.Error("First instance group has invalid format")

		return deploymentName, deploymentNameBlacksmith, errFirstInstanceGroupInvalidFormat
	}

	if strings.TrimSpace(nameValue) == "" {
		logger.Error("First instance group missing name field")

		return deploymentName, deploymentNameBlacksmith, errFirstInstanceGroupMissingName
	}

	instanceGroupName := strings.TrimSpace(nameValue)
	logger.Info("Resolved blacksmith deployment info: %s/%s", deploymentName, instanceGroupName)

	return deploymentName, instanceGroupName, nil
}

func (h *Handler) resolveServiceInstanceDeployment(ctx context.Context, instanceID string, logger interfaces.Logger) (string, string, error) {
	if h.vault == nil {
		logger.Error("Vault not configured for SSH handler")

		return "", "", errVaultNotConfigured
	}

	inst, exists, err := h.vault.FindInstance(ctx, instanceID)
	if err != nil {
		logger.Error("Failed to lookup instance %s in vault index: %v", instanceID, err)

		return "", "", fmt.Errorf("failed to lookup instance in vault: %w", err)
	}

	if !exists {
		logger.Error("Unable to find service instance %s in vault index", instanceID)

		return "", "", errServiceInstanceNotFound
	}

	if inst.PlanID == "" || inst.ServiceID == "" {
		logger.Error("Instance %s has missing plan_id or service_id", instanceID)

		return "", "", errServiceInstanceNotFound
	}

	deploymentName := fmt.Sprintf("%s-%s", inst.PlanID, instanceID)

	plan, hasPlan := h.lookupPlan(inst.ServiceID, inst.PlanID)

	instanceName := h.extractInstanceGroupFromManifest(ctx, instanceID, logger)
	if instanceName == "" {
		instanceName = h.defaultInstanceGroupName(inst.ServiceID, plan, hasPlan, logger)
	}

	return deploymentName, instanceName, nil
}

func (h *Handler) extractInstanceGroupFromManifest(ctx context.Context, instanceID string, logger interfaces.Logger) string {
	if h.vault == nil {
		return ""
	}

	manifestYAML, manifestRetrieved := h.retrieveManifestFromVault(ctx, instanceID, logger)
	if !manifestRetrieved {
		return ""
	}

	manifest, manifestParsed := h.parseManifestYAML(manifestYAML, instanceID, logger)
	if !manifestParsed {
		return ""
	}

	instanceGroupName, ok := h.extractFirstInstanceGroupName(manifest, instanceID, logger)
	if !ok {
		return ""
	}

	logger.Info("Successfully extracted instance group name from manifest: %s", instanceGroupName)

	return instanceGroupName
}

func (h *Handler) retrieveManifestFromVault(ctx context.Context, instanceID string, logger interfaces.Logger) (string, bool) {
	var manifestData struct {
		Manifest string `json:"manifest"`
	}

	exists, err := h.vault.Get(ctx, instanceID+"/manifest", &manifestData)
	if err != nil {
		logger.Error("Failed to retrieve manifest from vault for instance %s: %v", instanceID, err)

		return "", false
	}

	if !exists || strings.TrimSpace(manifestData.Manifest) == "" {
		logger.Error("Manifest does not exist in vault for instance %s", instanceID)

		return "", false
	}

	return manifestData.Manifest, true
}

func (h *Handler) parseManifestYAML(manifestYAML, instanceID string, logger interfaces.Logger) (map[interface{}]interface{}, bool) {
	var manifest map[interface{}]interface{}

	err := yaml.Unmarshal([]byte(manifestYAML), &manifest)
	if err != nil {
		logger.Error("Failed to parse manifest YAML for instance %s: %v", instanceID, err)

		return nil, false
	}

	return manifest, true
}

func (h *Handler) extractFirstInstanceGroupName(manifest map[interface{}]interface{}, instanceID string, logger interfaces.Logger) (string, bool) {
	instanceGroups, ok := manifest["instance_groups"].([]interface{})
	if !ok || len(instanceGroups) == 0 {
		logger.Error("Manifest has empty instance_groups for instance %s", instanceID)

		return "", false
	}

	firstGroup := instanceGroups[0]

	nameValue := h.extractGroupName(firstGroup, instanceID, logger)
	if nameValue == "" {
		return "", false
	}

	return strings.TrimSpace(nameValue), true
}

func (h *Handler) extractGroupName(firstGroup interface{}, instanceID string, logger interfaces.Logger) string {
	switch group := firstGroup.(type) {
	case map[interface{}]interface{}:
		if value, ok := group["name"].(string); ok {
			return value
		}
	case map[string]interface{}:
		if value, ok := group["name"].(string); ok {
			return value
		}
	default:
		logger.Error("First instance group has invalid format for instance %s", instanceID)

		return ""
	}

	logger.Error("First instance group missing name field for instance %s", instanceID)

	return ""
}

func (h *Handler) lookupPlan(serviceID, planID string) (services.Plan, bool) {
	if h.broker == nil {
		return services.Plan{}, false
	}

	plans := h.broker.GetPlans()
	if plans == nil {
		return services.Plan{}, false
	}

	searchKey := fmt.Sprintf("%s/%s", serviceID, planID)
	if plan, ok := plans[searchKey]; ok {
		return plan, true
	}

	if plan, ok := plans[planID]; ok {
		if plan.Service != nil && plan.Service.ID == serviceID {
			return plan, true
		}
	}

	for _, plan := range plans {
		if plan.ID == planID && plan.Service != nil && plan.Service.ID == serviceID {
			return plan, true
		}
	}

	return services.Plan{}, false
}

func (h *Handler) defaultInstanceGroupName(serviceID string, plan services.Plan, hasPlan bool, logger interfaces.Logger) string {
	planName := ""
	if hasPlan {
		planName = plan.Name
	}

	switch strings.ToLower(serviceID) {
	case "redis", "redis-cache":
		if hasPlan && strings.Contains(strings.ToLower(planName), "standalone") {
			logger.Info("Using instance group name 'standalone' for redis service (plan was: %s)", planName)

			return "standalone"
		}

		logger.Info("Using default instance group name 'redis' for redis service (plan was: %s)", planName)

		return "redis"
	case "rabbitmq":
		logger.Info("Using default instance group name 'rabbitmq' for rabbitmq service (plan was: %s)", planName)

		return "rabbitmq"
	case "postgresql":
		logger.Info("Using default instance group name 'postgres' for postgresql service (plan was: %s)", planName)

		return "postgres"
	default:
		if hasPlan && planName != "" {
			logger.Info("Could not determine instance group name, falling back to plan name: %s", planName)

			return planName
		}

		logger.Info("Could not determine instance group name, falling back to 'service'")

		return "service"
	}
}

// handleInstanceSSH handles WebSocket SSH for service instances.
func (h *Handler) handleInstanceSSH(writer http.ResponseWriter, req *http.Request, instanceID string) {
	logger := h.logger.Named("instance-websocket-ssh")
	logger.Debug("WebSocket SSH connection request for instance %s", instanceID)

	// Check if SSH UI Terminal is enabled
	if !h.isSSHUIEnabled() {
		logger.Info("SSH Terminal UI access denied for instance %s - disabled in configuration", instanceID)
		writer.WriteHeader(http.StatusForbidden)
		response.HandleJSON(writer, nil, errSSHTerminalUIDisabled)

		return
	}

	// Allow classic HTTP/1.1 Upgrade (GET) and HTTP/2 Extended CONNECT
	if req.Method != http.MethodGet && req.Method != http.MethodConnect {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		response.HandleJSON(writer, nil, errMethodNotAllowedWebSocket)

		return
	}

	deploymentName, instanceName, err := h.resolveServiceInstanceDeployment(req.Context(), instanceID, logger)
	if err != nil {
		switch {
		case errors.Is(err, errServiceInstanceNotFound):
			response.WriteError(writer, http.StatusNotFound, errServiceInstanceNotFound.Error())
		case errors.Is(err, errVaultNotConfigured):
			response.WriteError(writer, http.StatusInternalServerError, errVaultNotConfigured.Error())
		default:
			response.WriteError(writer, http.StatusInternalServerError, err.Error())
		}

		return
	}

	instanceIndex := 0

	query := req.URL.Query()
	if indexValue := strings.TrimSpace(query.Get("index")); indexValue != "" {
		parsedIndex, parseErr := strconv.Atoi(indexValue)
		if parseErr != nil {
			logger.Error("Invalid instance index '%s', defaulting to 0", indexValue)
		} else {
			instanceIndex = parsedIndex
			logger.Debug("Using instance index from query parameter: %d", instanceIndex)
		}
	}

	if override := strings.TrimSpace(query.Get("instance")); override != "" && override != "-" {
		instanceName = override
		logger.Debug("Using instance name from query parameter: %s", instanceName)
	}

	logger.Info("Establishing WebSocket SSH connection to %s/%s/%d", deploymentName, instanceName, instanceIndex)

	// Handle WebSocket SSH connection
	if h.webSocketHandler != nil {
		h.webSocketHandler.HandleWebSocket(req.Context(), writer, req, deploymentName, instanceName, instanceIndex)
	} else {
		logger.Error("WebSocket handler not available")
		writer.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(writer, nil, errWebSocketSSHServiceNotAvailable)
	}
}

// handleSSHStatus handles SSH status endpoint.
func (h *Handler) handleSSHStatus(writer http.ResponseWriter, _ *http.Request) {
	logger := h.logger.Named("websocket-ssh-status")
	logger.Debug("WebSocket SSH status request")

	// Return SSH status information
	status := map[string]interface{}{
		"enabled":           h.isSSHUIEnabled(),
		"websocket_enabled": h.webSocketHandler != nil,
	}

	// Add active sessions count if handler is available
	if h.webSocketHandler != nil {
		status["active_sessions"] = h.webSocketHandler.GetActiveSessions()
	}

	response.HandleJSON(writer, status, nil)
}

// isSSHUIEnabled checks if SSH UI Terminal is enabled.
func (h *Handler) isSSHUIEnabled() bool {
	return h.config.IsSSHUITerminalEnabled()
}
