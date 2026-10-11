package sshconn

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"golang.org/x/crypto/ssh"
)

// statusSuccess is the status the director gives an instance whose agent
// created the user.
const statusSuccess = "success"

var (
	// ErrNoHosts means the director's SSH setup named no instance.
	ErrNoHosts = errors.New("the director's SSH setup returned no instances")
	// ErrSetupFailed means the director's SSH setup returned an entry
	// Blacksmith cannot connect with.
	ErrSetupFailed = errors.New("the director's SSH setup did not succeed on every instance")
)

// Host is one instance the setup created the user on.
type Host struct {
	// Address is the instance's IP address.
	Address string
	// Username is the throwaway user the setup created.
	Username string
	// HostKey is the host public key the director reported for the
	// instance, in authorized-keys form.
	HostKey string
	// Job is the instance group.
	Job string
	// IndexOrID is the instance's ID, or its index when it has no ID.
	IndexOrID string
}

// Gateway is the SSH gateway the director's default_ssh_options name. It is
// empty when the director names none.
type Gateway struct {
	User string
	Host string
}

// Session is one SSH setup that succeeded. Cleanup removes the user from
// the instances the setup targeted, and it acts at most once however often
// it is called.
type Session struct {
	// Deployment is the deployment the setup targeted.
	Deployment string
	// Target is the instance selection the setup and its cleanup use.
	Target boshapi.SSHTarget
	// User is the throwaway user the setup created.
	User string
	// Hosts lists each instance the user was created on.
	Hosts []Host
	// Gateway is the gateway the director named, if any.
	Gateway Gateway
	// Signer logs in as User.
	Signer ssh.Signer
	// Cleanup removes User. It runs on a context of its own, bounded by
	// the adapter, so a caller whose context has ended still removes the
	// user.
	Cleanup func(ctx context.Context) error
}

// ReadHosts turns the director's setup results into the hosts to connect
// to. It refuses an empty result, any entry whose status is not success,
// and any entry with no IP address, because the user may then be missing
// from an instance Blacksmith would try to reach, or there is nothing to
// connect to. The gateway is the one the first entry names, as bosh-cli
// reads it.
func ReadHosts(results []boshapi.SSHResult, user string) ([]Host, Gateway, error) {
	if len(results) == 0 {
		return nil, Gateway{}, fmt.Errorf("%w for user %s", ErrNoHosts, user)
	}

	hosts := make([]Host, 0, len(results))

	for _, result := range results {
		instance := resultInstance(result)

		if result.Status != statusSuccess {
			return nil, Gateway{}, fmt.Errorf("%w: user %s on instance %s ended with status %q",
				ErrSetupFailed, user, instance, result.Status)
		}

		if result.IP == "" {
			return nil, Gateway{}, fmt.Errorf("%w: user %s on instance %s came back with no IP address to connect to",
				ErrSetupFailed, user, instance)
		}

		hosts = append(hosts, Host{
			Address:   result.IP,
			Username:  user,
			HostKey:   result.HostPublicKey,
			Job:       result.Job,
			IndexOrID: indexOrID(result),
		})
	}

	return hosts, Gateway{User: results[0].GatewayUser, Host: results[0].GatewayHost}, nil
}

// indexOrID names an instance by its ID, or by its index when it has no
// ID, as bosh-cli does.
func indexOrID(result boshapi.SSHResult) string {
	if result.ID != "" {
		return result.ID
	}

	if result.Index != nil {
		return strconv.Itoa(*result.Index)
	}

	return ""
}

// resultInstance names an entry's instance for an error.
func resultInstance(result boshapi.SSHResult) string {
	instanceID := indexOrID(result)

	switch {
	case result.Job == "" && instanceID == "":
		return "(unnamed)"
	case instanceID == "":
		return result.Job
	default:
		return result.Job + "/" + instanceID
	}
}
