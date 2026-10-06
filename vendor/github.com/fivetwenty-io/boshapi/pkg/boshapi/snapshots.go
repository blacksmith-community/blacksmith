package boshapi

// Snapshot represents a BOSH snapshot.
type Snapshot struct {
	Job       string `json:"job"`
	Index     int    `json:"index,omitempty"`
	UUID      string `json:"uuid"`
	CID       string `json:"snapshot_cid"`
	CreatedAt string `json:"created_at"`
	Clean     bool   `json:"clean"`
}
