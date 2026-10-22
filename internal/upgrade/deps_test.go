package upgrade

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"blacksmith/internal/apierr"
	"blacksmith/internal/config"
	"blacksmith/internal/credhub"
	"blacksmith/pkg/logger"
)

// stubCredHub satisfies CredHubAPI without calling anything.
type stubCredHub struct{}

func (stubCredHub) CertificateMetadata(context.Context, string) (*credhub.CertificateMeta, error) {
	return nil, nil //nolint:nilnil // a stub that is never called
}

func (stubCredHub) CredentialVersions(context.Context, string, int) ([]credhub.CredentialVersion, error) {
	return nil, nil
}

func (stubCredHub) CertificateVersion(context.Context, string) (*credhub.CertificatePublic, error) {
	return nil, nil //nolint:nilnil // a stub that is never called
}

func (stubCredHub) Regenerate(context.Context, string) (*credhub.RegenerateResult, error) {
	return nil, nil //nolint:nilnil // a stub that is never called
}

func TestConfigureAfterStartFails(t *testing.T) {
	t.Parallel()

	manager := NewManager(&logger.NoOpLogger{}, newWaitDirector(), newWaitVault())
	t.Cleanup(manager.Stop)

	manager.Start()

	err := manager.Configure(Deps{CredHub: stubCredHub{}})
	if err == nil {
		t.Fatal("Configure after Start must fail")
	}

	if manager.Configured() {
		t.Fatal("a refused Configure must leave the manager unconfigured")
	}
}

func TestConfigureBeforeStartSucceeds(t *testing.T) {
	t.Parallel()

	manager := NewManager(&logger.NoOpLogger{}, newWaitDirector(), newWaitVault())
	t.Cleanup(manager.Stop)

	err := manager.Configure(Deps{CredHub: stubCredHub{}})
	if err != nil || !manager.Configured() {
		t.Fatalf("Configure = %v, configured = %v", err, manager.Configured())
	}
}

func boolPtr(value bool) *bool { return &value }

func switches(rotation, patch bool) config.CertificatesConfig {
	return config.CertificatesConfig{
		Rotation:     config.CertificateSwitch{Enabled: boolPtr(rotation)},
		PatchInPlace: config.CertificateSwitch{Enabled: boolPtr(patch)},
	}
}

func TestCertWritesAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		switches config.CertificatesConfig
		credhub  CredHubAPI
		mode     RotationMode
		code     string
	}{
		{name: "rotation off", switches: switches(false, false), credhub: stubCredHub{}, mode: RotationModeRedeploy, code: apierr.CodeRotationDisabled},
		{name: "patch needs its own switch", switches: switches(true, false), credhub: stubCredHub{}, mode: RotationModePatch, code: apierr.CodePatchDisabled},
		{name: "converge after a patch needs the patch switch", switches: switches(true, false), credhub: stubCredHub{}, mode: RotationModeConverge, code: apierr.CodePatchDisabled},
		{name: "a patch switch alone does not turn rotation on", switches: switches(false, true), credhub: stubCredHub{}, mode: RotationModePatch, code: apierr.CodeRotationDisabled},
		{name: "no CredHub", switches: switches(true, true), credhub: nil, mode: RotationModePatch, code: apierr.CodeCredHubNotConfigured},
		{name: "redeploy with rotation on", switches: switches(true, false), credhub: stubCredHub{}, mode: RotationModeRedeploy},
		{name: "patch with both on", switches: switches(true, true), credhub: stubCredHub{}, mode: RotationModePatch},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			manager := NewManager(&logger.NoOpLogger{}, newWaitDirector(), newWaitVault())
			t.Cleanup(manager.Stop)

			err := manager.Configure(Deps{CredHub: test.credhub, Switches: func() config.CertificatesConfig { return test.switches }})
			if err != nil {
				t.Fatalf("Configure: %v", err)
			}

			got := manager.certWritesAllowed(test.mode)
			if test.code == "" {
				if got != nil {
					t.Fatalf("certWritesAllowed = %v, want nil", got)
				}

				return
			}

			var apiErr *apierr.Error
			if !errors.As(got, &apiErr) || apiErr.Code != test.code || apiErr.Status != http.StatusForbidden {
				t.Fatalf("certWritesAllowed = %v, want 403 %s", got, test.code)
			}
		})
	}
}

func TestCertWritesAllowedReadsSwitchesEachTime(t *testing.T) {
	t.Parallel()

	manager := NewManager(&logger.NoOpLogger{}, newWaitDirector(), newWaitVault())
	t.Cleanup(manager.Stop)

	current := switches(true, true)

	err := manager.Configure(Deps{CredHub: stubCredHub{}, Switches: func() config.CertificatesConfig { return current }})
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}

	if got := manager.certWritesAllowed(RotationModePatch); got != nil {
		t.Fatalf("with both on, got %v", got)
	}

	current = switches(false, false)

	var apiErr *apierr.Error
	if got := manager.certWritesAllowed(RotationModePatch); !errors.As(got, &apiErr) || apiErr.Code != apierr.CodeRotationDisabled {
		t.Fatalf("after the switch turned off, got %v", got)
	}
}

func TestCertWritesAllowedWithoutConfigureRefuses(t *testing.T) {
	t.Parallel()

	manager := NewManager(&logger.NoOpLogger{}, newWaitDirector(), newWaitVault())
	t.Cleanup(manager.Stop)

	var apiErr *apierr.Error
	if got := manager.certWritesAllowed(RotationModeRedeploy); !errors.As(got, &apiErr) || apiErr.Code != apierr.CodeRotationDisabled {
		t.Fatalf("an unconfigured manager must refuse writes, got %v", got)
	}
}
