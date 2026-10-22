// Package apierr carries an HTTP status and a machine-readable code with an
// error, so a handler can answer every failure with the same JSON body.
package apierr

import (
	"errors"
	"net/http"

	"blacksmith/pkg/http/response"
)

// Codes returned in the "code" field of an API error body.
const (
	// 400.
	CodeNoSelections     = "no_selections"
	CodeInvalidMode      = "invalid_mode"
	CodeUnknownVariable  = "unknown_variable"
	CodeNotRotatable     = "not_rotatable"
	CodeCandidateInvalid = "candidate_invalid"
	CodeApprovalMismatch = "approval_mismatch"
	CodeInvalidSettings  = "invalid_settings"
	CodeInvalidFilter    = "invalid_filter"

	// 403.
	CodeRotationDisabled     = "rotation_disabled"
	CodePatchDisabled        = "patch_disabled"
	CodeCredHubNotConfigured = "credhub_not_configured"
	CodeDirectorUnsupported  = "director_unsupported"
	CodeInventoryDisabled    = "inventory_disabled"

	// 404.
	CodeInstanceNotFound = "instance_not_found"
	CodeNotFound         = "not_found"

	// 409.
	CodeInstanceBusy           = "instance_busy"
	CodeDeploymentLocked       = "deployment_locked"
	CodeSigningCAChanged       = "signing_ca_changed"
	CodeNoHealthGate           = "no_health_gate"
	CodeVMsNotRunning          = "vms_not_running"
	CodePatchUnsupported       = "patch_unsupported"
	CodeUpgradePreviewRequired = "upgrade_preview_required"
	CodeRequestIDConflict      = "request_id_conflict"
	CodeNotAwaitingDecision    = "not_awaiting_decision"
	CodeInvalidTaskState       = "invalid_task_state"

	// 410.
	CodePreviewExpired = "preview_expired"

	// 500.
	CodeInternal = "internal_error"

	// 503.
	CodeTasksLoading       = "tasks_loading"
	CodeCredHubUnavailable = "credhub_unavailable"
)

// Error is an error that knows the HTTP status and code it should be
// answered with.
type Error struct {
	Status  int
	Code    string
	Message string
	Err     error
}

// New returns an Error with the given status, code, and message.
func New(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// Wrap returns an Error that answers with the message of err and unwraps to it.
func Wrap(status int, code string, err error) *Error {
	return &Error{Status: status, Code: code, Message: err.Error(), Err: err}
}

// Error returns the message.
func (e *Error) Error() string { return e.Message }

// Unwrap returns the wrapped error, if any.
func (e *Error) Unwrap() error { return e.Err }

// Write answers with the status, code, and message of the *Error that err
// holds. Any other error is answered with fallbackStatus and the code
// internal_error.
func Write(w http.ResponseWriter, err error, fallbackStatus int) {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		response.WriteErrorCode(w, apiErr.Status, apiErr.Code, apiErr.Message)

		return
	}

	response.WriteErrorCode(w, fallbackStatus, CodeInternal, err.Error())
}
