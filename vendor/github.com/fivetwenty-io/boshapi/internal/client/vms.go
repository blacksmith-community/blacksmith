package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

type vmsClient struct {
	httpClient *http.Client
}

// NewVMsClient creates a new VMs client.
func NewVMsClient(httpClient *http.Client) boshapi.VMsClient {
	return &vmsClient{httpClient: httpClient}
}

func (c *vmsClient) List(ctx context.Context, deployment string) ([]boshapi.VM, error) {
	if err := checkPathSegments("VMs().List", "deployment", deployment); err != nil {
		return nil, err
	}

	path := "/deployments/" + url.PathEscape(deployment) + "/vms"

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("listing VMs for %s: %w", deployment, err)
	}

	var vms []boshapi.VM

	if err := json.Unmarshal(resp.Body, &vms); err != nil {
		return nil, fmt.Errorf("parsing VMs: %w", err)
	}

	return vms, nil
}

func (c *vmsClient) ListDetailed(ctx context.Context, deployment string) (*boshapi.Task, error) {
	if err := checkPathSegments("VMs().ListDetailed", "deployment", deployment); err != nil {
		return nil, err
	}

	path := "/deployments/" + url.PathEscape(deployment) + "/vms"
	query := url.Values{"format": {"full"}}

	resp, err := c.httpClient.GetStartingTask(ctx, path, query)
	if err != nil {
		return nil, fmt.Errorf("listing detailed VMs for %s: %w", deployment, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing VMs task: %w", err)
	}

	return task, nil
}

func (c *vmsClient) DeleteVM(ctx context.Context, vmCID string) (*boshapi.Task, error) {
	if err := checkPathSegments("VMs().DeleteVM", "vmCID", vmCID); err != nil {
		return nil, err
	}

	path := "/vms/" + url.PathEscape(vmCID)

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting VM %s: %w", vmCID, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing delete VM task: %w", err)
	}

	return task, nil
}
