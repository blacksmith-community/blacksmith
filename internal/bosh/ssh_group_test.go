package bosh_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"reflect"
	"strconv"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/bosh/sshconn"
	"golang.org/x/crypto/ssh"
)

// Values the group SSH tests share.
const sshGroup = "valkey"

// groupSetupOutput is a setup result with one success entry on 127.0.0.1
// for each index of the valkey group. Each entry reports the key that
// hostKey returns for its index.
func groupSetupOutput(indices []int, hostKey func(index int) ssh.PublicKey) string {
	entries := make([]boshtest.SSHHostEntry, 0, len(indices))

	for _, index := range indices {
		entries = append(entries, boshtest.SSHHostEntry{
			Status:        "success",
			IP:            loopbackHost,
			HostPublicKey: boshtest.AuthorizedKey(hostKey(index)),
			Job:           sshGroup,
			Index:         index,
			ID:            "00000000-0000-0000-0000-00000000000" + strconv.Itoa(index+1),
		})
	}

	return boshtest.SSHSetupOutput(entries...)
}

// newHostKey makes a throwaway ed25519 host key.
func newHostKey(t *testing.T) ssh.Signer {
	t.Helper()

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate a host key: %v", err)
	}

	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("make a host signer: %v", err)
	}

	return signer
}

// One setup names the group and every index, and the session it returns
// holds one host for each instance.
func TestSSHGroupSessionOneSetupForThreeIndices(t *testing.T) {
	t.Parallel()

	indices := []int{0, 1, 2}

	var server *boshtest.SSHDirector

	server, director, _ := newSSHAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		if call.Command == boshtest.SSHSetup {
			return boshtest.SSHAnswer{Output: groupSetupOutput(indices, func(int) ssh.PublicKey { return server.HostKey.PublicKey() })}
		}

		return boshtest.SSHAnswer{}
	})

	var session *sshconn.Session

	err := callWithin(t, sshCallLimit, func() error {
		var err error

		session, err = director.SSHGroupSession(t.Context(), sshDeployment, sshGroup, indices)

		return err //nolint:wrapcheck // the test asserts on the adapter's own error
	})
	if err != nil {
		t.Fatalf("SSHGroupSession: %v", err)
	}

	setup := onlyCall(t, server, boshtest.SSHSetup)

	target, _ := setup.Fields["target"].(map[string]interface{})
	if target["job"] != sshGroup {
		t.Errorf("the setup names group %v, want %q", target["job"], sshGroup)
	}

	if want := []interface{}{0.0, 1.0, 2.0}; !reflect.DeepEqual(target["indexes"], want) {
		t.Errorf("the setup names indexes %v, want %v", target["indexes"], want)
	}

	if session.Target.Job != sshGroup || !reflect.DeepEqual(session.Target.Indices, indices) {
		t.Errorf("session target %+v, want group %s and indices %v", session.Target, sshGroup, indices)
	}

	if len(session.Hosts) != len(indices) {
		t.Fatalf("the session has %d hosts, want %d", len(session.Hosts), len(indices))
	}

	for _, host := range session.Hosts {
		if host.Username != setup.User() || host.Job != sshGroup {
			t.Errorf("host %+v, want user %s on group %s", host, setup.User(), sshGroup)
		}
	}

	if session.Cleanup == nil {
		t.Fatal("the session carries no cleanup")
	}

	err = session.Cleanup(t.Context())
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	cleanup := onlyCall(t, server, boshtest.SSHCleanup)

	cleanupTarget, _ := cleanup.Fields["target"].(map[string]interface{})
	if !reflect.DeepEqual(cleanupTarget, target) {
		t.Errorf("the cleanup names target %v, want the setup's %v", cleanupTarget, target)
	}
}

// An empty group would select every instance in the deployment, so it is
// refused before anything is sent.
func TestSSHGroupSessionRefusesEmptyGroup(t *testing.T) {
	t.Parallel()

	server, director, _ := newSSHAdapter(t, nil)

	session, err := director.SSHGroupSession(t.Context(), sshDeployment, "", []int{0, 1})
	if !errors.Is(err, bosh.ErrSSHGroupTargetInvalid) {
		t.Errorf("SSHGroupSession returned %v, want ErrSSHGroupTargetInvalid", err)
	}

	if session != nil {
		t.Errorf("SSHGroupSession returned session %+v with its error", session)
	}

	if calls := server.Calls(); len(calls) != 0 {
		t.Errorf("%d SSH requests were sent, want none", len(calls))
	}
}

// A target with no index, a negative index, or the same index twice is
// refused before anything is sent.
func TestSSHGroupSessionRefusesNoIndices(t *testing.T) {
	t.Parallel()

	server, director, _ := newSSHAdapter(t, nil)

	for name, indices := range map[string][]int{
		"nil":            nil,
		"empty":          {},
		"negative index": {0, -1},
		"repeated index": {1, 1},
	} {
		session, err := director.SSHGroupSession(t.Context(), sshDeployment, sshGroup, indices)
		if !errors.Is(err, bosh.ErrSSHGroupTargetInvalid) {
			t.Errorf("%s: SSHGroupSession returned %v, want ErrSSHGroupTargetInvalid", name, err)
		}

		if session != nil {
			t.Errorf("%s: SSHGroupSession returned session %+v with its error", name, session)
		}
	}

	if calls := server.Calls(); len(calls) != 0 {
		t.Errorf("%d SSH requests were sent, want none", len(calls))
	}
}

