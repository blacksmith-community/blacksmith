package client

import (
	"context"

	"github.com/fivetwenty-io/capi/v3/pkg/capi"
)

// Raw implements capi.Client.Raw.
func (c *Client) Raw(ctx context.Context, req capi.RawRequest) (*capi.RawResponse, error) {
	result, err := c.httpClient.DoRaw(ctx, req)
	if err != nil {
		return nil, err //nolint:wrapcheck // DoRaw already returns sentinel or contextual errors
	}

	return &capi.RawResponse{
		Status:    result.Status,
		Header:    result.Header,
		Body:      result.Body,
		Truncated: result.Truncated,
	}, nil
}
