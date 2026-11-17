// Package instancegate refuses a request for an instance the broker's
// service index does not hold as live, so no route builds a Vault path from
// an ID the broker doesn't own.
package instancegate

import (
	"errors"
	"net/http"

	"blacksmith/internal/apierr"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/ownership"
	"blacksmith/pkg/http/response"
)

// Permit reports false, after it answers, when the instance ID doesn't name
// a live entry of the service index. An ID that can't name an instance, an
// unknown ID, and a deprovisioned one all answer 404 with instance_not_found.
// An index that can't be read, or no Vault, answers 503 with
// vault_unavailable.
func Permit(writer http.ResponseWriter, req *http.Request, logger interfaces.Logger, vault interfaces.Vault, instanceID string) bool {
	if vault == nil {
		logger.Error("Vault not configured")
		response.WriteErrorCode(writer, http.StatusServiceUnavailable, apierr.CodeVaultUnavailable,
			"Blacksmith can't check the instance because Vault is not configured")

		return false
	}

	_, err := ownership.LiveInstance(req.Context(), vault, instanceID)

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
