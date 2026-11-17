package services

import (
	"net/http"

	"blacksmith/internal/handlers/instancegate"
)

// permitInstance reports false, after it answers, when the instance ID
// doesn't name a live entry of the service index. See instancegate.Permit
// for the answers.
func (h *Handler) permitInstance(writer http.ResponseWriter, req *http.Request, instanceID string) bool {
	return instancegate.Permit(writer, req, h.logger.Named("instance-gate"), h.vault, instanceID)
}
