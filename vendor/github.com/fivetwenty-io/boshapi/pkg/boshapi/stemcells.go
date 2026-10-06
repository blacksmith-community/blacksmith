package boshapi

// Stemcell represents a BOSH stemcell.
type Stemcell struct {
	Name            string               `json:"name"`
	OperatingSystem string               `json:"operating_system"`
	Version         string               `json:"version"`
	CID             string               `json:"cid"`
	CPI             string               `json:"cpi"`
	Deployments     []StemcellDeployment `json:"deployments,omitempty"`
	IsInUse         bool                 `json:"is_in_use"`
	APIVersion      int                  `json:"api_version,omitempty"`
}

// StemcellDeployment represents a deployment using a stemcell.
type StemcellDeployment struct {
	Name string `json:"name"`
}

// UploadStemcellOptions represents options for uploading a stemcell.
type UploadStemcellOptions struct {
	URL  string `json:"location,omitempty"`
	SHA1 string `json:"sha1,omitempty"`
	Fix  bool   `json:"fix,omitempty"`
}

// DeleteStemcellOptions represents options for deleting a stemcell.
type DeleteStemcellOptions struct {
	Force bool
}

// StemcellUploadCheck represents the request body for stemcell upload check.
type StemcellUploadCheck struct {
	Name    string `json:"stemcell_name"`
	Version string `json:"stemcell_version"`
}

// StemcellUploadNeeded represents the response for stemcell upload check.
type StemcellUploadNeeded struct {
	Needed bool `json:"needed"`
}
