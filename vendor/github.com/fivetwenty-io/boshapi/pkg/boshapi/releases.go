package boshapi

// Release represents a BOSH release.
type Release struct {
	Name            string           `json:"name"`
	ReleaseVersions []ReleaseVersion `json:"release_versions"`
}

// ReleaseVersion represents a specific version of a release.
type ReleaseVersion struct {
	Version            string   `json:"version"`
	CommitHash         string   `json:"commit_hash"`
	UncommittedChanges bool     `json:"uncommitted_changes"`
	CurrentlyDeployed  bool     `json:"currently_deployed"`
	JobNames           []string `json:"job_names"`
}

// ReleaseDetail represents detailed release information.
type ReleaseDetail struct {
	Name     string           `json:"name"`
	Versions []ReleaseVersion `json:"versions"`
	Packages []ReleasePackage `json:"packages,omitempty"`
	Jobs     []ReleaseJob     `json:"jobs,omitempty"`
}

// ReleasePackage represents a package within a release.
type ReleasePackage struct {
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Fingerprint  string   `json:"fingerprint"`
	SHA1         string   `json:"sha1"`
	Dependencies []string `json:"dependencies"`
}

// ReleaseJob represents a job within a release.
type ReleaseJob struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Fingerprint string `json:"fingerprint"`
	SHA1        string `json:"sha1"`
}

// ReleaseGetOptions represents options for getting release details.
type ReleaseGetOptions struct {
	Version string
}

// UploadReleaseOptions represents options for uploading a release.
type UploadReleaseOptions struct {
	URL    string `json:"location,omitempty"`
	SHA1   string `json:"sha1,omitempty"`
	Rebase bool   `json:"rebase,omitempty"`
	Fix    bool   `json:"fix,omitempty"`
}

// DeleteReleaseOptions represents options for deleting a release.
type DeleteReleaseOptions struct {
	Version string
	Force   bool
}

// ExportReleaseRequest represents a request to export a compiled release.
type ExportReleaseRequest struct {
	DeploymentName  string             `json:"deployment_name"`
	ReleaseName     string             `json:"release_name"`
	ReleaseVersion  string             `json:"release_version"`
	StemcellOS      string             `json:"stemcell_os"`
	StemcellVersion string             `json:"stemcell_version"`
	SHA2            bool               `json:"sha2,omitempty"`
	Jobs            []ExportReleaseJob `json:"jobs,omitempty"`
}

// ExportReleaseJob represents a job to include in an exported release.
type ExportReleaseJob struct {
	Name string `json:"name"`
}
