package capi

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/fivetwenty-io/capi/v3/internal/constants"
)

// Static errors for err113 compliance.
var (
	ErrUnsupportedResourceType        = errors.New("unsupported resource type")
	ErrUnsupportedOperationType       = errors.New("unsupported operation type")
	ErrInvalidDataTypeApp             = errors.New("invalid data type for app operation")
	ErrInvalidDataTypeSpace           = errors.New("invalid data type for space operation")
	ErrInvalidDataTypeOrg             = errors.New("invalid data type for org operation")
	ErrInvalidDataTypeRoute           = errors.New("invalid data type for route operation")
	ErrInvalidDataTypeServiceInstance = errors.New("invalid data type for service instance operation")
	ErrTransactionFailed              = errors.New("transaction failed")
	ErrRollbackIncomplete             = errors.New("rollback incomplete")
)

// Batch operation types (BatchOperation.Type). These select which CRUD
// method a batch entry invokes; distinct from the include/fields query
// parameter values declared in query_options.go.
const (
	batchOpCreate = "create"
	batchOpUpdate = "update"
	batchOpDelete = "delete"
)

// Batch resource types (BatchOperation.Resource). These identify which
// resource kind a batch entry targets; distinct from the include/fields
// query parameter values declared in query_options.go (e.g.
// ServiceCredentialBindingIncludeApp, RoleIncludeSpace), which select
// related resources to embed in a response rather than a CRUD target.
const (
	batchResourceApp   = "app"
	batchResourceSpace = "space"
)

// guidFromResult extracts the GUID of a created resource from a BatchResult's
// Data. Every CF resource type embeds Resource, whose promoted GUID field is
// found by reflection regardless of the concrete type, so rollback does not
// need a type switch over every resource. Returns false when Data is nil, not
// a struct pointer, or carries no non-empty GUID.
func guidFromResult(data any) (string, bool) {
	if data == nil {
		return "", false
	}

	value := reflect.ValueOf(data)
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return "", false
		}

		value = value.Elem()
	}

	if value.Kind() != reflect.Struct {
		return "", false
	}

	field := value.FieldByName("GUID")
	if !field.IsValid() || field.Kind() != reflect.String {
		return "", false
	}

	guid := field.String()

	return guid, guid != ""
}

// UpdateDataWrapper wraps update data with GUID for consistent handling.
type UpdateDataWrapper[T any] struct {
	GUID    string
	Request *T
}

// handleCrudOperation is a helper that handles common CRUD pattern.
func handleCrudOperation(
	operation BatchOperation,
	createFunc func() (any, error),
	updateFunc func() (any, error),
	deleteFunc func() (any, error),
	getFunc func() (any, error),
) *BatchResult {
	result := &BatchResult{ID: operation.ID}

	switch operation.Type {
	case batchOpCreate:
		data, err := createFunc()
		result.Success = err == nil
		result.Data = data
		result.Error = err
	case batchOpUpdate:
		data, err := updateFunc()
		result.Success = err == nil
		result.Data = data
		result.Error = err
	case batchOpDelete:
		data, err := deleteFunc()
		result.Success = err == nil
		result.Data = data
		result.Error = err
	case "get":
		data, err := getFunc()
		result.Success = err == nil
		result.Data = data
		result.Error = err
	default:
		result.Error = fmt.Errorf("%w: %s", ErrUnsupportedOperationType, operation.Type)
	}

	return result
}

// CRUDOperationConfig holds configuration for CRUD operations.
type CRUDOperationConfig struct {
	InvalidDataTypeErr error
	CreateFunc         func(ctx context.Context, operation BatchOperation) (any, error)
	UpdateFunc         func(ctx context.Context, operation BatchOperation) (any, error)
	DeleteFunc         func(ctx context.Context, operation BatchOperation) (any, error)
	GetFunc            func(ctx context.Context, operation BatchOperation) (any, error)
}

// ResourceClientOps defines the operations available for a resource client.
type ResourceClientOps[TCreateRequest, TUpdateRequest, TResponse any] interface {
	Create(ctx context.Context, request *TCreateRequest) (*TResponse, error)
	Update(ctx context.Context, guid string, request *TUpdateRequest) (*TResponse, error)
	Delete(ctx context.Context, guid string) (*Job, error)
	Get(ctx context.Context, guid string) (*TResponse, error)
}

