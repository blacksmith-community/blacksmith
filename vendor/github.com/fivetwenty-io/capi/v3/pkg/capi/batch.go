package capi

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// BatchOperation represents a single operation in a batch
type BatchOperation struct {
	ID       string
	Type     string // "create", "update", "delete", "get"
	Resource string // "app", "space", "org", etc.
	Data     interface{}
	Callback func(result *BatchResult)
}

// BatchResult represents the result of a batch operation
type BatchResult struct {
	ID       string
	Success  bool
	Data     interface{}
	Error    error
	Duration time.Duration
}

// BatchExecutor executes batch operations
type BatchExecutor struct {
	client      Client
	concurrency int
	timeout     time.Duration
}

// NewBatchExecutor creates a new batch executor
func NewBatchExecutor(client Client, concurrency int) *BatchExecutor {
	if concurrency <= 0 {
		concurrency = 5
	}
	return &BatchExecutor{
		client:      client,
		concurrency: concurrency,
		timeout:     30 * time.Second,
	}
}

// SetTimeout sets the timeout for batch operations
func (b *BatchExecutor) SetTimeout(timeout time.Duration) {
	b.timeout = timeout
}

// Execute runs a batch of operations
func (b *BatchExecutor) Execute(ctx context.Context, operations []BatchOperation) ([]BatchResult, error) {
	results := make([]BatchResult, len(operations))
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, b.concurrency)

	for i, op := range operations {
		wg.Add(1)
		go func(index int, operation BatchOperation) {
			defer wg.Done()

			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Execute operation with timeout
			opCtx, cancel := context.WithTimeout(ctx, b.timeout)
			defer cancel()

			start := time.Now()
			result := b.executeOperation(opCtx, operation)
			result.Duration = time.Since(start)
			results[index] = *result

			// Call callback if provided
			if operation.Callback != nil {
				operation.Callback(result)
			}
		}(i, op)
	}

	wg.Wait()
	return results, nil
}

// executeOperation executes a single operation
func (b *BatchExecutor) executeOperation(ctx context.Context, op BatchOperation) *BatchResult {
	result := &BatchResult{
		ID: op.ID,
	}

	switch op.Resource {
	case "app":
		result = b.executeAppOperation(ctx, op)
	case "space":
		result = b.executeSpaceOperation(ctx, op)
	case "organization":
		result = b.executeOrgOperation(ctx, op)
	case "route":
		result = b.executeRouteOperation(ctx, op)
	case "service_instance":
		result = b.executeServiceInstanceOperation(ctx, op)
	default:
		result.Success = false
		result.Error = fmt.Errorf("unsupported resource type: %s", op.Resource)
	}

	return result
}

// executeAppOperation handles app operations
func (b *BatchExecutor) executeAppOperation(ctx context.Context, op BatchOperation) *BatchResult {
	result := &BatchResult{ID: op.ID}

	switch op.Type {
	case "create":
		if req, ok := op.Data.(*AppCreateRequest); ok {
			app, err := b.client.Apps().Create(ctx, req)
			result.Success = err == nil
			result.Data = app
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for app create")
		}
	case "update":
		type updateData struct {
			GUID    string
			Request *AppUpdateRequest
		}
		if data, ok := op.Data.(*updateData); ok {
			app, err := b.client.Apps().Update(ctx, data.GUID, data.Request)
			result.Success = err == nil
			result.Data = app
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for app update")
		}
	case "delete":
		if guid, ok := op.Data.(string); ok {
			err := b.client.Apps().Delete(ctx, guid)
			result.Success = err == nil
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for app delete")
		}
	case "get":
		if guid, ok := op.Data.(string); ok {
			app, err := b.client.Apps().Get(ctx, guid)
			result.Success = err == nil
			result.Data = app
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for app get")
		}
	default:
		result.Error = fmt.Errorf("unsupported operation type: %s", op.Type)
	}

	return result
}

// executeSpaceOperation handles space operations
func (b *BatchExecutor) executeSpaceOperation(ctx context.Context, op BatchOperation) *BatchResult {
	result := &BatchResult{ID: op.ID}

	switch op.Type {
	case "create":
		if req, ok := op.Data.(*SpaceCreateRequest); ok {
			space, err := b.client.Spaces().Create(ctx, req)
			result.Success = err == nil
			result.Data = space
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for space create")
		}
	case "update":
		type updateData struct {
			GUID    string
			Request *SpaceUpdateRequest
		}
		if data, ok := op.Data.(*updateData); ok {
			space, err := b.client.Spaces().Update(ctx, data.GUID, data.Request)
			result.Success = err == nil
			result.Data = space
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for space update")
		}
	case "delete":
		if guid, ok := op.Data.(string); ok {
			job, err := b.client.Spaces().Delete(ctx, guid)
			result.Success = err == nil
			result.Data = job
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for space delete")
		}
	case "get":
		if guid, ok := op.Data.(string); ok {
			space, err := b.client.Spaces().Get(ctx, guid)
			result.Success = err == nil
			result.Data = space
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for space get")
		}
	default:
		result.Error = fmt.Errorf("unsupported operation type: %s", op.Type)
	}

	return result
}

