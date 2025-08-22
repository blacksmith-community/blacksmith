package websocket

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"blacksmith/bosh/ssh"
	"github.com/gorilla/websocket"
)

// SSHHandler manages WebSocket connections for SSH streaming
type SSHHandler struct {
	sshService    ssh.SSHService
	upgrader      websocket.Upgrader
	sessions      map[string]*SSHStreamSession
	sessionsMutex sync.RWMutex
	logger        Logger
	config        Config
}

// Logger interface for logging
type Logger interface {
	Info(format string, args ...interface{})
	Debug(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// Config holds WebSocket configuration
type Config struct {
	ReadBufferSize    int           `yaml:"read_buffer_size"`
	WriteBufferSize   int           `yaml:"write_buffer_size"`
	HandshakeTimeout  time.Duration `yaml:"handshake_timeout"`
	MaxMessageSize    int64         `yaml:"max_message_size"`
	PingInterval      time.Duration `yaml:"ping_interval"`
	PongTimeout       time.Duration `yaml:"pong_timeout"`
	MaxSessions       int           `yaml:"max_sessions"`
	SessionTimeout    time.Duration `yaml:"session_timeout"`
	EnableCompression bool          `yaml:"enable_compression"`
}

// SSHStreamSession represents an active SSH streaming session
type SSHStreamSession struct {
	ID           string
	Deployment   string
	Instance     string
	Index        int
	Conn         *websocket.Conn
	SSHSession   ssh.SSHSession
	Context      context.Context
	Cancel       context.CancelFunc
	LastActivity time.Time
	Mutex        sync.RWMutex
	Logger       Logger
}

// WSMessage represents a WebSocket message for SSH communication
type WSMessage struct {
	Type      string                 `json:"type"`
	Data      string                 `json:"data,omitempty"`
	Stream    string                 `json:"stream,omitempty"`
	Sequence  int64                  `json:"seq,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
	Meta      map[string]interface{} `json:"meta,omitempty"`
}

// Message types
const (
	MsgTypeInput     = "input"     // User input to SSH session
	MsgTypeOutput    = "output"    // Output from SSH session
	MsgTypeControl   = "control"   // Control messages (resize, signal, etc.)
	MsgTypeError     = "error"     // Error messages
	MsgTypeHandshake = "handshake" // Initial connection handshake
	MsgTypeHeartbeat = "heartbeat" // Keep-alive messages
	MsgTypeStatus    = "status"    // Session status updates
)

// Stream types
const (
	StreamStdin  = "stdin"
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// NewSSHHandler creates a new WebSocket SSH handler
func NewSSHHandler(sshService ssh.SSHService, config Config, logger Logger) *SSHHandler {
	if logger == nil {
		logger = &noOpLogger{}
	}

	// Set default configuration values
	if config.ReadBufferSize == 0 {
		config.ReadBufferSize = 4096
	}
	if config.WriteBufferSize == 0 {
		config.WriteBufferSize = 4096
	}
	if config.HandshakeTimeout == 0 {
		config.HandshakeTimeout = 10 * time.Second
	}
	if config.MaxMessageSize == 0 {
		config.MaxMessageSize = 32 * 1024 // 32KB
	}
	if config.PingInterval == 0 {
		config.PingInterval = 30 * time.Second
	}
	if config.PongTimeout == 0 {
		config.PongTimeout = 10 * time.Second
	}
	if config.MaxSessions == 0 {
		config.MaxSessions = 100
	}
	if config.SessionTimeout == 0 {
		config.SessionTimeout = 30 * time.Minute
	}

	upgrader := websocket.Upgrader{
		ReadBufferSize:    config.ReadBufferSize,
		WriteBufferSize:   config.WriteBufferSize,
		HandshakeTimeout:  config.HandshakeTimeout,
		EnableCompression: config.EnableCompression,
		CheckOrigin: func(r *http.Request) bool {
			// TODO: Implement proper origin checking for security
			return true
		},
	}

	handler := &SSHHandler{
		sshService:    sshService,
		upgrader:      upgrader,
		sessions:      make(map[string]*SSHStreamSession),
		sessionsMutex: sync.RWMutex{},
		logger:        logger,
		config:        config,
	}

	// Start session cleanup goroutine
	go handler.sessionCleanupLoop()

	return handler
}

// HandleWebSocket handles WebSocket upgrade and SSH session management
func (h *SSHHandler) HandleWebSocket(w http.ResponseWriter, r *http.Request, deployment, instance string, index int) {
	h.logger.Info("WebSocket SSH connection request for %s/%s/%d", deployment, instance, index)

	// Check session limit
	h.sessionsMutex.RLock()
	currentSessions := len(h.sessions)
	h.sessionsMutex.RUnlock()

	if currentSessions >= h.config.MaxSessions {
		h.logger.Error("Maximum WebSocket sessions (%d) reached", h.config.MaxSessions)
		http.Error(w, "Too many active sessions", http.StatusTooManyRequests)
		return
	}

	// Upgrade connection to WebSocket
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("Failed to upgrade WebSocket connection: %v", err)
		return
	}
	defer conn.Close()

	// Set connection limits
	conn.SetReadLimit(h.config.MaxMessageSize)

	// Create session
	sessionID := fmt.Sprintf("ws-ssh-%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), h.config.SessionTimeout)
	defer cancel()

	session := &SSHStreamSession{
		ID:           sessionID,
		Deployment:   deployment,
		Instance:     instance,
		Index:        index,
		Conn:         conn,
		Context:      ctx,
		Cancel:       cancel,
		LastActivity: time.Now(),
		Logger:       h.logger,
	}

	h.logger.Info("Created WebSocket SSH session: %s", sessionID)

	// Register session
	h.sessionsMutex.Lock()
	h.sessions[sessionID] = session
	h.sessionsMutex.Unlock()

	// Ensure session cleanup
	defer func() {
		h.sessionsMutex.Lock()
		delete(h.sessions, sessionID)
		h.sessionsMutex.Unlock()

		if session.SSHSession != nil {
			if err := session.SSHSession.Close(); err != nil {
				h.logger.Error("Failed to close SSH session: %v", err)
			}
		}

		h.logger.Info("Cleaned up WebSocket SSH session: %s", sessionID)
	}()

	// Handle the session
	h.handleSession(session)
}

// handleSession manages a WebSocket SSH session
func (h *SSHHandler) handleSession(session *SSHStreamSession) {
	h.logger.Debug("Starting WebSocket SSH session handler: %s", session.ID)

	// Set up ping/pong for connection keep-alive
	session.Conn.SetPongHandler(func(appData string) error {
		session.Mutex.Lock()
		session.LastActivity = time.Now()
		session.Mutex.Unlock()
		return session.Conn.SetReadDeadline(time.Now().Add(h.config.PongTimeout))
	})

	// Start ping routine
	go h.pingRoutine(session)

	// Send handshake message
	handshakeMsg := WSMessage{
		Type:      MsgTypeHandshake,
		Data:      "WebSocket SSH session established",
		Timestamp: time.Now(),
		Meta: map[string]interface{}{
			"session_id": session.ID,
			"deployment": session.Deployment,
			"instance":   session.Instance,
			"index":      session.Index,
		},
	}

	if err := h.sendMessage(session, handshakeMsg); err != nil {
		h.logger.Error("Failed to send handshake message: %v", err)
		return
	}

	// Handle incoming WebSocket messages
	for {
		select {
		case <-session.Context.Done():
			h.logger.Debug("WebSocket SSH session context cancelled: %s", session.ID)
			return
		default:
			// Read message from WebSocket
			var msg WSMessage
			err := session.Conn.ReadJSON(&msg)
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					h.logger.Error("WebSocket read error: %v", err)
				} else {
					h.logger.Debug("WebSocket connection closed: %s", session.ID)
				}
				return
			}

			// Update activity timestamp
			session.Mutex.Lock()
			session.LastActivity = time.Now()
			session.Mutex.Unlock()

			// Handle the message
			if err := h.handleMessage(session, msg); err != nil {
				h.logger.Error("Failed to handle WebSocket message: %v", err)
				h.sendErrorMessage(session, fmt.Sprintf("Message handling error: %v", err))
			}
		}
	}
}

// handleMessage processes incoming WebSocket messages
func (h *SSHHandler) handleMessage(session *SSHStreamSession, msg WSMessage) error {
	// Debug logging removed to reduce noise for input messages

	switch msg.Type {
	case MsgTypeControl:
		return h.handleControlMessage(session, msg)
	case MsgTypeInput:
		return h.handleInputMessage(session, msg)
	case MsgTypeHeartbeat:
		return h.handleHeartbeatMessage(session, msg)
	default:
		return fmt.Errorf("unknown message type: %s", msg.Type)
	}
}

// handleControlMessage handles control messages (session start, window resize, etc.)
func (h *SSHHandler) handleControlMessage(session *SSHStreamSession, msg WSMessage) error {
	h.logger.Debug("Handling control message for session: %s", session.ID)

	if msg.Meta == nil {
		return fmt.Errorf("control message missing meta data")
	}

	action, ok := msg.Meta["action"].(string)
	if !ok {
		return fmt.Errorf("control message missing action")
	}

	switch action {
	case "start":
		return h.startSSHSession(session, msg)
	case "resize":
		return h.resizeSession(session, msg)
	case "signal":
		return h.sendSignal(session, msg)
	default:
		return fmt.Errorf("unknown control action: %s", action)
	}
}

// startSSHSession initiates the SSH session
func (h *SSHHandler) startSSHSession(session *SSHStreamSession, msg WSMessage) error {
	h.logger.Info("Starting SSH session for WebSocket: %s", session.ID)

	// Create SSH request
	sshReq := &ssh.SSHRequest{
		Deployment: session.Deployment,
		Instance:   session.Instance,
		Index:      session.Index,
		Options: &ssh.SSHOptions{
			Terminal:     true,
			TerminalType: "xterm-256color",
			WindowWidth:  80,
			WindowHeight: 24,
			Interactive:  true,
		},
	}

	// Parse terminal options from message
	if msg.Meta != nil {
		if width, ok := msg.Meta["width"].(float64); ok {
			sshReq.Options.WindowWidth = int(width)
		}
		if height, ok := msg.Meta["height"].(float64); ok {
			sshReq.Options.WindowHeight = int(height)
		}
		if termType, ok := msg.Meta["term"].(string); ok && termType != "" {
			sshReq.Options.TerminalType = termType
		}
	}

	h.logger.Debug("SSH request: %+v", sshReq)

	// Create SSH session
	sshSession, err := h.sshService.CreateSession(sshReq)
	if err != nil {
		return fmt.Errorf("failed to create SSH session: %w", err)
	}

	session.SSHSession = sshSession

	// Start SSH session
	if err := sshSession.Start(); err != nil {
		return fmt.Errorf("failed to start SSH session: %w", err)
	}

	// Start output streaming
	go h.streamOutput(session)

	// Send session started confirmation
	statusMsg := WSMessage{
		Type:      MsgTypeStatus,
		Data:      "SSH session started",
		Timestamp: time.Now(),
		Meta: map[string]interface{}{
			"status": "connected",
		},
	}

	return h.sendMessage(session, statusMsg)
}

// handleInputMessage handles user input
func (h *SSHHandler) handleInputMessage(session *SSHStreamSession, msg WSMessage) error {
	if session.SSHSession == nil {
		return fmt.Errorf("SSH session not started")
	}

	// Send input to SSH session
	return session.SSHSession.SendInput([]byte(msg.Data))
}

// resizeSession handles terminal resize
func (h *SSHHandler) resizeSession(session *SSHStreamSession, msg WSMessage) error {
	if session.SSHSession == nil {
		return fmt.Errorf("SSH session not started")
	}

	width, widthOk := msg.Meta["width"].(float64)
	height, heightOk := msg.Meta["height"].(float64)

	if !widthOk || !heightOk {
		return fmt.Errorf("resize message missing width or height")
	}

	return session.SSHSession.SetWindowSize(int(width), int(height))
}

// sendSignal handles signal sending (future implementation)
func (h *SSHHandler) sendSignal(session *SSHStreamSession, msg WSMessage) error {
	// TODO: Implement signal sending when SSH session supports it
	h.logger.Debug("Signal sending not yet implemented")
	return nil
}

// handleHeartbeatMessage handles heartbeat/ping messages
func (h *SSHHandler) handleHeartbeatMessage(session *SSHStreamSession, msg WSMessage) error {
	// Send heartbeat response
	responseMsg := WSMessage{
		Type:      MsgTypeHeartbeat,
		Data:      "pong",
		Timestamp: time.Now(),
		Sequence:  msg.Sequence,
	}

	return h.sendMessage(session, responseMsg)
}

// streamOutput continuously reads from SSH session and sends to WebSocket
func (h *SSHHandler) streamOutput(session *SSHStreamSession) {
	h.logger.Debug("Starting output streaming for session: %s", session.ID)

	sequence := int64(0)

	for {
		select {
		case <-session.Context.Done():
			h.logger.Debug("Output streaming stopped for session: %s", session.ID)
			return
		default:
			if session.SSHSession == nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// Read output from SSH session
			output, err := session.SSHSession.ReadOutput()
			if err != nil {
				h.logger.Error("Failed to read SSH output: %v", err)
				h.sendErrorMessage(session, fmt.Sprintf("SSH read error: %v", err))
				return
			}

			if len(output) > 0 {
				sequence++

				// Send output to WebSocket
				outputMsg := WSMessage{
					Type:      MsgTypeOutput,
					Data:      string(output),
					Stream:    StreamStdout,
					Sequence:  sequence,
					Timestamp: time.Now(),
				}

				if err := h.sendMessage(session, outputMsg); err != nil {
					h.logger.Error("Failed to send output message: %v", err)
					return
				}
			}

			// Small delay to prevent busy loop
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// pingRoutine sends periodic ping messages to keep connection alive
func (h *SSHHandler) pingRoutine(session *SSHStreamSession) {
	ticker := time.NewTicker(h.config.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-session.Context.Done():
			return
		case <-ticker.C:
			if err := session.Conn.WriteMessage(websocket.PingMessage, []byte{}); err != nil {
				h.logger.Debug("Failed to send ping to session %s: %v", session.ID, err)
				return
			}
		}
	}
}

// sendMessage sends a message to the WebSocket connection
func (h *SSHHandler) sendMessage(session *SSHStreamSession, msg WSMessage) error {
	session.Mutex.Lock()
	defer session.Mutex.Unlock()

	return session.Conn.WriteJSON(msg)
}

// sendErrorMessage sends an error message to the WebSocket connection
func (h *SSHHandler) sendErrorMessage(session *SSHStreamSession, errorMsg string) {
	msg := WSMessage{
		Type:      MsgTypeError,
		Data:      errorMsg,
		Timestamp: time.Now(),
	}

	if err := h.sendMessage(session, msg); err != nil {
		h.logger.Error("Failed to send error message: %v", err)
	}
}

// sessionCleanupLoop periodically cleans up inactive sessions
func (h *SSHHandler) sessionCleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		h.cleanupInactiveSessions()
	}
}

// cleanupInactiveSessions removes sessions that have been inactive too long
func (h *SSHHandler) cleanupInactiveSessions() {
	h.sessionsMutex.Lock()
	defer h.sessionsMutex.Unlock()

	now := time.Now()
	for sessionID, session := range h.sessions {
		session.Mutex.RLock()
		lastActivity := session.LastActivity
		session.Mutex.RUnlock()

		if now.Sub(lastActivity) > h.config.SessionTimeout {
			h.logger.Info("Cleaning up inactive session: %s", sessionID)

			if session.SSHSession != nil {
				if err := session.SSHSession.Close(); err != nil {
					h.logger.Error("Failed to close SSH session %s: %v", sessionID, err)
				}
			}

			session.Cancel()
			delete(h.sessions, sessionID)
		}
	}
}

// GetActiveSessions returns the number of active sessions
func (h *SSHHandler) GetActiveSessions() int {
	h.sessionsMutex.RLock()
	defer h.sessionsMutex.RUnlock()
	return len(h.sessions)
}

// Close shuts down the handler and cleans up all sessions
func (h *SSHHandler) Close() error {
	h.logger.Info("Closing WebSocket SSH handler")

	h.sessionsMutex.Lock()
	defer h.sessionsMutex.Unlock()

	for sessionID, session := range h.sessions {
		h.logger.Debug("Closing session: %s", sessionID)

		if session.SSHSession != nil {
			if err := session.SSHSession.Close(); err != nil {
				h.logger.Error("Failed to close SSH session %s: %v", sessionID, err)
			}
		}

		session.Cancel()
	}

	h.sessions = make(map[string]*SSHStreamSession)
	return nil
}

// noOpLogger is a no-operation logger implementation
type noOpLogger struct{}

func (l *noOpLogger) Info(format string, args ...interface{})  {}
func (l *noOpLogger) Debug(format string, args ...interface{}) {}
func (l *noOpLogger) Error(format string, args ...interface{}) {}
