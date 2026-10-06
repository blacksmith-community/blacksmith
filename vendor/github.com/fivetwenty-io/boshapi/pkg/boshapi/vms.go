package boshapi

// VM represents a BOSH VM in a deployment.
type VM struct {
	AgentID     string   `json:"agent_id"`
	CID         string   `json:"cid"`
	Job         string   `json:"job"`
	Index       int      `json:"index"`
	ID          string   `json:"id"`
	AZ          string   `json:"az,omitempty"`
	IPs         []string `json:"ips"`
	VMCreatedAt string   `json:"vm_created_at"`
	Active      bool     `json:"active"`
	Permanent   bool     `json:"permanent_nats_credentials"`
}

// OrphanedVM represents an orphaned VM.
type OrphanedVM struct {
	CID            string   `json:"cid"`
	OrphanedAt     string   `json:"orphaned_at"`
	AZ             string   `json:"az"`
	DeploymentName string   `json:"deployment_name"`
	InstanceName   string   `json:"instance_name"`
	IPAddresses    []string `json:"ip_addresses"`
}
