package upgrade

import (
	"context"
	"errors"
	"net/http"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/config"
	"blacksmith/internal/credhub"
)

// ErrConfigureAfterStart is what Configure returns once the manager has
// started, because the loader may already have used the old dependencies.
var ErrConfigureAfterStart = errors.New("the upgrade manager can't be configured after it has started")

// CredHubAPI is what a certificate task needs from CredHub. *credhub.Client
// satisfies it.
type CredHubAPI interface {
	CertificateMetadata(ctx context.Context, name string) (*credhub.CertificateMeta, error)
	CredentialVersions(ctx context.Context, name string, limit int) ([]credhub.CredentialVersion, error)
	CertificateVersion(ctx context.Context, versionID string) (*credhub.CertificatePublic, error)
	Regenerate(ctx context.Context, certificateID string) (*credhub.RegenerateResult, error)
}

var _ CredHubAPI = (*credhub.Client)(nil)

// Deps holds what a certificate task needs besides the director and Vault.
type Deps struct {
	// CredHub is nil when the broker has no CredHub connection.
	CredHub CredHubAPI
	// CredHubState reports whether CredHub is usable, with a reason.
	CredHubState func() (credhub.CapabilityState, string)
	// ScanInstance scans one instance now.
	ScanInstance func(ctx context.Context, instanceID string) (certinventory.InstanceScan, error)
	// Switches returns the configuration switches as they stand now.
	Switches func() config.CertificatesConfig
	// Settings returns the certificate settings as they stand now.
	Settings func() certinventory.Settings
	// Now returns the current time. It defaults to time.Now.
	Now func() time.Time
	// Previews holds the rotation previews. Configure gives the manager a
	// store of its own when it is nil.
	Previews *PreviewStore
	// Prober reads the leaf certificate each listener serves, which a
	// redeploy checks once the deploy is done.
	Prober certinventory.Prober
}

// Configure gives the manager the dependencies its certificate tasks use. It
// must run before Start, because the loader resumes stored certificate tasks
// the moment it starts, and it returns an error once Start has run.
func (m *Manager) Configure(d Deps) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return ErrConfigureAfterStart
	}

	if d.Now == nil {
		d.Now = time.Now
	}

	if d.Previews == nil {
		d.Previews = NewPreviewStore(d.Now)
	}

	m.deps = d
	m.configured = true

	return nil
}

// Configured reports whether Configure has succeeded.
func (m *Manager) Configured() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.configured
}

// Dependencies returns the dependencies Configure gave the manager.
func (m *Manager) Dependencies() Deps {
	return m.certDeps()
}

// certDeps returns the dependencies as they stand.
func (m *Manager) certDeps() Deps {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.deps
}

// certWritesAllowed says whether a certificate task in this mode may change
// anything now. It reads the switches at the moment of the call, so turning
// one off stops the next write step. Following or settling a BOSH task that
// is already running does not call it, because stopping that would hide a
// deploy that is under way. A manager that was never configured refuses.
func (m *Manager) certWritesAllowed(mode RotationMode) error {
	d := m.certDeps()

	var switches config.CertificatesConfig
	if d.Switches != nil {
		switches = d.Switches()
	}

	if !switches.RotationEnabled() {
		return apierr.New(http.StatusForbidden, apierr.CodeRotationDisabled, "Certificate rotation is turned off. Set certificates.rotation.enabled to true to allow it.")
	}

	if mode == RotationModePatch && !switches.PatchInPlaceEnabled() {
		return apierr.New(http.StatusForbidden, apierr.CodePatchDisabled, "Patching certificates in place is turned off. Set certificates.patch_in_place.enabled to true to allow it.")
	}

	if d.CredHub == nil {
		return apierr.New(http.StatusForbidden, apierr.CodeCredHubNotConfigured, "CredHub is not configured, so certificates can't be regenerated.")
	}

	return nil
}
