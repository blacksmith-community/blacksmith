package boshapi

// DynamicDisk describes a dynamic disk known to the director, as returned
// by GET /dynamic_disks. Supported by BOSH Director v283.1.2 and later.
type DynamicDisk struct {
	// Name is the unique dynamic disk name.
	Name string `json:"name"`
	// DiskCID is the IaaS disk identifier.
	DiskCID string `json:"disk_cid"`
	// Deployment is the owning deployment name, empty when unowned.
	Deployment string `json:"deployment"`
	// Instance is the name of the instance the disk is attached to, empty when detached.
	Instance string `json:"instance"`
	// AvailabilityZone is the AZ the disk lives in.
	AvailabilityZone string `json:"availability_zone"`
	// Size is the disk size in megabytes.
	Size int `json:"size"`
	// DiskPoolName is the disk pool the disk was allocated from.
	DiskPoolName string `json:"disk_pool_name"`
	// CPI is the CPI name the disk was created through.
	CPI string `json:"cpi"`
	// Metadata is the metadata attached to the disk.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// ProvideDynamicDiskRequest represents a request to provide a dynamic disk
// to an instance. Supported by BOSH Director v283.0.0 and later.
type ProvideDynamicDiskRequest struct {
	// InstanceID is the ID of the instance the disk is provided to.
	InstanceID string `json:"instance_id"`
	// DiskName is the unique name for the dynamic disk.
	DiskName string `json:"disk_name"`
	// DiskPoolName is the name of the disk pool to allocate from.
	DiskPoolName string `json:"disk_pool_name"`
	// DiskSize is the disk size in megabytes; must be at least 1.
	DiskSize int `json:"disk_size"`
	// Metadata is optional metadata attached to the disk.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// CreateDynamicDiskRequest represents a request to create a dynamic disk in
// a deployment's availability zone without attaching it. Supported by BOSH
// Director v283.1.2 and later.
type CreateDynamicDiskRequest struct {
	// DeploymentName is the deployment that owns the disk.
	DeploymentName string `json:"deployment_name"`
	// AZ is the availability zone to create the disk in.
	AZ string `json:"az"`
	// DiskName is the unique name for the dynamic disk.
	DiskName string `json:"disk_name"`
	// DiskPoolName is the name of the disk pool to allocate from.
	DiskPoolName string `json:"disk_pool_name"`
	// DiskSize is the disk size in megabytes; must be at least 1.
	DiskSize int `json:"disk_size"`
	// Metadata is optional metadata attached to the disk.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// AttachDynamicDiskRequest represents a request to attach an existing
// dynamic disk to an instance. Supported by BOSH Director v283.1.2 and later.
type AttachDynamicDiskRequest struct {
	// InstanceID is the ID of the instance the disk is attached to.
	InstanceID string `json:"instance_id"`
	// Metadata is optional metadata attached to the disk.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}
