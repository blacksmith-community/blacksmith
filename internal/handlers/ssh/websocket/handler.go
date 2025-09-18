package websocket

import (
	"errors"
	"net/http"
	"regexp"

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"
)

// Error variables for err113 compliance.
var (
	errSSHTerminalUIDisabled           = errors.New("SSH Terminal UI is disabled in configuration")
	errMethodNotAllowedWebSocket       = errors.New("method not allowed: use GET or CONNECT for WebSocket")
	errWebSocketSSHServiceNotAvailable = errors.New("WebSocket SSH service not available")
)

// Handler handles SSH WebSocket streaming operations.
type Handler struct {
	logger           interfaces.Logger
	config           interfaces.Config
	webSocketHandler interfaces.WebSocketHandler
}

// Dependencies contains all dependencies needed by the SSH WebSocket handler.
type Dependencies struct {
	Logger           interfaces.Logger
	Config           interfaces.Config
	WebSocketHandler interfaces.WebSocketHandler
}

// NewHandler creates a new SSH WebSocket handler.
func NewHandler(deps Dependencies) *Handler {
	return &Handler{
		logger:           deps.Logger,
		config:           deps.Config,
		webSocketHandler: deps.WebSocketHandler,
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

	// TODO: Get deployment info - this needs to be implemented
	// For now, use placeholder values
	deploymentName := "blacksmith"
	instanceName := "blacksmith"
	instanceIndex := 0

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

	// TODO: Extract deployment info from instance ID - this needs vault lookup
	// For now, use placeholder values
	deploymentName := instanceID
	instanceName := "service"
	instanceIndex := 0

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
