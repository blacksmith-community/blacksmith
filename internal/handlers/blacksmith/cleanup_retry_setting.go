package blacksmith

import (
	"context"
	"net/http"
	"time"

	"blacksmith/internal/settings"
	"blacksmith/pkg/http/response"
)

// cleanupRetryRefusal is the answer to a cleanup_retry_interval that is
// not a duration of at least a minute.
const cleanupRetryRefusal = "cleanup_retry_interval must be a duration of at least 1m, such as '15m' or '1h'"

// cleanupRetryChoice is the interval between retries of owed cleanups in
// effect, from the Vault settings, the environment, the configuration, or
// the default.
func (h *Handler) cleanupRetryChoice(ctx context.Context) settings.CleanupRetryChoice {
	var configured *time.Duration

	if h.config != nil {
		configured = h.config.GetBrokerConfig().CleanupRetryInterval
	}

	var store settings.Store
	if h.vault != nil {
		store = h.vault
	}

	return settings.CleanupRetryInterval(ctx, store, configured)
}

// saveCleanupRetryInterval saves the interval between retries of owed
// cleanups in the Vault settings and records the change. It answers the
// request itself and returns false when the value is refused or the save
// fails. The sweep reads the interval before each wait, so it needs no
// restart.
func (h *Handler) saveCleanupRetryInterval(ctx context.Context, responseWriter http.ResponseWriter, raw string, result map[string]interface{}) bool {
	logger := h.logger.Named("blacksmith-settings")

	interval, err := settings.ParseCleanupRetryInterval(raw)
	if err != nil {
		responseWriter.WriteHeader(http.StatusBadRequest)
		response.HandleJSON(responseWriter, map[string]interface{}{errorKey: cleanupRetryRefusal}, nil)

		return false
	}

	old := h.cleanupRetryChoice(ctx).Interval.String()

	err = settings.SaveCleanupRetryInterval(ctx, h.vault, raw)
	if err != nil {
		logger.Error("Failed to save the cleanup retry interval: %v", err)
		responseWriter.WriteHeader(http.StatusInternalServerError)
		response.HandleJSON(responseWriter, map[string]interface{}{
			errorKey: "failed to save the cleanup retry interval: " + err.Error(),
		}, nil)

		return false
	}

	logger.Info("Cleanup retry interval updated to %v", interval)
	h.recordSetting(ctx, settingCleanupRetryInterval, old, interval.String())
	result[settingCleanupRetryInterval] = interval.String()

	return true
}
