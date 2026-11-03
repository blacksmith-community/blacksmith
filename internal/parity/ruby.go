package parity

import (
	"os"
	"os/exec"
	"testing"
)

// Ruby returns the path of the ruby that the psych comparisons run. When
// ruby is not on PATH it skips the test, except when the CI environment
// variable is set, where it fails the test, so a CI image without ruby
// cannot drop the comparison against the spruce-era goldens unnoticed.
func Ruby(tb testing.TB) string {
	tb.Helper()

	ruby, err := exec.LookPath("ruby")
	if err == nil {
		return ruby
	}

	if os.Getenv("CI") != "" {
		tb.Fatal("ruby is not installed, and CI is set, so the psych comparison cannot run")
	}

	tb.Skip("ruby is not installed")

	return ""
}
