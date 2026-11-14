//nolint:testpackage // the test reads the unexported config builder and duration parser
package recovery

import (
	"testing"
	"time"

	"blacksmith/internal/config"
)

func TestBuildReconcilerConfigIntervalForms(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		env  string
		want time.Duration
	}{
		{name: "go duration from config", yaml: "10m", want: 10 * time.Minute},
		{name: "bare seconds from config", yaml: "3600", want: time.Hour},
		{name: "fractional seconds from config", yaml: "60.5", want: 60*time.Second + 500*time.Millisecond},
		{name: "bare seconds from env", env: "120", want: 2 * time.Minute},
		{name: "env beats config", yaml: "10m", env: "90", want: 90 * time.Second},
		{name: "unset uses default", want: DefaultReconcilerInterval},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("BLACKSMITH_RECONCILER_INTERVAL", testCase.env)

			cfg := &config.Config{}
			cfg.Reconciler.Interval = testCase.yaml

			got := buildReconcilerConfig(cfg).Interval
			if got != testCase.want {
				t.Errorf("interval for config %q env %q = %v, want %v", testCase.yaml, testCase.env, got, testCase.want)
			}
		})
	}
}
