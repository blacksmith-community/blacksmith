package boshapi

// CleanupRequest represents a request to cleanup resources.
type CleanupRequest struct {
	Config CleanupConfig `json:"config"`
}

// CleanupConfig represents cleanup configuration.
type CleanupConfig struct {
	RemoveAll            bool `json:"remove_all"`
	KeepOrphanedDisks    bool `json:"keep_orphaned_disks,omitempty"`
	KeepOrphanedVMs      bool `json:"keep_orphaned_vms,omitempty"`
	KeepOrphanedNetworks bool `json:"keep_orphaned_networks,omitempty"`
}

// CleanupDryRunOptions represents options for cleanup dry run.
type CleanupDryRunOptions struct {
	RemoveAll bool `json:"remove_all"`
}

// CleanupDryRunResult represents the result of a cleanup dry run.
type CleanupDryRunResult struct {
	OrphanedDisks    int `json:"orphaned_disks"`
	OrphanedVMs      int `json:"orphaned_vms"`
	OrphanedNetworks int `json:"orphaned_networks"`
	Releases         int `json:"releases"`
	Stemcells        int `json:"stemcells"`
	CompiledPackages int `json:"compiled_packages"`
	DNSBlobs         int `json:"dns_blobs"`
	ExportedReleases int `json:"exported_releases"`
}
