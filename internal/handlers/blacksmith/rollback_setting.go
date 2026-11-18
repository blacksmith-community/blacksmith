package blacksmith

import (
	"context"
	"net/http"
	"strconv"

	"blacksmith/internal/settings"
	"blacksmith/pkg/http/response"
)

// rollbackChoice is whether the broker rolls back a failed update, from
// the Vault settings, the environment, the configuration, or the default.
// Nothing reads it to roll back yet; the Settings page shows it.
func (h *Handler) rollbackChoice(ctx context.Context) settings.RollbackChoice {
	var configured *bool

	if h.config != nil {
		configured = h.config.GetBrokerConfig().UpdateRollback
	}

	var store settings.Store
	if h.vault != nil {
		store = h.vault
	}

	choice := settings.UpdateRollback(ctx, store, configured)

	for _, refusal := range choice.Refused {
		h.logger.Named("blacksmith-settings").Warn("Skipped the update_rollback value from %s: %v", refusal.Source, refusal.Err)
	}

	return choice
}

// saveUpdateRollback saves the rollback setting in the Vault settings and
// records the change. It answers the request itself and returns false when
// the save fails.
func (h *Handler) saveUpdateRollback(ctx context.Context, responseWriter http.ResponseWriter, enabled bool, result map[string]interface{}) bool {
	logger := h.logger.Named("blacksmith-settings")

	old := strconv.FormatBool(h.rollbackChoice(ctx).On)

	err := settings.SaveUpdateRollback(ctx, h.vault, enabled)
	if err != nil {
		logger.Error("Failed to save update_rollback: %v", err)
		responseWriter.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(responseWriter, map[string]interface{}{
			errorKey: "failed to save update_rollback: " + err.Error(),
		}, nil)

		return false
	}

	logger.Info("update_rollback set to %t", enabled)
	h.recordSetting(ctx, settingUpdateRollback, old, strconv.FormatBool(enabled))
	result[settingUpdateRollback] = enabled

	return true
}
