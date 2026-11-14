//nolint:testpackage // the test reads the unexported config builder
package recovery

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/config"
	"blacksmith/pkg/logger"
)

// Interval values the tests share.
const (
	tenMinutes    = "10m"
	notAnInterval = "every hour"
	notSoon       = "soon"
	twoMinutes    = "120"
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
			using: "using the 5m0s default",
		},
		{
			name: "env value", env: notSoon,
			key: reconcilerIntervalEnvKey, bad: notSoon,
			want: DefaultReconcilerInterval, wantSource: reconcilerIntervalDefault,
			using: "using the 5m0s default",
		},
		{
			name: "bad env falls back to config", yaml: tenMinutes, env: notSoon,
			key: reconcilerIntervalEnvKey, bad: notSoon,
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
