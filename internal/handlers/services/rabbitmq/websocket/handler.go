package websocket

import (
	"context"
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
	w.Write([]byte("RabbitMQ WebSocket endpoints not yet fully implemented"))
}

// HandleRabbitMQStreaming handles WebSocket connections for rabbitmqctl command streaming.
func (h *Handler) HandleRabbitMQStreaming(w http.ResponseWriter, request *http.Request, instanceID, deploymentName, instanceName string, instanceIndex int) {
	logger := h.logger.Named("rabbitmqctl-websocket")
	logger.Info("WebSocket connection request for rabbitmqctl streaming on instance %s", instanceID)

	// Upgrade to WebSocket
	upgrader := gorillawebsocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // TODO: implement proper origin checking
		},
		ReadBufferSize:  websocketReadBufferSize,
		WriteBufferSize: websocketWriteBufferSize,
	}

	conn, err := upgrader.Upgrade(w, request, nil)
	if err != nil {
		logger.Error("WebSocket upgrade failed: %v", err)

		return
	}

	defer func() { _ = conn.Close() }()

	logger.Info("WebSocket connection established for instance %s", instanceID)

	// Set up message handling
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()

	// Handle incoming messages
	for {
		select {
		case <-ctx.Done():
			return
		default:
			var message struct {
				Type      string   `json:"type"`
				Category  string   `json:"category"`
				Command   string   `json:"command"`
				Arguments []string `json:"arguments"`
			}

			// Read message from WebSocket
			err := conn.ReadJSON(&message)
			if err != nil {
				if gorillawebsocket.IsUnexpectedCloseError(err, gorillawebsocket.CloseGoingAway, gorillawebsocket.CloseAbnormalClosure) {
					logger.Error("WebSocket read error: %v", err)
				}

				return
			}

			// Handle different message types
			switch message.Type {
			case commandTypeExecute:
				h.handleStreamingExecution(ctx, conn, instanceID, deploymentName, instanceName, instanceIndex, message.Category, message.Command, message.Arguments, logger)
			case "ping":
				// Respond to ping
				response := map[string]interface{}{
					"type": "pong",
				}

				err := conn.WriteJSON(response)
				if err != nil {
					logger.Error("Failed to send pong: %v", err)

					return
				}
			default:
				// Send error for unknown message type
				response := map[string]interface{}{
					"type":  "error",
					"error": "unknown message type: " + message.Type,
				}

				err := conn.WriteJSON(response)
				if err != nil {
					logger.Error("Failed to send error response: %v", err)

					return
				}
			}
		}
	}
}

// HandleRabbitMQPluginsStreaming handles WebSocket connections for rabbitmq-plugins command streaming.
func (h *Handler) HandleRabbitMQPluginsStreaming(w http.ResponseWriter, request *http.Request, instanceID, deploymentName, instanceName string, instanceIndex int) {
	logger := h.logger.Named("rabbitmq-plugins-websocket")
	logger.Info("WebSocket connection request for rabbitmq-plugins streaming on instance %s", instanceID)

	// Upgrade to WebSocket
	upgrader := gorillawebsocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // TODO: implement proper origin checking
		},
		ReadBufferSize:  websocketReadBufferSize,
		WriteBufferSize: websocketWriteBufferSize,
	}

	conn, err := upgrader.Upgrade(w, request, nil)
	if err != nil {
		logger.Error("WebSocket upgrade failed: %v", err)

		return
	}

	defer func() { _ = conn.Close() }()

	logger.Info("WebSocket connection established for instance %s", instanceID)

	// Set up message handling
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()

	// Handle incoming messages
	for {
		select {
		case <-ctx.Done():
			return
		default:
			var message struct {
				Type      string   `json:"type"`
				Category  string   `json:"category"`
				Command   string   `json:"command"`
				Arguments []string `json:"arguments"`
			}

			// Read message from WebSocket
			err := conn.ReadJSON(&message)
			if err != nil {
				if gorillawebsocket.IsUnexpectedCloseError(err, gorillawebsocket.CloseGoingAway, gorillawebsocket.CloseAbnormalClosure) {
					logger.Error("WebSocket read error: %v", err)
				}

				return
			}

			// Handle different message types
			switch message.Type {
			case commandTypeExecute:
				logger.Info("Executing rabbitmq-plugins command: %s.%s with args %v", message.Category, message.Command, message.Arguments)
				h.handlePluginsStreamingExecution(ctx, conn, instanceID, deploymentName, instanceName, instanceIndex, message.Category, message.Command, message.Arguments, logger)

			case "ping":
				response := map[string]interface{}{
					"type": "pong",
				}

				err := conn.WriteJSON(response)
				if err != nil {
					logger.Error("Failed to send pong response: %v", err)

					return
				}

			default:
				response := map[string]interface{}{
					"type":  "error",
					"error": "unknown message type: " + message.Type,
				}

				err := conn.WriteJSON(response)
				if err != nil {
					logger.Error("Failed to send error response: %v", err)

					return
				}
			}
		}
	}
}
