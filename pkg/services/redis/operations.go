package redis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-redis/redis/v8"

	"blacksmith/pkg/services/common"
)

// Handler handles Redis operations.
type Handler struct {
	clientManager *ClientManager
	logger        func(string, ...interface{})
}

// NewHandler creates a new Redis operations handler.
func NewHandler(logger func(string, ...interface{})) *Handler {
	if logger == nil {
		logger = func(string, ...interface{}) {} // No-op logger
	}

	return &Handler{
		clientManager: NewClientManager(DefaultTTL),
		logger:        logger,
	}
}

// TestConnection tests the Redis connection.
func (h *Handler) TestConnection(ctx context.Context, vaultCreds common.Credentials, opts common.ConnectionOptions) (*common.TestResult, error) {
	start := time.Now()

	creds, credsErr := NewCredentials(vaultCreds)
	if credsErr != nil {
		return nil, fmt.Errorf("invalid credentials: %w", credsErr)
	}

	client, clientErr := h.clientManager.GetClient(ctx, "test", creds, opts.UseTLS)
	if clientErr != nil {
		return nil, fmt.Errorf("failed to get Redis client: %w", clientErr)
	}

	// Test basic operations
	pingResult := client.Ping(ctx)

	pingErr := pingResult.Err()
	if pingErr != nil {
		return &common.TestResult{
			Success:   false,
			Error:     fmt.Sprintf("PING failed: %v", pingErr),
			Timestamp: start.Unix(),
			Duration:  time.Since(start),
		}, nil
	}

	// Get server info
	infoResult := client.Info(ctx)

	var serverInfo map[string]string

	infoErr := infoResult.Err()
	if infoErr == nil {
		serverInfo = parseInfoString(infoResult.Val())
	}

	return &common.TestResult{
		Success: true,
		Data: map[string]interface{}{
			"ping_response": pingResult.Val(),
			"server_info":   serverInfo,
			"connection": map[string]interface{}{
				"host": creds.Host,
				"port": creds.Port,
				"tls":  opts.UseTLS,
			},
		},
		Timestamp: start.Unix(),
		Duration:  time.Since(start),
	}, nil
}

// GetCapabilities returns the capabilities of the Redis service.
func (h *Handler) GetCapabilities() []common.Capability {
	return []common.Capability{
		{Name: "info", Description: "Get Redis server information", Category: "monitoring"},
		{Name: "set", Description: "Set a key-value pair", Category: "data"},
		{Name: "get", Description: "Get value by key", Category: "data"},
		{Name: "delete", Description: "Delete keys", Category: "data"},
		{Name: "command", Description: "Execute Redis commands", Category: "admin"},
		{Name: "keys", Description: "List keys by pattern", Category: "data"},
		{Name: "flush", Description: "Flush database", Category: "admin"},
	}
}

// Close closes all connections.
func (h *Handler) Close() error {
	h.clientManager.CloseAll()

	return nil
}

// HandleInfo gets Redis server information.
func (h *Handler) HandleInfo(ctx context.Context, instanceID string, vaultCreds common.Credentials, useTLS bool) (*InfoResult, error) {
	creds, err := NewCredentials(vaultCreds)
	if err != nil {
		return nil, err
	}

	client, err := h.clientManager.GetClient(ctx, instanceID, creds, useTLS)
	if err != nil {
		return nil, err
	}

	info, err := client.Info(ctx).Result()
	if err != nil {
		return nil, common.NewServiceError("redis", "E004", fmt.Sprintf("INFO failed: %v", err), true)
	}

	// Convert map[string]string to map[string]interface{}
	infoData := make(map[string]interface{})
	parsedInfo := parseInfoString(info)

	for k, v := range parsedInfo {
		infoData[k] = v
	}

	return &InfoResult{
		Success: true,
		Data:    infoData,
		Raw:     info,
	}, nil
}

// HandleSet performs a Redis SET operation.
func (h *Handler) HandleSet(ctx context.Context, instanceID string, vaultCreds common.Credentials, req *SetRequest) (*SetResult, error) {
	creds, err := NewCredentials(vaultCreds)
	if err != nil {
		return nil, err
	}

	client, err := h.clientManager.GetClient(ctx, instanceID, creds, req.UseTLS)
	if err != nil {
		return nil, err
	}

	var expiration time.Duration
	if req.TTL > 0 {
		expiration = time.Duration(req.TTL) * time.Second
	}

	err = client.Set(ctx, req.Key, req.Value, expiration).Err()
	if err != nil {
		return nil, common.NewServiceError("redis", "E005", fmt.Sprintf("SET failed: %v", err), true)
	}

	return &SetResult{
		Success: true,
		Key:     req.Key,
		Value:   req.Value,
		TTL:     req.TTL,
	}, nil
}

