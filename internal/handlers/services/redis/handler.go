package redis

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"
	"blacksmith/pkg/services"
	"blacksmith/pkg/services/common"
	"blacksmith/pkg/services/redis"
)

// Constants for Redis handler operations.
const (
	// Default connection timeout for Redis operations.
	defaultRedisConnectionTimeout = 30 * time.Second
)

// Handler handles Redis service HTTP requests.
type Handler struct {
	logger          interfaces.Logger
	vault           interfaces.Vault
	servicesManager *services.Manager
}

// NewHandler creates a new Redis handler.
func NewHandler(logger interfaces.Logger, vault interfaces.Vault, servicesManager *services.Manager) *Handler {
	return &Handler{
		logger:          logger,
		vault:           vault,
		servicesManager: servicesManager,
	}
}

// CanHandle checks if this handler can handle the given path.
func (h *Handler) CanHandle(path string) bool {
	// Check for Redis service endpoints
	pattern := regexp.MustCompile(`^/b/[^/]+/redis/.+$`)

	return pattern.MatchString(path)
}

// ServeHTTP handles HTTP requests for Redis endpoints.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	pattern := regexp.MustCompile(`^/b/([^/]+)/redis/(.+)$`)

	matches := pattern.FindStringSubmatch(req.URL.Path)
	if matches == nil {
		response.WriteError(writer, http.StatusNotFound, "endpoint not found")

		return
	}

	instanceID := matches[1]
	operation := matches[2]

	logger := h.logger.Named("redis-testing")
	logger.Debug("Redis operation %s for instance %s", operation, instanceID)

	// Get credentials from vault
	var creds map[string]interface{}

	exists, err := h.vault.Get(req.Context(), instanceID+"/credentials", &creds)
	if err != nil || !exists {
		logger.Error("Unable to find credentials for instance %s", instanceID)
		response.WriteError(writer, http.StatusNotFound, "credentials not found")

		return
	}

	// Check if this is a Redis instance
	if !services.IsRedisInstance(common.Credentials(creds)) {
		logger.Debug("Instance %s is not identified as Redis", instanceID)
		response.WriteError(writer, http.StatusBadRequest, "not a Redis instance")

		return
	}

	// Security validation
	params := map[string]interface{}{
		"operation":   operation,
		"instance_id": instanceID,
	}

	err = h.servicesManager.Security.ValidateRequest(instanceID, operation, params)
	if err != nil {
		if h.servicesManager.Security.HandleSecurityError(writer, err) {
			return
		}
	}

	// Add rate limit headers
	if headers := h.servicesManager.Security.GetRateLimitHeaders(instanceID, operation); headers != nil {
		for key, value := range headers {
			writer.Header().Set(key, value)
		}
	}

	// Handle Redis operations
	ctx, cancel := context.WithTimeout(req.Context(), DefaultHandlerTimeout)
	defer cancel()

	h.handleRedisOperation(ctx, writer, req, instanceID, operation, creds, logger)
}

// handleRedisOperation handles specific Redis operations.
func (h *Handler) handleRedisOperation(ctx context.Context, writer http.ResponseWriter, req *http.Request, instanceID, operation string, creds map[string]interface{}, _ interfaces.Logger) {
	switch operation {
	case "test":
		h.handleTest(ctx, writer, req, instanceID, creds)
	case "info":
		h.handleInfo(ctx, writer, req, instanceID, creds)
	case "set":
		h.handleSet(ctx, writer, req, instanceID, creds)
	case "get":
		h.handleGet(ctx, writer, req, instanceID, creds)
	case "delete":
		h.handleDelete(ctx, writer, req, instanceID, creds)
	case "command":
		h.handleCommand(ctx, writer, req, instanceID, creds)
	case "keys":
		h.handleKeys(ctx, writer, req, instanceID, creds)
	case "flush":
		h.handleFlush(ctx, writer, req, instanceID, creds)
	default:
		response.WriteError(writer, http.StatusBadRequest, "unknown Redis operation: "+operation)
	}
}

// handleTest handles Redis connection test.
func (h *Handler) handleTest(ctx context.Context, writer http.ResponseWriter, req *http.Request, _ string, creds map[string]interface{}) {
	useTLS := req.URL.Query().Get("use_tls") == "true"
	connectionType := req.URL.Query().Get("connection_type")

	// Handle connection type parameter from frontend
	if connectionType == "tls" {
		useTLS = true
	}

	opts := common.ConnectionOptions{
		UseTLS:  useTLS,
		Timeout: defaultRedisConnectionTimeout,
	}
	result, err := h.servicesManager.Redis.TestConnection(ctx, common.Credentials(creds), opts)
	response.HandleJSON(writer, result, err)
}

