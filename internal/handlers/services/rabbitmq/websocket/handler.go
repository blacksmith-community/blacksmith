package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"blacksmith/internal/interfaces"
	"blacksmith/internal/services"
	rabbitmqssh "blacksmith/internal/services/rabbitmq"
	gorillawebsocket "github.com/gorilla/websocket"
	"gopkg.in/yaml.v3"
)

// Static errors for err113 linter compliance.
var (
	errDeploymentParameterRequired    = errors.New("deployment parameter is required")
	errInstanceParameterRequired      = errors.New("instance parameter is required")
	errInvalidIndexValue              = errors.New("invalid index value")
	errInstanceNotFoundInVault        = errors.New("instance not found in vault")
	errInstanceMissingPlanOrServiceID = errors.New("instance has missing plan_id or service_id")
)

const (
	commandTypeExecute = "execute"

	// String splitting into category/command format.
	commandPathParts = 2

	// WebSocket buffer sizes.
	websocketReadBufferSize  = 1024
	websocketWriteBufferSize = 1024
)

var (
	rabbitMQCtlPathPattern     = regexp.MustCompile(`^/b/([^/]+)/rabbitmq/rabbitmqctl/(.+)$`)
	rabbitMQPluginsPathPattern = regexp.MustCompile(`^/b/([^/]+)/rabbitmq/plugins/(.+)$`)
)

// Handler handles RabbitMQ WebSocket streaming operations.
type Handler struct {
	logger                         interfaces.Logger
	vault                          interfaces.Vault
	broker                         interfaces.Broker
	rabbitMQExecutorService        interfaces.RabbitMQExecutorService
	rabbitMQPluginsExecutorService interfaces.RabbitMQPluginsExecutorService
	rabbitMQAuditService           interfaces.RabbitMQAuditService
	rabbitMQPluginsAuditService    interfaces.RabbitMQPluginsAuditService
	rabbitMQMetadataService        interfaces.RabbitMQMetadataService
	rabbitMQPluginsMetadataService interfaces.RabbitMQPluginsMetadataService
}

// Dependencies contains all dependencies needed by the RabbitMQ WebSocket handler.
type Dependencies struct {
	Logger                         interfaces.Logger
	Vault                          interfaces.Vault
	Broker                         interfaces.Broker
	RabbitMQExecutorService        interfaces.RabbitMQExecutorService
	RabbitMQPluginsExecutorService interfaces.RabbitMQPluginsExecutorService
	RabbitMQAuditService           interfaces.RabbitMQAuditService
	RabbitMQPluginsAuditService    interfaces.RabbitMQPluginsAuditService
	RabbitMQMetadataService        interfaces.RabbitMQMetadataService
	RabbitMQPluginsMetadataService interfaces.RabbitMQPluginsMetadataService
}

// NewHandler creates a new RabbitMQ WebSocket handler.
func NewHandler(deps Dependencies) *Handler {
	return &Handler{
		logger:                         deps.Logger,
		vault:                          deps.Vault,
		broker:                         deps.Broker,
		rabbitMQExecutorService:        deps.RabbitMQExecutorService,
		rabbitMQPluginsExecutorService: deps.RabbitMQPluginsExecutorService,
		rabbitMQAuditService:           deps.RabbitMQAuditService,
		rabbitMQPluginsAuditService:    deps.RabbitMQPluginsAuditService,
		rabbitMQMetadataService:        deps.RabbitMQMetadataService,
		rabbitMQPluginsMetadataService: deps.RabbitMQPluginsMetadataService,
	}
}

// CanHandle checks if this handler can handle the given path.
func (h *Handler) CanHandle(path string) bool {
	// ONLY handle rabbitmqctl and plugins WebSocket streaming endpoints
	// Other RabbitMQ operations (test, publish, consume, etc.) are handled by the testing handler
	if matched, _ := regexp.MatchString(`^/b/[^/]+/rabbitmq/rabbitmqctl/.+$`, path); matched {
		return true
	}

	if matched, _ := regexp.MatchString(`^/b/[^/]+/rabbitmq/plugins/.+$`, path); matched {
		return true
	}

	return false
}

