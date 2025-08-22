package rabbitmq

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// RabbitMQPluginsCategory represents a category of rabbitmq-plugins commands
type RabbitMQPluginsCategory struct {
	Name        string                   `json:"name"`
	DisplayName string                   `json:"display_name"`
	Description string                   `json:"description"`
	Commands    []RabbitMQPluginsCommand `json:"commands"`
}

// RabbitMQPluginsCommand represents a specific rabbitmq-plugins command with its metadata
type RabbitMQPluginsCommand struct {
	Name        string                           `json:"name"`
	Description string                           `json:"description"`
	Arguments   []RabbitMQPluginsCommandArgument `json:"arguments"`
	Options     []RabbitMQPluginsCommandOption   `json:"options"`
	Usage       string                           `json:"usage"`
	Examples    []string                         `json:"examples"`
	Category    string                           `json:"category"`
	Timeout     int                              `json:"timeout"`
	Dangerous   bool                             `json:"dangerous"`
}

// RabbitMQPluginsCommandArgument represents a command argument
type RabbitMQPluginsCommandArgument struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Type        string `json:"type"`
	Default     string `json:"default,omitempty"`
}

// RabbitMQPluginsCommandOption represents a command option/flag
type RabbitMQPluginsCommandOption struct {
	Name        string `json:"name"`
	Short       string `json:"short,omitempty"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Default     string `json:"default,omitempty"`
}

// RabbitMQPluginsExecution represents a command execution record
type RabbitMQPluginsExecution struct {
	InstanceID string   `json:"instance_id"`
	Category   string   `json:"category"`
	Command    string   `json:"command"`
	Arguments  []string `json:"arguments"`
	Timestamp  int64    `json:"timestamp"`
	Output     string   `json:"output"`
	ExitCode   int      `json:"exit_code"`
	Success    bool     `json:"success"`
	Duration   int64    `json:"duration"`
	User       string   `json:"user,omitempty"`
}

// PluginsMetadataService provides RabbitMQ plugins command metadata management
type PluginsMetadataService struct {
	categories map[string]*RabbitMQPluginsCategory
	commands   map[string]*RabbitMQPluginsCommand
	logger     Logger
}

// NewPluginsMetadataService creates a new plugins metadata service
func NewPluginsMetadataService(logger Logger) *PluginsMetadataService {
	if logger == nil {
		logger = &noOpLogger{}
	}

	service := &PluginsMetadataService{
		categories: make(map[string]*RabbitMQPluginsCategory),
		commands:   make(map[string]*RabbitMQPluginsCommand),
		logger:     logger,
	}

	// Initialize with default command metadata
	service.initializeCommandMetadata()

	return service
}

// GetCategories returns all command categories
func (p *PluginsMetadataService) GetCategories() []RabbitMQPluginsCategory {
	var categories []RabbitMQPluginsCategory
	for _, cat := range p.categories {
		categories = append(categories, *cat)
	}

	// Sort by display name for consistent ordering
	sort.Slice(categories, func(i, j int) bool {
		return categories[i].DisplayName < categories[j].DisplayName
	})

	return categories
}

// GetCategory returns a specific category by name
func (p *PluginsMetadataService) GetCategory(name string) (*RabbitMQPluginsCategory, error) {
	category, exists := p.categories[name]
	if !exists {
		return nil, fmt.Errorf("category '%s' not found", name)
	}
	return category, nil
}

// GetCommand returns a specific command by name
func (p *PluginsMetadataService) GetCommand(name string) (*RabbitMQPluginsCommand, error) {
	command, exists := p.commands[name]
	if !exists {
		return nil, fmt.Errorf("command '%s' not found", name)
	}
	return command, nil
}

// GetCommandsByCategory returns all commands for a specific category
func (p *PluginsMetadataService) GetCommandsByCategory(categoryName string) ([]RabbitMQPluginsCommand, error) {
	category, err := p.GetCategory(categoryName)
	if err != nil {
		return nil, err
	}
	return category.Commands, nil
}

// ValidateCommand validates a command and its arguments
func (p *PluginsMetadataService) ValidateCommand(commandName string, arguments []string) error {
	command, err := p.GetCommand(commandName)
	if err != nil {
		return err
	}

	// Check for dangerous commands
	if command.Dangerous {
		p.logger.Error("Executing dangerous command: %s", commandName)
	}

	// Validate plugin names for enable/disable/set commands
	if commandName == "enable" || commandName == "disable" || commandName == "set" {
		return p.validatePluginNames(arguments)
	}

	return nil
}

// validatePluginNames validates plugin names format
func (p *PluginsMetadataService) validatePluginNames(pluginNames []string) error {
	for _, name := range pluginNames {
		if name == "" {
			continue
		}
		if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
			return fmt.Errorf("invalid plugin name: %s", name)
		}
	}
	return nil
}

// initializeCommandMetadata initializes all command metadata
func (p *PluginsMetadataService) initializeCommandMetadata() {
	p.initializeHelpCategory()
	p.initializeMonitoringCategory()
	p.initializePluginManagementCategory()
}

// initializeHelpCategory initializes help command category
func (p *PluginsMetadataService) initializeHelpCategory() {
	helpCategory := &RabbitMQPluginsCategory{
		Name:        "help",
		DisplayName: "Help",
		Description: "Get help information for rabbitmq-plugins commands",
		Commands:    []RabbitMQPluginsCommand{},
	}

	// help command
	helpCmd := RabbitMQPluginsCommand{
		Name:        "help",
		Description: "Displays usage information for a command",
		Category:    "help",
		Usage:       "rabbitmq-plugins help [<command>]",
		Examples: []string{
			"rabbitmq-plugins help",
			"rabbitmq-plugins help list",
			"rabbitmq-plugins help enable",
		},
		Arguments: []RabbitMQPluginsCommandArgument{
			{
				Name:        "command",
				Description: "Command to get help for",
				Required:    false,
				Type:        "string",
			},
		},
		Timeout: 30,
	}

	// autocomplete command
	autocompleteCmd := RabbitMQPluginsCommand{
		Name:        "autocomplete",
		Description: "Provides command name autocomplete variants",
		Category:    "help",
		Usage:       "rabbitmq-plugins autocomplete <partial_command>",
		Examples: []string{
			"rabbitmq-plugins autocomplete li",
			"rabbitmq-plugins autocomplete en",
		},
		Arguments: []RabbitMQPluginsCommandArgument{
			{
				Name:        "partial_command",
				Description: "Partial command name to autocomplete",
				Required:    true,
				Type:        "string",
			},
		},
		Timeout: 30,
	}

	// version command
	versionCmd := RabbitMQPluginsCommand{
		Name:        "version",
		Description: "Displays CLI tools version",
		Category:    "help",
		Usage:       "rabbitmq-plugins version",
		Examples: []string{
			"rabbitmq-plugins version",
		},
		Timeout: 30,
	}

	helpCategory.Commands = append(helpCategory.Commands, helpCmd, autocompleteCmd, versionCmd)
	p.categories["help"] = helpCategory
	p.commands["help"] = &helpCmd
	p.commands["autocomplete"] = &autocompleteCmd
	p.commands["version"] = &versionCmd
}

// initializeMonitoringCategory initializes monitoring command category
func (p *PluginsMetadataService) initializeMonitoringCategory() {
	monitoringCategory := &RabbitMQPluginsCategory{
		Name:        "monitoring",
		DisplayName: "Monitoring",
		Description: "Monitor plugin status and directories",
		Commands:    []RabbitMQPluginsCommand{},
	}

	// directories command
	directoriesCmd := RabbitMQPluginsCommand{
		Name:        "directories",
		Description: "Displays plugin directory and enabled plugin file paths",
		Category:    "monitoring",
		Usage:       "rabbitmq-plugins directories [--online] [--offline]",
		Examples: []string{
			"rabbitmq-plugins directories",
			"rabbitmq-plugins directories --online",
			"rabbitmq-plugins directories --offline",
		},
		Options: []RabbitMQPluginsCommandOption{
			{
				Name:        "online",
				Description: "Contact the running node to retrieve the directories",
				Type:        "boolean",
			},
			{
				Name:        "offline",
				Description: "Read the directories from the configuration files",
				Type:        "boolean",
			},
		},
		Timeout: 30,
	}

	// is_enabled command
	isEnabledCmd := RabbitMQPluginsCommand{
		Name:        "is_enabled",
		Description: "Health check for plugin enablement status",
		Category:    "monitoring",
		Usage:       "rabbitmq-plugins is_enabled <plugin_name> [--online] [--offline]",
		Examples: []string{
			"rabbitmq-plugins is_enabled rabbitmq_management",
			"rabbitmq-plugins is_enabled rabbitmq_management --online",
			"rabbitmq-plugins is_enabled rabbitmq_management --offline",
		},
		Arguments: []RabbitMQPluginsCommandArgument{
			{
				Name:        "plugin_name",
				Description: "Name of the plugin to check",
				Required:    true,
				Type:        "string",
			},
		},
		Options: []RabbitMQPluginsCommandOption{
			{
				Name:        "online",
				Description: "Contact the running node to check the status",
				Type:        "boolean",
			},
			{
				Name:        "offline",
				Description: "Check the status from the configuration files",
				Type:        "boolean",
			},
		},
		Timeout: 30,
	}

	monitoringCategory.Commands = append(monitoringCategory.Commands, directoriesCmd, isEnabledCmd)
	p.categories["monitoring"] = monitoringCategory
	p.commands["directories"] = &directoriesCmd
	p.commands["is_enabled"] = &isEnabledCmd
}

// initializePluginManagementCategory initializes plugin management command category
func (p *PluginsMetadataService) initializePluginManagementCategory() {
	pluginMgmtCategory := &RabbitMQPluginsCategory{
		Name:        "plugin_management",
		DisplayName: "Plugin Management",
		Description: "Manage plugin installation and configuration",
		Commands:    []RabbitMQPluginsCommand{},
	}

	// list command
	listCmd := RabbitMQPluginsCommand{
		Name:        "list",
		Description: "Lists plugins and their state",
		Category:    "plugin_management",
		Usage:       "rabbitmq-plugins list [<plugin_name>] [options]",
		Examples: []string{
			"rabbitmq-plugins list",
			"rabbitmq-plugins list --verbose",
			"rabbitmq-plugins list --enabled",
			"rabbitmq-plugins list rabbitmq_management",
		},
		Arguments: []RabbitMQPluginsCommandArgument{
			{
				Name:        "plugin_name",
				Description: "Specific plugin name to list (pattern matching supported)",
				Required:    false,
				Type:        "string",
			},
		},
		Options: []RabbitMQPluginsCommandOption{
			{
				Name:        "verbose",
				Short:       "v",
				Description: "Show plugin descriptions and versions",
				Type:        "boolean",
			},
			{
				Name:        "minimal",
				Short:       "m",
				Description: "Show only plugin names",
				Type:        "boolean",
			},
			{
				Name:        "enabled",
				Short:       "E",
				Description: "Show only explicitly enabled plugins",
				Type:        "boolean",
			},
			{
				Name:        "implicitly-enabled",
				Short:       "e",
				Description: "Show only implicitly enabled plugins",
				Type:        "boolean",
			},
		},
		Timeout: 60,
	}

	// enable command
	enableCmd := RabbitMQPluginsCommand{
		Name:        "enable",
		Description: "Enables one or more plugins",
		Category:    "plugin_management",
		Usage:       "rabbitmq-plugins enable <plugin_name> [<plugin_name>...] [options]",
		Examples: []string{
			"rabbitmq-plugins enable rabbitmq_management",
			"rabbitmq-plugins enable rabbitmq_management rabbitmq_shovel",
			"rabbitmq-plugins enable --all",
			"rabbitmq-plugins enable rabbitmq_management --offline",
		},
		Arguments: []RabbitMQPluginsCommandArgument{
			{
				Name:        "plugin_names",
				Description: "Names of plugins to enable (or --all for all plugins)",
				Required:    true,
				Type:        "string[]",
			},
		},
		Options: []RabbitMQPluginsCommandOption{
			{
				Name:        "online",
				Description: "Contact the running node and enable immediately",
				Type:        "boolean",
			},
			{
				Name:        "offline",
				Description: "Update the configuration files only",
				Type:        "boolean",
			},
			{
				Name:        "all",
				Description: "Enable all available plugins",
				Type:        "boolean",
			},
		},
		Timeout:   120,
		Dangerous: false,
	}

	// disable command
	disableCmd := RabbitMQPluginsCommand{
		Name:        "disable",
		Description: "Disables one or more plugins",
		Category:    "plugin_management",
		Usage:       "rabbitmq-plugins disable <plugin_name> [<plugin_name>...] [options]",
		Examples: []string{
			"rabbitmq-plugins disable rabbitmq_management",
			"rabbitmq-plugins disable rabbitmq_management rabbitmq_shovel",
			"rabbitmq-plugins disable --all",
			"rabbitmq-plugins disable rabbitmq_management --offline",
		},
		Arguments: []RabbitMQPluginsCommandArgument{
			{
				Name:        "plugin_names",
				Description: "Names of plugins to disable (or --all for all plugins)",
				Required:    true,
				Type:        "string[]",
			},
		},
		Options: []RabbitMQPluginsCommandOption{
			{
				Name:        "online",
				Description: "Contact the running node and disable immediately",
				Type:        "boolean",
			},
			{
				Name:        "offline",
				Description: "Update the configuration files only",
				Type:        "boolean",
			},
			{
				Name:        "all",
				Description: "Disable all plugins (DANGEROUS)",
				Type:        "boolean",
			},
		},
		Timeout:   120,
		Dangerous: true,
	}

	// set command
	setCmd := RabbitMQPluginsCommand{
		Name:        "set",
		Description: "Enables specified plugins, disables the rest",
		Category:    "plugin_management",
		Usage:       "rabbitmq-plugins set <plugin_name> [<plugin_name>...] [options]",
		Examples: []string{
			"rabbitmq-plugins set rabbitmq_management",
			"rabbitmq-plugins set rabbitmq_management rabbitmq_shovel",
			"rabbitmq-plugins set rabbitmq_management --offline",
		},
		Arguments: []RabbitMQPluginsCommandArgument{
			{
				Name:        "plugin_names",
				Description: "Names of plugins to keep enabled (all others will be disabled)",
				Required:    true,
				Type:        "string[]",
			},
		},
		Options: []RabbitMQPluginsCommandOption{
			{
				Name:        "online",
				Description: "Contact the running node and apply changes immediately",
				Type:        "boolean",
			},
			{
				Name:        "offline",
				Description: "Update the configuration files only",
				Type:        "boolean",
			},
		},
		Timeout:   120,
		Dangerous: true,
	}

	pluginMgmtCategory.Commands = append(pluginMgmtCategory.Commands, listCmd, enableCmd, disableCmd, setCmd)
	p.categories["plugin_management"] = pluginMgmtCategory
	p.commands["list"] = &listCmd
	p.commands["enable"] = &enableCmd
	p.commands["disable"] = &disableCmd
	p.commands["set"] = &setCmd
}

// GetCommandHelp returns detailed help for a specific command
func (p *PluginsMetadataService) GetCommandHelp(commandName string) (string, error) {
	command, err := p.GetCommand(commandName)
	if err != nil {
		return "", err
	}

	var help strings.Builder
	help.WriteString(fmt.Sprintf("Command: %s\n", command.Name))
	help.WriteString(fmt.Sprintf("Description: %s\n", command.Description))
	help.WriteString(fmt.Sprintf("Usage: %s\n", command.Usage))

	if len(command.Arguments) > 0 {
		help.WriteString("\nArguments:\n")
		for _, arg := range command.Arguments {
			required := ""
			if arg.Required {
				required = " (required)"
			}
			help.WriteString(fmt.Sprintf("  %s: %s%s\n", arg.Name, arg.Description, required))
		}
	}

	if len(command.Options) > 0 {
		help.WriteString("\nOptions:\n")
		for _, option := range command.Options {
			short := ""
			if option.Short != "" {
				short = fmt.Sprintf(", -%s", option.Short)
			}
			help.WriteString(fmt.Sprintf("  --%s%s: %s\n", option.Name, short, option.Description))
		}
	}

	if len(command.Examples) > 0 {
		help.WriteString("\nExamples:\n")
		for _, example := range command.Examples {
			help.WriteString(fmt.Sprintf("  %s\n", example))
		}
	}

	if command.Dangerous {
		help.WriteString("\n⚠️  WARNING: This is a potentially dangerous command. Use with caution.\n")
	}

	return help.String(), nil
}

// ToJSON converts command metadata to JSON
func (p *PluginsMetadataService) ToJSON() (string, error) {
	data := struct {
		Categories []RabbitMQPluginsCategory `json:"categories"`
		Commands   []RabbitMQPluginsCommand  `json:"commands"`
	}{
		Categories: p.GetCategories(),
	}

	for _, cmd := range p.commands {
		data.Commands = append(data.Commands, *cmd)
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal to JSON: %v", err)
	}

	return string(jsonData), nil
}
