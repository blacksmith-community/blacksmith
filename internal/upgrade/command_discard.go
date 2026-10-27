package upgrade

import (
	"context"
	"net/http"

	"blacksmith/internal/apierr"
)

// maxDiscardIDBytes bounds the IDs of a discard. A UUID with a prefix is far
// shorter, and the bound keeps the revoked set from growing with huge IDs.
const maxDiscardIDBytes = 128

// maxClientRequestIDBytes bounds the client_request_id of a command create to
// the same length.
const maxClientRequestIDBytes = maxDiscardIDBytes

// CommandDiscardRequest is the body of a request that discards a pending
// command create.
type CommandDiscardRequest struct {
	ClientRequestID string `json:"client_request_id"`
	PreviewID       string `json:"preview_id"`
}

// CommandDiscardResult says whether the discard stopped the create. When a
// task already exists for the request, Discarded is false and TaskID names it.
type CommandDiscardResult struct {
	Discarded bool   `json:"discarded"`
	TaskID    string `json:"task_id,omitempty"`
}

// DiscardCommandRequest stops a command create that may still be on its way.
// A create cannot succeed without its preview, so a discard revokes the
// preview and a late create fails at the store. It takes the lock a create
// with the same request ID holds, so a create that is under way finishes
// first and the discard then finds its task. It never starts a run and
// writes no audit record, because it makes no task.
func (m *Manager) DiscardCommandRequest(_ context.Context, req CommandDiscardRequest) (CommandDiscardResult, error) {
	select {
	case <-m.loaded:
	default:
		return CommandDiscardResult{}, ErrTasksLoading
	}

	if req.ClientRequestID == "" || req.PreviewID == "" {
		return CommandDiscardResult{}, apierr.New(http.StatusBadRequest, apierr.CodeInvalidRequest,
			"A discard needs a client_request_id and a preview_id.")
	}

	if len(req.ClientRequestID) > maxDiscardIDBytes || len(req.PreviewID) > maxDiscardIDBytes {
		return CommandDiscardResult{}, apierr.New(http.StatusBadRequest, apierr.CodeInvalidRequest,
			"The client_request_id or the preview_id is too long.")
	}

	unlock := m.lockRequest(req.ClientRequestID)
	defer unlock()

	if taskID, found := m.taskForRequest(req.ClientRequestID); found {
		m.logger.Info("Discard of command request %s with preview %s found task %s", req.ClientRequestID, req.PreviewID, taskID)

		return CommandDiscardResult{TaskID: taskID}, nil
	}

	if store := m.certDeps().Previews; store != nil {
		store.Revoke(req.PreviewID)
	}

	m.logger.Info("Discard of command request %s with preview %s revoked the preview", req.ClientRequestID, req.PreviewID)

	return CommandDiscardResult{Discarded: true}, nil
}

// taskForRequest returns the ID of a task, of any kind and status, that the
// client request ID made. A create whose save was never confirmed is failed
// in memory and is not in the request index, so the tasks are scanned too.
func (m *Manager) taskForRequest(requestID string) (string, bool) {
	if taskID, _, ok := m.requests.Lookup(requestID); ok {
		return taskID, true
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	for id, task := range m.tasks {
		if task.ClientRequestID == requestID {
			return id, true
		}
	}

	return "", false
}
