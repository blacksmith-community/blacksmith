package client

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

type resourcesClient struct {
	httpClient *http.Client
}

// NewResourcesClient creates a new resources client.
func NewResourcesClient(httpClient *http.Client) boshapi.ResourcesClient {
	return &resourcesClient{httpClient: httpClient}
}

// Download streams the blobstore resource with the given ID. It is a plain
// read, so an answer that comes before the body is retried under the read
// rules, and nothing is retried once the caller holds the body. GET
// /resources/{id} starts no director task, so it is not a task-creating read.
func (c *resourcesClient) Download(ctx context.Context, blobstoreID string) (io.ReadCloser, error) {
	if strings.TrimSpace(blobstoreID) == "" {
		return nil, fmt.Errorf("downloading a blobstore resource: %w; nothing was sent. Pass the blobstore ID "+
			"that the director returned, such as the one in the result of a log-fetch task", boshapi.ErrBlobstoreIDRequired)
	}

	if blobstoreID == "." || blobstoreID == ".." {
		return nil, fmt.Errorf("downloading blobstore resource %q: a path segment of dots is read as the current "+
			"or the parent path, so GET /resources/%s would not reach a resource, and nothing was sent. "+
			"Pass the blobstore ID that the director returned, which is never %q", blobstoreID, blobstoreID, blobstoreID)
	}

	body, err := c.httpClient.GetStream(ctx, "/resources/"+url.PathEscape(blobstoreID), nil)
	if err != nil {
		return nil, fmt.Errorf("downloading blobstore resource %s: %w", blobstoreID, err)
	}

	return body, nil
}
