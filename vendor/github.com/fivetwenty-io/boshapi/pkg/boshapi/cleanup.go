package boshapi

// CleanupRequest represents a request to cleanup resources.
type CleanupRequest struct {
	Config CleanupConfig `json:"config"`
}

// CleanupConfig represents cleanup configuration. The director reads only
// these two options. It deletes orphaned VMs and orphaned networks during a
// cleanup whatever the caller asks, and KeepOrphanedDisks is the one kind of
// orphan it can leave in place.
type CleanupConfig struct {
	RemoveAll         bool `json:"remove_all"`
	KeepOrphanedDisks bool `json:"keep_orphaned_disks,omitempty"`
}

// CleanupDryRunOptions represents options for cleanup dry run. The director
// reads both as query parameters.
type CleanupDryRunOptions struct {
	RemoveAll bool `json:"-"`

	// KeepOrphanedDisks leaves orphaned disks out of the preview, as a
	// cleanup that keeps them would.
	KeepOrphanedDisks bool `json:"-"`
}

// CleanupDryRunResult lists what a cleanup would remove. The director sends
// each field as a list, so a field is empty rather than zero when nothing
// qualifies.
type CleanupDryRunResult struct {
	Releases         []CleanupRelease         `json:"releases"`
	Stemcells        []Stemcell               `json:"stemcells"`
	CompiledPackages []CleanupCompiledPackage `json:"compiled_packages"`
	OrphanedDisks    []OrphanDisk             `json:"orphaned_disks"`
	OrphanedVMs      []OrphanedVM             `json:"orphaned_vms"`

	// ExportedReleases holds the blobstore ID of each exported release.
	ExportedReleases []string `json:"exported_releases"`

	// DNSBlobs holds the blobstore ID of each DNS blob.
	DNSBlobs []string `json:"dns_blobs"`
}

// CleanupRelease names a release and the versions a cleanup would delete.
type CleanupRelease struct {
	Name     string   `json:"name"`
	Versions []string `json:"versions"`
}

// CleanupCompiledPackage names a compiled package a cleanup would delete.
type CleanupCompiledPackage struct {
	PackageName     string `json:"package_name"`
	StemcellOS      string `json:"stemcell_os"`
	StemcellVersion string `json:"stemcell_version"`
}
