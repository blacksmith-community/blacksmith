package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

type releasesClient struct {
	httpClient *http.Client
}

// NewReleasesClient creates a new releases client.
func NewReleasesClient(httpClient *http.Client) boshapi.ReleasesClient {
	return &releasesClient{httpClient: httpClient}
}

func (c *releasesClient) List(ctx context.Context) ([]boshapi.Release, error) {
	resp, err := c.httpClient.Get(ctx, "/releases", nil)
	if err != nil {
		return nil, fmt.Errorf("listing releases: %w", err)
	}

	var releases []boshapi.Release

	if err := json.Unmarshal(resp.Body, &releases); err != nil {
		return nil, fmt.Errorf("parsing releases: %w", err)
	}

	return releases, nil
}

func (c *releasesClient) Get(ctx context.Context, name string, opts *boshapi.ReleaseGetOptions) (*boshapi.ReleaseDetail, error) {
	path := "/releases/" + url.PathEscape(name)

	var query url.Values

	if opts != nil && opts.Version != "" {
		query = url.Values{"version": {opts.Version}}
	}

	resp, err := c.httpClient.Get(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("getting release %s: %w", name, err)
	}

	var release boshapi.ReleaseDetail

	if err := json.Unmarshal(resp.Body, &release); err != nil {
		return nil, fmt.Errorf("parsing release: %w", err)
	}

	return &release, nil
}

func (c *releasesClient) Upload(ctx context.Context, opts *boshapi.UploadReleaseOptions) (*boshapi.Task, error) {
	resp, err := c.httpClient.Post(ctx, "/releases", opts)
	if err != nil {
		return nil, fmt.Errorf("uploading release: %w", err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing upload task: %w", err)
	}

	return task, nil
}

func (c *releasesClient) Delete(ctx context.Context, name string, opts *boshapi.DeleteReleaseOptions) (*boshapi.Task, error) {
	path := "/releases/" + url.PathEscape(name)

	query := url.Values{}

	if opts != nil {
		if opts.Version != "" {
			query.Set("version", opts.Version)
		}

		if opts.Force {
			query.Set("force", "true")
		}
	}

	resp, err := c.httpClient.DeleteWithQuery(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("deleting release %s: %w", name, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing delete task: %w", err)
	}

	return task, nil
}

func (c *releasesClient) ExportRelease(ctx context.Context, req *boshapi.ExportReleaseRequest) (*boshapi.Task, error) {
	resp, err := c.httpClient.Post(ctx, "/releases/export", req)
	if err != nil {
		return nil, fmt.Errorf("exporting release: %w", err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing export task: %w", err)
	}

	return task, nil
}