// ServeHTTP handles RabbitMQ WebSocket endpoints.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if matches := rabbitMQCtlPathPattern.FindStringSubmatch(request.URL.Path); matches != nil {
		h.routeRabbitMQCtl(writer, request, matches[1], matches[2])

		return
	}

	if matches := rabbitMQPluginsPathPattern.FindStringSubmatch(request.URL.Path); matches != nil {
		h.routeRabbitMQPlugins(writer, request, matches[1], matches[2])

		return
	}

	h.writeJSONError(writer, http.StatusNotFound, "RabbitMQ WebSocket endpoint not found")
}

// HandleRabbitMQStreaming handles WebSocket connections for rabbitmqctl command streaming.
func (h *Handler) HandleRabbitMQStreaming(writer http.ResponseWriter, request *http.Request, instanceID, deploymentName, instanceName string, instanceIndex int) {
	logger := h.logger.Named("rabbitmqctl-websocket")
	logger.Info("WebSocket connection request for rabbitmqctl streaming on instance %s", instanceID)

	conn, err := h.upgradeToWebSocket(writer, request, logger)
	if err != nil {
		return
	}

	defer func() { _ = conn.Close() }()

	logger.Info("WebSocket connection established for instance %s", instanceID)

	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()

	h.handleWebSocketMessages(ctx, conn, instanceID, deploymentName, instanceName, instanceIndex, logger)
}

// HandleRabbitMQPluginsStreaming handles WebSocket connections for rabbitmq-plugins command streaming.
func (h *Handler) HandleRabbitMQPluginsStreaming(writer http.ResponseWriter, request *http.Request, instanceID, deploymentName, instanceName string, instanceIndex int) {
	logger := h.logger.Named("rabbitmq-plugins-websocket")
	logger.Info("WebSocket connection request for rabbitmq-plugins streaming on instance %s", instanceID)

	conn, err := h.upgradeToWebSocket(writer, request, logger)
	if err != nil {
		return
	}

	defer func() { _ = conn.Close() }()

	logger.Info("WebSocket connection established for instance %s", instanceID)

	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()

	h.handlePluginsWebSocketMessages(ctx, conn, instanceID, deploymentName, instanceName, instanceIndex, logger)
}

func (h *Handler) upgradeToWebSocket(writer http.ResponseWriter, request *http.Request, logger interfaces.Logger) (*gorillawebsocket.Conn, error) {
	upgrader := gorillawebsocket.Upgrader{
		CheckOrigin: func(req *http.Request) bool {
			if h.isOriginAllowed(req, logger) {
				return true
			}

			logger.Warn("Rejected WebSocket origin %q for host %q", request.Header.Get("Origin"), req.Host)

			return false
		},
		ReadBufferSize:  websocketReadBufferSize,
		WriteBufferSize: websocketWriteBufferSize,
	}

	conn, err := upgrader.Upgrade(writer, request, nil)
	if err != nil {
		logger.Error("WebSocket upgrade failed: %v", err)

		return nil, fmt.Errorf("failed to upgrade websocket: %w", err)
	}

	return conn, nil
}

type operationHandlers struct {
	handleCategories func(http.ResponseWriter, *http.Request)
	handleHistory    func(http.ResponseWriter, *http.Request, string)
	handleExecute    func(http.ResponseWriter, *http.Request, string)
	handleStreaming  func(http.ResponseWriter, *http.Request, string, string, string, int)
	operationName    string
}

func (h *Handler) handleOperationSwitch(writer http.ResponseWriter, request *http.Request, instanceID, operation string, logger interfaces.Logger, handlers operationHandlers) {
	switch operation {
	case "categories":
		if request.Method != http.MethodGet {
			h.writeJSONError(writer, http.StatusMethodNotAllowed, "Method not allowed. Use GET.")

			return
		}

		handlers.handleCategories(writer, request)
	case "history":
		handlers.handleHistory(writer, request, instanceID)
	case "execute":
		if request.Method != http.MethodPost {
			h.writeJSONError(writer, http.StatusMethodNotAllowed, "Method not allowed. Use POST.")

			return
		}

		handlers.handleExecute(writer, request, instanceID)
	case "stream":
		if request.Method != http.MethodGet {
			h.writeJSONError(writer, http.StatusMethodNotAllowed, "Method not allowed. Use GET for WebSocket upgrade.")

			return
		}

		if !h.isWebSocketUpgrade(request) {
			h.writeJSONError(writer, http.StatusBadRequest, "WebSocket upgrade required")

			return
		}

		deploymentName, instanceName, instanceIndex, err := h.extractContextParameters(request)
		if err != nil {
			logger.Warn("Invalid context parameters for %s stream: %v", handlers.operationName, err)
			h.writeJSONError(writer, http.StatusBadRequest, err.Error())

			return
		}

		handlers.handleStreaming(writer, request, instanceID, deploymentName, instanceName, instanceIndex)
	default:
		h.writeJSONError(writer, http.StatusNotFound, "unknown "+handlers.operationName+" operation: "+operation)
	}
}

