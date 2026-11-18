package settings_test

import (
	"errors"
	"testing"

	"blacksmith/internal/config"
	"blacksmith/internal/settings"
)

// envTrue is the environment variable's text for on.
const envTrue = "true"

func boolean(value bool) *bool {
	return &value
}

func savedRollback(value interface{}) *memoryStore {
	return &memoryStore{present: true, record: map[string]interface{}{settings.UpdateRollbackKey: value}}
}

// The rollback setting comes from Vault first, then the environment
// variable, then broker.update_rollback, then the default of off. A layer
// whose value isn't a boolean is skipped and reported, and the next layer
// decides.
func TestUpdateRollbackSetting_Precedence(t *testing.T) {
	cases := []struct {
		name        string
		store       *memoryStore
		env         string
		configured  *bool
		wantOn      bool
		wantSource  string
		wantRefused []string
	}{
		{"Vault beats the environment", savedRollback(false), envTrue, boolean(true), false, settings.SourceVault, nil},
		{"a saved text boolean counts", savedRollback("true"), "", nil, true, settings.SourceVault, nil},
		{"the environment beats the file", &memoryStore{}, envTrue, boolean(false), true, settings.RollbackSourceEnvironment, nil},
		{"the file beats the default", &memoryStore{}, "", boolean(true), true, settings.RollbackSourceConfig, nil},
		{"the default is off", &memoryStore{}, "", nil, false, settings.SourceDefault, nil},
		{"no store skips Vault", nil, "", boolean(true), true, settings.RollbackSourceConfig, nil},
		{
			"a saved value that isn't a boolean is skipped", savedRollback(7), envTrue, nil,
			true, settings.RollbackSourceEnvironment, []string{settings.SourceVault},
		},
		{
			"a saved word that isn't a boolean is skipped", savedRollback("sometimes"), "", boolean(true),
			true, settings.RollbackSourceConfig, []string{settings.SourceVault},
		},
		{
			"an environment value that isn't a boolean is skipped", &memoryStore{}, "maybe", boolean(true),
			true, settings.RollbackSourceConfig, []string{settings.RollbackSourceEnvironment},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv(config.UpdateRollbackEnvKey, testCase.env)

			var store settings.Store
			if testCase.store != nil {
				store = testCase.store
			}

			choice := settings.UpdateRollback(t.Context(), store, testCase.configured)
			if choice.On != testCase.wantOn || choice.Source != testCase.wantSource {
				t.Errorf("UpdateRollback = %v from %q, want %v from %q", choice.On, choice.Source, testCase.wantOn, testCase.wantSource)
			}

			var refused []string
			for _, refusal := range choice.Refused {
				refused = append(refused, refusal.Source)
			}

			if len(refused) != len(testCase.wantRefused) || (len(refused) > 0 && refused[0] != testCase.wantRefused[0]) {
				t.Errorf("refused %v, want %v", refused, testCase.wantRefused)
			}
		})
	}
}

// A Vault read that fails leaves the rollback off, even when a lower layer
// would turn it on, and reports the failure.
func TestUpdateRollbackSetting_UnreadableVaultCountsAsOff(t *testing.T) {
	t.Setenv(config.UpdateRollbackEnvKey, envTrue)

	choice := settings.UpdateRollback(t.Context(), &memoryStore{getErr: errVaultDown}, boolean(true))
	if choice.On || choice.Source != settings.SourceVaultUnreadable {
		t.Errorf("UpdateRollback = %v from %q, want off from %q", choice.On, choice.Source, settings.SourceVaultUnreadable)
	}

	if len(choice.Refused) != 1 || !errors.Is(choice.Refused[0].Err, errVaultDown) {
		t.Errorf("refused %+v, want the Vault failure", choice.Refused)
	}
}

// Saving the rollback setting keeps every other saved setting as it was.
func TestSaveUpdateRollbackKeepsTheOtherSettings(t *testing.T) {
	t.Parallel()

	store := saved(savedFortyFive)

	err := settings.SaveUpdateRollback(t.Context(), store, true)
	if err != nil {
		t.Fatalf("SaveUpdateRollback: %v", err)
	}

	if store.record[settings.UpdateRollbackKey] != true || store.record[settings.ErrandTimeoutKey] != savedFortyFive {
		t.Errorf("record = %v, want the rollback on and the errand wait kept", store.record)
	}

	err = settings.SaveUpdateRollback(t.Context(), nil, true)
	if !errors.Is(err, settings.ErrNoStore) {
		t.Errorf("a save with no store returned %v, want ErrNoStore", err)
	}
}
