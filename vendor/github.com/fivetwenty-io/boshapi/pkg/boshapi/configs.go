package boshapi

import (
	"net/url"
	"strconv"
)

// limitParam is the query parameter that caps how many records a list returns.
const limitParam = "limit"

// defaultConfigVersionLimit is the number of versions the cloud, runtime, and
// CPI config lists return when the caller names none. It is the limit bosh-cli
// sends for bosh cloud-config, so the newest version comes back.
const defaultConfigVersionLimit = 1

// ConfigEntry represents a BOSH config entry (cloud, runtime, cpi, or generic).
type ConfigEntry struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Content   string `json:"content"`
	Team      string `json:"team,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	Current   bool   `json:"current,omitempty"`
}

// ConfigRevision is one stored version of a cloud, runtime, or CPI config as
// the director lists it. The director sends only the config's content, as
// Properties, and the time it was stored, so the config's ID, name, type, and
// team are not available from these routes.
type ConfigRevision struct {
	Properties string `json:"properties"`
	CreatedAt  string `json:"created_at"`
}

// ConfigListOptions represents options for listing configs.
type ConfigListOptions struct {
	Type   string
	Name   string
	Latest bool
	// Limit caps the number of versions returned per config. Zero or a
	// negative value sends no limit.
	Limit int
}

// ToValues converts ConfigListOptions to url.Values. It always sends latest
// as true or false, as bosh-cli does, because a request that omits it gets
// the director's implicit latest-only behaviour. A nil receiver behaves like
// the zero value.
func (o *ConfigListOptions) ToValues() url.Values {
	if o == nil {
		o = &ConfigListOptions{}
	}

	values := url.Values{}

	if o.Type != "" {
		values.Set("type", o.Type)
	}

	if o.Name != "" {
		values.Set("name", o.Name)
	}

	values.Set("latest", strconv.FormatBool(o.Latest))

	if o.Limit > 0 {
		values.Set(limitParam, strconv.Itoa(o.Limit))
	}

	return values
}

// CreateConfigRequest represents a request to create a config.
type CreateConfigRequest struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

// ConfigDiff represents the diff between two configs.
type ConfigDiff struct {
	Diff [][]interface{} `json:"diff"`
	From ConfigDiffRef   `json:"from,omitempty"`
	To   ConfigDiffRef   `json:"to,omitempty"`
}

// ConfigDiffRef represents a config reference in a diff.
type ConfigDiffRef struct {
	ID string `json:"id,omitempty"`
}

// ConfigDiffRequest represents a request to diff configs.
type ConfigDiffRequest struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

// CloudConfigListOptions represents options for listing cloud configs.
type CloudConfigListOptions struct {
	Name string
	// Limit is the number of versions to return, newest first. Zero or a
	// negative value sends 1, the limit bosh-cli sends, because the
	// director rejects a list request that has none.
	Limit int
}

// ToValues converts CloudConfigListOptions to url.Values. It always sends a
// limit, because the director answers 400 without one. A nil receiver behaves
// like the zero value.
func (o *CloudConfigListOptions) ToValues() url.Values {
	if o == nil {
		o = &CloudConfigListOptions{}
	}

	values := url.Values{limitParam: {strconv.Itoa(max(o.Limit, defaultConfigVersionLimit))}}

	if o.Name != "" {
		values.Set("name", o.Name)
	}

	return values
}

// RuntimeConfigListOptions represents options for listing runtime configs.
type RuntimeConfigListOptions struct {
	Name string
	// Limit is the number of versions to return, newest first. Zero or a
	// negative value sends 1, the limit bosh-cli sends, because the
	// director rejects a list request that has none.
	Limit int
}

// ToValues converts RuntimeConfigListOptions to url.Values. It always sends a
// limit, because the director answers 400 without one. A nil receiver behaves
// like the zero value.
func (o *RuntimeConfigListOptions) ToValues() url.Values {
	if o == nil {
		o = &RuntimeConfigListOptions{}
	}

	values := url.Values{limitParam: {strconv.Itoa(max(o.Limit, defaultConfigVersionLimit))}}

	if o.Name != "" {
		values.Set("name", o.Name)
	}

	return values
}

// CPIConfigListOptions represents options for listing CPI configs. The
// director reads only the limit.
type CPIConfigListOptions struct {
	// Limit is the number of versions to return, newest first. Zero or a
	// negative value sends 1, the limit bosh-cli sends, because the
	// director rejects a list request that has none.
	Limit int
}

// ToValues converts CPIConfigListOptions to url.Values. It always sends a
// limit, because the director answers 400 without one. A nil receiver behaves
// like the zero value.
func (o *CPIConfigListOptions) ToValues() url.Values {
	if o == nil {
		o = &CPIConfigListOptions{}
	}

	values := url.Values{limitParam: {strconv.Itoa(max(o.Limit, defaultConfigVersionLimit))}}

	return values
}

// UpdateCloudConfigOptions represents options for updating cloud config.
type UpdateCloudConfigOptions struct {
	Name string
}

// UpdateRuntimeConfigOptions represents options for updating runtime config.
type UpdateRuntimeConfigOptions struct {
	Name string
}
