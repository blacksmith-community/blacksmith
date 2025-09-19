package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"blacksmith/internal/bosh/ssh"
	wsn "nhooyr.io/websocket"
)

// Constants for WebSocket SSH handler.
const (
	SessionCleanupInterval     = 5 * time.Minute
	DefaultStreamOutputRetries = 5
)

// Static errors for err113 compliance.
var (
	ErrUnknownMessageType             = errors.New("unknown message type")
	ErrControlMessageMissingMetaData  = errors.New("control message missing meta data")
	ErrControlMessageMissingAction    = errors.New("control message missing action")
	ErrUnknownControlAction           = errors.New("unknown control action")
	ErrSSHSessionNotStarted           = errors.New("SSH session not started")
	ErrResizeMessageMissingDimensions = errors.New("resize message missing width or height")
)

// SSHHandler manages WebSocket connections for SSH streaming.
type SSHHandler struct {
	sshService    ssh.SSHService
	sessions      map[string]*SSHStreamSession
	sessionsMutex sync.RWMutex
	logger        Logger
	config        Config
}

// Logger interface for logging.
type Logger interface {
	Infof(format string, args ...interface{})
	Debugf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

// Config holds WebSocket configuration.
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

// SSHStreamSession represents an active SSH streaming session.
type SSHStreamSession struct {
	ID           string
	Deployment   string
	Instance     string
	Index        int
	Conn         WSConn
	SSHSession   ssh.SSHSession
	Cancel       context.CancelFunc
	LastActivity time.Time
	Mutex        sync.RWMutex
	Logger       Logger
}

// WSConn abstracts WebSocket operations used by SSH handler.
type WSConn interface {
	ReadJSON(v interface{}) error
	WriteJSON(v interface{}) error
	Ping(ctx context.Context) error
	Close() error
	SetReadLimit(n int64)
}

// nhooyrWSConn implements WSConn using nhooyr.io/websocket.
type nhooyrWSConn struct {
	conn *wsn.Conn
}

func (c *nhooyrWSConn) ReadJSON(target interface{}) error {
	ctx := context.Background()

	_, data, err := c.conn.Read(ctx)
	if err != nil {
		return fmt.Errorf("failed to read from websocket connection: %w", err)
	}

	err = json.Unmarshal(data, target)
	if err != nil {
		return fmt.Errorf("failed to unmarshal JSON data: %w", err)
	}

	return nil
}

func (c *nhooyrWSConn) WriteJSON(v interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), SSHDialTimeout)
	defer cancel()

	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	err = c.conn.Write(ctx, wsn.MessageText, b)
	if err != nil {
		return fmt.Errorf("failed to write to websocket connection: %w", err)
	}

	return nil
}

func (c *nhooyrWSConn) Ping(ctx context.Context) error {
	err := c.conn.Ping(ctx)
	if err != nil {
		return fmt.Errorf("failed to ping websocket connection: %w", err)
	}

	return nil
}

func (c *nhooyrWSConn) Close() error {
	err := c.conn.Close(wsn.StatusNormalClosure, "")
	if err != nil {
		return fmt.Errorf("failed to close websocket connection: %w", err)
	}

	return nil
}

func (c *nhooyrWSConn) SetReadLimit(n int64) {
	c.conn.SetReadLimit(n)
}

