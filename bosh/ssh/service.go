package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
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
	Timeout               time.Duration
	ConnectTimeout        time.Duration
	SessionInitTimeout    time.Duration // New: SSH session initialization timeout
	OutputReadTimeout     time.Duration // New: Timeout for reading output
	MaxConcurrent         int
	MaxOutputSize         int
	KeepAlive             time.Duration
	RetryAttempts         int
	RetryDelay            time.Duration
	InsecureIgnoreHostKey bool   // Allow insecure host key verification for BOSH environments
	KnownHostsFile        string // Path to known_hosts file for host key verification
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
		config:      s.config,
		service:     s,
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

// getHostKeyCallback returns the appropriate host key callback based on configuration
func (s *ServiceImpl) getHostKeyCallback() ssh.HostKeyCallback {
	// If insecure mode is explicitly enabled, use it
	if s.config.InsecureIgnoreHostKey {
		s.logger.Info("Using insecure host key verification (not recommended for production)")
		return ssh.InsecureIgnoreHostKey() // #nosec G106 - Intentional insecure mode for BOSH environments
	}

	// Use secure mode with auto-discovery (default behavior)
	return s.autoDiscoveryHostKeyCallback()
}

// autoDiscoveryHostKeyCallback creates a host key callback that automatically adds unknown hosts
func (s *ServiceImpl) autoDiscoveryHostKeyCallback() ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		// First, try to verify against existing known_hosts
		if s.config.KnownHostsFile != "" {
			callback, err := knownhosts.New(s.config.KnownHostsFile)
			if err == nil {
				// If known_hosts file exists and loads successfully, try verification
				err := callback(hostname, remote, key)
				if err == nil {
					// Host key is already known and valid
					s.logger.Debug("SSH host key verified for %s", hostname)
					return nil
				}

				// Check if this is just an unknown host (not a key mismatch)
				var keyErr *knownhosts.KeyError
				if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
					// This is an unknown host (no keys in Want slice)
					s.logger.Info("Unknown SSH host %s, adding to known_hosts", hostname)
					return s.addHostToKnownHosts(hostname, key)
				}

				// This is a key mismatch - reject the connection
				s.logger.Error("SSH host key mismatch for %s: %v", hostname, err)
				return err
			}
			// If we can't load known_hosts, we'll add this host as the first entry
			s.logger.Debug("Could not load known_hosts file, will create with first host: %v", err)
		}

		// Add the host to known_hosts (this handles both new file creation and new host addition)
		s.logger.Info("Adding SSH host %s to known_hosts", hostname)
		return s.addHostToKnownHosts(hostname, key)
	}
}

// addHostToKnownHosts adds a host key to the known_hosts file
func (s *ServiceImpl) addHostToKnownHosts(hostname string, key ssh.PublicKey) error {
	if s.config.KnownHostsFile == "" {
		return fmt.Errorf("no known_hosts file configured")
	}

	// Ensure the directory exists
	dir := filepath.Dir(s.config.KnownHostsFile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create known_hosts directory %s: %w", dir, err)
	}

	// Open the known_hosts file for appending (create if it doesn't exist)
	file, err := os.OpenFile(s.config.KnownHostsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open known_hosts file %s: %w", s.config.KnownHostsFile, err)
	}
	defer file.Close()

	// Format the host key entry (hostname algorithm key)
	keyLine := knownhosts.Line([]string{hostname}, key)

	// Append to the file
	if _, err := file.WriteString(keyLine + "\n"); err != nil {
		return fmt.Errorf("failed to write to known_hosts file: %w", err)
	}

	s.logger.Debug("Added SSH host key for %s to %s", hostname, s.config.KnownHostsFile)
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
	config      Config
	service     *ServiceImpl // Reference to parent service for host key callback

	// SSH connection components
	sshClient  *ssh.Client
	sshSession *ssh.Session
	stdin      io.WriteCloser
	stdout     io.Reader
	stderr     io.Reader

	// Cleanup function from BOSH
	cleanupFunc func() error

	// Channel for output streaming
	outputChan chan []byte
	errorChan  chan error
	closeChan  chan struct{}

	// Keepalive components
	keepAliveDone chan struct{}

	// Ensure Close is only called once
	closeOnce sync.Once
}

