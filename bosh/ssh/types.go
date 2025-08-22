package ssh

import (
	"io"
	"time"
)

// SSHService defines the interface for BOSH SSH operations
type SSHService interface {
	// Execute a one-off command on a BOSH VM
	ExecuteCommand(request *SSHRequest) (*SSHResponse, error)

	// Create an interactive SSH session for streaming
	CreateSession(request *SSHRequest) (SSHSession, error)

	// Close the SSH service and cleanup resources
	Close() error
}

// SSHExecutor handles command execution on BOSH VMs
type SSHExecutor interface {
	// Execute a command with timeout
	Execute(request *SSHRequest) (*SSHResponse, error)

	// Execute a command with custom timeout
	ExecuteWithTimeout(request *SSHRequest, timeout time.Duration) (*SSHResponse, error)

	// Execute a command with streaming output
	ExecuteWithStream(request *SSHRequest, output io.Writer) (*SSHResponse, error)
}

// SSHSession represents an interactive SSH session
type SSHSession interface {
	// Start the SSH session
	Start() error

	// Send input to the session
	SendInput(data []byte) error

	// Read output from the session
	ReadOutput() ([]byte, error)

	// Close the session
	Close() error

	// Get session status
	Status() SessionStatus

	// Set window size for terminal sessions
	SetWindowSize(width, height int) error
}

// SSHRequest represents a request to execute SSH command or create session
type SSHRequest struct {
	// Target deployment, instance, and index
	Deployment string `json:"deployment"`
	Instance   string `json:"instance"`
	Index      int    `json:"index"`

	// Command and arguments (for command execution)
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`

	// Environment variables
	Env map[string]string `json:"env,omitempty"`

	// Working directory
	WorkingDir string `json:"working_dir,omitempty"`

	// Timeout for command execution (in seconds)
	Timeout int `json:"timeout,omitempty"`

	// SSH options
	Options *SSHOptions `json:"options,omitempty"`
}

// SSHResponse represents the response from SSH command execution
type SSHResponse struct {
	// Execution status
	Success  bool  `json:"success"`
	ExitCode int   `json:"exit_code"`
	Duration int64 `json:"duration"` // Duration in milliseconds

	// Output streams
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`

	// Error information
	Error string `json:"error,omitempty"`

	// Metadata
	Timestamp time.Time `json:"timestamp"`
	RequestID string    `json:"request_id,omitempty"`
}

// SSHOptions contains SSH connection and execution options
type SSHOptions struct {
	// Connection options
	ConnectTimeout time.Duration `json:"connect_timeout,omitempty"`
	KeepAlive      time.Duration `json:"keep_alive,omitempty"`

	// Terminal options (for interactive sessions)
	Terminal     bool   `json:"terminal,omitempty"`
	TerminalType string `json:"terminal_type,omitempty"`
	WindowWidth  int    `json:"window_width,omitempty"`
	WindowHeight int    `json:"window_height,omitempty"`

	// Execution options
	Interactive bool `json:"interactive,omitempty"`
	Privileged  bool `json:"privileged,omitempty"`

	// Output options
	BufferOutput  bool `json:"buffer_output,omitempty"`
	MaxOutputSize int  `json:"max_output_size,omitempty"`
}

// SessionStatus represents the status of an SSH session
type SessionStatus string

const (
	SessionStatusCreated    SessionStatus = "created"
	SessionStatusConnecting SessionStatus = "connecting"
	SessionStatusConnected  SessionStatus = "connected"
	SessionStatusActive     SessionStatus = "active"
	SessionStatusClosing    SessionStatus = "closing"
	SessionStatusClosed     SessionStatus = "closed"
	SessionStatusError      SessionStatus = "error"
)

// SSHError represents SSH-specific errors
type SSHError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Cause   error  `json:"-"`
}

func (e *SSHError) Error() string {
	if e.Cause != nil {
		return e.Message + ": " + e.Cause.Error()
	}
	return e.Message
}

func (e *SSHError) Unwrap() error {
	return e.Cause
}

// SSH error codes
const (
	SSHErrorCodeConnection     = "SSH_CONNECTION_FAILED"
	SSHErrorCodeAuthentication = "SSH_AUTHENTICATION_FAILED"
	SSHErrorCodeExecution      = "SSH_EXECUTION_FAILED"
	SSHErrorCodeTimeout        = "SSH_TIMEOUT"
	SSHErrorCodePermission     = "SSH_PERMISSION_DENIED"
	SSHErrorCodeInvalidRequest = "SSH_INVALID_REQUEST"
	SSHErrorCodeInternal       = "SSH_INTERNAL_ERROR"
)

// NewSSHError creates a new SSH error
func NewSSHError(code, message string, cause error) *SSHError {
	return &SSHError{
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}
