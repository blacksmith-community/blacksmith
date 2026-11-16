package blacksmith

import (
	"context"
	"net/http"
	"time"

	"blacksmith/internal/settings"
	"blacksmith/pkg/http/response"
)

// errorKey is the key of the message in a refused save's answer.
const errorKey = "error"

// errandChoice is the errand wait in effect, from the Vault settings, the
// environment, the configuration, or the default.
func (h *Handler) errandChoice(ctx context.Context) settings.ErrandChoice {
	var configured *time.Duration

	if h.config != nil {
		configured = h.config.GetBrokerConfig().ErrandTimeout
	}

	var store settings.Store
	if h.vault != nil {
		store = h.vault
	}

	return settings.ErrandWait(ctx, store, configured)
}

// saveErrandTimeout saves the errand wait in the Vault settings and
// records the change. It answers the request itself and returns false when
// the value is refused or the save fails.
func (h *Handler) saveErrandTimeout(ctx context.Context, responseWriter http.ResponseWriter, raw string, result map[string]interface{}) bool {
	logger := h.logger.Named("blacksmith-settings")

	wait, err := settings.ParseErrandTimeout(raw)
	if err != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		response.HandleJSON(responseWriter, map[string]interface{}{
			errorKey: "errand_timeout must be a duration above zero, such as '45m' or '1h30m'",
		}, nil)

		return false
	}

	if h.vault == nil {
		responseWriter.WriteHeader(http.StatusServiceUnavailable)
		response.HandleJSON(responseWriter, map[string]interface{}{errorKey: "vault is not available"}, nil)

		return false
	}

	old := h.errandChoice(ctx).Wait.String()

	err = settings.SaveErrandTimeout(ctx, h.vault, raw)
	if err != nil {
		logger.Error("Failed to save the errand timeout: %v", err)
		responseWriter.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(responseWriter, map[string]interface{}{
			errorKey: "failed to save the errand timeout: " + err.Error(),
		}, nil)

		return false
	}

	logger.Info("Errand timeout updated to %v", wait)
	h.recordSetting(ctx, settingErrandTimeout, old, wait.String())
	result[settingErrandTimeout] = wait.String()

	return true
}
