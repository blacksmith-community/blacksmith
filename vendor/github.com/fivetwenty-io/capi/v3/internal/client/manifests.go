package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	internalhttp "github.com/fivetwenty-io/capi/v3/internal/http"
	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// ManifestsClient implements capi.ManifestsClient.
// Static errors for err113 compliance.
var (
	ErrSpaceGUIDRequired       = errors.New("space GUID is required")
	ErrAppGUIDRequired         = errors.New("app GUID is required")
	ErrManifestContentRequired = errors.New("manifest content is required")
	ErrNoJobLocationReturned   = errors.New("no job location returned")
	ErrAPIError                = errors.New("API error")
	ErrAPIStatusError          = errors.New("API status error")
)

type ManifestsClient struct {
	httpClient *internalhttp.Client
}

// NewManifestsClient creates a new ManifestsClient.
func NewManifestsClient(httpClient *internalhttp.Client) *ManifestsClient {
	return &ManifestsClient{
		httpClient: httpClient,
	}
}

// ApplyManifest applies a manifest to a space.
func (c *ManifestsClient) ApplyManifest(ctx context.Context, spaceGUID string, manifest []byte) (*capi.Job, error) {
	if spaceGUID == "" {
		return nil, ErrSpaceGUIDRequired
	}

	if len(manifest) == 0 {
		return nil, ErrManifestContentRequired
	}

	url := fmt.Sprintf("/v3/spaces/%s/actions/apply_manifest", spaceGUID)

	// Create request with YAML content type
	req := &internalhttp.Request{
		Method: http.MethodPost,
		Path:   url,
		Body:   manifest,
		Headers: map[string]string{
			"Content-Type": "application/x-yaml",
		},
	}

	resp, err := c.httpClient.Do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to apply manifest: %w", err)
	}

	if resp.StatusCode != http.StatusAccepted {
		return nil, parseErrorResponse(resp)
	}

	// Get job location from header
	jobLocation := resp.Headers.Get("Location")
	if jobLocation == "" {
		return nil, ErrNoJobLocationReturned
	}

	// Parse job ID from location
	var jobID string

	_, err = fmt.Sscanf(jobLocation, "/v3/jobs/%s", &jobID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse job location: %w", err)
	}

	// Return a minimal job object with the ID
	return &capi.Job{
		Resource: capi.Resource{
			GUID: jobID,
		},
		State: "PROCESSING",
	}, nil
}

// GenerateManifest generates a manifest for an app.
func (c *ManifestsClient) GenerateManifest(ctx context.Context, appGUID string) ([]byte, error) {
	if appGUID == "" {
		return nil, ErrAppGUIDRequired
	}

	url := fmt.Sprintf("/v3/apps/%s/manifest", appGUID)

	resp, err := c.httpClient.Get(ctx, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to generate manifest: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, parseErrorResponse(resp)
	}

	// The response body is the YAML manifest
	return resp.Body, nil
}

// CreateManifestDiff creates a diff between current and proposed manifest.
func (c *ManifestsClient) CreateManifestDiff(ctx context.Context, spaceGUID string, manifest []byte) (*capi.ManifestDiff, error) {
	if spaceGUID == "" {
		return nil, ErrSpaceGUIDRequired
	}

	if len(manifest) == 0 {
		return nil, ErrManifestContentRequired
	}

	url := fmt.Sprintf("/v3/spaces/%s/manifest_diff", spaceGUID)

	// Create request with YAML content type
	req := &internalhttp.Request{
		Method: http.MethodPost,
		Path:   url,
		Body:   manifest,
		Headers: map[string]string{
			"Content-Type": "application/x-yaml",
		},
	}

	resp, err := c.httpClient.Do(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create manifest diff: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return nil, parseErrorResponse(resp)
	}

	var diffResponse capi.ManifestDiffResponse

	err = json.Unmarshal(resp.Body, &diffResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to decode manifest diff: %w", err)
	}

	return &capi.ManifestDiff{
		Diff: diffResponse.Diff,
	}, nil
}

// parseErrorResponse handles non-success HTTP responses.
func parseErrorResponse(resp *internalhttp.Response) error {
	var apiError struct {
		Errors []struct {
			Code   int    `json:"code"`
			Title  string `json:"title"`
			Detail string `json:"detail"`
		} `json:"errors"`
	}

	err := json.Unmarshal(resp.Body, &apiError)
	if err == nil && len(apiError.Errors) > 0 {
		return fmt.Errorf("%s: %s: %w", apiError.Errors[0].Title, apiError.Errors[0].Detail, ErrAPIError)
	}

	return fmt.Errorf("status %d - %s: %w", resp.StatusCode, string(resp.Body), ErrAPIStatusError)
}
