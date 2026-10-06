package boshapi

// Problem represents a cloud consistency check problem.
type Problem struct {
	ID          int                    `json:"id"`
	Type        string                 `json:"type"`
	Description string                 `json:"description"`
	Data        map[string]interface{} `json:"data,omitempty"`
	Resolutions []Resolution           `json:"resolutions"`
}

// Resolution represents a possible resolution for a problem.
type Resolution struct {
	Name string `json:"name"`
	Plan string `json:"plan"`
}

// ResolveProblemsRequest represents a request to resolve cloud check problems.
type ResolveProblemsRequest struct {
	Resolutions map[string]string `json:"resolutions"`
	MaxInFlight string            `json:"max_in_flight,omitempty"`
}

// ScanAndFixRequest represents a request to scan and fix deployment problems.
type ScanAndFixRequest struct {
	Jobs []ScanAndFixJob `json:"jobs"`
}

// ScanAndFixJob represents a job to scan and fix.
type ScanAndFixJob struct {
	Name    string `json:"name"`
	Indices []int  `json:"indices"`
}