// A setup result that does not hold exactly one host for each requested
// index, or that names one instance twice, gives no session, and the user
// the setup created is removed as after any other failed setup.
func TestSSHGroupSessionRefusesHostsThatDoNotMatch(t *testing.T) {
	t.Parallel()

	hostKey := newHostKey(t).PublicKey()
	sameKey := func(int) ssh.PublicKey { return hostKey }

	for name, output := range map[string]string{
		"one host short":     groupSetupOutput([]int{0, 1}, sameKey),
		"one host too many":  groupSetupOutput([]int{0, 1, 2, 3}, sameKey),
		"one instance twice": groupSetupOutput([]int{0, 1, 1}, sameKey),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			server, director, store, _ := newFailedSetupAdapter(t, func(boshtest.SSHCall) boshtest.SSHAnswer {
				return boshtest.SSHAnswer{Output: output}
			})

			var session *sshconn.Session

			err := callWithin(t, sshCallLimit, func() error {
				var err error

				session, err = director.SSHGroupSession(t.Context(), sshDeployment, sshGroup, []int{0, 1, 2})

				return err //nolint:wrapcheck // the test asserts on the adapter's own error
			})
			if !errors.Is(err, bosh.ErrSSHGroupHostsInvalid) {
				t.Errorf("SSHGroupSession returned %v, want ErrSSHGroupHostsInvalid", err)
			}

			if session != nil {
				t.Errorf("SSHGroupSession returned session %+v with its error", session)
			}

			setup := onlyCall(t, server, boshtest.SSHSetup)
			awaitCleanup(t, server, setup)
			awaitCleared(t, store, setup.User())
		})
	}
}

// DialSSHHost connects to the host it is given, as the session's user,
// and accepts only the host key the director reported for that host.
func TestDialSSHHostPinsTheGivenHost(t *testing.T) {
	t.Parallel()

	indices := []int{0, 1}
	other := newHostKey(t)

	var server *boshtest.SSHDirector

	server, director, _ := newSSHAdapter(t, func(call boshtest.SSHCall) boshtest.SSHAnswer {
		if call.Command == boshtest.SSHSetup {
			// Only index 1 reports the key the test VM presents.
			return boshtest.SSHAnswer{Output: groupSetupOutput(indices, func(index int) ssh.PublicKey {
				if index == 1 {
					return server.HostKey.PublicKey()
				}

				return other.PublicKey()
			})}
		}

		return boshtest.SSHAnswer{}
	})

	vmServer := startExecServer(t, func(user string, _ ssh.PublicKey) bool {
		setups := server.CallsOf(boshtest.SSHSetup)

		return len(setups) == 1 && user == setups[0].User()
	}, server.HostKey)
	bosh.SetSSHPort(director, vmServer.port)

	var session *sshconn.Session

	err := callWithin(t, sshCallLimit, func() error {
		var err error

		session, err = director.SSHGroupSession(t.Context(), sshDeployment, sshGroup, indices)

		return err //nolint:wrapcheck // the test asserts on the adapter's own error
	})
	if err != nil {
		t.Fatalf("SSHGroupSession: %v", err)
	}

	t.Cleanup(func() { _ = session.Cleanup(t.Context()) })

	client, err := director.DialSSHHost(t.Context(), session, session.Hosts[1])
	if err != nil {
		t.Fatalf("DialSSHHost on the pinned host: %v", err)
	}

	defer func() { _ = client.Close() }()

	remote, err := client.NewSession()
	if err != nil {
		t.Fatalf("open a session: %v", err)
	}

	output, err := remote.Output("true")
	_ = remote.Close()

	if err != nil || string(output) != "ran: true\n" {
		t.Errorf("the command returned %q and %v, want it to run", output, err)
	}

	_, err = director.DialSSHHost(t.Context(), session, session.Hosts[0])
	if !errors.Is(err, sshconn.ErrHostKeyMismatch) {
		t.Errorf("DialSSHHost on a host whose key the VM does not present returned %v, want ErrHostKeyMismatch", err)
	}
}

// DialSSHHost refuses a missing session before it dials anything.
func TestDialSSHHostRefusesNoSession(t *testing.T) {
	t.Parallel()

	_, director, _ := newSSHAdapter(t, nil)

	client, err := director.DialSSHHost(t.Context(), nil, sshconn.Host{Address: loopbackHost})
	if !errors.Is(err, bosh.ErrSSHSessionMissing) || client != nil {
		t.Errorf("DialSSHHost with no session returned %v and %v, want bosh.ErrSSHSessionMissing", client, err)
	}
}
