package boshapi

// Variable represents a BOSH deployment variable.
type Variable struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DeployedVariableUsage represents deployed variable usage information.
type DeployedVariableUsage struct {
	VariableName string                  `json:"variable_name"`
	Deployments  []DeployedVariableEntry `json:"deployments"`
}

// DeployedVariableEntry represents a deployment using a variable.
type DeployedVariableEntry struct {
	DeploymentName string `json:"deployment_name"`
	VariableID     string `json:"variable_id"`
}

// DeploymentConfig represents a config associated with a deployment.
type DeploymentConfig struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// DeploymentConfigListOptions represents options for listing deployment configs.
type DeploymentConfigListOptions struct {
	DeploymentName string
}
