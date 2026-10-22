package certscripts_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/upgrade/certscripts"
)

// healthyClusterInfo is the part of CLUSTER INFO that the gate keeps,
// for a healthy cluster of three nodes.
const healthyClusterInfo = "cluster_state:ok\n" +
	"cluster_slots_assigned:16384\n" +
	"cluster_slots_ok:16384\n" +
	"cluster_slots_pfail:0\n" +
	"cluster_slots_fail:0\n" +
	"cluster_known_nodes:3\n" +
	"cluster_size:3\n"

// rabbitFixture is cluster_status from RabbitMQ 3.13 with the JSON
// formatter, for three disc nodes that are all running.
const rabbitFixture = "testdata/rabbitmq-3.13-cluster-status.json"

func readRabbitFixture(t *testing.T) []byte {
	t.Helper()

	body, err := os.ReadFile(rabbitFixture)
	if err != nil {
		t.Fatal(err)
	}

	return body
}

// editRabbitStatus changes one field of the fixture.
func editRabbitStatus(t *testing.T, key string, value any) []byte {
	t.Helper()

	var doc map[string]any

	err := json.Unmarshal(readRabbitFixture(t), &doc)
	if err != nil {
		t.Fatal(err)
	}

	doc[key] = value

	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	return body
}

func TestParseClusterInfo(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		out     string
		vms     int
		healthy bool
	}{
		{"a healthy cluster", healthyClusterInfo, 3, true},
		{"a failed cluster", strings.Replace(healthyClusterInfo, "cluster_state:ok", "cluster_state:fail", 1), 3, false},
		{"slots not ok", strings.Replace(healthyClusterInfo, "cluster_slots_ok:16384", "cluster_slots_ok:16000", 1), 3, false},
		{"slots not assigned", strings.Replace(healthyClusterInfo, "cluster_slots_assigned:16384", "cluster_slots_assigned:8192", 1), 3, false},
		{"two known nodes of three", strings.Replace(healthyClusterInfo, "cluster_known_nodes:3", "cluster_known_nodes:2", 1), 3, false},
		{"more known nodes than VMs", healthyClusterInfo, 2, false},
		{"no state", strings.Replace(healthyClusterInfo, "cluster_state:ok\n", "", 1), 3, false},
		{"no known nodes", strings.Replace(healthyClusterInfo, "cluster_known_nodes:3\n", "", 1), 3, false},
		{"empty output", "", 3, false},
		{"a repeated key", healthyClusterInfo + "cluster_state:ok\n", 3, false},
		{"a stray line", healthyClusterInfo + "NOAUTH Authentication required.\n", 3, false},
		{"no VMs", healthyClusterInfo, 0, false},
	}

	for _, test := range cases {
		err := certscripts.ParseClusterInfo([]byte(test.out), test.vms)
		if (err == nil) != test.healthy {
			t.Errorf("%s: ParseClusterInfo = %v, want healthy %v", test.name, err, test.healthy)
		}

		if err != nil && strings.Contains(err.Error(), "NOAUTH") {
			t.Errorf("%s: the error repeats the output: %v", test.name, err)
		}
	}
}

func TestParseRabbitStatus(t *testing.T) {
	t.Parallel()

	err := certscripts.ParseRabbitStatus(0, readRabbitFixture(t))
	if err != nil {
		t.Fatalf("a healthy cluster failed: %v", err)
	}

	if certscripts.ParseRabbitStatus(69, readRabbitFixture(t)) == nil {
		t.Error("a failed check_running passed")
	}

	stopped := editRabbitStatus(t, "running_nodes", []string{
		"rabbit@q0.rabbitmq.default.rabbitmq-1.bosh",
		"rabbit@q2.rabbitmq.default.rabbitmq-1.bosh",
	})
	if certscripts.ParseRabbitStatus(0, stopped) == nil {
		t.Error("a cluster with a stopped disc node passed")
	}

	if certscripts.ParseRabbitStatus(0, editRabbitStatus(t, "disk_nodes", []string{})) == nil {
		t.Error("a cluster with no disc nodes passed")
	}

	err = certscripts.ParseRabbitStatus(0, []byte("Error: unable to perform an operation on node 'rabbit@secret-host'"))
	if err == nil {
		t.Fatal("output that is not JSON passed")
	}

	if strings.Contains(err.Error(), "secret-host") {
		t.Errorf("the error repeats the output: %v", err)
	}
}