// Start starts the SSH session
func (s *SessionImpl) Start() error {
	s.logger.Info("Starting SSH session: %s", s.id)

	// Add timeout context for SSH session initialization using configured timeout
	sessionInitTimeout := s.config.SessionInitTimeout
	if sessionInitTimeout == 0 {
		sessionInitTimeout = 60 * time.Second // Fallback default
	}
	ctx, cancel := context.WithTimeout(context.Background(), sessionInitTimeout)
	defer cancel()

	s.statusMutex.Lock()
	s.status = SessionStatusConnecting
	s.statusMutex.Unlock()

	// Set error status on failure
	defer func() {
		if r := recover(); r != nil {
			s.statusMutex.Lock()
			s.status = SessionStatusError
			s.statusMutex.Unlock()
			panic(r)
		}
	}()

	// Extract session information for SSH setup
	if s.sessionInfo == nil {
		s.statusMutex.Lock()
		s.status = SessionStatusError
		s.statusMutex.Unlock()
		return NewSSHError(SSHErrorCodeInternal, "No session info available", nil)
	}

	sessionMap, ok := s.sessionInfo.(map[string]interface{})
	if !ok {
		s.statusMutex.Lock()
		s.status = SessionStatusError
		s.statusMutex.Unlock()
		return NewSSHError(SSHErrorCodeInternal, "Invalid session info format", nil)
	}

	// Extract SSH connection details
	privateKey, ok := sessionMap["private_key"].(string)
	if !ok {
		s.statusMutex.Lock()
		s.status = SessionStatusError
		s.statusMutex.Unlock()
		return NewSSHError(SSHErrorCodeInternal, "Private key not found in session info", nil)
	}

	hosts, ok := sessionMap["hosts"].([]interface{})
	if !ok || len(hosts) == 0 {
		s.statusMutex.Lock()
		s.status = SessionStatusError
		s.statusMutex.Unlock()
		return NewSSHError(SSHErrorCodeInternal, "No hosts available for SSH connection", nil)
	}

	// Extract the first host information
	host := hosts[0].(map[string]interface{})
	hostAddress := host["Host"].(string)
	username := host["Username"].(string)

	// Parse the private key
	signer, err := ssh.ParsePrivateKey([]byte(privateKey))
	if err != nil {
		s.statusMutex.Lock()
		s.status = SessionStatusError
		s.statusMutex.Unlock()
		return NewSSHError(SSHErrorCodeConnection, "Failed to parse private key", err)
	}

	// Create SSH client configuration with context timeout
	config := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: s.service.getHostKeyCallback(),
		Timeout:         30 * time.Second,
	}

	// Handle gateway connection if present
	gatewayHost, hasGateway := sessionMap["gateway_host"].(string)
	if hasGateway && gatewayHost != "" {
		gatewayUser := sessionMap["gateway_user"].(string)

		// Connect to gateway first
		gatewayAddr := fmt.Sprintf("%s:22", gatewayHost)
		gatewayConfig := &ssh.ClientConfig{
			User: gatewayUser,
			Auth: []ssh.AuthMethod{
				ssh.PublicKeys(signer),
			},
			HostKeyCallback: s.service.getHostKeyCallback(),
			Timeout:         30 * time.Second,
		}

		// Check context timeout
		select {
		case <-ctx.Done():
			s.statusMutex.Lock()
			s.status = SessionStatusError
			s.statusMutex.Unlock()
			return NewSSHError(SSHErrorCodeConnection, "SSH session initialization timeout", ctx.Err())
		default:
		}

		gatewayClient, err := ssh.Dial("tcp", gatewayAddr, gatewayConfig)
		if err != nil {
			s.statusMutex.Lock()
			s.status = SessionStatusError
			s.statusMutex.Unlock()
			return NewSSHError(SSHErrorCodeConnection, fmt.Sprintf("Failed to connect to gateway %s", gatewayAddr), err)
		}

		// Connect to target host through gateway
		targetAddr := fmt.Sprintf("%s:22", hostAddress)
		conn, err := gatewayClient.Dial("tcp", targetAddr)
		if err != nil {
			if err := gatewayClient.Close(); err != nil {
				s.logger.Error("Failed to close gateway client: %v", err)
			}
			s.statusMutex.Lock()
			s.status = SessionStatusError
			s.statusMutex.Unlock()
			return NewSSHError(SSHErrorCodeConnection, fmt.Sprintf("Failed to connect to target host %s through gateway", targetAddr), err)
		}

		nconn, chans, reqs, err := ssh.NewClientConn(conn, targetAddr, config)
		if err != nil {
			if err := gatewayClient.Close(); err != nil {
				s.logger.Error("Failed to close gateway client: %v", err)
			}
			s.statusMutex.Lock()
			s.status = SessionStatusError
			s.statusMutex.Unlock()
			return NewSSHError(SSHErrorCodeConnection, "Failed to establish SSH connection through gateway", err)
		}
		s.sshClient = ssh.NewClient(nconn, chans, reqs)
	} else {
		// Check context timeout
		select {
		case <-ctx.Done():
			s.statusMutex.Lock()
			s.status = SessionStatusError
			s.statusMutex.Unlock()
			return NewSSHError(SSHErrorCodeConnection, "SSH session initialization timeout", ctx.Err())
		default:
		}

		// Direct connection
		addr := fmt.Sprintf("%s:22", hostAddress)
		client, err := ssh.Dial("tcp", addr, config)
		if err != nil {
			s.statusMutex.Lock()
			s.status = SessionStatusError
			s.statusMutex.Unlock()
			return NewSSHError(SSHErrorCodeConnection, fmt.Sprintf("Failed to connect to %s", addr), err)
		}
		s.sshClient = client
	}

	// Create SSH session
	session, err := s.sshClient.NewSession()
	if err != nil {
		if err := s.sshClient.Close(); err != nil {
			s.logger.Error("Failed to close SSH client: %v", err)
		}
		s.statusMutex.Lock()
		s.status = SessionStatusError
		s.statusMutex.Unlock()
		return NewSSHError(SSHErrorCodeConnection, "Failed to create SSH session", err)
	}
	s.sshSession = session

	// Set up PTY
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,     // enable echoing
		ssh.TTY_OP_ISPEED: 14400, // input speed = 14.4kbaud
		ssh.TTY_OP_OSPEED: 14400, // output speed = 14.4kbaud
	}

	if err := session.RequestPty("xterm-256color", 80, 24, modes); err != nil {
		if err := session.Close(); err != nil {
			s.logger.Error("Failed to close SSH session: %v", err)
		}
		if err := s.sshClient.Close(); err != nil {
			s.logger.Error("Failed to close SSH client: %v", err)
		}
		s.statusMutex.Lock()
		s.status = SessionStatusError
		s.statusMutex.Unlock()
		return NewSSHError(SSHErrorCodeConnection, "Failed to request PTY", err)
	}

	// Set up stdin pipe
	stdin, err := session.StdinPipe()
	if err != nil {
		if err := session.Close(); err != nil {
			s.logger.Error("Failed to close SSH session: %v", err)
		}
		if err := s.sshClient.Close(); err != nil {
			s.logger.Error("Failed to close SSH client: %v", err)
		}
		s.statusMutex.Lock()
		s.status = SessionStatusError
		s.statusMutex.Unlock()
		return NewSSHError(SSHErrorCodeConnection, "Failed to create stdin pipe", err)
	}
	s.stdin = stdin

	// Set up stdout pipe
	stdout, err := session.StdoutPipe()
	if err != nil {
		if err := session.Close(); err != nil {
			s.logger.Error("Failed to close SSH session: %v", err)
		}
		if err := s.sshClient.Close(); err != nil {
			s.logger.Error("Failed to close SSH client: %v", err)
		}
		s.statusMutex.Lock()
		s.status = SessionStatusError
		s.statusMutex.Unlock()
		return NewSSHError(SSHErrorCodeConnection, "Failed to create stdout pipe", err)
	}
	s.stdout = stdout

	// Set up stderr pipe
	stderr, err := session.StderrPipe()
	if err != nil {
		if err := session.Close(); err != nil {
			s.logger.Error("Failed to close SSH session: %v", err)
		}
		if err := s.sshClient.Close(); err != nil {
			s.logger.Error("Failed to close SSH client: %v", err)
		}
		s.statusMutex.Lock()
		s.status = SessionStatusError
		s.statusMutex.Unlock()
		return NewSSHError(SSHErrorCodeConnection, "Failed to create stderr pipe", err)
	}
	s.stderr = stderr

	// Initialize channels
	s.outputChan = make(chan []byte, 100)
	s.errorChan = make(chan error, 10)
	s.closeChan = make(chan struct{})
	s.keepAliveDone = make(chan struct{})

	// Store cleanup function
	if cleanupFunc, ok := sessionMap["cleanup_func"].(func() error); ok {
		s.cleanupFunc = cleanupFunc
	}

	// Start shell
	if err := session.Shell(); err != nil {
		if err := session.Close(); err != nil {
			s.logger.Error("Failed to close SSH session: %v", err)
		}
		if err := s.sshClient.Close(); err != nil {
			s.logger.Error("Failed to close SSH client: %v", err)
		}
		s.statusMutex.Lock()
		s.status = SessionStatusError
		s.statusMutex.Unlock()
		return NewSSHError(SSHErrorCodeConnection, "Failed to start shell", err)
	}

	// Start output readers
	go s.readOutput()

	// Start keepalive if configured
	if s.config.KeepAlive > 0 {
		go s.keepAlive()
	}

	s.statusMutex.Lock()
	s.status = SessionStatusConnected
	s.statusMutex.Unlock()

	s.logger.Info("SSH session started successfully: %s", s.id)
	return nil
}

