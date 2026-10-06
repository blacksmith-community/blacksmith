package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

type instancesClient struct {
	httpClient *http.Client
}

// NewInstancesClient creates a new instances client.
func NewInstancesClient(httpClient *http.Client) boshapi.InstancesClient {
	return &instancesClient{httpClient: httpClient}
}

// validateJobStateOptions applies the director's constraints on
// recreate_vms_created_before before any request is sent.
func validateJobStateOptions(opts *boshapi.UpdateStateOptions) error {
	if opts == nil || opts.RecreateVMsCreatedBefore == "" {
		return nil
	}

	if opts.NewState != "recreate" {
		return boshapi.ErrRecreateVMsCreatedBeforeRequiresRecreateState
	}

	return validateRFC3339(opts.RecreateVMsCreatedBefore)
}

// wildcardSegment is the path segment the director and bosh-cli use for
// "every job" and "every instance".
const wildcardSegment = "*"

// jobPathSegment escapes one path segment, except that a segment which is
// exactly "*" is sent as a literal "*" the way bosh-cli sends it. url.PathEscape
// would turn it into "%2A".
func jobPathSegment(segment string) string {
	if segment == wildcardSegment {
		return segment
	}

	return url.PathEscape(segment)
}

func (c *instancesClient) List(ctx context.Context, deployment string) ([]boshapi.Instance, error) {
	path := "/deployments/" + url.PathEscape(deployment) + "/instances"

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("listing instances for %s: %w", deployment, err)
	}

	var instances []boshapi.Instance

	if err := json.Unmarshal(resp.Body, &instances); err != nil {
		return nil, fmt.Errorf("parsing instances: %w", err)
	}

	return instances, nil
}

