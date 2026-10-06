package boshapi

// Deployment represents a BOSH deployment.
type Deployment struct {
	Name        string          `json:"name"`
	Teams       []string        `json:"teams,omitempty"`
	CloudConfig string          `json:"cloud_config,omitempty"`
	Stemcells   []DeploymentRef `json:"stemcells,omitempty"`
	Releases    []DeploymentRef `json:"releases,omitempty"`
	Locked      bool            `json:"locked,omitempty"`
}

// DeploymentManifest represents a deployment's manifest response.
type DeploymentManifest struct {
	Manifest string `json:"manifest"`
}

// DeployOptions represents options for deploying a manifest. Options are
// transmitted as query parameters on POST /deployments.
type DeployOptions struct {
	Recreate           bool   `json:"recreate,omitempty"`
	RecreatePersistent bool   `json:"recreate_persistent_disks,omitempty"`
	SkipDrain          string `json:"skip_drain,omitempty"`
	Fix                bool   `json:"fix,omitempty"`
	DryRun             bool   `json:"dry_run,omitempty"`
	MaxInFlight        string `json:"max_in_flight,omitempty"`
	Canaries           string `json:"canaries,omitempty"`
	// RecreateVMsCreatedBefore limits recreation to VMs created before this
	// RFC 3339 timestamp. Requires Recreate to be true. Supported by BOSH
	// Director v283.0.0 and later.
	RecreateVMsCreatedBefore string `json:"recreate_vms_created_before,omitempty"`
	// ForceLatestVariables makes the director use the latest version of every
	// variable instead of reusing versions from the previous deployment.
	ForceLatestVariables bool `json:"force_latest_variables,omitempty"`
}

// DeleteDeploymentOptions represents options for deleting a deployment.
type DeleteDeploymentOptions struct {
	Force bool `json:"force,omitempty"`
}

// DeploymentDiff represents the diff between current and proposed deployment.
type DeploymentDiff struct {
	Context map[string]interface{} `json:"context,omitempty"`
	Diff    [][]interface{}        `json:"diff"`
}

// DiffOptions represents options for a diff request.
type DiffOptions struct {
	Redact bool `json:"redact,omitempty"`
}

// DeploymentListOptions and its ToValues method are defined in query.go.
