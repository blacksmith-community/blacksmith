package shield

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/pivotal-cf/brokerapi/v8/domain"
	"github.com/shieldproject/shield/client/v2/shield"
)

// Static errors for err113 compliance.
var (
	ErrInvalidCredentialsFormat = errors.New("invalid credentials format for RabbitMQ service")
	ErrMissingAdminUsername     = errors.New("missing admin_username in RabbitMQ credentials")
	ErrMissingAdminPassword     = errors.New("missing admin_password in RabbitMQ credentials")
)

type (
	AuthMethod = shield.AuthMethod

	TokenAuth = shield.TokenAuth
	LocalAuth = shield.LocalAuth
)

var (
	_ Client = (*NetworkClient)(nil)
	_ Client = (*NoopClient)(nil)
)

// The client interface, also useful for mocking and testing.
type Client interface {
	io.Closer

	CreateSchedule(instance string, details domain.ProvisionDetails, url string, creds interface{}) error
	DeleteSchedule(instance string, details domain.DeprovisionDetails) error
}

// Logger interface for shield package.
type Logger interface {
	Debug(format string, args ...interface{})
	Info(format string, args ...interface{})
	Error(format string, args ...interface{})
}

// defaultLogger provides a default logger implementation.
type defaultLogger struct {
	debugging bool
}

func newDefaultLogger() *defaultLogger {
	return &defaultLogger{
		debugging: os.Getenv("DEBUG") != "" || os.Getenv("BLACKSMITH_DEBUG") != "",
	}
}

func (l *defaultLogger) Debug(f string, args ...interface{}) {
	if l.debugging {
		l.printf("DEBUG", f, args...)
	}
}

func (l *defaultLogger) Info(f string, args ...interface{}) {
	l.printf("INFO", f, args...)
}

func (l *defaultLogger) Error(f string, args ...interface{}) {
	l.printf("ERROR", f, args...)
}

func (l *defaultLogger) printf(lvl, f string, args ...interface{}) {
	m := fmt.Sprintf(f, args...)
	now := time.Now().Format("2006-01-02 15:04:05.000")
	m = fmt.Sprintf("%s %-5s  [shield] %s\n", now, lvl, m)
	fmt.Fprintf(os.Stderr, "%s", m)
}

// A noop implementation that always returns nil for all methods.
type NoopClient struct{}

func (cli *NoopClient) Close() error {
	return nil
}
func (cli *NoopClient) CreateSchedule(instance string, details domain.ProvisionDetails, url string, creds interface{}) error {
	return nil
}
func (cli *NoopClient) DeleteSchedule(instance string, details domain.DeprovisionDetails) error {
	return nil
}

// The actual implementation of the client with network connectivity.
type NetworkClient struct {
	shield *shield.Client
	logger Logger

	agent string

	tenant *shield.Tenant
	store  *shield.Store

	schedule string
	retain   string

	enabledOnTargets []string

	auth AuthMethod
}

type Config struct {
	Address  string
	Insecure bool

	Agent string

	Tenant string
	Store  string

	Schedule string
	Retain   string

	EnabledOnTargets []string

	Authentication AuthMethod
	Logger         Logger // Optional logger, will use default if nil
}