func (h *Handler) routeRabbitMQCtl(writer http.ResponseWriter, request *http.Request, instanceID, operation string) {
	logger := h.logger.Named("rabbitmqctl-router")
	logger.Debug("Handling rabbitmqctl request: instance=%s operation=%s method=%s", instanceID, operation, request.Method)

	if strings.HasPrefix(operation, "category/") {
		remainder := strings.TrimPrefix(operation, "category/")

		if request.Method != http.MethodGet {
			h.writeJSONError(writer, http.StatusMethodNotAllowed, "Method not allowed. Use GET.")

			return
		}

		if strings.Contains(remainder, "/command/") {
			parts := strings.SplitN(remainder, "/command/", commandPathParts)
			if len(parts) == commandPathParts {
				h.handleRabbitMQCtlCommand(writer, request, parts[0], parts[1])

				return
			}
		}

		h.handleRabbitMQCtlCategory(writer, request, remainder)

		return
	}

	handlers := operationHandlers{
		handleCategories: h.handleRabbitMQCtlCategories,
		handleHistory:    h.handleRabbitMQCtlHistory,
		handleExecute:    h.handleRabbitMQCtlExecute,
		handleStreaming:  h.HandleRabbitMQStreaming,
		operationName:    "rabbitmqctl",
	}

	h.handleOperationSwitch(writer, request, instanceID, operation, logger, handlers)
}

func (h *Handler) routeRabbitMQPlugins(writer http.ResponseWriter, request *http.Request, instanceID, operation string) {
	logger := h.logger.Named("rabbitmq-plugins-router")
	logger.Debug("Handling rabbitmq-plugins request: instance=%s operation=%s method=%s", instanceID, operation, request.Method)

	if strings.HasPrefix(operation, "category/") {
		remainder := strings.TrimPrefix(operation, "category/")

		if request.Method != http.MethodGet {
			h.writeJSONError(writer, http.StatusMethodNotAllowed, "Method not allowed. Use GET.")

			return
		}

		if strings.Contains(remainder, "/command/") {
			parts := strings.SplitN(remainder, "/command/", commandPathParts)
			if len(parts) == commandPathParts {
				h.handleRabbitMQPluginsCommand(writer, request, parts[1])

				return
			}
		}

		h.handleRabbitMQPluginsCategory(writer, request, remainder)

		return
	}

	handlers := operationHandlers{
		handleCategories: h.handleRabbitMQPluginsCategories,
		handleHistory:    h.handleRabbitMQPluginsHistory,
		handleExecute:    h.handleRabbitMQPluginsExecute,
		handleStreaming:  h.HandleRabbitMQPluginsStreaming,
		operationName:    "rabbitmq-plugins",
	}

	h.handleOperationSwitch(writer, request, instanceID, operation, logger, handlers)
}

func (h *Handler) extractContextParameters(request *http.Request) (string, string, int, error) {
	query := request.URL.Query()
	deploymentName := strings.TrimSpace(query.Get("deployment"))
	instanceName := strings.TrimSpace(query.Get("instance"))
	indexValue := strings.TrimSpace(query.Get("index"))

	if deploymentName == "" {
		return "", "", 0, errDeploymentParameterRequired
	}

	if instanceName == "" {
		return "", "", 0, errInstanceParameterRequired
	}

	instanceIndex := 0

	if indexValue != "" {
		parsedIndex, err := strconv.Atoi(indexValue)
		if err != nil {
			return "", "", 0, fmt.Errorf("%s: %w", indexValue, errInvalidIndexValue)
		}

		instanceIndex = parsedIndex
	}

	return deploymentName, instanceName, instanceIndex, nil
}

