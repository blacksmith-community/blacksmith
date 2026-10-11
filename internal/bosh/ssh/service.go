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

	"blacksmith/internal/bosh/sshconn"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Constants for SSH service.
const (
	// File permissions.
	dirPermissions  = 0700
	filePermissions = 0600

	// Terminal settings.
	terminalBaudRate = 14400 // 14.4kbaud
	terminalColumns  = 80
	terminalRows     = 24

	// Channel buffer sizes.
	outputChannelBuffer = 100

	// Number of goroutines for reading output pipes.
	outputPipeGoroutines = 2

	// SSH connection timeout.
	sshConnectionTimeout = 30 * time.Second
	errorChannelBuffer   = 10

	// Timeouts.
	defaultSessionInitTimeout = 60 * time.Second
	defaultConnectTimeout     = 30 * time.Second
	defaultOutputTimeout      = 2 * time.Second

	// I/O buffer size.
	ioBufferSize = 4096

	// sshPort is the port that sshd listens on, on every BOSH VM.
	sshPort = "22"
)

// Static error variables to satisfy err113.
var (
	ErrRequestCannotBeNil           = errors.New("request cannot be nil")
	ErrDeploymentNameRequired       = errors.New("deployment name is required")
	ErrInstanceNameRequired         = errors.New("instance name is required")
	ErrInstanceIndexNonNegative     = errors.New("instance index must be non-negative")
	ErrNoKnownHostsFileConfigured   = errors.New("no known_hosts file configured")
	ErrUnexpectedTypeForHost        = errors.New("unexpected type for host")
	ErrUnexpectedTypeForHostAddress = errors.New("unexpected type for host address")
	ErrUnexpectedTypeForUsername    = errors.New("unexpected type for username")
	ErrGatewayUserMustBeString      = errors.New("gateway_user must be a string")
)

// ServiceImpl implements the SSHService interface.
type ServiceImpl struct {
	director     DirectorInterface
	config       Config
	sessionPool  map[string]SSHSession
	sessionMutex sync.RWMutex
	logger       Logger

	// port is the port the terminal connects to on a VM and on a
	// gateway. It is sshPort everywhere but in tests, which point it at
	// a loopback server.
	port string
}

// DirectorInterface defines the methods we need from the BOSH director.
type DirectorInterface interface {
	SSHCommand(ctx context.Context, deployment, instance string, index int, command string, args []string, options map[string]interface{}) (string, error)
	SSHSession(ctx context.Context, deployment, instance string, index int, options map[string]interface{}) (interface{}, error)
}

// Config holds configuration for the SSH service.
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

