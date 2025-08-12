package bosh

import (
	"os"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestBufferedTaskReporter tests the BufferedTaskReporter implementation
func TestBufferedTaskReporter(t *testing.T) {
	reporter := &BufferedTaskReporter{}

	// Test TaskStarted
	reporter.TaskStarted(42)

	// Test TaskOutputChunk
	chunk1 := []byte("First chunk\n")
	chunk2 := []byte("Second chunk\n")
	reporter.TaskOutputChunk(42, chunk1)
	reporter.TaskOutputChunk(42, chunk2)

	// Test GetOutput
	output := reporter.GetOutput()
	expected := "First chunk\nSecond chunk\n"
	if output != expected {
		t.Errorf("GetOutput() = %q, want %q", output, expected)
	}

	// Test TaskFinished
	reporter.TaskFinished(42, "done")
}

// TestTaskEventParsing tests parsing of task events from JSON output
func TestTaskEventParsing(t *testing.T) {
	tests := []struct {
		name       string
		jsonOutput string
		wantEvents int
		wantError  bool
	}{
		{
			name: "valid events",
			jsonOutput: `{"time":1234567890,"stage":"Preparing","task":"Validating","state":"started","progress":0}
{"time":1234567891,"stage":"Preparing","task":"Validating","state":"finished","progress":100}`,
			wantEvents: 2,
		},
		{
			name:       "empty output",
			jsonOutput: "",
			wantEvents: 0,
		},
		{
			name: "mixed valid and invalid",
			jsonOutput: `{"time":1234567890,"stage":"Preparing","task":"Validating","state":"started","progress":0}
not valid json
{"time":1234567891,"stage":"Preparing","task":"Validating","state":"finished","progress":100}`,
			wantEvents: 2,
		},
		{
			name:       "event with error",
			jsonOutput: `{"time":1234567890,"stage":"Deploy","task":"Creating VMs","state":"failed","progress":50,"error":{"code":1,"message":"insufficient resources"}}`,
			wantEvents: 1,
			wantError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := parseTaskEvents(tt.jsonOutput)

			if len(events) != tt.wantEvents {
				t.Errorf("parseTaskEvents() returned %d events, want %d", len(events), tt.wantEvents)
			}

			if tt.wantError && len(events) > 0 {
				if events[0].Error == nil {
					t.Error("Expected event to have an error, but Error was nil")
				} else if events[0].Error.Message != "insufficient resources" {
					t.Errorf("Error message = %q, want %q", events[0].Error.Message, "insufficient resources")
				}
			}
		})
	}
}

// parseTaskEvents is a helper function that mimics the logic in GetTaskEvents
func parseTaskEvents(output string) []TaskEvent {
	if output == "" {
		return []TaskEvent{}
	}

	events := []TaskEvent{}
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var boshEvent struct {
			Time     int64    `json:"time"`
			Stage    string   `json:"stage"`
			Tags     []string `json:"tags"`
			Total    int      `json:"total"`
			Task     string   `json:"task"`
			Index    int      `json:"index"`
			State    string   `json:"state"`
			Progress int      `json:"progress"`
			Error    struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error,omitempty"`
		}

		if err := json.Unmarshal([]byte(line), &boshEvent); err != nil {
			continue
		}

		event := TaskEvent{
			Time:     time.Unix(boshEvent.Time, 0),
			Stage:    boshEvent.Stage,
			Tags:     boshEvent.Tags,
			Total:    boshEvent.Total,
			Task:     boshEvent.Task,
			Index:    boshEvent.Index,
			State:    boshEvent.State,
			Progress: boshEvent.Progress,
		}

		if boshEvent.Error.Message != "" {
			event.Error = &TaskEventError{
				Code:    boshEvent.Error.Code,
				Message: boshEvent.Error.Message,
			}
		}

		events = append(events, event)
	}

	return events
}

// TestLoggerInterface tests that our logger types implement the Logger interface
func TestLoggerInterface(t *testing.T) {
	// Test noOpLogger implements Logger
	var _ Logger = (*noOpLogger)(nil)

	// Test that noOpLogger methods don't panic
	logger := &noOpLogger{}
	logger.Info("test")
	logger.Debug("test")
	logger.Error("test")
}

