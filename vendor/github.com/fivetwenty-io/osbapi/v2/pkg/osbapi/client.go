package osbapi

import "context"

// Client defines the interface for platforms to interact with an
// Open Service Broker API v2.17 compliant broker.
//
// Methods mirror the OSB API endpoints. Methods that support async operations
// take an `async` parameter and return a bool indicating whether the operation
// was accepted asynchronously (202).
type Client interface {
	// GetCatalog retrieves the service broker's catalog.
	GetCatalog(ctx context.Context) (*Catalog, error)

	// Provision requests provisioning of a new service instance.
	// Returns (response, isAsync, error).
	Provision(ctx context.Context, instanceID string, req ProvisionRequest, async bool) (ProvisionResponse, bool, error)

	// Deprovision requests deletion of a service instance.
	// Returns (response, isAsync, error).
	Deprovision(ctx context.Context, instanceID string, req DeprovisionRequest, async bool) (DeprovisionResponse, bool, error)

	// GetInstance retrieves a service instance.
	GetInstance(ctx context.Context, instanceID string, req FetchInstanceRequest) (FetchInstanceResponse, error)

	// Update requests modification of a service instance.
	// Returns (response, isAsync, error).
	Update(ctx context.Context, instanceID string, req UpdateRequest, async bool) (UpdateResponse, bool, error)

	// PollLastOperation polls the status of an async instance operation.
	PollLastOperation(ctx context.Context, instanceID string, req LastOperationRequest) (LastOperationResponse, error)

	// Bind requests creation of a new service binding.
	// Returns (response, isAsync, error).
	Bind(ctx context.Context, instanceID, bindingID string, req BindRequest, async bool) (BindResponse, bool, error)

	// Unbind requests deletion of a service binding.
	// Returns (response, isAsync, error).
	Unbind(ctx context.Context, instanceID, bindingID string, req UnbindRequest, async bool) (UnbindResponse, bool, error)

	// GetBinding retrieves a service binding.
	GetBinding(ctx context.Context, instanceID, bindingID string, req FetchBindingRequest) (FetchBindingResponse, error)

	// PollBindingLastOperation polls the status of an async binding operation.
	PollBindingLastOperation(ctx context.Context, instanceID, bindingID string, req LastOperationRequest) (LastOperationResponse, error)

	// PollInstanceUntilComplete polls a last operation endpoint until the
	// operation succeeds, fails, or the timeout is reached.
	PollInstanceUntilComplete(ctx context.Context, instanceID string, opts PollConfig) (LastOperationResponse, error)

	// PollBindingUntilComplete polls a binding last operation endpoint until
	// the operation succeeds, fails, or the timeout is reached.
	PollBindingUntilComplete(ctx context.Context, instanceID, bindingID string, opts PollConfig) (LastOperationResponse, error)
}
