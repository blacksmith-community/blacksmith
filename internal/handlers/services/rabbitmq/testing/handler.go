package testing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/http/response"
	"blacksmith/pkg/services"
	"blacksmith/pkg/services/common"
	"blacksmith/pkg/services/rabbitmq"
)

const (
	defaultOperationTimeout = 30 * time.Second
	trueString              = "true"
)

var (
	rabbitMQTestingPathPattern  = regexp.MustCompile(`^/b/([^/]+)/rabbitmq/(.+)$`)
	errCredentialsNotFound      = errors.New("credentials not found")
	errNotRabbitMQInstance      = errors.New("not a RabbitMQ instance")
)

type Handler struct {
	logger          interfaces.Logger
	vault           interfaces.Vault
	servicesManager *services.Manager
	security        *services.SecurityMiddleware
}

type Dependencies struct {
	Logger          interfaces.Logger
	Vault           interfaces.Vault
	ServicesManager *services.Manager
	Security        *services.SecurityMiddleware
}

func NewHandler(deps Dependencies) *Handler {
	return &Handler{
		logger:          deps.Logger,
		vault:           deps.Vault,
		servicesManager: deps.ServicesManager,
		security:        deps.Security,
	}
}

func (h *Handler) CanHandle(path string) bool {
	matches := rabbitMQTestingPathPattern.FindStringSubmatch(path)
	if matches == nil {
		return false
	}

	operation := matches[2]

	switch operation {
	case "test", "publish", "consume", "queues", "queue-ops", "management":
		return true
	default:
		return false
	}
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	responseWriter := writer
	req := request

	matches := rabbitMQTestingPathPattern.FindStringSubmatch(req.URL.Path)
	if matches == nil {
		response.WriteError(responseWriter, http.StatusNotFound, "Invalid RabbitMQ testing endpoint")

		return
	}

	h.handleRequest(responseWriter, req, matches[1], matches[2])
}

func (h *Handler) handleRequest(responseWriter http.ResponseWriter, req *http.Request, instanceID, operation string) {
	logger := h.logger.Named("rabbitmq-testing")
	logger.Debug("RabbitMQ operation %s for instance %s", operation, instanceID)

	ctx := req.Context()

	creds, err := h.getAndValidateCredentials(ctx, instanceID, logger)
	if err != nil {
		h.handleCredentialError(responseWriter, err)

		return
	}

	secErr := h.validateSecurity(instanceID, operation, responseWriter)
	if secErr != nil {
		return
	}

	h.addRateLimitHeaders(instanceID, operation, responseWriter)

	opCtx, cancel := context.WithTimeout(ctx, defaultOperationTimeout)
	defer cancel()

	h.routeOperation(opCtx, responseWriter, req, instanceID, operation, creds, logger)
}

func (h *Handler) handleCredentialError(responseWriter http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errCredentialsNotFound):
		response.WriteError(responseWriter, http.StatusNotFound, err.Error())
	case errors.Is(err, errNotRabbitMQInstance):
		response.WriteError(responseWriter, http.StatusBadRequest, err.Error())
	default:
		response.WriteError(responseWriter, http.StatusInternalServerError, err.Error())
	}
}

func (h *Handler) routeOperation(ctx context.Context, responseWriter http.ResponseWriter, req *http.Request, instanceID, operation string, creds common.Credentials, logger interfaces.Logger) {
	switch operation {
	case "test":
		h.handleTest(ctx, responseWriter, req, instanceID, creds, logger)
	case "publish":
		h.handlePublish(ctx, responseWriter, req, instanceID, creds, logger)
	case "consume":
		h.handleConsume(ctx, responseWriter, req, instanceID, creds, logger)
	case "queues":
		h.handleQueues(ctx, responseWriter, req, instanceID, creds, logger)
	case "queue-ops":
		h.handleQueueOps(ctx, responseWriter, req, instanceID, creds, logger)
	case "management":
		h.handleManagement(ctx, responseWriter, req, instanceID, creds, logger)
	default:
		response.WriteError(responseWriter, http.StatusNotFound, "Unknown RabbitMQ operation: "+operation)
	}
}