// WSMessage represents a WebSocket message for SSH communication.
type WSMessage struct {
	Type      string                 `json:"type"`
	Data      string                 `json:"data,omitempty"`
	Stream    string                 `json:"stream,omitempty"`
	Sequence  int64                  `json:"seq,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
	Meta      map[string]interface{} `json:"meta,omitempty"`
}

// Message types.
const (
	MsgTypeInput     = "input"     // User input to SSH session
	MsgTypeOutput    = "output"    // Output from SSH session
	MsgTypeControl   = "control"   // Control messages (resize, signal, etc.)
	MsgTypeError     = "error"     // Error messages
	MsgTypeHandshake = "handshake" // Initial connection handshake
	MsgTypeHeartbeat = "heartbeat" // Keep-alive messages
	MsgTypeStatus    = "status"    // Session status updates
)

// Stream types.
const (
	StreamStdin  = "stdin"
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// NewSSHHandler creates a new WebSocket SSH handler.
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
		config.HandshakeTimeout = HandshakeTimeout
	}

	if config.MaxMessageSize == 0 {
		config.MaxMessageSize = MaxMessageSize
	}

	if config.PingInterval == 0 {
		config.PingInterval = PingInterval
	}

	if config.PongTimeout == 0 {
		config.PongTimeout = PongTimeout
	}

	if config.MaxSessions == 0 {
		config.MaxSessions = 100
	}

	if config.SessionTimeout == 0 {
		config.SessionTimeout = SessionTimeout
	}

	handler := &SSHHandler{
		sshService:    sshService,
		sessions:      make(map[string]*SSHStreamSession),
		sessionsMutex: sync.RWMutex{},
		logger:        logger,
		config:        config,
	}

	// Start session cleanup goroutine
	go handler.sessionCleanupLoop()

	return handler
}

// HandleWebSocket handles WebSocket upgrade and SSH session management.
//
//nolint:funlen
func (h *SSHHandler) HandleWebSocket(ctx context.Context, writer http.ResponseWriter, request *http.Request, deployment, instance string, index int) {
	h.logger.Infof("WebSocket SSH connection request for %s/%s/%d", deployment, instance, index)

	_, hj := writer.(http.Hijacker)
	h.logger.Debugf("WS upgrade debug: proto=%s, hijacker=%v, writer=%T", request.Proto, hj, writer)

	// Check session limit
	h.sessionsMutex.RLock()
	currentSessions := len(h.sessions)
	h.sessionsMutex.RUnlock()

	if currentSessions >= h.config.MaxSessions {
		h.logger.Errorf("Maximum WebSocket sessions (%d) reached", h.config.MaxSessions)
		http.Error(writer, "Too many active sessions", http.StatusTooManyRequests)

		return
	}

	// Accept connection via nhooyr (supports HTTP/1.1 and HTTP/2)
	rawConn, err := acceptWS(writer, request, h.config.EnableCompression)
	if err != nil {
		h.logger.Errorf("Failed to upgrade WebSocket connection: %v", err)

		return
	}

	conn := rawConn

	defer func() { _ = conn.Close() }()

	// Set connection limits
	conn.SetReadLimit(h.config.MaxMessageSize)

	// Create session
	sessionID := fmt.Sprintf("ws-ssh-%d", time.Now().UnixNano())
	sessionCtx, cancel := context.WithTimeout(ctx, h.config.SessionTimeout)

	defer cancel()

	session := &SSHStreamSession{
		ID:           sessionID,
		Deployment:   deployment,
		Instance:     instance,
		Index:        index,
		Conn:         conn,
		Cancel:       cancel,
		LastActivity: time.Now(),
		Logger:       h.logger,
	}

	h.logger.Infof("Created WebSocket SSH session: %s", sessionID)

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
			err := session.SSHSession.Close()
			if err != nil {
				h.logger.Errorf("Failed to close SSH session: %v", err)
			}
		}

		h.logger.Infof("Cleaned up WebSocket SSH session: %s", sessionID)
	}()

	// Handle the session
	h.handleSession(sessionCtx, session)
}

// Close shuts down the handler and cleans up all sessions.
func (h *SSHHandler) Close() error {
	h.logger.Infof("Closing WebSocket SSH handler")

	h.sessionsMutex.Lock()
	defer h.sessionsMutex.Unlock()

	for sessionID, session := range h.sessions {
		h.logger.Debugf("Closing session: %s", sessionID)

		if session.SSHSession != nil {
			err := session.SSHSession.Close()
			if err != nil {
				h.logger.Errorf("Failed to close SSH session %s: %v", sessionID, err)
			}
		}

		session.Cancel()
	}

	h.sessions = make(map[string]*SSHStreamSession)

	return nil
}

// GetActiveSessions returns the number of active sessions.
func (h *SSHHandler) GetActiveSessions() int {
	h.sessionsMutex.RLock()
	defer h.sessionsMutex.RUnlock()

	return len(h.sessions)
}

// handleSession manages a WebSocket SSH session.
func (h *SSHHandler) handleSession(ctx context.Context, session *SSHStreamSession) {
	h.logger.Debugf("Starting WebSocket SSH session handler: %s", session.ID)

	// Start ping routine
	go h.pingRoutine(ctx, session)

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

	err := h.sendMessage(session, handshakeMsg)
	if err != nil {
		h.logger.Errorf("Failed to send handshake message: %v", err)

		return
	}

	// Handle incoming WebSocket messages
	for {
		select {
		case <-ctx.Done():
			h.logger.Debugf("WebSocket SSH session context cancelled: %s", session.ID)

			return
		default:
			// Read message from WebSocket
			var msg WSMessage

			err := session.Conn.ReadJSON(&msg)
			if err != nil {
				h.logger.Debugf("WebSocket connection closed or read error: %v", err)

				return
			}

			// Update activity timestamp
			session.Mutex.Lock()
			session.LastActivity = time.Now()
			session.Mutex.Unlock()

			// Handle the message
			err = h.handleMessage(ctx, session, msg)
			if err != nil {
				h.logger.Errorf("Failed to handle WebSocket message: %v", err)
				h.sendErrorMessage(session, fmt.Sprintf("Message handling error: %v", err))
			}
		}
	}
}

// handleMessage processes incoming WebSocket messages.
func (h *SSHHandler) handleMessage(ctx context.Context, session *SSHStreamSession, msg WSMessage) error {
	// Debug logging removed to reduce noise for input messages
	switch msg.Type {
	case MsgTypeControl:
		return h.handleControlMessage(ctx, session, msg)
	case MsgTypeInput:
		return h.handleInputMessage(session, msg)
	case MsgTypeHeartbeat:
		return h.handleHeartbeatMessage(session, msg)
	default:
		return fmt.Errorf("%w: %s", ErrUnknownMessageType, msg.Type)
	}
}

// handleControlMessage handles control messages (session start, window resize, etc.)
func (h *SSHHandler) handleControlMessage(ctx context.Context, session *SSHStreamSession, msg WSMessage) error {
	h.logger.Debugf("Handling control message for session: %s", session.ID)

	if msg.Meta == nil {
		return ErrControlMessageMissingMetaData
	}

	action, ok := msg.Meta["action"].(string)
	if !ok {
		return ErrControlMessageMissingAction
	}

	switch action {
	case "start":
		return h.startSSHSession(ctx, session, msg)
	case "resize":
		return h.resizeSession(session, msg)
	case "signal":
		return h.sendSignal(session)
	default:
		return fmt.Errorf("%w: %s", ErrUnknownControlAction, action)
	}
}

// startSSHSession initiates the SSH session.
func (h *SSHHandler) startSSHSession(ctx context.Context, session *SSHStreamSession, msg WSMessage) error {
	h.logger.Infof("Starting SSH session for WebSocket: %s", session.ID)

	// Create SSH request
	sshReq := &ssh.SSHRequest{
		Deployment: session.Deployment,
		Instance:   session.Instance,
		Index:      session.Index,
		Options: &ssh.SSHOptions{
			Terminal:     true,
			TerminalType: "xterm-256color",
			WindowWidth:  DefaultTerminalWidth,
			WindowHeight: DefaultTerminalHeight,
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

	h.logger.Debugf("SSH request: %+v", sshReq)

	// Create SSH session
	sshSession, err := h.sshService.CreateSession(sshReq)
	if err != nil {
		return fmt.Errorf("failed to create SSH session: %w", err)
	}

	session.SSHSession = sshSession

	// Start SSH session
	err = sshSession.Start()
	if err != nil {
		return fmt.Errorf("failed to start SSH session: %w", err)
	}

	// Start output streaming
	go h.streamOutput(ctx, session)

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

// resizeSession handles terminal resize.
func (h *SSHHandler) resizeSession(session *SSHStreamSession, msg WSMessage) error {
	if session.SSHSession == nil {
		return ErrSSHSessionNotStarted
	}

	width, widthOk := msg.Meta["width"].(float64)
	height, heightOk := msg.Meta["height"].(float64)

	if !widthOk || !heightOk {
		return ErrResizeMessageMissingDimensions
	}

	err := session.SSHSession.SetWindowSize(int(width), int(height))
	if err != nil {
		return fmt.Errorf("failed to set SSH session window size: %w", err)
	}

	return nil
}

// sendSignal handles signal sending (future implementation).
func (h *SSHHandler) sendSignal(_ *SSHStreamSession) error {
	// TODO: Implement signal sending when SSH session supports it
	h.logger.Debugf("Signal sending not yet implemented")

	return nil
}

// handleHeartbeatMessage handles heartbeat/ping messages.
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

// StreamOutputState holds state for output streaming.
type StreamOutputState struct {
	Sequence         int64
	RetryCount       int
	MaxRetries       int
	LastActivityTime time.Time
}

// streamOutput continuously reads from SSH session and sends to WebSocket.
func (h *SSHHandler) streamOutput(ctx context.Context, session *SSHStreamSession) {
	h.logger.Debugf("Starting output streaming for session: %s", session.ID)

	state := &StreamOutputState{
		Sequence:         0,
		RetryCount:       0,
		MaxRetries:       DefaultStreamOutputRetries,
		LastActivityTime: time.Now(),
	}

	for {
		select {
		case <-ctx.Done():
			h.logger.Debugf("Output streaming stopped for session: %s", session.ID)

			return
		default:
			if !h.processStreamingLoop(session, state) {
				return
			}
		}
	}
}

// processStreamingLoop processes one iteration of the streaming loop.
func (h *SSHHandler) processStreamingLoop(session *SSHStreamSession, state *StreamOutputState) bool {
	if !h.ensureSSHSessionReady(session, state) {
		return false
	}

	output, err := session.SSHSession.ReadOutput()
	if !h.handleSSHReadError(err, session, state) {
		return false
	}

	if len(output) > 0 {
		if !h.processSSHOutput(output, session, state) {
			return false
		}
	}

	time.Sleep(ShortSleep)

	return true
}

// ensureSSHSessionReady ensures SSH session is ready or handles retry logic.
func (h *SSHHandler) ensureSSHSessionReady(session *SSHStreamSession, state *StreamOutputState) bool {
	if session.SSHSession != nil {
		if state.RetryCount > 0 {
			h.logger.Debugf("SSH session available for session %s after %d retries", session.ID, state.RetryCount)
			state.RetryCount = 0
		}

		return true
	}

	time.Sleep(LongSleep)

	state.RetryCount++
	if state.RetryCount > state.MaxRetries {
		h.logger.Errorf("SSH session not initialized after %d retries for session: %s (waited %v)",
			state.MaxRetries, session.ID, time.Since(state.LastActivityTime))
		h.sendErrorMessage(session, "SSH session initialization failed")

		return false
	}

	return true
}

// handleSSHReadError handles errors from reading SSH output.
func (h *SSHHandler) handleSSHReadError(err error, session *SSHStreamSession, state *StreamOutputState) bool {
	if err == nil {
		return true
	}

	sessionStatus := session.SSHSession.Status()
	duration := time.Since(state.LastActivityTime)

	// Check if error is due to session state transition
	if err.Error() == "Session not connected" {
		h.logger.Debugf("SSH session %s temporarily not connected (status: %v, duration: %v), retrying...",
			session.ID, sessionStatus, duration)
		time.Sleep(MediumSleep)

		return true
	}

	h.logger.Errorf("Failed to read SSH output for session %s: %v (status: %v, duration: %v, sequence: %d)",
		session.ID, err, sessionStatus, duration, state.Sequence)
	h.sendErrorMessage(session, fmt.Sprintf("SSH read error: %v", err))

	return false
}

// processSSHOutput processes successful SSH output.
func (h *SSHHandler) processSSHOutput(output []byte, session *SSHStreamSession, state *StreamOutputState) bool {
	state.Sequence++
	state.LastActivityTime = time.Now()

	// Log periodic activity for debugging
	if state.Sequence%100 == 0 {
		h.logger.Debugf("SSH session %s activity: sequence %d, output size %d bytes",
			session.ID, state.Sequence, len(output))
	}

	// Send output to WebSocket
	outputMsg := WSMessage{
		Type:      MsgTypeOutput,
		Data:      string(output),
		Stream:    StreamStdout,
		Sequence:  state.Sequence,
		Timestamp: time.Now(),
	}

	err := h.sendMessage(session, outputMsg)
	if err != nil {
		h.logger.Errorf("Failed to send output message for session %s (sequence: %d): %v",
			session.ID, state.Sequence, err)

		return false
	}

	return true
}

// pingRoutine sends periodic ping messages to keep connection alive.
func (h *SSHHandler) pingRoutine(ctx context.Context, session *SSHStreamSession) {
	ticker := time.NewTicker(h.config.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, h.config.PongTimeout)

			err := session.Conn.Ping(pingCtx)
			if err != nil {
				cancel()
				h.logger.Debugf("Failed to send ping to session %s: %v", session.ID, err)

				return
			}

			cancel()
		}
	}
}

// sendMessage sends a message to the WebSocket connection.
func (h *SSHHandler) sendMessage(session *SSHStreamSession, msg WSMessage) error {
	session.Mutex.Lock()
	defer session.Mutex.Unlock()

	err := session.Conn.WriteJSON(msg)
	if err != nil {
		return fmt.Errorf("failed to write JSON message to websocket: %w", err)
	}

	return nil
}

// sendErrorMessage sends an error message to the WebSocket connection.
func (h *SSHHandler) sendErrorMessage(session *SSHStreamSession, errorMsg string) {
	msg := WSMessage{
		Type:      MsgTypeError,
		Data:      errorMsg,
		Timestamp: time.Now(),
	}

	err := h.sendMessage(session, msg)
	if err != nil {
		h.logger.Errorf("Failed to send error message: %v", err)
	}
}

// sessionCleanupLoop periodically cleans up inactive sessions.
func (h *SSHHandler) sessionCleanupLoop() {
	ticker := time.NewTicker(SessionCleanupInterval)
	defer ticker.Stop()

	for range ticker.C {
		h.cleanupInactiveSessions()
	}
}

// cleanupInactiveSessions removes sessions that have been inactive too long.
func (h *SSHHandler) cleanupInactiveSessions() {
	h.sessionsMutex.Lock()
	defer h.sessionsMutex.Unlock()

	now := time.Now()

	for sessionID, session := range h.sessions {
		session.Mutex.RLock()
		lastActivity := session.LastActivity
		session.Mutex.RUnlock()

		if now.Sub(lastActivity) > h.config.SessionTimeout {
			h.logger.Infof("Cleaning up inactive session: %s", sessionID)

			if session.SSHSession != nil {
				err := session.SSHSession.Close()
				if err != nil {
					h.logger.Errorf("Failed to close SSH session %s: %v", sessionID, err)
				}
			}

			session.Cancel()
			delete(h.sessions, sessionID)
		}
	}
}

// handleInputMessage handles user input.
func (h *SSHHandler) handleInputMessage(session *SSHStreamSession, msg WSMessage) error {
	if session.SSHSession == nil {
		// Don't log this as an error since it's expected during initialization
		h.logger.Debugf("Received input for session %s before SSH session is ready, ignoring", session.ID)

		return nil // Return nil to avoid error propagation
	}

	// Send input to SSH session
	err := session.SSHSession.SendInput([]byte(msg.Data))
	if err != nil {
		return fmt.Errorf("failed to send input to SSH session: %w", err)
	}

	return nil
}

// noOpLogger is a no-operation logger implementation.
type noOpLogger struct{}

func (l *noOpLogger) Infof(format string, args ...interface{})  {}
func (l *noOpLogger) Debugf(format string, args ...interface{}) {}
func (l *noOpLogger) Errorf(format string, args ...interface{}) {}
