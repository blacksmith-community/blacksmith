package rabbitmq

import (
	"testing"
)

// MockLogger implements the Logger interface for testing
type MockLogger struct {
	messages []string
}

func (m *MockLogger) Info(format string, args ...interface{}) {
	m.messages = append(m.messages, "INFO")
}
func (m *MockLogger) Debug(format string, args ...interface{}) {
	m.messages = append(m.messages, "DEBUG")
}
func (m *MockLogger) Error(format string, args ...interface{}) {
	m.messages = append(m.messages, "ERROR")
}

func TestNewPluginsMetadataService(t *testing.T) {
	logger := &MockLogger{}
	service := NewPluginsMetadataService(logger)

	if service == nil {
		t.Fatal("NewPluginsMetadataService returned nil")
	}

	if service.logger != logger {
		t.Error("Logger not set correctly")
	}

	// Test with nil logger
	service2 := NewPluginsMetadataService(nil)
	if service2 == nil {
		t.Fatal("NewPluginsMetadataService with nil logger returned nil")
	}
}

func TestPluginsMetadataService_GetCategories(t *testing.T) {
	logger := &MockLogger{}
	service := NewPluginsMetadataService(logger)

	categories := service.GetCategories()

	if len(categories) == 0 {
		t.Error("Expected at least one category")
	}

	// Check that help category exists
	var helpFound bool
	for _, cat := range categories {
		if cat.Name == "help" {
			helpFound = true
			if len(cat.Commands) == 0 {
				t.Error("help category should have commands")
			}
			break
		}
	}

	if !helpFound {
		t.Error("help category not found")
	}
}

func TestPluginsMetadataService_GetCommandsByCategory(t *testing.T) {
	logger := &MockLogger{}
	service := NewPluginsMetadataService(logger)

	tests := []struct {
		name           string
		category       string
		expectError    bool
		expectCommands bool
	}{
		{
			name:           "Valid category - help",
			category:       "help",
			expectError:    false,
			expectCommands: true,
		},
		{
			name:           "Valid category - monitoring",
			category:       "monitoring",
			expectError:    false,
			expectCommands: true,
		},
		{
			name:           "Valid category - plugin_management",
			category:       "plugin_management",
			expectError:    false,
			expectCommands: true,
		},
		{
			name:        "Invalid category",
			category:    "NonExistent",
			expectError: true,
		},
		{
			name:        "Empty category",
			category:    "",
			expectError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			commands, err := service.GetCommandsByCategory(test.category)

			if test.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if test.expectCommands && len(commands) == 0 {
				t.Error("Expected commands but got none")
			}
		})
	}
}

func TestPluginsMetadataService_GetCommand(t *testing.T) {
	logger := &MockLogger{}
	service := NewPluginsMetadataService(logger)

	tests := []struct {
		name        string
		command     string
		expectError bool
	}{
		{
			name:        "Valid command - help",
			command:     "help",
			expectError: false,
		},
		{
			name:        "Valid command - list",
			command:     "list",
			expectError: false,
		},
		{
			name:        "Valid command - enable",
			command:     "enable",
			expectError: false,
		},
		{
			name:        "Invalid command",
			command:     "nonexistent",
			expectError: true,
		},
		{
			name:        "Empty command",
			command:     "",
			expectError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd, err := service.GetCommand(test.command)

			if test.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if cmd == nil {
				t.Error("Expected command but got nil")
				return
			}

			if cmd.Name != test.command {
				t.Errorf("Expected command name %s, got %s", test.command, cmd.Name)
			}
		})
	}
}

