package rabbitmq

import (
	"context"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"blacksmith/pkg/services/common"
)

// Handler handles RabbitMQ operations
type Handler struct {
	connManager      *ConnectionManager
	managementClient *ManagementClient
	logger           func(string, ...interface{})
}

// NewHandler creates a new RabbitMQ operations handler
func NewHandler(logger func(string, ...interface{})) *Handler {
	if logger == nil {
		logger = func(string, ...interface{}) {} // No-op logger
	}

	return &Handler{
		connManager:      NewConnectionManager(5 * time.Minute),
		managementClient: NewManagementClient(),
		logger:           logger,
	}
}

// TestConnection tests the RabbitMQ connection
func (h *Handler) TestConnection(ctx context.Context, vaultCreds common.Credentials, opts common.ConnectionOptions) (*common.TestResult, error) {
	start := time.Now()

	creds, err := NewCredentials(vaultCreds)
	if err != nil {
		return &common.TestResult{
			Success:   false,
			Error:     err.Error(),
			Timestamp: start.Unix(),
			Duration:  time.Since(start),
		}, nil
	}

	// Use override parameters if provided
	var testErr error
	if opts.OverrideUser != "" || opts.OverridePassword != "" || opts.OverrideVHost != "" {
		testErr = h.connManager.TestConnectionWithOverrides(creds, opts.UseAMQPS, opts.OverrideUser, opts.OverridePassword, opts.OverrideVHost)
	} else {
		testErr = h.connManager.TestConnection(creds, opts.UseAMQPS)
	}

	if testErr != nil {
		return &common.TestResult{
			Success:   false,
			Error:     testErr.Error(),
			Timestamp: start.Unix(),
			Duration:  time.Since(start),
		}, nil
	}

	// Determine which user and vhost were actually used for the connection
	actualUser := creds.Username
	actualVHost := creds.VHost

	if opts.OverrideUser != "" {
		actualUser = opts.OverrideUser
	}
	if opts.OverrideVHost != "" {
		actualVHost = opts.OverrideVHost
	}

	return &common.TestResult{
		Success: true,
		Data: map[string]interface{}{
			"connection": map[string]interface{}{
				"host":  creds.Host,
				"port":  creds.Port,
				"vhost": actualVHost,
				"user":  actualUser,
				"amqps": opts.UseAMQPS,
			},
		},
		Timestamp: start.Unix(),
		Duration:  time.Since(start),
	}, nil
}

// GetCapabilities returns the capabilities of the RabbitMQ service
func (h *Handler) GetCapabilities() []common.Capability {
	return []common.Capability{
		{Name: "publish", Description: "Publish messages to queues", Category: "messaging"},
		{Name: "consume", Description: "Consume messages from queues", Category: "messaging"},
		{Name: "queues", Description: "List and inspect queues", Category: "management"},
		{Name: "queue-ops", Description: "Create, delete, and purge queues", Category: "management"},
		{Name: "management", Description: "Access management API", Category: "admin"},
	}
}

// Close closes all connections
func (h *Handler) Close() error {
	h.connManager.CloseAll()
	return nil
}

// HandlePublish publishes a message to RabbitMQ
func (h *Handler) HandlePublish(ctx context.Context, instanceID string, vaultCreds common.Credentials, req *PublishRequest) (*PublishResult, error) {
	// Create default connection options for backward compatibility
	opts := common.ConnectionOptions{
		UseAMQPS: req.UseAMQPS,
		Timeout:  30 * time.Second,
	}
	return h.HandlePublishWithOptions(ctx, instanceID, vaultCreds, req, opts)
}

// HandlePublishWithOptions publishes a message to RabbitMQ with connection options
func (h *Handler) HandlePublishWithOptions(ctx context.Context, instanceID string, vaultCreds common.Credentials, req *PublishRequest, opts common.ConnectionOptions) (*PublishResult, error) {
	creds, err := NewCredentials(vaultCreds)
	if err != nil {
		return nil, err
	}

	// Get connection with override support
	_, ch, err := h.connManager.GetConnectionWithOptions(instanceID, creds, opts)
	if err != nil {
		return nil, err
	}

	// Declare queue (idempotent operation)
	q, err := ch.QueueDeclare(
		req.Queue, // name
		true,      // durable
		false,     // auto-delete
		false,     // exclusive
		false,     // no-wait
		nil,       // arguments
	)
	if err != nil {
		return nil, common.NewServiceError("rabbitmq", "E003", fmt.Sprintf("failed to declare queue: %v", err), true)
	}

	// Determine exchange (use default if empty)
	exchange := req.Exchange
	if exchange == "" {
		exchange = "" // Default exchange
	}

	// Determine routing key (use queue name if publishing to default exchange)
	routingKey := q.Name
	if exchange != "" {
		routingKey = req.Queue // Use original queue name for named exchanges
	}

	// Prepare message properties
	deliveryMode := amqp.Transient
	if req.Persistent {
		deliveryMode = amqp.Persistent
	}

	// Publish message
	err = ch.PublishWithContext(
		ctx,
		exchange,   // exchange
		routingKey, // routing key
		false,      // mandatory
		false,      // immediate
		amqp.Publishing{
			ContentType:  "text/plain",
			Body:         []byte(req.Message),
			Timestamp:    time.Now(),
			DeliveryMode: deliveryMode,
		},
	)
	if err != nil {
		return nil, common.NewServiceError("rabbitmq", "E004", fmt.Sprintf("failed to publish: %v", err), true)
	}

	return &PublishResult{
		Success:   true,
		Queue:     req.Queue,
		Exchange:  exchange,
		Message:   req.Message,
		Timestamp: time.Now().Unix(),
	}, nil
}

