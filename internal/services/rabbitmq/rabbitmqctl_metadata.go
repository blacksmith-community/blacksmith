package rabbitmq

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Static errors for err113 compliance.
var (
	ErrRabbitMQCtlCategoryNotFound      = errors.New("category not found")
	ErrRabbitMQCtlCommandNotFound       = errors.New("command not found")
	ErrRabbitMQCtlInsufficientArguments = errors.New("insufficient arguments")
	ErrRabbitMQCtlIntegerValueEmpty     = errors.New("integer value cannot be empty")
	ErrRabbitMQCtlInvalidBooleanValue   = errors.New("boolean value must be true/false, yes/no, on/off, or 1/0")
	ErrRabbitMQCtlVirtualHostEmpty      = errors.New("virtual host name cannot be empty")
	ErrRabbitMQCtlUsernameEmpty         = errors.New("username cannot be empty")
	ErrRabbitMQCtlQueueNameEmpty        = errors.New("queue name cannot be empty")
)

const (
	// Default timeout for RabbitMQ control commands in seconds.
	defaultRabbitMQCtlTimeout = 30
)

// RabbitMQCtlCategory represents a category of rabbitmqctl commands.
type RabbitMQCtlCategory struct {
	Name        string               `json:"name"`
	DisplayName string               `json:"display_name"`
	Description string               `json:"description"`
	Commands    []RabbitMQCtlCommand `json:"commands"`
}

// RabbitMQCtlCommand represents a specific rabbitmqctl command with its metadata.
type RabbitMQCtlCommand struct {
	Name        string                       `json:"name"`
	Description string                       `json:"description"`
	Arguments   []RabbitMQCtlCommandArgument `json:"arguments"`
	Options     []RabbitMQCtlCommandOption   `json:"options"`
	Usage       string                       `json:"usage"`
	Examples    []string                     `json:"examples"`
	Category    string                       `json:"category"`
	Timeout     int                          `json:"timeout"`
	Dangerous   bool                         `json:"dangerous"`
}

// RabbitMQCtlCommandArgument represents a command argument.
type RabbitMQCtlCommandArgument struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Type        string `json:"type"`
	Default     string `json:"default,omitempty"`
}