// handleInfo handles Redis INFO command.
func (h *Handler) handleInfo(ctx context.Context, writer http.ResponseWriter, req *http.Request, instanceID string, creds map[string]interface{}) {
	useTLS := req.URL.Query().Get("use_tls") == "true"
	result, err := h.servicesManager.Redis.HandleInfo(ctx, instanceID, common.Credentials(creds), useTLS)
	response.HandleJSON(writer, result, err)
}

// handleSet handles Redis SET operation.
func (h *Handler) handleSet(ctx context.Context, writer http.ResponseWriter, req *http.Request, instanceID string, creds map[string]interface{}) {
	var setReq redis.SetRequest

	err := json.NewDecoder(req.Body).Decode(&setReq)
	if err != nil {
		response.WriteError(writer, http.StatusBadRequest, "invalid request body: "+err.Error())

		return
	}

	setReq.InstanceID = instanceID
	result, err := h.servicesManager.Redis.HandleSet(ctx, instanceID, common.Credentials(creds), &setReq)
	response.HandleJSON(writer, result, err)
}

// handleGet handles Redis GET operation.
func (h *Handler) handleGet(ctx context.Context, writer http.ResponseWriter, req *http.Request, instanceID string, creds map[string]interface{}) {
	var getReq redis.GetRequest

	err := json.NewDecoder(req.Body).Decode(&getReq)
	if err != nil {
		response.WriteError(writer, http.StatusBadRequest, "invalid request body: "+err.Error())

		return
	}

	getReq.InstanceID = instanceID
	result, err := h.servicesManager.Redis.HandleGet(ctx, instanceID, common.Credentials(creds), &getReq)
	response.HandleJSON(writer, result, err)
}

// handleDelete handles Redis DELETE operation.
func (h *Handler) handleDelete(ctx context.Context, writer http.ResponseWriter, req *http.Request, instanceID string, creds map[string]interface{}) {
	var delReq redis.DeleteRequest

	err := json.NewDecoder(req.Body).Decode(&delReq)
	if err != nil {
		response.WriteError(writer, http.StatusBadRequest, "invalid request body: "+err.Error())

		return
	}

	delReq.InstanceID = instanceID
	result, err := h.servicesManager.Redis.HandleDelete(ctx, instanceID, common.Credentials(creds), &delReq)
	response.HandleJSON(writer, result, err)
}

// handleCommand handles Redis custom command execution.
func (h *Handler) handleCommand(ctx context.Context, writer http.ResponseWriter, req *http.Request, instanceID string, creds map[string]interface{}) {
	var cmdReq redis.CommandRequest

	err := json.NewDecoder(req.Body).Decode(&cmdReq)
	if err != nil {
		response.WriteError(writer, http.StatusBadRequest, "invalid request body: "+err.Error())

		return
	}

	cmdReq.InstanceID = instanceID
	result, err := h.servicesManager.Redis.HandleCommand(ctx, instanceID, common.Credentials(creds), &cmdReq)
	response.HandleJSON(writer, result, err)
}

// handleKeys handles Redis KEYS operation.
func (h *Handler) handleKeys(ctx context.Context, writer http.ResponseWriter, req *http.Request, instanceID string, creds map[string]interface{}) {
	var keysReq redis.KeysRequest

	err := json.NewDecoder(req.Body).Decode(&keysReq)
	if err != nil {
		response.WriteError(writer, http.StatusBadRequest, "invalid request body: "+err.Error())

		return
	}

	keysReq.InstanceID = instanceID
	result, err := h.servicesManager.Redis.HandleKeys(ctx, instanceID, common.Credentials(creds), &keysReq)
	response.HandleJSON(writer, result, err)
}

// handleFlush handles Redis FLUSH operation.
func (h *Handler) handleFlush(ctx context.Context, writer http.ResponseWriter, req *http.Request, instanceID string, creds map[string]interface{}) {
	var flushReq redis.FlushRequest

	err := json.NewDecoder(req.Body).Decode(&flushReq)
	if err != nil {
		response.WriteError(writer, http.StatusBadRequest, "invalid request body: "+err.Error())

		return
	}

	flushReq.InstanceID = instanceID
	result, err := h.servicesManager.Redis.HandleFlush(ctx, instanceID, common.Credentials(creds), &flushReq)
	response.HandleJSON(writer, result, err)
}
