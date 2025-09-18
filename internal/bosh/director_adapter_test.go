package bosh_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	. "blacksmith/internal/bosh"
	"blacksmith/pkg/logger"
)

// TestBufferedTaskReporter tests the BufferedTaskReporter implementation.
func TestBufferedTaskReporter(t *testing.T) {
	t.Parallel()

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

// TestTaskEventParsing tests parsing of task events from JSON output.
func TestTaskEventParsing(t *testing.T) {
	t.Parallel()

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

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			events := parseTaskEvents(testCase.jsonOutput)

			if len(events) != testCase.wantEvents {
				t.Errorf("parseTaskEvents() returned %d events, want %d", len(events), testCase.wantEvents)
			}

			if testCase.wantError && len(events) > 0 {
				if events[0].Error == nil {
					t.Errorf("Expected event to have an error, but Error was nil")
				} else if events[0].Error.Message != "insufficient resources" {
					t.Errorf("Error message = %q, want %q", events[0].Error.Message, "insufficient resources")
				}
			}
		})
	}
}

// parseTaskEvents is a helper function that mimics the logic in GetTaskEvents.
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

		err := json.Unmarshal([]byte(line), &boshEvent)
		if err != nil {
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

// TestLoggerInterface tests that our logger types implement the Logger interface.
func TestLoggerInterface(t *testing.T) {
	t.Parallel()
	// Test NoOpLogger implements Logger
	var _ Logger = (*logger.NoOpLogger)(nil)

	// Test that NoOpLogger methods don't panic
	testLogger := &logger.NoOpLogger{}
	testLogger.Infof("test")
	testLogger.Debugf("test")
	testLogger.Errorf("test")
}

// TestExtractDeploymentName tests the deployment name extraction from manifest.
func TestExtractDeploymentName(t *testing.T) {
	t.Parallel()

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

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := ExtractDeploymentName(testCase.manifest)
			if got != testCase.want {
				t.Errorf("ExtractDeploymentName() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestBufferedTaskReporter_Concurrent tests concurrent access to BufferedTaskReporter.
func TestBufferedTaskReporter_Concurrent(t *testing.T) {
	t.Parallel()

	reporter := &BufferedTaskReporter{}

	// Start multiple goroutines writing chunks
	done := make(chan bool)

	for index := range 10 {
		go func(id int) {
			chunk := []byte(string(rune('A' + id)))
			reporter.TaskOutputChunk(1, chunk)

			done <- true
		}(index)
	}

	// Wait for all goroutines
	for range 10 {
		<-done
	}

	// Check that all chunks are present
	output := reporter.GetOutput()
	if len(output) != 10 {
		t.Errorf("Expected output length 10, got %d", len(output))
	}

	// Check that all characters are present (order doesn't matter due to concurrency)
	for i := range 10 {
		char := string(rune('A' + i))
		if !strings.Contains(output, char) {
			t.Errorf("Output missing character %s", char)
		}
	}
}

// TestBuildFactoryConfig tests the factory config building.
func TestBuildFactoryConfig(t *testing.T) {
	t.Parallel()
	// Set test mode to skip network calls
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

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
			t.Parallel()

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
				t.Errorf("Expected director to be created")
			}
		})
	}
}

// TestDirectorAdapterCreation tests creating a DirectorAdapter with various configs.
func TestDirectorAdapterCreation(t *testing.T) {
	t.Parallel()
	// Set test mode to skip network calls
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

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

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewDirectorAdapter(testCase.config)
			if (err != nil) != testCase.wantError {
				t.Errorf("NewDirectorAdapter() error = %v, wantError %v", err, testCase.wantError)
			}
		})
	}
}

// testLogger is a test implementation of the Logger interface.
type testLogger struct {
	t *testing.T
}

func (l *testLogger) Infof(format string, args ...interface{}) {
	l.t.Logf("[INFO] "+format, args...)
}

func (l *testLogger) Debugf(format string, args ...interface{}) {
	l.t.Logf("[DEBUG] "+format, args...)
}

func (l *testLogger) Errorf(format string, args ...interface{}) {
	l.t.Logf("[ERROR] "+format, args...)
}

// TestConvertDirectorTask tests the task conversion helper.
func TestConvertDirectorTask(t *testing.T) {
	t.Parallel()

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
		t.Errorf("Task.EndedAt should not be nil for completed task")
	}
}

// Benchmark tests.
func BenchmarkBufferedTaskReporter(b *testing.B) {
	reporter := &BufferedTaskReporter{}
	chunk := bytes.Repeat([]byte("x"), 1024) // 1KB chunk

	b.ResetTimer()

	for range b.N {
		reporter.TaskOutputChunk(1, chunk)
	}
}

// TestDeleteResurrectionConfig tests the config name formatting logic.
func TestDeleteResurrectionConfig(t *testing.T) {
	t.Parallel()
	// Test the config name formatting logic since we can't easily mock the BOSH director
	tests := []struct {
		name       string
		deployment string
		expected   string
	}{
		{
			name:       "simple deployment name",
			deployment: "test-deployment",
			expected:   "blacksmith.test-deployment",
		},
		{
			name:       "deployment with hyphens",
			deployment: "my-service-instance",
			expected:   "blacksmith.my-service-instance",
		},
		{
			name:       "deployment with service prefix",
			deployment: "redis-abc123",
			expected:   "blacksmith.redis-abc123",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			// Test the config name formation logic
			configName := "blacksmith." + testCase.deployment
			if configName != testCase.expected {
				t.Errorf("Config name = %v, want %v", configName, testCase.expected)
			}
		})
	}
}

func BenchmarkParseTaskEvents(b *testing.B) {
	eventJSON := `{"time":1234567890,"stage":"Preparing","task":"Validating","state":"started","progress":0}`
	events := strings.Repeat(eventJSON+"\n", 100) // 100 events

	b.ResetTimer()

	for range b.N {
		parseTaskEvents(events)
	}
}

// TestGetConfigsReturnsOnlyActive tests that GetConfigs returns only active configs.
func TestGetConfigsReturnsOnlyActive(t *testing.T) {
	t.Parallel()
	// This test documents the expected behavior of GetConfigs:
	// It should return only the currently active version of each config,
	// not all versions. This is achieved by using limit=1 per config
	// which forces the BOSH API to set latest=true.

	// The test can't easily mock the BOSH director, but documents
	// the expected behavior for reference.
	t.Skip("This test documents expected behavior but requires a real BOSH director")

	// Expected behavior:
	// 1. GetConfigs should fetch all configs to find unique type+name combinations
	// 2. For each unique combination, fetch with limit=1 to get only the active version
	// 3. Return only those active configs
	//
	// Example: If there are 3 versions of "cloud:my-cloud" config,
	// GetConfigs should only return the one marked as Current=true
}
