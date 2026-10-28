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

// config reads the switches. A zero-value authorizer has none, so it
// answers with commands off and every gate fails closed.
func (a *ConfigAuthorizer) config() config.CommandsConfig {
	if a == nil || a.switches == nil {
		off := false

		return config.CommandsConfig{Operation: config.CommandSwitch{On: &off}}
	}

	return a.switches()
}

// AuthorizeRequest refuses a request when commands are off, when the second
// credential is wrong, when the run is as root and root runs are off, or
// when the user is neither root nor vcap.
func (a *ConfigAuthorizer) AuthorizeRequest(_ context.Context, r *http.Request, runAs string) error {
	cfg := a.config()

	err := checkFeatureAndCredential(cfg, r)
	if err != nil {
		return err
	}

	return checkRunAs(cfg, runAs)
}

// AuthorizeRead refuses a read when commands are off or the second
// credential is wrong. It never checks the root switch.
func (a *ConfigAuthorizer) AuthorizeRead(_ context.Context, r *http.Request) error {
	return checkFeatureAndCredential(a.config(), r)
}

// AuthorizeStored refuses a stored task when commands are off, when it runs
// as root and root runs are off, or when its user is neither root nor vcap.
func (a *ConfigAuthorizer) AuthorizeStored(_ context.Context, spec CommandTaskSpec) error {
	cfg := a.config()

	if !cfg.CommandsEnabled() {
		return errCommandsDisabled()
	}

	return checkRunAs(cfg, spec.RunAs)
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
			"This broker needs a valid command key in the "+CommandKeyHeader+" header before it previews or runs a command.")
	}

	return nil
}

// checkRunAs allows only the known users, and allows root only when root
// runs are on. It answers 400 for any other user.
func checkRunAs(cfg config.CommandsConfig, runAs string) error {
	switch runAs {
	case runAsVcap:
		return nil
	case runAsRoot:
		if cfg.RootAllowed() {
			return nil
		}

		return apierr.New(http.StatusForbidden, apierr.CodeRootDisabled,
			"Running commands as root is switched off on this broker, so choose vcap. An administrator can turn it on with commands.allow_root in the broker configuration.")
	default:
		return apierr.New(http.StatusBadRequest, apierr.CodeInvalidRunUser,
			"A command runs as root or as vcap, so choose one of those.")
	}
}

func errCommandsDisabled() error {
	return apierr.New(http.StatusForbidden, apierr.CodeCommandsDisabled,
		"Running commands is switched off on this broker. An administrator turns it on with commands.enabled in the broker configuration.")
}
