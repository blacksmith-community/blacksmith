package main

import "blacksmith/internal/config"

// commandSettings returns the function the upgrade manager reads the commands
// configuration through, so it sees the configuration as it stands on each call.
func commandSettings(cfg *config.Config) func() config.CommandsConfig {
	return func() config.CommandsConfig { return cfg.Commands }
}

// boshSettings returns the function a command preview reads the BOSH
// configuration through.
func boshSettings(cfg *config.Config) func() config.BOSHConfig {
	return func() config.BOSHConfig { return cfg.BOSH }
}