func (h *Handler) getAndValidateCredentials(ctx context.Context, instanceID string, logger interfaces.Logger) (common.Credentials, error) {
	var creds map[string]interface{}

	exists, err := h.vault.Get(ctx, instanceID+"/credentials", &creds)
	if err != nil || !exists {
		logger.Error("Unable to find credentials for instance %s", instanceID)

		return nil, errCredentialsNotFound
	}

	if !services.IsRabbitMQInstance(common.Credentials(creds)) {
		logger.Debug("Instance %s is not identified as RabbitMQ", instanceID)

		return nil, errNotRabbitMQInstance
	}

	return common.Credentials(creds), nil
}

func (h *Handler) validateSecurity(instanceID, operation string, writer http.ResponseWriter) error {
	if h.security == nil {
		return nil
	}

	params := map[string]interface{}{
		"operation":   operation,
		"instance_id": instanceID,
	}

	err := h.security.ValidateRequest(instanceID, operation, params)
	if err != nil {
		if h.security.HandleSecurityError(writer, err) {
			return fmt.Errorf("security validation failed: %w", err)
		}
	}

	return nil
}

func (h *Handler) addRateLimitHeaders(instanceID, operation string, writer http.ResponseWriter) {
	if h.security == nil {
		return
	}

	if headers := h.security.GetRateLimitHeaders(instanceID, operation); headers != nil {
		for key, value := range headers {
			writer.Header().Set(key, value)
		}
	}
}

func (h *Handler) handleTest(ctx context.Context, writer http.ResponseWriter, request *http.Request, _ string, creds common.Credentials, _ interfaces.Logger) {
	useAMQPS := request.URL.Query().Get("use_amqps") == trueString
	connectionType := request.URL.Query().Get("connection_type")
	connectionUser := request.URL.Query().Get("connection_user")
	connectionVHost := request.URL.Query().Get("connection_vhost")
	connectionPassword := request.URL.Query().Get("connection_password")

	if connectionType == "amqps" {
		useAMQPS = true
	}

	opts := common.ConnectionOptions{
		UseAMQPS:         useAMQPS,
		Timeout:          defaultOperationTimeout,
		OverrideUser:     connectionUser,
		OverridePassword: connectionPassword,
		OverrideVHost:    connectionVHost,
	}

	result, err := h.servicesManager.RabbitMQ.TestConnection(ctx, creds, opts)
	response.HandleJSON(writer, result, err)
}

func (h *Handler) handlePublish(ctx context.Context, writer http.ResponseWriter, request *http.Request, instanceID string, creds common.Credentials, _ interfaces.Logger) {
	var pubReq rabbitmq.PublishRequest

	err := json.NewDecoder(request.Body).Decode(&pubReq)
	if err != nil {
		response.WriteError(writer, http.StatusBadRequest, "invalid request body: "+err.Error())

		return
	}

	pubReq.InstanceID = instanceID

	opts := common.ConnectionOptions{
		UseAMQPS:         pubReq.UseAMQPS,
		Timeout:          defaultOperationTimeout,
		OverrideUser:     pubReq.ConnectionUser,
		OverridePassword: pubReq.ConnectionPassword,
		OverrideVHost:    pubReq.ConnectionVHost,
	}

	result, err := h.servicesManager.RabbitMQ.HandlePublishWithOptions(ctx, instanceID, creds, &pubReq, opts)
	response.HandleJSON(writer, result, err)
}

func (h *Handler) handleConsume(ctx context.Context, writer http.ResponseWriter, request *http.Request, instanceID string, creds common.Credentials, _ interfaces.Logger) {
	var consReq rabbitmq.ConsumeRequest

	err := json.NewDecoder(request.Body).Decode(&consReq)
	if err != nil {
		response.WriteError(writer, http.StatusBadRequest, "invalid request body: "+err.Error())

		return
	}

	consReq.InstanceID = instanceID

	opts := common.ConnectionOptions{
		UseAMQPS:         consReq.UseAMQPS,
		Timeout:          defaultOperationTimeout,
		OverrideUser:     consReq.ConnectionUser,
		OverridePassword: consReq.ConnectionPassword,
		OverrideVHost:    consReq.ConnectionVHost,
	}

	result, err := h.servicesManager.RabbitMQ.HandleConsumeWithOptions(ctx, instanceID, creds, &consReq, opts)
	response.HandleJSON(writer, result, err)
}