// Logger interface for logging.
type Logger interface {
	Infof(format string, args ...interface{})
	Debugf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

// NewSSHService creates a new SSH service instance.
func NewSSHService(director DirectorInterface, config Config, logger Logger) *ServiceImpl {
	if logger == nil {
		logger = &noOpLogger{}
	}

	return &ServiceImpl{
		director:     director,
		config:       config,
		sessionPool:  make(map[string]SSHSession),
		sessionMutex: sync.RWMutex{},
		logger:       logger,
		port:         sshPort,
	}
}

// createSSHResponse creates an SSH response based on execution results.
func createSSHResponse(success bool, exitCode int, duration time.Duration, output string, err error, startTime time.Time) *SSHResponse {
	response := &SSHResponse{
		Success:   success,
		ExitCode:  exitCode,
		Duration:  duration.Milliseconds(),
		Timestamp: startTime,
		RequestID: "",
	}

	if success {
		response.Stdout = output
		response.Stderr = ""
		response.Error = ""
	} else {
		response.Stdout = ""
		response.Stderr = ""
		response.Error = err.Error()
	}

	return response
}

// ExecuteCommand executes a one-off command on a BOSH VM.
func (s *ServiceImpl) ExecuteCommand(ctx context.Context, request *SSHRequest) (*SSHResponse, error) {
	s.logger.Infof("Executing SSH command on %s/%s/%d", request.Deployment, request.Instance, request.Index)
	s.logger.Debugf("Command: %s %v", request.Command, request.Args)

	startTime := time.Now()

	err := s.validateRequest(request)
	if err != nil {
		return nil, NewSSHError(SSHErrorCodeInvalidRequest, "Invalid SSH request", err)
	}

	options := s.prepareSSHCommandOptions(request)

	output, err := s.director.SSHCommand(
		ctx,
		request.Deployment,
		request.Instance,
		request.Index,
		request.Command,
		request.Args,
		options,
	)

	duration := time.Since(startTime)

	if err != nil {
		s.logger.Errorf("SSH command failed: %v", err)

		return createSSHResponse(false, 1, duration, "", err, startTime), nil
	}

	s.logger.Infof("SSH command completed successfully in %v", duration)

	return createSSHResponse(true, 0, duration, output, nil, startTime), nil
}

// CreateSession creates an interactive SSH session for streaming
//
//nolint:ireturn // Interface return is intentional - part of SSHService interface contract
func (s *ServiceImpl) CreateSession(ctx context.Context, request *SSHRequest) (SSHSession, error) {
	s.logger.Infof("Creating SSH session for %s/%s/%d", request.Deployment, request.Instance, request.Index)

	err := s.validateRequest(request)
	if err != nil {
		return nil, NewSSHError(SSHErrorCodeInvalidRequest, "Invalid SSH request", err)
	}

	err = s.checkSessionLimit()
	if err != nil {
		return nil, err
	}

	options := s.prepareSSHSessionOptions(request)

	sessionInfo, err := s.director.SSHSession(ctx, request.Deployment, request.Instance, request.Index, options)
	if err != nil {
		s.logger.Errorf("Failed to create SSH session: %v", err)

		return nil, NewSSHError(SSHErrorCodeConnection, "Failed to create SSH session", err)
	}

	session := s.createSessionImpl(request, sessionInfo)
	s.addSessionToPool(session)

	s.logger.Infof("SSH session created successfully: %s", session.id)

	return session, nil
}

// Close closes every open session, which removes each session's user,
// and empties the pool. The pool is taken under the lock and the
// sessions are closed after it is released, because each session's
// Close removes it from the pool under the same lock.
func (s *ServiceImpl) Close() error {
	s.logger.Infof("Closing SSH service")

	s.sessionMutex.Lock()
	sessions := s.sessionPool
	s.sessionPool = make(map[string]SSHSession)
	s.sessionMutex.Unlock()

	var errs []error

	for id, session := range sessions {
		err := session.Close()
		if err != nil {
			s.logger.Errorf("Failed to close session %s: %v", id, err)
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return NewSSHError(SSHErrorCodeInternal, "Failed to close some sessions", errs[0])
	}

	s.logger.Infof("SSH service closed successfully")

	return nil
}

// IsSSHService implements the interfaces.SSHService interface.
func (s *ServiceImpl) IsSSHService() bool {
	return true
}

func (s *ServiceImpl) prepareSSHCommandOptions(request *SSHRequest) map[string]interface{} {
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

	return options
}

func (s *ServiceImpl) checkSessionLimit() error {
	s.sessionMutex.RLock()
	currentSessions := len(s.sessionPool)
	s.sessionMutex.RUnlock()

	if currentSessions >= s.config.MaxConcurrent {
		return NewSSHError(SSHErrorCodeInternal,
			fmt.Sprintf("Maximum concurrent sessions (%d) reached", s.config.MaxConcurrent), nil)
	}

	return nil
}

func (s *ServiceImpl) prepareSSHSessionOptions(request *SSHRequest) map[string]interface{} {
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

	return options
}

// createSessionImpl builds the session for a director's SSH setup. The
// director's cleanup is copied into the session here rather than in
// Start, because the setup has already created the user on the VM, so a
// session whose Start never runs, or fails, must still remove it when it
// closes.
func (s *ServiceImpl) createSessionImpl(request *SSHRequest, sessionInfo interface{}) *SessionImpl {
	// A random ID keeps two sessions on one instance apart in the pool,
	// however close together they start.
	sessionID := uuid.NewString()

	cleanupFunc := directorCleanup(sessionInfo)
	if cleanupFunc == nil {
		s.logger.Errorf("The director's SSH setup for session %s carries no cleanup, so the SSH user it created on %s/%s/%d stays on the VM after the session closes",
			sessionID, request.Deployment, request.Instance, request.Index)
	}

	return &SessionImpl{
		id:            sessionID,
		deployment:    request.Deployment,
		instance:      request.Instance,
		index:         request.Index,
		sessionInfo:   sessionInfo,
		status:        SessionStatusCreated,
		statusMutex:   sync.RWMutex{},
		logger:        s.logger,
		config:        s.config,
		service:       s,
		sshClient:     nil,
		sshSession:    nil,
		stdin:         nil,
		stdout:        nil,
		stderr:        nil,
		cleanupFunc:   cleanupFunc,
		outputChan:    nil,
		errorChan:     nil,
		closeChan:     nil,
		keepAliveDone: nil,
		closeOnce:     sync.Once{},
	}
}

// directorCleanup returns the cleanup that the director adapter puts in
// its SSH session info, or nil when the info carries none.
func directorCleanup(sessionInfo interface{}) func() error {
	sessionMap, isMap := sessionInfo.(map[string]interface{})
	if !isMap {
		return nil
	}

	cleanupFunc, hasCleanup := sessionMap["cleanup_func"].(func() error)
	if !hasCleanup {
		return nil
	}

	return cleanupFunc
}

func (s *ServiceImpl) addSessionToPool(session *SessionImpl) {
	s.sessionMutex.Lock()
	s.sessionPool[session.id] = session
	s.sessionMutex.Unlock()
}

// removeSessionFromPool drops a closed session, so the session limit
// counts only open sessions.
func (s *ServiceImpl) removeSessionFromPool(id string) {
	s.sessionMutex.Lock()
	delete(s.sessionPool, id)
	s.sessionMutex.Unlock()
}

// validateRequest validates an SSH request.
func (s *ServiceImpl) validateRequest(request *SSHRequest) error {
	if request == nil {
		return ErrRequestCannotBeNil
	}

	if request.Deployment == "" {
		return ErrDeploymentNameRequired
	}

	if request.Instance == "" {
		return ErrInstanceNameRequired
	}

	if request.Index < 0 {
		return ErrInstanceIndexNonNegative
	}

	return nil
}

// getHostKeyCallback returns the appropriate host key callback based on configuration.
func (s *ServiceImpl) getHostKeyCallback() ssh.HostKeyCallback {
	// If insecure mode is explicitly enabled, use it
	if s.config.InsecureIgnoreHostKey {
		s.logger.Infof("Using insecure host key verification (not recommended for production)")

		return ssh.InsecureIgnoreHostKey() // #nosec G106 - Intentional insecure mode for BOSH environments
	}

	// Use secure mode with auto-discovery (default behavior)
	return s.autoDiscoveryHostKeyCallback()
}

// autoDiscoveryHostKeyCallback creates a host key callback that automatically adds unknown hosts.
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
					s.logger.Debugf("SSH host key verified for %s", hostname)

					return nil
				}

				// Check if this is just an unknown host (not a key mismatch)
				var keyErr *knownhosts.KeyError
				if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
					// This is an unknown host (no keys in Want slice)
					s.logger.Infof("Unknown SSH host %s, adding to known_hosts", hostname)

					return s.addHostToKnownHosts(hostname, key)
				}

				// This is a key mismatch - reject the connection
				s.logger.Errorf("SSH host key mismatch for %s: %v", hostname, err)

				return err
			}
			// If we can't load known_hosts, we'll add this host as the first entry
			s.logger.Debugf("Could not load known_hosts file, will create with first host: %v", err)
		}

		// Add the host to known_hosts (this handles both new file creation and new host addition)
		s.logger.Infof("Adding SSH host %s to known_hosts", hostname)

		return s.addHostToKnownHosts(hostname, key)
	}
}

