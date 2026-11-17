package blacksmith_test

import (
	"net/http"
	"testing"
	"time"

	handlerpkg "blacksmith/internal/handlers/blacksmith"
	"blacksmith/pkg/logger"
)

// A refused settings update changes nothing. Each request here pairs a valid
// field with an invalid one, in both orders the handler saves them, and the
// valid field must stay at its old value.
func TestUpdateSettingsRefusalSavesNothing(t *testing.T) {
	t.Parallel()

	bodies := map[string]string{
		"versions then interval":     `{"vault_max_versions": 7, "reconciler_interval": "1s"}`,
		"versions then errand":       `{"vault_max_versions": 7, "errand_timeout": "nonsense"}`,
		"interval then errand":       `{"reconciler_interval": "2h", "errand_timeout": "-5m"}`,
		"interval then bad versions": `{"reconciler_interval": "2h", "vault_max_versions": 5000}`,
		"errand then bad versions":   `{"errand_timeout": "45m", "vault_max_versions": -1}`,
		"errand then bad interval":   `{"errand_timeout": "45m", "reconciler_interval": "soon"}`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			setup := newSettingsChangesSetup(t)

			recorder := setup.save(t, body)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %s", recorder.Code, recorder.Body.String())
			}

			if setup.vault.versions != 5 || setup.interval.updated || setup.interval.interval != time.Hour {
				t.Errorf("a refused update saved versions %d, interval updated %v (%v)", setup.vault.versions, setup.interval.updated, setup.interval.interval)
			}

			if got := setup.changes(t); len(got) != 0 {
				t.Errorf("a refused update recorded %d changes", len(got))
			}
		})
	}
}

// A missing store refuses the whole request too, so a saved field is never
// followed by a 503.
func TestUpdateSettingsMissingStoreSavesNothing(t *testing.T) {
	t.Parallel()

	setup := newSettingsChangesSetup(t)
	setup.handler = newHandlerWithoutReconciler(setup)

	recorder := setup.save(t, `{"vault_max_versions": 7, "reconciler_interval": "2h"}`)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503: %s", recorder.Code, recorder.Body.String())
	}

	if setup.vault.versions != 5 {
		t.Errorf("the versions were saved as %d before the 503", setup.vault.versions)
	}
}

func newHandlerWithoutReconciler(setup settingsChangesSetup) *handlerpkg.Handler {
	return handlerpkg.NewHandler(handlerpkg.Dependencies{
		Logger: logger.Get().Named("test"), Config: createTestConfig(), Vault: setup.vault, Changes: setup.store,
	})
}