// createCRUDOperationConfig creates a generic CRUD operation configuration.
func createCRUDOperationConfig[TCreateRequest, TUpdateRequest, TResponse any](
	invalidDataTypeErr error,
	client ResourceClientOps[TCreateRequest, TUpdateRequest, TResponse],
) CRUDOperationConfig {
	return CRUDOperationConfig{
		InvalidDataTypeErr: invalidDataTypeErr,
		CreateFunc: func(ctx context.Context, operation BatchOperation) (any, error) {
			if req, ok := operation.Data.(*TCreateRequest); ok {
				return client.Create(ctx, req)
			}

			return nil, fmt.Errorf("%w create", invalidDataTypeErr)
		},
		UpdateFunc: func(ctx context.Context, operation BatchOperation) (any, error) {
			if data, ok := operation.Data.(*UpdateDataWrapper[TUpdateRequest]); ok {
				return client.Update(ctx, data.GUID, data.Request)
			}

			return nil, fmt.Errorf("%w update", invalidDataTypeErr)
		},
		DeleteFunc: func(ctx context.Context, operation BatchOperation) (any, error) {
			if guid, ok := operation.Data.(string); ok {
				return client.Delete(ctx, guid)
			}

			return nil, fmt.Errorf("%w delete", invalidDataTypeErr)
		},
		GetFunc: func(ctx context.Context, operation BatchOperation) (any, error) {
			if guid, ok := operation.Data.(string); ok {
				return client.Get(ctx, guid)
			}

			return nil, fmt.Errorf("%w get", invalidDataTypeErr)
		},
	}
}

// BatchOperation represents a single operation in a batch.
type BatchOperation struct {
	ID       string
	Type     string // "create", "update", "delete", "get"
	Resource string // "app", "space", "org", etc.
	Data     any
	Callback func(result *BatchResult)
}

// BatchResult represents the result of a batch operation.
type BatchResult struct {
	ID       string
	Success  bool
	Data     any
	Error    error
	Duration time.Duration
}

// BatchExecutor executes batch operations.
type BatchExecutor struct {
	client      Client
	concurrency int
	timeout     time.Duration
}

// NewBatchExecutor creates a new batch executor.
func NewBatchExecutor(client Client, concurrency int) *BatchExecutor {
	if concurrency <= 0 {
		concurrency = 5
	}

	return &BatchExecutor{
		client:      client,
		concurrency: concurrency,
		timeout:     constants.DefaultHTTPTimeout,
	}
}

// SetTimeout sets the timeout for batch operations.
func (b *BatchExecutor) SetTimeout(timeout time.Duration) {
	b.timeout = timeout
}

// Execute runs a batch of operations.
func (b *BatchExecutor) Execute(ctx context.Context, operations []BatchOperation) ([]BatchResult, error) {
	results := make([]BatchResult, len(operations))

	var waitGroup sync.WaitGroup

	semaphore := make(chan struct{}, b.concurrency)

	for index, operation := range operations {
		waitGroup.Add(1)

		go func(index int, operation BatchOperation) {
			defer waitGroup.Done()

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
		}(index, operation)
	}

	waitGroup.Wait()

	return results, nil
}

// executeGenericCrudOperation handles generic CRUD operations using the provided configuration.
func (b *BatchExecutor) executeGenericCrudOperation(ctx context.Context, operation BatchOperation, config CRUDOperationConfig) *BatchResult {
	return handleCrudOperation(operation,
		func() (any, error) { return config.CreateFunc(ctx, operation) },
		func() (any, error) { return config.UpdateFunc(ctx, operation) },
		func() (any, error) { return config.DeleteFunc(ctx, operation) },
		func() (any, error) { return config.GetFunc(ctx, operation) },
	)
}

// createSpaceOperationConfig creates CRUD operation configuration for spaces.
func (b *BatchExecutor) createSpaceOperationConfig() CRUDOperationConfig {
	return CRUDOperationConfig{
		InvalidDataTypeErr: ErrInvalidDataTypeSpace,
		CreateFunc: func(ctx context.Context, operation BatchOperation) (any, error) {
			if req, ok := operation.Data.(*SpaceCreateRequest); ok {
				return b.client.Spaces().Create(ctx, req)
			}

			return nil, fmt.Errorf("%w create", ErrInvalidDataTypeSpace)
		},
		UpdateFunc: func(ctx context.Context, operation BatchOperation) (any, error) {
			if data, ok := operation.Data.(*UpdateDataWrapper[SpaceUpdateRequest]); ok {
				return b.client.Spaces().Update(ctx, data.GUID, data.Request)
			}

			return nil, fmt.Errorf("%w update", ErrInvalidDataTypeSpace)
		},
		DeleteFunc: func(ctx context.Context, operation BatchOperation) (any, error) {
			if guid, ok := operation.Data.(string); ok {
				return b.client.Spaces().Delete(ctx, guid)
			}

			return nil, fmt.Errorf("%w delete", ErrInvalidDataTypeSpace)
		},
		GetFunc: func(ctx context.Context, operation BatchOperation) (any, error) {
			if guid, ok := operation.Data.(string); ok {
				return b.client.Spaces().Get(ctx, guid)
			}

			return nil, fmt.Errorf("%w get", ErrInvalidDataTypeSpace)
		},
	}
}