func NewClient(cfg Config) (*NetworkClient, error) {
	var logger Logger
	if cfg.Logger != nil {
		logger = cfg.Logger
	} else {
		logger = newDefaultLogger()
	}

	logger.Info("Creating new Shield client for address: %s", cfg.Address)
	logger.Debug("Shield config - Tenant: %s, Store: %s, Agent: %s, Schedule: %s, Retain: %s",
		cfg.Tenant, cfg.Store, cfg.Agent, cfg.Schedule, cfg.Retain)
	logger.Debug("Shield config - Insecure: %v, EnabledOnTargets: %v", cfg.Insecure, cfg.EnabledOnTargets)

	cli := &shield.Client{
		URL:                cfg.Address,
		InsecureSkipVerify: cfg.Insecure,
	}

	logger.Debug("Authenticating with Shield...")

	err := cli.Authenticate(cfg.Authentication)
	if err != nil {
		logger.Error("Failed to authenticate with Shield: %v", err)

		return nil, fmt.Errorf("shield authentication failed: %w", err)
	}

	logger.Debug("Successfully authenticated with Shield")

	logger.Debug("Finding tenant '%s' in Shield", cfg.Tenant)

	tenant, err := cli.FindMyTenant(cfg.Tenant, false)
	if err != nil {
		logger.Error("Failed to find tenant '%s': %v", cfg.Tenant, err)

		return nil, fmt.Errorf("failed to find tenant '%s': %w", cfg.Tenant, err)
	}

	logger.Debug("Found tenant: %s (UUID: %s)", tenant.Name, tenant.UUID)

	logger.Debug("Finding usable store '%s' for tenant", cfg.Store)

	store, err := cli.FindUsableStore(tenant, cfg.Store, false)
	if err != nil {
		logger.Error("Failed to find usable store '%s': %v", cfg.Store, err)

		return nil, fmt.Errorf("failed to find usable store '%s': %w", cfg.Store, err)
	}

	logger.Debug("Found store: %s (UUID: %s)", store.Name, store.UUID)

	logger.Info("Successfully created Shield client for tenant '%s' with store '%s'", tenant.Name, store.Name)

	return &NetworkClient{
		shield: cli,
		logger: logger,

		agent: cfg.Agent,

		tenant: tenant,
		store:  store,

		schedule: cfg.Schedule,
		retain:   cfg.Retain,

		enabledOnTargets: cfg.EnabledOnTargets,

		auth: cfg.Authentication,
	}, nil
}

func (cli *NetworkClient) Close() error {
	cli.logger.Debug("Closing Shield client connection")

	err := cli.shield.Logout()
	if err != nil {
		cli.logger.Error("Error during Shield logout: %v", err)

		return fmt.Errorf("shield logout failed: %w", err)
	}

	cli.logger.Debug("Shield client connection closed successfully")

	return nil
}

func join(s ...string) string {
	return strings.Join(s, ":")
}

func (cli *NetworkClient) CreateSchedule(instanceID string, details domain.ProvisionDetails, host string, creds interface{}) error {
	cli.logger.Info("Creating Shield schedule for instance %s (service: %s, plan: %s)",
		instanceID, details.ServiceID, details.PlanID)
	cli.logger.Debug("Instance details - Org: %s, Space: %s, Host: %s",
		details.OrganizationGUID, details.SpaceGUID, host)

	cli.logger.Debug("Re-authenticating with Shield for schedule creation")

	err := cli.shield.Authenticate(cli.auth)
	if err != nil {
		cli.logger.Error("Failed to re-authenticate with Shield: %v", err)

		return fmt.Errorf("shield re-authentication failed: %w", err)
	}

	cli.logger.Debug("Shield re-authentication successful")

	serviceMapping := map[string]string{
		"rabbitmq": "rabbitmq-broker",
		"redis":    "redis-broker",
	}

	// Verify that the target should be backed up.
	cli.logger.Debug("Checking if service '%s' is enabled for backup", details.ServiceID)

	enabled := false

	for _, target := range cli.enabledOnTargets {
		enabled = target == details.ServiceID || enabled
	}

	if !enabled {
		cli.logger.Info("Service '%s' is not enabled for Shield backup, skipping schedule creation", details.ServiceID)

		return nil
	}

	cli.logger.Debug("Service '%s' is enabled for backup, proceeding with schedule creation", details.ServiceID)

	// Generate the target configurations.
	cli.logger.Debug("Generating target configuration for service '%s'", details.ServiceID)

	var config map[string]interface{}

	switch details.ServiceID {
	case "rabbitmq":
		rmqURL := "http://" + net.JoinHostPort(host, "15672")
		cli.logger.Debug("Configuring RabbitMQ target with URL: %s", rmqURL)

		credsMap, ok := creds.(map[string]interface{})
		if !ok {
			cli.logger.Error("Invalid credentials format for RabbitMQ, expected map[string]interface{}, got %T", creds)

			return ErrInvalidCredentialsFormat
		}

		adminUser, ok := credsMap["admin_username"].(string)
		if !ok {
			cli.logger.Error("Missing or invalid admin_username in RabbitMQ credentials")

			return ErrMissingAdminUsername
		}

		adminPass, ok := credsMap["admin_password"].(string)
		if !ok {
			cli.logger.Error("Missing or invalid admin_password in RabbitMQ credentials")

			return ErrMissingAdminPassword
		}

		config = map[string]interface{}{
			"rmq_url":      rmqURL,
			"rmq_username": adminUser,
			"rmq_password": adminPass,
		}

		cli.logger.Debug("RabbitMQ target configuration created successfully")
	default:
		cli.logger.Info("No Shield backup configuration available for service '%s', skipping", details.ServiceID)

		return nil
	}

	targetName := join("targets", details.ServiceID, details.PlanID, instanceID)
	cli.logger.Debug("Creating Shield target: %s", targetName)
	target := &shield.Target{
		Name:    targetName,
		Summary: "This target is managed by Blacksmith.",

		Plugin: serviceMapping[details.ServiceID],
		Agent:  cli.agent,
		Config: config,
	}

	target, err = cli.shield.CreateTarget(cli.tenant, target)
	if err != nil {
		cli.logger.Error("Failed to create Shield target '%s': %v", targetName, err)

		return fmt.Errorf("failed to create Shield target: %w", err)
	}

	cli.logger.Info("Successfully created Shield target '%s' (UUID: %s)", target.Name, target.UUID)

	jobName := join("jobs", details.ServiceID, details.PlanID, instanceID)
	cli.logger.Debug("Creating Shield job: %s (schedule: %s, retain: %s)", jobName, cli.schedule, cli.retain)
	job := &shield.Job{
		Name:    jobName,
		Summary: "This job is managed by Blacksmith.",

		TargetUUID: target.UUID,
		StoreUUID:  cli.store.UUID,
		Schedule:   cli.schedule,
		Retain:     cli.retain,
	}

	createdJob, err := cli.shield.CreateJob(cli.tenant, job)
	if err != nil {
		cli.logger.Error("Failed to create Shield job '%s': %v", jobName, err)

		return fmt.Errorf("failed to create Shield job: %w", err)
	}

	cli.logger.Info("Successfully created Shield job '%s' (UUID: %s) for instance %s",
		createdJob.Name, createdJob.UUID, instanceID)

	return nil
}

