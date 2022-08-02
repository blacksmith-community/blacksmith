package shield

import (
	"io"

	"github.com/shieldproject/shield/client/v2/shield"
)

var (
	_ Client = (*NetworkClient)(nil)
	_ Client = (*NoopClient)(nil)
)

type Client interface {
	io.Closer

	Authenticate(token string) error

	CreateSchedule(instance string, params interface{}) error
	DeleteSchedule(instance string) error
}

// A noop implementation that always returns nil for all methods.
type NoopClient struct{}

func (sh *NoopClient) Close() error {
	return nil
}
func (sh *NoopClient) Authenticate(token string) error {
	return nil
}
func (sh *NoopClient) CreateSchedule(instance string, params interface{}) error {
	return nil
}
func (sh *NoopClient) DeleteSchedule(instance string) error {
	return nil
}

// The actual implementation of the client with network connectivity.
type NetworkClient struct {
	cli *shield.Client
}

func NewClient(address string, insecure bool) *NetworkClient {
	return &NetworkClient{
		cli: &shield.Client{
			URL:                address,
			InsecureSkipVerify: insecure,
		},
	}
}

func (sh *NetworkClient) Close() error {
	return sh.cli.Logout()
}

func (sh *NetworkClient) Authenticate(token string) error {
	return sh.cli.Authenticate(&shield.TokenAuth{Token: token})
}

func (sh *NetworkClient) CreateSchedule(instance string, params interface{}) error {
	return nil
}

func (sh *NetworkClient) DeleteSchedule(instance string) error {
	return nil
}
