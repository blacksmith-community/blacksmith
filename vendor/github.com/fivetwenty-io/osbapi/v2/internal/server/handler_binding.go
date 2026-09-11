package server

import (
	"encoding/json"
	"errors"
	"net/http"

	osbapi "github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// bindHandler handles PUT /v2/service_instances/{instance_id}/service_bindings/{binding_id}.
func (h *handler) bindHandler(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("instance_id")
	bindingID := r.PathValue("binding_id")

	var req osbapi.BindRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, &osbapi.OSBError{
			Description: "invalid request body: " + err.Error(),
		})
		return
	}

	async := r.URL.Query().Get("accepts_incomplete") == "true"

	resp, isAsync, err := h.broker.Bind(r.Context(), instanceID, bindingID, req, async)
	if err != nil {
		if errors.Is(err, osbapi.ErrBindingAlreadyExists) {
			writeJSON(w, http.StatusOK, resp)
			return
		}
		h.handleError(w, err)
		return
	}

	status := http.StatusCreated
	if isAsync {
		status = http.StatusAccepted
	}
	writeJSON(w, status, resp)
}

// fetchBindingHandler handles GET /v2/service_instances/{instance_id}/service_bindings/{binding_id}.
func (h *handler) fetchBindingHandler(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("instance_id")
	bindingID := r.PathValue("binding_id")

	req := osbapi.FetchBindingRequest{
		ServiceID: r.URL.Query().Get("service_id"),
		PlanID:    r.URL.Query().Get("plan_id"),
	}

	resp, err := h.broker.GetBinding(r.Context(), instanceID, bindingID, req)
	if err != nil {
		h.handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// unbindHandler handles DELETE /v2/service_instances/{instance_id}/service_bindings/{binding_id}.
func (h *handler) unbindHandler(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("instance_id")
	bindingID := r.PathValue("binding_id")

	req := osbapi.UnbindRequest{
		ServiceID: r.URL.Query().Get("service_id"),
		PlanID:    r.URL.Query().Get("plan_id"),
	}

	async := r.URL.Query().Get("accepts_incomplete") == "true"

	resp, isAsync, err := h.broker.Unbind(r.Context(), instanceID, bindingID, req, async)
	if err != nil {
		h.handleError(w, err)
		return
	}

	status := http.StatusOK
	if isAsync {
		status = http.StatusAccepted
	}
	writeJSON(w, status, resp)
}

// bindingLastOperationHandler handles GET /v2/service_instances/{instance_id}/service_bindings/{binding_id}/last_operation.
func (h *handler) bindingLastOperationHandler(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("instance_id")
	bindingID := r.PathValue("binding_id")

	req := osbapi.LastOperationRequest{
		ServiceID: r.URL.Query().Get("service_id"),
		PlanID:    r.URL.Query().Get("plan_id"),
		Operation: r.URL.Query().Get("operation"),
	}

	resp, err := h.broker.LastBindingOperation(r.Context(), instanceID, bindingID, req)
	if err != nil {
		h.handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