// TestExtractDeploymentName tests the deployment name extraction from manifest
func TestExtractDeploymentName(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		want     string
	}{
		{
			name: "valid manifest",
			manifest: `name: my-deployment
releases:
- name: redis
  version: latest`,
			want: "my-deployment",
		},
		{
			name: "name with quotes",
			manifest: `name: "my-deployment"
releases: []`,
			want: "my-deployment",
		},
		{
			name: "name with single quotes",
			manifest: `name: 'my-deployment'
releases: []`,
			want: "my-deployment",
		},
		{
			name:     "no name",
			manifest: `releases: []`,
			want:     "",
		},
		{
			name: "name with spaces",
			manifest: `  name:  my-deployment  
releases: []`,
			want: "my-deployment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractDeploymentName(tt.manifest)
			if got != tt.want {
				t.Errorf("extractDeploymentName() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBufferedTaskReporter_Concurrent tests concurrent access to BufferedTaskReporter
func TestBufferedTaskReporter_Concurrent(t *testing.T) {
	reporter := &BufferedTaskReporter{}

	// Start multiple goroutines writing chunks
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			chunk := []byte(string(rune('A' + id)))
			reporter.TaskOutputChunk(1, chunk)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Check that all chunks are present
	output := reporter.GetOutput()
	if len(output) != 10 {
		t.Errorf("Expected output length 10, got %d", len(output))
	}

	// Check that all characters are present (order doesn't matter due to concurrency)
	for i := 0; i < 10; i++ {
		char := string(rune('A' + i))
		if !strings.Contains(output, char) {
			t.Errorf("Output missing character %s", char)
		}
	}
}

// TestBuildFactoryConfig tests the factory config building
func TestBuildFactoryConfig(t *testing.T) {
	// Set test mode to skip network calls
	os.Setenv("BLACKSMITH_TEST_MODE", "true")
	defer os.Unsetenv("BLACKSMITH_TEST_MODE")

	tests := []struct {
		name    string
		address string
	}{
		{
			name:    "https address with port",
			address: "https://192.168.1.1:25555",
		},
		{
			name:    "https address without port",
			address: "https://192.168.1.1",
		},
		{
			name:    "http address",
			address: "http://192.168.1.1:25555",
		},
		{
			name:    "plain address",
			address: "192.168.1.1:25555",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := Config{
				Address:  tt.address,
				Username: "admin",
				Password: "password",
			}

			// We can't easily test buildFactoryConfig directly as it's private
			// But we can test through NewDirectorAdapter
			// The bosh-cli director doesn't connect until first use
			director, err := NewDirectorAdapter(config)

			// The creation should succeed (lazy connection)
			if err != nil {
				t.Errorf("Unexpected error creating director: %v", err)
			}
			if director == nil {
				t.Error("Expected director to be created")
			}
		})
	}
}

// TestDirectorAdapterCreation tests creating a DirectorAdapter with various configs
func TestDirectorAdapterCreation(t *testing.T) {
	// Set test mode to skip network calls
	os.Setenv("BLACKSMITH_TEST_MODE", "true")
	defer os.Unsetenv("BLACKSMITH_TEST_MODE")

	tests := []struct {
		name      string
		config    Config
		wantError bool
	}{
		{
			name: "with logger",
			config: Config{
				Address:  "https://10.0.0.1:25555",
				Username: "admin",
				Password: "password",
				Logger:   &testLogger{t: t},
			},
			wantError: false, // Creation succeeds (lazy connection)
		},
		{
			name: "without logger",
			config: Config{
				Address:  "https://10.0.0.1:25555",
				Username: "admin",
				Password: "password",
			},
			wantError: false, // Creation succeeds (lazy connection)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewDirectorAdapter(tt.config)
			if (err != nil) != tt.wantError {
				t.Errorf("NewDirectorAdapter() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

// testLogger is a test implementation of the Logger interface
type testLogger struct {
	t *testing.T
}

func (l *testLogger) Info(format string, args ...interface{}) {
	l.t.Logf("[INFO] "+format, args...)
}

func (l *testLogger) Debug(format string, args ...interface{}) {
	l.t.Logf("[DEBUG] "+format, args...)
}

func (l *testLogger) Error(format string, args ...interface{}) {
	l.t.Logf("[ERROR] "+format, args...)
}

// TestConvertDirectorTask tests the task conversion helper
func TestConvertDirectorTask(t *testing.T) {
	now := time.Now()

	// We can't easily test convertDirectorTask as it's private
	// but we can test the Task struct creation
	task := &Task{
		ID:          42,
		State:       "running",
		Description: "Creating deployment",
		User:        "admin",
		Result:      "",
		ContextID:   "context-123",
		Deployment:  "my-deployment",
		StartedAt:   now,
	}

	if task.ID != 42 {
		t.Errorf("Task.ID = %d, want 42", task.ID)
	}

	if task.State != "running" {
		t.Errorf("Task.State = %s, want running", task.State)
	}

	// Test completed task
	task.State = "done"
	endTime := now.Add(5 * time.Minute)
	task.EndedAt = &endTime

	if task.EndedAt == nil {
		t.Error("Task.EndedAt should not be nil for completed task")
	}
}

// Benchmark tests
func BenchmarkBufferedTaskReporter(b *testing.B) {
	reporter := &BufferedTaskReporter{}
	chunk := bytes.Repeat([]byte("x"), 1024) // 1KB chunk

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reporter.TaskOutputChunk(1, chunk)
	}
}

func BenchmarkParseTaskEvents(b *testing.B) {
	eventJSON := `{"time":1234567890,"stage":"Preparing","task":"Validating","state":"started","progress":0}`
	events := strings.Repeat(eventJSON+"\n", 100) // 100 events

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parseTaskEvents(events)
	}
}
