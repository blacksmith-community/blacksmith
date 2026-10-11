package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

type disksClient struct {
	httpClient *http.Client
}

// NewDisksClient creates a new disks client.
func NewDisksClient(httpClient *http.Client) boshapi.DisksClient {
	return &disksClient{httpClient: httpClient}
}

func (c *disksClient) ListOrphanDisks(ctx context.Context) ([]boshapi.OrphanDisk, error) {
	resp, err := c.httpClient.Get(ctx, "/orphan_disks", nil)
	if err != nil {
		return nil, fmt.Errorf("listing orphan disks: %w", err)
	}

	var disks []boshapi.OrphanDisk

	if err := json.Unmarshal(resp.Body, &disks); err != nil {
		return nil, fmt.Errorf("parsing orphan disks: %w", err)
	}

	return disks, nil
}

func (c *disksClient) DeleteOrphanDisk(ctx context.Context, diskCID string) (*boshapi.Task, error) {
	if err := checkPathSegments("Disks().DeleteOrphanDisk", "diskCID", diskCID); err != nil {
		return nil, err
	}

	path := "/orphan_disks/" + url.PathEscape(diskCID)

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting orphan disk %s: %w", diskCID, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing delete disk task: %w", err)
	}

	return task, nil
}

func (c *disksClient) AttachDisk(ctx context.Context, opts *boshapi.AttachDiskOptions) (*boshapi.Task, error) {
	if opts == nil || opts.DiskCID == "" || opts.Deployment == "" || opts.Job == "" || opts.InstanceID == "" {
		return nil, fmt.Errorf("attaching disk: %w; nothing was sent. Pass the disk CID, the deployment, the job, "+
			"and the instance ID", boshapi.ErrAttachDiskOptionsInvalid)
	}

	if err := checkPathSegments("Disks().AttachDisk", "disk CID", opts.DiskCID); err != nil {
		return nil, err
	}

	path := "/disks/" + url.PathEscape(opts.DiskCID) + "/attachments"

	query := url.Values{
		"deployment":  {opts.Deployment},
		"job":         {opts.Job},
		"instance_id": {opts.InstanceID},
	}
	if opts.DiskProperties != "" {
		query.Set("disk_properties", opts.DiskProperties)
	}

	resp, err := c.httpClient.PutWithQuery(ctx, path, nil, query)
	if err != nil {
		return nil, fmt.Errorf("attaching disk: %w", err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing attach disk task: %w", err)
	}

	return task, nil
}
