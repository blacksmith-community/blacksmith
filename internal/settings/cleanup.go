package settings

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"blacksmith/internal/config"
)

// CleanupRetryIntervalKey is the key of the interval between retries of
// owed cleanups in the settings record.
const CleanupRetryIntervalKey = "cleanup_retry_interval"

// SourceCleanupRetryConfig names broker.cleanup_retry_interval as the
// source of the interval. The Vault settings, the environment variable,
// and the default are named as for the other settings.
const SourceCleanupRetryConfig = "broker.cleanup_retry_interval"

// ErrInvalidCleanupRetryInterval means a value is not a duration from
// config.MinCleanupRetryInterval to config.MaxCleanupRetryInterval.
var ErrInvalidCleanupRetryInterval = errors.New("the cleanup retry interval must be a duration from 1m to 6h, such as 15m")

// CleanupRetryChoice is the interval between retries of owed cleanups,
// where it came from, and every layer above it that was refused.
type CleanupRetryChoice struct {
	Interval time.Duration
	Source   string
	Refused  []Refusal
}

// ParseCleanupRetryInterval reads a duration such as 15m. A bare number, a
// value under a minute or over six hours, and anything else that isn't a
// duration is refused, whichever layer it comes from.
func ParseCleanupRetryInterval(raw string) (time.Duration, error) {
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || parsed < config.MinCleanupRetryInterval || parsed > config.MaxCleanupRetryInterval {
		return 0, fmt.Errorf("%w (got %q)", ErrInvalidCleanupRetryInterval, raw)
	}

	return parsed, nil
}

// cleanupRetrySetting is the interval between retries of owed cleanups as
// a runtime setting.
//
//nolint:gochecknoglobals // a fixed description of the setting, never changed
var cleanupRetrySetting = durationSetting{
	key:           CleanupRetryIntervalKey,
	environment:   config.CleanupRetryIntervalEnvKey,
	configuration: SourceCleanupRetryConfig,
	fallback:      config.DefaultCleanupRetryInterval,
	parse:         ParseCleanupRetryInterval,
	invalid:       ErrInvalidCleanupRetryInterval,
}

// CleanupRetryInterval picks the interval between retries of owed cleanups
// from the Vault settings, then the BLACKSMITH_CLEANUP_RETRY_INTERVAL
// environment variable, then broker.cleanup_retry_interval as configured,
// then the default. A layer whose value is refused is skipped and
// reported, and the next layer decides. A nil store skips the Vault layer.
func CleanupRetryInterval(ctx context.Context, store Store, configured *time.Duration) CleanupRetryChoice {
	interval, source, refused := cleanupRetrySetting.choose(ctx, store, configured)

	return CleanupRetryChoice{Interval: interval, Source: source, Refused: refused}
}

// SaveCleanupRetryInterval saves the interval between retries of owed
// cleanups to Vault as a duration, after it checks the value. Every other
// saved setting stays as it was.
func SaveCleanupRetryInterval(ctx context.Context, store Store, raw string) error {
	return cleanupRetrySetting.save(ctx, store, raw)
}
