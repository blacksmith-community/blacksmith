package settings

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"blacksmith/internal/config"
)

// UpdateRollbackKey is the key of the rollback setting in the settings
// record.
const UpdateRollbackKey = "update_rollback"

// The places the rollback setting can come from, beside SourceVault and
// SourceDefault, as they appear in logs and in the settings answer.
const (
	RollbackSourceEnvironment = config.UpdateRollbackEnvKey
	RollbackSourceConfig      = "broker.update_rollback"
	// SourceVaultUnreadable is the source of a rollback setting that is off
	// because the Vault settings couldn't be read.
	SourceVaultUnreadable = "Vault settings, unreadable, so off"
)

// ErrInvalidUpdateRollback means a value is not a boolean.
var ErrInvalidUpdateRollback = errors.New("update_rollback must be true or false")

// RollbackChoice is whether the broker rolls back a failed update, where
// that came from, and every layer above it that was refused.
type RollbackChoice struct {
	On      bool
	Source  string
	Refused []Refusal
}

// ParseUpdateRollback reads a boolean such as true or false, in the forms
// strconv.ParseBool accepts. Anything else is refused.
func ParseUpdateRollback(raw string) (bool, error) {
	parsed, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, fmt.Errorf("%w (got %q)", ErrInvalidUpdateRollback, raw)
	}

	return parsed, nil
}

// UpdateRollback picks the rollback setting from the Vault settings, then
// the BLACKSMITH_UPDATE_ROLLBACK environment variable, then
// broker.update_rollback as configured, then the default of off. A layer
// whose value isn't a boolean is skipped and reported, and the next layer
// decides. A Vault read that fails leaves the rollback off, because a
// rollback is a redeploy and the broker never redeploys on a guess. A nil
// store skips the Vault layer.
func UpdateRollback(ctx context.Context, store Store, configured *bool) RollbackChoice {
	var refused []Refusal

	saved, present, err := savedUpdateRollback(ctx, store)

	switch {
	case errors.Is(err, ErrInvalidUpdateRollback):
		refused = append(refused, Refusal{Source: SourceVault, Err: err})
	case err != nil:
		return RollbackChoice{Source: SourceVaultUnreadable, Refused: []Refusal{{Source: SourceVault, Err: err}}}
	case present:
		return RollbackChoice{On: saved, Source: SourceVault}
	}

	if value := os.Getenv(config.UpdateRollbackEnvKey); strings.TrimSpace(value) != "" {
		parsed, err := ParseUpdateRollback(value)
		if err == nil {
			return RollbackChoice{On: parsed, Source: RollbackSourceEnvironment, Refused: refused}
		}

		refused = append(refused, Refusal{Source: RollbackSourceEnvironment, Err: err})
	}

	if configured != nil {
		return RollbackChoice{On: *configured, Source: RollbackSourceConfig, Refused: refused}
	}

	return RollbackChoice{Source: SourceDefault, Refused: refused}
}

// savedUpdateRollback reads the rollback setting saved in Vault. A saved
// boolean counts, and so does text that reads as one. Anything else comes
// back as ErrInvalidUpdateRollback, so it is refused and reported.
func savedUpdateRollback(ctx context.Context, store Store) (bool, bool, error) {
	if store == nil {
		return false, false, nil
	}

	record, err := Read(ctx, store)
	if err != nil {
		return false, false, err
	}

	raw, found := record[UpdateRollbackKey]
	if !found {
		return false, false, nil
	}

	switch value := raw.(type) {
	case bool:
		return value, true, nil
	case string:
		parsed, err := ParseUpdateRollback(value)
		if err != nil {
			return false, false, err
		}

		return parsed, true, nil
	default:
		return false, false, fmt.Errorf("%w (the saved value is a %T)", ErrInvalidUpdateRollback, raw)
	}
}

// SaveUpdateRollback saves the rollback setting to Vault as a boolean.
// Every other saved setting stays as it was.
func SaveUpdateRollback(ctx context.Context, store Store, on bool) error {
	if store == nil {
		return ErrNoStore
	}

	return Save(ctx, store, UpdateRollbackKey, on)
}
