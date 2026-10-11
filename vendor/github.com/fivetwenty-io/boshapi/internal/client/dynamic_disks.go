package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

type dynamicDisksClient struct {
	httpClient *http.Client
}

// NewDynamicDisksClient creates a new dynamic disks client.
func NewDynamicDisksClient(httpClient *http.Client) boshapi.DynamicDisksClient {
	return &dynamicDisksClient{httpClient: httpClient}
}

func (c *dynamicDisksClient) List(ctx context.Context) ([]boshapi.DynamicDisk, error) {
	resp, err := c.httpClient.Get(ctx, "/dynamic_disks", nil)
	if err != nil {
		return nil, fmt.Errorf("listing dynamic disks: %w", err)
	}

	var disks []boshapi.DynamicDisk

	if err := json.Unmarshal(resp.Body, &disks); err != nil {
		return nil, fmt.Errorf("parsing dynamic disks: %w", err)
	}

	return disks, nil
}

// validateCreateRequest mirrors the director's required-field constraints
// (all names non-empty, disk size at least 1) so invalid requests fail
// before any request is sent.
func validateCreateRequest(req *boshapi.CreateDynamicDiskRequest) error {
	switch {
	case req == nil:
		return boshapi.ErrCreateRequestRequired
	case req.DeploymentName == "":
		return boshapi.ErrDeploymentNameRequired
	case req.AZ == "":
		return boshapi.ErrAZRequired
	case req.DiskName == "":
		return boshapi.ErrDiskNameRequired
	case req.DiskPoolName == "":
		return boshapi.ErrDiskPoolNameRequired
	case req.DiskSize < 1:
		return boshapi.ErrDiskSizeInvalid
	}

	return nil
}

func (c *dynamicDisksClient) Create(ctx context.Context, req *boshapi.CreateDynamicDiskRequest) (*boshapi.Task, error) {
	if err := validateCreateRequest(req); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Post(ctx, "/dynamic_disks", req)
	if err != nil {
		return nil, fmt.Errorf("creating dynamic disk: %w", err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing create dynamic disk task: %w", err)
	}

	return task, nil
}

func (c *dynamicDisksClient) Attach(ctx context.Context, diskName string, req *boshapi.AttachDynamicDiskRequest) (*boshapi.Task, error) {
	if diskName == "" {
		return nil, boshapi.ErrDiskNameRequired
	}

	if req == nil {
		return nil, boshapi.ErrAttachRequestRequired
	}

	if req.InstanceID == "" {
		return nil, boshapi.ErrInstanceIDRequired
	}

	if err := checkPathSegments("DynamicDisks().Attach", "diskName", diskName); err != nil {
		return nil, err
	}

	path := "/dynamic_disks/" + url.PathEscape(diskName) + "/attach"

	resp, err := c.httpClient.Post(ctx, path, req)
	if err != nil {
		return nil, fmt.Errorf("attaching dynamic disk %s: %w", diskName, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing attach dynamic disk task: %w", err)
	}

	return task, nil
}

// validateProvideRequest mirrors the director's required-field constraints
// (all names non-empty, disk size at least 1) so invalid requests fail
// before any request is sent.
func validateProvideRequest(req *boshapi.ProvideDynamicDiskRequest) error {
	switch {
	case req == nil:
		return boshapi.ErrProvideRequestRequired
	case req.InstanceID == "":
		return boshapi.ErrInstanceIDRequired
	case req.DiskName == "":
		return boshapi.ErrDiskNameRequired
	case req.DiskPoolName == "":
		return boshapi.ErrDiskPoolNameRequired
	case req.DiskSize < 1:
		return boshapi.ErrDiskSizeInvalid
	}

	return nil
}

func (c *dynamicDisksClient) Provide(ctx context.Context, req *boshapi.ProvideDynamicDiskRequest) (*boshapi.Task, error) {
	if err := validateProvideRequest(req); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Post(ctx, "/dynamic_disks/provide", req)
	if err != nil {
		return nil, fmt.Errorf("providing dynamic disk: %w", err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing provide dynamic disk task: %w", err)
	}

	return task, nil
}

func (c *dynamicDisksClient) Detach(ctx context.Context, diskName string) (*boshapi.Task, error) {
	if diskName == "" {
		return nil, boshapi.ErrDiskNameRequired
	}

	if err := checkPathSegments("DynamicDisks().Detach", "diskName", diskName); err != nil {
		return nil, err
	}

	path := "/dynamic_disks/" + url.PathEscape(diskName) + "/detach"

	resp, err := c.httpClient.Post(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("detaching dynamic disk %s: %w", diskName, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing detach dynamic disk task: %w", err)
	}

	return task, nil
}

func (c *dynamicDisksClient) Delete(ctx context.Context, diskName string) (*boshapi.Task, error) {
	if diskName == "" {
		return nil, boshapi.ErrDiskNameRequired
	}

	if err := checkPathSegments("DynamicDisks().Delete", "diskName", diskName); err != nil {
		return nil, err
	}

	path := "/dynamic_disks/" + url.PathEscape(diskName)

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting dynamic disk %s: %w", diskName, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing delete dynamic disk task: %w", err)
	}

	return task, nil
}
