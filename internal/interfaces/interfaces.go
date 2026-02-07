package interfaces

import (
	"context"
	"net/http"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/config"
	"blacksmith/internal/services"
	rabbitmqssh "blacksmith/internal/services/rabbitmq"
	"blacksmith/internal/vmmonitor"
	"blacksmith/pkg/logger"
	pkgservices "blacksmith/pkg/services"
	"blacksmith/pkg/vault"
)

// Logger interface for logging operations across all internal packages.
type Logger = logger.Logger

// Vault interface for vault operations across all internal packages.
type Vault interface {
	Get(ctx context.Context, path string, result interface{}) (bool, error)
	Put(ctx context.Context, path string, data interface{}) error
	Delete(ctx context.Context, path string) error
	FindInstance(ctx context.Context, instanceID string) (*vault.Instance, bool, error)
	// CF Registration methods (matching actual implementation)
	ListCFRegistrations(ctx context.Context) ([]map[string]interface{}, error)
	SaveCFRegistration(ctx context.Context, registration map[string]interface{}) error
	GetCFRegistration(ctx context.Context, registrationID string, out interface{}) (bool, error)
	DeleteCFRegistration(ctx context.Context, registrationID string) error
	UpdateCFRegistrationStatus(ctx context.Context, registrationID, status, errorMsg string) error
	SaveCFRegistrationProgress(ctx context.Context, registrationID string, progress map[string]interface{}) error
}

// Config interface for configuration access across all internal packages.
type Config interface {
	IsSSHUITerminalEnabled() bool
	GetEnvironment() string
	GetBOSHConfig() config.BOSHConfig
	GetVaultConfig() config.VaultConfig
	GetBrokerConfig() config.BrokerConfig
	GetCFConfig() config.CFBrokerConfig
}

// Broker interface for broker operations across all internal packages.
type Broker interface {
	// Placeholder method to distinguish from other interfaces - implement as needed
	IsBroker() bool
	GetPlans() map[string]services.Plan
	GetVault() Vault
}

// CFManager interface for Cloud Foundry operations across all internal packages.
type CFManager interface {
	// GetClient returns a CF API client for the specified endpoint
	GetClient(endpointName string) (interface{}, error)
	// IsCFManager identifies this as a CFManager implementation
	IsCFManager() bool
	// GetStatus returns the current status of all CF endpoints
	GetStatus() map[string]interface{}
}

// VMMonitor interface for VM monitoring across all internal packages.
type VMMonitor interface {
	// Placeholder method to distinguish from other interfaces - implement as needed
	IsVMMonitor() bool
	// GetServiceVMStatus retrieves VM status for a service instance
	GetServiceVMStatus(ctx context.Context, serviceID string) (*vmmonitor.VMStatus, error)
	// GetStatus returns the current state of the VM monitor for debugging
	GetStatus() vmmonitor.MonitorStatus
}

// SSHService interface for SSH operations across all internal packages.
type SSHService interface {
	ssh.SSHService
	IsSSHService() bool
}

// RabbitMQSSHService interface for RabbitMQ SSH operations across all internal packages.
type RabbitMQSSHService interface {
	IsRabbitMQSSHService() bool
	ListQueues(deployment, instance string, index int) (*rabbitmqssh.RabbitMQCommandResult, error)
	ListConnections(deployment, instance string, index int) (*rabbitmqssh.RabbitMQCommandResult, error)
	ListChannels(deployment, instance string, index int) (*rabbitmqssh.RabbitMQCommandResult, error)
	ListUsers(deployment, instance string, index int) (*rabbitmqssh.RabbitMQCommandResult, error)
	ClusterStatus(deployment, instance string, index int) (*rabbitmqssh.RabbitMQCommandResult, error)
	NodeHealth(deployment, instance string, index int) (*rabbitmqssh.RabbitMQCommandResult, error)
	Status(deployment, instance string, index int) (*rabbitmqssh.RabbitMQCommandResult, error)
	Environment(deployment, instance string, index int) (*rabbitmqssh.RabbitMQCommandResult, error)
	ExecuteCommand(deployment, instance string, index int, cmd rabbitmqssh.RabbitMQCommand) (*rabbitmqssh.RabbitMQCommandResult, error)
}

