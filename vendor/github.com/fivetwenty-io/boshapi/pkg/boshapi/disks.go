package boshapi

// OrphanDisk represents an orphaned persistent disk.
type OrphanDisk struct {
	DiskCID         string                 `json:"disk_cid"`
	Size            int64                  `json:"size"`
	AZ              string                 `json:"az"`
	DeploymentName  string                 `json:"deployment_name"`
	InstanceName    string                 `json:"instance_name"`
	CloudProperties map[string]interface{} `json:"cloud_properties,omitempty"`
	OrphanedAt      string                 `json:"orphaned_at"`
}

// AttachDiskOptions represents options for attaching a disk. The director
// reads Deployment, Job, InstanceID, and DiskProperties as query parameters
// and takes DiskCID from the request path. Disks().AttachDisk returns
// ErrAttachDiskOptionsInvalid before sending when DiskCID, Deployment, Job, or
// InstanceID is empty.
type AttachDiskOptions struct {
	Deployment string
	Job        string
	InstanceID string
	DiskCID    string
	// DiskProperties is passed through to the director as a string, such as
	// "copy" as bosh-cli sends it. Empty means the director's default.
	DiskProperties string
}