// addHostToKnownHosts adds a host key to the known_hosts file.
func (s *ServiceImpl) addHostToKnownHosts(hostname string, key ssh.PublicKey) error {
	if s.config.KnownHostsFile == "" {
		return ErrNoKnownHostsFileConfigured
	}

	// Ensure the directory exists
	dir := filepath.Dir(s.config.KnownHostsFile)

	err := os.MkdirAll(dir, dirPermissions)
	if err != nil {
		return fmt.Errorf("failed to create known_hosts directory %s: %w", dir, err)
	}

	// Open the known_hosts file for appending (create if it doesn't exist)
	file, err := os.OpenFile(s.config.KnownHostsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePermissions)
	if err != nil {
		return fmt.Errorf("failed to open known_hosts file %s: %w", s.config.KnownHostsFile, err)
	}

	defer func() { _ = file.Close() }()

	// Format the host key entry (hostname algorithm key)
	keyLine := knownhosts.Line([]string{hostname}, key)

	// Append to the file
	_, err = file.WriteString(keyLine + "\n")
	if err != nil {
		return fmt.Errorf("failed to write to known_hosts file: %w", err)
	}

	s.logger.Debugf("Added SSH host key for %s to %s", hostname, s.config.KnownHostsFile)

	return nil
}

// SessionImpl implements the SSHSession interface.
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

	// cleanupFunc removes the SSH user that the director's setup created.
	// Close calls it once, after closing the connection.
	cleanupFunc func() error

	// Channel for output streaming
	outputChan chan []byte
	errorChan  chan error
	closeChan  chan struct{}

	// Keepalive components
	keepAliveDone chan struct{}

	// Ensure Close is only called once
	closeOnce sync.Once

	// connMutex guards closed and cancelStart, and the connection fields
	// above while Start publishes them and Close takes them. Start builds
	// its connection in locals and publishes it only when Close has not
	// run, so a Close that lands while Start runs never misses it.
	connMutex   sync.Mutex
	closed      bool
	cancelStart context.CancelFunc
}