// createOrgOperationConfig creates CRUD operation configuration for organizations.
func (b *BatchExecutor) createOrgOperationConfig() CRUDOperationConfig {
	return createCRUDOperationConfig(ErrInvalidDataTypeOrg, b.client.Organizations())
}

// createRouteOperationConfig creates CRUD operation configuration for routes.
func (b *BatchExecutor) createRouteOperationConfig() CRUDOperationConfig {
	return CRUDOperationConfig{
		InvalidDataTypeErr: ErrInvalidDataTypeRoute,
		CreateFunc: func(ctx context.Context, operation BatchOperation) (any, error) {
			if req, ok := operation.Data.(*RouteCreateRequest); ok {
				return b.client.Routes().Create(ctx, req)
			}

			return nil, fmt.Errorf("%w create", ErrInvalidDataTypeRoute)
		},
		UpdateFunc: func(ctx context.Context, operation BatchOperation) (any, error) {
			if data, ok := operation.Data.(*UpdateDataWrapper[RouteUpdateRequest]); ok {
				return b.client.Routes().Update(ctx, data.GUID, data.Request)
			}

			return nil, fmt.Errorf("%w update", ErrInvalidDataTypeRoute)
		},
		DeleteFunc: func(ctx context.Context, operation BatchOperation) (any, error) {
			if guid, ok := operation.Data.(string); ok {
				return b.client.Routes().Delete(ctx, guid)
			}

			return nil, fmt.Errorf("%w delete", ErrInvalidDataTypeRoute)
		},
		GetFunc: func(ctx context.Context, operation BatchOperation) (any, error) {
			if guid, ok := operation.Data.(string); ok {
				return b.client.Routes().Get(ctx, guid)
			}

			return nil, fmt.Errorf("%w get", ErrInvalidDataTypeRoute)
		},
	}
}

// executeOperation executes a single operation.
func (b *BatchExecutor) executeOperation(ctx context.Context, operation BatchOperation) *BatchResult {
	result := &BatchResult{
		ID: operation.ID,
	}

	switch operation.Resource {
	case batchResourceApp:
		result = b.executeAppOperation(ctx, operation)
	case batchResourceSpace:
		result = b.executeSpaceOperation(ctx, operation)
	case "organization":
		result = b.executeOrgOperation(ctx, operation)
	case "route":
		result = b.executeRouteOperation(ctx, operation)
	case "service_instance":
		result = b.executeServiceInstanceOperation(ctx, operation)
	default:
		result.Success = false
		result.Error = fmt.Errorf("%w: %s", ErrUnsupportedResourceType, operation.Resource)
	}

	return result
}

// executeAppOperation handles app operations using the common CRUD helper.
func (b *BatchExecutor) executeAppOperation(ctx context.Context, operation BatchOperation) *BatchResult {
	return handleCrudOperation(operation,
		func() (any, error) {
			if req, ok := operation.Data.(*AppCreateRequest); ok {
				return b.client.Apps().Create(ctx, req)
			}

			return nil, fmt.Errorf("%w create", ErrInvalidDataTypeApp)
		},
		func() (any, error) {
			if data, ok := operation.Data.(*UpdateDataWrapper[AppUpdateRequest]); ok {
				return b.client.Apps().Update(ctx, data.GUID, data.Request)
			}

			return nil, fmt.Errorf("%w update", ErrInvalidDataTypeApp)
		},
		func() (any, error) {
			if guid, ok := operation.Data.(string); ok {
				return b.client.Apps().Delete(ctx, guid)
			}

			return nil, fmt.Errorf("%w delete", ErrInvalidDataTypeApp)
		},
		func() (any, error) {
			if guid, ok := operation.Data.(string); ok {
				return b.client.Apps().Get(ctx, guid)
			}

			return nil, fmt.Errorf("%w get", ErrInvalidDataTypeApp)
		},
	)
}

// executeSpaceOperation handles space operations using the common CRUD helper.
func (b *BatchExecutor) executeSpaceOperation(ctx context.Context, operation BatchOperation) *BatchResult {
	config := b.createSpaceOperationConfig()

	return b.executeGenericCrudOperation(ctx, operation, config)
}

