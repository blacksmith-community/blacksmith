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

// BuildpacksClient implements capi.BuildpacksClient.
type BuildpacksClient struct {
	httpClient *http.Client
}

// NewBuildpacksClient creates a new buildpacks client.
func NewBuildpacksClient(httpClient *http.Client) *BuildpacksClient {
	return &BuildpacksClient{
		httpClient: httpClient,
	}
}

// Create implements capi.BuildpacksClient.Create.
func (c *BuildpacksClient) Create(ctx context.Context, request *capi.BuildpackCreateRequest) (*capi.Buildpack, error) {
	path := "/v3/buildpacks"

	resp, err := c.httpClient.Post(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("creating buildpack: %w", err)
	}

	var buildpack capi.Buildpack

	err = json.Unmarshal(resp.Body, &buildpack)
	if err != nil {
		return nil, fmt.Errorf("parsing buildpack response: %w", err)
	}

	return &buildpack, nil
}

// Get implements capi.BuildpacksClient.Get.
func (c *BuildpacksClient) Get(ctx context.Context, guid string) (*capi.Buildpack, error) {
	path := "/v3/buildpacks/" + guid

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting buildpack: %w", err)
	}

	var buildpack capi.Buildpack

	err = json.Unmarshal(resp.Body, &buildpack)
	if err != nil {
		return nil, fmt.Errorf("parsing buildpack: %w", err)
	}

	return &buildpack, nil
}

// List implements capi.BuildpacksClient.List.
func (c *BuildpacksClient) List(ctx context.Context, params *capi.QueryParams, opts ...capi.BuildpackListOption) (*capi.ListResponse[capi.Buildpack], error) {
	path := "/v3/buildpacks"

	var queryParams url.Values
	if params != nil {
		queryParams = params.ToValues()
	}

	queryParams = capi.ApplyQueryOptions(queryParams, opts)

	resp, err := c.httpClient.Get(ctx, path, queryParams)
	if err != nil {
		return nil, fmt.Errorf("listing buildpacks: %w", err)
	}

	var list capi.ListResponse[capi.Buildpack]

	err = json.Unmarshal(resp.Body, &list)
	if err != nil {
		return nil, fmt.Errorf("parsing buildpacks list: %w", err)
	}

	return &list, nil
}

// Update implements capi.BuildpacksClient.Update.
func (c *BuildpacksClient) Update(ctx context.Context, guid string, request *capi.BuildpackUpdateRequest) (*capi.Buildpack, error) {
	path := "/v3/buildpacks/" + guid

	resp, err := c.httpClient.Patch(ctx, path, request)
	if err != nil {
		return nil, fmt.Errorf("updating buildpack: %w", err)
	}

	var buildpack capi.Buildpack

	err = json.Unmarshal(resp.Body, &buildpack)
	if err != nil {
		return nil, fmt.Errorf("parsing buildpack: %w", err)
	}

	return &buildpack, nil
}

// Delete implements capi.BuildpacksClient.Delete.
// CF V3 DELETE /v3/buildpacks/{guid} returns 202 Accepted with an empty body
// and the async job reference in the Location header. See Apps.Delete for
// the canonical Location-extraction pattern.
func (c *BuildpacksClient) Delete(ctx context.Context, guid string) (*capi.Job, error) {
	path := "/v3/buildpacks/" + guid

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting buildpack: %w", err)
	}

	return jobFromLocationHeader(resp, "deleting buildpack")
}

// Upload implements capi.BuildpacksClient.Upload.
func (c *BuildpacksClient) Upload(ctx context.Context, guid string, bits io.Reader) (*capi.Buildpack, error) {
	path := fmt.Sprintf("/v3/buildpacks/%s/upload", guid)

	// Create a buffer to store our multipart form
	var buf bytes.Buffer

	writer := multipart.NewWriter(&buf)

	// Create a form file field
	part, err := writer.CreateFormFile("bits", "buildpack.zip")
	if err != nil {
		return nil, fmt.Errorf("creating form file: %w", err)
	}

	// Copy the bits to the form field
	_, err = io.Copy(part, bits)
	if err != nil {
		return nil, fmt.Errorf("copying bits to form: %w", err)
	}

	// Close the writer to finalize the form
	err = writer.Close()
	if err != nil {
		return nil, fmt.Errorf("closing multipart writer: %w", err)
	}

	// Use PostRaw to send multipart form data
	resp, err := c.httpClient.PostRaw(ctx, path, buf.Bytes(), writer.FormDataContentType())
	if err != nil {
		return nil, fmt.Errorf("uploading buildpack bits: %w", err)
	}

	var buildpack capi.Buildpack

	err = json.Unmarshal(resp.Body, &buildpack)
	if err != nil {
		return nil, fmt.Errorf("parsing buildpack response: %w", err)
	}

	return &buildpack, nil
}
