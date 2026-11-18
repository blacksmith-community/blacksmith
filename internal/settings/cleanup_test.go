package settings_test

import (
	"errors"
	"testing"
	"time"

	"blacksmith/internal/config"
	"blacksmith/internal/settings"
)

func savedInterval(value interface{}) *memoryStore {
	return &memoryStore{present: true, record: map[string]interface{}{settings.CleanupRetryIntervalKey: value}}
}

// The interval between retries of owed cleanups comes from Vault first,
// then the environment variable, then broker.cleanup_retry_interval, then
// the default of 15 minutes. A layer under a minute, or not a duration, is
// refused and the next layer decides.
func TestCleanupRetryIntervalFollowsItsPrecedence(t *testing.T) {
	t.Setenv(config.CleanupRetryIntervalEnvKey, "")

	cases := map[string]struct {
		store      *memoryStore
		env        string
		configured *time.Duration
		want       time.Duration
		source     string
		refused    int
	}{
		"vault beats everything":   {store: savedInterval("10m"), env: twentyMinutes, configured: minutes(40), want: 10 * time.Minute, source: settings.SourceVault},
		"env beats config":         {store: &memoryStore{}, env: twentyMinutes, configured: minutes(40), want: 20 * time.Minute, source: config.CleanupRetryIntervalEnvKey},
		"config beats the default": {store: &memoryStore{}, configured: minutes(40), want: 40 * time.Minute, source: settings.SourceCleanupRetryConfig},
		"the default last":         {store: &memoryStore{}, want: config.DefaultCleanupRetryInterval, source: settings.SourceDefault},
		"seconds in vault refused": {store: savedInterval("30s"), env: twentyMinutes, want: 20 * time.Minute, source: config.CleanupRetryIntervalEnvKey, refused: 1},
		"negative env refused":     {store: &memoryStore{}, env: "-5m", configured: minutes(40), want: 40 * time.Minute, source: settings.SourceCleanupRetryConfig, refused: 1},
		"a non-string is refused":  {store: savedInterval(600), want: config.DefaultCleanupRetryInterval, source: settings.SourceDefault, refused: 1},
		"unreadable vault skipped": {store: &memoryStore{getErr: errVaultDown}, env: twentyMinutes, want: 20 * time.Minute, source: config.CleanupRetryIntervalEnvKey, refused: 1},
		"the errand key is apart":  {store: saved("10m"), want: config.DefaultCleanupRetryInterval, source: settings.SourceDefault},
	}

	for name, scenario := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(config.CleanupRetryIntervalEnvKey, scenario.env)

			got := settings.CleanupRetryInterval(t.Context(), scenario.store, scenario.configured)

			if got.Interval != scenario.want || got.Source != scenario.source || len(got.Refused) != scenario.refused {
				t.Errorf("CleanupRetryInterval = %v from %q with %d refused, want %v from %q with %d refused (refusals %v)",
					got.Interval, got.Source, len(got.Refused), scenario.want, scenario.source, scenario.refused, got.Refused)
			}
		})
	}
}

// Saving the interval keeps every other key of the record, and refuses a
// value under a minute or one that is not a duration.
func TestSavingTheCleanupRetryIntervalKeepsTheOtherSettings(t *testing.T) {
	t.Parallel()

	store := saved(savedFortyFive)

	err := settings.SaveCleanupRetryInterval(t.Context(), store, "20m")
	if err != nil {
		t.Fatalf("SaveCleanupRetryInterval: %v", err)
	}

	if store.record[settings.ErrandTimeoutKey] != savedFortyFive || store.record[settings.CleanupRetryIntervalKey] != "20m0s" {
		t.Errorf("record = %v, want the errand timeout kept and the interval saved as 20m0s", store.record)
	}

	for _, bad := range []string{"59s", "0s", "-1m", "90", "soon", ""} {
		err := settings.SaveCleanupRetryInterval(t.Context(), store, bad)
		if !errors.Is(err, settings.ErrInvalidCleanupRetryInterval) {
			t.Errorf("SaveCleanupRetryInterval(%q) = %v, want ErrInvalidCleanupRetryInterval", bad, err)
		}
	}

	err = settings.SaveCleanupRetryInterval(t.Context(), nil, "20m")
	if !errors.Is(err, settings.ErrNoStore) {
		t.Errorf("SaveCleanupRetryInterval without a store = %v, want ErrNoStore", err)
	}
}
