//nolint:testpackage // the test reads the unexported config builder
package recovery

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/config"
	internalVault "blacksmith/internal/vault"
	"blacksmith/pkg/logger"
	"blacksmith/pkg/reconciler"
	"blacksmith/pkg/testutil"
)

// Interval values the tests share.
const (
	tenMinutes    = "10m"
	notAnInterval = "every hour"
	notSoon       = "soon"
	twoMinutes    = "120"
	usingDefault  = "using the 5m0s default"
	usingEnv      = "using 2m0s from BLACKSMITH_RECONCILER_INTERVAL"
	savedKey      = "reconciler_interval"
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

// savedIntervalCase is a value saved in Vault and what loading it must do
// while the environment sets 2m and the config file sets 10m.
type savedIntervalCase struct {
	name  string
	saved string
	want  time.Duration
	warn  []string
}

func savedIntervalCases() []savedIntervalCase {
	return []savedIntervalCase{
		{name: "go duration", saved: "20m", want: 20 * time.Minute},
		{name: "bare seconds", saved: "3600", want: time.Hour},
		{name: "not a duration", saved: notSoon, want: 2 * time.Minute, warn: []string{savedKey, `"soon"`, usingEnv}},
		{name: "negative", saved: "-5", want: 2 * time.Minute, warn: []string{savedKey, `"-5"`, usingEnv}},
		{
			name: "under the minimum", saved: "5", want: reconciler.MinInterval,
			warn: []string{savedKey, `"5"`, "10s minimum", "using 10s from Vault settings"},
		},
	}
}

// A saved interval goes through the same parser as the config file and the
// environment, so it may be bare seconds, and it wins over both. A saved
// value that won't parse is skipped with a warning and the starting
// interval stays. A saved value under the minimum is raised to it with a
// warning.
func TestLoadSettingsFromVaultParsesSavedInterval(t *testing.T) {
	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("starting the in-process vault: %v", err)
	}

	vaultClient := internalVault.New(server.Addr, server.RootToken, true)

	for _, testCase := range savedIntervalCases() {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(reconcilerIntervalEnvKey, twoMinutes)

			err := vaultClient.Put(t.Context(), "blacksmith/settings", map[string]interface{}{savedKey: testCase.saved})
			if err != nil {
				t.Fatalf("persisting the setting: %v", err)
			}

			cfg := &config.Config{}
			cfg.Reconciler.Interval = tenMinutes

			reconcilerConfig, source := buildReconcilerConfig(cfg, &warnRecorder{})
			rec := &warnRecorder{}
			adapter := &ReconcilerAdapter{vault: vaultClient, logger: rec, config: reconcilerConfig, intervalSource: source}

			adapter.loadSettingsFromVault(t.Context())

			if got := adapter.GetInterval(); got != testCase.want {
				t.Errorf("interval = %v, want %v", got, testCase.want)
			}

			if len(testCase.warn) == 0 {
				if len(rec.messages) != 0 {
					t.Errorf("unexpected warnings %q", rec.messages)
				}

				return
			}

			requireOneWarning(t, rec, testCase.warn...)
		})
	}
}

// newStartingAdapter returns an adapter on vaultClient whose interval came
// from the environment, as Start would see it before reading Vault. The
// caller sets the environment.
func newStartingAdapter(t *testing.T, vaultClient *internalVault.Vault) (*ReconcilerAdapter, *warnRecorder) {
	t.Helper()

	reconcilerConfig, source := buildReconcilerConfig(&config.Config{}, &warnRecorder{})
	rec := &warnRecorder{}

	return &ReconcilerAdapter{vault: vaultClient, logger: rec, config: reconcilerConfig, intervalSource: source}, rec
}