// Start connects to the VM and starts a shell. A Close while Start runs
// cancels it, and Start then closes whatever it opened rather than
// publishing it, so the connection never outlives the session.
func (s *SessionImpl) Start() error {
	s.logger.Infof("Starting SSH session: %s", s.id)

	// Set up timeout context
	ctx, cancel := s.createTimeoutContext()
	defer cancel()

	if !s.beginStart(cancel) {
		return NewSSHError(SSHErrorCodeConnection, "SSH session "+s.id+" was closed before it started", nil)
	}

	// Set error status on failure
	defer s.handlePanicAndSetErrorStatus()

	// Extract and validate session information
	sessionDetails, err := s.extractSessionDetails()
	if err != nil {
		s.failStart()

		return err
	}

	client, err := s.establishSSHConnection(ctx, sessionDetails)
	if err != nil {
		s.failStart()

		return err
	}

	shell, err := s.openShell(ctx, client)
	if err != nil {
		s.discardConnection(nil, client)
		s.failStart()

		return err
	}

	if !s.publishConnection(client, shell) {
		s.discardConnection(shell, client)
		s.failStart()

		return NewSSHError(SSHErrorCodeConnection, "SSH session "+s.id+" was closed while it started", nil)
	}

	s.logger.Infof("SSH session started successfully: %s", s.id)

	return nil
}

// SendInput sends input to the session.
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

// ReadOutput reads output from the session.
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
		outputTimeout = defaultOutputTimeout // Fallback default - increased from 100ms to 2s for better stability
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

// SetWindowSize sets the window size for terminal sessions.
func (s *SessionImpl) SetWindowSize(width, height int) error {
	s.logger.Debugf("Setting window size for session %s: %dx%d", s.id, width, height)

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
	err := s.sshSession.WindowChange(height, width)
	if err != nil {
		return NewSSHError(SSHErrorCodeConnection, "Failed to change window size", err)
	}

	s.logger.Debugf("Window size set for session %s", s.id)

	return nil
}

// Close closes the session.
func (s *SessionImpl) Close() error {
	var closeErr error

	s.closeOnce.Do(func() {
		s.logger.Infof("Closing SSH session: %s", s.id)

		// The session leaves the pool before anything that can block or
		// fail, and ends closed even when its cleanup panics, because
		// closeOnce counts a panicking close as done and no later Close
		// would prune it.
		if s.service != nil {
			s.service.removeSessionFromPool(s.id)
		}

		defer s.setStatus(SessionStatusClosed)

		conn := s.takeConnection()

		// Signal output readers and keepalive to stop
		if conn.closeChan != nil {
			close(conn.closeChan)
		}

		if conn.keepAliveDone != nil {
			close(conn.keepAliveDone)
		}

		if conn.session != nil {
			s.closeQuietly("SSH session", conn.session)
		}

		if conn.client != nil {
			s.closeQuietly("SSH client", conn.client)
		}

		// Remove the SSH user that the director's setup created.
		if s.cleanupFunc != nil {
			err := s.cleanupFunc()
			if err != nil {
				s.logger.Errorf("Failed to remove the SSH user for session %s: %v", s.id, err)

				closeErr = NewSSHError(SSHErrorCodeInternal, "Failed to remove the SSH user for session "+s.id, err)
			}
		}

		s.logger.Infof("SSH session closed: %s", s.id)
	})

	return closeErr
}

