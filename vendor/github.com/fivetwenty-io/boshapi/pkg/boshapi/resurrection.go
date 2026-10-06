package boshapi

// ResurrectionState represents the resurrection state.
type ResurrectionState struct {
	Paused bool `json:"resurrection_paused"`
}

// ResurrectionRequest represents a request to update resurrection state.
type ResurrectionRequest struct {
	ResurrectionPaused bool `json:"resurrection_paused"`
}
