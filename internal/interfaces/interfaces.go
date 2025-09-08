package interfaces

import (
	"context"
	"net/http"

	"blacksmith/pkg/logger"
	"blacksmith/pkg/services"
	rabbitmqssh "blacksmith/services/rabbitmq"
)

// Logger interface for logging operations across all internal packages.
type Logger = logger.Logger

// Vault interface for vault operations across all internal packages.
type Vault interface {
	Get(ctx context.Context, path string, result interface{}) (bool, error)
	// CF Registration methods (matching actual implementation)
	ListCFRegistrations(ctx context.Context) ([]map[string]interface{}, error)
	SaveCFRegistration(ctx context.Context, registration map[string]interface{}) error
	GetCFRegistration(ctx context.Context, registrationID string, out interface{}) (bool, error)
	DeleteCFRegistration(ctx context.Context, registrationID string) error
	UpdateCFRegistrationStatus(ctx context.Context, registrationID, status, errorMsg string) error
}

// Config interface for configuration access across all internal packages.
type Config interface {
	IsSSHUITerminalEnabled() bool
}

// Broker interface for broker operations across all internal packages.
type Broker interface {
	// Add broker methods as needed
}

// CFManager interface for Cloud Foundry operations across all internal packages.
type CFManager interface {
	// Add CF manager methods as needed
}

// VMMonitor interface for VM monitoring across all internal packages.
type VMMonitor interface {
	// Add VM monitor methods as needed
}

// SSHService interface for SSH operations across all internal packages.
type SSHService interface {
	// Add SSH service methods as needed
}

// RabbitMQSSHService interface for RabbitMQ SSH operations across all internal packages.
type RabbitMQSSHService interface {
	// Add RabbitMQ SSH service methods as needed
}

// RabbitMQMetadataService interface for RabbitMQ metadata operations across all internal packages.
type RabbitMQMetadataService interface {
	// Add RabbitMQ metadata service methods as needed
}

// RabbitMQExecutorService interface for RabbitMQ executor operations across all internal packages.
type RabbitMQExecutorService interface {
	ExecuteCommand(ctx context.Context, execCtx rabbitmqssh.ExecutionContext, deployment, instance string, index int, category, command string, arguments []string) (*rabbitmqssh.StreamingExecutionResult, error)
	ExecuteCommandSync(ctx context.Context, execCtx rabbitmqssh.ExecutionContext, deployment, instance string, index int, category, command string, arguments []string) (*rabbitmqssh.RabbitMQCtlExecution, error)
}

// RabbitMQAuditService interface for RabbitMQ audit operations across all internal packages.
type RabbitMQAuditService interface {
	LogStreamingExecution(ctx context.Context, result *rabbitmqssh.StreamingExecutionResult, user, clientIP string) error
}

// RabbitMQPluginsMetadataService interface for RabbitMQ plugins metadata operations across all internal packages.
type RabbitMQPluginsMetadataService interface {
	// Add RabbitMQ plugins metadata service methods as needed
}

// RabbitMQPluginsExecutorService interface for RabbitMQ plugins executor operations across all internal packages.
type RabbitMQPluginsExecutorService interface {
	ExecuteCommand(ctx context.Context, execCtx rabbitmqssh.PluginsExecutionContext, deployment, instance string, index int, category, command string, arguments []string) (*rabbitmqssh.PluginsStreamingExecutionResult, error)
	ExecuteCommandSync(ctx context.Context, execCtx rabbitmqssh.PluginsExecutionContext, deployment, instance string, index int, category, command string, arguments []string) (string, int, error)
}

// RabbitMQPluginsAuditService interface for RabbitMQ plugins audit operations across all internal packages.
type RabbitMQPluginsAuditService interface {
	LogExecution(ctx context.Context, execution *rabbitmqssh.RabbitMQPluginsExecution, user, clientIP, executionID string, duration int64) error
}

// WebSocketHandler interface for WebSocket operations across all internal packages.
type WebSocketHandler interface {
	HandleWebSocket(ctx context.Context, w http.ResponseWriter, req *http.Request, deploymentName, instanceName string, instanceIndex int)
	GetActiveSessions() int
}

// ServicesManager interface wrapper for external services manager.
type ServicesManager interface {
	GetSecurity() *services.SecurityMiddleware
	GetRedis() RedisService
}

// RedisService interface for Redis operations.
type RedisService interface {
	// Add Redis service methods as needed from pkg/services/redis
}
