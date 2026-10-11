package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

type stemcellsClient struct {
	httpClient *http.Client
}

// NewStemcellsClient creates a new stemcells client.
func NewStemcellsClient(httpClient *http.Client) boshapi.StemcellsClient {
	return &stemcellsClient{httpClient: httpClient}
}

func (c *stemcellsClient) List(ctx context.Context) ([]boshapi.Stemcell, error) {
	resp, err := c.httpClient.Get(ctx, "/stemcells", nil)
	if err != nil {
		return nil, fmt.Errorf("listing stemcells: %w", err)
	}

	var stemcells []boshapi.Stemcell

	if err := json.Unmarshal(resp.Body, &stemcells); err != nil {
		return nil, fmt.Errorf("parsing stemcells: %w", err)
	}

	return stemcells, nil
}

func (c *stemcellsClient) Upload(ctx context.Context, opts *boshapi.UploadStemcellOptions) (*boshapi.Task, error) {
	if opts == nil || opts.URL == "" {
		return nil, fmt.Errorf("uploading stemcell: %w; nothing was sent. Pass the URL of the tarball, or use "+
			"UploadFile to send one from disk", boshapi.ErrUploadLocationRequired)
	}

	// The director reads fix from the query and location and sha1 from
	// the JSON body.
	body := map[string]string{"location": opts.URL}
	query := url.Values{}

	if opts.SHA1 != "" {
		body["sha1"] = opts.SHA1
	}

	if opts.Fix {
		query.Set("fix", "true")
	}

	resp, err := c.httpClient.PostWithQuery(ctx, "/stemcells", body, query)
	if err != nil {
		return nil, fmt.Errorf("uploading stemcell: %w", err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing upload task: %w", err)
	}

	return task, nil
}

func (c *stemcellsClient) UploadFile(ctx context.Context, opts *boshapi.UploadStemcellFileOptions) (*boshapi.Task, error) {
	if opts == nil || opts.Body == nil || opts.Size <= 0 {
		return nil, fmt.Errorf("uploading stemcell file: %w; nothing was sent. Pass the tarball as Body "+
			"and its length in bytes as Size", boshapi.ErrUploadFileInvalid)
	}

	// The director reads fix from the query. Its nginx takes the tarball as
	// a raw body with this type and hands it on as a file, so there is no
	// JSON body.
	query := url.Values{}

	if opts.Fix {
		query.Set("fix", "true")
	}

	resp, err := c.httpClient.PostStream(ctx, "/stemcells", query, tarballContentType, opts.Body, opts.Size)
	if err != nil {
		return nil, fmt.Errorf("uploading stemcell file: %w", err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing upload task: %w", err)
	}

	return task, nil
}

func (c *stemcellsClient) Delete(ctx context.Context, name, version string, opts *boshapi.DeleteStemcellOptions) (*boshapi.Task, error) {
	if err := checkPathSegments("Stemcells().Delete", "name", name, "version", version); err != nil {
		return nil, err
	}

	path := "/stemcells/" + url.PathEscape(name) + "/" + url.PathEscape(version)

	query := url.Values{}

	if opts != nil && opts.Force {
		query.Set("force", "true")
	}

	resp, err := c.httpClient.DeleteWithQuery(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("deleting stemcell %s/%s: %w", name, version, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing delete task: %w", err)
	}

	return task, nil
}

type stemcellUploadsClient struct {
	httpClient *http.Client
}

// NewStemcellUploadsClient creates a new stemcell uploads client.
func NewStemcellUploadsClient(httpClient *http.Client) boshapi.StemcellUploadsClient {
	return &stemcellUploadsClient{httpClient: httpClient}
}

func (c *stemcellUploadsClient) CheckUploadNeeded(ctx context.Context, name, version string) (bool, error) {
	body := &boshapi.StemcellUploadCheck{
		Name:    name,
		Version: version,
	}

	resp, err := c.httpClient.Post(ctx, "/stemcell_uploads", body)
	if err != nil {
		return false, fmt.Errorf("checking stemcell upload: %w", err)
	}

	var result boshapi.StemcellUploadNeeded

	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return false, fmt.Errorf("parsing stemcell upload check: %w", err)
	}

	return result.Needed, nil
}
