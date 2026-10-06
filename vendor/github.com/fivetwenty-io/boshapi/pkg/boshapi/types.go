package boshapi

// DeploymentRef represents a reference to a release or stemcell used by a deployment.
type DeploymentRef struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
