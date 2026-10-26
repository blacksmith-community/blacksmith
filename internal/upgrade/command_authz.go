package upgrade

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"

	"blacksmith/internal/apierr"
	"blacksmith/internal/config"
)

// CommandKeyHeader carries the second credential of a command request. It
// is never read from the JSON body.
const CommandKeyHeader = "X-Blacksmith-Command-Key"

// The only users a command may run as. Anything else is refused.
const (
	runAsRoot = "root"
	runAsVcap = "vcap"
)

// CommandAuthorizer decides whether a command request may go ahead, and
// whether a stored command task may still run. A later RBAC or OAuth
// integration replaces the implementation and nothing else.
type CommandAuthorizer interface {
	// AuthorizeRequest checks a preview or a create. It sees the request,
	// so it can read a second credential, and the user the command runs
	// as.
	AuthorizeRequest(ctx context.Context, r *http.Request, runAs string) error
	// AuthorizeRead checks an output read of a run that already happened.
	// It is an audit read, so it applies the feature switch and the
	// second credential and never the root switch, whatever user the run
	// used.
	AuthorizeRead(ctx context.Context, r *http.Request) error
	// AuthorizeStored checks a task that already exists, before it is
	// queued, resumed, or allowed to run its next node. It has no request
	// to read, so it checks only what the configuration still allows.
	AuthorizeStored(ctx context.Context, spec CommandTaskSpec) error
}

// ConfigAuthorizer authorizes commands from the commands configuration. It
// asks for the configuration on every call, so a change takes effect on the
// next gate a stored task reaches.
type ConfigAuthorizer struct {
	switches func() config.CommandsConfig
}

// NewConfigAuthorizer returns an authorizer that reads switches on every
// call. A nil switches function turns commands off.
func NewConfigAuthorizer(switches func() config.CommandsConfig) *ConfigAuthorizer {
	if switches == nil {
		off := false
		switches = func() config.CommandsConfig {
			return config.CommandsConfig{Operation: config.CommandSwitch{On: &off}}
		}
	}

	return &ConfigAuthorizer{switches: switches}
}

// AuthorizeRequest refuses a request when commands are off, when the run is
// as root and root runs are off, or when the second credential is wrong.
func (a *ConfigAuthorizer) AuthorizeRequest(_ context.Context, r *http.Request, runAs string) error {
	cfg := a.switches()

	err := checkFeatureAndCredential(cfg, r)
	if err != nil {
		return err
	}

	return checkRoot(cfg, runAs)
}

// AuthorizeRead refuses a read when commands are off or the second
// credential is wrong. It never checks the root switch.
func (a *ConfigAuthorizer) AuthorizeRead(_ context.Context, r *http.Request) error {
	return checkFeatureAndCredential(a.switches(), r)
}

// AuthorizeStored refuses a stored task when commands are off, or when it
// runs as root and root runs are off.
func (a *ConfigAuthorizer) AuthorizeStored(_ context.Context, spec CommandTaskSpec) error {
	cfg := a.switches()

	if !cfg.CommandsEnabled() {
		return errCommandsDisabled()
	}

	return checkRoot(cfg, spec.RunAs)
}

// checkFeatureAndCredential applies the feature switch and the second
// credential, which a request and a read share.
func checkFeatureAndCredential(cfg config.CommandsConfig, r *http.Request) error {
	if !cfg.CommandsEnabled() {
		return errCommandsDisabled()
	}

	key := cfg.CredentialKey()
	if key == "" {
		return nil
	}

	// Hashing both sides gives the comparison equal-length inputs, so its
	// timing does not reveal the length of the key.
	got := sha256.Sum256([]byte(r.Header.Get(CommandKeyHeader)))
	want := sha256.Sum256([]byte(key))

	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		return apierr.New(http.StatusUnauthorized, apierr.CodeCommandCredentialRequired,
			"a valid command key is required in the "+CommandKeyHeader+" header")
	}

	return nil
}

// checkRoot allows only the known users, and allows root only when root
// runs are on.
func checkRoot(cfg config.CommandsConfig, runAs string) error {
	switch runAs {
	case runAsVcap:
		return nil
	case runAsRoot:
		if cfg.RootAllowed() {
			return nil
		}

		return apierr.New(http.StatusForbidden, apierr.CodeRootDisabled,
			"running commands as root is disabled")
	default:
		return apierr.New(http.StatusBadRequest, apierr.CodeInvalidRunUser,
			"commands run as root or vcap")
	}
}

func errCommandsDisabled() error {
	return apierr.New(http.StatusForbidden, apierr.CodeCommandsDisabled, "running commands is disabled")
}
