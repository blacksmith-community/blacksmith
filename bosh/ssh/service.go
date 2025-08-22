package ssh

import (
	"fmt"
	"sync"
	"time"
)

// ServiceImpl implements the SSHService interface
type ServiceImpl struct {
	director     DirectorInterface
	config       Config
	sessionPool  map[string]SSHSession
	sessionMutex sync.RWMutex
	logger       Logger
}

// DirectorInterface defines the methods we need from the BOSH director
type DirectorInterface interface {
	SSHCommand(deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error)
	SSHSession(deployment, instance string, index int, options map[string]interface{}) (interface{}, error)
}

// Config holds configuration for the SSH service
type Config struct {
	Timeout        time.Duration
	ConnectTimeout time.Duration
	MaxConcurrent  int
	MaxOutputSize  int
	KeepAlive      time.Duration
	RetryAttempts  int
	RetryDelay     time.Duration
}

// Logger interface for logging
type Logger interface {
	Info(format string, args ...interface{})
	Debug(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// NewSSHService creates a new SSH service instance
func NewSSHService(director DirectorInterface, config Config, logger Logger) SSHService {
	if logger == nil {
		logger = &noOpLogger{}
	}

	return &ServiceImpl{
		director:     director,
		config:       config,
		sessionPool:  make(map[string]SSHSession),
		sessionMutex: sync.RWMutex{},
		logger:       logger,
	}
}

// ExecuteCommand executes a one-off command on a BOSH VM
func (s *ServiceImpl) ExecuteCommand(request *SSHRequest) (*SSHResponse, error) {
	s.logger.Info("Executing SSH command on %s/%s/%d", request.Deployment, request.Instance, request.Index)
	s.logger.Debug("Command: %s %v", request.Command, request.Args)

	startTime := time.Now()

	// Validate request
	if err := s.validateRequest(request); err != nil {
		return nil, NewSSHError(SSHErrorCodeInvalidRequest, "Invalid SSH request", err)
	}

	// Use the timeout from the request or default config timeout

	// Prepare options
	options := make(map[string]interface{})
	if request.Options != nil {
		if request.Options.ConnectTimeout > 0 {
			options["connect_timeout"] = request.Options.ConnectTimeout
		}
		if request.Options.BufferOutput {
			options["buffer_output"] = true
		}
		if request.Options.MaxOutputSize > 0 {
			options["max_output_size"] = request.Options.MaxOutputSize
		}
	}

	// Execute via director
	output, err := s.director.SSHCommand(
		request.Deployment,
		request.Instance,
		request.Index,
		request.Command,
		request.Args,
		options,
	)

	duration := time.Since(startTime)

	if err != nil {
		s.logger.Error("SSH command failed: %v", err)
		return &SSHResponse{
			Success:   false,
			ExitCode:  1,
			Duration:  duration.Milliseconds(),
			Error:     err.Error(),
			Timestamp: startTime,
		}, nil
	}

	s.logger.Info("SSH command completed successfully in %v", duration)

	return &SSHResponse{
		Success:   true,
		ExitCode:  0,
		Duration:  duration.Milliseconds(),
		Stdout:    output,
		Timestamp: startTime,
	}, nil
}

// CreateSession creates an interactive SSH session for streaming
func (s *ServiceImpl) CreateSession(request *SSHRequest) (SSHSession, error) {
	s.logger.Info("Creating SSH session for %s/%s/%d", request.Deployment, request.Instance, request.Index)

	// Validate request
	if err := s.validateRequest(request); err != nil {
		return nil, NewSSHError(SSHErrorCodeInvalidRequest, "Invalid SSH request", err)
	}

	// Check concurrent session limit
	s.sessionMutex.RLock()
	currentSessions := len(s.sessionPool)
	s.sessionMutex.RUnlock()

	if currentSessions >= s.config.MaxConcurrent {
		return nil, NewSSHError(SSHErrorCodeInternal,
			fmt.Sprintf("Maximum concurrent sessions (%d) reached", s.config.MaxConcurrent), nil)
	}

	// Prepare options
	options := make(map[string]interface{})
	if request.Options != nil {
		if request.Options.Terminal {
			options["terminal"] = true
			options["terminal_type"] = request.Options.TerminalType
			options["window_width"] = request.Options.WindowWidth
			options["window_height"] = request.Options.WindowHeight
		}
		if request.Options.Interactive {
			options["interactive"] = true
		}
	}

	// Create session via director
	sessionInfo, err := s.director.SSHSession(
		request.Deployment,
		request.Instance,
		request.Index,
		options,
	)

	if err != nil {
		s.logger.Error("Failed to create SSH session: %v", err)
		return nil, NewSSHError(SSHErrorCodeConnection, "Failed to create SSH session", err)
	}

	// Create session wrapper
	session := &SessionImpl{
		id:          fmt.Sprintf("%s-%s-%d-%d", request.Deployment, request.Instance, request.Index, time.Now().Unix()),
		deployment:  request.Deployment,
		instance:    request.Instance,
		index:       request.Index,
		sessionInfo: sessionInfo,
		status:      SessionStatusCreated,
		logger:      s.logger,
	}

	// Add to session pool
	s.sessionMutex.Lock()
	s.sessionPool[session.id] = session
	s.sessionMutex.Unlock()

	s.logger.Info("SSH session created successfully: %s", session.id)

	return session, nil
}

// Close closes the SSH service and cleanup resources
func (s *ServiceImpl) Close() error {
	s.logger.Info("Closing SSH service")

	s.sessionMutex.Lock()
	defer s.sessionMutex.Unlock()

	var errs []error
	for id, session := range s.sessionPool {
		if err := session.Close(); err != nil {
			s.logger.Error("Failed to close session %s: %v", id, err)
			errs = append(errs, err)
		}
	}

	// Clear session pool
	s.sessionPool = make(map[string]SSHSession)

	if len(errs) > 0 {
		return NewSSHError(SSHErrorCodeInternal, "Failed to close some sessions", errs[0])
	}

	s.logger.Info("SSH service closed successfully")
	return nil
}

// validateRequest validates an SSH request
func (s *ServiceImpl) validateRequest(request *SSHRequest) error {
	if request == nil {
		return fmt.Errorf("request cannot be nil")
	}

	if request.Deployment == "" {
		return fmt.Errorf("deployment name is required")
	}

	if request.Instance == "" {
		return fmt.Errorf("instance name is required")
	}

	if request.Index < 0 {
		return fmt.Errorf("instance index must be non-negative")
	}

	return nil
}

// SessionImpl implements the SSHSession interface
type SessionImpl struct {
	id          string
	deployment  string
	instance    string
	index       int
	sessionInfo interface{}
	status      SessionStatus
	statusMutex sync.RWMutex
	logger      Logger
}

// Start starts the SSH session
func (s *SessionImpl) Start() error {
	s.logger.Info("Starting SSH session: %s", s.id)

	s.statusMutex.Lock()
	s.status = SessionStatusConnecting
	s.statusMutex.Unlock()

	// TODO: Implement actual SSH connection establishment
	// For now, just mark as connected
	s.statusMutex.Lock()
	s.status = SessionStatusConnected
	s.statusMutex.Unlock()

	s.logger.Info("SSH session started successfully: %s", s.id)
	return nil
}

// SendInput sends input to the session
func (s *SessionImpl) SendInput(data []byte) error {
	s.logger.Debug("Sending input to session %s: %d bytes", s.id, len(data))

	s.statusMutex.RLock()
	status := s.status
	s.statusMutex.RUnlock()

	if status != SessionStatusConnected && status != SessionStatusActive {
		return NewSSHError(SSHErrorCodeConnection, "Session not connected", nil)
	}

	// TODO: Implement actual input sending
	s.logger.Debug("Input sent to session %s", s.id)
	return nil
}

// ReadOutput reads output from the session
func (s *SessionImpl) ReadOutput() ([]byte, error) {
	s.logger.Debug("Reading output from session: %s", s.id)

	s.statusMutex.RLock()
	status := s.status
	s.statusMutex.RUnlock()

	if status != SessionStatusConnected && status != SessionStatusActive {
		return nil, NewSSHError(SSHErrorCodeConnection, "Session not connected", nil)
	}

	// TODO: Implement actual output reading
	// For now, return empty data
	return []byte{}, nil
}

// Close closes the session
func (s *SessionImpl) Close() error {
	s.logger.Info("Closing SSH session: %s", s.id)

	s.statusMutex.Lock()
	s.status = SessionStatusClosing
	s.statusMutex.Unlock()

	// TODO: Implement actual session cleanup
	// For now, just mark as closed

	s.statusMutex.Lock()
	s.status = SessionStatusClosed
	s.statusMutex.Unlock()

	s.logger.Info("SSH session closed: %s", s.id)
	return nil
}

// Status returns the current session status
func (s *SessionImpl) Status() SessionStatus {
	s.statusMutex.RLock()
	defer s.statusMutex.RUnlock()
	return s.status
}

// SetWindowSize sets the window size for terminal sessions
func (s *SessionImpl) SetWindowSize(width, height int) error {
	s.logger.Debug("Setting window size for session %s: %dx%d", s.id, width, height)

	s.statusMutex.RLock()
	status := s.status
	s.statusMutex.RUnlock()

	if status != SessionStatusConnected && status != SessionStatusActive {
		return NewSSHError(SSHErrorCodeConnection, "Session not connected", nil)
	}

	// TODO: Implement actual window size setting
	s.logger.Debug("Window size set for session %s", s.id)
	return nil
}

// noOpLogger is a no-operation logger implementation
type noOpLogger struct{}

func (l *noOpLogger) Info(format string, args ...interface{})  {}
func (l *noOpLogger) Debug(format string, args ...interface{}) {}
func (l *noOpLogger) Error(format string, args ...interface{}) {}
