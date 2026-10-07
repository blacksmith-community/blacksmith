package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	nethttp "net/http"
	"net/url"

	"github.com/fivetwenty-io/boshapi/internal/http"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// manifestBodyKey is the JSON body field name used by the diff endpoints to
// carry the manifest/config YAML being diffed.
const manifestBodyKey = "manifest"

type configsClient struct {
	httpClient *http.Client
}

// NewConfigsClient creates a new generic configs client.
func NewConfigsClient(httpClient *http.Client) boshapi.ConfigsClient {
	return &configsClient{httpClient: httpClient}
}

func (c *configsClient) List(ctx context.Context, opts *boshapi.ConfigListOptions) ([]boshapi.ConfigEntry, error) {
	resp, err := c.httpClient.Get(ctx, "/configs", opts.ToValues())
	if err != nil {
		return nil, fmt.Errorf("listing configs: %w", err)
	}

	var configs []boshapi.ConfigEntry

	if err := json.Unmarshal(resp.Body, &configs); err != nil {
		return nil, fmt.Errorf("parsing configs: %w", err)
	}

	return configs, nil
}

func (c *configsClient) Get(ctx context.Context, configID string) (*boshapi.ConfigEntry, error) {
	resp, err := c.httpClient.Get(ctx, "/configs/"+url.PathEscape(configID), nil)
	if err != nil {
		var apiErr *boshapi.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == nethttp.StatusNotFound {
			return nil, &boshapi.ConfigNotFoundError{ID: configID}
		}

		return nil, fmt.Errorf("getting config %s: %w", configID, err)
	}

	var config boshapi.ConfigEntry

	if err := json.Unmarshal(resp.Body, &config); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	return &config, nil
}

func (c *configsClient) Create(ctx context.Context, req *boshapi.CreateConfigRequest) (*boshapi.ConfigEntry, error) {
	resp, err := c.httpClient.Post(ctx, "/configs", req)
	if err != nil {
		return nil, fmt.Errorf("creating config: %w", err)
	}

	var config boshapi.ConfigEntry

	if err := json.Unmarshal(resp.Body, &config); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	return &config, nil
}

func (c *configsClient) Delete(ctx context.Context, id string) error {
	query := url.Values{"id": {id}}

	_, err := c.httpClient.DeleteWithQuery(ctx, "/configs", query)
	if err != nil {
		return fmt.Errorf("deleting config %s: %w", id, err)
	}

	return nil
}

func (c *configsClient) DeleteByTypeAndName(ctx context.Context, configType, name string) error {
	query := url.Values{
		"type": {configType},
		"name": {name},
	}

	_, err := c.httpClient.DeleteWithQuery(ctx, "/configs", query)
	if err != nil {
		return fmt.Errorf("deleting config %s/%s: %w", configType, name, err)
	}

	return nil
}

func (c *configsClient) Diff(ctx context.Context, req *boshapi.ConfigDiffRequest) (*boshapi.ConfigDiff, error) {
	resp, err := c.httpClient.Post(ctx, "/configs/diff", req)
	if err != nil {
		return nil, fmt.Errorf("diffing config: %w", err)
	}

	var diff boshapi.ConfigDiff

	if err := json.Unmarshal(resp.Body, &diff); err != nil {
		return nil, fmt.Errorf("parsing config diff: %w", err)
	}

	return &diff, nil
}

// Cloud configs client.
type cloudConfigsClient struct {
	httpClient *http.Client
}

// NewCloudConfigsClient creates a new cloud configs client.
func NewCloudConfigsClient(httpClient *http.Client) boshapi.CloudConfigsClient {
	return &cloudConfigsClient{httpClient: httpClient}
}

func (c *cloudConfigsClient) Get(ctx context.Context, opts *boshapi.CloudConfigListOptions) ([]boshapi.ConfigVersion, error) {
	var query url.Values

	if opts != nil {
		query = opts.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, "/cloud_configs", query)
	if err != nil {
		return nil, fmt.Errorf("getting cloud configs: %w", err)
	}

	var configs []boshapi.ConfigVersion

	if err := json.Unmarshal(resp.Body, &configs); err != nil {
		return nil, fmt.Errorf("parsing cloud configs: %w", err)
	}

	return configs, nil
}

func (c *cloudConfigsClient) Update(ctx context.Context, yaml string, opts *boshapi.UpdateCloudConfigOptions) error {
	query := url.Values{}

	if opts != nil && opts.Name != "" {
		query.Set("name", opts.Name)
	}

	_, err := c.httpClient.PostYAML(ctx, "/cloud_configs", yaml, query)
	if err != nil {
		return fmt.Errorf("updating cloud config: %w", err)
	}

	return nil
}

