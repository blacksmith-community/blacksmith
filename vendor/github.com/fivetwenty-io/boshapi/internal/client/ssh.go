package client

import (
	"context"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// SSH client.
type sshClient struct {
	httpClient *http.Client
}

// NewSSHClient creates a new SSH client.
func NewSSHClient(httpClient *http.Client) boshapi.SSHClient {
	return &sshClient{httpClient: httpClient}
}

func (c *sshClient) Setup(ctx context.Context, deployment string, req *boshapi.SSHRequest) (*boshapi.Task, error) {
	return c.run(ctx, "setting up", deployment, req)
}

func (c *sshClient) Cleanup(ctx context.Context, deployment string, req *boshapi.SSHRequest) (*boshapi.Task, error) {
	return c.run(ctx, "cleaning up", deployment, req)
}

// run posts an SSH request to the director's SSH route and returns the task
// the director started. The action names the operation in any error.
func (c *sshClient) run(ctx context.Context, action, deployment string, req *boshapi.SSHRequest) (*boshapi.Task, error) {
	path := "/deployments/" + url.PathEscape(deployment) + "/ssh"

	resp, err := c.httpClient.Post(ctx, path, req)
	if err != nil {
		return nil, fmt.Errorf("%s SSH for %s: %w", action, deployment, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing SSH task for %s: %w", deployment, err)
	}

	return task, nil
}