// executeOrgOperation handles organization operations using the common CRUD helper.
func (b *BatchExecutor) executeOrgOperation(ctx context.Context, operation BatchOperation) *BatchResult {
	config := b.createOrgOperationConfig()

	return b.executeGenericCrudOperation(ctx, operation, config)
}

// executeRouteOperation handles route operations using the common CRUD helper.
func (b *BatchExecutor) executeRouteOperation(ctx context.Context, operation BatchOperation) *BatchResult {
	config := b.createRouteOperationConfig()

	return b.executeGenericCrudOperation(ctx, operation, config)
}

// executeServiceInstanceOperation handles service instance operations with special return handling.
func (b *BatchExecutor) executeServiceInstanceOperation(ctx context.Context, operation BatchOperation) *BatchResult {
	return handleCrudOperation(operation,
		func() (any, error) {
			if req, ok := operation.Data.(*ServiceInstanceCreateRequest); ok {
				return b.client.ServiceInstances().Create(ctx, req)
			}

			return nil, fmt.Errorf("%w create", ErrInvalidDataTypeServiceInstance)
		},
		func() (any, error) {
			if data, ok := operation.Data.(*UpdateDataWrapper[ServiceInstanceUpdateRequest]); ok {
				return b.client.ServiceInstances().Update(ctx, data.GUID, data.Request)
			}

			return nil, fmt.Errorf("%w update", ErrInvalidDataTypeServiceInstance)
		},
		func() (any, error) {
			if guid, ok := operation.Data.(string); ok {
				return b.client.ServiceInstances().Delete(ctx, guid)
			}

			return nil, fmt.Errorf("%w delete", ErrInvalidDataTypeServiceInstance)
		},
		func() (any, error) {
			if guid, ok := operation.Data.(string); ok {
				return b.client.ServiceInstances().Get(ctx, guid)
			}

			return nil, fmt.Errorf("%w get", ErrInvalidDataTypeServiceInstance)
		},
	)
}

// BatchBuilder helps build batch operations.
type BatchBuilder struct {
	operations []BatchOperation
}

// NewBatchBuilder creates a new batch builder.
func NewBatchBuilder() *BatchBuilder {
	return &BatchBuilder{
		operations: make([]BatchOperation, 0),
	}
}

// AddCreateApp adds an app creation operation.
func (b *BatchBuilder) AddCreateApp(id string, request *AppCreateRequest) *BatchBuilder {
	b.operations = append(b.operations, BatchOperation{
		ID:       id,
		Type:     batchOpCreate,
		Resource: batchResourceApp,
		Data:     request,
	})

	return b
}

// AddUpdateApp adds an app update operation.
func (b *BatchBuilder) AddUpdateApp(id, guid string, request *AppUpdateRequest) *BatchBuilder {
	b.operations = append(b.operations, BatchOperation{
		ID:       id,
		Type:     batchOpUpdate,
		Resource: batchResourceApp,
		Data: &UpdateDataWrapper[AppUpdateRequest]{
			GUID:    guid,
			Request: request,
		},
	})

	return b
}

// AddDeleteApp adds an app deletion operation.
func (b *BatchBuilder) AddDeleteApp(id, guid string) *BatchBuilder {
	b.operations = append(b.operations, BatchOperation{
		ID:       id,
		Type:     batchOpDelete,
		Resource: batchResourceApp,
		Data:     guid,
	})

	return b
}

// AddGetApp adds an app get operation.
func (b *BatchBuilder) AddGetApp(id, guid string) *BatchBuilder {
	b.operations = append(b.operations, BatchOperation{
		ID:       id,
		Type:     "get",
		Resource: batchResourceApp,
		Data:     guid,
	})

	return b
}

// AddCreateSpace adds a space creation operation.
func (b *BatchBuilder) AddCreateSpace(id string, request *SpaceCreateRequest) *BatchBuilder {
	b.operations = append(b.operations, BatchOperation{
		ID:       id,
		Type:     batchOpCreate,
		Resource: batchResourceSpace,
		Data:     request,
	})

	return b
}

// AddUpdateSpace adds a space update operation.
func (b *BatchBuilder) AddUpdateSpace(id, guid string, request *SpaceUpdateRequest) *BatchBuilder {
	b.operations = append(b.operations, BatchOperation{
		ID:       id,
		Type:     batchOpUpdate,
		Resource: batchResourceSpace,
		Data: &UpdateDataWrapper[SpaceUpdateRequest]{
			GUID:    guid,
			Request: request,
		},
	})

	return b
}