func (c *cloudConfigsClient) Diff(ctx context.Context, yaml string, opts *boshapi.DiffOptions) (*boshapi.ConfigDiff, error) {
	body := map[string]interface{}{manifestBodyKey: yaml}

	if opts != nil {
		body["redact"] = opts.Redact
	}

	resp, err := c.httpClient.Post(ctx, "/cloud_configs/diff", body)
	if err != nil {
		return nil, fmt.Errorf("diffing cloud config: %w", err)
	}

	var diff boshapi.ConfigDiff

	if err := json.Unmarshal(resp.Body, &diff); err != nil {
		return nil, fmt.Errorf("parsing cloud config diff: %w", err)
	}

	return &diff, nil
}

// Runtime configs client.
type runtimeConfigsClient struct {
	httpClient *http.Client
}

// NewRuntimeConfigsClient creates a new runtime configs client.
func NewRuntimeConfigsClient(httpClient *http.Client) boshapi.RuntimeConfigsClient {
	return &runtimeConfigsClient{httpClient: httpClient}
}

func (c *runtimeConfigsClient) Get(ctx context.Context, opts *boshapi.RuntimeConfigListOptions) ([]boshapi.ConfigVersion, error) {
	var query url.Values

	if opts != nil {
		query = opts.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, "/runtime_configs", query)
	if err != nil {
		return nil, fmt.Errorf("getting runtime configs: %w", err)
	}

	var configs []boshapi.ConfigVersion

	if err := json.Unmarshal(resp.Body, &configs); err != nil {
		return nil, fmt.Errorf("parsing runtime configs: %w", err)
	}

	return configs, nil
}

func (c *runtimeConfigsClient) Update(ctx context.Context, yaml string, opts *boshapi.UpdateRuntimeConfigOptions) error {
	query := url.Values{}

	if opts != nil && opts.Name != "" {
		query.Set("name", opts.Name)
	}

	_, err := c.httpClient.PostYAML(ctx, "/runtime_configs", yaml, query)
	if err != nil {
		return fmt.Errorf("updating runtime config: %w", err)
	}

	return nil
}

func (c *runtimeConfigsClient) Diff(ctx context.Context, yaml string, opts *boshapi.DiffOptions) (*boshapi.ConfigDiff, error) {
	body := map[string]interface{}{manifestBodyKey: yaml}

	if opts != nil {
		body["redact"] = opts.Redact
	}

	resp, err := c.httpClient.Post(ctx, "/runtime_configs/diff", body)
	if err != nil {
		return nil, fmt.Errorf("diffing runtime config: %w", err)
	}

	var diff boshapi.ConfigDiff

	if err := json.Unmarshal(resp.Body, &diff); err != nil {
		return nil, fmt.Errorf("parsing runtime config diff: %w", err)
	}

	return &diff, nil
}

// CPI configs client.
type cpiConfigsClient struct {
	httpClient *http.Client
}

// NewCPIConfigsClient creates a new CPI configs client.
func NewCPIConfigsClient(httpClient *http.Client) boshapi.CPIConfigsClient {
	return &cpiConfigsClient{httpClient: httpClient}
}

func (c *cpiConfigsClient) Get(ctx context.Context, opts *boshapi.CPIConfigListOptions) ([]boshapi.ConfigVersion, error) {
	var query url.Values

	if opts != nil {
		query = opts.ToValues()
	}

	resp, err := c.httpClient.Get(ctx, "/cpi_configs", query)
	if err != nil {
		return nil, fmt.Errorf("getting CPI configs: %w", err)
	}

	var configs []boshapi.ConfigVersion

	if err := json.Unmarshal(resp.Body, &configs); err != nil {
		return nil, fmt.Errorf("parsing CPI configs: %w", err)
	}

	return configs, nil
}

func (c *cpiConfigsClient) Update(ctx context.Context, yaml string) error {
	_, err := c.httpClient.PostYAML(ctx, "/cpi_configs", yaml, nil)
	if err != nil {
		return fmt.Errorf("updating CPI config: %w", err)
	}

	return nil
}

func (c *cpiConfigsClient) Diff(ctx context.Context, yaml string, opts *boshapi.DiffOptions) (*boshapi.ConfigDiff, error) {
	body := map[string]interface{}{manifestBodyKey: yaml}

	if opts != nil {
		body["redact"] = opts.Redact
	}

	resp, err := c.httpClient.Post(ctx, "/cpi_configs/diff", body)
	if err != nil {
		return nil, fmt.Errorf("diffing CPI config: %w", err)
	}

	var diff boshapi.ConfigDiff

	if err := json.Unmarshal(resp.Body, &diff); err != nil {
		return nil, fmt.Errorf("parsing CPI config diff: %w", err)
	}

	return &diff, nil
}
