package sshconn

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

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
	// ErrGatewayUnsupported means the director's SSH setup named a gateway
	// to reach the instances through, which Blacksmith cannot use.
	ErrGatewayUnsupported = errors.New("the director's SSH setup names a gateway")
)

// Host is one instance the setup created the user on.
type Host struct {
	// Address is the instance's IP address.
	Address string
	// Username is the throwaway user the setup created.
	Username string
	// HostKey is the host public key the director reported for the
	// instance. Every connection to the instance pins it.
	HostKey ssh.PublicKey
	// Job is the instance group.
	Job string
	// IndexOrID is the instance's ID, or its index when it has no ID.
	IndexOrID string
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
	// Signer logs in as User.
	Signer ssh.Signer
	// Cleanup removes User. It runs on a context of its own, bounded by
	// the adapter, so a caller whose context has ended still removes the
	// user.
	Cleanup func(ctx context.Context) error
}

// Instance names the host's instance as an operator does, group/id.
func (h Host) Instance() string {
	return instanceName(h.Job, h.IndexOrID)
}

// ReadHosts turns the director's setup results into the hosts to connect
// to. It refuses an empty result, any entry whose status is not success,
// any entry with no IP address, any entry that names a gateway host, and
// any entry whose host key is missing or does not parse. The user may then
// be missing from an instance Blacksmith would try to reach, there is
// nothing to connect to, the instance is reachable only through a gateway
// that would refuse the throwaway key, or there is no key to check the VM
// against. A gateway user without a gateway host names no gateway.
func ReadHosts(results []boshapi.SSHResult, user string) ([]Host, error) {
	if len(results) == 0 {
		return nil, fmt.Errorf("%w for user %s", ErrNoHosts, user)
	}

	hosts := make([]Host, 0, len(results))

	for _, result := range results {
		instance := resultInstance(result)

		if result.Status != statusSuccess {
			return nil, fmt.Errorf("%w: user %s on instance %s ended with status %q",
				ErrSetupFailed, user, instance, result.Status)
		}

		if result.IP == "" {
			return nil, fmt.Errorf("%w: user %s on instance %s came back with no IP address to connect to",
				ErrSetupFailed, user, instance)
		}

		if result.GatewayHost != "" {
			return nil, fmt.Errorf("%w: user %s on instance %s is to be reached through the gateway %s that the director's default_ssh_options name. "+
				"Blacksmith does not support SSH through a gateway, because the gateway would refuse the throwaway key Blacksmith logs in with",
				ErrGatewayUnsupported, user, instance, gatewayName(result))
		}

		hostKey, err := readHostKey(result, user, instance)
		if err != nil {
			return nil, err
		}

		hosts = append(hosts, Host{
			Address:   result.IP,
			Username:  user,
			HostKey:   hostKey,
			Job:       result.Job,
			IndexOrID: indexOrID(result),
		})
	}

	return hosts, nil
}

// gatewayName names an entry's gateway as user@host, or as the host alone
// when the entry names no gateway user.
func gatewayName(result boshapi.SSHResult) string {
	if result.GatewayUser == "" {
		return result.GatewayHost
	}

	return result.GatewayUser + "@" + result.GatewayHost
}

// readHostKey parses the host key the director reported for an entry, in
// the authorized-keys form of the VM's ssh_host_rsa_key.pub.
//
//nolint:ireturn // ssh.ParseAuthorizedKey returns the key as ssh.PublicKey.
func readHostKey(result boshapi.SSHResult, user, instance string) (ssh.PublicKey, error) {
	if strings.TrimSpace(result.HostPublicKey) == "" {
		return nil, fmt.Errorf("%w: user %s on instance %s came back with no host key, so Blacksmith cannot check that it connects to that VM",
			ErrHostKeyUnusable, user, instance)
	}

	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(result.HostPublicKey)) //nolint:dogsled // Only the key matters.
	if err != nil {
		return nil, fmt.Errorf("%w: the host key for user %s on instance %s does not parse: %w",
			ErrHostKeyUnusable, user, instance, err)
	}

	return key, nil
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
	return instanceName(result.Job, indexOrID(result))
}

// instanceName names an instance by its group and its ID or index.
func instanceName(job, instanceID string) string {
	switch {
	case job == "" && instanceID == "":
		return "(unnamed)"
	case instanceID == "":
		return job
	default:
		return job + "/" + instanceID
	}
}
