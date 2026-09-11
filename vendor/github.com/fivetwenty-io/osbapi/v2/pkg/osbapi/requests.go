package osbapi

// DeprovisionRequest holds query parameters for deprovision operations.
type DeprovisionRequest struct {
	ServiceID string `json:"service_id"`
	PlanID    string `json:"plan_id"`
}

// FetchInstanceRequest holds query parameters for fetching an instance.
type FetchInstanceRequest struct {
	ServiceID string `json:"service_id,omitempty"`
	PlanID    string `json:"plan_id,omitempty"`
}

// LastOperationRequest holds query parameters for polling last operation.
type LastOperationRequest struct {
	ServiceID string `json:"service_id,omitempty"`
	PlanID    string `json:"plan_id,omitempty"`
	Operation string `json:"operation,omitempty"`
}

// LastOperationResponse represents the last operation polling response.
type LastOperationResponse struct {
	State       OperationState `json:"state"`
	Description string         `json:"description,omitempty"`
}

// OperationState represents the state of an asynchronous operation.
type OperationState string

const (
	// StateInProgress indicates the operation is still in progress.
	StateInProgress OperationState = "in progress"
	// StateSucceeded indicates the operation completed successfully.
	StateSucceeded OperationState = "succeeded"
	// StateFailed indicates the operation failed.
	StateFailed OperationState = "failed"
)