// Status returns the current session status.
func (s *SessionImpl) Status() SessionStatus {
	s.statusMutex.RLock()
	defer s.statusMutex.RUnlock()

	return s.status
}

// beginStart marks the session connecting and keeps the cancel of the
// Start's context, so a Close can end the Start. It returns false when the
// session has already closed.
func (s *SessionImpl) beginStart(cancel context.CancelFunc) bool {
	s.connMutex.Lock()
	defer s.connMutex.Unlock()

	if s.closed {
		return false
	}

	s.cancelStart = cancel
	s.setStatus(SessionStatusConnecting)

	return true
}

// publishConnection makes a started connection the session's own and
// starts reading its output. When Close has already run, it publishes
// nothing and returns false, and the caller closes the connection.
func (s *SessionImpl) publishConnection(client *ssh.Client, shell *shellConn) bool {
	s.connMutex.Lock()
	defer s.connMutex.Unlock()

	if s.closed {
		return false
	}

	s.sshClient = client
	s.sshSession = shell.session
	s.stdin = shell.stdin
	s.stdout = shell.stdout
	s.stderr = shell.stderr

	s.initializeSessionChannels()
	s.startBackgroundProcesses()
	s.setStatus(SessionStatusConnected)

	return true
}

// failStart marks a failed Start, unless a Close has already marked the
// session closing or closed.
func (s *SessionImpl) failStart() {
	s.statusMutex.Lock()
	defer s.statusMutex.Unlock()

	if s.status == SessionStatusClosing || s.status == SessionStatusClosed {
		return
	}

	s.status = SessionStatusError
}

// openConnection is the part of a session's connection that Close
// closes.
type openConnection struct {
	client        *ssh.Client
	session       *ssh.Session
	closeChan     chan struct{}
	keepAliveDone chan struct{}
}

// takeConnection marks the session closing, ends a Start that is still
// running, and returns the connection that Start published, if any.
func (s *SessionImpl) takeConnection() openConnection {
	s.connMutex.Lock()
	defer s.connMutex.Unlock()

	s.closed = true

	if s.cancelStart != nil {
		s.cancelStart()
	}

	s.setStatus(SessionStatusClosing)

	return openConnection{
		client:        s.sshClient,
		session:       s.sshSession,
		closeChan:     s.closeChan,
		keepAliveDone: s.keepAliveDone,
	}
}

// createTimeoutContext creates a timeout context for session initialization.
func (s *SessionImpl) createTimeoutContext() (context.Context, context.CancelFunc) {
	sessionInitTimeout := s.config.SessionInitTimeout
	if sessionInitTimeout == 0 {
		sessionInitTimeout = defaultSessionInitTimeout
	}

	return context.WithTimeout(context.Background(), sessionInitTimeout)
}

// setStatus safely sets the session status.
func (s *SessionImpl) setStatus(status SessionStatus) {
	s.statusMutex.Lock()
	s.status = status
	s.statusMutex.Unlock()
}

// handlePanicAndSetErrorStatus handles panics and sets error status.
func (s *SessionImpl) handlePanicAndSetErrorStatus() {
	if r := recover(); r != nil {
		s.failStart()
		panic(r)
	}
}

