package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

type snapshotsClient struct {
	httpClient *http.Client
}

// NewSnapshotsClient creates a new snapshots client.
func NewSnapshotsClient(httpClient *http.Client) boshapi.SnapshotsClient {
	return &snapshotsClient{httpClient: httpClient}
}

func (c *snapshotsClient) List(ctx context.Context, deployment string) ([]boshapi.Snapshot, error) {
	if err := checkPathSegments("Snapshots().List", "deployment", deployment); err != nil {
		return nil, err
	}

	path := "/deployments/" + url.PathEscape(deployment) + "/snapshots"

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("listing snapshots for %s: %w", deployment, err)
	}

	var snapshots []boshapi.Snapshot

	if err := json.Unmarshal(resp.Body, &snapshots); err != nil {
		return nil, fmt.Errorf("parsing snapshots: %w", err)
	}

	return snapshots, nil
}

func (c *snapshotsClient) ListForInstance(ctx context.Context, deployment, job string, index int) ([]boshapi.Snapshot, error) {
	if err := checkPathSegments("Snapshots().ListForInstance", "deployment", deployment, "job", job); err != nil {
		return nil, err
	}

	path := "/deployments/" + url.PathEscape(deployment) + "/jobs/" + url.PathEscape(job) + "/" + strconv.Itoa(index) + "/snapshots"

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("listing snapshots for %s/%s/%d: %w", deployment, job, index, err)
	}

	var snapshots []boshapi.Snapshot

	if err := json.Unmarshal(resp.Body, &snapshots); err != nil {
		return nil, fmt.Errorf("parsing instance snapshots: %w", err)
	}

	return snapshots, nil
}

func (c *snapshotsClient) Create(ctx context.Context, deployment string) (*boshapi.Task, error) {
	if err := checkPathSegments("Snapshots().Create", "deployment", deployment); err != nil {
		return nil, err
	}

	path := "/deployments/" + url.PathEscape(deployment) + "/snapshots"

	resp, err := c.httpClient.Post(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("creating snapshot for %s: %w", deployment, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing create snapshot task: %w", err)
	}

	return task, nil
}

func (c *snapshotsClient) CreateForInstance(ctx context.Context, deployment, job, indexOrID string) (*boshapi.Task, error) {
	if err := checkPathSegments("Snapshots().CreateForInstance", "deployment", deployment, "job", job, "indexOrID", indexOrID); err != nil {
		return nil, err
	}

	path := "/deployments/" + url.PathEscape(deployment) + "/jobs/" + url.PathEscape(job) + "/" + url.PathEscape(indexOrID) + "/snapshots"

	resp, err := c.httpClient.Post(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("creating snapshot for %s/%s/%s: %w", deployment, job, indexOrID, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing instance snapshot task: %w", err)
	}

	return task, nil
}

func (c *snapshotsClient) DeleteAll(ctx context.Context, deployment string) (*boshapi.Task, error) {
	if err := checkPathSegments("Snapshots().DeleteAll", "deployment", deployment); err != nil {
		return nil, err
	}

	path := "/deployments/" + url.PathEscape(deployment) + "/snapshots"

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting all snapshots for %s: %w", deployment, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing delete snapshots task: %w", err)
	}

	return task, nil
}

func (c *snapshotsClient) Delete(ctx context.Context, deployment, snapshotCID string) (*boshapi.Task, error) {
	if err := checkPathSegments("Snapshots().Delete", "deployment", deployment, "snapshotCID", snapshotCID); err != nil {
		return nil, err
	}

	path := "/deployments/" + url.PathEscape(deployment) + "/snapshots/" + url.PathEscape(snapshotCID)

	resp, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("deleting snapshot %s: %w", snapshotCID, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing delete snapshot task: %w", err)
	}

	return task, nil
}