func TestParseRabbitGate(t *testing.T) {
	t.Parallel()

	status := string(readRabbitFixture(t))

	err := certscripts.ParseRabbitGate([]byte("check_running exit=0\ncluster_status exit=0\n" + status))
	if err != nil {
		t.Fatalf("a healthy gate failed: %v", err)
	}

	bad := []string{
		"check_running exit=1\ncluster_status exit=0\n" + status,
		"check_running exit=0\ncluster_status exit=69\n" + status,
		"cluster_status exit=0\ncheck_running exit=0\n" + status,
		"check_running exit=0\n",
		"",
		"check_running exit=x\ncluster_status exit=0\n" + status,
	}

	for _, out := range bad {
		if certscripts.ParseRabbitGate([]byte(out)) == nil {
			t.Errorf("gate output %.40q passed", out)
		}
	}
}

func TestGateFor(t *testing.T) {
	t.Parallel()

	const (
		redis    = "redis"
		postgres = "postgresql"
	)

	cases := []struct {
		service string
		vms     int
		want    string
	}{
		{redis, 3, certscripts.GateRedisCluster},
		{"Redis", 6, certscripts.GateRedisCluster},
		{"valkey", 3, certscripts.GateValkeyCluster},
		{"VALKEY", 2, certscripts.GateValkeyCluster},
		{"rabbitmq", 3, certscripts.GateRabbitMQCluster},
		{redis, 1, certscripts.GateSingleVM},
		{postgres, 1, certscripts.GateSingleVM},
		{postgres, 3, ""},
		{"", 3, ""},
		{redis, 0, ""},
		{redis, -1, ""},
	}

	gates := map[string]string{
		certscripts.GateSingleVM:        "single-vm",
		certscripts.GateRedisCluster:    "redis-cluster",
		certscripts.GateValkeyCluster:   "valkey-cluster",
		certscripts.GateRabbitMQCluster: "rabbitmq-cluster",
	}

	for constant, name := range gates {
		if constant != name {
			t.Errorf("gate constant %q, want %q", constant, name)
		}
	}

	for _, test := range cases {
		if got := certscripts.GateFor(test.service, test.vms); got != test.want {
			t.Errorf("GateFor(%q, %d) = %q, want %q", test.service, test.vms, got, test.want)
		}
	}
}

func TestScriptGate(t *testing.T) {
	t.Parallel()

	body, err := certscripts.Script("gate")
	if err != nil {
		t.Fatalf("Script(gate): %v", err)
	}

	if body != readSource(t, libScript)+readSource(t, "gate") {
		t.Error("Script(gate) is not lib.sh followed by gate.sh")
	}
}

// gatePassword is all digits, so a value that echoes it would pass the
// gate's line filter for a numeric field, and only the password check
// stops it.
const gatePassword = "20261022"

// fakeCLI logs its arguments and environment beside itself, prints its
// environment, and then prints CLUSTER INFO with CRLF endings and one
// line that carries the password.
const fakeCLI = `#!/bin/sh
dir=${0%/*}
printf '%s\n' "$@" > "$dir/argv.log"
env > "$dir/env.log"
env
printf 'cluster_state:ok\r\ncluster_slots_assigned:16384\r\ncluster_slots_ok:16384\r\n'
printf 'cluster_known_nodes:3\r\ncluster_size:%s\r\n' "$REDISCLI_AUTH"
`

