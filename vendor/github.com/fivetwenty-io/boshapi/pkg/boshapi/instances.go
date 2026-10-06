package boshapi

// Instance represents a BOSH deployment instance.
type Instance struct {
	AgentID            string    `json:"agent_id"`
	CID                string    `json:"cid"`
	Job                string    `json:"job"`
	Index              int       `json:"index"`
	ID                 string    `json:"id"`
	AZ                 string    `json:"az"`
	IPs                []string  `json:"ips"`
	VMType             string    `json:"vm_type"`
	VMCreatedAt        string    `json:"vm_created_at"`
	DiskCIDs           []string  `json:"disk_cids"`
	State              string    `json:"state"`
	Bootstrap          bool      `json:"bootstrap"`
	Ignore             bool      `json:"ignore"`
	Processes          []Process `json:"processes,omitempty"`
	Vitals             *Vitals   `json:"vitals,omitempty"`
	ResurrectionPaused bool      `json:"resurrection_paused"`
}

// InstanceDetail represents detailed instance information (from format=full).
type InstanceDetail struct {
	Instance
	VMCID   string   `json:"vm_cid"`
	DiskCID string   `json:"disk_cid"`
	Expects *Expects `json:"expects,omitempty"`
}

// Expects represents expected configuration for an instance.
type Expects struct {
	VMType    string `json:"vm_type"`
	Stemcell  string `json:"stemcell"`
	NetworkID string `json:"network_id"`
}

// Process represents a running process on an instance.
type Process struct {
	Name   string      `json:"name"`
	State  string      `json:"state"`
	Uptime *UptimeInfo `json:"uptime,omitempty"`
	Mem    *MemInfo    `json:"mem,omitempty"`
	CPU    *CPUInfo    `json:"cpu,omitempty"`
}

// UptimeInfo represents process uptime.
type UptimeInfo struct {
	Secs int64 `json:"secs"`
}

// MemInfo represents process memory usage.
type MemInfo struct {
	KB      int64   `json:"kb"`
	Percent float64 `json:"percent"`
}

// CPUInfo represents process CPU usage. Total is the figure the agent
// reports for a process. Sys, User, and Wait are filled only when the
// director's output carries them.
type CPUInfo struct {
	Total float64 `json:"total"`
	Sys   string  `json:"sys,omitempty"`
	User  string  `json:"user,omitempty"`
	Wait  string  `json:"wait,omitempty"`
}

// Vitals represents instance vitals.
type Vitals struct {
	CPU  VitalsCPU  `json:"cpu"`
	Disk VitalsDisk `json:"disk"`
	Mem  VitalsMem  `json:"mem"`
	Swap VitalsMem  `json:"swap"`
	Load []string   `json:"load"`
}

// VitalsCPU represents CPU vitals.
type VitalsCPU struct {
	Sys  string `json:"sys"`
	User string `json:"user"`
	Wait string `json:"wait"`
}

// VitalsDisk represents disk vitals.
type VitalsDisk struct {
	Ephemeral  VitalsDiskUsage `json:"ephemeral"`
	Persistent VitalsDiskUsage `json:"persistent"`
	System     VitalsDiskUsage `json:"system"`
}

// VitalsDiskUsage represents individual disk usage.
type VitalsDiskUsage struct {
	InodePercent string `json:"inode_percent"`
	Percent      string `json:"percent"`
}

// VitalsMem represents memory vitals.
type VitalsMem struct {
	KB      string `json:"kb"`
	Percent string `json:"percent"`
}

// UpdateStateOptions represents options for updating instance or job state.
// Options are transmitted as query parameters on
// PUT /deployments/{deployment}/jobs/{job}[/{index_or_id}].
type UpdateStateOptions struct {
	NewState  string `json:"new_state"`
	SkipDrain bool   `json:"skip_drain,omitempty"`
	Fix       bool   `json:"fix,omitempty"`
	DryRun    bool   `json:"dry_run,omitempty"`
	// Hard requests a hard stop (VM deleted, persistent disk kept). It is
	// translated to state "detached" when NewState is "stopped".
	Hard bool `json:"hard,omitempty"`
	// Canaries and MaxInFlight only take effect on job-level state changes
	// (UpdateJobState); the director ignores them on per-instance routes.
	Canaries    string `json:"canaries,omitempty"`
	MaxInFlight string `json:"max_in_flight,omitempty"`
	// RecreateVMsCreatedBefore limits recreation to VMs created before this
	// RFC 3339 timestamp. Requires NewState "recreate" and is only accepted
	// by the director on job-level state changes (use UpdateJobState).
	// Supported by BOSH Director v283.0.0 and later.
	RecreateVMsCreatedBefore string `json:"recreate_vms_created_before,omitempty"`
}

// StopInstanceOptions represents options for the per-instance stop action
// (POST /deployments/{deployment}/instance_groups/{group}/{id}/actions/stop).
type StopInstanceOptions struct {
	SkipDrain bool `json:"skip_drain,omitempty"`
	// Hard deletes the VM while keeping the persistent disk.
	Hard bool `json:"hard,omitempty"`
}

// RestartInstanceOptions represents options for the per-instance restart
// action.
type RestartInstanceOptions struct {
	SkipDrain bool `json:"skip_drain,omitempty"`
}

// RecreateInstanceOptions represents options for the per-instance recreate
// action. Recreate is always a hard stop; the director enforces this.
type RecreateInstanceOptions struct {
	SkipDrain bool `json:"skip_drain,omitempty"`
	// IgnoreUnresponsiveAgent proceeds with recreation even when the
	// instance's agent does not respond.
	IgnoreUnresponsiveAgent bool `json:"ignore_unresponsive_agent,omitempty"`
}

// LogOptions represents options for fetching instance logs.
type LogOptions struct {
	Type    string   `json:"type,omitempty"`    // "job" or "agent"
	Filters []string `json:"filters,omitempty"` // glob patterns
}
