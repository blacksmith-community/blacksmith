package rabbitmq_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/bosh/ssh"
	. "blacksmith/internal/services/rabbitmq"
)

const leakSentinelRabbitPassword = "sentinel-rabbit-password-do-not-log" //nolint:gosec // fake credential the test proves stays out of logs

type captureLogger struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureLogger) record(format string, args []interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}

func (c *captureLogger) Infof(format string, args ...interface{})  { c.record(format, args) }
func (c *captureLogger) Debugf(format string, args ...interface{}) { c.record(format, args) }
func (c *captureLogger) Errorf(format string, args ...interface{}) { c.record(format, args) }

func (c *captureLogger) output() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return strings.Join(c.lines, "\n")
}

type fakeSSH struct{}

func (fakeSSH) ExecuteCommand(*ssh.SSHRequest) (*ssh.SSHResponse, error) {
	return &ssh.SSHResponse{Success: true}, nil
}

func (fakeSSH) CreateSession(*ssh.SSHRequest) (ssh.SSHSession, error) { return nil, nil } //nolint:nilnil // unused by these tests
func (fakeSSH) Close() error                                          { return nil }

func TestRabbitMQCtlExecutionNeverLogsArguments(t *testing.T) {
	t.Parallel()

	for _, command := range []string{"add_user", "change_password"} {
		capture := &captureLogger{}
		executor := NewExecutorService(NewRabbitMQSSHService(fakeSSH{}, capture), NewMetadataService(capture), capture)

		_, err := executor.ExecuteCommandSync(context.Background(), ExecutionContext{InstanceID: "inst-1"}, "dep", "rabbitmq", 0, "users", command,
			[]string{"app-user", leakSentinelRabbitPassword})
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}

		out := capture.output()
		if !strings.Contains(out, command) {
			t.Errorf("%s: expected the command name in the log, got:\n%s", command, out)
		}

		if strings.Contains(out, leakSentinelRabbitPassword) {
			t.Errorf("%s: log output leaked the password:\n%s", command, out)
		}
	}
}

func TestRabbitMQCtlCommandBuilderNeverLogsArguments(t *testing.T) {
	t.Parallel()

	capture := &captureLogger{}
	executor := NewExecutorService(NewRabbitMQSSHService(fakeSSH{}, capture), NewMetadataService(capture), capture)

	cmd := executor.BuildRabbitMQCtlCommand("add_user", []string{"app-user", leakSentinelRabbitPassword})
	if !strings.Contains(strings.Join(cmd, " "), leakSentinelRabbitPassword) {
		t.Fatal("the built command must still carry the password")
	}

	if capture.output() == "" || strings.Contains(capture.output(), leakSentinelRabbitPassword) {
		t.Errorf("unexpected log output:\n%s", capture.output())
	}
}