func TestPluginsMetadataService_ValidateCommand(t *testing.T) {
	logger := &MockLogger{}
	service := NewPluginsMetadataService(logger)

	tests := []struct {
		name        string
		command     string
		arguments   []string
		expectError bool
	}{
		{
			name:        "Valid command without args - help",
			command:     "help",
			arguments:   []string{},
			expectError: false,
		},
		{
			name:        "Valid command with args - enable",
			command:     "enable",
			arguments:   []string{"rabbitmq_management"},
			expectError: false,
		},
		{
			name:        "Valid command with multiple args - enable",
			command:     "enable",
			arguments:   []string{"rabbitmq_management", "rabbitmq_web_dispatch"},
			expectError: false,
		},
		{
			name:        "Invalid command",
			command:     "nonexistent",
			arguments:   []string{},
			expectError: true,
		},
		{
			name:        "Command with dangerous arguments",
			command:     "enable",
			arguments:   []string{"; rm -rf /"},
			expectError: true,
		},
		{
			name:        "Command with SQL injection",
			command:     "list",
			arguments:   []string{"'; DROP TABLE users; --"},
			expectError: false, // Current implementation doesn't validate this at command level
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := service.ValidateCommand(test.command, test.arguments)

			if test.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestRabbitMQPluginsCommand_Structure(t *testing.T) {
	// Test that command structures have required fields
	logger := &MockLogger{}
	service := NewPluginsMetadataService(logger)

	categories := service.GetCategories()

	for _, category := range categories {
		if len(category.Commands) == 0 {
			t.Errorf("Category %s has no commands", category.Name)
		}

		for _, cmd := range category.Commands {
			if cmd.Name == "" {
				t.Error("Command has empty name")
			}
			if cmd.Description == "" {
				t.Error("Command has empty description")
			}
			if cmd.Usage == "" {
				t.Error("Command has empty usage")
			}
			if cmd.Timeout <= 0 {
				t.Errorf("Command %s has invalid timeout: %d", cmd.Name, cmd.Timeout)
			}
		}
	}
}

func TestGetCategory(t *testing.T) {
	logger := &MockLogger{}
	service := NewPluginsMetadataService(logger)

	tests := []struct {
		name         string
		categoryName string
		expectFound  bool
	}{
		{
			name:         "Existing category - help",
			categoryName: "help",
			expectFound:  true,
		},
		{
			name:         "Existing category - monitoring",
			categoryName: "monitoring",
			expectFound:  true,
		},
		{
			name:         "Non-existing category",
			categoryName: "NonExistent",
			expectFound:  false,
		},
		{
			name:         "Empty category name",
			categoryName: "",
			expectFound:  false,
		},
		{
			name:         "Case sensitive check",
			categoryName: "Help", // This should fail since category name is "help"
			expectFound:  false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			category, err := service.GetCategory(test.categoryName)

			if test.expectFound {
				if err != nil {
					t.Errorf("Expected category but got error: %v", err)
				}
				if category == nil {
					t.Error("Expected category but got nil")
				} else if category.Name != test.categoryName {
					t.Errorf("Expected category name %s, got %s", test.categoryName, category.Name)
				}
			} else {
				if err == nil {
					t.Error("Expected error for non-existent category")
				}
			}
		})
	}
}

func TestValidatePluginNames(t *testing.T) {
	logger := &MockLogger{}
	service := NewPluginsMetadataService(logger)

	tests := []struct {
		name        string
		pluginNames []string
		expectError bool
	}{
		{
			name:        "Valid plugin names",
			pluginNames: []string{"rabbitmq_management", "valid_plugin"},
			expectError: false,
		},
		{
			name:        "Empty plugin names",
			pluginNames: []string{},
			expectError: false,
		},
		{
			name:        "Plugin names with spaces",
			pluginNames: []string{"valid plugin with spaces"},
			expectError: false,
		},
		{
			name:        "Plugin names with SQL injection",
			pluginNames: []string{"'; DROP TABLE users; --"},
			expectError: false, // Current implementation may not catch all SQL injection patterns
		},
		{
			name:        "Plugin names with script injection",
			pluginNames: []string{"<script>alert('xss')</script>"},
			expectError: true,
		},
		{
			name:        "Plugin names with dangerous characters",
			pluginNames: []string{"; rm -rf /"},
			expectError: true,
		},
		{
			name:        "Plugin names with command injection",
			pluginNames: []string{"`cat /etc/passwd`"},
			expectError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := service.validatePluginNames(test.pluginNames)

			if test.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

func TestPluginsMetadataService_CommandLookup(t *testing.T) {
	logger := &MockLogger{}
	service := NewPluginsMetadataService(logger)

	// Test that all expected commands are present
	expectedCommands := []string{
		"help", "version", "directories",
		"list", "is_enabled",
		"enable", "disable", "set",
	}

	for _, cmdName := range expectedCommands {
		t.Run("Command_"+cmdName, func(t *testing.T) {
			cmd, err := service.GetCommand(cmdName)
			if err != nil {
				t.Errorf("Command %s should exist but got error: %v", cmdName, err)
			}
			if cmd == nil {
				t.Errorf("Command %s should exist but got nil", cmdName)
			}
			if cmd != nil && cmd.Name != cmdName {
				t.Errorf("Expected command name %s, got %s", cmdName, cmd.Name)
			}
		})
	}
}

func TestPluginsMetadataService_DangerousCommands(t *testing.T) {
	logger := &MockLogger{}
	service := NewPluginsMetadataService(logger)

	// Commands that should be marked as dangerous
	dangerousCommands := []string{"disable", "set"}

	for _, cmdName := range dangerousCommands {
		t.Run("Dangerous_"+cmdName, func(t *testing.T) {
			cmd, err := service.GetCommand(cmdName)
			if err != nil {
				t.Errorf("Command %s should exist: %v", cmdName, err)
				return
			}
			if !cmd.Dangerous {
				t.Errorf("Command %s should be marked as dangerous", cmdName)
			}
		})
	}

	// Commands that should NOT be dangerous
	safeCommands := []string{"help", "version", "list", "directories", "is_enabled"}

	for _, cmdName := range safeCommands {
		t.Run("Safe_"+cmdName, func(t *testing.T) {
			cmd, err := service.GetCommand(cmdName)
			if err != nil {
				t.Errorf("Command %s should exist: %v", cmdName, err)
				return
			}
			if cmd.Dangerous {
				t.Errorf("Command %s should NOT be marked as dangerous", cmdName)
			}
		})
	}
}

func TestPluginsMetadataService_CategoryCount(t *testing.T) {
	logger := &MockLogger{}
	service := NewPluginsMetadataService(logger)

	categories := service.GetCategories()

	// Should have exactly 3 categories
	expectedCount := 3
	if len(categories) != expectedCount {
		t.Errorf("Expected %d categories, got %d", expectedCount, len(categories))
	}

	// Check category names (using internal names, not display names)
	expectedCategoryNames := []string{"help", "monitoring", "plugin_management"}
	foundCategories := make(map[string]bool)

	for _, cat := range categories {
		foundCategories[cat.Name] = true
	}

	for _, expectedName := range expectedCategoryNames {
		if !foundCategories[expectedName] {
			t.Errorf("Expected category %s not found. Found categories: %v", expectedName, foundCategories)
		}
	}
}

func TestPluginsMetadataService_CommandTimeout(t *testing.T) {
	logger := &MockLogger{}
	service := NewPluginsMetadataService(logger)

	categories := service.GetCategories()

	for _, category := range categories {
		for _, cmd := range category.Commands {
			if cmd.Timeout <= 0 {
				t.Errorf("Command %s has invalid timeout: %d", cmd.Name, cmd.Timeout)
			}
			if cmd.Timeout > 300 { // No command should take more than 5 minutes
				t.Errorf("Command %s has excessive timeout: %d seconds", cmd.Name, cmd.Timeout)
			}
		}
	}
}

// Benchmark tests
func BenchmarkGetCommand(b *testing.B) {
	logger := &MockLogger{}
	service := NewPluginsMetadataService(logger)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = service.GetCommand("list")
	}
}

func BenchmarkGetCategories(b *testing.B) {
	logger := &MockLogger{}
	service := NewPluginsMetadataService(logger)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = service.GetCategories()
	}
}

func BenchmarkValidateCommand(b *testing.B) {
	logger := &MockLogger{}
	service := NewPluginsMetadataService(logger)
	args := []string{"rabbitmq_management", "rabbitmq_web_dispatch"}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = service.ValidateCommand("enable", args)
	}
}
