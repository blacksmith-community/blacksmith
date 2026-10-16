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
	if err := checkPathSegments("Deployments().Get", "name", name); err != nil {
		return nil, err
	}

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

func (c *deploymentsClient) CertificateExpiry(ctx context.Context, name string) ([]boshapi.DeploymentCertificateExpiry, error) {
	if err := checkPathSegments("Deployments().CertificateExpiry", "name", name); err != nil {
		return nil, err
	}

	path := "/deployments/" + url.PathEscape(name) + "/certificate_expiry"

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("getting certificate expiry for deployment %s: %w", name, err)
	}

	var certs []boshapi.DeploymentCertificateExpiry

	if err := json.Unmarshal(resp.Body, &certs); err != nil {
		return nil, fmt.Errorf("parsing certificate expiry: %w", err)
	}

	return certs, nil
}

func (c *deploymentsClient) Deploy(ctx context.Context, manifest string, opts *boshapi.DeployOptions) (*boshapi.Task, error) {
	if err := validateDeployOptions(opts); err != nil {
		return nil, err
	}

	var query url.Values

	if opts != nil {
		query = opts.ToValues()
	}

	// The director answers with a 302 or 303 whose Location holds the task ID.
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
	if err := checkPathSegments("Deployments().Delete", "name", name); err != nil {
		return nil, err
	}

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
	if err := checkPathSegments("Deployments().Diff", "name", name); err != nil {
		return nil, err
	}

	path := "/deployments/" + url.PathEscape(name) + "/diff"

	// The director reads the raw manifest from the body of a text/yaml
	// request.
	query := diffQuery(opts, true, false)

	resp, err := c.httpClient.PostYAMLDiff(ctx, path, manifest, query)
	if err != nil {
		return nil, fmt.Errorf("diffing deployment %s: %w", name, err)
	}
	// The director reports a diff it cannot compute with a 200 whose body
	// holds an error message, so the message is read here and never reaches
	// the caller as part of a diff.
	var answer struct {
		boshapi.DeploymentDiff
		Error string `json:"error"`
	}
	if err := json.Unmarshal(resp.Body, &answer); err != nil {
		return nil, fmt.Errorf("parsing deployment diff: %w", err)
	}
	if answer.Error != "" {
		return nil, &boshapi.DiffError{Target: "deployment " + name, Message: answer.Error}
	}

	return &answer.DeploymentDiff, nil
}