// SendInput sends input to the session
func (s *SessionImpl) SendInput(data []byte) error {
	s.statusMutex.RLock()
	status := s.status
	s.statusMutex.RUnlock()

	switch status {
	case SessionStatusConnecting:
		// Don't accept input during connection phase
		return NewSSHError(SSHErrorCodeConnection, "SSH session is still connecting, please wait", nil)
	case SessionStatusError:
		return NewSSHError(SSHErrorCodeConnection, "SSH session failed during initialization", nil)
	case SessionStatusClosed, SessionStatusClosing:
		return NewSSHError(SSHErrorCodeConnection, "Session closed", nil)
	case SessionStatusCreated:
		return NewSSHError(SSHErrorCodeConnection, "SSH session not yet started", nil)
	case SessionStatusConnected, SessionStatusActive:
		// Continue normal operation
	default:
		return NewSSHError(SSHErrorCodeConnection, "SSH session in invalid state", nil)
	}

	if s.stdin == nil {
		return NewSSHError(SSHErrorCodeConnection, "SSH stdin not available", nil)
	}

	// Mark session as active on first input
	if status == SessionStatusConnected {
		s.statusMutex.Lock()
		s.status = SessionStatusActive
		s.statusMutex.Unlock()
	}

	// Write input to SSH session
	_, err := s.stdin.Write(data)
	if err != nil {
		return NewSSHError(SSHErrorCodeConnection, "Failed to write to SSH session", err)
	}

	return nil
}