// executeOrgOperation handles organization operations
func (b *BatchExecutor) executeOrgOperation(ctx context.Context, op BatchOperation) *BatchResult {
	result := &BatchResult{ID: op.ID}

	switch op.Type {
	case "create":
		if req, ok := op.Data.(*OrganizationCreateRequest); ok {
			org, err := b.client.Organizations().Create(ctx, req)
			result.Success = err == nil
			result.Data = org
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for org create")
		}
	case "update":
		type updateData struct {
			GUID    string
			Request *OrganizationUpdateRequest
		}
		if data, ok := op.Data.(*updateData); ok {
			org, err := b.client.Organizations().Update(ctx, data.GUID, data.Request)
			result.Success = err == nil
			result.Data = org
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for org update")
		}
	case "delete":
		if guid, ok := op.Data.(string); ok {
			job, err := b.client.Organizations().Delete(ctx, guid)
			result.Success = err == nil
			result.Data = job
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for org delete")
		}
	case "get":
		if guid, ok := op.Data.(string); ok {
			org, err := b.client.Organizations().Get(ctx, guid)
			result.Success = err == nil
			result.Data = org
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for org get")
		}
	default:
		result.Error = fmt.Errorf("unsupported operation type: %s", op.Type)
	}

	return result
}

// executeRouteOperation handles route operations
func (b *BatchExecutor) executeRouteOperation(ctx context.Context, op BatchOperation) *BatchResult {
	result := &BatchResult{ID: op.ID}

	switch op.Type {
	case "create":
		if req, ok := op.Data.(*RouteCreateRequest); ok {
			route, err := b.client.Routes().Create(ctx, req)
			result.Success = err == nil
			result.Data = route
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for route create")
		}
	case "update":
		type updateData struct {
			GUID    string
			Request *RouteUpdateRequest
		}
		if data, ok := op.Data.(*updateData); ok {
			route, err := b.client.Routes().Update(ctx, data.GUID, data.Request)
			result.Success = err == nil
			result.Data = route
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for route update")
		}
	case "delete":
		if guid, ok := op.Data.(string); ok {
			job, err := b.client.Routes().Delete(ctx, guid)
			result.Success = err == nil
			result.Data = job
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for route delete")
		}
	case "get":
		if guid, ok := op.Data.(string); ok {
			route, err := b.client.Routes().Get(ctx, guid)
			result.Success = err == nil
			result.Data = route
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for route get")
		}
	default:
		result.Error = fmt.Errorf("unsupported operation type: %s", op.Type)
	}

	return result
}

// executeServiceInstanceOperation handles service instance operations
func (b *BatchExecutor) executeServiceInstanceOperation(ctx context.Context, op BatchOperation) *BatchResult {
	result := &BatchResult{ID: op.ID}

	switch op.Type {
	case "create":
		if req, ok := op.Data.(*ServiceInstanceCreateRequest); ok {
			// Service instances can return either a job or instance
			resp, err := b.client.ServiceInstances().Create(ctx, req)
			result.Success = err == nil
			result.Data = resp
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for service instance create")
		}
	case "update":
		type updateData struct {
			GUID    string
			Request *ServiceInstanceUpdateRequest
		}
		if data, ok := op.Data.(*updateData); ok {
			resp, err := b.client.ServiceInstances().Update(ctx, data.GUID, data.Request)
			result.Success = err == nil
			result.Data = resp
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for service instance update")
		}
	case "delete":
		if guid, ok := op.Data.(string); ok {
			resp, err := b.client.ServiceInstances().Delete(ctx, guid)
			result.Success = err == nil
			result.Data = resp
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for service instance delete")
		}
	case "get":
		if guid, ok := op.Data.(string); ok {
			instance, err := b.client.ServiceInstances().Get(ctx, guid)
			result.Success = err == nil
			result.Data = instance
			result.Error = err
		} else {
			result.Error = fmt.Errorf("invalid data type for service instance get")
		}
	default:
		result.Error = fmt.Errorf("unsupported operation type: %s", op.Type)
	}

	return result
}

// BatchBuilder helps build batch operations
type BatchBuilder struct {
	operations []BatchOperation
}

// NewBatchBuilder creates a new batch builder
func NewBatchBuilder() *BatchBuilder {
	return &BatchBuilder{
		operations: make([]BatchOperation, 0),
	}
}

// AddCreateApp adds an app creation operation
func (b *BatchBuilder) AddCreateApp(id string, request *AppCreateRequest) *BatchBuilder {
	b.operations = append(b.operations, BatchOperation{
		ID:       id,
		Type:     "create",
		Resource: "app",
		Data:     request,
	})
	return b
}

// AddUpdateApp adds an app update operation
func (b *BatchBuilder) AddUpdateApp(id, guid string, request *AppUpdateRequest) *BatchBuilder {
	b.operations = append(b.operations, BatchOperation{
		ID:       id,
		Type:     "update",
		Resource: "app",
		Data: &struct {
			GUID    string
			Request *AppUpdateRequest
		}{
			GUID:    guid,
			Request: request,
		},
	})
	return b
}

