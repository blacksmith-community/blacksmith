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

// AttachDiskOptions represents options for attaching a disk.
type AttachDiskOptions struct {
	Deployment     string                 `json:"deployment"`
	Job            string                 `json:"job"`
	InstanceID     string                 `json:"instance_id"`
	DiskCID        string                 `json:"disk_cid"`
	DiskProperties map[string]interface{} `json:"disk_properties,omitempty"`
}