// ReadOutput reads output from the session
func (s *SessionImpl) ReadOutput() ([]byte, error) {
	s.statusMutex.RLock()
	status := s.status
	s.statusMutex.RUnlock()

	switch status {
	case SessionStatusConnecting:
		// Allow reading during connection phase but return empty data
		// This prevents "Session not connected" errors during initialization
		return []byte{}, nil
	case SessionStatusError:
		return nil, NewSSHError(SSHErrorCodeConnection, "SSH session failed during initialization", nil)
	case SessionStatusClosed, SessionStatusClosing:
		return nil, NewSSHError(SSHErrorCodeConnection, "Session closed", nil)
	case SessionStatusConnected, SessionStatusActive:
		// Continue normal operation
	case SessionStatusCreated:
		// Wait for connection to establish
		return []byte{}, nil
	default:
		return []byte{}, nil // Wait for proper state
	}

	// Read from output channel with configurable timeout for better stability
	outputTimeout := s.config.OutputReadTimeout
	if outputTimeout == 0 {
		outputTimeout = 2 * time.Second // Fallback default - increased from 100ms to 2s for better stability
	}

	select {
	case output := <-s.outputChan:
		return output, nil
	case err := <-s.errorChan:
		return nil, err
	case <-s.closeChan:
		return nil, NewSSHError(SSHErrorCodeConnection, "Session closed", nil)
	case <-time.After(outputTimeout):
		return []byte{}, nil
	}
}