// RabbitMQCtlCommandOption represents a command option/flag.
type RabbitMQCtlCommandOption struct {
	Name        string `json:"name"`
	Short       string `json:"short,omitempty"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Default     string `json:"default,omitempty"`
}

// RabbitMQCtlExecution represents a command execution record.
type RabbitMQCtlExecution struct {
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

// MetadataService provides RabbitMQ command metadata management.
type MetadataService struct {
	categories map[string]*RabbitMQCtlCategory
	commands   map[string]*RabbitMQCtlCommand
	logger     Logger
}

// NewMetadataService creates a new metadata service.
func NewMetadataService(logger Logger) *MetadataService {
	if logger == nil {
		logger = &noOpLogger{}
	}

	service := &MetadataService{
		categories: make(map[string]*RabbitMQCtlCategory),
		commands:   make(map[string]*RabbitMQCtlCommand),
		logger:     logger,
	}

	// Initialize with default command metadata
	service.initializeCommandMetadata()

	return service
}

// GetCategories returns all command categories.
func (m *MetadataService) GetCategories() []RabbitMQCtlCategory {
	categories := make([]RabbitMQCtlCategory, 0, len(m.categories))
	for _, cat := range m.categories {
		categories = append(categories, *cat)
	}

	// Sort by display name for consistent ordering
	sort.Slice(categories, func(i, j int) bool {
		return categories[i].DisplayName < categories[j].DisplayName
	})

	return categories
}

// GetCategory returns a specific category with its commands.
func (m *MetadataService) GetCategory(name string) (*RabbitMQCtlCategory, error) {
	category, exists := m.categories[name]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrRabbitMQCtlCategoryNotFound, name)
	}

	return category, nil
}

// GetCommand returns detailed information about a specific command.
func (m *MetadataService) GetCommand(category, command string) (*RabbitMQCtlCommand, error) {
	commandKey := fmt.Sprintf("%s.%s", category, command)

	cmd, exists := m.commands[commandKey]
	if !exists {
		return nil, fmt.Errorf("%w: %s in category %s", ErrRabbitMQCtlCommandNotFound, command, category)
	}

	return cmd, nil
}

// GetCommandsByCategory returns all commands in a specific category.
func (m *MetadataService) GetCommandsByCategory(categoryName string) ([]RabbitMQCtlCommand, error) {
	category, err := m.GetCategory(categoryName)
	if err != nil {
		return nil, err
	}

	return category.Commands, nil
}

// ValidateCommand validates a command and its arguments.
func (m *MetadataService) ValidateCommand(category, command string, args []string) error {
	cmd, err := m.GetCommand(category, command)
	if err != nil {
		return err
	}

	// Check required arguments
	requiredArgs := 0

	for _, arg := range cmd.Arguments {
		if arg.Required {
			requiredArgs++
		}
	}

	if len(args) < requiredArgs {
		return fmt.Errorf("%w: command %s requires at least %d arguments, got %d", ErrRabbitMQCtlInsufficientArguments, command, requiredArgs, len(args))
	}

	// Validate argument types (basic validation)
	for index, arg := range args {
		if index < len(cmd.Arguments) {
			argDef := cmd.Arguments[index]

			err := m.validateArgumentType(argDef.Type, arg)
			if err != nil {
				return fmt.Errorf("argument %d (%s): %w", index+1, argDef.Name, err)
			}
		}
	}

	return nil
}

// GetCommandsAsJSON returns all commands and categories as JSON.
func (m *MetadataService) GetCommandsAsJSON() ([]byte, error) {
	data := map[string]interface{}{
		"categories": m.GetCategories(),
		"meta": map[string]interface{}{
			"total_categories": len(m.categories),
			"total_commands":   len(m.commands),
		},
	}

	result, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata to JSON: %w", err)
	}

	return result, nil
}

// validateArgumentType performs basic type validation for command arguments.
// IsRabbitMQMetadataService implements the interfaces.RabbitMQMetadataService interface.
func (m *MetadataService) IsRabbitMQMetadataService() bool {
	return true
}

func (m *MetadataService) validateArgumentType(argType, value string) error {
	validator, exists := m.getValidatorForType(argType)
	if !exists {
		// Unknown type, allow anything
		return nil
	}

	return validator(value)
}

// getValidatorForType returns a validator function for the given type.
func (m *MetadataService) getValidatorForType(argType string) (func(string) error, bool) {
	validators := map[string]func(string) error{
		"string":   m.validateStringType,
		"int":      m.validateIntegerType,
		"integer":  m.validateIntegerType,
		"bool":     m.validateBooleanType,
		"boolean":  m.validateBooleanType,
		"vhost":    m.validateVhostType,
		"username": m.validateUsernameType,
		"queue":    m.validateQueueType,
	}

	validator, exists := validators[argType]

	return validator, exists
}

// validateStringType validates string type arguments.
func (m *MetadataService) validateStringType(value string) error {
	// Any string is valid
	return nil
}

// validateIntegerType validates integer type arguments.
func (m *MetadataService) validateIntegerType(value string) error {
	// Basic integer check
	if strings.TrimSpace(value) == "" {
		return ErrRabbitMQCtlIntegerValueEmpty
	}
	// Additional validation could be added here
	return nil
}

// validateBooleanType validates boolean type arguments.
func (m *MetadataService) validateBooleanType(value string) error {
	lower := strings.ToLower(strings.TrimSpace(value))
	validBooleans := []string{"true", "false", "1", "0", "yes", "no", "on", "off"}

	for _, valid := range validBooleans {
		if lower == valid {
			return nil
		}
	}

	return ErrRabbitMQCtlInvalidBooleanValue
}

// validateVhostType validates virtual host name arguments.
func (m *MetadataService) validateVhostType(value string) error {
	// Virtual host name validation
	if strings.TrimSpace(value) == "" {
		return ErrRabbitMQCtlVirtualHostEmpty
	}

	return nil
}

// validateUsernameType validates username arguments.
func (m *MetadataService) validateUsernameType(value string) error {
	// Username validation
	if strings.TrimSpace(value) == "" {
		return ErrRabbitMQCtlUsernameEmpty
	}

	return nil
}

// validateQueueType validates queue name arguments.
func (m *MetadataService) validateQueueType(value string) error {
	// Queue name validation
	if strings.TrimSpace(value) == "" {
		return ErrRabbitMQCtlQueueNameEmpty
	}

	return nil
}

// initializeCommandMetadata initializes the service with rabbitmqctl command metadata.
func (m *MetadataService) initializeCommandMetadata() {
	// Initialize categories
	m.initializeCategories()

	// Initialize commands for each category
	m.initializeNodesCommands()
	m.initializeClusterCommands()
	m.initializeUsersCommands()
	m.initializeAccessCommands()
	m.initializeMonitoringCommands()
	m.initializeParametersCommands()
	m.initializePoliciesCommands()
	m.initializeVHostsCommands()
	m.initializeQueuesCommands()
	m.initializeDefinitionsCommands()
	m.initializeOperationsCommands()
	m.initializeFeatureFlagsCommands()
	m.initializeConfigEnvCommands()
	m.initializeMQTTCommands()
	m.initializeManagementCommands()
	m.initializeSTOMPCommands()
	m.initializeStreamCommands()
	m.initializeOtherCommands()

	m.logger.Infof("Initialized %d categories with %d total commands", len(m.categories), len(m.commands))
}

// initializeCategories creates all command categories.
func (m *MetadataService) createCategoryDefinitions() []RabbitMQCtlCategory {
	var categories []RabbitMQCtlCategory

	// Add core management categories
	categories = append(categories, m.createCoreCategories()...)

	// Add resource management categories
	categories = append(categories, m.createResourceCategories()...)

	// Add plugin categories
	categories = append(categories, m.createPluginCategories()...)

	return categories
}

// createCoreCategories creates the core management categories.
func (m *MetadataService) createCoreCategories() []RabbitMQCtlCategory {
	return []RabbitMQCtlCategory{
		{
			Name:        "nodes",
			DisplayName: "Nodes",
			Description: "Node management and status commands",
			Commands:    []RabbitMQCtlCommand{},
		},
		{
			Name:        "cluster",
			DisplayName: "Cluster",
			Description: "Cluster management and configuration commands",
			Commands:    []RabbitMQCtlCommand{},
		},
		{
			Name:        "users",
			DisplayName: "Users",
			Description: "User management commands",
			Commands:    []RabbitMQCtlCommand{},
		},
		{
			Name:        "access",
			DisplayName: "Access",
			Description: "Access control and permissions commands",
			Commands:    []RabbitMQCtlCommand{},
		},
		{
			Name:        "monitoring",
			DisplayName: "Monitoring",
			Description: "Monitoring and status reporting commands",
			Commands:    []RabbitMQCtlCommand{},
		},
		{
			Name:        "operations",
			DisplayName: "Operations",
			Description: "Operational and maintenance commands",
			Commands:    []RabbitMQCtlCommand{},
		},
	}
}

// createResourceCategories creates the resource management categories.
func (m *MetadataService) createResourceCategories() []RabbitMQCtlCategory {
	return []RabbitMQCtlCategory{
		{
			Name:        "parameters",
			DisplayName: "Parameters",
			Description: "Parameter management commands",
			Commands:    []RabbitMQCtlCommand{},
		},
		{
			Name:        "policies",
			DisplayName: "Policies",
			Description: "Policy management commands",
			Commands:    []RabbitMQCtlCommand{},
		},
		{
			Name:        "vhosts",
			DisplayName: "VHosts",
			Description: "Virtual host management commands",
			Commands:    []RabbitMQCtlCommand{},
		},
		{
			Name:        "queues",
			DisplayName: "Queues",
			Description: "Queue management and inspection commands",
			Commands:    []RabbitMQCtlCommand{},
		},
		{
			Name:        "definitions",
			DisplayName: "Definitions",
			Description: "Import and export definition commands",
			Commands:    []RabbitMQCtlCommand{},
		},
		{
			Name:        "feature_flags",
			DisplayName: "Feature Flags",
			Description: "Feature flag management commands",
			Commands:    []RabbitMQCtlCommand{},
		},
		{
			Name:        "config_env",
			DisplayName: "Config & Env",
			Description: "Configuration and environment commands",
			Commands:    []RabbitMQCtlCommand{},
		},
	}
}

// createPluginCategories creates the plugin-related categories.
func (m *MetadataService) createPluginCategories() []RabbitMQCtlCategory {
	return []RabbitMQCtlCategory{
		{
			Name:        "mqtt",
			DisplayName: "MQTT",
			Description: "MQTT plugin management commands",
			Commands:    []RabbitMQCtlCommand{},
		},
		{
			Name:        "management",
			DisplayName: "Management",
			Description: "Management plugin related commands",
			Commands:    []RabbitMQCtlCommand{},
		},
		{
			Name:        "stomp",
			DisplayName: "STOMP",
			Description: "STOMP plugin management commands",
			Commands:    []RabbitMQCtlCommand{},
		},
		{
			Name:        "stream",
			DisplayName: "Stream",
			Description: "Stream plugin management commands",
			Commands:    []RabbitMQCtlCommand{},
		},
		{
			Name:        "other",
			DisplayName: "Other",
			Description: "Miscellaneous commands",
			Commands:    []RabbitMQCtlCommand{},
		},
	}
}

func (m *MetadataService) initializeCategories() {
	categories := m.createCategoryDefinitions()

	for _, category := range categories {
		m.categories[category.Name] = &category
	}
}

// addCommandToCategory adds a command to a category and the global commands map.
func (m *MetadataService) addCommandToCategory(categoryName string, command RabbitMQCtlCommand) {
	// Set the category on the command
	command.Category = categoryName

	// Add to global commands map
	commandKey := fmt.Sprintf("%s.%s", categoryName, command.Name)
	m.commands[commandKey] = &command

	// Add to category
	if category, exists := m.categories[categoryName]; exists {
		category.Commands = append(category.Commands, command)
	}
}

// initializeNodesCommands initializes node-related commands.
func (m *MetadataService) initializeNodesCommands() {
	commands := []RabbitMQCtlCommand{
		{
			Name:        "node_health_check",
			Description: "Runs basic health checks for the local node",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options:     []RabbitMQCtlCommandOption{},
			Usage:       "rabbitmqctl node_health_check",
			Examples:    []string{"rabbitmqctl node_health_check"},
			Timeout:     defaultRabbitMQCtlTimeout,
		},
		{
			Name:        "ping",
			Description: "Ping the RabbitMQ node",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options:     []RabbitMQCtlCommandOption{},
			Usage:       "rabbitmqctl ping",
			Examples:    []string{"rabbitmqctl ping"},
			Timeout:     DefaultRabbitmqctlTimeout,
		},
	}

	for _, cmd := range commands {
		m.addCommandToCategory("nodes", cmd)
	}
}

// initializeClusterCommands initializes cluster-related commands.
func (m *MetadataService) initializeClusterCommands() {
	commands := []RabbitMQCtlCommand{
		{
			Name:        "cluster_status",
			Description: "Display cluster status information",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options:     []RabbitMQCtlCommandOption{},
			Usage:       "rabbitmqctl cluster_status",
			Examples:    []string{"rabbitmqctl cluster_status"},
			Timeout:     defaultRabbitMQCtlTimeout,
		},
		{
			Name:        "join_cluster",
			Description: "Join a cluster",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "node", Description: "Node to join", Required: true, Type: "string"},
			},
			Options: []RabbitMQCtlCommandOption{
				{Name: "--ram", Description: "Join as a RAM node", Type: "bool"},
			},
			Usage:     "rabbitmqctl join_cluster [--ram] <node>",
			Examples:  []string{"rabbitmqctl join_cluster rabbit@server1"},
			Timeout:   LongRabbitmqctlTimeout,
			Dangerous: true,
		},
	}

	for _, cmd := range commands {
		m.addCommandToCategory("cluster", cmd)
	}
}

// initializeUsersCommands initializes user management commands.
func (m *MetadataService) initializeUsersCommands() {
	// Add user listing command
	m.addCommandToCategory("users", m.createListUsersCommand())

	// Add user modification commands
	for _, cmd := range m.createUserModificationCommands() {
		m.addCommandToCategory("users", cmd)
	}
}

// createListUsersCommand creates the list_users command.
func (m *MetadataService) createListUsersCommand() RabbitMQCtlCommand {
	return RabbitMQCtlCommand{
		Name:        "list_users",
		Description: "List all users",
		Arguments:   []RabbitMQCtlCommandArgument{},
		Options:     []RabbitMQCtlCommandOption{},
		Usage:       "rabbitmqctl list_users",
		Examples:    []string{"rabbitmqctl list_users"},
		Timeout:     defaultRabbitMQCtlTimeout,
	}
}

// createUserModificationCommands creates user modification commands.
func (m *MetadataService) createUserModificationCommands() []RabbitMQCtlCommand {
	return []RabbitMQCtlCommand{
		{
			Name:        "add_user",
			Description: "Add a new user",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "username", Description: "Username", Required: true, Type: "username"},
				{Name: "password", Description: "Password", Required: true, Type: "string"},
			},
			Options:   []RabbitMQCtlCommandOption{},
			Usage:     "rabbitmqctl add_user <username> <password>",
			Examples:  []string{"rabbitmqctl add_user myuser mypassword"},
			Timeout:   defaultRabbitMQCtlTimeout,
			Dangerous: true,
		},
		{
			Name:        "delete_user",
			Description: "Delete a user",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "username", Description: "Username to delete", Required: true, Type: "username"},
			},
			Options:   []RabbitMQCtlCommandOption{},
			Usage:     "rabbitmqctl delete_user <username>",
			Examples:  []string{"rabbitmqctl delete_user myuser"},
			Timeout:   defaultRabbitMQCtlTimeout,
			Dangerous: true,
		},
		{
			Name:        "change_password",
			Description: "Change user password",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "username", Description: "Username", Required: true, Type: "username"},
				{Name: "password", Description: "New password", Required: true, Type: "string"},
			},
			Options:   []RabbitMQCtlCommandOption{},
			Usage:     "rabbitmqctl change_password <username> <password>",
			Examples:  []string{"rabbitmqctl change_password myuser newpassword"},
			Timeout:   defaultRabbitMQCtlTimeout,
			Dangerous: true,
		},
		{
			Name:        "set_user_tags",
			Description: "Set user tags",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "username", Description: "Username", Required: true, Type: "username"},
				{Name: "tags", Description: "Tags (administrator, monitoring, etc.)", Required: false, Type: "string"},
			},
			Options:   []RabbitMQCtlCommandOption{},
			Usage:     "rabbitmqctl set_user_tags <username> [tag ...]",
			Examples:  []string{"rabbitmqctl set_user_tags myuser administrator"},
			Timeout:   defaultRabbitMQCtlTimeout,
			Dangerous: true,
		},
	}
}

// initializeAccessCommands initializes access control commands.
func (m *MetadataService) initializeAccessCommands() {
	commands := []RabbitMQCtlCommand{
		{
			Name:        "list_permissions",
			Description: "List permissions for all users",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "vhost", Description: "Virtual host", Required: false, Type: "vhost", Default: "/"},
			},
			Options:  []RabbitMQCtlCommandOption{},
			Usage:    "rabbitmqctl list_permissions [-p <vhost>]",
			Examples: []string{"rabbitmqctl list_permissions", "rabbitmqctl list_permissions -p /"},
			Timeout:  defaultRabbitMQCtlTimeout,
		},
		{
			Name:        "list_user_permissions",
			Description: "List permissions for a specific user",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "username", Description: "Username", Required: true, Type: "username"},
			},
			Options:  []RabbitMQCtlCommandOption{},
			Usage:    "rabbitmqctl list_user_permissions <username>",
			Examples: []string{"rabbitmqctl list_user_permissions myuser"},
			Timeout:  defaultRabbitMQCtlTimeout,
		},
		{
			Name:        "set_permissions",
			Description: "Set user permissions for a virtual host",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "username", Description: "Username", Required: true, Type: "username"},
				{Name: "configure", Description: "Configure permission regex", Required: true, Type: "string"},
				{Name: "write", Description: "Write permission regex", Required: true, Type: "string"},
				{Name: "read", Description: "Read permission regex", Required: true, Type: "string"},
			},
			Options: []RabbitMQCtlCommandOption{
				{Name: "-p", Description: "Virtual host", Type: "vhost", Default: "/"},
			},
			Usage:     "rabbitmqctl set_permissions [-p <vhost>] <username> <configure> <write> <read>",
			Examples:  []string{"rabbitmqctl set_permissions myuser \".*\" \".*\" \".*\""},
			Timeout:   defaultRabbitMQCtlTimeout,
			Dangerous: true,
		},
		{
			Name:        "clear_permissions",
			Description: "Clear user permissions for a virtual host",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "username", Description: "Username", Required: true, Type: "username"},
			},
			Options: []RabbitMQCtlCommandOption{
				{Name: "-p", Description: "Virtual host", Type: "vhost", Default: "/"},
			},
			Usage:     "rabbitmqctl clear_permissions [-p <vhost>] <username>",
			Examples:  []string{"rabbitmqctl clear_permissions myuser"},
			Timeout:   defaultRabbitMQCtlTimeout,
			Dangerous: true,
		},
	}

	for _, cmd := range commands {
		m.addCommandToCategory("access", cmd)
	}
}

// initializeMonitoringCommands initializes monitoring commands.
// getMonitoringCommandDefinitions returns the monitoring command definitions.
//
//nolint:funlen // Data definition function with command metadata
func (m *MetadataService) getMonitoringCommandDefinitions() []RabbitMQCtlCommand {
	return []RabbitMQCtlCommand{
		{
			Name:        "status",
			Description: "Display status information",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options:     []RabbitMQCtlCommandOption{},
			Usage:       "rabbitmqctl status",
			Examples:    []string{"rabbitmqctl status"},
			Timeout:     defaultRabbitMQCtlTimeout,
		},
		{
			Name:        "environment",
			Description: "Display environment information",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options:     []RabbitMQCtlCommandOption{},
			Usage:       "rabbitmqctl environment",
			Examples:    []string{"rabbitmqctl environment"},
			Timeout:     defaultRabbitMQCtlTimeout,
		},
		{
			Name:        "report",
			Description: "Generate a server status report",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options:     []RabbitMQCtlCommandOption{},
			Usage:       "rabbitmqctl report",
			Examples:    []string{"rabbitmqctl report"},
			Timeout:     LongRabbitmqctlTimeout,
		},
		{
			Name:        "list_connections",
			Description: "List all connections",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "connectioninfoitem", Description: "Connection info items to display", Required: false, Type: "string"},
			},
			Options:  []RabbitMQCtlCommandOption{},
			Usage:    "rabbitmqctl list_connections [connectioninfoitem ...]",
			Examples: []string{"rabbitmqctl list_connections", "rabbitmqctl list_connections name state"},
			Timeout:  defaultRabbitMQCtlTimeout,
		},
		{
			Name:        "list_channels",
			Description: "List all channels",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "channelinfoitem", Description: "Channel info items to display", Required: false, Type: "string"},
			},
			Options:  []RabbitMQCtlCommandOption{},
			Usage:    "rabbitmqctl list_channels [channelinfoitem ...]",
			Examples: []string{"rabbitmqctl list_channels", "rabbitmqctl list_channels name connection"},
			Timeout:  defaultRabbitMQCtlTimeout,
		},
		{
			Name:        "list_consumers",
			Description: "List all consumers",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "consumerinfoitem", Description: "Consumer info items to display", Required: false, Type: "string"},
			},
			Options: []RabbitMQCtlCommandOption{
				{Name: "-p", Description: "Virtual host", Type: "vhost", Default: "/"},
			},
			Usage:    "rabbitmqctl list_consumers [-p <vhost>] [consumerinfoitem ...]",
			Examples: []string{"rabbitmqctl list_consumers", "rabbitmqctl list_consumers -p / queue_name"},
			Timeout:  defaultRabbitMQCtlTimeout,
		},
	}
}

func (m *MetadataService) initializeMonitoringCommands() {
	commands := m.getMonitoringCommandDefinitions()
	for _, cmd := range commands {
		m.addCommandToCategory("monitoring", cmd)
	}
}

// initializeParametersCommands initializes parameter management commands.
func (m *MetadataService) initializeParametersCommands() {
	commands := []RabbitMQCtlCommand{
		{
			Name:        "list_parameters",
			Description: "List all parameters",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "component_name", Description: "Component name", Required: false, Type: "string"},
			},
			Options: []RabbitMQCtlCommandOption{
				{Name: "-p", Description: "Virtual host", Type: "vhost", Default: "/"},
			},
			Usage:    "rabbitmqctl list_parameters [-p <vhost>] [component_name]",
			Examples: []string{"rabbitmqctl list_parameters", "rabbitmqctl list_parameters federation-upstream"},
			Timeout:  defaultRabbitMQCtlTimeout,
		},
		{
			Name:        "set_parameter",
			Description: "Set a parameter",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "component_name", Description: "Component name", Required: true, Type: "string"},
				{Name: "name", Description: "Parameter name", Required: true, Type: "string"},
				{Name: "value", Description: "Parameter value (JSON)", Required: true, Type: "string"},
			},
			Options: []RabbitMQCtlCommandOption{
				{Name: "-p", Description: "Virtual host", Type: "vhost", Default: "/"},
			},
			Usage:     "rabbitmqctl set_parameter [-p <vhost>] <component_name> <name> <value>",
			Examples:  []string{"rabbitmqctl set_parameter federation-upstream my-upstream '{\"uri\":\"amqp://server\"}'"},
			Timeout:   defaultRabbitMQCtlTimeout,
			Dangerous: true,
		},
		{
			Name:        "clear_parameter",
			Description: "Clear a parameter",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "component_name", Description: "Component name", Required: true, Type: "string"},
				{Name: "name", Description: "Parameter name", Required: true, Type: "string"},
			},
			Options: []RabbitMQCtlCommandOption{
				{Name: "-p", Description: "Virtual host", Type: "vhost", Default: "/"},
			},
			Usage:     "rabbitmqctl clear_parameter [-p <vhost>] <component_name> <name>",
			Examples:  []string{"rabbitmqctl clear_parameter federation-upstream my-upstream"},
			Timeout:   defaultRabbitMQCtlTimeout,
			Dangerous: true,
		},
	}

	for _, cmd := range commands {
		m.addCommandToCategory("parameters", cmd)
	}
}

// initializePoliciesCommands initializes policy management commands.
func (m *MetadataService) initializePoliciesCommands() {
	commands := []RabbitMQCtlCommand{
		{
			Name:        "list_policies",
			Description: "List all policies",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options: []RabbitMQCtlCommandOption{
				{Name: "-p", Description: "Virtual host", Type: "vhost", Default: "/"},
			},
			Usage:    "rabbitmqctl list_policies [-p <vhost>]",
			Examples: []string{"rabbitmqctl list_policies", "rabbitmqctl list_policies -p /"},
			Timeout:  defaultRabbitMQCtlTimeout,
		},
		{
			Name:        "set_policy",
			Description: "Set a policy",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "name", Description: "Policy name", Required: true, Type: "string"},
				{Name: "pattern", Description: "Pattern to match", Required: true, Type: "string"},
				{Name: "definition", Description: "Policy definition (JSON)", Required: true, Type: "string"},
			},
			Options: []RabbitMQCtlCommandOption{
				{Name: "-p", Description: "Virtual host", Type: "vhost", Default: "/"},
				{Name: "--priority", Description: "Policy priority", Type: "int", Default: "0"},
				{Name: "--apply-to", Description: "Apply to (queues/exchanges)", Type: "string", Default: "all"},
			},
			Usage:     "rabbitmqctl set_policy [-p <vhost>] [--priority <priority>] [--apply-to <apply-to>] <name> <pattern> <definition>",
			Examples:  []string{"rabbitmqctl set_policy ha-all \"^ha\\.\" '{\"ha-mode\":\"all\"}'"},
			Timeout:   defaultRabbitMQCtlTimeout,
			Dangerous: true,
		},
		{
			Name:        "clear_policy",
			Description: "Clear a policy",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "name", Description: "Policy name", Required: true, Type: "string"},
			},
			Options: []RabbitMQCtlCommandOption{
				{Name: "-p", Description: "Virtual host", Type: "vhost", Default: "/"},
			},
			Usage:     "rabbitmqctl clear_policy [-p <vhost>] <name>",
			Examples:  []string{"rabbitmqctl clear_policy ha-all"},
			Timeout:   defaultRabbitMQCtlTimeout,
			Dangerous: true,
		},
	}

	for _, cmd := range commands {
		m.addCommandToCategory("policies", cmd)
	}
}

// initializeVHostsCommands initializes virtual host management commands.
func (m *MetadataService) initializeVHostsCommands() {
	commands := []RabbitMQCtlCommand{
		{
			Name:        "list_vhosts",
			Description: "List all virtual hosts",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options:     []RabbitMQCtlCommandOption{},
			Usage:       "rabbitmqctl list_vhosts",
			Examples:    []string{"rabbitmqctl list_vhosts"},
			Timeout:     defaultRabbitMQCtlTimeout,
		},
		{
			Name:        "add_vhost",
			Description: "Add a virtual host",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "vhost", Description: "Virtual host name", Required: true, Type: "vhost"},
			},
			Options:   []RabbitMQCtlCommandOption{},
			Usage:     "rabbitmqctl add_vhost <vhost>",
			Examples:  []string{"rabbitmqctl add_vhost /test"},
			Timeout:   defaultRabbitMQCtlTimeout,
			Dangerous: true,
		},
		{
			Name:        "delete_vhost",
			Description: "Delete a virtual host",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "vhost", Description: "Virtual host name", Required: true, Type: "vhost"},
			},
			Options:   []RabbitMQCtlCommandOption{},
			Usage:     "rabbitmqctl delete_vhost <vhost>",
			Examples:  []string{"rabbitmqctl delete_vhost /test"},
			Timeout:   defaultRabbitMQCtlTimeout,
			Dangerous: true,
		},
	}

	for _, cmd := range commands {
		m.addCommandToCategory("vhosts", cmd)
	}
}

// initializeQueuesCommands initializes queue management commands.
func (m *MetadataService) initializeQueuesCommands() {
	commands := []RabbitMQCtlCommand{
		{
			Name:        "list_queues",
			Description: "List all queues",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "queueinfoitem", Description: "Queue info items to display", Required: false, Type: "string"},
			},
			Options: []RabbitMQCtlCommandOption{
				{Name: "-p", Description: "Virtual host", Type: "vhost", Default: "/"},
			},
			Usage:    "rabbitmqctl list_queues [-p <vhost>] [queueinfoitem ...]",
			Examples: []string{"rabbitmqctl list_queues", "rabbitmqctl list_queues name messages consumers"},
			Timeout:  defaultRabbitMQCtlTimeout,
		},
		{
			Name:        "purge_queue",
			Description: "Purge a queue",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "queue", Description: "Queue name", Required: true, Type: "queue"},
			},
			Options: []RabbitMQCtlCommandOption{
				{Name: "-p", Description: "Virtual host", Type: "vhost", Default: "/"},
			},
			Usage:     "rabbitmqctl purge_queue [-p <vhost>] <queue>",
			Examples:  []string{"rabbitmqctl purge_queue myqueue"},
			Timeout:   LongRabbitmqctlTimeout,
			Dangerous: true,
		},
		{
			Name:        "delete_queue",
			Description: "Delete a queue",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "queue", Description: "Queue name", Required: true, Type: "queue"},
			},
			Options: []RabbitMQCtlCommandOption{
				{Name: "-p", Description: "Virtual host", Type: "vhost", Default: "/"},
				{Name: "--if-empty", Description: "Only delete if empty", Type: "bool"},
				{Name: "--if-unused", Description: "Only delete if unused", Type: "bool"},
			},
			Usage:     "rabbitmqctl delete_queue [-p <vhost>] [--if-empty] [--if-unused] <queue>",
			Examples:  []string{"rabbitmqctl delete_queue myqueue", "rabbitmqctl delete_queue --if-empty myqueue"},
			Timeout:   defaultRabbitMQCtlTimeout,
			Dangerous: true,
		},
	}

	for _, cmd := range commands {
		m.addCommandToCategory("queues", cmd)
	}
}

// initializeDefinitionsCommands initializes import/export definition commands.
func (m *MetadataService) initializeDefinitionsCommands() {
	commands := []RabbitMQCtlCommand{
		{
			Name:        "export_definitions",
			Description: "Export definitions to a file",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "file", Description: "Output file path", Required: true, Type: "string"},
			},
			Options:  []RabbitMQCtlCommandOption{},
			Usage:    "rabbitmqctl export_definitions <file>",
			Examples: []string{"rabbitmqctl export_definitions /tmp/definitions.json"},
			Timeout:  LongRabbitmqctlTimeout,
		},
		{
			Name:        "import_definitions",
			Description: "Import definitions from a file",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "file", Description: "Input file path", Required: true, Type: "string"},
			},
			Options:   []RabbitMQCtlCommandOption{},
			Usage:     "rabbitmqctl import_definitions <file>",
			Examples:  []string{"rabbitmqctl import_definitions /tmp/definitions.json"},
			Timeout:   LongRabbitmqctlTimeout,
			Dangerous: true,
		},
	}

	for _, cmd := range commands {
		m.addCommandToCategory("definitions", cmd)
	}
}

// initializeOperationsCommands initializes operational commands.
func (m *MetadataService) initializeOperationsCommands() {
	commands := []RabbitMQCtlCommand{
		{
			Name:        "stop_app",
			Description: "Stop the RabbitMQ application",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options:     []RabbitMQCtlCommandOption{},
			Usage:       "rabbitmqctl stop_app",
			Examples:    []string{"rabbitmqctl stop_app"},
			Timeout:     LongRabbitmqctlTimeout,
			Dangerous:   true,
		},
		{
			Name:        "start_app",
			Description: "Start the RabbitMQ application",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options:     []RabbitMQCtlCommandOption{},
			Usage:       "rabbitmqctl start_app",
			Examples:    []string{"rabbitmqctl start_app"},
			Timeout:     LongRabbitmqctlTimeout,
			Dangerous:   true,
		},
		{
			Name:        "reset",
			Description: "Reset node to default state",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options:     []RabbitMQCtlCommandOption{},
			Usage:       "rabbitmqctl reset",
			Examples:    []string{"rabbitmqctl reset"},
			Timeout:     LongRabbitmqctlTimeout,
			Dangerous:   true,
		},
		{
			Name:        "force_reset",
			Description: "Force reset node to default state",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options:     []RabbitMQCtlCommandOption{},
			Usage:       "rabbitmqctl force_reset",
			Examples:    []string{"rabbitmqctl force_reset"},
			Timeout:     LongRabbitmqctlTimeout,
			Dangerous:   true,
		},
		{
			Name:        "rotate_logs",
			Description: "Rotate log files",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options:     []RabbitMQCtlCommandOption{},
			Usage:       "rabbitmqctl rotate_logs",
			Examples:    []string{"rabbitmqctl rotate_logs"},
			Timeout:     defaultRabbitMQCtlTimeout,
		},
	}

	for _, cmd := range commands {
		m.addCommandToCategory("operations", cmd)
	}
}

// initializeFeatureFlagsCommands initializes feature flag commands.
func (m *MetadataService) initializeFeatureFlagsCommands() {
	commands := []RabbitMQCtlCommand{
		{
			Name:        "list_feature_flags",
			Description: "List all feature flags",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options:     []RabbitMQCtlCommandOption{},
			Usage:       "rabbitmqctl list_feature_flags",
			Examples:    []string{"rabbitmqctl list_feature_flags"},
			Timeout:     defaultRabbitMQCtlTimeout,
		},
		{
			Name:        "enable_feature_flag",
			Description: "Enable a feature flag",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "flag", Description: "Feature flag name", Required: true, Type: "string"},
			},
			Options:   []RabbitMQCtlCommandOption{},
			Usage:     "rabbitmqctl enable_feature_flag <flag>",
			Examples:  []string{"rabbitmqctl enable_feature_flag quorum_queue"},
			Timeout:   defaultRabbitMQCtlTimeout,
			Dangerous: true,
		},
	}

	for _, cmd := range commands {
		m.addCommandToCategory("feature_flags", cmd)
	}
}

// initializeConfigEnvCommands initializes config and environment commands.
func (m *MetadataService) initializeConfigEnvCommands() {
	commands := []RabbitMQCtlCommand{
		{
			Name:        "environment",
			Description: "Display environment information",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options:     []RabbitMQCtlCommandOption{},
			Usage:       "rabbitmqctl environment",
			Examples:    []string{"rabbitmqctl environment"},
			Timeout:     defaultRabbitMQCtlTimeout,
		},
		{
			Name:        "eval",
			Description: "Execute Erlang expression",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "expression", Description: "Erlang expression", Required: true, Type: "string"},
			},
			Options:   []RabbitMQCtlCommandOption{},
			Usage:     "rabbitmqctl eval <expression>",
			Examples:  []string{"rabbitmqctl eval 'rabbit_mnesia:status().'"},
			Timeout:   LongRabbitmqctlTimeout,
			Dangerous: true,
		},
	}

	for _, cmd := range commands {
		m.addCommandToCategory("config_env", cmd)
	}
}

// initializeMQTTCommands initializes MQTT plugin commands.
func (m *MetadataService) initializeMQTTCommands() {
	commands := []RabbitMQCtlCommand{
		{
			Name:        "list_mqtt_connections",
			Description: "List MQTT connections",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options:     []RabbitMQCtlCommandOption{},
			Usage:       "rabbitmqctl list_mqtt_connections",
			Examples:    []string{"rabbitmqctl list_mqtt_connections"},
			Timeout:     defaultRabbitMQCtlTimeout,
		},
	}

	for _, cmd := range commands {
		m.addCommandToCategory("mqtt", cmd)
	}
}

// initializeManagementCommands initializes management plugin commands.
func (m *MetadataService) initializeManagementCommands() {
	commands := []RabbitMQCtlCommand{
		{
			Name:        "list_bindings",
			Description: "List all bindings",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "bindinginfoitem", Description: "Binding info items", Required: false, Type: "string"},
			},
			Options: []RabbitMQCtlCommandOption{
				{Name: "-p", Description: "Virtual host", Type: "vhost", Default: "/"},
			},
			Usage:    "rabbitmqctl list_bindings [-p <vhost>] [bindinginfoitem ...]",
			Examples: []string{"rabbitmqctl list_bindings", "rabbitmqctl list_bindings source_name destination_name"},
			Timeout:  defaultRabbitMQCtlTimeout,
		},
		{
			Name:        "list_exchanges",
			Description: "List all exchanges",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "exchangeinfoitem", Description: "Exchange info items", Required: false, Type: "string"},
			},
			Options: []RabbitMQCtlCommandOption{
				{Name: "-p", Description: "Virtual host", Type: "vhost", Default: "/"},
			},
			Usage:    "rabbitmqctl list_exchanges [-p <vhost>] [exchangeinfoitem ...]",
			Examples: []string{"rabbitmqctl list_exchanges", "rabbitmqctl list_exchanges name type"},
			Timeout:  defaultRabbitMQCtlTimeout,
		},
	}

	for _, cmd := range commands {
		m.addCommandToCategory("management", cmd)
	}
}

// initializeSTOMPCommands initializes STOMP plugin commands.
func (m *MetadataService) initializeSTOMPCommands() {
	commands := []RabbitMQCtlCommand{
		{
			Name:        "list_stomp_connections",
			Description: "List STOMP connections",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options:     []RabbitMQCtlCommandOption{},
			Usage:       "rabbitmqctl list_stomp_connections",
			Examples:    []string{"rabbitmqctl list_stomp_connections"},
			Timeout:     defaultRabbitMQCtlTimeout,
		},
	}

	for _, cmd := range commands {
		m.addCommandToCategory("stomp", cmd)
	}
}

// initializeStreamCommands initializes stream plugin commands.
func (m *MetadataService) initializeStreamCommands() {
	commands := []RabbitMQCtlCommand{
		{
			Name:        "list_stream_connections",
			Description: "List stream connections",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options:     []RabbitMQCtlCommandOption{},
			Usage:       "rabbitmqctl list_stream_connections",
			Examples:    []string{"rabbitmqctl list_stream_connections"},
			Timeout:     defaultRabbitMQCtlTimeout,
		},
	}

	for _, cmd := range commands {
		m.addCommandToCategory("stream", cmd)
	}
}

// initializeOtherCommands initializes miscellaneous commands.
func (m *MetadataService) initializeOtherCommands() {
	commands := []RabbitMQCtlCommand{
		{
			Name:        "help",
			Description: "Display help information",
			Arguments: []RabbitMQCtlCommandArgument{
				{Name: "command", Description: "Command to get help for", Required: false, Type: "string"},
			},
			Options:  []RabbitMQCtlCommandOption{},
			Usage:    "rabbitmqctl help [command]",
			Examples: []string{"rabbitmqctl help", "rabbitmqctl help list_queues"},
			Timeout:  DefaultRabbitmqctlTimeout,
		},
		{
			Name:        "version",
			Description: "Display version information",
			Arguments:   []RabbitMQCtlCommandArgument{},
			Options:     []RabbitMQCtlCommandOption{},
			Usage:       "rabbitmqctl version",
			Examples:    []string{"rabbitmqctl version"},
			Timeout:     DefaultRabbitmqctlTimeout,
		},
	}

	for _, cmd := range commands {
		m.addCommandToCategory("other", cmd)
	}
}
