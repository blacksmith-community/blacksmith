//go:build !linux

package termstyle

import "errors"

// readPid1Name reports that no garden container is possible here, because
// garden runs only on Linux.
func readPid1Name() (string, error) {
	return "", errors.New("process names are read from /proc on linux only")
}
