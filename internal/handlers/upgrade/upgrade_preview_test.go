//nolint:testpackage // the tests build a Handler around a fake task manager
package upgrade

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/upgrade"
	"blacksmith/pkg/logger"
)

const (
	previewedInstance = "7a1b"
	secondInstance    = "9c2d"
)

// previewTasks is a taskManager that answers an upgrade preview with
// preview or err, and records the request it was given.
type previewTasks struct {
	taskManager

	preview *upgrade.UpgradePreview
	err     error
	got     upgrade.CreateTaskRequest
}

func (p *previewTasks) PreviewUpgrade(_ context.Context, req upgrade.CreateTaskRequest) (*upgrade.UpgradePreview, error) {
	p.got = req

	return p.preview, p.err
}

func postUpgradePreview(t *testing.T, tasks *previewTasks, body string) (int, map[string]any) {
	t.Helper()

	handler := &Handler{logger: logger.NewNoOpLogger(), tasks: tasks}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/b/upgrade/preview", strings.NewReader(body)))

	var answer map[string]any

	err := json.Unmarshal(rec.Body.Bytes(), &answer)
	if err != nil {
		t.Fatalf("body is not valid JSON: %v (%s)", err, rec.Body.String())
	}

	return rec.Code, answer
}

func TestUpgradePreviewJSON(t *testing.T) {
	t.Parallel()

	expires := time.Date(2026, 10, 23, 12, 15, 0, 0, time.UTC)
	tasks := &previewTasks{preview: &upgrade.UpgradePreview{
		ID:          "up-1",
		ExpiresAt:   expires,
		InstanceIDs: []string{previewedInstance, secondInstance},
		Targets:     []string{"stemcell ubuntu-noble/1.2"},
		Diverged: []upgrade.DivergedInstance{{
			InstanceID: previewedInstance,
			Divergence: &certinventory.DivergenceRef{State: certinventory.DivergenceUnconverged, TaskID: "patch-1"},
			Variables:  []string{"/ws-bosh/redis-7a1b/redis_crt"},
			Additional: []upgrade.VersionPair{{Name: "/ws-bosh/redis-7a1b/redis_crt", DeployedVersionID: "v1", LatestVersionID: "v2"}},
		}},
	}}

	status, answer := postUpgradePreview(t, tasks,
		`{"instance_ids":["7a1b","9c2d"],"target_stemcell":{"os":"ubuntu-noble","version":"1.2"},"upgrade_preview_id":"ignored"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", status, answer)
	}

	if !slices.Equal(tasks.got.InstanceIDs, []string{previewedInstance, secondInstance}) || tasks.got.TargetStemcell.Version != "1.2" {
		t.Fatalf("the manager got %+v", tasks.got)
	}

	if answer["id"] != "up-1" || answer["expires_at"] != expires.Format(time.RFC3339) {
		t.Fatalf("unexpected id or expiry: %v", answer)
	}

	ids, _ := answer["instance_ids"].([]any)
	if len(ids) != 2 || ids[0] != previewedInstance || ids[1] != secondInstance {
		t.Fatalf("unexpected instance_ids: %v", answer["instance_ids"])
	}

	diverged, _ := answer["diverged"].([]any)
	if len(diverged) != 1 {
		t.Fatalf("unexpected diverged: %v", answer["diverged"])
	}

	first, _ := diverged[0].(map[string]any)
	divergence, _ := first["divergence"].(map[string]any)

	if first["instance_id"] != previewedInstance || divergence["state"] != certinventory.DivergenceUnconverged {
		t.Fatalf("unexpected diverged instance: %v", first)
	}

	additional, _ := first["additional"].([]any)
	variables, _ := first["variables"].([]any)

	if len(additional) != 1 || len(variables) != 1 {
		t.Fatalf("unexpected effects: %v", first)
	}
}

func TestUpgradePreviewHandlerErrors(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		body   string
		err    error
		status int
		code   string
	}{
		"invalid JSON":  {body: `{`, status: http.StatusBadRequest, code: apierr.CodeInvalidRequest},
		"no instances":  {body: `{}`, err: upgrade.ErrNoInstances, status: http.StatusBadRequest},
		"coded failure": {body: `{}`, err: apierr.New(http.StatusServiceUnavailable, apierr.CodeCredHubUnavailable, "down"), status: http.StatusServiceUnavailable, code: apierr.CodeCredHubUnavailable},
		"still loading": {body: `{}`, err: upgrade.ErrTasksLoading, status: http.StatusServiceUnavailable, code: apierr.CodeTasksLoading},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			status, answer := postUpgradePreview(t, &previewTasks{err: test.err}, test.body)
			if status != test.status || answer["success"] != false {
				t.Fatalf("status = %d, want %d: %v", status, test.status, answer)
			}

			if test.code != "" && answer["code"] != test.code {
				t.Fatalf("code = %v, want %s", answer["code"], test.code)
			}
		})
	}
}
