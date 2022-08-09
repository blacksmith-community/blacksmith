package shield

import (
	"io"
	"strings"

	"github.com/pivotal-cf/brokerapi"
	"github.com/shieldproject/shield/client/v2/shield"
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

	Authenticate(auth shield.AuthMethod) error

	CreateSchedule(instance string, details brokerapi.ProvisionDetails, url string, creds interface{}) error
	DeleteSchedule(instance string, details brokerapi.DeprovisionDetails) error
}

// A noop implementation that always returns nil for all methods.
type NoopClient struct{}

func (cli *NoopClient) Close() error {
	return nil
}
func (cli *NoopClient) Authenticate(auth shield.AuthMethod) error {
	return nil
}
func (cli *NoopClient) CreateSchedule(instance string, details brokerapi.ProvisionDetails, url string, creds interface{}) error {
	return nil
}
func (cli *NoopClient) DeleteSchedule(instance string, details brokerapi.DeprovisionDetails) error {
	return nil
}

// The actual implementation of the client with network connectivity.
type NetworkClient struct {
	shield *shield.Client

	agent string

	tenant *shield.Tenant
	store  *shield.Store

	schedule string
	retain   string

	enabledOnTargets []string
}

type Config struct {
	Address  string
	Insecure bool

	Agent string

	TenantUUID string
	StoreUUID  string

	Schedule string
	Retain   string

	EnabledOnTargets []string
}

func NewClient(cfg Config) *NetworkClient {
	return &NetworkClient{
		shield: &shield.Client{
			URL:                cfg.Address,
			InsecureSkipVerify: cfg.Insecure,
		},

		agent: cfg.Agent,

		tenant: &shield.Tenant{UUID: cfg.TenantUUID},
		store:  &shield.Store{UUID: cfg.StoreUUID},

		schedule: cfg.Schedule,
		retain:   cfg.Retain,

		enabledOnTargets: cfg.EnabledOnTargets,
	}
}

func (cli *NetworkClient) Close() error {
	return cli.shield.Logout()
}

func (cli *NetworkClient) Authenticate(auth shield.AuthMethod) error {
	return cli.shield.Authenticate(auth)
}

func join(s ...string) string {
	return strings.Join(s, ":")
}

func (cli *NetworkClient) CreateSchedule(instanceID string, details brokerapi.ProvisionDetails, url string, creds interface{}) error {
	m := map[string]string{
		"rabbitmq": "rabbitmq-broker",
		"redis":    "redis-broker",
	}

	// Verify that the target should be backed up.
	var enabled bool = false
	for _, target := range cli.enabledOnTargets {
		enabled = target == details.ServiceID || enabled
	}
	if !enabled {
		return nil
	}

	// Generate the target configurations.
	var config map[string]interface{}
	switch details.ServiceID {
	case "rabbitmq":
		config = map[string]interface{}{
			"rmq_url": "https://" + url,

			"rmq_username": creds.(map[string]interface{})["username"],
			"rmq_password": creds.(map[string]interface{})["password"],

			"skip_ssl_validation": true,
		}
	default:
		return nil
	}

	target := &shield.Target{
		Name:    join("targets", details.ServiceID, details.PlanID, instanceID),
		Summary: "This target is managed by Blacksmith.",

		Plugin: m[details.ServiceID],
		Agent:  cli.agent,
		Config: config,
	}

	target, err := cli.shield.CreateTarget(cli.tenant, target)
	if err != nil {
		return err
	}

	job := &shield.Job{
		Name:    join("jobs", details.ServiceID, details.PlanID, instanceID),
		Summary: "This job is managed by Blacksmith.",

		TargetUUID: target.UUID,
		StoreUUID:  cli.store.UUID,
		Schedule:   cli.schedule,
		Retain:     cli.retain,
		Retries:    3,
	}

	_, err = cli.shield.CreateJob(cli.tenant, job)
	if err != nil {
		return err
	}

	return nil
}

func (cli *NetworkClient) DeleteSchedule(instanceID string, details brokerapi.DeprovisionDetails) error {
	name := join("jobs", details.ServiceID, details.PlanID, instanceID)
	job, err := cli.shield.FindJob(cli.tenant, name, false)
	if err != nil {
		return err
	}

	// TODO: do we need to verify that the response is OK? What if it's not?
	_, err = cli.shield.DeleteTarget(cli.tenant, &shield.Target{UUID: job.Target.UUID})
	if err != nil {
		return err
	}

	// TODO: do we need to verify that the response is OK? What if it's not?
	_, err = cli.shield.DeleteJob(cli.tenant, &shield.Job{UUID: job.UUID})
	if err != nil {
		return err
	}

	return nil
}
