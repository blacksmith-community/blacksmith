package certscripts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The health gates. Patch mode runs a cluster's gate on each VM between
// restarts. A single VM needs no gate, and a multi-VM instance of a
// service with no gate cannot be patched.
const (
	GateSingleVM        = "single-vm"
	GateRedisCluster    = "redis-cluster"
	GateValkeyCluster   = "valkey-cluster"
	GateRabbitMQCluster = "rabbitmq-cluster"
)

// GateFor returns the health gate of an instance of service with vmCount
// VMs. One VM gives GateSingleVM. More than one gives the service's
// cluster gate, matched on its name in any case, or empty when the
// service has none. A count below one gives empty, because nothing can
// be patched.
func GateFor(service string, vmCount int) string {
	switch {
	case vmCount == 1:
		return GateSingleVM
	case vmCount < 1:
		return ""
	}

	switch strings.ToLower(service) {
	case "redis":
		return GateRedisCluster
	case "valkey":
		return GateValkeyCluster
	case "rabbitmq":
		return GateRabbitMQCluster
	default:
		return ""
	}
}

// ErrGateFailed is the error the gate parsers return for a cluster that
// is not healthy, or for gate output they cannot read. Their errors
// never repeat the output.
var ErrGateFailed = errors.New("certscripts: the health gate failed")

// clusterSlots is the number of hash slots in a Redis or Valkey cluster.
const clusterSlots = "16384"

// clusterLine is the form of a line that gate.sh keeps from CLUSTER INFO.
var clusterLine = regexp.MustCompile(`^(cluster_[a-z_]+):([0-9a-z]+)$`)

// ParseClusterInfo reads what gate.sh prints for a Redis or Valkey
// cluster, which is lines of CLUSTER INFO. It passes when
// cluster_state is ok, all 16384 slots are assigned and ok, and
// cluster_known_nodes equals vmCount. Any other line, a repeated key, or
// a missing key fails.
func ParseClusterInfo(out []byte, vmCount int) error {
	if vmCount < 1 {
		return fmt.Errorf("%w: the instance has no VMs", ErrGateFailed)
	}

	info := make(map[string]string)

	for index, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}

		match := clusterLine.FindStringSubmatch(line)
		if match == nil {
			return fmt.Errorf("%w: line %d is not a CLUSTER INFO field", ErrGateFailed, index+1)
		}

		if _, seen := info[match[1]]; seen {
			return fmt.Errorf("%w: line %d repeats %s", ErrGateFailed, index+1, match[1])
		}

		info[match[1]] = match[2]
	}

	want := []struct{ key, value string }{
		{"cluster_state", "ok"},
		{"cluster_slots_assigned", clusterSlots},
		{"cluster_slots_ok", clusterSlots},
		{"cluster_known_nodes", strconv.Itoa(vmCount)},
	}

	for _, field := range want {
		got, ok := info[field.key]
		if !ok {
			return fmt.Errorf("%w: CLUSTER INFO has no %s", ErrGateFailed, field.key)
		}

		if got != field.value {
			return fmt.Errorf("%w: %s is not %s", ErrGateFailed, field.key, field.value)
		}
	}

	return nil
}

// rabbitStatus is the part of `rabbitmq-diagnostics cluster_status
// --formatter json` that the gate reads.
type rabbitStatus struct {
	DiskNodes    []string `json:"disk_nodes"`
	RunningNodes []string `json:"running_nodes"`
}

// rabbitStatusDocument returns the status document in output. RabbitMQ
// 4.2 can print Erlang error reports to stdout ahead of the JSON, so
// when the whole output is not JSON, the document is the one line that
// is a JSON object. Zero such lines or more than one return the output
// unchanged, which then fails to decode.
func rabbitStatusDocument(output []byte) []byte {
	if json.Valid(output) {
		return output
	}

	var found []byte

	for _, line := range bytes.Split(output, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))

		if !bytes.HasPrefix(line, []byte("{")) || !json.Valid(line) {
			continue
		}

		if found != nil {
			return output
		}

		found = line
	}

	if found == nil {
		return output
	}

	return found
}

// ParseRabbitStatus passes when `rabbitmq-diagnostics check_running`
// exited 0 and statusJSON, the cluster status in the JSON format,
// lists at least one disc node and every disc node as running.
func ParseRabbitStatus(checkRunningExit int, statusJSON []byte) error {
	if checkRunningExit != 0 {
		return fmt.Errorf("%w: check_running exited %d", ErrGateFailed, checkRunningExit)
	}

	var status rabbitStatus

	// The decoder's error can quote the output, so it is not wrapped.
	if json.Unmarshal(rabbitStatusDocument(statusJSON), &status) != nil {
		return fmt.Errorf("%w: the cluster status is not valid JSON", ErrGateFailed)
	}

	if len(status.DiskNodes) == 0 {
		return fmt.Errorf("%w: the cluster status lists no disc nodes", ErrGateFailed)
	}

	running := make(map[string]bool, len(status.RunningNodes))
	for _, node := range status.RunningNodes {
		running[node] = true
	}

	stopped := 0

	for _, node := range status.DiskNodes {
		if !running[node] {
			stopped++
		}
	}

	if stopped > 0 {
		return fmt.Errorf("%w: %d of %d disc nodes are not running", ErrGateFailed, stopped, len(status.DiskNodes))
	}

	return nil
}

// The RabbitMQ gate prints two result lines and then the status, so its
// output splits into three parts.
const (
	rabbitResults = 2
	rabbitParts   = rabbitResults + 1
)

// rabbitExit is the form of the two lines that head the RabbitMQ gate's
// output.
var rabbitExit = regexp.MustCompile(`^(check_running|cluster_status) exit=(0|[1-9][0-9]{0,2})$`)

// ParseRabbitGate reads what gate.sh prints for a RabbitMQ cluster,
// which is `check_running exit=<n>`, then `cluster_status exit=<n>`, then
// the cluster status as JSON, and applies ParseRabbitStatus.
func ParseRabbitGate(out []byte) error {
	lines := bytes.SplitN(out, []byte("\n"), rabbitParts)
	if len(lines) < rabbitParts {
		return fmt.Errorf("%w: the RabbitMQ gate output is incomplete", ErrGateFailed)
	}

	exits := make([]int, rabbitResults)

	for index, want := range []string{"check_running", "cluster_status"} {
		match := rabbitExit.FindStringSubmatch(string(lines[index]))
		if match == nil || match[1] != want {
			return fmt.Errorf("%w: line %d of the RabbitMQ gate output is not the %s result", ErrGateFailed, index+1, want)
		}

		exits[index], _ = strconv.Atoi(match[2])
	}

	if exits[1] != 0 {
		return fmt.Errorf("%w: cluster_status exited %d", ErrGateFailed, exits[1])
	}

	return ParseRabbitStatus(exits[0], lines[rabbitResults])
}
