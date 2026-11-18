package settings

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// durationSetting is a runtime setting that holds a duration. Its value
// comes from the Vault settings, then an environment variable, then the
// configuration file, then a default.
type durationSetting struct {
	// key is the setting's key in the settings record.
	key string

	// environment and configuration name the environment variable and the
	// configuration key, as sources in logs and in the settings answer.
	environment   string
	configuration string

	fallback time.Duration
	parse    func(raw string) (time.Duration, error)

	// invalid is the error a saved value that isn't text is refused with.
	invalid error
}

// choose picks the duration from the first layer that holds one it
// accepts, and returns it with its source and every layer above it that
// was refused. A nil store skips the Vault layer.
func (setting durationSetting) choose(ctx context.Context, store Store, configured *time.Duration) (time.Duration, string, []Refusal) {
	var refused []Refusal

	for _, layer := range []struct {
		source string
		raw    func() (string, bool, error)
	}{
		{SourceVault, func() (string, bool, error) { return setting.saved(ctx, store) }},
		{setting.environment, func() (string, bool, error) {
			value := os.Getenv(setting.environment)

			return value, strings.TrimSpace(value) != "", nil
		}},
		{setting.configuration, func() (string, bool, error) {
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

		value, err := setting.parse(raw)
		if err != nil {
			refused = append(refused, Refusal{Source: layer.source, Err: err})

			continue
		}

		return value, layer.source, refused
	}

	return setting.fallback, SourceDefault, refused
}

// saved reads the setting's value saved in Vault. A value that isn't text
// comes back as an error, so it is refused and reported.
func (setting durationSetting) saved(ctx context.Context, store Store) (string, bool, error) {
	if store == nil {
		return "", false, nil
	}

	record, err := Read(ctx, store)
	if err != nil {
		return "", false, err
	}

	raw, found := record[setting.key]
	if !found {
		return "", false, nil
	}

	text, isText := raw.(string)
	if !isText {
		return "", false, fmt.Errorf("%w (the saved value is a %T)", setting.invalid, raw)
	}

	return text, true, nil
}

// save checks raw and saves it to Vault as a duration. Every other saved
// setting stays as it was.
func (setting durationSetting) save(ctx context.Context, store Store, raw string) error {
	value, err := setting.parse(raw)
	if err != nil {
		return err
	}

	if store == nil {
		return ErrNoStore
	}

	return Save(ctx, store, setting.key, value.String())
}
