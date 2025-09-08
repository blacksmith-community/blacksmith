package rabbitmq

import (
	"time"

	"blacksmith/pkg/services/common"
)

// Credentials represents RabbitMQ credentials from Vault.
type Credentials struct {
	Host               string                            `json:"host"`
	Hostname           string                            `json:"hostname"`
	Port               int                               `json:"port"`
	TLSPort            int                               `json:"tls_port"`
	Username           string                            `json:"username"`
	Password           string                            `json:"password"`
	ManagementUsername string                            `json:"management_username"`
	ManagementPassword string                            `json:"management_password"`
	VHost              string                            `json:"vhost"`
	URI                string                            `json:"uri"`
	TLSURI             string                            `json:"tls_uri"`
	Protocols          map[string]map[string]interface{} `json:"protocols"`
}

// NewCredentials creates RabbitMQ credentials from Vault data.
func NewCredentials(vaultData common.Credentials) (*Credentials, error) {
	creds := &Credentials{
		Host:               vaultData.GetString("host"),
		Hostname:           vaultData.GetString("hostname"),
		Port:               vaultData.GetInt("port"),
		TLSPort:            vaultData.GetInt("tls_port"),
		Username:           vaultData.GetString("username"),
		Password:           vaultData.GetString("password"),
		ManagementUsername: vaultData.GetString("management_username"),
		ManagementPassword: vaultData.GetString("management_password"),
		VHost:              vaultData.GetString("vhost"),
		URI:                vaultData.GetString("uri"),
		TLSURI:             vaultData.GetString("tls_uri"),
	}

	// Handle protocols map
	if protocols := vaultData.GetMap("protocols"); protocols != nil {
		creds.Protocols = make(map[string]map[string]interface{})
		for proto, data := range protocols {
			if protoData, ok := data.(map[string]interface{}); ok {
				creds.Protocols[proto] = protoData
			}
		}
	}

	// Use hostname as fallback for host
	if creds.Host == "" && creds.Hostname != "" {
		creds.Host = creds.Hostname
	}

	// Validate required fields
	if creds.Host == "" {
		return nil, common.NewServiceError("rabbitmq", "E001", "missing host in credentials", false)
	}

	if creds.Port == 0 {
		creds.Port = 5672 // Default AMQP port
	}

	if creds.Username == "" {
		return nil, common.NewServiceError("rabbitmq", "E002", "missing username in credentials", false)
	}

	// Default VHost
	if creds.VHost == "" {
		creds.VHost = "/"
	}

	return creds, nil
}

// PublishRequest represents a RabbitMQ publish operation request.
type PublishRequest struct {
	InstanceID         string `json:"instance_id"`
	Queue              string `json:"queue"`
	Exchange           string `json:"exchange"`
	Message            string `json:"message"`
	Persistent         bool   `json:"persistent"`
	UseAMQPS           bool   `json:"use_amqps"`
	ConnectionUser     string `json:"connection_user"`
	ConnectionPassword string `json:"connection_password"`
	ConnectionVHost    string `json:"connection_vhost"`
}

// PublishResult represents a RabbitMQ publish operation result.
type PublishResult struct {
	Success   bool   `json:"success"`
	Queue     string `json:"queue"`
	Exchange  string `json:"exchange"`
	Message   string `json:"message"`
	Timestamp int64  `json:"timestamp"`
}

// ConsumeRequest represents a RabbitMQ consume operation request.
type ConsumeRequest struct {
	InstanceID         string `json:"instance_id"`
	Queue              string `json:"queue"`
	Count              int    `json:"count"`
	Timeout            int    `json:"timeout"` // milliseconds
	AutoAck            bool   `json:"auto_ack"`
	UseAMQPS           bool   `json:"use_amqps"`
	ConnectionUser     string `json:"connection_user"`
	ConnectionPassword string `json:"connection_password"`
	ConnectionVHost    string `json:"connection_vhost"`
}

// ConsumeResult represents a RabbitMQ consume operation result.
type ConsumeResult struct {
	Success  bool      `json:"success"`
	Queue    string    `json:"queue"`
	Messages []Message `json:"messages"`
	Count    int       `json:"count"`
}

// Message represents a RabbitMQ message.
type Message struct {
	Body       string `json:"body"`
	MessageID  string `json:"message_id"`
	Timestamp  int64  `json:"timestamp"`
	Exchange   string `json:"exchange"`
	RoutingKey string `json:"routing_key"`
}

// QueueInfoRequest represents a queue information request.
type QueueInfoRequest struct {
	InstanceID         string `json:"instance_id"`
	UseAMQPS           bool   `json:"use_amqps"`
	ConnectionUser     string `json:"connection_user"`
	ConnectionPassword string `json:"connection_password"`
	ConnectionVHost    string `json:"connection_vhost"`
}

// QueueInfoResult represents queue information.
type QueueInfoResult struct {
	Success bool    `json:"success"`
	Queues  []Queue `json:"queues"`
}

// Queue represents a RabbitMQ queue.
type Queue struct {
	Name      string `json:"name"`
	Messages  int    `json:"messages"`
	Consumers int    `json:"consumers"`
	VHost     string `json:"vhost"`
	Durable   bool   `json:"durable"`
	State     string `json:"state"`
}

// QueueOpsRequest represents queue operations request.
type QueueOpsRequest struct {
	InstanceID         string `json:"instance_id"`
	Queue              string `json:"queue"`
	Operation          string `json:"operation"` // create, delete, purge
	Durable            bool   `json:"durable"`
	UseAMQPS           bool   `json:"use_amqps"`
	ConnectionUser     string `json:"connection_user"`
	ConnectionPassword string `json:"connection_password"`
	ConnectionVHost    string `json:"connection_vhost"`
}

// QueueOpsResult represents queue operations result.
type QueueOpsResult struct {
	Success   bool   `json:"success"`
	Queue     string `json:"queue"`
	Operation string `json:"operation"`
}

// ManagementRequest represents a management API request.
type ManagementRequest struct {
	InstanceID         string `json:"instance_id"`
	Path               string `json:"path"`
	Method             string `json:"method"`
	UseSSL             bool   `json:"use_ssl"`
	ConnectionUser     string `json:"connection_user"`
	ConnectionPassword string `json:"connection_password"`
	ConnectionVHost    string `json:"connection_vhost"`
}

// ManagementResult represents a management API result.
type ManagementResult struct {
	Success    bool        `json:"success"`
	StatusCode int         `json:"status_code"`
	Data       interface{} `json:"data"`
}

// ConnectionInfo contains connection metadata.
type ConnectionInfo struct {
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	VHost     string    `json:"vhost"`
	AMQPS     bool      `json:"amqps"`
	Connected bool      `json:"connected"`
	LastPing  time.Time `json:"last_ping"`
}