// HandleConsume consumes messages from RabbitMQ
func (h *Handler) HandleConsume(ctx context.Context, instanceID string, vaultCreds common.Credentials, req *ConsumeRequest) (*ConsumeResult, error) {
	// Create default connection options for backward compatibility
	opts := common.ConnectionOptions{
		UseAMQPS: req.UseAMQPS,
		Timeout:  30 * time.Second,
	}
	return h.HandleConsumeWithOptions(ctx, instanceID, vaultCreds, req, opts)
}

// HandleConsumeWithOptions consumes messages from RabbitMQ with connection options
func (h *Handler) HandleConsumeWithOptions(ctx context.Context, instanceID string, vaultCreds common.Credentials, req *ConsumeRequest, opts common.ConnectionOptions) (*ConsumeResult, error) {
	creds, err := NewCredentials(vaultCreds)
	if err != nil {
		return nil, err
	}

	// Get connection with override support
	_, ch, err := h.connManager.GetConnectionWithOptions(instanceID, creds, opts)
	if err != nil {
		return nil, err
	}

	// Ensure queue exists
	_, err = ch.QueueDeclare(
		req.Queue, // name
		true,      // durable
		false,     // auto-delete
		false,     // exclusive
		false,     // no-wait
		nil,       // arguments
	)
	if err != nil {
		return nil, common.NewServiceError("rabbitmq", "E005", fmt.Sprintf("failed to declare queue: %v", err), true)
	}

	// Start consuming
	msgs, err := ch.Consume(
		req.Queue, // queue
		"",        // consumer tag
		false,     // auto-ack
		false,     // exclusive
		false,     // no-local
		false,     // no-wait
		nil,       // args
	)
	if err != nil {
		return nil, common.NewServiceError("rabbitmq", "E006", fmt.Sprintf("failed to consume: %v", err), true)
	}

	// Collect messages with timeout
	var messages []Message
	maxCount := req.Count
	if maxCount <= 0 {
		maxCount = 10 // Default limit
	}

	timeout := time.Duration(req.Timeout) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second // Default timeout
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for i := 0; i < maxCount; i++ {
		select {
		case msg := <-msgs:
			messages = append(messages, Message{
				Body:       string(msg.Body),
				MessageID:  msg.MessageId,
				Timestamp:  msg.Timestamp.Unix(),
				Exchange:   msg.Exchange,
				RoutingKey: msg.RoutingKey,
			})

			if req.AutoAck {
				msg.Ack(false)
			} else {
				msg.Nack(false, true) // Requeue the message
			}

		case <-timeoutCtx.Done():
			// Timeout reached
			goto done

		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

done:
	return &ConsumeResult{
		Success:  true,
		Queue:    req.Queue,
		Messages: messages,
		Count:    len(messages),
	}, nil
}

// HandleQueueInfo retrieves information about queues
func (h *Handler) HandleQueueInfo(ctx context.Context, instanceID string, vaultCreds common.Credentials, req *QueueInfoRequest) (*QueueInfoResult, error) {
	// Create default connection options for backward compatibility
	opts := common.ConnectionOptions{
		UseAMQPS: req.UseAMQPS,
		Timeout:  30 * time.Second,
	}
	return h.HandleQueueInfoWithOptions(ctx, instanceID, vaultCreds, req, opts)
}

// HandleQueueInfoWithOptions retrieves information about queues with connection options
func (h *Handler) HandleQueueInfoWithOptions(ctx context.Context, instanceID string, vaultCreds common.Credentials, req *QueueInfoRequest, opts common.ConnectionOptions) (*QueueInfoResult, error) {
	creds, err := NewCredentials(vaultCreds)
	if err != nil {
		return nil, err
	}

	// Apply overrides to credentials for management API if provided
	if opts.OverrideUser != "" || opts.OverridePassword != "" || opts.OverrideVHost != "" {
		testCreds := *creds
		if opts.OverrideUser != "" {
			testCreds.Username = opts.OverrideUser
		}
		if opts.OverridePassword != "" {
			testCreds.Password = opts.OverridePassword
		}
		if opts.OverrideVHost != "" {
			testCreds.VHost = opts.OverrideVHost
		}
		creds = &testCreds
	}

	// Try to get queue info via Management API first
	if h.managementClient != nil {
		queues, err := h.managementClient.GetQueues(creds, opts.UseAMQPS)
		if err == nil {
			return &QueueInfoResult{
				Success: true,
				Queues:  queues,
			}, nil
		}
		h.logger("Failed to get queues via management API: %v", err)
	}

	// Fallback: we can't list all queues without management API
	// Return empty list
	return &QueueInfoResult{
		Success: true,
		Queues:  []Queue{},
	}, nil
}

// HandleQueueOps performs queue operations (create, delete, purge)
func (h *Handler) HandleQueueOps(ctx context.Context, instanceID string, vaultCreds common.Credentials, req *QueueOpsRequest) (*QueueOpsResult, error) {
	// Create default connection options for backward compatibility
	opts := common.ConnectionOptions{
		UseAMQPS: req.UseAMQPS,
		Timeout:  30 * time.Second,
	}
	return h.HandleQueueOpsWithOptions(ctx, instanceID, vaultCreds, req, opts)
}

// HandleQueueOpsWithOptions performs queue operations with connection options
func (h *Handler) HandleQueueOpsWithOptions(ctx context.Context, instanceID string, vaultCreds common.Credentials, req *QueueOpsRequest, opts common.ConnectionOptions) (*QueueOpsResult, error) {
	creds, err := NewCredentials(vaultCreds)
	if err != nil {
		return nil, err
	}

	// Get connection with override support
	_, ch, err := h.connManager.GetConnectionWithOptions(instanceID, creds, opts)
	if err != nil {
		return nil, err
	}

	switch req.Operation {
	case "create":
		_, err = ch.QueueDeclare(
			req.Queue,   // name
			req.Durable, // durable
			false,       // auto-delete
			false,       // exclusive
			false,       // no-wait
			nil,         // arguments
		)
		if err != nil {
			return nil, common.NewServiceError("rabbitmq", "E007", fmt.Sprintf("failed to create queue: %v", err), true)
		}

	case "delete":
		_, err = ch.QueueDelete(
			req.Queue, // name
			false,     // if-unused
			false,     // if-empty
			false,     // no-wait
		)
		if err != nil {
			return nil, common.NewServiceError("rabbitmq", "E008", fmt.Sprintf("failed to delete queue: %v", err), true)
		}

	case "purge":
		_, err = ch.QueuePurge(
			req.Queue, // name
			false,     // no-wait
		)
		if err != nil {
			return nil, common.NewServiceError("rabbitmq", "E009", fmt.Sprintf("failed to purge queue: %v", err), true)
		}

	default:
		return nil, common.NewServiceError("rabbitmq", "E010", fmt.Sprintf("unknown operation: %s", req.Operation), false)
	}

	return &QueueOpsResult{
		Success:   true,
		Queue:     req.Queue,
		Operation: req.Operation,
	}, nil
}

// HandleManagement handles management API requests
func (h *Handler) HandleManagement(ctx context.Context, instanceID string, vaultCreds common.Credentials, req *ManagementRequest) (*ManagementResult, error) {
	// Create default connection options for backward compatibility
	opts := common.ConnectionOptions{
		UseAMQPS: false, // Management API doesn't use AMQP
		Timeout:  30 * time.Second,
	}
	return h.HandleManagementWithOptions(ctx, instanceID, vaultCreds, req, opts)
}

// HandleManagementWithOptions handles management API requests with connection options
func (h *Handler) HandleManagementWithOptions(ctx context.Context, instanceID string, vaultCreds common.Credentials, req *ManagementRequest, opts common.ConnectionOptions) (*ManagementResult, error) {
	creds, err := NewCredentials(vaultCreds)
	if err != nil {
		return nil, err
	}

	// Apply overrides to credentials for management API if provided
	if opts.OverrideUser != "" || opts.OverridePassword != "" || opts.OverrideVHost != "" {
		testCreds := *creds
		if opts.OverrideUser != "" {
			// For management API, set both regular and management-specific credentials
			testCreds.Username = opts.OverrideUser
			testCreds.ManagementUsername = opts.OverrideUser
		}
		if opts.OverridePassword != "" {
			// For management API, set both regular and management-specific credentials
			testCreds.Password = opts.OverridePassword
			testCreds.ManagementPassword = opts.OverridePassword
		}
		if opts.OverrideVHost != "" {
			testCreds.VHost = opts.OverrideVHost
		}
		creds = &testCreds
	}

	if h.managementClient == nil {
		return nil, common.NewServiceError("rabbitmq", "E011", "management client not available", false)
	}

	statusCode, data, err := h.managementClient.Request(creds, req.Method, req.Path, req.UseSSL)
	if err != nil {
		return nil, common.NewServiceError("rabbitmq", "E012", fmt.Sprintf("management API request failed: %v", err), true)
	}

	return &ManagementResult{
		Success:    statusCode >= 200 && statusCode < 300,
		StatusCode: statusCode,
		Data:       data,
	}, nil
}