func (cli *NetworkClient) DeleteSchedule(instanceID string, details domain.DeprovisionDetails) error {
	cli.logger.Info("Deleting Shield schedule for instance %s (service: %s, plan: %s)",
		instanceID, details.ServiceID, details.PlanID)

	cli.logger.Debug("Re-authenticating with Shield for schedule deletion")

	err := cli.shield.Authenticate(cli.auth)
	if err != nil {
		cli.logger.Error("Failed to re-authenticate with Shield: %v", err)

		return fmt.Errorf("shield re-authentication failed: %w", err)
	}

	cli.logger.Debug("Shield re-authentication successful")

	name := join("jobs", details.ServiceID, details.PlanID, instanceID)
	cli.logger.Debug("Finding Shield job: %s", name)

	job, err := cli.shield.FindJob(cli.tenant, name, false)
	if err != nil {
		cli.logger.Error("Failed to find Shield job '%s': %v", name, err)

		return fmt.Errorf("failed to find Shield job '%s': %w", name, err)
	}

	cli.logger.Debug("Found Shield job '%s' (UUID: %s)", job.Name, job.UUID)

	cli.logger.Debug("Deleting Shield job with UUID: %s", job.UUID)

	_, err = cli.shield.DeleteJob(cli.tenant, &shield.Job{UUID: job.UUID})
	if err != nil {
		cli.logger.Error("Failed to delete Shield job (UUID: %s): %v", job.UUID, err)

		return fmt.Errorf("failed to delete Shield job: %w", err)
	}

	cli.logger.Debug("Shield job deletion response received")
	cli.logger.Info("Successfully deleted Shield job '%s' (UUID: %s)", job.Name, job.UUID)

	cli.logger.Debug("Deleting Shield target with UUID: %s", job.Target.UUID)

	_, err = cli.shield.DeleteTarget(cli.tenant, &shield.Target{UUID: job.Target.UUID})
	if err != nil {
		cli.logger.Error("Failed to delete Shield target (UUID: %s): %v", job.Target.UUID, err)
		// Log error but continue since job is already deleted
		cli.logger.Error("Warning: Shield target deletion failed but continuing as job is already deleted")

		return fmt.Errorf("failed to delete Shield target: %w", err)
	}

	cli.logger.Debug("Shield target deletion response received")
	cli.logger.Info("Successfully deleted Shield target (UUID: %s) for instance %s", job.Target.UUID, instanceID)

	return nil
}
