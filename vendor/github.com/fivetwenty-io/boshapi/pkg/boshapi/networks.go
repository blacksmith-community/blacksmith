package boshapi

// OrphanedNetwork represents an orphaned network.
type OrphanedNetwork struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	CreatedAt  string `json:"created_at"`
	OrphanedAt string `json:"orphaned_at"`
}
