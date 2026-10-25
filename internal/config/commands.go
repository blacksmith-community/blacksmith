package config

// defaultDirectorWorkers is what a bosh.director_workers of 0 means. It is
// the director's own worker count, set at src/kits/bosh/bosh-deployment/bosh.yml:97.
// The bound on how many commands run at once is applied against this count
// later, so it is not baked into the number here.
const defaultDirectorWorkers = 4

// CommandsConfig holds the settings for running a command on the nodes of
// service deployments.
type CommandsConfig struct {
	Operation  CommandSwitch     `yaml:",inline"`
	Root       *bool             `yaml:"allow_root"`
	Credential CommandCredential `yaml:"credential"`
}

// CommandSwitch turns the command operation on or off. A nil On means the
// operator did not set it, and the operation takes its default.
type CommandSwitch struct {
	On *bool `yaml:"enabled"`
}

// CommandCredential is the second credential a command request must carry
// when one is configured. The key is a secret.
type CommandCredential struct {
	Key string `yaml:"key"`
}

// CommandsEnabled reports whether commands may run. They are on unless the
// operator turns them off.
func (c CommandsConfig) CommandsEnabled() bool {
	return c.Operation.On == nil || *c.Operation.On
}

// RootAllowed reports whether a command may run as root. It is off unless
// the operator turns it on, and it is never on while commands are disabled.
func (c CommandsConfig) RootAllowed() bool {
	return c.CommandsEnabled() && c.Root != nil && *c.Root
}

// CredentialKey returns the second credential, or an empty string when none
// is configured.
func (c CommandsConfig) CredentialKey() string {
	return c.Credential.Key
}

// DirectorWorkerCount returns how many workers the director has, which is
// the default when bosh.director_workers is unset.
func (b BOSHConfig) DirectorWorkerCount() int {
	if b.DirectorWorkers == 0 {
		return defaultDirectorWorkers
	}

	return b.DirectorWorkers
}