// newGateTree makes a root with a rendered config and a fake CLI.
func newGateTree(t *testing.T, config, conf, cli string) *vmTree {
	t.Helper()
	requireRoot(t)

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	tree := &vmTree{root: root}
	tree.mkdir(t, filepath.Dir(config))
	tree.write(t, config, conf)
	tree.mkdir(t, filepath.Dir(cli))
	tree.writeMode(t, cli, fakeCLI, 0o755)

	return tree
}

func readLog(t *testing.T, path string) string {
	t.Helper()

	body, err := os.ReadFile(path) //nolint:gosec // a log the fake wrote under the test root
	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}

func TestGateNeverPrintsPassword(t *testing.T) {
	t.Parallel()

	const (
		config = "/var/vcap/jobs/cluster/config/valkey.conf"
		caFile = "/var/vcap/jobs/cluster/config/tls/valkey.ca"
		cli    = "/var/vcap/packages/valkey-9/bin/valkey-cli"
	)

	conf := "requirepass " + gatePassword + "\r\n" +
		"masterauth " + gatePassword + "\n" +
		"port 0\n" +
		"tls-port 6379\n" +
		"tls-ca-cert-file " + caFile + "\n"

	tree := newGateTree(t, config, conf, cli)

	out, code := runScript(t, tree.root, nil, "gate", "valkey-cluster")
	if code != 0 {
		t.Fatalf("gate exited %d, want 0", code)
	}

	if strings.Contains(out, gatePassword) {
		t.Errorf("the gate printed the password:\n%s", out)
	}

	want := "cluster_state:ok\ncluster_slots_assigned:16384\ncluster_slots_ok:16384\ncluster_known_nodes:3\n"
	if out != want {
		t.Errorf("output = %q, want %q", out, want)
	}

	err := certscripts.ParseClusterInfo([]byte(out), 3)
	if err != nil {
		t.Errorf("ParseClusterInfo: %v", err)
	}

	argv := readLog(t, tree.at(filepath.Dir(cli)+"/argv.log"))
	if strings.Contains(argv, gatePassword) {
		t.Error("the password is on the CLI's command line")
	}

	wantArgv := strings.Join([]string{"--tls", "--cacert", tree.at(caFile), "-h", "127.0.0.1", "-p", "6379", "CLUSTER", "INFO"}, "\n") + "\n"
	if argv != wantArgv {
		t.Errorf("argv = %q, want %q", argv, wantArgv)
	}

	env := readLog(t, tree.at(filepath.Dir(cli)+"/env.log"))
	for _, name := range []string{"REDISCLI_AUTH", "VALKEYCLI_AUTH"} {
		if !strings.Contains(env, "\n"+name+"="+gatePassword+"\n") && !strings.HasPrefix(env, name+"="+gatePassword+"\n") {
			t.Errorf("the CLI's environment has no %s with the password", name)
		}
	}
}

func TestGateRedisPlainPort(t *testing.T) {
	t.Parallel()

	const (
		config = "/var/vcap/jobs/cluster/config/redis.conf"
		cli    = "/var/vcap/packages/redis/bin/redis-cli"
	)

	tree := newGateTree(t, config, "requirepass "+gatePassword+"\nprotected-mode no\n", cli)

	out, code := runScript(t, tree.root, nil, "gate", "redis-cluster")
	if code != 0 {
		t.Fatalf("gate exited %d, want 0", code)
	}

	if strings.Contains(out, gatePassword) {
		t.Errorf("the gate printed the password:\n%s", out)
	}

	argv := readLog(t, tree.at(filepath.Dir(cli)+"/argv.log"))
	if want := "-h\n127.0.0.1\n-p\n6379\nCLUSTER\nINFO\n"; argv != want {
		t.Errorf("argv = %q, want %q", argv, want)
	}
}