func (h *Handler) handleQueues(ctx context.Context, writer http.ResponseWriter, request *http.Request, instanceID string, creds common.Credentials, _ interfaces.Logger) {
	var queueReq rabbitmq.QueueInfoRequest

	if request.Method == http.MethodPost {
		decodeErr := json.NewDecoder(request.Body).Decode(&queueReq)
		if decodeErr != nil {
			queueReq.UseAMQPS = request.URL.Query().Get("use_amqps") == trueString
		}
	} else {
		queueReq.UseAMQPS = request.URL.Query().Get("use_amqps") == trueString
	}

	queueReq.InstanceID = instanceID

	opts := common.ConnectionOptions{
		UseAMQPS:         queueReq.UseAMQPS,
		Timeout:          defaultOperationTimeout,
		OverrideUser:     queueReq.ConnectionUser,
		OverridePassword: queueReq.ConnectionPassword,
		OverrideVHost:    queueReq.ConnectionVHost,
	}

	result, err := h.servicesManager.RabbitMQ.HandleQueueInfoWithOptions(ctx, instanceID, creds, &queueReq, opts)
	response.HandleJSON(writer, result, err)
}

func (h *Handler) handleQueueOps(ctx context.Context, writer http.ResponseWriter, request *http.Request, instanceID string, creds common.Credentials, _ interfaces.Logger) {
	var queueOpsReq rabbitmq.QueueOpsRequest

	err := json.NewDecoder(request.Body).Decode(&queueOpsReq)
	if err != nil {
		response.WriteError(writer, http.StatusBadRequest, "invalid request body: "+err.Error())

		return
	}

	queueOpsReq.InstanceID = instanceID

	opts := common.ConnectionOptions{
		UseAMQPS:         queueOpsReq.UseAMQPS,
		Timeout:          defaultOperationTimeout,
		OverrideUser:     queueOpsReq.ConnectionUser,
		OverridePassword: queueOpsReq.ConnectionPassword,
		OverrideVHost:    queueOpsReq.ConnectionVHost,
	}

	result, err := h.servicesManager.RabbitMQ.HandleQueueOpsWithOptions(ctx, instanceID, creds, &queueOpsReq, opts)
	response.HandleJSON(writer, result, err)
}

func (h *Handler) handleManagement(ctx context.Context, writer http.ResponseWriter, request *http.Request, instanceID string, creds common.Credentials, _ interfaces.Logger) {
	var mgmtReq rabbitmq.ManagementRequest

	if request.Method == http.MethodPost {
		err := json.NewDecoder(request.Body).Decode(&mgmtReq)
		if err != nil {
			response.WriteError(writer, http.StatusBadRequest, "invalid request body: "+err.Error())

			return
		}
	} else {
		mgmtReq.Method = request.Method
		mgmtReq.Path = request.URL.Query().Get("path")
		mgmtReq.UseSSL = request.URL.Query().Get("use_ssl") == trueString
	}

	connectionUser := request.URL.Query().Get("connection_user")
	connectionPassword := request.URL.Query().Get("connection_password")
	connectionVHost := request.URL.Query().Get("connection_vhost")

	opts := common.ConnectionOptions{
		UseAMQPS:         false,
		Timeout:          defaultOperationTimeout,
		OverrideUser:     connectionUser,
		OverridePassword: connectionPassword,
		OverrideVHost:    connectionVHost,
	}

	result, err := h.servicesManager.RabbitMQ.HandleManagementWithOptions(ctx, instanceID, creds, &mgmtReq, opts)
	response.HandleJSON(writer, result, err)
}