package certificates_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/apierr"
	"blacksmith/internal/handlers/certificates"
	"blacksmith/internal/upgrade"
	"blacksmith/pkg/logger"
)

// fakePreviewer records the preview request it got and answers with the
// preview or the error it holds.
type fakePreviewer struct {
	got     upgrade.PreviewRequest
	preview *upgrade.Preview
	err     error
}

func (f *fakePreviewer) PreviewRotation(_ context.Context, req upgrade.PreviewRequest) (*upgrade.Preview, error) {
	f.got = req

	return f.preview, f.err
}

func doPreview(t *testing.T, svc *fakeInventory, previewer certificates.RotationPreviewer, body string) *httptest.ResponseRecorder {
	t.Helper()

	h := certificates.NewInventoryHandler(svc, previewer, logger.NewNoOpLogger())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/b/certificates/rotation/preview", strings.NewReader(body))
	h.ServeHTTP(rec, req)

	return rec
}

func TestPreviewRouteAnswersThePreview(t *testing.T) {
	t.Parallel()

	previewer := &fakePreviewer{preview: &upgrade.Preview{
		ID:        "pv-5d0c",
		Instances: []upgrade.PreviewInstance{{InstanceID: "7a1b", Selected: []upgrade.PreviewVariable{}}},
		Warnings:  []string{},
	}}

	body := `{"mode":"patch","converge":"pending","selections":[{"instance_id":"7a1b","variables":["/ws-bosh/redis-7a1b/redis_crt"]}]}`

	rec := doPreview(t, &fakeInventory{enabled: true}, previewer, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got := previewer.got
	if got.Mode != upgrade.RotationModePatch || got.Converge != upgrade.ConvergePending || len(got.Selections) != 1 ||
		got.Selections[0].InstanceID != "7a1b" || len(got.Selections[0].Variables) != 1 {
		t.Fatalf("the request was decoded wrongly: %+v", got)
	}

	var answer struct {
		PreviewID string            `json:"preview_id"`
		Instances []json.RawMessage `json:"instances"`
	}

	err := json.Unmarshal(rec.Body.Bytes(), &answer)
	if err != nil || answer.PreviewID != "pv-5d0c" || len(answer.Instances) != 1 {
		t.Fatalf("unexpected body %s: %v", rec.Body.String(), err)
	}
}

func TestPreviewRouteMapsErrors(t *testing.T) {
	t.Parallel()

	selection := `{"mode":"redeploy","selections":[{"instance_id":"7a1b","variables":["x"]}]}`

	tests := []struct {
		name      string
		enabled   bool
		previewer certificates.RotationPreviewer
		body      string
		status    int
		code      string
	}{
		{
			name: "a refusal from the manager", enabled: true, body: selection,
			previewer: &fakePreviewer{err: apierr.New(http.StatusForbidden, apierr.CodeRotationDisabled, "off")},
			status:    http.StatusForbidden, code: apierr.CodeRotationDisabled,
		},
		{
			name: "a body that is not JSON", enabled: true, body: "{", previewer: &fakePreviewer{},
			status: http.StatusBadRequest, code: apierr.CodeNoSelections,
		},
		{
			name: "the inventory off", enabled: false, body: selection, previewer: &fakePreviewer{},
			status: http.StatusForbidden, code: apierr.CodeInventoryDisabled,
		},
		{
			name: "no previewer", enabled: true, body: selection,
			status: http.StatusInternalServerError, code: apierr.CodeInternal,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := doPreview(t, &fakeInventory{enabled: tc.enabled}, tc.previewer, tc.body)
			if rec.Code != tc.status || errCode(t, rec) != tc.code {
				t.Fatalf("expected %d %s, got %d %s", tc.status, tc.code, rec.Code, rec.Body.String())
			}
		})
	}
}
