package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/constants"
	http_internal "github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// PackagesClient implements the capi.PackagesClient interface.
type PackagesClient struct {
	httpClient *http_internal.Client
}

// NewPackagesClient creates a new PackagesClient.
func NewPackagesClient(httpClient *http_internal.Client) *PackagesClient {
	return &PackagesClient{
		httpClient: httpClient,
	}
}

// Create creates a new package.
func (c *PackagesClient) Create(ctx context.Context, request *capi.PackageCreateRequest) (*capi.Package, error) {
	path := constants.APIPathPackages

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating package: %w", err)
	}

	var pkg capi.Package

	err = json.Unmarshal(resp.Body, &pkg)
	if err != nil {
		return nil, fmt.Errorf("parsing package response: %w", err)
	}

	return &pkg, nil
}

// Get retrieves a specific package.
func (c *PackagesClient) Get(ctx context.Context, guid string) (*capi.Package, error) {
	path := "/v3/packages/" + guid

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting package: %w", err)
	}

	var pkg capi.Package

	err = json.Unmarshal(resp.Body, &pkg)
	if err != nil {
		return nil, fmt.Errorf("parsing package response: %w", err)
	}

	return &pkg, nil
}

// List lists all packages.
func (c *PackagesClient) List(ctx context.Context, params *capi.QueryParams, opts ...capi.PackageListOption) (*capi.ListResponse[capi.Package], error) {
	path := constants.APIPathPackages

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	queryParams = capi.ApplyQueryOptions(queryParams, opts)

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing packages: %w", err)
	}

	var result capi.ListResponse[capi.Package]

	err = json.Unmarshal(resp.Body, &result)
	if err != nil {
		return nil, fmt.Errorf("parsing packages list response: %w", err)
	}

	return &result, nil
}

// Update updates a package's metadata.
func (c *PackagesClient) Update(ctx context.Context, guid string, request *capi.PackageUpdateRequest) (*capi.Package, error) {
	path := "/v3/packages/" + guid

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating package: %w", err)
	}

	var pkg capi.Package

	err = json.Unmarshal(resp.Body, &pkg)
	if err != nil {
		return nil, fmt.Errorf("parsing package response: %w", err)
	}

	return &pkg, nil
}

// Delete issues DELETE /v3/packages/{guid}. CF v3 returns 202 Accepted with a
// Location header pointing at /v3/jobs/{jobGuid}. We extract the job GUID from
// the header and return a Job with its GUID populated; callers use Jobs().Get
// or Jobs().PollUntilComplete for full state. Same pattern as Apps().Delete
// and Roles().Delete.
func (c *PackagesClient) Delete(ctx context.Context, guid string) (*capi.Job, error) {
	path := "/v3/packages/" + guid

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting package: %w", err)
	}

	return jobFromLocationHeader(resp, "deleting package")
}

// Upload uploads bits to a package.
func (c *PackagesClient) Upload(ctx context.Context, guid string, zipFile []byte) (*capi.Package, error) {
	path := fmt.Sprintf("/v3/packages/%s/upload", guid)

	respBody, err := uploadMultipartFile(ctx, c.httpClient, path, "package.zip", zipFile, "package")
	if err != nil {
		return nil, err
	}

	var pkg capi.Package

	err = json.Unmarshal(respBody, &pkg)
	if err != nil {
		return nil, fmt.Errorf("parsing package response: %w", err)
	}

	return &pkg, nil
}

// Download downloads a package.
func (c *PackagesClient) Download(ctx context.Context, guid string) ([]byte, error) {
	path := fmt.Sprintf("/v3/packages/%s/download", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("downloading package: %w", err)
	}

	// The response body contains the actual file content
	content, err := io.ReadAll(bytes.NewReader(resp.Body))
	if err != nil {
		return nil, fmt.Errorf("reading package content: %w", err)
	}

	return content, nil
}

// Copy copies a package to another app.
func (c *PackagesClient) Copy(ctx context.Context, sourceGUID string, request *capi.PackageCopyRequest) (*capi.Package, error) {
	path := constants.APIPathPackages

	// Build query parameters
	queryParams := url.Values{}
	queryParams.Set("source_guid", sourceGUID)

	// Use Do method directly to pass query parameters properly
	resp, err := c.httpClient.Do(ctx, &http_internal.Request{
		Method: http.MethodPost,
		Path:   path,
		Query:  queryParams,
		Body:   request,
	})
	if err != nil {
		return nil, fmt.Errorf("copying package: %w", err)
	}

	var pkg capi.Package

	err = json.Unmarshal(resp.Body, &pkg)
	if err != nil {
		return nil, fmt.Errorf("parsing package response: %w", err)
	}

	return &pkg, nil
}
