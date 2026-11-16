package settings

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"blacksmith/internal/config"
)

// ErrandTimeoutKey is the key of the errand wait in the settings record.
const ErrandTimeoutKey = "errand_timeout"

// The places an errand wait can come from, from the strongest to the
// weakest, as they appear in logs and in the settings answer.
const (
	SourceVault       = "Vault settings"
	SourceEnvironment = config.ErrandTimeoutEnvKey
	SourceConfig      = "broker.errand_timeout"
	SourceDefault     = "default"
)

// MaxErrandWait is the longest wait the broker keeps. A longer value is
// clamped to it, so the margin added to the wait for the write deadline
// can't overflow a duration.
const MaxErrandWait = time.Duration(math.MaxInt64) - time.Hour

// ErrInvalidErrandTimeout means a value is not a duration above zero.
var ErrInvalidErrandTimeout = errors.New("the errand timeout must be a duration above zero, such as 45m")

// Refusal says which layer's value was refused, and why.
type Refusal struct {
	Source string
	Err    error
}

// ErrandChoice is the errand wait in effect, where it came from, and every
// layer above it that was refused.
type ErrandChoice struct {
	Wait    time.Duration
	Source  string
	Refused []Refusal
}

// ParseErrandTimeout reads a duration such as 45m. A bare number, a value
// of zero or less, and anything else that isn't a duration is refused.
func ParseErrandTimeout(raw string) (time.Duration, error) {
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%w (got %q)", ErrInvalidErrandTimeout, raw)
	}

	return min(parsed, MaxErrandWait), nil
}

// ErrandWait picks the errand wait from the Vault settings, then the
// BLACKSMITH_ERRAND_TIMEOUT environment variable, then broker.errand_timeout
// as configured, then the default. A layer whose value is refused is
// skipped and reported, and the next layer decides. A nil store skips the
// Vault layer.
func ErrandWait(ctx context.Context, store Store, configured *time.Duration) ErrandChoice {
	var refused []Refusal

	for _, layer := range []struct {
		source string
		raw    func() (string, bool, error)
	}{
		{SourceVault, func() (string, bool, error) { return savedErrandTimeout(ctx, store) }},
		{SourceEnvironment, func() (string, bool, error) {
			value := os.Getenv(config.ErrandTimeoutEnvKey)

			return value, strings.TrimSpace(value) != "", nil
		}},
		{SourceConfig, func() (string, bool, error) {
			if configured == nil {
				return "", false, nil
			}

			return configured.String(), true, nil
		}},
	} {
		raw, present, err := layer.raw()
		if err != nil {
			refused = append(refused, Refusal{Source: layer.source, Err: err})

			continue
		}

		if !present {
			continue
		}

		wait, err := ParseErrandTimeout(raw)
		if err != nil {
			refused = append(refused, Refusal{Source: layer.source, Err: err})

			continue
		}

		return ErrandChoice{Wait: wait, Source: layer.source, Refused: refused}
	}

	return ErrandChoice{Wait: config.DefaultErrandTimeout, Source: SourceDefault, Refused: refused}
}

// savedErrandTimeout reads the errand wait saved in Vault. A value that
// isn't text comes back as an error, so it is refused and reported.
func savedErrandTimeout(ctx context.Context, store Store) (string, bool, error) {
	if store == nil {
		return "", false, nil
	}

	record, err := Read(ctx, store)
	if err != nil {
		return "", false, err
	}

	raw, found := record[ErrandTimeoutKey]
	if !found {
		return "", false, nil
	}

	text, isText := raw.(string)
	if !isText {
		return "", false, fmt.Errorf("%w (the saved value is a %T)", ErrInvalidErrandTimeout, raw)
	}

	return text, true, nil
}

// SaveErrandTimeout saves the errand wait to Vault as a duration, after it
// checks the value. Every other saved setting stays as it was.
func SaveErrandTimeout(ctx context.Context, store Store, raw string) error {
	wait, err := ParseErrandTimeout(raw)
	if err != nil {
		return err
	}

	if store == nil {
		return ErrNoStore
	}

	return Save(ctx, store, ErrandTimeoutKey, wait.String())
}
