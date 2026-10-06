package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// deploymentQueryKey is the query parameter name used to scope link
// listings to a single deployment.
const deploymentQueryKey = "deployment"

type linksClient struct {
	httpClient *http.Client
}

// NewLinksClient creates a new links client.
func NewLinksClient(httpClient *http.Client) boshapi.LinksClient {
	return &linksClient{httpClient: httpClient}
}

func (c *linksClient) List(ctx context.Context, deployment string) ([]boshapi.Link, error) {
	query := url.Values{deploymentQueryKey: {deployment}}

	resp, err := c.httpClient.Get(ctx, "/links", query)
	if err != nil {
		return nil, fmt.Errorf("listing links for %s: %w", deployment, err)
	}

	var links []boshapi.Link

	if err := json.Unmarshal(resp.Body, &links); err != nil {
		return nil, fmt.Errorf("parsing links: %w", err)
	}

	return links, nil
}

func (c *linksClient) Create(ctx context.Context, req *boshapi.CreateLinkRequest) (*boshapi.Link, error) {
	resp, err := c.httpClient.Post(ctx, "/links", req)
	if err != nil {
		return nil, fmt.Errorf("creating link: %w", err)
	}

	var link boshapi.Link

	if err := json.Unmarshal(resp.Body, &link); err != nil {
		return nil, fmt.Errorf("parsing link: %w", err)
	}

	return &link, nil
}

func (c *linksClient) Delete(ctx context.Context, linkID string) error {
	path := "/links/" + url.PathEscape(linkID)

	_, err := c.httpClient.Delete(ctx, path)
	if err != nil {
		return fmt.Errorf("deleting link %s: %w", linkID, err)
	}

	return nil
}

func (c *linksClient) GetAddress(ctx context.Context, opts *boshapi.LinkAddressOptions) (*boshapi.LinkAddress, error) {
	query := url.Values{"link_id": {opts.LinkID}}

	if len(opts.AZs) > 0 {
		query.Set("azs", strings.Join(opts.AZs, ","))
	}

	if opts.Status != "" {
		query.Set("status", opts.Status)
	}

	resp, err := c.httpClient.Get(ctx, "/link_address", query)
	if err != nil {
		return nil, fmt.Errorf("getting link address: %w", err)
	}

	var address boshapi.LinkAddress

	if err := json.Unmarshal(resp.Body, &address); err != nil {
		return nil, fmt.Errorf("parsing link address: %w", err)
	}

	return &address, nil
}

type linkProvidersClient struct {
	httpClient *http.Client
}

// NewLinkProvidersClient creates a new link providers client.
func NewLinkProvidersClient(httpClient *http.Client) boshapi.LinkProvidersClient {
	return &linkProvidersClient{httpClient: httpClient}
}

func (c *linkProvidersClient) List(ctx context.Context, deployment string) ([]boshapi.LinkProvider, error) {
	query := url.Values{deploymentQueryKey: {deployment}}

	resp, err := c.httpClient.Get(ctx, "/link_providers", query)
	if err != nil {
		return nil, fmt.Errorf("listing link providers for %s: %w", deployment, err)
	}

	var providers []boshapi.LinkProvider

	if err := json.Unmarshal(resp.Body, &providers); err != nil {
		return nil, fmt.Errorf("parsing link providers: %w", err)
	}

	return providers, nil
}

type linkConsumersClient struct {
	httpClient *http.Client
}

// NewLinkConsumersClient creates a new link consumers client.
func NewLinkConsumersClient(httpClient *http.Client) boshapi.LinkConsumersClient {
	return &linkConsumersClient{httpClient: httpClient}
}

func (c *linkConsumersClient) List(ctx context.Context, deployment string) ([]boshapi.LinkConsumer, error) {
	query := url.Values{deploymentQueryKey: {deployment}}

	resp, err := c.httpClient.Get(ctx, "/link_consumers", query)
	if err != nil {
		return nil, fmt.Errorf("listing link consumers for %s: %w", deployment, err)
	}

	var consumers []boshapi.LinkConsumer

	if err := json.Unmarshal(resp.Body, &consumers); err != nil {
		return nil, fmt.Errorf("parsing link consumers: %w", err)
	}

	return consumers, nil
}
