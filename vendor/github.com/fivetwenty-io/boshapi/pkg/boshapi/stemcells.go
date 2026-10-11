package boshapi

import (
	"encoding/json"
	"fmt"
	"io"
)

// Stemcell represents a BOSH stemcell. Deployments lists the deployments that
// use it, and IsInUse reports whether that list is empty.
type Stemcell struct {
	Name            string               `json:"name"`
	OperatingSystem string               `json:"operating_system"`
	Version         string               `json:"version"`
	CID             string               `json:"cid"`
	CPI             string               `json:"cpi"`
	Deployments     []StemcellDeployment `json:"deployments,omitempty"`
	APIVersion      int                  `json:"api_version,omitempty"`
	ID              int                  `json:"id"`
}

// IsInUse reports whether any deployment uses the stemcell. The director does
// not send a flag for this, so the answer comes from Deployments.
func (s Stemcell) IsInUse() bool {
	return len(s.Deployments) > 0
}

// StemcellDeployment represents a deployment using a stemcell.
type StemcellDeployment struct {
	Name string `json:"name"`
}

// UploadStemcellOptions represents options for uploading a stemcell. URL and
// SHA1 go in the request body, and Fix goes in the query, which is the only
// place the director reads it.
type UploadStemcellOptions struct {
	URL  string `json:"location,omitempty"`
	SHA1 string `json:"sha1,omitempty"`
	Fix  bool   `json:"-"`
}

// UploadStemcellFileOptions represents options for uploading a stemcell
// tarball from the caller's own disk. Body is read as it is sent, never held
// in memory, and Size is its length in bytes, which goes out as the request's
// Content-Length. Exactly Size bytes are read from Body, and a Body that ends
// sooner fails the upload. The caller keeps ownership of Body and closes it.
// Fix goes in the query.
type UploadStemcellFileOptions struct {
	Body io.Reader
	Size int64
	Fix  bool
}

// DeleteStemcellOptions represents options for deleting a stemcell.
type DeleteStemcellOptions struct {
	Force bool
}

// StemcellUploadCheck represents the request body for stemcell upload check.
// It encodes as {"stemcell": {"name": ..., "version": ...}}, the only shape
// the director accepts.
type StemcellUploadCheck struct {
	Name    string
	Version string
}

// MarshalJSON encodes the check with the name and version nested in a
// stemcell object.
func (c StemcellUploadCheck) MarshalJSON() ([]byte, error) {
	type stemcell struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}

	data, err := json.Marshal(struct {
		Stemcell stemcell `json:"stemcell"`
	}{Stemcell: stemcell(c)})
	if err != nil {
		return nil, fmt.Errorf("encoding stemcell upload check: %w", err)
	}

	return data, nil
}

// StemcellUploadNeeded represents the response for stemcell upload check.
type StemcellUploadNeeded struct {
	Needed bool `json:"needed"`
}
