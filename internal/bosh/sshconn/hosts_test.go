package sshconn_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"blacksmith/internal/bosh/sshconn"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
	"golang.org/x/crypto/ssh"
)

const (
	hostsTestUser = "bosh_0123456789abcde"
	hostsTestJob  = "redis"
	statusSuccess = "success"
	firstHostIP   = "10.0.0.5"
	secondHostIP  = "10.0.0.6"
	// hostsTestDeployment is the deployment the pinning tests name.
	hostsTestDeployment = "redis-1"
	firstHostID         = "abc"
	secondHostID        = "def"
	// gatewayTestUser and gatewayTestHost name the gateway a director's
	// default_ssh_options can carry.
	gatewayTestUser = "jumpbox"
	gatewayTestHost = "jumpbox.example.com"
	// firstHostInstance is the first host's instance name, job/id.
	firstHostInstance = hostsTestJob + "/" + firstHostID
)

func intPointer(value int) *int { return &value }

// authorizedKey renders a host key the way the director reports it, with
// the comment the agent's .pub file carries.
func authorizedKey(key ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " root@vm"
}

// A result with one success entry per instance becomes one host per entry,
// each logged in as the setup's user, with the host key the director
// reported for it.
func TestReadHostsReturnsEveryHost(t *testing.T) {
	t.Parallel()

	keys := hostKeys(t)

	results := []boshapi.SSHResult{
		{Status: statusSuccess, IP: firstHostIP, HostPublicKey: authorizedKey(keys.rsa.PublicKey()), Job: hostsTestJob, Index: intPointer(0), ID: firstHostID},
		{Status: statusSuccess, IP: secondHostIP, HostPublicKey: authorizedKey(keys.ecdsa.PublicKey()), Job: hostsTestJob, Index: intPointer(1), ID: secondHostID},
	}

	hosts, err := sshconn.ReadHosts(results, hostsTestUser)
	if err != nil {
		t.Fatalf("ReadHosts: %v", err)
	}

	want := []sshconn.Host{
		{Address: firstHostIP, Username: hostsTestUser, HostKey: keys.rsa.PublicKey(), Job: hostsTestJob, IndexOrID: firstHostID},
		{Address: secondHostIP, Username: hostsTestUser, HostKey: keys.ecdsa.PublicKey(), Job: hostsTestJob, IndexOrID: secondHostID},
	}

	if len(hosts) != len(want) {
		t.Fatalf("got %d hosts, want %d: %+v", len(hosts), len(want), hosts)
	}

	for index := range want {
		got, wanted := hosts[index], want[index]
		if got.Address != wanted.Address || got.Username != wanted.Username || got.Job != wanted.Job || got.IndexOrID != wanted.IndexOrID {
			t.Errorf("host %d is %+v, want %+v", index, got, wanted)
		}

		if got.HostKey == nil || !bytes.Equal(got.HostKey.Marshal(), wanted.HostKey.Marshal()) {
			t.Errorf("host %d has host key %v, want the one the director reported", index, got.HostKey)
		}
	}
}

// An instance with no ID is named by its index.
func TestReadHostsNamesAnInstanceWithoutAnIDByItsIndex(t *testing.T) {
	t.Parallel()

	hosts, err := sshconn.ReadHosts([]boshapi.SSHResult{
		{Status: statusSuccess, IP: firstHostIP, HostPublicKey: authorizedKey(hostKeys(t).ed25519.PublicKey()), Job: hostsTestJob, Index: intPointer(3)},
	}, hostsTestUser)
	if err != nil {
		t.Fatalf("ReadHosts: %v", err)
	}

	if hosts[0].IndexOrID != "3" {
		t.Errorf("IndexOrID %q, want %q", hosts[0].IndexOrID, "3")
	}
}

// A gateway user without a gateway host names no gateway, so the result
// is read as one that reaches its hosts directly.
func TestReadHostsIgnoresAGatewayUserWithoutAHost(t *testing.T) {
	t.Parallel()

	hosts, err := sshconn.ReadHosts([]boshapi.SSHResult{
		{Status: statusSuccess, IP: firstHostIP, HostPublicKey: authorizedKey(hostKeys(t).ed25519.PublicKey()), Job: hostsTestJob, ID: firstHostID, GatewayUser: gatewayTestUser},
	}, hostsTestUser)
	if err != nil {
		t.Fatalf("ReadHosts: %v", err)
	}

	if len(hosts) != 1 || hosts[0].Address != firstHostIP {
		t.Errorf("hosts %+v, want %s alone", hosts, firstHostIP)
	}
}

