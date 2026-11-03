// Package vault provides vault client management and configuration
// for the vault operator backend.
package vault

import "strings"

// Target represents a vault target configuration.
type Target struct {
	URL        string `yaml:"url"`
	Token      string `yaml:"token"`
	Namespace  string `yaml:"namespace"`
	SkipVerify bool   `yaml:"skip_verify"`
}

// SkipVerify checks whether the given environment variable value indicates
// that TLS verification should be skipped.
func SkipVerify(env string) bool {
	env = strings.ToLower(env)
	if env == "" || env == "no" || env == "false" || env == "0" || env == "off" {
		return false
	}
	return true
}
