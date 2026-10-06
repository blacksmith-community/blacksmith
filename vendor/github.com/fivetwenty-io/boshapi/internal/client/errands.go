package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

type errandsClient struct {
	httpClient *http.Client
}

// NewErrandsClient creates a new errands client.
func NewErrandsClient(httpClient *http.Client) boshapi.ErrandsClient {
	return &errandsClient{httpClient: httpClient}
}

func (c *errandsClient) List(ctx context.Context, deployment string) ([]boshapi.Errand, error) {
	path := "/deployments/" + url.PathEscape(deployment) + "/errands"

	resp, err := c.httpClient.Get(ctx, path, nil)
	if err != nil {
		return nil, fmt.Errorf("listing errands for %s: %w", deployment, err)
	}

	var errands []boshapi.Errand

	if err := json.Unmarshal(resp.Body, &errands); err != nil {
		return nil, fmt.Errorf("parsing errands: %w", err)
	}

	return errands, nil
}

func (c *errandsClient) Run(ctx context.Context, deployment, errandName string, opts *boshapi.RunErrandOptions) (*boshapi.Task, error) {
	path := "/deployments/" + url.PathEscape(deployment) + "/errands/" + url.PathEscape(errandName) + "/runs"

	// The director raises a NoMethodError and answers 500 with an empty body
	// when the request body is JSON null or lacks "instances", so the body
	// is always an object that carries all three keys.
	body := boshapi.RunErrandOptions{}

	if opts != nil {
		body = *opts
	}

	if body.Instances == nil {
		body.Instances = []boshapi.ErrandInstance{}
	}

	resp, err := c.httpClient.Post(ctx, path, body)
	if err != nil {
		return nil, fmt.Errorf("running errand %s on %s: %w", errandName, deployment, err)
	}

	task, err := resp.RequireTask()
	if err != nil {
		return nil, fmt.Errorf("parsing errand task: %w", err)
	}

	return task, nil
}
