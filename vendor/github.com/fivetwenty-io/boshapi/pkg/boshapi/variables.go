package boshapi

// Variable represents a BOSH deployment variable.
type Variable struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DeployedVariableUsage represents the deployments that use a variable.
type DeployedVariableUsage struct {
	Deployments []DeployedVariableEntry `json:"deployments"`
}

// DeployedVariableEntry represents a deployment using a variable. Version is
// the ID of the variable version that the deployment's last successful deploy
// uses.
type DeployedVariableEntry struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// DeploymentConfig links a deployment to a config that it uses. ID is the
// deployment's ID, and Deployment is its name.
type DeploymentConfig struct {
	ID         int                  `json:"id"`
	Deployment string               `json:"deployment"`
	Config     DeploymentConfigInfo `json:"config"`
}

// DeploymentConfigInfo identifies the config that a deployment uses. The
// director sends the numeric ID of the config here, where Configs() reports
// it as a string.
type DeploymentConfigInfo struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
}

// DeploymentConfigListOptions represents options for listing deployment
// configs. The director answers an empty list unless it is given at least one
// deployment name, so List requires one.
type DeploymentConfigListOptions struct {
	// Deployments holds the names of the deployments to list configs for.
	// Each is sent as its own deployment[] value.
	Deployments []string
}
