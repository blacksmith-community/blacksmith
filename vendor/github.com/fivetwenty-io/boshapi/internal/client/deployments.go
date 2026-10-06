package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// validateRFC3339 checks that a recreate_vms_created_before value parses as
// an RFC 3339 timestamp, mirroring the director's server-side validation.
func validateRFC3339(timestamp string) error {
	if _, err := time.Parse(time.RFC3339, timestamp); err != nil {
		return fmt.Errorf("invalid RFC 3339 timestamp for recreate_vms_created_before: %w", err)
	}

	return nil
}

// validateDeployOptions applies the director's constraints on
// recreate_vms_created_before before any request is sent.
func validateDeployOptions(opts *boshapi.DeployOptions) error {
	if opts == nil || opts.RecreateVMsCreatedBefore == "" {
		return nil
	}

	if !opts.Recreate {
		return boshapi.ErrRecreateVMsCreatedBeforeRequiresRecreate
	}

	return validateRFC3339(opts.RecreateVMsCreatedBefore)
}

type deploymentsClient struct {
	httpClient *http.Client
}

func NewDeploymentsClient(httpClient *http.Client) boshapi.DeploymentsClient {
	return &deploymentsClient{httpClient: httpClient}
}

func (c *deploymentsClient) List(ctx context.Context, opts *boshapi.DeploymentListOptions) ([]boshapi.Deployment, error) {
	var query url.Values
	if opts != nil {
		query = opts.ToValues()
	}
	resp, err := c.httpClient.Get(ctx, "/deployments", query)
	if err != nil {
		return nil, fmt.Errorf("listing deployments: %w", err)
	}
	var deployments []boshapi.Deployment
	if err := json.Unmarshal(resp.Body, &deployments); err != nil {
		return nil, fmt.Errorf("parsing deployments: %w", err)
	}

	return deployments, nil
}

func (c *deploymentsClient) Get(ctx context.Context, name string) (*boshapi.DeploymentManifest, error) {
	path := "/deployments/" + url.PathEscape(name)
	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting deployment %s: %w", name, err)
	}
	var manifest boshapi.DeploymentManifest
	if err := json.Unmarshal(resp.Body, &manifest); err != nil {
		return nil, fmt.Errorf("parsing deployment manifest: %w", err)
	}

	return &manifest, nil
}

func (c *deploymentsClient) Deploy(ctx context.Context, manifest string, opts *boshapi.DeployOptions) (*boshapi.Task, error) {
	if err := validateDeployOptions(opts); err != nil {
		return nil, err
	}

	var query url.Values

	if opts != nil {
		query = opts.ToValues()
	}

	// The director answers with a 302 whose Location holds the task ID.
	resp, err := c.httpClient.PostYAML(ctx, "/deployments", manifest, query)
	if err != nil {
		return nil, fmt.Errorf("deploying: %w", err)
	}
	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing deploy task: %w", err)
	}

	return task, nil
}

func (c *deploymentsClient) Delete(ctx context.Context, name string, opts *boshapi.DeleteDeploymentOptions) (*boshapi.Task, error) {
	path := "/deployments/" + url.PathEscape(name)
	var query url.Values
	if opts != nil && opts.Force {
		query = url.Values{"force": {"true"}}
	}
	resp, err := c.httpClient.DeleteWithQuery(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("deleting deployment %s: %w", name, err)
	}
	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing delete task: %w", err)
	}

	return task, nil
}

func (c *deploymentsClient) Diff(ctx context.Context, name, manifest string, opts *boshapi.DiffOptions) (*boshapi.DeploymentDiff, error) {
	path := "/deployments/" + url.PathEscape(name) + "/diff"
	body := map[string]interface{}{manifestBodyKey: manifest}
	if opts != nil {
		body["redact"] = opts.Redact
	}
	resp, err := c.httpClient.Post(ctx, path, body)
	if err != nil {
		return nil, fmt.Errorf("diffing deployment %s: %w", name, err)
	}
	var diff boshapi.DeploymentDiff
	if err := json.Unmarshal(resp.Body, &diff); err != nil {
		return nil, fmt.Errorf("parsing deployment diff: %w", err)
	}

	return &diff, nil
}
