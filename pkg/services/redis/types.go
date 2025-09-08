package redis

import (
	"time"

	"blacksmith/pkg/services/common"
)

// Credentials represents Redis credentials from Vault.
type Credentials struct {
	Host     string `json:"host"`
	Hostname string `json:"hostname"`
	Port     int    `json:"port"`
	TLSPort  int    `json:"tls_port"`
	Password string `json:"password"`
	URI      string `json:"uri"`
	TLSURI   string `json:"tls_uri"`
}

// NewCredentials creates Redis credentials from Vault data.
func NewCredentials(vaultData common.Credentials) (*Credentials, error) {
	creds := &Credentials{
		Host:     vaultData.GetString("host"),
		Hostname: vaultData.GetString("hostname"),
		Port:     vaultData.GetInt("port"),
		TLSPort:  vaultData.GetInt("tls_port"),
		Password: vaultData.GetString("password"),
		URI:      vaultData.GetString("uri"),
		TLSURI:   vaultData.GetString("tls_uri"),
	}

	// Use hostname as fallback for host
	if creds.Host == "" && creds.Hostname != "" {
		creds.Host = creds.Hostname
	}

	// Validate required fields
	if creds.Host == "" {
		return nil, common.NewServiceError("redis", "E001", "missing host in credentials", false)
	}

	if creds.Port == 0 {
		creds.Port = 6379 // Default Redis port
	}

	return creds, nil
}

// SetRequest represents a Redis SET operation request.
type SetRequest struct {
	InstanceID string `json:"instance_id"`
	Key        string `json:"key"`
	Value      string `json:"value"`
	TTL        int    `json:"ttl"`
	UseTLS     bool   `json:"use_tls"`
}

// SetResult represents a Redis SET operation result.
type SetResult struct {
	Success bool   `json:"success"`
	Key     string `json:"key"`
	Value   string `json:"value"`
	TTL     int    `json:"ttl"`
}

// GetRequest represents a Redis GET operation request.
type GetRequest struct {
	InstanceID string `json:"instance_id"`
	Key        string `json:"key"`
	UseTLS     bool   `json:"use_tls"`
}

// GetResult represents a Redis GET operation result.
type GetResult struct {
	Success bool   `json:"success"`
	Key     string `json:"key"`
	Value   string `json:"value"`
	Exists  bool   `json:"exists"`
}

// DeleteRequest represents a Redis DEL operation request.
type DeleteRequest struct {
	InstanceID string   `json:"instance_id"`
	Keys       []string `json:"keys"`
	UseTLS     bool     `json:"use_tls"`
}

// DeleteResult represents a Redis DEL operation result.
type DeleteResult struct {
	Success     bool     `json:"success"`
	Keys        []string `json:"keys"`
	DeletedKeys int      `json:"deleted_keys"`
}

// CommandRequest represents a Redis command execution request.
type CommandRequest struct {
	InstanceID string        `json:"instance_id"`
	Command    string        `json:"command"`
	Args       []interface{} `json:"args"`
	UseTLS     bool          `json:"use_tls"`
}

// CommandResult represents a Redis command execution result.
type CommandResult struct {
	Success bool        `json:"success"`
	Command string      `json:"command"`
	Result  interface{} `json:"result"`
}

// KeysRequest represents a Redis KEYS operation request.
type KeysRequest struct {
	InstanceID string `json:"instance_id"`
	Pattern    string `json:"pattern"`
	UseTLS     bool   `json:"use_tls"`
}

// KeysResult represents a Redis KEYS operation result.
type KeysResult struct {
	Success bool     `json:"success"`
	Pattern string   `json:"pattern"`
	Keys    []string `json:"keys"`
	Count   int      `json:"count"`
}

// FlushRequest represents a Redis flush operation request.
type FlushRequest struct {
	InstanceID string `json:"instance_id"`
	Database   int    `json:"database"`
	UseTLS     bool   `json:"use_tls"`
}

// FlushResult represents a Redis flush operation result.
type FlushResult struct {
	Success  bool `json:"success"`
	Database int  `json:"database"`
}

// InfoResult represents Redis INFO command result.
type InfoResult struct {
	Success bool                   `json:"success"`
	Data    map[string]interface{} `json:"data"`
	Raw     string                 `json:"raw"`
}

// ConnectionInfo contains connection metadata.
type ConnectionInfo struct {
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	TLS       bool      `json:"tls"`
	Database  int       `json:"database"`
	Connected bool      `json:"connected"`
	LastPing  time.Time `json:"last_ping"`
}
