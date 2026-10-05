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

const leakSentinelPluginArg = "sentinel-plugin-argument-do-not-log"

func newPluginsExecutor(capture *captureLogger) *PluginsExecutorService {
	return NewPluginsExecutorService(NewRabbitMQSSHService(fakeSSH{}, capture), NewPluginsMetadataService(capture), capture)
}

func TestRabbitMQPluginsExecutionNeverLogsArguments(t *testing.T) {
	t.Parallel()

	t.Run("sync", func(t *testing.T) {
		t.Parallel()

		capture := &captureLogger{}

		_, _, err := newPluginsExecutor(capture).ExecuteCommandSync(context.Background(), PluginsExecutionContext{InstanceID: "inst-1"},
			"dep", "rabbitmq", 0, "plugin_management", "enable", []string{leakSentinelPluginArg})
		if err != nil {
			t.Fatalf("ExecuteCommandSync: %v", err)
		}

		assertLoggedWithoutArgument(t, capture, "enable")
	})

	t.Run("streaming", func(t *testing.T) {
		t.Parallel()

		capture := &captureLogger{}

		result, err := newPluginsExecutor(capture).ExecuteCommand(context.Background(), PluginsExecutionContext{InstanceID: "inst-1"},
			"dep", "rabbitmq", 0, "plugin_management", "enable", []string{leakSentinelPluginArg})
		if err != nil {
			t.Fatalf("ExecuteCommand: %v", err)
		}

		// The streamed output is for the caller and may carry the argument.
		// Draining it lets the command finish and log its completion.
		for range result.Output {
		}

		assertLoggedWithoutArgument(t, capture, "enable")
	})
}

func assertLoggedWithoutArgument(t *testing.T, capture *captureLogger, command string) {
	t.Helper()

	out := capture.output()
	if !strings.Contains(out, command) {
		t.Errorf("expected the command name %q in the log, got:\n%s", command, out)
	}

	if strings.Contains(out, leakSentinelPluginArg) {
		t.Errorf("log output leaked a plugin argument:\n%s", out)
	}
}