// A result Blacksmith cannot connect with is refused, and the error says
// why.
func TestReadHostsRefusesAResultItCannotUse(t *testing.T) {
	t.Parallel()

	hostKey := authorizedKey(hostKeys(t).ed25519.PublicKey())

	cases := map[string]struct {
		results []boshapi.SSHResult
		want    error
		says    []string
	}{
		"empty": {
			results: []boshapi.SSHResult{},
			want:    sshconn.ErrNoHosts,
			says:    []string{hostsTestUser},
		},
		"failed entry": {
			results: []boshapi.SSHResult{
				{Status: statusSuccess, IP: firstHostIP, HostPublicKey: hostKey, Job: hostsTestJob, Index: intPointer(0)},
				{Status: "error", Job: hostsTestJob, Index: intPointer(1), ID: secondHostID},
			},
			want: sshconn.ErrSetupFailed,
			says: []string{"redis/def", `"error"`, hostsTestUser},
		},
		"entry without an address": {
			results: []boshapi.SSHResult{{Status: statusSuccess, HostPublicKey: hostKey, Job: hostsTestJob, Index: intPointer(0)}},
			want:    sshconn.ErrSetupFailed,
			says:    []string{"redis/0", "no IP address"},
		},
		"entry without a host key": {
			results: []boshapi.SSHResult{
				{Status: statusSuccess, IP: firstHostIP, HostPublicKey: hostKey, Job: hostsTestJob, Index: intPointer(0), ID: firstHostID},
				{Status: statusSuccess, IP: secondHostIP, Job: hostsTestJob, Index: intPointer(1), ID: secondHostID},
			},
			want: sshconn.ErrHostKeyUnusable,
			says: []string{"redis/def", "no host key", hostsTestUser},
		},
		"entry that names a gateway": {
			results: []boshapi.SSHResult{
				{Status: statusSuccess, IP: firstHostIP, HostPublicKey: hostKey, Job: hostsTestJob, Index: intPointer(0), ID: firstHostID},
				{Status: statusSuccess, IP: secondHostIP, HostPublicKey: hostKey, Job: hostsTestJob, Index: intPointer(1), ID: secondHostID, GatewayUser: gatewayTestUser, GatewayHost: gatewayTestHost},
			},
			want: sshconn.ErrGatewayUnsupported,
			says: []string{
				"redis/def", hostsTestUser, gatewayTestUser + "@" + gatewayTestHost, "default_ssh_options",
				"does not support SSH through a gateway", "refuse the throwaway key",
			},
		},
		"entry that names a gateway host without a user": {
			results: []boshapi.SSHResult{
				{Status: statusSuccess, IP: firstHostIP, HostPublicKey: hostKey, Job: hostsTestJob, Index: intPointer(0), ID: firstHostID, GatewayHost: gatewayTestHost},
			},
			want: sshconn.ErrGatewayUnsupported,
			says: []string{firstHostInstance, "gateway " + gatewayTestHost + " ", "default_ssh_options"},
		},
		"entry whose host key does not parse": {
			results: []boshapi.SSHResult{
				{Status: statusSuccess, IP: firstHostIP, HostPublicKey: "ssh-rsa not-base64", Job: hostsTestJob, Index: intPointer(0), ID: firstHostID},
			},
			want: sshconn.ErrHostKeyUnusable,
			says: []string{firstHostInstance, "does not parse", hostsTestUser},
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			hosts, err := sshconn.ReadHosts(testCase.results, hostsTestUser)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("got hosts %+v and error %v, want an error that wraps %v", hosts, err, testCase.want)
			}

			for _, part := range testCase.says {
				if !strings.Contains(err.Error(), part) {
					t.Errorf("error %q does not say %q", err, part)
				}
			}
		})
	}
}

// Setup sends the command, the deployment, the single-instance target, and
// the user with its key, in the director's field names.
func TestSetupRequestIsTheDirectorsBody(t *testing.T) {
	t.Parallel()

	target := boshapi.SSHTarget{Job: hostsTestJob, Indices: []int{2}}

	body, err := json.Marshal(sshconn.SetupRequest("redis-1", target, hostsTestUser, "ssh-ed25519 AAAA"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	want := `{"command":"setup","deployment_name":"redis-1","target":{"job":"redis","indexes":[2],"ids":[]},` +
		`"params":{"user":"bosh_0123456789abcde","public_key":"ssh-ed25519 AAAA"}}`
	if string(body) != want {
		t.Errorf("body\n%s\nwant\n%s", body, want)
	}
}

// Cleanup sends the same target and a pattern that matches the user alone.
func TestCleanupRequestMatchesOnlyItsUser(t *testing.T) {
	t.Parallel()

	target := boshapi.SSHTarget{Job: hostsTestJob, Indices: []int{2}}

	request := sshconn.CleanupRequest("redis-1", target, hostsTestUser)

	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	want := `{"command":"cleanup","deployment_name":"redis-1","target":{"job":"redis","indexes":[2],"ids":[]},` +
		`"params":{"user_regex":"^bosh_0123456789abcde$"}}`
	if string(body) != want {
		t.Errorf("body\n%s\nwant\n%s", body, want)
	}

	pattern := regexp.MustCompile(request.Params.UserRegex)

	for user, want := range map[string]bool{
		hostsTestUser:         true,
		hostsTestUser + "f":   false,
		"x" + hostsTestUser:   false,
		"bosh_0123456789abcd": false,
	} {
		if got := pattern.MatchString(user); got != want {
			t.Errorf("pattern %q matches %q: %v, want %v", request.Params.UserRegex, user, got, want)
		}
	}
}

// A target reads the way an operator names an instance.
func TestTargetNameReadsAsTheInstance(t *testing.T) {
	t.Parallel()

	for target, want := range map[string]struct {
		target boshapi.SSHTarget
		name   string
	}{
		"index":    {boshapi.SSHTarget{Job: hostsTestJob, Indices: []int{2}}, "redis/2"},
		"id":       {boshapi.SSHTarget{Job: hostsTestJob, IDs: []string{firstHostID}}, firstHostInstance},
		"group":    {boshapi.SSHTarget{Job: hostsTestJob}, hostsTestJob},
		"two":      {boshapi.SSHTarget{Job: hostsTestJob, Indices: []int{0, 1}}, "redis/0,1"},
		"anywhere": {boshapi.SSHTarget{}, "every instance"},
	} {
		if got := sshconn.TargetName(want.target); got != want.name {
			t.Errorf("%s: TargetName %q, want %q", target, got, want.name)
		}
	}
}
