package bosh

import (
	"strings"
	"testing"
)

func TestForceLatestVariablesSupportedFormats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input     string
		supported bool
		reason    string
	}{
		{"283.1.11", true, ""},
		{"283.1.11 (00000000)", true, ""},
		{"279.0.0", true, ""},
		{"278.9.2", false, "older than 279"},
		{"", false, "did not report a version"},
		{"v283", false, "could not read"},
		{"garbage", false, "could not read"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()

			ok, reason := ForceLatestVariablesSupported(tt.input)
			if ok != tt.supported {
				t.Fatalf("supported = %v, want %v (reason %q)", ok, tt.supported, reason)
			}

			if tt.supported && reason != "" {
				t.Fatalf("reason = %q, want empty", reason)
			}

			if !tt.supported && !strings.Contains(reason, tt.reason) {
				t.Fatalf("reason = %q, want it to contain %q", reason, tt.reason)
			}
		})
	}
}
