package boshapi

// Problem represents a cloud consistency check problem.
type Problem struct {
	ID          int    `json:"id"`
	Type        string `json:"type"`
	Description string `json:"description"`
	// InstanceGroup is the name of the instance group the problem belongs
	// to, which is the key MaxInFlightOverrides uses. It is empty for a
	// problem that belongs to no instance group.
	InstanceGroup string                 `json:"instance_group"`
	Data          map[string]interface{} `json:"data,omitempty"`
	Resolutions   []Resolution           `json:"resolutions"`
}

// Resolution represents a possible resolution for a problem.
type Resolution struct {
	// Name is the key the director looks the resolution up by, such as
	// "recreate_vm" or "ignore". ResolveProblemsRequest.Resolutions must
	// carry it.
	Name string `json:"name"`
	// Plan is the text that describes what the resolution does, such as
	// "Recreate VM". The director does not accept it as an answer.
	Plan string `json:"plan"`
}

// ResolveProblemsRequest represents a request to resolve cloud check problems.
type ResolveProblemsRequest struct {
	// Resolutions maps a problem ID, such as "4", to the name of the
	// resolution to apply, such as "recreate_vm". A nil value is sent as JSON
	// null, and the director then applies the automatic resolution of that
	// problem. It must name at least one problem, or ResolveProblems returns
	// ErrResolutionsRequired before sending.
	Resolutions map[string]*string `json:"resolutions"`
	// MaxInFlightOverrides maps an instance group name to the max_in_flight
	// value, such as "2" or "50%", to use while resolving its problems.
	MaxInFlightOverrides map[string]string `json:"max_in_flight_overrides,omitempty"`
}

// ScanAndFixRequest represents a request to scan and fix deployment problems.
// ScanAndFix builds the body itself, so the struct carries no JSON tags.
type ScanAndFixRequest struct {
	Jobs []ScanAndFixJob
}

// ScanAndFixJob represents a job to scan and fix. Name is an instance group
// name, and Instances names the instances to scan, each by index, such as
// "0", or by instance ID, such as "4e0e7b4f-6b5d-4a37-8d0e-1b2a3c4d5e6f".
// The director accepts both, and its resurrector sends IDs.
type ScanAndFixJob struct {
	Name      string
	Instances []string
}