// AddDeleteSpace adds a space deletion operation.
func (b *BatchBuilder) AddDeleteSpace(id, guid string) *BatchBuilder {
	b.operations = append(b.operations, BatchOperation{
		ID:       id,
		Type:     batchOpDelete,
		Resource: batchResourceSpace,
		Data:     guid,
	})

	return b
}

// AddCreateOrganization adds an organization creation operation.
func (b *BatchBuilder) AddCreateOrganization(id string, request *OrganizationCreateRequest) *BatchBuilder {
	b.operations = append(b.operations, BatchOperation{
		ID:       id,
		Type:     batchOpCreate,
		Resource: "organization",
		Data:     request,
	})

	return b
}

// AddOperation adds a custom operation.
func (b *BatchBuilder) AddOperation(operation BatchOperation) *BatchBuilder {
	b.operations = append(b.operations, operation)

	return b
}

// Build returns the built operations.
func (b *BatchBuilder) Build() []BatchOperation {
	return b.operations
}

// BatchTransaction represents a transactional batch of operations.
type BatchTransaction struct {
	operations []BatchOperation
	results    []BatchResult
	executor   *BatchExecutor
	rollback   bool
}

// NewBatchTransaction creates a new batch transaction.
//
// Rollback is disabled by default: it deletes resources created by the
// transaction's successful operations, which is destructive and cannot
// recreate resources removed or mutated by "delete"/"update" operations.
// Opt in with SetRollback(true) when every operation is a "create" and
// automatic cleanup of partial work is desired.
func NewBatchTransaction(executor *BatchExecutor) *BatchTransaction {
	return &BatchTransaction{
		executor:   executor,
		operations: make([]BatchOperation, 0),
		rollback:   false,
	}
}

// Add adds an operation to the transaction.
func (t *BatchTransaction) Add(operation BatchOperation) *BatchTransaction {
	t.operations = append(t.operations, operation)

	return t
}

// SetRollback sets whether to rollback on failure.
func (t *BatchTransaction) SetRollback(rollback bool) *BatchTransaction {
	t.rollback = rollback

	return t
}

// Execute executes the transaction.
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
		failed := fmt.Errorf("%w, %d operations failed: %v", ErrTransactionFailed, len(failedOps), failedOps)

		// Attempt to undo the successful operations.
		rollbackErr := t.performRollback(ctx)
		if rollbackErr != nil {
			return results, fmt.Errorf("%w; %w", failed, rollbackErr)
		}

		return results, failed
	}

	return results, err
}

// performRollback undoes the successful operations of a failed transaction by
// deleting the resources they created, in reverse order so dependent
// resources are removed before their dependencies. Only "create" operations
// have a well-defined inverse: "delete" and "update" operations cannot be
// reversed automatically (the prior state is not retained) and are reported
// for manual intervention. It returns ErrRollbackIncomplete describing any
// operation that could not be reversed and any delete that failed, or nil when
// every successful operation was undone.
func (t *BatchTransaction) performRollback(ctx context.Context) error {
	var (
		rollbackOps  []BatchOperation
		irreversible []string
	)

	for index := len(t.results) - 1; index >= 0; index-- {
		if !t.results[index].Success {
			continue
		}

		original := t.operations[index]

		switch original.Type {
		case batchOpCreate:
			guid, ok := guidFromResult(t.results[index].Data)
			if !ok {
				irreversible = append(irreversible,
					fmt.Sprintf("%s (created %s, no GUID in result)", original.ID, original.Resource))

				continue
			}

			rollbackOps = append(rollbackOps, BatchOperation{
				ID:       "rollback_" + original.ID,
				Type:     batchOpDelete,
				Resource: original.Resource,
				Data:     guid,
			})
		case batchOpDelete, batchOpUpdate:
			irreversible = append(irreversible,
				fmt.Sprintf("%s (%s %s cannot be auto-reversed)", original.ID, original.Type, original.Resource))
		}
	}

	var rollbackFailures []string

	if len(rollbackOps) > 0 {
		rollbackResults, _ := t.executor.Execute(ctx, rollbackOps)
		for _, result := range rollbackResults {
			if !result.Success {
				rollbackFailures = append(rollbackFailures, fmt.Sprintf("%s: %v", result.ID, result.Error))
			}
		}
	}

	if len(irreversible) == 0 && len(rollbackFailures) == 0 {
		return nil
	}

	return fmt.Errorf("%w: irreversible operations %v; failed deletes %v",
		ErrRollbackIncomplete, irreversible, rollbackFailures)
}
