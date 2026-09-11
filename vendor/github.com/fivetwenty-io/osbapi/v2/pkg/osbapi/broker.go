package osbapi

import "context"

// ServiceBroker defines the interface that broker authors must implement to
// handle Open Service Broker API v2.17 requests.
//
// Each method corresponds to an OSB API endpoint. Methods that support async
// operations take an `async` parameter indicating the platform accepts
// deferred responses. These methods return a bool indicating whether the
// operation is actually async (true = 202 Accepted, false = 200/201 OK).
type ServiceBroker interface {
	// GetCatalog returns the service catalog.
	GetCatalog(ctx context.Context) (*Catalog, error)

	// Provision creates a new service instance.
	// Returns (response, isAsync, error). If isAsync is true, the server returns 202.
	Provision(ctx context.Context, instanceID string, req ProvisionRequest, async bool) (ProvisionResponse, bool, error)

	// Deprovision deletes a service instance.
	// Returns (response, isAsync, error). If isAsync is true, the server returns 202.
	Deprovision(ctx context.Context, instanceID string, req DeprovisionRequest, async bool) (DeprovisionResponse, bool, error)

	// GetInstance fetches a service instance.
	GetInstance(ctx context.Context, instanceID string, req FetchInstanceRequest) (FetchInstanceResponse, error)

	// Update modifies an existing service instance.
	// Returns (response, isAsync, error). If isAsync is true, the server returns 202.
	Update(ctx context.Context, instanceID string, req UpdateRequest, async bool) (UpdateResponse, bool, error)

	// LastOperation polls the status of an async instance operation.
	LastOperation(ctx context.Context, instanceID string, req LastOperationRequest) (LastOperationResponse, error)

	// Bind creates a new service binding.
	// Returns (response, isAsync, error). If isAsync is true, the server returns 202.
	Bind(ctx context.Context, instanceID, bindingID string, req BindRequest, async bool) (BindResponse, bool, error)

	// Unbind deletes a service binding.
	// Returns (response, isAsync, error). If isAsync is true, the server returns 202.
	Unbind(ctx context.Context, instanceID, bindingID string, req UnbindRequest, async bool) (UnbindResponse, bool, error)

	// GetBinding fetches a service binding.
	GetBinding(ctx context.Context, instanceID, bindingID string, req FetchBindingRequest) (FetchBindingResponse, error)

	// LastBindingOperation polls the status of an async binding operation.
	LastBindingOperation(ctx context.Context, instanceID, bindingID string, req LastOperationRequest) (LastOperationResponse, error)
}