// extractSessionDetails extracts and validates session connection details.
func (s *SessionImpl) extractSessionDetails() (*sessionConnectionDetails, error) {
	if s.sessionInfo == nil {
		return nil, NewSSHError(SSHErrorCodeInternal, "No session info available", nil)
	}

	sessionMap, ok := s.sessionInfo.(map[string]interface{})
	if !ok {
		return nil, NewSSHError(SSHErrorCodeInternal, "Invalid session info format", nil)
	}

	details := &sessionConnectionDetails{}

	// Extract private key
	privateKey, exists := sessionMap["private_key"].(string)
	if !exists {
		return nil, NewSSHError(SSHErrorCodeInternal, "Private key not found in session info", nil)
	}

	details.privateKey = privateKey

	// Extract and validate hosts
	hosts, exists := sessionMap["hosts"].([]interface{})
	if !exists || len(hosts) == 0 {
		return nil, NewSSHError(SSHErrorCodeInternal, "No hosts available for SSH connection", nil)
	}

	hostInfo, err := s.extractHostInfo(hosts[0])
	if err != nil {
		return nil, err
	}

	details.host = hostInfo

	// Extract gateway information if present
	if gatewayHost, hasGateway := sessionMap["gateway_host"].(string); hasGateway && gatewayHost != "" {
		gatewayUser, ok := sessionMap["gateway_user"].(string)
		if !ok {
			return nil, ErrGatewayUserMustBeString
		}

		details.gateway = &gatewayInfo{
			host: gatewayHost,
			user: gatewayUser,
		}
	}

	return details, nil
}

// sessionConnectionDetails holds SSH connection details.
type sessionConnectionDetails struct {
	privateKey string
	host       *hostInfo
	gateway    *gatewayInfo
}

// hostInfo holds host connection details.
type hostInfo struct {
	address  string
	username string
}

// gatewayInfo holds gateway connection details.
type gatewayInfo struct {
	host string
	user string
}

// extractHostInfo extracts host information from the hosts array.
func (s *SessionImpl) extractHostInfo(hostData interface{}) (*hostInfo, error) {
	hostMap, isValid := hostData.(map[string]interface{})
	if !isValid {
		return nil, NewSSHError(SSHErrorCodeInternal, "Invalid host type", fmt.Errorf("%w: %T", ErrUnexpectedTypeForHost, hostData))
	}

	hostAddressRaw, exists := hostMap["Host"]
	if !exists {
		return nil, NewSSHError(SSHErrorCodeInternal, "Host address not found", nil)
	}

	hostAddress, exists := hostAddressRaw.(string)
	if !exists {
		return nil, NewSSHError(SSHErrorCodeInternal, "Invalid host address type", fmt.Errorf("%w: %T", ErrUnexpectedTypeForHostAddress, hostAddressRaw))
	}

	usernameRaw, exists := hostMap["Username"]
	if !exists {
		return nil, NewSSHError(SSHErrorCodeInternal, "Username not found", nil)
	}

	username, exists := usernameRaw.(string)
	if !exists {
		return nil, NewSSHError(SSHErrorCodeInternal, "Invalid username type", fmt.Errorf("%w: %T", ErrUnexpectedTypeForUsername, usernameRaw))
	}

	return &hostInfo{
		address:  hostAddress,
		username: username,
	}, nil
}

// establishSSHConnection establishes the SSH connection (direct or through gateway).
func (s *SessionImpl) establishSSHConnection(ctx context.Context, details *sessionConnectionDetails) (*ssh.Client, error) {
	// Parse the private key
	signer, err := ssh.ParsePrivateKey([]byte(details.privateKey))
	if err != nil {
		return nil, NewSSHError(SSHErrorCodeConnection, "Failed to parse private key", err)
	}

	config := &ssh.ClientConfig{
		User: details.host.username,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: s.service.getHostKeyCallback(),
		Timeout:         defaultConnectTimeout,
	}

	if details.gateway != nil {
		return s.connectThroughGateway(ctx, details, config, signer)
	}

	return s.connectDirect(ctx, details.host.address, config)
}

// connectThroughGateway establishes connection through a gateway.
func (s *SessionImpl) connectThroughGateway(ctx context.Context, details *sessionConnectionDetails, config *ssh.ClientConfig, signer ssh.Signer) (*ssh.Client, error) {
	// Check timeout
	select {
	case <-ctx.Done():
		return nil, NewSSHError(SSHErrorCodeConnection, "SSH session initialization timeout", ctx.Err())
	default:
	}

	gatewayAddr := net.JoinHostPort(details.gateway.host, s.service.port)
	gatewayConfig := &ssh.ClientConfig{
		User: details.gateway.user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: s.service.getHostKeyCallback(),
		Timeout:         sshConnectionTimeout,
	}

	gatewayClient, err := ssh.Dial("tcp", gatewayAddr, gatewayConfig)
	if err != nil {
		return nil, NewSSHError(SSHErrorCodeConnection, "Failed to connect to gateway "+gatewayAddr, err)
	}

	targetAddr := net.JoinHostPort(details.host.address, s.service.port)

	conn, err := gatewayClient.Dial("tcp", targetAddr)
	if err != nil {
		_ = gatewayClient.Close()

		return nil, NewSSHError(SSHErrorCodeConnection, fmt.Sprintf("Failed to connect to target host %s through gateway", targetAddr), err)
	}

	nconn, chans, reqs, err := ssh.NewClientConn(conn, targetAddr, config)
	if err != nil {
		_ = gatewayClient.Close()

		return nil, NewSSHError(SSHErrorCodeConnection, "Failed to establish SSH connection through gateway", err)
	}

	return ssh.NewClient(nconn, chans, reqs), nil
}

