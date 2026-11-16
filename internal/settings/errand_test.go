package settings_test

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/config"
	"blacksmith/internal/settings"
)

const (
	savedFortyFive = "45m0s"
	twentyMinutes  = "20m"
)

var errVaultDown = errors.New("vault is down")

// memoryStore is a Vault that holds the one settings record.
type memoryStore struct {
	mu      sync.Mutex
	record  map[string]interface{}
	present bool
	getErr  error
	putErr  error
}

func (s *memoryStore) Get(_ context.Context, path string, out interface{}) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.getErr != nil {
		return false, s.getErr
	}

	if path != settings.Path || !s.present {
		return false, nil
	}

	target, _ := out.(*map[string]interface{})

	copied := map[string]interface{}{}
	for key, value := range s.record {
		copied[key] = value
	}

	*target = copied

	return true, nil
}

func (s *memoryStore) Put(_ context.Context, path string, data interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.putErr != nil {
		return s.putErr
	}

	record, _ := data.(map[string]interface{})
	s.record, s.present = record, path == settings.Path

	return nil
}

func saved(value interface{}) *memoryStore {
	return &memoryStore{present: true, record: map[string]interface{}{settings.ErrandTimeoutKey: value}}
}

func minutes(count int) *time.Duration {
	value := time.Duration(count) * time.Minute

	return &value
}

// The errand wait comes from Vault first, then the environment variable,
// then broker.errand_timeout, then the default of 30 minutes. A layer that
// is zero or less, or not a duration, is refused and the next layer
// decides.
func TestErrandWaitFollowsItsPrecedence(t *testing.T) {
	t.Setenv(config.ErrandTimeoutEnvKey, "")

	cases := map[string]struct {
		store      *memoryStore
		env        string
		configured *time.Duration
		want       time.Duration
		source     string
		refused    int
	}{
		"vault beats everything":     {store: saved("10m"), env: twentyMinutes, configured: minutes(40), want: 10 * time.Minute, source: settings.SourceVault},
		"env beats config":           {store: &memoryStore{}, env: twentyMinutes, configured: minutes(40), want: 20 * time.Minute, source: settings.SourceEnvironment},
		"config beats the default":   {store: &memoryStore{}, configured: minutes(40), want: 40 * time.Minute, source: settings.SourceConfig},
		"the default last":           {store: &memoryStore{}, want: config.DefaultErrandTimeout, source: settings.SourceDefault},
		"zero in vault is refused":   {store: saved("0s"), env: twentyMinutes, want: 20 * time.Minute, source: settings.SourceEnvironment, refused: 1},
		"negative in env is refused": {store: &memoryStore{}, env: "-5m", configured: minutes(40), want: 40 * time.Minute, source: settings.SourceConfig, refused: 1},
		"a bare number is refused":   {store: saved("600"), want: config.DefaultErrandTimeout, source: settings.SourceDefault, refused: 1},
		"a non-string is refused":    {store: saved(600), want: config.DefaultErrandTimeout, source: settings.SourceDefault, refused: 1},
		"every layer refused":        {store: saved("0s"), env: "0", configured: minutes(0), want: config.DefaultErrandTimeout, source: settings.SourceDefault, refused: 3},
		"an unreadable vault is skipped": {
			store: &memoryStore{getErr: errVaultDown}, env: twentyMinutes, want: 20 * time.Minute, source: settings.SourceEnvironment, refused: 1,
		},
	}

	for name, scenario := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(config.ErrandTimeoutEnvKey, scenario.env)

			got := settings.ErrandWait(t.Context(), scenario.store, scenario.configured)

			if got.Wait != scenario.want || got.Source != scenario.source || len(got.Refused) != scenario.refused {
				t.Errorf("ErrandWait = %v from %q with %d refused, want %v from %q with %d refused (refusals %v)",
					got.Wait, got.Source, len(got.Refused), scenario.want, scenario.source, scenario.refused, got.Refused)
			}
		})
	}
}

// With no Vault the wait comes from the layers below it.
func TestErrandWaitWithoutAVault(t *testing.T) {
	t.Setenv(config.ErrandTimeoutEnvKey, "")

	got := settings.ErrandWait(t.Context(), nil, minutes(7))
	if got.Wait != 7*time.Minute || got.Source != settings.SourceConfig {
		t.Errorf("ErrandWait = %v from %q, want 7m from config", got.Wait, got.Source)
	}
}

// Saving an errand timeout keeps every other key of the record, and
// refuses a value that is zero or less or is not a duration.
func TestSavingTheErrandTimeoutKeepsTheOtherSettings(t *testing.T) {
	t.Parallel()

	store := &memoryStore{present: true, record: map[string]interface{}{"reconciler_interval": "5m"}}

	err := settings.SaveErrandTimeout(t.Context(), store, "45m")
	if err != nil {
		t.Fatalf("SaveErrandTimeout: %v", err)
	}

	if store.record["reconciler_interval"] != "5m" || store.record[settings.ErrandTimeoutKey] != savedFortyFive {
		t.Errorf("record = %v, want the interval kept and the timeout saved as 45m0s", store.record)
	}

	for _, bad := range []string{"0s", "-1m", "90", "soon", ""} {
		err := settings.SaveErrandTimeout(t.Context(), store, bad)
		if !errors.Is(err, settings.ErrInvalidErrandTimeout) {
			t.Errorf("SaveErrandTimeout(%q) = %v, want ErrInvalidErrandTimeout", bad, err)
		}
	}

	if store.record[settings.ErrandTimeoutKey] != savedFortyFive {
		t.Errorf("a refused value changed the record to %v", store.record)
	}
}

// Saving the reconciler interval keeps the errand timeout beside it.
func TestSavingAnotherSettingKeepsTheErrandTimeout(t *testing.T) {
	t.Parallel()

	store := saved(savedFortyFive)

	err := settings.Save(t.Context(), store, "reconciler_interval", "10m0s")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	if store.record[settings.ErrandTimeoutKey] != savedFortyFive || store.record["reconciler_interval"] != "10m0s" {
		t.Errorf("record = %v, want both keys", store.record)
	}
}

// A failed read of the record stops the save, so a save never writes over
// settings it couldn't read.
func TestSaveRefusesToOverwriteAnUnreadableRecord(t *testing.T) {
	t.Parallel()

	store := &memoryStore{getErr: errVaultDown, present: true, record: map[string]interface{}{"x": "y"}}

	err := settings.Save(t.Context(), store, "reconciler_interval", "10m0s")
	if !errors.Is(err, errVaultDown) {
		t.Fatalf("Save = %v, want the read error", err)
	}

	if _, wrote := store.record["reconciler_interval"]; wrote {
		t.Error("the save wrote over a record it could not read")
	}
}

// A value near the largest duration is clamped, so adding the margin
// to it can't overflow.
func TestAnErrandWaitIsClampedBelowOverflow(t *testing.T) {
	t.Setenv(config.ErrandTimeoutEnvKey, "")

	huge := time.Duration(math.MaxInt64)

	got := settings.ErrandWait(t.Context(), nil, &huge)
	if got.Wait <= 0 || got.Wait > settings.MaxErrandWait {
		t.Errorf("Wait = %v, want it clamped to at most %v", got.Wait, settings.MaxErrandWait)
	}

	if got.Wait+time.Hour < got.Wait {
		t.Error("the clamped wait plus an hour overflows")
	}
}
