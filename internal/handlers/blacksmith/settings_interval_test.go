package blacksmith_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	handlerpkg "blacksmith/internal/handlers/blacksmith"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/reconciler"
)

// intervalRecorder is a reconciler that remembers the interval it was given.
type intervalRecorder struct {
	interval time.Duration
	updated  bool
}

func (r *intervalRecorder) UpdateInterval(_ context.Context, interval time.Duration) error {
	r.interval = interval
	r.updated = true

	return nil
}

func (r *intervalRecorder) GetInterval() time.Duration { return r.interval }

// The settings endpoint takes the same interval forms the config file does,
// so a bare number of seconds is as good as a Go duration.
func TestUpdateSettingsReconcilerIntervalForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		value    string
		wantCode int
		want     time.Duration
	}{
		{name: "go duration", value: "5m", wantCode: http.StatusOK, want: 5 * time.Minute},
		{name: "bare seconds", value: "3600", wantCode: http.StatusOK, want: time.Hour},
		{name: "fractional seconds", value: "60.5", wantCode: http.StatusOK, want: 60*time.Second + 500*time.Millisecond},
		{name: "bare seconds under the floor", value: "5", wantCode: http.StatusBadRequest},
		{name: "go duration under the floor", value: "5s", wantCode: http.StatusBadRequest},
		{name: "not a duration", value: "soon", wantCode: http.StatusBadRequest},
		{name: "empty", value: "", wantCode: http.StatusBadRequest},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			recorder := &intervalRecorder{}
			handler := handlerpkg.NewHandler(handlerpkg.Dependencies{
				Logger:     logger.Get().Named("test"),
				Config:     createTestConfig(),
				Reconciler: recorder,
			})

			body := `{"reconciler_interval": "` + testCase.value + `"}`
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/b/settings", strings.NewReader(body))
			response := httptest.NewRecorder()

			handler.UpdateSettings(response, request)

			if response.Code != testCase.wantCode {
				t.Fatalf("status %d, want %d: %s", response.Code, testCase.wantCode, response.Body.String())
			}

			if testCase.wantCode == http.StatusOK && recorder.interval != testCase.want {
				t.Errorf("reconciler got %v, want %v", recorder.interval, testCase.want)
			}

			if testCase.wantCode != http.StatusOK && recorder.updated {
				t.Errorf("reconciler was updated to %v for a rejected value", recorder.interval)
			}

			if testCase.wantCode == http.StatusBadRequest && !strings.Contains(response.Body.String(), reconciler.MinInterval.String()) {
				t.Errorf("rejection %q does not name the %v minimum", response.Body.String(), reconciler.MinInterval)
			}
		})
	}
}