// AddDeleteApp adds an app deletion operation
func (b *BatchBuilder) AddDeleteApp(id, guid string) *BatchBuilder {
	b.operations = append(b.operations, BatchOperation{
		ID:       id,
		Type:     "delete",
		Resource: "app",
		Data:     guid,
	})
	return b
}

// AddGetApp adds an app get operation
func (b *BatchBuilder) AddGetApp(id, guid string) *BatchBuilder {
	b.operations = append(b.operations, BatchOperation{
		ID:       id,
		Type:     "get",
		Resource: "app",
		Data:     guid,
	})
	return b
}

// AddCreateSpace adds a space creation operation
func (b *BatchBuilder) AddCreateSpace(id string, request *SpaceCreateRequest) *BatchBuilder {
	b.operations = append(b.operations, BatchOperation{
		ID:       id,
		Type:     "create",
		Resource: "space",
		Data:     request,
	})
	return b
}

// AddUpdateSpace adds a space update operation
func (b *BatchBuilder) AddUpdateSpace(id, guid string, request *SpaceUpdateRequest) *BatchBuilder {
	b.operations = append(b.operations, BatchOperation{
		ID:       id,
		Type:     "update",
		Resource: "space",
		Data: &struct {
			GUID    string
			Request *SpaceUpdateRequest
		}{
			GUID:    guid,
			Request: request,
		},
	})
	return b
}

// AddDeleteSpace adds a space deletion operation
func (b *BatchBuilder) AddDeleteSpace(id, guid string) *BatchBuilder {
	b.operations = append(b.operations, BatchOperation{
		ID:       id,
		Type:     "delete",
		Resource: "space",
		Data:     guid,
	})
	return b
}

// AddCreateOrganization adds an organization creation operation
func (b *BatchBuilder) AddCreateOrganization(id string, request *OrganizationCreateRequest) *BatchBuilder {
	b.operations = append(b.operations, BatchOperation{
		ID:       id,
		Type:     "create",
		Resource: "organization",
		Data:     request,
	})
	return b
}

// AddOperation adds a custom operation
func (b *BatchBuilder) AddOperation(op BatchOperation) *BatchBuilder {
	b.operations = append(b.operations, op)
	return b
}

// Build returns the built operations
func (b *BatchBuilder) Build() []BatchOperation {
	return b.operations
}

// BatchTransaction represents a transactional batch of operations
type BatchTransaction struct {
	operations []BatchOperation
	results    []BatchResult
	executor   *BatchExecutor
	rollback   bool
}

// NewBatchTransaction creates a new batch transaction
func NewBatchTransaction(executor *BatchExecutor) *BatchTransaction {
	return &BatchTransaction{
		executor:   executor,
		operations: make([]BatchOperation, 0),
		rollback:   true,
	}
}

// Add adds an operation to the transaction
func (t *BatchTransaction) Add(op BatchOperation) *BatchTransaction {
	t.operations = append(t.operations, op)
	return t
}

// SetRollback sets whether to rollback on failure
func (t *BatchTransaction) SetRollback(rollback bool) *BatchTransaction {
	t.rollback = rollback
	return t
}

// Execute executes the transaction
func (t *BatchTransaction) Execute(ctx context.Context) ([]BatchResult, error) {
	results, err := t.executor.Execute(ctx, t.operations)
	t.results = results

	// Check for failures
	var failedOps []string
	for _, result := range results {
		if !result.Success {
			failedOps = append(failedOps, result.ID)
		}
	}

	// If there were failures and rollback is enabled
	if len(failedOps) > 0 && t.rollback {
		// Attempt to rollback successful operations
		t.performRollback(ctx)
		return results, fmt.Errorf("transaction failed, %d operations failed: %v", len(failedOps), failedOps)
	}

	return results, err
}

// performRollback attempts to rollback successful operations
func (t *BatchTransaction) performRollback(ctx context.Context) {
	// This is a simplified rollback - in practice, this would need to be more sophisticated
	var rollbackOps []BatchOperation

	for i, result := range t.results {
		if result.Success {
			original := t.operations[i]
			// Create inverse operation
			switch original.Type {
			case "create":
				// Delete what was created
				if original.Resource == "app" || original.Resource == "space" ||
					original.Resource == "organization" || original.Resource == "route" {
					// Extract GUID from result data if possible
					// This would need proper type assertions based on resource type
					rollbackOps = append(rollbackOps, BatchOperation{
						ID:       fmt.Sprintf("rollback_%s", original.ID),
						Type:     "delete",
						Resource: original.Resource,
						Data:     result.Data, // This would need proper GUID extraction
					})
				}
			case "delete":
				// Can't easily recreate deleted resources
				// Log this for manual intervention
			case "update":
				// Would need to store original state to rollback updates
				// Log this for manual intervention
			}
		}
	}

	// Execute rollback operations if any
	if len(rollbackOps) > 0 {
		_, _ = t.executor.Execute(ctx, rollbackOps)
	}
}
