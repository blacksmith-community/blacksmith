package boshapi

// Errand represents a BOSH errand.
type Errand struct {
	Name string `json:"name"`
}

// RunErrandOptions represents options for running an errand. The JSON keys
// are the ones the director reads, and all three are always sent. Instances
// narrows the run to the listed instance groups or instances, and an empty
// list runs the errand on every instance that can run it.
type RunErrandOptions struct {
	KeepAlive   bool             `json:"keep-alive"`
	WhenChanged bool             `json:"when-changed"`
	Instances   []ErrandInstance `json:"instances"`
}

// ErrandInstance represents a specific instance for an errand.
type ErrandInstance struct {
	Group string `json:"group,omitempty"`
	ID    string `json:"id,omitempty"`
}
