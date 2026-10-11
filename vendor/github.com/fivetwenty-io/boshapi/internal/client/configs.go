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

// checkConfigID refuses an ID that is not a decimal integer before anything
// is sent. The director's config IDs are integers, and an ID such as "" or
// ".." would change the route the request reaches, since "/configs/" is the
// list route and an unescaped dot segment climbs out of "/configs".
func checkConfigID(action, configID string) error {
	if configID == "" {
		return fmt.Errorf("%s: %w; the ID is empty and nothing was sent. Pass the ID of the config",
			action, boshapi.ErrConfigIDInvalid)
	}

	for _, char := range configID {
		if char < '0' || char > '9' {
			return fmt.Errorf("%s: %w; %q is not a decimal integer and nothing was sent. "+
				"Pass the ID that List reports for the config", action, boshapi.ErrConfigIDInvalid, configID)
		}
	}

	return nil
}

func (c *configsClient) Get(ctx context.Context, configID string) (*boshapi.ConfigEntry, error) {
	if err := checkConfigID("getting a config", configID); err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Get(ctx, "/configs/"+configID, nil)
	if err != nil {
		var apiErr *boshapi.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == nethttp.StatusNotFound {
			return nil, &boshapi.ConfigNotFoundError{ID: configID, Method: nethttp.MethodGet, Err: apiErr}
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

func (c *configsClient) Delete(ctx context.Context, configID string) error {
	if err := checkConfigID("deleting a config", configID); err != nil {
		return err
	}

	_, err := c.httpClient.Delete(ctx, "/configs/"+configID)
	if err != nil {
		var apiErr *boshapi.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == nethttp.StatusNotFound {
			return &boshapi.ConfigNotFoundError{ID: configID, Method: nethttp.MethodDelete, Err: apiErr}
		}

		return fmt.Errorf("deleting config %s: %w", configID, err)
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
	resp, err := c.httpClient.PostDiff(ctx, "/configs/diff", req)
	if err != nil {
		return nil, diffFailure("config", err)
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

func (c *cloudConfigsClient) Get(ctx context.Context, opts *boshapi.CloudConfigListOptions) ([]boshapi.ConfigRevision, error) {
	query := opts.ToValues()

	resp, err := c.httpClient.Get(ctx, "/cloud_configs", query)
	if err != nil {
		return nil, fmt.Errorf("getting cloud configs: %w", err)
	}

	var configs []boshapi.ConfigRevision

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

// Diff ignores NoRedact because the director's cloud config diff has no redact
// option.
func (c *cloudConfigsClient) Diff(ctx context.Context, yaml string, opts *boshapi.DiffOptions) (*boshapi.ConfigDiff, error) {
	return diffConfig(ctx, c.httpClient, "/cloud_configs/diff", yaml, "cloud config", diffQuery(opts, false, true))
}

// Runtime configs client.
type runtimeConfigsClient struct {
	httpClient *http.Client
}

// NewRuntimeConfigsClient creates a new runtime configs client.
func NewRuntimeConfigsClient(httpClient *http.Client) boshapi.RuntimeConfigsClient {
	return &runtimeConfigsClient{httpClient: httpClient}
}

func (c *runtimeConfigsClient) Get(ctx context.Context, opts *boshapi.RuntimeConfigListOptions) ([]boshapi.ConfigRevision, error) {
	query := opts.ToValues()

	resp, err := c.httpClient.Get(ctx, "/runtime_configs", query)
	if err != nil {
		return nil, fmt.Errorf("getting runtime configs: %w", err)
	}

	var configs []boshapi.ConfigRevision

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
	return diffConfig(ctx, c.httpClient, "/runtime_configs/diff", yaml, "runtime config", diffQuery(opts, true, true))
}

// CPI configs client.
type cpiConfigsClient struct {
	httpClient *http.Client
}

// NewCPIConfigsClient creates a new CPI configs client.
func NewCPIConfigsClient(httpClient *http.Client) boshapi.CPIConfigsClient {
	return &cpiConfigsClient{httpClient: httpClient}
}

func (c *cpiConfigsClient) Get(ctx context.Context, opts *boshapi.CPIConfigListOptions) ([]boshapi.ConfigRevision, error) {
	query := opts.ToValues()

	resp, err := c.httpClient.Get(ctx, "/cpi_configs", query)
	if err != nil {
		return nil, fmt.Errorf("getting CPI configs: %w", err)
	}

	var configs []boshapi.ConfigRevision

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
	return diffConfig(ctx, c.httpClient, "/cpi_configs/diff", yaml, "CPI config", diffQuery(opts, true, false))
}

// diffConfig posts yaml to a config diff route and decodes the answer. The
// director declares these routes as YAML-only and reads the raw body as the
// config, so the body is sent as text/yaml. label names the config kind in
// errors.
func diffConfig(
	ctx context.Context, httpClient *http.Client, path, yaml, label string, query url.Values,
) (*boshapi.ConfigDiff, error) {
	resp, err := httpClient.PostYAMLDiff(ctx, path, yaml, query)
	if err != nil {
		return nil, diffFailure(label, err)
	}

	// The director reports a diff it cannot compute with a 200 whose body
	// holds an error message, so the message is read here and never reaches
	// the caller as part of a diff.
	var answer struct {
		boshapi.ConfigDiff
		Error string `json:"error"`
	}

	if err := json.Unmarshal(resp.Body, &answer); err != nil {
		return nil, fmt.Errorf("parsing %s diff: %w", label, err)
	}

	if answer.Error != "" {
		return nil, &boshapi.DiffError{Target: label, Message: answer.Error}
	}

	return &answer.ConfigDiff, nil
}

// diffFailure turns the error of a diff request into the error to return. The
// generic config diff route answers a config it cannot diff with a 400 whose
// body is {"diff":[],"error":"..."}, so that answer becomes a DiffError, as
// the 200 form does. Any other failure, including a 400 that carries the
// director's own error code, keeps its APIError.
func diffFailure(label string, err error) error {
	var apiErr *boshapi.APIError

	if errors.As(err, &apiErr) && apiErr.StatusCode == nethttp.StatusBadRequest && apiErr.Code == 0 {
		var body struct {
			Error string `json:"error"`
		}

		if json.Unmarshal([]byte(apiErr.Description), &body) == nil && body.Error != "" {
			return &boshapi.DiffError{Target: label, Message: body.Error, Cause: apiErr}
		}
	}

	return fmt.Errorf("diffing %s: %w", label, err)
}

// diffQuery returns the query of a config diff. The director redacts unless
// the redact parameter is the string false, so redact is sent only when the
// caller opts out, and only for a route that reads it. The name picks the
// config to compare against, and the director uses default when it is
// absent, so it is sent only when set and only for a route that reads it.
func diffQuery(opts *boshapi.DiffOptions, withRedact, withName bool) url.Values {
	if opts == nil {
		return nil
	}

	query := url.Values{}

	if withRedact && opts.NoRedact {
		query.Set("redact", "false")
	}

	if withName && opts.Name != "" {
		query.Set("name", opts.Name)
	}

	return query
}
