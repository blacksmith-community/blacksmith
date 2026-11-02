package config_test

import (
	"testing"

	"go.yaml.in/yaml/v2"

	"blacksmith/internal/config"
)

func TestCertificatesSwitchDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		yaml                string
		inventory, rotation bool
		patch               bool
	}{
		{"empty document", ``, true, false, false},
		{"rotation on", "certificates:\n  rotation: {enabled: true}\n", true, true, false},
		{"patch alone", "certificates:\n  patch_in_place: {enabled: true}\n", true, false, false},
		{"patch and rotation", "certificates:\n  rotation: {enabled: true}\n  patch_in_place: {enabled: true}\n", true, true, true},
		{"inventory off", "certificates:\n  inventory: {enabled: false}\n", false, false, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var cfg config.Config

			err := yaml.Unmarshal([]byte(test.yaml), &cfg)
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}

			certs := cfg.Certificates
			if got := certs.InventoryEnabled(); got != test.inventory {
				t.Errorf("InventoryEnabled() = %v, want %v", got, test.inventory)
			}

			if got := certs.RotationEnabled(); got != test.rotation {
				t.Errorf("RotationEnabled() = %v, want %v", got, test.rotation)
			}

			if got := certs.PatchInPlaceEnabled(); got != test.patch {
				t.Errorf("PatchInPlaceEnabled() = %v, want %v", got, test.patch)
			}
		})
	}
}
