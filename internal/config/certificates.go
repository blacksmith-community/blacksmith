package config

// CertificatesConfig holds the switches for the certificate features.
type CertificatesConfig struct {
	Inventory    CertificateSwitch `yaml:"inventory"`
	Rotation     CertificateSwitch `yaml:"rotation"`
	PatchInPlace CertificateSwitch `yaml:"patch_in_place"`
}

// CertificateSwitch turns one certificate feature on or off. A nil Enabled
// means the operator did not set it, and the feature takes its default.
type CertificateSwitch struct {
	Enabled *bool `yaml:"enabled"`
}

// InventoryEnabled reports whether the certificate inventory runs. It is on
// unless the operator turns it off.
func (c CertificatesConfig) InventoryEnabled() bool {
	return c.Inventory.Enabled == nil || *c.Inventory.Enabled
}

// RotationEnabled reports whether certificate rotation may change anything.
// It is off unless the operator turns it on.
func (c CertificatesConfig) RotationEnabled() bool {
	return c.Rotation.Enabled != nil && *c.Rotation.Enabled
}

// PatchInPlaceEnabled reports whether patching a certificate on a VM is
// allowed. It needs both its own switch and the rotation switch.
func (c CertificatesConfig) PatchInPlaceEnabled() bool {
	return c.RotationEnabled() && c.PatchInPlace.Enabled != nil && *c.PatchInPlace.Enabled
}
