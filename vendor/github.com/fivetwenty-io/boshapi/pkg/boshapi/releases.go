package boshapi

import "io"

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

// ReleaseDetail represents detailed release information. Versions holds the
// version strings the director lists when no version is requested, and it is
// empty when one is.
type ReleaseDetail struct {
	Versions []string         `json:"versions"`
	Packages []ReleasePackage `json:"packages,omitempty"`
	Jobs     []ReleaseJob     `json:"jobs,omitempty"`
}

// ReleasePackage represents a package within a release. The director fills
// different fields for the two views of a release. Without a version it sends
// Name, SHA1, Version, and Dependencies. With a version it sends Name,
// BlobstoreID, SHA1, Fingerprint, and CompiledPackages.
type ReleasePackage struct {
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	Fingerprint      string            `json:"fingerprint"`
	SHA1             string            `json:"sha1"`
	Dependencies     []string          `json:"dependencies"`
	BlobstoreID      string            `json:"blobstore_id,omitempty"`
	CompiledPackages []CompiledPackage `json:"compiled_packages,omitempty"`
}

// CompiledPackage is a package compiled for one stemcell. Stemcell names the
// stemcell as <os>/<version>, such as "ubuntu-noble/1.100".
type CompiledPackage struct {
	Stemcell    string `json:"stemcell"`
	SHA1        string `json:"sha1"`
	BlobstoreID string `json:"blobstore_id"`
}

// ReleaseJob represents a job within a release. Without a version the
// director sends Name, SHA1, Version, and Packages, the names of the packages
// that the job uses. With a version it sends Name, BlobstoreID, SHA1,
// Fingerprint, Consumes, and Provides, whose entries are the link
// declarations of the job spec.
type ReleaseJob struct {
	Name        string           `json:"name"`
	Version     string           `json:"version"`
	Fingerprint string           `json:"fingerprint"`
	SHA1        string           `json:"sha1"`
	Packages    []string         `json:"packages,omitempty"`
	BlobstoreID string           `json:"blobstore_id,omitempty"`
	Consumes    []map[string]any `json:"consumes,omitempty"`
	Provides    []map[string]any `json:"provides,omitempty"`
}

// ReleaseJobDetail is a release job as GET /jobs answers with it. Spec is the
// job's parsed spec file, whose keys, such as templates, packages, and
// properties, vary by job, so it stays a generic map.
type ReleaseJobDetail struct {
	Name        string         `json:"name"`
	Fingerprint string         `json:"fingerprint"`
	Spec        map[string]any `json:"spec"`
}

// ReleaseGetOptions represents options for getting release details.
type ReleaseGetOptions struct {
	Version string
}

// UploadReleaseOptions represents options for uploading a release. URL and
// SHA1 go in the request body, and Rebase and Fix go in the query, which is
// the only place the director reads them.
type UploadReleaseOptions struct {
	URL    string `json:"location,omitempty"`
	SHA1   string `json:"sha1,omitempty"`
	Rebase bool   `json:"-"`
	Fix    bool   `json:"-"`
}

// UploadReleaseFileOptions represents options for uploading a release tarball
// from the caller's own disk. Body is read as it is sent, never held in
// memory, and Size is its length in bytes, which goes out as the request's
// Content-Length. Exactly Size bytes are read from Body, and a Body that ends
// sooner fails the upload. The caller keeps ownership of Body and closes it.
// Rebase and Fix go in the query. The director reads no SHA-1 for a file
// upload, so there is none to send.
type UploadReleaseFileOptions struct {
	Body   io.Reader
	Size   int64
	Rebase bool
	Fix    bool
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
