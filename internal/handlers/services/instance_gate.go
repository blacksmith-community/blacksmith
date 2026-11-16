package services

import (
	"errors"
	"net/http"

	"blacksmith/internal/apierr"
	"blacksmith/internal/ownership"
	"blacksmith/pkg/http/response"
)

// permitInstance reports false, after it answers, when the instance ID
// doesn't name a live entry of the service index. An ID that can't name an
// instance, an unknown ID, and a deprovisioned one all answer 404, and an
// index that can't be read answers 503, so no route builds a Vault path
// from an ID the broker doesn't own.
func (h *Handler) permitInstance(writer http.ResponseWriter, req *http.Request, instanceID string) bool {
	logger := h.logger.Named("instance-gate")

	if h.vault == nil {
		logger.Error("Vault not configured")
		response.WriteErrorCode(writer, http.StatusServiceUnavailable, apierr.CodeVaultUnavailable,
			"Blacksmith can't check the instance because Vault is not configured")

		return false
	}

	_, err := ownership.LiveInstance(req.Context(), h.vault, instanceID)

	switch {
	case err == nil:
		return true
	case errors.Is(err, ownership.ErrInvalidID), errors.Is(err, ownership.ErrNotLive):
		response.WriteErrorCode(writer, http.StatusNotFound, apierr.CodeInstanceNotFound,
			"Blacksmith has no service instance with this ID")
	default:
		logger.Error("Failed to read the service index: %v", err)
		response.WriteErrorCode(writer, http.StatusServiceUnavailable, apierr.CodeVaultUnavailable,
			"Blacksmith can't check the instance because Vault did not answer")
	}

	return false
}