func (h *Handler) getDeploymentContext(ctx context.Context, instanceID string) (string, string, error) {
	inst, exists, err := h.vault.FindInstance(ctx, instanceID)
	if err != nil {
		return "", "", fmt.Errorf("failed to lookup instance in vault: %w", err)
	}

	if !exists {
		return "", "", errInstanceNotFoundInVault
	}

	if inst.PlanID == "" || inst.ServiceID == "" {
		return "", "", errInstanceMissingPlanOrServiceID
	}

	deploymentName := fmt.Sprintf("%s-%s", inst.PlanID, instanceID)

	plan, hasPlan := h.lookupPlan(inst.ServiceID, inst.PlanID)

	instanceName := h.extractInstanceGroupFromManifest(ctx, instanceID)
	if instanceName == "" {
		instanceName = h.defaultInstanceGroupName(inst.ServiceID, plan, hasPlan)
	}

	return deploymentName, instanceName, nil
}

func (h *Handler) extractInstanceGroupFromManifest(ctx context.Context, instanceID string) string {
	if h.vault == nil {
		return ""
	}

	var manifestData struct {
		Manifest string `json:"manifest"`
	}

	exists, err := h.vault.Get(ctx, instanceID+"/manifest", &manifestData)
	if err != nil || !exists || strings.TrimSpace(manifestData.Manifest) == "" {
		return ""
	}

	var manifest map[interface{}]interface{}

	err = yaml.Unmarshal([]byte(manifestData.Manifest), &manifest)
	if err != nil {
		return ""
	}

	instanceGroups, ok := manifest["instance_groups"].([]interface{})
	if !ok || len(instanceGroups) == 0 {
		return ""
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
	}

	return strings.TrimSpace(nameValue)
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

func (h *Handler) defaultInstanceGroupName(serviceID string, plan services.Plan, hasPlan bool) string {
	planName := ""
	if hasPlan {
		planName = plan.Name
	}

	switch strings.ToLower(serviceID) {
	case "rabbitmq":
		if hasPlan && (strings.Contains(strings.ToLower(planName), "standalone") || strings.Contains(strings.ToLower(planName), "single")) {
			return "rabbitmq"
		}

		return "node"
	default:
		return "unknown"
	}
}

func (h *Handler) isWebSocketUpgrade(request *http.Request) bool {
	if request.Method != http.MethodGet {
		return false
	}

	upgrade := request.Header.Get("Upgrade")
	connection := request.Header.Get("Connection")

	if upgrade == "" && request.Method == http.MethodConnect {
		// HTTP/2 extended CONNECT does not include the Upgrade header.
		return true
	}

	return strings.EqualFold(upgrade, "websocket") && strings.Contains(strings.ToLower(connection), "upgrade")
}

func (h *Handler) isOriginAllowed(request *http.Request, logger interfaces.Logger) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return true
	}

	originURL, err := url.Parse(origin)
	if err != nil || originURL.Scheme == "" || originURL.Host == "" {
		logger.Warn("Rejecting WebSocket origin %q due to parse error: %v", origin, err)

		return false
	}

	requestHost := h.normalizeHost(request.Host)
	originHost := h.normalizeHost(originURL.Host)

	if strings.EqualFold(originHost, requestHost) {
		return true
	}

	if originHostname := originURL.Hostname(); originHostname != "" {
		if strings.EqualFold(originHostname, requestHost) {
			return true
		}

		switch strings.ToLower(originHostname) {
		case "localhost", "127.0.0.1", "::1", "[::1]":
			return true
		}
	}

	return false
}

func (h *Handler) normalizeHost(host string) string {
	if host == "" {
		return host
	}

	parsedHost, _, err := net.SplitHostPort(host)
	if err == nil {
		return parsedHost
	}

	return host
}

func (h *Handler) writeJSON(writer http.ResponseWriter, data interface{}) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)

	err := json.NewEncoder(writer).Encode(data)
	if err != nil {
		h.logger.Error("Failed to write JSON response: %v", err)
	}
}

func (h *Handler) writeJSONError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = fmt.Fprintf(writer, `{"error": %q}`, message)
}

func (h *Handler) handleWebSocketMessages(ctx context.Context, conn *gorillawebsocket.Conn, instanceID, deploymentName, instanceName string, instanceIndex int, logger interfaces.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			err := h.processWebSocketMessage(ctx, conn, instanceID, deploymentName, instanceName, instanceIndex, logger)
			if err != nil {
				return
			}
		}
	}
}

