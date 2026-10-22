package bosh

import (
	"fmt"
	"strconv"
	"strings"
)

// MinForceLatestVariablesVersion is the first director release that accepts
// the force_latest_variables deploy option.
const MinForceLatestVariablesVersion = 279

// ForceLatestVariablesSupported reports whether a director that reports the
// given version accepts force_latest_variables. When it does not, the second
// value is the reason to show to operators.
func ForceLatestVariablesSupported(version string) (bool, string) {
	version = strings.TrimSpace(version)
	if version == "" {
		return false, "the director did not report a version"
	}

	end := strings.IndexAny(version, ". ")
	if end < 0 {
		end = len(version)
	}

	major, err := strconv.Atoi(version[:end])
	if err != nil || major < 0 {
		return false, fmt.Sprintf("could not read the director version %q", version)
	}

	if major < MinForceLatestVariablesVersion {
		return false, fmt.Sprintf("the director version %s is older than %d, which is the first to accept latest variables",
			version, MinForceLatestVariablesVersion)
	}

	return true, ""
}