// TestGatePicksHighestVersion shows that with several versions of the
// CLI's package installed, the gate runs the highest version, which is
// valkey-10 here and not valkey-9, the last in the glob's order.
func TestGatePicksHighestVersion(t *testing.T) {
	t.Parallel()

	const (
		config = "/var/vcap/jobs/cluster/config/valkey.conf"
		newest = "/var/vcap/packages/valkey-10/bin/valkey-cli"
		older  = "/var/vcap/packages/valkey-9/bin/valkey-cli"
	)

	tree := newGateTree(t, config, "port 6379\n", newest)
	tree.mkdir(t, filepath.Dir(older))
	tree.writeMode(t, older, fakeCLI, 0o755)

	if _, code := runScript(t, tree.root, nil, "gate", "valkey-cluster"); code != 0 {
		t.Fatalf("gate exited %d, want 0", code)
	}

	if !tree.exists(t, filepath.Dir(newest)+"/argv.log") {
		t.Error("the gate did not run the valkey-10 CLI")
	}

	if tree.exists(t, filepath.Dir(older)+"/argv.log") {
		t.Error("the gate ran the valkey-9 CLI")
	}
}

func TestGateRefusesUnknownGate(t *testing.T) {
	t.Parallel()

	tree := newGateTree(t, "/var/vcap/jobs/cluster/config/redis.conf", "requirepass x\n", "/var/vcap/packages/redis/bin/redis-cli")

	for _, gate := range []string{"single-vm", "postgresql-cluster", ""} {
		if _, code := runScript(t, tree.root, nil, "gate", gate); code != usageExit {
			t.Errorf("gate %q exited %d, want 2", gate, code)
		}
	}

	if _, code := runScript(t, tree.root, nil, "gate", "redis-cluster", "extra"); code != usageExit {
		t.Errorf("gate with an extra argument exited %d, want 2", code)
	}
}

// fakeDiagnostics logs each call with the settings it saw and prints the
// status fixture that sits beside it.
const fakeDiagnostics = `#!/bin/sh
dir=${0%/*}
echo "$1 HOME=$HOME NODE=$RABBITMQ_NODENAME LONG=$RABBITMQ_USE_LONGNAME" >> "$dir/calls.log"
case "$1" in
check_running)
	echo "Checking if RabbitMQ is running on node $RABBITMQ_NODENAME ..."
	;;
cluster_status)
	cat "$dir/status.json"
	;;
*)
	exit 64
	;;
esac
`

func TestGateRabbitMQ(t *testing.T) {
	t.Parallel()
	requireRoot(t)

	const (
		sbin = "/var/vcap/packages/rabbitmq/sbin"
		node = "rabbit@q0.rabbitmq.default.rabbitmq-1.bosh"
		pass = "rabbit-admin-pass"
	)

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	tree := &vmTree{root: root}
	tree.mkdir(t, sbin)
	tree.mkdir(t, "/var/vcap/jobs/rabbitmq")
	tree.mkdir(t, "/home/vcap")
	tree.writeMode(t, sbin+"/rabbitmq-diagnostics", fakeDiagnostics, 0o755)
	tree.write(t, sbin+"/status.json", string(readRabbitFixture(t)))
	tree.write(t, "/var/vcap/jobs/rabbitmq/env", "#!/bin/bash\n"+
		"export HOME=\"${HOME:-/home/vcap}\"\n"+
		"export RABBITMQ_ADMIN_PASS=\""+pass+"\"\n"+
		"export RABBITMQ_NODENAME=\""+node+"\"\n")

	out, code := runScript(t, tree.root, nil, "gate", "rabbitmq-cluster")
	if code != 0 {
		t.Fatalf("gate exited %d, want 0", code)
	}

	if strings.Contains(out, pass) {
		t.Error("the gate printed a password from the job's env file")
	}

	err = certscripts.ParseRabbitGate([]byte(out))
	if err != nil {
		t.Errorf("ParseRabbitGate: %v\noutput:\n%s", err, out)
	}

	calls := readLog(t, tree.at(sbin+"/calls.log"))
	settings := " HOME=" + tree.at("/home/vcap") + " NODE=" + node + " LONG=true\n"

	if want := "check_running" + settings + "cluster_status" + settings; calls != want {
		t.Errorf("calls = %q, want %q", calls, want)
	}
}