func (c *instancesClient) ListDetailed(ctx context.Context, deployment string) (*boshapi.Task, error) {
	path := "/deployments/" + url.PathEscape(deployment) + "/instances"
	query := url.Values{"format": {"full"}}

	resp, err := c.httpClient.GetStartingTask(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("listing detailed instances for %s: %w", deployment, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing instances task: %w", err)
	}

	return task, nil
}

func (c *instancesClient) UpdateState(ctx context.Context, deployment, job, indexOrID string, opts *boshapi.UpdateStateOptions) (*boshapi.Task, error) {
	var query url.Values

	if opts != nil {
		// The director only accepts recreate_vms_created_before on the
		// job-level route; reject it here instead of letting it be ignored.
		if opts.RecreateVMsCreatedBefore != "" {
			return nil, boshapi.ErrRecreateVMsCreatedBeforeJobLevelOnly
		}

		query = opts.ToValues()
	}

	path := "/deployments/" + url.PathEscape(deployment) + "/jobs/" + jobPathSegment(job) + "/" + jobPathSegment(indexOrID)

	// State changes send options as query parameters with an empty YAML
	// body; the director then applies the change to the stored manifest.
	resp, err := c.httpClient.PutYAML(ctx, path, "", query)
	if err != nil {
		return nil, fmt.Errorf("updating state for %s/%s/%s: %w", deployment, job, indexOrID, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing update state task: %w", err)
	}

	return task, nil
}

func (c *instancesClient) UpdateJobState(ctx context.Context, deployment, job string, opts *boshapi.UpdateStateOptions) (*boshapi.Task, error) {
	if err := validateJobStateOptions(opts); err != nil {
		return nil, err
	}

	var query url.Values

	if opts != nil {
		query = opts.ToValues()
	}

	path := "/deployments/" + url.PathEscape(deployment) + "/jobs/" + jobPathSegment(job)

	resp, err := c.httpClient.PutYAML(ctx, path, "", query)
	if err != nil {
		return nil, fmt.Errorf("updating job state for %s/%s: %w", deployment, job, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing update job state task: %w", err)
	}

	return task, nil
}

func (c *instancesClient) Stop(ctx context.Context, deployment, job, indexOrID string, opts *boshapi.UpdateStateOptions) (*boshapi.Task, error) {
	if opts == nil {
		opts = &boshapi.UpdateStateOptions{}
	}

	opts.NewState = "stopped"

	return c.UpdateState(ctx, deployment, job, indexOrID, opts)
}

func (c *instancesClient) Start(ctx context.Context, deployment, job, indexOrID string, opts *boshapi.UpdateStateOptions) (*boshapi.Task, error) {
	if opts == nil {
		opts = &boshapi.UpdateStateOptions{}
	}

	opts.NewState = "started"

	return c.UpdateState(ctx, deployment, job, indexOrID, opts)
}

func (c *instancesClient) Restart(ctx context.Context, deployment, job, indexOrID string, opts *boshapi.UpdateStateOptions) (*boshapi.Task, error) {
	if opts == nil {
		opts = &boshapi.UpdateStateOptions{}
	}

	opts.NewState = "restart"

	return c.UpdateState(ctx, deployment, job, indexOrID, opts)
}

func (c *instancesClient) Recreate(ctx context.Context, deployment, job, indexOrID string, opts *boshapi.UpdateStateOptions) (*boshapi.Task, error) {
	if opts == nil {
		opts = &boshapi.UpdateStateOptions{}
	}

	opts.NewState = "recreate"

	return c.UpdateState(ctx, deployment, job, indexOrID, opts)
}

// postInstanceAction posts to a per-instance action route
// (/deployments/{d}/instance_groups/{ig}/{id}/actions/{action}) and parses
// the task ID from the 302 redirect.
func (c *instancesClient) postInstanceAction(ctx context.Context, deployment, instanceGroup, indexOrID, action string, query url.Values) (*boshapi.Task, error) {
	path := "/deployments/" + url.PathEscape(deployment) + "/instance_groups/" + url.PathEscape(instanceGroup) + "/" + url.PathEscape(indexOrID) + "/actions/" + url.PathEscape(action)

	resp, err := c.httpClient.PostWithQuery(ctx, path, nil, query)
	if err != nil {
		return nil, fmt.Errorf("%s instance %s/%s/%s: %w", action, deployment, instanceGroup, indexOrID, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing %s instance task: %w", action, err)
	}

	return task, nil
}

func (c *instancesClient) StopInstance(ctx context.Context, deployment, instanceGroup, indexOrID string, opts *boshapi.StopInstanceOptions) (*boshapi.Task, error) {
	var query url.Values

	if opts != nil {
		query = opts.ToValues()
	}

	return c.postInstanceAction(ctx, deployment, instanceGroup, indexOrID, "stop", query)
}

func (c *instancesClient) StartInstance(ctx context.Context, deployment, instanceGroup, indexOrID string) (*boshapi.Task, error) {
	return c.postInstanceAction(ctx, deployment, instanceGroup, indexOrID, "start", nil)
}

func (c *instancesClient) RestartInstance(ctx context.Context, deployment, instanceGroup, indexOrID string, opts *boshapi.RestartInstanceOptions) (*boshapi.Task, error) {
	var query url.Values

	if opts != nil {
		query = opts.ToValues()
	}

	return c.postInstanceAction(ctx, deployment, instanceGroup, indexOrID, "restart", query)
}

func (c *instancesClient) RecreateInstance(ctx context.Context, deployment, instanceGroup, indexOrID string, opts *boshapi.RecreateInstanceOptions) (*boshapi.Task, error) {
	var query url.Values

	if opts != nil {
		query = opts.ToValues()
	}

	return c.postInstanceAction(ctx, deployment, instanceGroup, indexOrID, "recreate", query)
}

func (c *instancesClient) Ignore(ctx context.Context, deployment, instanceGroup, indexOrID string, ignore bool) error {
	path := "/deployments/" + url.PathEscape(deployment) + "/instance_groups/" + url.PathEscape(instanceGroup) + "/" + url.PathEscape(indexOrID) + "/ignore"

	body := map[string]bool{"ignore": ignore}

	_, err := c.httpClient.Put(ctx, path, body)
	if err != nil {
		return fmt.Errorf("ignoring instance %s/%s/%s: %w", deployment, instanceGroup, indexOrID, err)
	}

	return nil
}

func (c *instancesClient) FetchLogs(ctx context.Context, deployment, job, indexOrID string, opts *boshapi.LogOptions) (*boshapi.Task, error) {
	path := "/deployments/" + url.PathEscape(deployment) + "/jobs/" + jobPathSegment(job) + "/" + jobPathSegment(indexOrID) + "/logs"

	query := url.Values{}

	if opts != nil {
		if opts.Type != "" {
			query.Set("type", opts.Type)
		}
	}

	resp, err := c.httpClient.GetStartingTask(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("fetching logs for %s/%s/%s: %w", deployment, job, indexOrID, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing logs task: %w", err)
	}

	return task, nil
}
