//nolint:testpackage // the test reads the unexported config builder
package recovery

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/config"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/reconciler"
)

// Interval values the tests share.
const (
	tenMinutes    = "10m"
	notAnInterval = "every hour"
	notSoon       = "soon"
	twoMinutes    = "120"
	usingDefault  = "using the 5m0s default"
)

func TestBuildReconcilerConfigIntervalForms(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		env  string
		want time.Duration
	}{
		{name: "go duration from config", yaml: tenMinutes, want: 10 * time.Minute},
		{name: "bare seconds from config", yaml: "3600", want: time.Hour},
		{name: "fractional seconds from config", yaml: "60.5", want: 60*time.Second + 500*time.Millisecond},
		{name: "bare seconds from env", env: twoMinutes, want: 2 * time.Minute},
		{name: "env beats config", yaml: tenMinutes, env: "90", want: 90 * time.Second},
		{name: "unset uses default", want: DefaultReconcilerInterval},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(reconcilerIntervalEnvKey, testCase.env)

			cfg := &config.Config{}
			cfg.Reconciler.Interval = testCase.yaml

			got, _ := buildReconcilerConfig(cfg, &warnRecorder{})
			if got.Interval != testCase.want {
				t.Errorf("interval for config %q env %q = %v, want %v", testCase.yaml, testCase.env, got.Interval, testCase.want)
			}
		})
	}
}

// warnRecorder keeps what the code under test warns about and drops its
// info lines. It leaves every other Logger method to the embedded nil
// interface, because the interval code only warns and informs.
type warnRecorder struct {
	logger.Logger

	messages []string
}

func (w *warnRecorder) Info(string, ...interface{}) {}

func (w *warnRecorder) Warn(msg string, args ...interface{}) {
	w.messages = append(w.messages, fmt.Sprintf(msg, args...))
}

// requireOneWarning fails the test unless exactly one warning was recorded
// and it mentions every part.
func requireOneWarning(t *testing.T, rec *warnRecorder, parts ...string) {
	t.Helper()

	if len(rec.messages) != 1 {
		t.Fatalf("got %d warnings %q, want 1", len(rec.messages), rec.messages)
	}

	for _, part := range parts {
		if !strings.Contains(rec.messages[0], part) {
			t.Errorf("warning %q does not mention %q", rec.messages[0], part)
		}
	}
}

// invalidIntervalCase is a config file and environment pair with one value
// that the config builder must warn about.
type invalidIntervalCase struct {
	name       string
	yaml       string
	env        string
	key        string
	bad        string
	want       time.Duration
	wantSource string
	using      string
}

func invalidIntervalCases() []invalidIntervalCase {
	return []invalidIntervalCase{
		{
			name: "config value", yaml: notAnInterval,
			key: reconcilerIntervalConfigKey, bad: notAnInterval,
			want: DefaultReconcilerInterval, wantSource: reconcilerIntervalDefault,
			using: usingDefault,
		},
		{
			name: "env value", env: notSoon,
			key: reconcilerIntervalEnvKey, bad: notSoon,
			want: DefaultReconcilerInterval, wantSource: reconcilerIntervalDefault,
			using: usingDefault,
		},
		{
			name: "bad env falls back to config", yaml: tenMinutes, env: notSoon,
			key: reconcilerIntervalEnvKey, bad: notSoon,
			want: 10 * time.Minute, wantSource: reconcilerIntervalConfigKey,
			using: "using 10m0s from reconciler.interval",
		},
		{
			name: "negative config value", yaml: "-5",
			key: reconcilerIntervalConfigKey, bad: "-5",
			want: DefaultReconcilerInterval, wantSource: reconcilerIntervalDefault,
			using: usingDefault,
		},
		{
			name: "zero env value", yaml: tenMinutes, env: "0",
			key: reconcilerIntervalEnvKey, bad: "0",
			want: 10 * time.Minute, wantSource: reconcilerIntervalConfigKey,
			using: "using 10m0s from reconciler.interval",
		},
		{
			name: "bad config while env sets the interval", yaml: notAnInterval, env: twoMinutes,
			key: reconcilerIntervalConfigKey, bad: notAnInterval,
			want: 2 * time.Minute, wantSource: reconcilerIntervalEnvKey,
			using: "using 2m0s from BLACKSMITH_RECONCILER_INTERVAL",
		},
	}
}

func TestBuildReconcilerConfigWarnsOnInvalidInterval(t *testing.T) {
	for _, testCase := range invalidIntervalCases() {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(reconcilerIntervalEnvKey, testCase.env)

			cfg := &config.Config{}
			cfg.Reconciler.Interval = testCase.yaml

			rec := &warnRecorder{}

			got, source := buildReconcilerConfig(cfg, rec)
			if got.Interval != testCase.want || source != testCase.wantSource {
				t.Errorf("interval = %v from %q, want %v from %q", got.Interval, source, testCase.want, testCase.wantSource)
			}

			requireOneWarning(t, rec, testCase.key, `"`+testCase.bad+`"`, testCase.using)
		})
	}
}

func TestBuildReconcilerConfigStaysQuietOnValidInterval(t *testing.T) {
	t.Setenv(reconcilerIntervalEnvKey, "")

	cfg := &config.Config{}
	cfg.Reconciler.Interval = "3600"

	rec := &warnRecorder{}
	buildReconcilerConfig(cfg, rec)

	if len(rec.messages) != 0 {
		t.Errorf("unexpected warnings %q", rec.messages)
	}
}

// An interval under the manager's minimum is raised to it with a warning,
// so the adapter, and through it GET /b/blacksmith/settings, reports the
// interval the manager actually runs at.
func TestBuildReconcilerConfigRaisesIntervalToMinimum(t *testing.T) {
	tests := []struct {
		name       string
		yaml       string
		env        string
		key        string
		bad        string
		wantSource string
	}{
		{name: "env seconds", env: "5", key: reconcilerIntervalEnvKey, bad: "5", wantSource: reconcilerIntervalEnvKey},
		{name: "config seconds", yaml: "5", key: reconcilerIntervalConfigKey, bad: "5", wantSource: reconcilerIntervalConfigKey},
		{name: "config duration", yaml: "500ms", key: reconcilerIntervalConfigKey, bad: "500ms", wantSource: reconcilerIntervalConfigKey},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(reconcilerIntervalEnvKey, testCase.env)

			cfg := &config.Config{}
			cfg.Reconciler.Interval = testCase.yaml

			rec := &warnRecorder{}

			reconcilerConfig, source := buildReconcilerConfig(cfg, rec)
			if reconcilerConfig.Interval != reconciler.MinInterval || source != testCase.wantSource {
				t.Errorf("interval = %v from %q, want %v from %q", reconcilerConfig.Interval, source, reconciler.MinInterval, testCase.wantSource)
			}

			requireOneWarning(t, rec, testCase.key, `"`+testCase.bad+`"`, "10s minimum")

			adapter := &ReconcilerAdapter{logger: rec, config: reconcilerConfig, intervalSource: source}
			if got, running := adapter.GetInterval(), adapter.buildManager().GetInterval(); got != running {
				t.Errorf("adapter reports %v while the manager runs at %v", got, running)
			}
		})
	}
}
