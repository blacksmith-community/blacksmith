package websocket

import (
	"context"
	"fmt"
	"net/http"
	"regexp"

	"blacksmith/internal/interfaces"
	gorillawebsocket "github.com/gorilla/websocket"
)

const (
	commandTypeExecute = "execute"

	// WebSocket buffer sizes.
	websocketReadBufferSize  = 1024
	websocketWriteBufferSize = 1024
)

// Handler handles RabbitMQ WebSocket streaming operations.
type Handler struct {
	logger                         interfaces.Logger
	rabbitMQExecutorService        interfaces.RabbitMQExecutorService
	rabbitMQPluginsExecutorService interfaces.RabbitMQPluginsExecutorService
	rabbitMQAuditService           interfaces.RabbitMQAuditService
	rabbitMQPluginsAuditService    interfaces.RabbitMQPluginsAuditService
}

// Dependencies contains all dependencies needed by the RabbitMQ WebSocket handler.
type Dependencies struct {
	Logger                         interfaces.Logger
	RabbitMQExecutorService        interfaces.RabbitMQExecutorService
	RabbitMQPluginsExecutorService interfaces.RabbitMQPluginsExecutorService
	RabbitMQAuditService           interfaces.RabbitMQAuditService
	RabbitMQPluginsAuditService    interfaces.RabbitMQPluginsAuditService
}

// NewHandler creates a new RabbitMQ WebSocket handler.
func NewHandler(deps Dependencies) *Handler {
	return &Handler{
		logger:                         deps.Logger,
		rabbitMQExecutorService:        deps.RabbitMQExecutorService,
		rabbitMQPluginsExecutorService: deps.RabbitMQPluginsExecutorService,
		rabbitMQAuditService:           deps.RabbitMQAuditService,
		rabbitMQPluginsAuditService:    deps.RabbitMQPluginsAuditService,
	}
}

// CanHandle checks if this handler can handle the given path.
func (h *Handler) CanHandle(path string) bool {
	// Check for RabbitMQ rabbitmqctl endpoints
	if matched, _ := regexp.MatchString(`^/b/[^/]+/rabbitmq/rabbitmqctl/.+$`, path); matched {
		return true
	}
	// Check for RabbitMQ plugins endpoints
	if matched, _ := regexp.MatchString(`^/b/[^/]+/rabbitmq/plugins/.+$`, path); matched {
		return true
	}
	// Check for generic RabbitMQ endpoints
	if matched, _ := regexp.MatchString(`^/b/[^/]+/rabbitmq/.+$`, path); matched {
		return true
	}

	return false
}

// ServeHTTP handles RabbitMQ WebSocket endpoints.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// For now, return not implemented
	// TODO: Implement proper routing to the specific handler methods
	w.WriteHeader(http.StatusNotImplemented)

	_, err := w.Write([]byte("RabbitMQ WebSocket endpoints not yet fully implemented"))
	if err != nil {
		h.logger.Warn("Failed to write response: %v", err)
	}
}

// HandleRabbitMQStreaming handles WebSocket connections for rabbitmqctl command streaming.
func (h *Handler) HandleRabbitMQStreaming(w http.ResponseWriter, request *http.Request, instanceID, deploymentName, instanceName string, instanceIndex int) {
	logger := h.logger.Named("rabbitmqctl-websocket")
	logger.Info("WebSocket connection request for rabbitmqctl streaming on instance %s", instanceID)

	conn, err := h.upgradeToWebSocket(w, request, logger)
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
func (h *Handler) HandleRabbitMQPluginsStreaming(w http.ResponseWriter, request *http.Request, instanceID, deploymentName, instanceName string, instanceIndex int) {
	logger := h.logger.Named("rabbitmq-plugins-websocket")
	logger.Info("WebSocket connection request for rabbitmq-plugins streaming on instance %s", instanceID)

	conn, err := h.upgradeToWebSocket(w, request, logger)
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
		CheckOrigin: func(r *http.Request) bool {
			return true // TODO: implement proper origin checking
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
