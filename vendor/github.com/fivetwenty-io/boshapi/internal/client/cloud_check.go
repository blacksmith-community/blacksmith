package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

type cloudCheckClient struct {
	httpClient *http.Client
}

// NewCloudCheckClient creates a new cloud check client.
func NewCloudCheckClient(httpClient *http.Client) boshapi.CloudCheckClient {
	return &cloudCheckClient{httpClient: httpClient}
}

func (c *cloudCheckClient) Scan(ctx context.Context, deployment string) (*boshapi.Task, error) {
	path := "/deployments/" + url.PathEscape(deployment) + "/scans"

	resp, err := c.httpClient.Post(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("scanning deployment %s: %w", deployment, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing scan task: %w", err)
	}

	return task, nil
}

func (c *cloudCheckClient) ListProblems(ctx context.Context, deployment string) ([]boshapi.Problem, error) {
	path := "/deployments/" + url.PathEscape(deployment) + "/problems"

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("listing problems for %s: %w", deployment, err)
	}

	var problems []boshapi.Problem

	if err := json.Unmarshal(resp.Body, &problems); err != nil {
		return nil, fmt.Errorf("parsing problems: %w", err)
	}

	return problems, nil
}

func (c *cloudCheckClient) ResolveProblems(ctx context.Context, deployment string, req *boshapi.ResolveProblemsRequest) (*boshapi.Task, error) {
	path := "/deployments/" + url.PathEscape(deployment) + "/problems"

	resp, err := c.httpClient.Put(ctx, path, req)
	if err != nil {
		return nil, fmt.Errorf("resolving problems for %s: %w", deployment, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing resolve task: %w", err)
	}

	return task, nil
}

func (c *cloudCheckClient) ScanAndFix(ctx context.Context, deployment string, req *boshapi.ScanAndFixRequest) (*boshapi.Task, error) {
	path := "/deployments/" + url.PathEscape(deployment) + "/scan_and_fix"

	resp, err := c.httpClient.Put(ctx, path, req)
	if err != nil {
		return nil, fmt.Errorf("scan and fix for %s: %w", deployment, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing scan and fix task: %w", err)
	}

	return task, nil
}
