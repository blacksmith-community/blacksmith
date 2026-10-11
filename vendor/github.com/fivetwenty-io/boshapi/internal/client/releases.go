package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"net/url"

	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// tarballContentType is the content type bosh-cli sends with a local release
// or stemcell tarball.
const tarballContentType = "application/x-compressed"

// errJobCount reports that GET /jobs answered with other than the one job
// that a release name, job name, and fingerprint select.
var errJobCount = errors.New("the director sends exactly one job")

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
	if err := checkPathSegments("Releases().Get", "name", name); err != nil {
		return nil, err
	}

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

func (c *releasesClient) GetJob(ctx context.Context, releaseName, jobName, fingerprint string) (*boshapi.ReleaseJobDetail, error) {
	if releaseName == "" || jobName == "" || fingerprint == "" {
		return nil, fmt.Errorf("getting release job: %w; nothing was sent. Pass the release name, the job name, "+
			"and the job's fingerprint", boshapi.ErrJobSelectorRequired)
	}

	query := url.Values{
		"name":         {jobName},
		"release_name": {releaseName},
		"fingerprint":  {fingerprint},
	}

	resp, err := c.httpClient.Get(ctx, "/jobs", query)
	if err != nil {
		var apiErr *boshapi.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == nethttp.StatusNotFound {
			return nil, &boshapi.ReleaseJobNotFoundError{Release: releaseName, Name: jobName, Fingerprint: fingerprint}
		}

		return nil, fmt.Errorf("getting job %s of release %s: %w", jobName, releaseName, err)
	}

	var jobs []boshapi.ReleaseJobDetail

	if err := json.Unmarshal(resp.Body, &jobs); err != nil {
		return nil, fmt.Errorf("parsing job: %w", err)
	}

	if len(jobs) != 1 {
		return nil, fmt.Errorf("parsing job: %w: the director answered GET /jobs with %d jobs", errJobCount, len(jobs))
	}

	return &jobs[0], nil
}

func (c *releasesClient) Upload(ctx context.Context, opts *boshapi.UploadReleaseOptions) (*boshapi.Task, error) {
	if opts == nil || opts.URL == "" {
		return nil, fmt.Errorf("uploading release: %w; nothing was sent. Pass the URL of the tarball, or use "+
			"UploadFile to send one from disk", boshapi.ErrUploadLocationRequired)
	}

	// The director reads rebase and fix from the query and location and
	// sha1 from the JSON body.
	body := map[string]string{"location": opts.URL}
	query := url.Values{}

	if opts.SHA1 != "" {
		body["sha1"] = opts.SHA1
	}

	if opts.Rebase {
		query.Set("rebase", "true")
	}

	if opts.Fix {
		query.Set("fix", "true")
	}

	resp, err := c.httpClient.PostWithQuery(ctx, "/releases", body, query)
	if err != nil {
		return nil, fmt.Errorf("uploading release: %w", err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing upload task: %w", err)
	}

	return task, nil
}

func (c *releasesClient) UploadFile(ctx context.Context, opts *boshapi.UploadReleaseFileOptions) (*boshapi.Task, error) {
	if opts == nil || opts.Body == nil || opts.Size <= 0 {
		return nil, fmt.Errorf("uploading release file: %w; nothing was sent. Pass the tarball as Body "+
			"and its length in bytes as Size", boshapi.ErrUploadFileInvalid)
	}

	// The director reads rebase and fix from the query. Its nginx takes the
	// tarball as a raw body with this type and hands it on as a file, so
	// there is no JSON body and no SHA-1.
	query := url.Values{}

	if opts.Rebase {
		query.Set("rebase", "true")
	}

	if opts.Fix {
		query.Set("fix", "true")
	}

	resp, err := c.httpClient.PostStream(ctx, "/releases", query, tarballContentType, opts.Body, opts.Size)
	if err != nil {
		return nil, fmt.Errorf("uploading release file: %w", err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing upload task: %w", err)
	}

	return task, nil
}

func (c *releasesClient) Delete(ctx context.Context, name string, opts *boshapi.DeleteReleaseOptions) (*boshapi.Task, error) {
	if err := checkPathSegments("Releases().Delete", "name", name); err != nil {
		return nil, err
	}

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
	if req == nil {
		return nil, boshapi.ErrExportRequestRequired
	}

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
