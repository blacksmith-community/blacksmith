package bosh

import (
	"context"
	"errors"
	"fmt"
	"net"

	"blacksmith/internal/bosh/sshconn"
	"golang.org/x/crypto/ssh"
)

// ErrSSHGroupTargetInvalid is the sentinel SSHGroupSession returns when its
// target does not name a group and at least one instance index. Nothing
// is sent.
var ErrSSHGroupTargetInvalid = errors.New("an SSH group target must name its group and at least one distinct, non-negative index")

// ErrSSHGroupHostsInvalid is the sentinel SSHGroupSession returns when the
// director's setup result does not hold exactly one host for each index
// it was asked for, or names one instance twice. The user the setup
// created is removed.
var ErrSSHGroupHostsInvalid = errors.New("the SSH setup did not return exactly one host for each requested instance")

// ErrSSHSessionMissing is the sentinel DialSSHHost returns when it is
// given no session, or a session with no key to log in with.
var ErrSSHSessionMissing = errors.New("there is no SSH session to connect with")

// SSHGroupSession sets up one throwaway SSH user on several instances of
// one group, so a caller that works on each of them sends one setup and
// one cleanup rather than one of each per VM. It goes through the same
// setup as SSHSession, so the host keys the director reports are pinned,
// the user is recorded for the cleanup sweep before the setup is sent,
// and the session's Cleanup removes the user from every instance it
// targeted. An empty group would select every instance in the deployment,
// so it is refused before anything is sent, as is an index list that is
// empty, negative, or names an index twice. A setup result that does not
// hold exactly one host for each index, or names an instance twice, gives
// no session and an error that wraps ErrSSHGroupHostsInvalid, and its
// user is removed as after any other failed setup. The call changes no
// deployment, so a pooled director gives it no deploy slot.
//
//nolint:nonamedreturns // explainContext reads the named error, as in every other adapter method.
func (d *DirectorAdapter) SSHGroupSession(ctx context.Context, deployment, group string, indices []int) (_ *sshconn.Session, err error) {
	defer d.explainContext(ctx, "SSHGroupSession", &err)

	target, err := sshGroupTarget(group, indices)
	if err != nil {
		return nil, err
	}

	d.log.Infof("Creating SSH session for deployment %s, instances %s", deployment, sshconn.TargetName(target))

	session, err := d.setUpSSH(ctx, "SSHGroupSession", deployment, target, func(hosts []sshconn.Host) error {
		err := checkGroupHosts(group, len(indices), hosts)
		if err != nil {
			d.log.Errorf("SSH setup for deployment %s returned hosts that do not match its target: %v", deployment, err)
		}

		return err
	})
	if err != nil {
		return nil, err
	}

	return session, nil
}

// checkGroupHosts refuses a setup result that does not hold exactly one
// host for each of the want instances of group, or that names one
// instance twice. A director may leave an instance out, and the hosts
// come back in its order, so a caller that pairs hosts with instances
// can then rely on each instance appearing once.
func checkGroupHosts(group string, want int, hosts []sshconn.Host) error {
	if len(hosts) != want {
		return fmt.Errorf("%w: asked for %d instance(s) of group %s and got %d host(s)",
			ErrSSHGroupHostsInvalid, want, group, len(hosts))
	}

	seen := make(map[string]bool, len(hosts))

	for _, host := range hosts {
		if seen[host.Instance()] {
			return fmt.Errorf("%w: instance %s of group %s appears twice", ErrSSHGroupHostsInvalid, host.Instance(), group)
		}

		seen[host.Instance()] = true
	}

	return nil
}

// DialSSHHost connects to one host of session as the session's user. The
// connection accepts only the host key the director reported for that
// host, so a caller holding a group session reaches each VM in turn with
// the same pinning a single-instance session gets.
func (d *DirectorAdapter) DialSSHHost(ctx context.Context, session *sshconn.Session, host sshconn.Host) (*ssh.Client, error) {
	if session == nil || session.Signer == nil {
		return nil, ErrSSHSessionMissing
	}

	config, err := sshconn.HostConfig(session.Deployment, host, session.Signer)
	if err != nil {
		d.log.Errorf("Refused to connect to %s of deployment %s: %v", host.Instance(), session.Deployment, err)

		return nil, fmt.Errorf("failed to connect to host: %w", err)
	}

	config.Timeout = httpClientTimeout

	targetAddr := net.JoinHostPort(host.Address, d.sshPort)

	client, err := sshconn.DialDirect(ctx, targetAddr, config)
	if err != nil {
		err = sshconn.ExplainHostKeyRefusal(err, session.Deployment, host, targetAddr)
		d.log.Errorf("Failed to connect to %s of deployment %s at %s: %v", host.Instance(), session.Deployment, targetAddr, err)

		return nil, fmt.Errorf("failed to connect to host: %w", err)
	}

	return client, nil
}
