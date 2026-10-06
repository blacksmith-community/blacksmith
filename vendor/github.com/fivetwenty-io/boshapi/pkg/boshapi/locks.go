package boshapi

// Lock represents a BOSH Director lock.
type Lock struct {
	Type     string   `json:"type"`
	Resource []string `json:"resource"`
	Timeout  string   `json:"timeout"`
	TaskID   string   `json:"task_id"`
}
