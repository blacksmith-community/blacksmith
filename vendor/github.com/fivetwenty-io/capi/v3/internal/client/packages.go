package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"

	"github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// PackagesClient implements the capi.PackagesClient interface
type PackagesClient struct {
	httpClient *http.Client
}

// NewPackagesClient creates a new PackagesClient
func NewPackagesClient(httpClient *http.Client) *PackagesClient {
	return &PackagesClient{
		httpClient: httpClient,
	}
}

// Create creates a new package
func (c *PackagesClient) Create(ctx context.Context, request *capi.PackageCreateRequest) (*capi.Package, error) {
	path := "/v3/packages"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating package: %w", err)
	}

	var pkg capi.Package
	if err := json.Unmarshal(resp.Body, &pkg); err != nil {
		return nil, fmt.Errorf("parsing package response: %w", err)
	}

	return &pkg, nil
}

// Get retrieves a specific package
func (c *PackagesClient) Get(ctx context.Context, guid string) (*capi.Package, error) {
	path := fmt.Sprintf("/v3/packages/%s", guid)

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting package: %w", err)
	}

	var pkg capi.Package
	if err := json.Unmarshal(resp.Body, &pkg); err != nil {
		return nil, fmt.Errorf("parsing package response: %w", err)
	}

	return &pkg, nil
}

// List lists all packages
func (c *PackagesClient) List(ctx context.Context, params *capi.QueryParams) (*capi.ListResponse[capi.Package], error) {
	path := "/v3/packages"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing packages: %w", err)
	}

	var result capi.ListResponse[capi.Package]
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("parsing packages list response: %w", err)
	}

	return &result, nil
}

// Update updates a package's metadata
func (c *PackagesClient) Update(ctx context.Context, guid string, request *capi.PackageUpdateRequest) (*capi.Package, error) {
	path := fmt.Sprintf("/v3/packages/%s", guid)

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating package: %w", err)
	}

	var pkg capi.Package
	if err := json.Unmarshal(resp.Body, &pkg); err != nil {
		return nil, fmt.Errorf("parsing package response: %w", err)
	}

	return &pkg, nil
}

// Delete deletes a package
func (c *PackagesClient) Delete(ctx context.Context, guid string) error {
	path := fmt.Sprintf("/v3/packages/%s", guid)

	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("deleting package: %w", err)
	}

	return nil
}

// Upload uploads bits to a package
func (c *PackagesClient) Upload(ctx context.Context, guid string, zipFile []byte) (*capi.Package, error) {
	path := fmt.Sprintf("/v3/packages/%s/upload", guid)

	// Create multipart form data
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add the file field
	part, err := writer.CreateFormFile("bits", "package.zip")
	if err != nil {
		return nil, fmt.Errorf("creating form file: %w", err)
	}

	if _, err := part.Write(zipFile); err != nil {
		return nil, fmt.Errorf("writing file to form: %w", err)
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("closing multipart writer: %w", err)
	}

	// Use PostRaw to send multipart form data
	resp, err := c.httpClient.PostRaw(ctx, path, buf.Bytes(), writer.FormDataContentType())
	if err != nil {
		return nil, fmt.Errorf("uploading package: %w", err)
	}

	var pkg capi.Package
	if err := json.Unmarshal(resp.Body, &pkg); err != nil {
		return nil, fmt.Errorf("parsing package response: %w", err)
	}

	return &pkg, nil
}

// Download downloads a package
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

// Copy copies a package to another app
func (c *PackagesClient) Copy(ctx context.Context, sourceGUID string, request *capi.PackageCopyRequest) (*capi.Package, error) {
	path := "/v3/packages"

	// Build query parameters
	queryParams := url.Values{}
	queryParams.Set("source_guid", sourceGUID)

	// Use Do method directly to pass query parameters properly
	resp, err := c.httpClient.Do(ctx, &http.Request{
		Method: "POST",
		Path:   path,
		Query:  queryParams,
		Body:   request,
	})
	if err != nil {
		return nil, fmt.Errorf("copying package: %w", err)
	}

	var pkg capi.Package
	if err := json.Unmarshal(resp.Body, &pkg); err != nil {
		return nil, fmt.Errorf("parsing package response: %w", err)
	}

	return &pkg, nil
}
