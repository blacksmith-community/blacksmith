// Package ownname resolves the name of the broker's own BOSH deployment, so
// the ownership gate, the UI, and the SSH handler all agree on it.
package ownname

import (
	"os"
	"strings"
)

// InstanceFile is the file BOSH writes the deployment name to on every
// instance of the deployment.
const InstanceFile = "/var/vcap/instance/deployment"

// Resolve returns the broker's own deployment name and true. The name comes
// from instanceFile when that file holds one, and otherwise from the
// configured environment plus -blacksmith. With neither, it returns false,
// and then nothing counts as the broker's own deployment.
func Resolve(instanceFile, environment string) (string, bool) {
	data, err := os.ReadFile(instanceFile) //nolint:gosec // the path is the BOSH instance file, or a test's own file
	if err == nil {
		name := strings.TrimSpace(string(data))
		if name != "" {
			return name, true
		}
	}

	environment = strings.TrimSpace(environment)
	if environment == "" {
		return "", false
	}

	return environment + "-blacksmith", true
}