// RabbitMQMetadataService interface for RabbitMQ metadata operations across all internal packages.
type RabbitMQMetadataService interface {
	GetCategories() []rabbitmqssh.RabbitMQCtlCategory
	GetCategory(name string) (*rabbitmqssh.RabbitMQCtlCategory, error)
	GetCommand(category, command string) (*rabbitmqssh.RabbitMQCtlCommand, error)
	IsRabbitMQMetadataService() bool
}

// RabbitMQExecutorService interface for RabbitMQ executor operations across all internal packages.
type RabbitMQExecutorService interface {
	ExecuteCommand(ctx context.Context, execCtx rabbitmqssh.ExecutionContext, deployment, instance string, index int, category, command string, arguments []string) (*rabbitmqssh.StreamingExecutionResult, error)
	ExecuteCommandSync(ctx context.Context, execCtx rabbitmqssh.ExecutionContext, deployment, instance string, index int, category, command string, arguments []string) (*rabbitmqssh.RabbitMQCtlExecution, error)
}

// RabbitMQAuditService interface for RabbitMQ audit operations across all internal packages.
type RabbitMQAuditService interface {
	LogStreamingExecution(ctx context.Context, result *rabbitmqssh.StreamingExecutionResult, user, clientIP string) error
	GetAuditHistory(ctx context.Context, instanceID string, limit int) ([]rabbitmqssh.AuditEntry, error)
}

// RabbitMQPluginsMetadataService interface for RabbitMQ plugins metadata operations across all internal packages.
type RabbitMQPluginsMetadataService interface {
	GetCategories() []rabbitmqssh.RabbitMQPluginsCategory
	GetCategory(name string) (*rabbitmqssh.RabbitMQPluginsCategory, error)
	GetCommand(name string) (*rabbitmqssh.RabbitMQPluginsCommand, error)
	IsRabbitMQPluginsMetadataService() bool
}

// RabbitMQPluginsExecutorService interface for RabbitMQ plugins executor operations across all internal packages.
type RabbitMQPluginsExecutorService interface {
	ExecuteCommand(ctx context.Context, execCtx rabbitmqssh.PluginsExecutionContext, deployment, instance string, index int, category, command string, arguments []string) (*rabbitmqssh.PluginsStreamingExecutionResult, error)
	ExecuteCommandSync(ctx context.Context, execCtx rabbitmqssh.PluginsExecutionContext, deployment, instance string, index int, category, command string, arguments []string) (string, int, error)
}

// RabbitMQPluginsAuditService interface for RabbitMQ plugins audit operations across all internal packages.
type RabbitMQPluginsAuditService interface {
	LogExecution(ctx context.Context, execution *rabbitmqssh.RabbitMQPluginsExecution, user, clientIP, executionID string, duration int64) error
	GetHistory(ctx context.Context, instanceID string, limit int) ([]rabbitmqssh.PluginsHistoryEntry, error)
}

// WebSocketHandler interface for WebSocket operations across all internal packages.
type WebSocketHandler interface {
	HandleWebSocket(ctx context.Context, w http.ResponseWriter, req *http.Request, deploymentName, instanceName string, instanceIndex int)
	GetActiveSessions() int
}

// ServicesManager interface wrapper for external services manager.
type ServicesManager interface {
	GetSecurity() *pkgservices.SecurityMiddleware
	GetRedis() RedisService
}

// RedisService interface for Redis operations.
type RedisService interface {
	// Placeholder method to distinguish from other interfaces - implement as needed
	IsRedisService() bool
}

// Director interface for BOSH director operations.
// This is an alias to the bosh.Director interface.
type Director = bosh.Director