// HandleGet performs a Redis GET operation.
func (h *Handler) HandleGet(ctx context.Context, instanceID string, vaultCreds common.Credentials, req *GetRequest) (*GetResult, error) {
	creds, err := NewCredentials(vaultCreds)
	if err != nil {
		return nil, err
	}

	client, err := h.clientManager.GetClient(ctx, instanceID, creds, req.UseTLS)
	if err != nil {
		return nil, err
	}

	val, err := client.Get(ctx, req.Key).Result()
	if errors.Is(err, redis.Nil) {
		return &GetResult{
			Success: true,
			Key:     req.Key,
			Value:   "",
			Exists:  false,
		}, nil
	} else if err != nil {
		return nil, common.NewServiceError("redis", "E006", fmt.Sprintf("GET failed: %v", err), true)
	}

	return &GetResult{
		Success: true,
		Key:     req.Key,
		Value:   val,
		Exists:  true,
	}, nil
}

// HandleDelete performs a Redis DEL operation.
func (h *Handler) HandleDelete(ctx context.Context, instanceID string, vaultCreds common.Credentials, req *DeleteRequest) (*DeleteResult, error) {
	creds, err := NewCredentials(vaultCreds)
	if err != nil {
		return nil, err
	}

	client, err := h.clientManager.GetClient(ctx, instanceID, creds, req.UseTLS)
	if err != nil {
		return nil, err
	}

	deleted, err := client.Del(ctx, req.Keys...).Result()
	if err != nil {
		return nil, common.NewServiceError("redis", "E007", fmt.Sprintf("DEL failed: %v", err), true)
	}

	return &DeleteResult{
		Success:     true,
		Keys:        req.Keys,
		DeletedKeys: int(deleted),
	}, nil
}

// HandleCommand executes a Redis command.
func (h *Handler) HandleCommand(ctx context.Context, instanceID string, vaultCreds common.Credentials, req *CommandRequest) (*CommandResult, error) {
	creds, err := NewCredentials(vaultCreds)
	if err != nil {
		return nil, err
	}

	client, err := h.clientManager.GetClient(ctx, instanceID, creds, req.UseTLS)
	if err != nil {
		return nil, err
	}

	// Build command arguments
	args := []interface{}{req.Command}
	args = append(args, req.Args...)

	result, err := client.Do(ctx, args...).Result()
	if err != nil {
		return nil, common.NewServiceError("redis", "E008", fmt.Sprintf("Command failed: %v", err), true)
	}

	return &CommandResult{
		Success: true,
		Command: req.Command,
		Result:  result,
	}, nil
}

// HandleKeys lists keys matching a pattern.
func (h *Handler) HandleKeys(ctx context.Context, instanceID string, vaultCreds common.Credentials, req *KeysRequest) (*KeysResult, error) {
	creds, err := NewCredentials(vaultCreds)
	if err != nil {
		return nil, err
	}

	client, err := h.clientManager.GetClient(ctx, instanceID, creds, req.UseTLS)
	if err != nil {
		return nil, err
	}

	pattern := req.Pattern
	if pattern == "" {
		pattern = "*"
	}

	keys, err := client.Keys(ctx, pattern).Result()
	if err != nil {
		return nil, common.NewServiceError("redis", "E009", fmt.Sprintf("KEYS failed: %v", err), true)
	}

	return &KeysResult{
		Success: true,
		Pattern: pattern,
		Keys:    keys,
		Count:   len(keys),
	}, nil
}

// HandleFlush flushes the Redis database.
func (h *Handler) HandleFlush(ctx context.Context, instanceID string, vaultCreds common.Credentials, req *FlushRequest) (*FlushResult, error) {
	creds, err := NewCredentials(vaultCreds)
	if err != nil {
		return nil, err
	}

	client, err := h.clientManager.GetClient(ctx, instanceID, creds, req.UseTLS)
	if err != nil {
		return nil, err
	}

	err = client.FlushDB(ctx).Err()
	if err != nil {
		return nil, common.NewServiceError("redis", "E010", fmt.Sprintf("FLUSHDB failed: %v", err), true)
	}

	return &FlushResult{
		Success:  true,
		Database: req.Database,
	}, nil
}

// parseInfoString parses Redis INFO command output into a map.
func parseInfoString(info string) map[string]string {
	result := make(map[string]string)
	lines := strings.Split(info, "\n")

	var section string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			if strings.HasPrefix(line, "# ") {
				section = strings.TrimPrefix(line, "# ")
			}

			continue
		}

		parts := strings.SplitN(line, ":", InfoFieldParts)
		if len(parts) == InfoFieldParts {
			key := parts[0]
			if section != "" {
				key = section + "_" + key
			}

			result[key] = parts[1]
		}
	}

	return result
}