func (h *Handler) processWebSocketMessage(ctx context.Context, conn *gorillawebsocket.Conn, instanceID, deploymentName, instanceName string, instanceIndex int, logger interfaces.Logger) error {
	var message struct {
		Type      string   `json:"type"`
		Category  string   `json:"category"`
		Command   string   `json:"command"`
		Arguments []string `json:"arguments"`
	}

	err := conn.ReadJSON(&message)
	if err != nil {
		if gorillawebsocket.IsUnexpectedCloseError(err, gorillawebsocket.CloseGoingAway, gorillawebsocket.CloseAbnormalClosure) {
			logger.Error("WebSocket read error: %v", err)
		}

		return fmt.Errorf("failed to read websocket message: %w", err)
	}

	return h.handleMessageType(ctx, conn, instanceID, deploymentName, instanceName, instanceIndex, message, logger)
}

func (h *Handler) handleMessageType(ctx context.Context, conn *gorillawebsocket.Conn, instanceID, deploymentName, instanceName string, instanceIndex int, message struct {
	Type      string   `json:"type"`
	Category  string   `json:"category"`
	Command   string   `json:"command"`
	Arguments []string `json:"arguments"`
}, logger interfaces.Logger) error {
	switch message.Type {
	case commandTypeExecute:
		h.handleStreamingExecution(ctx, conn, instanceID, deploymentName, instanceName, instanceIndex, message.Category, message.Command, message.Arguments, logger)

		return nil
	case "ping":
		return h.sendPongResponse(conn, logger)
	default:
		return h.sendUnknownMessageError(conn, message.Type, logger)
	}
}

func (h *Handler) sendPongResponse(conn *gorillawebsocket.Conn, logger interfaces.Logger) error {
	response := map[string]interface{}{
		"type": "pong",
	}

	err := conn.WriteJSON(response)
	if err != nil {
		logger.Error("Failed to send pong: %v", err)

		return fmt.Errorf("failed to send pong response: %w", err)
	}

	return nil
}

func (h *Handler) sendUnknownMessageError(conn *gorillawebsocket.Conn, messageType string, logger interfaces.Logger) error {
	response := map[string]interface{}{
		"type":  "error",
		"error": "unknown message type: " + messageType,
	}

	err := conn.WriteJSON(response)
	if err != nil {
		logger.Error("Failed to send error response: %v", err)

		return fmt.Errorf("failed to send error response: %w", err)
	}

	return nil
}

func (h *Handler) handlePluginsWebSocketMessages(ctx context.Context, conn *gorillawebsocket.Conn, instanceID, deploymentName, instanceName string, instanceIndex int, logger interfaces.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			err := h.processPluginsWebSocketMessage(ctx, conn, instanceID, deploymentName, instanceName, instanceIndex, logger)
			if err != nil {
				return
			}
		}
	}
}

func (h *Handler) processPluginsWebSocketMessage(ctx context.Context, conn *gorillawebsocket.Conn, instanceID, deploymentName, instanceName string, instanceIndex int, logger interfaces.Logger) error {
	var message struct {
		Type      string   `json:"type"`
		Category  string   `json:"category"`
		Command   string   `json:"command"`
		Arguments []string `json:"arguments"`
	}

	err := conn.ReadJSON(&message)
	if err != nil {
		if gorillawebsocket.IsUnexpectedCloseError(err, gorillawebsocket.CloseGoingAway, gorillawebsocket.CloseAbnormalClosure) {
			logger.Error("WebSocket read error: %v", err)
		}

		return fmt.Errorf("failed to read websocket message: %w", err)
	}

	return h.handlePluginsMessageType(ctx, conn, instanceID, deploymentName, instanceName, instanceIndex, message, logger)
}

func (h *Handler) handlePluginsMessageType(ctx context.Context, conn *gorillawebsocket.Conn, instanceID, deploymentName, instanceName string, instanceIndex int, message struct {
	Type      string   `json:"type"`
	Category  string   `json:"category"`
	Command   string   `json:"command"`
	Arguments []string `json:"arguments"`
}, logger interfaces.Logger) error {
	switch message.Type {
	case commandTypeExecute:
		logger.Info("Executing rabbitmq-plugins command: %s.%s with args %v", message.Category, message.Command, message.Arguments)
		h.handlePluginsStreamingExecution(ctx, conn, instanceID, deploymentName, instanceName, instanceIndex, message.Category, message.Command, message.Arguments, logger)

		return nil
	case "ping":
		return h.sendPongResponse(conn, logger)
	default:
		return h.sendUnknownMessageError(conn, message.Type, logger)
	}
}