// readOutput continuously reads from stdout and stderr
func (s *SessionImpl) readOutput() {
	var wg sync.WaitGroup
	wg.Add(2)

	// Read stdout
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			select {
			case <-s.closeChan:
				return
			default:
				n, err := s.stdout.Read(buf)
				if err != nil {
					if err != io.EOF {
						s.errorChan <- err
					}
					return
				}
				if n > 0 {
					// Send a copy of the data
					output := make([]byte, n)
					copy(output, buf[:n])
					select {
					case s.outputChan <- output:
					case <-s.closeChan:
						return
					}
				}
			}
		}
	}()

	// Read stderr
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			select {
			case <-s.closeChan:
				return
			default:
				n, err := s.stderr.Read(buf)
				if err != nil {
					if err != io.EOF {
						s.errorChan <- err
					}
					return
				}
				if n > 0 {
					// Send a copy of the data
					output := make([]byte, n)
					copy(output, buf[:n])
					select {
					case s.outputChan <- output:
					case <-s.closeChan:
						return
					}
				}
			}
		}
	}()

	wg.Wait()
	close(s.outputChan)
}

// keepAlive sends periodic SSH keepalive packets to prevent connection timeout
func (s *SessionImpl) keepAlive() {
	s.logger.Debug("Starting SSH keepalive for session: %s (interval: %v)", s.id, s.config.KeepAlive)

	ticker := time.NewTicker(s.config.KeepAlive)
	defer ticker.Stop()

	for {
		select {
		case <-s.keepAliveDone:
			s.logger.Debug("SSH keepalive stopped for session: %s", s.id)
			return
		case <-s.closeChan:
			s.logger.Debug("SSH keepalive stopped due to session closure: %s", s.id)
			return
		case <-ticker.C:
			if s.sshClient != nil {
				// Send SSH keepalive packet
				_, _, err := s.sshClient.SendRequest("keepalive@openssh.com", true, nil)
				if err != nil {
					s.logger.Debug("Failed to send SSH keepalive for session %s: %v", s.id, err)
					// Don't return on error, just continue trying
				} else {
					s.logger.Debug("Sent SSH keepalive for session: %s", s.id)
				}
			}
		}
	}
}

// Close closes the session
func (s *SessionImpl) Close() error {
	var closeErr error

	s.closeOnce.Do(func() {
		s.logger.Info("Closing SSH session: %s", s.id)

		s.statusMutex.Lock()
		s.status = SessionStatusClosing
		s.statusMutex.Unlock()

		// Signal output readers and keepalive to stop
		if s.closeChan != nil {
			close(s.closeChan)
		}
		if s.keepAliveDone != nil {
			close(s.keepAliveDone)
		}

		// Close SSH session
		if s.sshSession != nil {
			if err := s.sshSession.Close(); err != nil {
				s.logger.Error("Failed to close SSH session: %v", err)
			}
		}

		// Close SSH client
		if s.sshClient != nil {
			if err := s.sshClient.Close(); err != nil {
				s.logger.Error("Failed to close SSH client: %v", err)
			}
		}

		// Call BOSH cleanup function
		if s.cleanupFunc != nil {
			if err := s.cleanupFunc(); err != nil {
				s.logger.Error("Failed to cleanup BOSH SSH: %v", err)
			}
		}

		s.statusMutex.Lock()
		s.status = SessionStatusClosed
		s.statusMutex.Unlock()

		s.logger.Info("SSH session closed: %s", s.id)
	})

	return closeErr
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

	if s.sshSession == nil {
		return NewSSHError(SSHErrorCodeConnection, "SSH session not available", nil)
	}

	// Send window change request
	if err := s.sshSession.WindowChange(height, width); err != nil {
		return NewSSHError(SSHErrorCodeConnection, "Failed to change window size", err)
	}

	s.logger.Debug("Window size set for session %s", s.id)
	return nil
}

// noOpLogger is a no-operation logger implementation
type noOpLogger struct{}

func (l *noOpLogger) Info(format string, args ...interface{})  {}
func (l *noOpLogger) Debug(format string, args ...interface{}) {}
func (l *noOpLogger) Error(format string, args ...interface{}) {}
