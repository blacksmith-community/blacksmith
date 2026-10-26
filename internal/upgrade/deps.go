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
	// CertificateKeyPair reads a version's certificate and private key.
	// Only patch mode calls it, and the caller zeroes the pair when it is
	// done with it.
	CertificateKeyPair(ctx context.Context, versionID string) (*credhub.KeyPair, error)
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
	// UpgradePreviews holds the previews of upgrades that include a
	// diverged instance. Configure gives the manager a store of its own
	// when it is nil.
	UpgradePreviews *UpgradePreviewStore
	// Prober reads the leaf certificate each listener serves, which a
	// redeploy checks once the deploy is done.
	Prober certinventory.Prober
	// Divergence holds the records of patches BOSH does not know about. It
	// is nil when the broker has no Vault, and then no settled deploy asks
	// for a rescan.
	Divergence *certinventory.DivergenceStore
	// PatchRunner opens one SSH session on the VMs of an instance group
	// at the indices given, for patch mode. It is nil when the broker
	// cannot open one, and then no instance can be patched.
	PatchRunner func(ctx context.Context, deployment, group string, indices []int) (PatchSession, error)
	// WorkerCount reports how many workers the director has, and whether
	// it knows. A nil WorkerCount, or one that answers false, sends the
	// preview to the BOSH configuration. The director's /info exposes no
	// count (boshapi@v0.283.9/pkg/boshapi/info.go:4-14), so the default
	// seam always answers false.
	WorkerCount func(ctx context.Context) (int, bool)
	// BOSH returns the BOSH configuration as it stands now. A nil BOSH
	// means the default configuration.
	BOSH func() config.BOSHConfig
	// Authorizer decides whether a command request may go ahead. Configure
	// gives the manager a ConfigAuthorizer over Commands when it is nil.
	Authorizer CommandAuthorizer
	// Commands returns the commands configuration as it stands now. A nil
	// Commands turns commands off.
	Commands func() config.CommandsConfig
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

	if d.UpgradePreviews == nil {
		d.UpgradePreviews = NewUpgradePreviewStore(d.Now)
	}

	if d.Authorizer == nil {
		d.Authorizer = NewConfigAuthorizer(d.Commands)
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
	return writesAllowed(m.certDeps(), mode)
}

// certWritesAllowedLocked is certWritesAllowed for a caller that holds
// mu.
func (m *Manager) certWritesAllowedLocked(mode RotationMode) error {
	return writesAllowed(m.deps, mode)
}

// writesAllowed says whether the switches and the CredHub client of the
// dependencies let a certificate task in this mode change anything now.
func writesAllowed(deps Deps, mode RotationMode) error {
	var switches config.CertificatesConfig
	if deps.Switches != nil {
		switches = deps.Switches()
	}

	if !switches.InventoryEnabled() {
		return apierr.New(http.StatusForbidden, apierr.CodeInventoryDisabled, "Certificate inventory is turned off. Set certificates.inventory.enabled to true to allow certificate changes.")
	}

	if !switches.RotationEnabled() {
		return apierr.New(http.StatusForbidden, apierr.CodeRotationDisabled, "Certificate rotation is turned off. Set certificates.rotation.enabled to true to allow it.")
	}

	if mode == RotationModePatch && !switches.PatchInPlaceEnabled() {
		return apierr.New(http.StatusForbidden, apierr.CodePatchDisabled, "Patching certificates in place is turned off. Set certificates.patch_in_place.enabled to true to allow it.")
	}

	if deps.CredHub == nil {
		return apierr.New(http.StatusForbidden, apierr.CodeCredHubNotConfigured, "CredHub is not configured, so certificates can't be regenerated.")
	}

	return nil
}
