package blacksmith

import (
	"context"
	"net/http"
	"strconv"

	"blacksmith/internal/apierr"
	"blacksmith/internal/audit"
	"blacksmith/pkg/http/response"
)

const (
	// settingsTarget names the broker's own settings in a change record.
	settingsTarget = "broker"

	settingVaultMaxVersions   = "vault_max_versions"
	settingReconcilerInterval = "reconciler_interval"
	settingErrandTimeout      = "errand_timeout"
	settingUpdateRollback     = "update_rollback"

	// defaultChangeList is how many changes a page shows unless it asks
	// for another number.
	defaultChangeList = 10
)

// settingBefore reads the mount's max_versions before a save changes it,
// so the record can say what it was. Without a store it reads nothing.
func (h *Handler) settingBefore(ctx context.Context) string {
	if h.changes == nil {
		return ""
	}

	versions, err := h.vault.GetKVMaxVersions(ctx)
	if err != nil {
		h.logger.Named("blacksmith-settings").Error("Could not read vault max_versions before the change, so its record says the old value is unknown: %v", err)

		return audit.Unknown
	}

	return strconv.Itoa(versions)
}

// recordSetting keeps a record of a broker setting a save changed. The
// store writes it in the background, so the save's answer never waits on
// it.
func (h *Handler) recordSetting(ctx context.Context, key, old, current string) {
	if old == current {
		return
	}

	h.changes.Record(ctx, audit.ChangeRecord{
		Scope: audit.ScopeSettings, Target: settingsTarget, Change: audit.ChangeUpdate,
		Key: key, Old: old, New: current,
	})
}

// HandleChanges answers GET /b/blacksmith/changes with the newest change
// records of one scope: settings, endpoints, or instances. It takes
// ?scope= (settings unless given) and ?limit= (10 unless given, at most
// 100). A secret's values are never in the answer.
func (h *Handler) HandleChanges(writer http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		response.HandleJSON(writer, nil, ErrMethodNotAllowed)

		return
	}

	query := req.URL.Query()

	scope := query.Get("scope")
	if scope == "" {
		scope = audit.ScopeSettings
	}

	if scope != audit.ScopeSettings && scope != audit.ScopeEndpoints && scope != audit.ScopeInstances {
		response.WriteErrorCode(writer, http.StatusBadRequest, apierr.CodeInvalidRequest, "The scope must be settings, endpoints, or instances.")

		return
	}

	limit := defaultChangeList

	if text := query.Get("limit"); text != "" {
		parsed, err := strconv.Atoi(text)
		if err != nil || parsed < 1 || parsed > audit.MaxChangeList {
			response.WriteErrorCode(writer, http.StatusBadRequest, apierr.CodeInvalidRequest, "The limit must be a whole number from 1 to "+strconv.Itoa(audit.MaxChangeList)+".")

			return
		}

		limit = parsed
	}

	if h.changes == nil {
		response.WriteErrorCode(writer, http.StatusServiceUnavailable, apierr.CodeVaultUnavailable, "The broker has no Vault to keep change records in.")

		return
	}

	records, err := h.changes.List(req.Context(), scope, limit)
	if err != nil {
		h.logger.Named("blacksmith-changes").Error("Could not read the %s change records from Vault: %v", scope, err)
		response.WriteErrorCode(writer, http.StatusServiceUnavailable, apierr.CodeVaultUnavailable, "The change records could not be read from Vault.")

		return
	}

	response.JSON(writer, http.StatusOK, map[string]interface{}{"changes": records})
}