// A Vault read that fails at startup leaves the interval from the
// environment in place, and the warning says so without the response body
// or the token. A missing secret stays quiet.
func TestLoadSettingsFromVaultWarnsWhenTheReadFails(t *testing.T) {
	t.Setenv(reconcilerIntervalEnvKey, twoMinutes)

	server, err := testutil.NewVaultDevServer(t)
	if err != nil {
		t.Fatalf("starting the in-process vault: %v", err)
	}

	t.Run("missing secret", func(t *testing.T) {
		t.Setenv(reconcilerIntervalEnvKey, twoMinutes)

		adapter, rec := newStartingAdapter(t, internalVault.New(server.Addr, server.RootToken, true))
		adapter.loadSettingsFromVault(t.Context())

		if len(rec.messages) != 0 {
			t.Errorf("unexpected warnings %q", rec.messages)
		}
	})

	t.Run("read refused", func(t *testing.T) {
		t.Setenv(reconcilerIntervalEnvKey, twoMinutes)

		const badToken = "not-the-root-token"

		adapter, rec := newStartingAdapter(t, internalVault.New(server.Addr, badToken, true))
		adapter.loadSettingsFromVault(t.Context())

		if got := adapter.GetInterval(); got != 2*time.Minute {
			t.Errorf("interval = %v, want 2m0s", got)
		}

		requireOneWarning(t, rec, "saved reconciler interval", "HTTP 403", usingEnv)

		for _, secret := range []string{badToken, "permission denied"} {
			if strings.Contains(rec.messages[0], secret) {
				t.Errorf("warning %q carries %q", rec.messages[0], secret)
			}
		}
	})

	t.Run("not a string", func(t *testing.T) {
		t.Setenv(reconcilerIntervalEnvKey, twoMinutes)

		vaultClient := internalVault.New(server.Addr, server.RootToken, true)

		err := vaultClient.Put(t.Context(), "blacksmith/settings", map[string]interface{}{savedKey: 300})
		if err != nil {
			t.Fatalf("persisting the setting: %v", err)
		}

		adapter, rec := newStartingAdapter(t, vaultClient)
		adapter.loadSettingsFromVault(t.Context())

		if got := adapter.GetInterval(); got != 2*time.Minute {
			t.Errorf("interval = %v, want 2m0s", got)
		}

		requireOneWarning(t, rec, savedKey, "not a string", usingEnv)
	})
}

// The settings endpoint changes and reads the interval from separate
// request goroutines while Start may be building the manager, so none of
// GetInterval, UpdateInterval, and buildManager may race. Run with -race
// to catch a regression.
func TestAdapterIntervalConcurrentUpdateAndRead(t *testing.T) {
	t.Setenv(reconcilerIntervalEnvKey, "")

	reconcilerConfig, source := buildReconcilerConfig(&config.Config{}, &warnRecorder{})
	adapter := &ReconcilerAdapter{logger: &warnRecorder{}, config: reconcilerConfig, intervalSource: source}
	adapter.manager = adapter.buildManager()

	const rounds = 200

	var waitGroup sync.WaitGroup

	waitGroup.Go(func() {
		for i := range rounds {
			err := adapter.UpdateInterval(t.Context(), time.Duration(i+1)*time.Minute)
			if err != nil {
				t.Errorf("UpdateInterval: %v", err)

				return
			}
		}
	})

	waitGroup.Go(func() {
		for range rounds {
			if adapter.GetInterval() <= 0 {
				t.Error("GetInterval returned a non-positive interval")

				return
			}
		}
	})

	waitGroup.Go(func() {
		for range rounds / 10 {
			if adapter.buildManager() == nil {
				t.Error("buildManager returned nil")

				return
			}
		}
	})

	waitGroup.Wait()

	want := rounds * time.Minute
	if got := adapter.GetInterval(); got != want {
		t.Errorf("adapter interval = %v, want %v", got, want)
	}

	if got := adapter.currentManager().GetInterval(); got != want {
		t.Errorf("manager interval = %v, want %v", got, want)
	}
}

// A save that lands while Start is between copying the config into the
// manager and publishing it must still reach the running manager. The
// newManager hook copies the interval, as buildManager does, and then
// holds Start in that window until the save has had its chance.
func TestSaveDuringStartReachesTheRunningManager(t *testing.T) {
	t.Setenv(reconcilerIntervalEnvKey, "")

	reconcilerConfig, source := buildReconcilerConfig(&config.Config{}, &warnRecorder{})
	adapter := &ReconcilerAdapter{logger: &warnRecorder{}, config: reconcilerConfig, intervalSource: source}

	building := make(chan struct{})
	release := make(chan struct{})

	adapter.newManager = func() reconciler.Manager {
		managerConfig := reconcilerConfig
		managerConfig.Interval = adapter.GetInterval()

		close(building)
		<-release

		return reconciler.NewReconcilerManager(managerConfig, nil, nil, nil, &loggerWrapper{logger: adapter.log()}, nil)
	}

	published := make(chan reconciler.Manager, 1)

	go func() { published <- adapter.loadSettingsAndPublishManager(t.Context()) }()

	<-building

	const saved = 20 * time.Minute

	saveDone := make(chan error, 1)

	go func() { saveDone <- adapter.UpdateInterval(t.Context(), saved) }()

	// Without intervalMu the save finishes at once, before the manager is
	// published. With it, the save waits, so give it a moment and go on.
	select {
	case err := <-saveDone:
		saveDone <- err
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	manager := <-published
	if manager == nil {
		t.Fatal("no manager was published")
	}

	err := <-saveDone
	if err != nil {
		t.Fatalf("UpdateInterval: %v", err)
	}

	if got := manager.GetInterval(); got != saved {
		t.Errorf("running manager interval = %v, want the saved %v", got, saved)
	}

	if got := adapter.GetInterval(); got != saved {
		t.Errorf("adapter interval = %v, want the saved %v", got, saved)
	}
}
