package certscripts_test

import (
	"os"
	"strings"
	"testing"

	"blacksmith/internal/upgrade/certscripts"
)

// The RabbitMQ 4.2.8 captures come from a lab node. Its stdout carries
// 181 lines of Erlang error reports ahead of the one JSON line, because
// the code server could not read the working directory as vcap.
const (
	rabbit42Status  = "testdata/rabbitmq-4.2.8-cluster-status.json"
	rabbit42Stopped = "testdata/rabbitmq-4.2.8-cluster-status-node-stopped.json"
	rabbit42Stdout  = "testdata/rabbitmq-4.2.8-cluster-status-stdout.txt"
)

const rabbitGateHead = "check_running exit=0\ncluster_status exit=0\n"

func readFixture(t *testing.T, path string) string {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}

func TestParseRabbitStatusV42(t *testing.T) {
	t.Parallel()

	noisy := readFixture(t, rabbit42Stdout)
	clean := readFixture(t, rabbit42Status)
	stopped := readFixture(t, rabbit42Stopped)

	if err := certscripts.ParseRabbitStatus(0, []byte(clean)); err != nil {
		t.Errorf("a healthy 4.2 cluster failed: %v", err)
	}

	if err := certscripts.ParseRabbitStatus(0, []byte(noisy)); err != nil {
		t.Errorf("a healthy 4.2 cluster with error reports ahead of the JSON failed: %v", err)
	}

	if certscripts.ParseRabbitStatus(0, []byte(stopped)) == nil {
		t.Error("a 4.2 cluster with a stopped member passed")
	}

	// The noise is the capture's first 181 lines, so the stopped status
	// goes in the place of its last line.
	lines := strings.Split(noisy, "\n")
	noisyStopped := strings.Join(lines[:len(lines)-2], "\n") + "\n" + stopped
	if certscripts.ParseRabbitStatus(0, []byte(noisyStopped)) == nil {
		t.Error("a 4.2 cluster with a stopped member and error reports passed")
	}

	if certscripts.ParseRabbitStatus(69, []byte(noisy)) == nil {
		t.Error("a failed check_running passed")
	}

	reports := strings.Join(lines[:len(lines)-2], "\n") + "\n"
	if certscripts.ParseRabbitStatus(0, []byte(reports)) == nil {
		t.Error("error reports with no status passed")
	}

	if certscripts.ParseRabbitStatus(0, []byte(noisyStopped+clean)) == nil {
		t.Error("two status documents passed")
	}

	if certscripts.ParseRabbitStatus(0, []byte(clean+"{\"disk_nodes\":[]}\n")) == nil {
		t.Error("a second status document after the first passed")
	}
}

func TestParseRabbitGateV42(t *testing.T) {
	t.Parallel()

	noisy := readFixture(t, rabbit42Stdout)
	stopped := readFixture(t, rabbit42Stopped)

	err := certscripts.ParseRabbitGate([]byte(rabbitGateHead + noisy))
	if err != nil {
		t.Errorf("a healthy 4.2 gate failed: %v", err)
	}

	if certscripts.ParseRabbitGate([]byte(rabbitGateHead+stopped)) == nil {
		t.Error("a 4.2 gate with a stopped member passed")
	}
}