func (h *Handler) handleRabbitMQCtlCategories(writer http.ResponseWriter, _ *http.Request) {
	if h.rabbitMQMetadataService == nil {
		h.writeJSONError(writer, http.StatusServiceUnavailable, "RabbitMQ metadata service not available")

		return
	}

	categories := h.rabbitMQMetadataService.GetCategories()
	h.writeJSON(writer, categories)
}

func (h *Handler) handleRabbitMQCtlCategory(writer http.ResponseWriter, _ *http.Request, categoryName string) {
	if h.rabbitMQMetadataService == nil {
		h.writeJSONError(writer, http.StatusServiceUnavailable, "RabbitMQ metadata service not available")

		return
	}

	category, err := h.rabbitMQMetadataService.GetCategory(categoryName)
	if err != nil {
		h.writeJSONError(writer, http.StatusNotFound, "Category not found: "+categoryName)

		return
	}

	h.writeJSON(writer, category)
}

func (h *Handler) handleRabbitMQCtlCommand(writer http.ResponseWriter, _ *http.Request, categoryName, commandName string) {
	if h.rabbitMQMetadataService == nil {
		h.writeJSONError(writer, http.StatusServiceUnavailable, "RabbitMQ metadata service not available")

		return
	}

	command, err := h.rabbitMQMetadataService.GetCommand(categoryName, commandName)
	if err != nil {
		h.writeJSONError(writer, http.StatusNotFound, "Command not found: "+categoryName+"/"+commandName)

		return
	}

	h.writeJSON(writer, command)
}

func (h *Handler) handleRabbitMQCtlHistory(writer http.ResponseWriter, request *http.Request, instanceID string) {
	if h.rabbitMQAuditService == nil {
		h.writeJSONError(writer, http.StatusServiceUnavailable, "RabbitMQ audit service not available")

		return
	}

	switch request.Method {
	case http.MethodGet:
		const defaultLimit = 100

		history, err := h.rabbitMQAuditService.GetAuditHistory(request.Context(), instanceID, defaultLimit)
		if err != nil {
			h.logger.Error("Failed to get audit history: %v", err)
			h.writeJSONError(writer, http.StatusInternalServerError, "Failed to retrieve audit history")

			return
		}

		h.writeJSON(writer, history)
	case http.MethodDelete:
		h.writeJSON(writer, map[string]interface{}{
			"success": true,
			"message": "Command execution history cleared",
		})
	default:
		h.writeJSONError(writer, http.StatusMethodNotAllowed, "Method not allowed. Use GET or DELETE.")
	}
}

type executeRequest struct {
	Category  string   `json:"category"`
	Command   string   `json:"command"`
	Arguments []string `json:"arguments"`
}

func (h *Handler) handleRabbitMQCtlExecute(writer http.ResponseWriter, request *http.Request, instanceID string) {
	if h.rabbitMQExecutorService == nil {
		h.writeJSONError(writer, http.StatusServiceUnavailable, "RabbitMQ executor service not available")

		return
	}

	var req executeRequest

	err := json.NewDecoder(request.Body).Decode(&req)
	if err != nil {
		h.writeJSONError(writer, http.StatusBadRequest, "Invalid request body: "+err.Error())

		return
	}

	deploymentName, instanceName, err := h.getDeploymentContext(request.Context(), instanceID)
	if err != nil {
		h.logger.Error("Failed to get deployment context for instance %s: %v", instanceID, err)
		h.writeJSONError(writer, http.StatusInternalServerError, "Failed to get deployment context: "+err.Error())

		return
	}

	execCtx := rabbitmqssh.ExecutionContext{
		InstanceID: instanceID,
		User:       "blacksmith",
		ClientIP:   request.RemoteAddr,
	}

	result, err := h.rabbitMQExecutorService.ExecuteCommandSync(
		request.Context(),
		execCtx,
		deploymentName,
		instanceName,
		0,
		req.Category,
		req.Command,
		req.Arguments,
	)
	if err != nil {
		h.logger.Error("Failed to execute command: %v", err)
		h.writeJSONError(writer, http.StatusInternalServerError, "Command execution failed: "+err.Error())

		return
	}

	h.writeJSON(writer, result)
}

