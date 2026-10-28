package apierr_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"blacksmith/internal/apierr"
)

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()

	var body map[string]interface{}

	err := json.Unmarshal(rec.Body.Bytes(), &body)
	if err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}

	return body
}

func TestApierrWriteFallsBackToInternal(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	apierr.Write(rec, errors.New("boom"), http.StatusInternalServerError)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	if body := decode(t, rec); body["code"] != "internal_error" || body["error"] != "boom" {
		t.Fatalf("unexpected body: %v", body)
	}

	rec = httptest.NewRecorder()
	apierr.Write(rec, fmt.Errorf("x: %w", apierr.New(http.StatusGone, "preview_expired", "gone")), http.StatusInternalServerError)

	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", rec.Code)
	}

	if body := decode(t, rec); body["code"] != "preview_expired" || body["error"] != "gone" {
		t.Fatalf("unexpected body: %v", body)
	}
}

func TestWrapUnwraps(t *testing.T) {
	t.Parallel()

	inner := errors.New("inner")
	err := apierr.Wrap(http.StatusConflict, apierr.CodeInstanceBusy, inner)

	if !errors.Is(err, inner) {
		t.Fatal("Wrap does not unwrap to the inner error")
	}

	if err.Error() != "inner" {
		t.Fatalf("Error() = %q", err.Error())
	}
}

func TestCodesMatchSpec(t *testing.T) {
	t.Parallel()

	table := []struct{ got, want string }{
		{apierr.CodeInvalidRequest, "invalid_request"},
		{apierr.CodeNoSelections, "no_selections"},
		{apierr.CodeInvalidMode, "invalid_mode"},
		{apierr.CodeUnknownVariable, "unknown_variable"},
		{apierr.CodeNotRotatable, "not_rotatable"},
		{apierr.CodeCandidateInvalid, "candidate_invalid"},
		{apierr.CodeApprovalMismatch, "approval_mismatch"},
		{apierr.CodeInvalidSettings, "invalid_settings"},
		{apierr.CodeInvalidFilter, "invalid_filter"},
		{apierr.CodeRotationDisabled, "rotation_disabled"},
		{apierr.CodePatchDisabled, "patch_disabled"},
		{apierr.CodeCredHubNotConfigured, "credhub_not_configured"},
		{apierr.CodeDirectorUnsupported, "director_unsupported"},
		{apierr.CodeInventoryDisabled, "inventory_disabled"},
		{apierr.CodeInstanceNotFound, "instance_not_found"},
		{apierr.CodeNotFound, "not_found"},
		{apierr.CodeInstanceBusy, "instance_busy"},
		{apierr.CodeDeploymentLocked, "deployment_locked"},
		{apierr.CodeSigningCAChanged, "signing_ca_changed"},
		{apierr.CodeNoHealthGate, "no_health_gate"},
		{apierr.CodeVMsNotRunning, "vms_not_running"},
		{apierr.CodePatchUnsupported, "patch_unsupported"},
		{apierr.CodeUpgradePreviewRequired, "upgrade_preview_required"},
		{apierr.CodeRequestIDConflict, "request_id_conflict"},
		{apierr.CodeNotAwaitingDecision, "not_awaiting_decision"},
		{apierr.CodeInvalidTaskState, "invalid_task_state"},
		{apierr.CodePreviewExpired, "preview_expired"},
		{apierr.CodeInternal, "internal_error"},
		{apierr.CodeTasksLoading, "tasks_loading"},
		{apierr.CodeCredHubUnavailable, "credhub_unavailable"},
		{apierr.CodeVaultUnavailable, "vault_unavailable"},
		{apierr.CodeCleanupNotStarted, "cleanup_not_started"},
		{apierr.CodeCommandsDisabled, "commands_disabled"},
		{apierr.CodeRootDisabled, "root_disabled"},
		{apierr.CodeCommandCredentialRequired, "command_credential_required"},
		{apierr.CodePreviewMismatch, "preview_mismatch"},
		{apierr.CodePreviewBound, "preview_bound"},
		{apierr.CodeCommandTooLong, "command_too_long"},
		{apierr.CodeInvalidRunUser, "invalid_run_user"},
		{apierr.CodeTimeoutOutOfRange, "timeout_out_of_range"},
		{apierr.CodeMaxInFlightTooHigh, "max_in_flight_too_high"},
	}

	if len(table) != 41 {
		t.Fatalf("table has %d codes, want 41", len(table))
	}

	for _, row := range table {
		if row.got != row.want {
			t.Errorf("constant holds %q, want %q", row.got, row.want)
		}
	}
}