// connectDirect connects to the VM with the dial that the director
// adapter shares, so the TCP connect and the SSH handshake both end with
// ctx, the session's init timeout.
func (s *SessionImpl) connectDirect(ctx context.Context, hostAddress string, config *ssh.ClientConfig) (*ssh.Client, error) {
	addr := net.JoinHostPort(hostAddress, s.service.port)

	client, err := sshconn.DialDirect(ctx, addr, config)
	if err != nil {
		return nil, NewSSHError(SSHErrorCodeConnection, "Failed to connect to "+addr, err)
	}

	return client, nil
}

// openShell runs setupSSHSession under ctx, the session's init timeout.
// A VM can finish the handshake and then never answer the session, PTY,
// or shell request, and none of those requests takes a context, so client
// is closed when ctx ends, which ends whichever request is waiting.
func (s *SessionImpl) openShell(ctx context.Context, client *ssh.Client) (*shellConn, error) {
	stopClosing := context.AfterFunc(ctx, func() { _ = client.Close() })

	shell, err := s.setupSSHSession(client)

	if !stopClosing() {
		// ctx ended while the requests ran and the client is closed, so
		// a shell that started anyway cannot be used.
		if shell != nil {
			s.closeQuietly("SSH session", shell.session)
		}

		return nil, NewSSHError(SSHErrorCodeConnection, "SSH session ended before its shell started", context.Cause(ctx))
	}

	if err != nil {
		return nil, err
	}

	return shell, nil
}

// shellConn is the SSH session of a terminal's shell, with its pipes.
type shellConn struct {
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader
	stderr  io.Reader
}

// setupSSHSession opens a session on client and starts a shell in it with
// a PTY and pipes. When a later step fails, the session it opened is
// closed again.
func (s *SessionImpl) setupSSHSession(client *ssh.Client) (*shellConn, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, NewSSHError(SSHErrorCodeConnection, "Failed to create SSH session", err)
	}

	shell := &shellConn{session: session}

	err = startShell(shell)
	if err != nil {
		s.closeQuietly("SSH session", session)

		return nil, err
	}

	return shell, nil
}

// startShell requests the PTY, takes the pipes, and starts the shell.
func startShell(shell *shellConn) error {
	err := setupPTY(shell.session)
	if err != nil {
		return err
	}

	err = setupPipes(shell)
	if err != nil {
		return err
	}

	err = shell.session.Shell()
	if err != nil {
		return NewSSHError(SSHErrorCodeConnection, "Failed to start shell", err)
	}

	return nil
}

// setupPTY sets up the pseudo-terminal.
func setupPTY(session *ssh.Session) error {
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: terminalBaudRate,
		ssh.TTY_OP_OSPEED: terminalBaudRate,
	}

	err := session.RequestPty("xterm-256color", terminalColumns, terminalRows, modes)
	if err != nil {
		return NewSSHError(SSHErrorCodeConnection, "Failed to request PTY", err)
	}

	return nil
}

// setupPipes sets up stdin, stdout, and stderr pipes.
func setupPipes(shell *shellConn) error {
	var err error

	shell.stdin, err = shell.session.StdinPipe()
	if err != nil {
		return NewSSHError(SSHErrorCodeConnection, "Failed to create stdin pipe", err)
	}

	shell.stdout, err = shell.session.StdoutPipe()
	if err != nil {
		return NewSSHError(SSHErrorCodeConnection, "Failed to create stdout pipe", err)
	}

	shell.stderr, err = shell.session.StderrPipe()
	if err != nil {
		return NewSSHError(SSHErrorCodeConnection, "Failed to create stderr pipe", err)
	}

	return nil
}