func (h *Handler) handleRabbitMQPluginsCategories(writer http.ResponseWriter, _ *http.Request) {
	if h.rabbitMQPluginsMetadataService == nil {
		h.writeJSONError(writer, http.StatusServiceUnavailable, "RabbitMQ plugins metadata service not available")

		return
	}

	categories := h.rabbitMQPluginsMetadataService.GetCategories()
	h.writeJSON(writer, categories)
}

func (h *Handler) handleRabbitMQPluginsCategory(writer http.ResponseWriter, _ *http.Request, categoryName string) {
	if h.rabbitMQPluginsMetadataService == nil {
		h.writeJSONError(writer, http.StatusServiceUnavailable, "RabbitMQ plugins metadata service not available")

		return
	}

	category, err := h.rabbitMQPluginsMetadataService.GetCategory(categoryName)
	if err != nil {
		h.writeJSONError(writer, http.StatusNotFound, "Category not found: "+categoryName)

		return
	}

	h.writeJSON(writer, category)
}

func (h *Handler) handleRabbitMQPluginsCommand(writer http.ResponseWriter, _ *http.Request, commandName string) {
	if h.rabbitMQPluginsMetadataService == nil {
		h.writeJSONError(writer, http.StatusServiceUnavailable, "RabbitMQ plugins metadata service not available")

		return
	}

	command, err := h.rabbitMQPluginsMetadataService.GetCommand(commandName)
	if err != nil {
		h.writeJSONError(writer, http.StatusNotFound, "Command not found: "+commandName)

		return
	}

	h.writeJSON(writer, command)
}

func (h *Handler) handleRabbitMQPluginsHistory(writer http.ResponseWriter, request *http.Request, instanceID string) {
	if h.rabbitMQPluginsAuditService == nil {
		h.writeJSONError(writer, http.StatusServiceUnavailable, "RabbitMQ plugins audit service not available")

		return
	}

	switch request.Method {
	case http.MethodGet:
		const defaultLimit = 100

		history, err := h.rabbitMQPluginsAuditService.GetHistory(request.Context(), instanceID, defaultLimit)
		if err != nil {
			h.logger.Error("Failed to get plugins history: %v", err)
			h.writeJSONError(writer, http.StatusInternalServerError, "Failed to retrieve plugins history")

			return
		}

		h.writeJSON(writer, history)
	case http.MethodDelete:
		h.writeJSON(writer, map[string]interface{}{
			"success": true,
			"message": "Command execution history cleared",
		})
	default:
		h.writeJSONError(writer, http.StatusMethodNotAllowed, "Method not allowed. Use GET or DELETE.")
	}
}

func (h *Handler) handleRabbitMQPluginsExecute(writer http.ResponseWriter, request *http.Request, instanceID string) {
	if h.rabbitMQPluginsExecutorService == nil {
		h.writeJSONError(writer, http.StatusServiceUnavailable, "RabbitMQ plugins executor service not available")

		return
	}

	var req executeRequest

	err := json.NewDecoder(request.Body).Decode(&req)
	if err != nil {
		h.writeJSONError(writer, http.StatusBadRequest, "Invalid request body: "+err.Error())

		return
	}

	deploymentName, instanceName, err := h.getDeploymentContext(request.Context(), instanceID)
	if err != nil {
		h.logger.Error("Failed to get deployment context for instance %s: %v", instanceID, err)
		h.writeJSONError(writer, http.StatusInternalServerError, "Failed to get deployment context: "+err.Error())

		return
	}

	execCtx := rabbitmqssh.PluginsExecutionContext{
		InstanceID: instanceID,
		User:       "blacksmith",
		ClientIP:   request.RemoteAddr,
	}

	output, exitCode, err := h.rabbitMQPluginsExecutorService.ExecuteCommandSync(
		request.Context(),
		execCtx,
		deploymentName,
		instanceName,
		0,
		req.Category,
		req.Command,
		req.Arguments,
	)
	if err != nil {
		h.logger.Error("Failed to execute plugins command: %v", err)
		h.writeJSONError(writer, http.StatusInternalServerError, "Command execution failed: "+err.Error())

		return
	}

	result := map[string]interface{}{
		"output":    output,
		"exit_code": exitCode,
		"success":   exitCode == 0,
	}

	h.writeJSON(writer, result)
}
