package websocket

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	rabbitmqssh "blacksmith/internal/services/rabbitmq"
	"blacksmith/pkg/logger"

	gorillawebsocket "github.com/gorilla/websocket"
)

var errFakeExecutor = errors.New("fake executor refuses")

// failingPluginsExecutor refuses every command, which ends the handler's flow
// after a single error reply.
type failingPluginsExecutor struct{}

func (failingPluginsExecutor) ExecuteCommand(context.Context, rabbitmqssh.PluginsExecutionContext, string, string, int, string, string, []string) (*rabbitmqssh.PluginsStreamingExecutionResult, error) {
	return nil, errFakeExecutor
}

func (failingPluginsExecutor) ExecuteCommandSync(context.Context, rabbitmqssh.PluginsExecutionContext, string, string, int, string, string, []string) (string, int, error) {
	return "", 1, errFakeExecutor
}

const leakSentinelPluginArgument = "sentinel-plugin-argument-do-not-log"

type captureLogger struct {
	*logger.NoOpLogger

	mu    sync.Mutex
	lines []string
}

func (c *captureLogger) record(format string, args []interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}

func (c *captureLogger) Info(format string, args ...interface{})  { c.record(format, args) }
func (c *captureLogger) Debug(format string, args ...interface{}) { c.record(format, args) }
func (c *captureLogger) Error(format string, args ...interface{}) { c.record(format, args) }
func (c *captureLogger) Warn(format string, args ...interface{})  { c.record(format, args) }

func (c *captureLogger) output() string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return strings.Join(c.lines, "\n")
}

// A plugins execute message logs the command and how many arguments it had,
// never the arguments.
func TestPluginsExecuteMessageNeverLogsArguments(t *testing.T) {
	t.Parallel()

	capture := &captureLogger{NoOpLogger: &logger.NoOpLogger{}}
	handler := NewHandler(Dependencies{Logger: capture, RabbitMQPluginsExecutorService: failingPluginsExecutor{}})

	done := make(chan error, 1)
	upgrader := gorillawebsocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			done <- err

			return
		}

		defer func() { _ = conn.Close() }()

		message := struct {
			Type      string   `json:"type"`
			Category  string   `json:"category"`
			Command   string   `json:"command"`
			Arguments []string `json:"arguments"`
		}{Type: commandTypeExecute, Category: "plugin_management", Command: "enable", Arguments: []string{leakSentinelPluginArgument}}

		done <- handler.handlePluginsMessageType(context.Background(), conn, "inst-1", "dep", "rabbitmq", 0, message, capture)
	}))
	defer server.Close()

	client, _, err := gorillawebsocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	defer func() { _ = client.Close() }()

	// The executor refuses, so the handler answers with an error and returns.
	var reply map[string]interface{}

	err = client.ReadJSON(&reply)
	if err != nil {
		t.Fatalf("read the handler's reply: %v", err)
	}

	err = <-done
	if err != nil {
		t.Fatalf("handlePluginsMessageType: %v", err)
	}

	if reply["type"] != "error" {
		t.Fatalf("expected an error reply, got %v", reply)
	}

	out := capture.output()
	if !strings.Contains(out, "plugin_management.enable") || !strings.Contains(out, "1 args") {
		t.Errorf("expected the command and its argument count in the log, got:\n%s", out)
	}

	if strings.Contains(out, leakSentinelPluginArgument) {
		t.Errorf("log output leaked a plugin argument:\n%s", out)
	}
}

// With no plugins executor configured, the handler reports the error to the
// client and stops. It must not go on to call the nil executor.
func TestPluginsStreamingExecutionStopsWithoutAnExecutor(t *testing.T) {
	t.Parallel()

	capture := &captureLogger{NoOpLogger: &logger.NoOpLogger{}}
	handler := NewHandler(Dependencies{Logger: capture})
	upgrader := gorillawebsocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	done := make(chan interface{}, 1)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			done <- err

			return
		}

		defer func() { _ = conn.Close() }()

		defer func() { done <- recover() }()

		handler.handlePluginsStreamingExecution(context.Background(), conn, "inst-1", "dep", "rabbitmq", 0, "plugin_management", "list", nil, capture)
	}))
	defer server.Close()

	client, _, err := gorillawebsocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	defer func() { _ = client.Close() }()

	var reply map[string]interface{}

	err = client.ReadJSON(&reply)
	if err != nil || reply["type"] != "error" {
		t.Fatalf("expected one error reply, got %v and %v", reply, err)
	}

	recovered := <-done
	if recovered != nil {
		t.Fatalf("the handler went on to use the missing executor and panicked: %v", recovered)
	}
}