// discardConnection closes a connection that Start opened and never
// published.
func (s *SessionImpl) discardConnection(shell *shellConn, client *ssh.Client) {
	if shell != nil {
		s.closeQuietly("SSH session", shell.session)
	}

	s.closeQuietly("SSH client", client)
}

// closeQuietly closes part of a connection and logs a failure, except
// io.EOF and net.ErrClosed, which mean the VM or the init timeout closed
// it first.
func (s *SessionImpl) closeQuietly(what string, closer io.Closer) {
	err := closer.Close()
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return
	}

	s.logger.Errorf("Failed to close %s for session %s: %v", what, s.id, err)
}

// initializeSessionChannels initializes communication channels.
func (s *SessionImpl) initializeSessionChannels() {
	s.outputChan = make(chan []byte, outputChannelBuffer)
	s.errorChan = make(chan error, errorChannelBuffer)
	s.closeChan = make(chan struct{})
	s.keepAliveDone = make(chan struct{})
}

// startBackgroundProcesses starts the background goroutines.
func (s *SessionImpl) startBackgroundProcesses() {
	go s.readOutput()

	if s.config.KeepAlive > 0 {
		go s.keepAlive()
	}
}

// readOutput continuously reads from stdout and stderr.
func (s *SessionImpl) readOutput() {
	var waitGroup sync.WaitGroup
	waitGroup.Add(outputPipeGoroutines)

	// Read stdout in separate goroutine
	go s.readFromPipe(s.stdout, &waitGroup, "stdout")

	// Read stderr in separate goroutine
	go s.readFromPipe(s.stderr, &waitGroup, "stderr")

	waitGroup.Wait()
	close(s.outputChan)
}

// readFromPipe reads from a pipe and sends data to the output channel.
func (s *SessionImpl) readFromPipe(pipe io.Reader, waitGroup *sync.WaitGroup, _ string) {
	defer waitGroup.Done()

	buf := make([]byte, ioBufferSize)

	for {
		if s.shouldStopReading() {
			return
		}

		bytesRead, err := pipe.Read(buf)
		if err != nil {
			if err != io.EOF {
				s.errorChan <- err
			}

			return
		}

		if bytesRead > 0 {
			if !s.sendOutputData(buf[:bytesRead]) {
				return
			}
		}
	}
}

// shouldStopReading checks if reading should stop due to close signal.
func (s *SessionImpl) shouldStopReading() bool {
	select {
	case <-s.closeChan:
		return true
	default:
		return false
	}
}

// sendOutputData sends output data to the channel, returning false if should stop.
func (s *SessionImpl) sendOutputData(data []byte) bool {
	// Send a copy of the data
	output := make([]byte, len(data))
	copy(output, data)

	select {
	case s.outputChan <- output:
		return true
	case <-s.closeChan:
		return false
	}
}

// keepAlive sends periodic SSH keepalive packets to prevent connection timeout.
func (s *SessionImpl) keepAlive() {
	s.logger.Debugf("Starting SSH keepalive for session: %s (interval: %v)", s.id, s.config.KeepAlive)

	ticker := time.NewTicker(s.config.KeepAlive)
	defer ticker.Stop()

	for {
		select {
		case <-s.keepAliveDone:
			s.logger.Debugf("SSH keepalive stopped for session: %s", s.id)

			return
		case <-s.closeChan:
			s.logger.Debugf("SSH keepalive stopped due to session closure: %s", s.id)

			return
		case <-ticker.C:
			if s.sshClient != nil {
				// Send SSH keepalive packet
				_, _, err := s.sshClient.SendRequest("keepalive@openssh.com", true, nil)
				if err != nil {
					s.logger.Debugf("Failed to send SSH keepalive for session %s: %v", s.id, err)
					// Don't return on error, just continue trying
				} else {
					s.logger.Debugf("Sent SSH keepalive for session: %s", s.id)
				}
			}
		}
	}
}

// noOpLogger is a no-operation logger implementation.
type noOpLogger struct{}

func (l *noOpLogger) Infof(format string, args ...interface{})  {}
func (l *noOpLogger) Debugf(format string, args ...interface{}) {}
func (l *noOpLogger) Errorf(format string, args ...interface{}) {}
