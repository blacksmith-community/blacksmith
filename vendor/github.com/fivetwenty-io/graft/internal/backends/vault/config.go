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
	// CACert is a PEM file of CA certificates and CAPath a directory of
	// them. Either one replaces the system roots, with CACert winning
	// when both are set; see graft.VaultConfig.CACert.
	CACert string `yaml:"ca_cert"`
	CAPath string `yaml:"ca_path"`

	// caCertField and caPathField name CACert and CAPath in error
	// messages, such as "VAULT_PROD_CACERT"; empty means the field name.
	caCertField string
	caPathField string
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
