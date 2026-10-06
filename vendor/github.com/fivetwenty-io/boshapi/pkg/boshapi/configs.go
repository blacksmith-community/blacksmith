package boshapi

import (
	"net/url"
	"strconv"
)

// ConfigEntry represents a BOSH config entry (cloud, runtime, cpi, or generic).
type ConfigEntry struct {
	ID        string   `json:"id,omitempty"`
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Content   string   `json:"content"`
	Teams     []string `json:"teams,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
	Current   bool     `json:"current,omitempty"`
}

// ConfigVersion represents a versioned config entry.
type ConfigVersion struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Content   string   `json:"content"`
	Teams     []string `json:"teams,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
	Current   bool     `json:"current,omitempty"`
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
		values.Set("limit", strconv.Itoa(o.Limit))
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
	Name   string
	Latest bool
}

// ToValues converts CloudConfigListOptions to url.Values.
func (o *CloudConfigListOptions) ToValues() url.Values {
	if o == nil {
		return nil
	}

	values := url.Values{}

	if o.Name != "" {
		values.Set("name", o.Name)
	}

	if o.Latest {
		values.Set("latest", "true")
	}

	return values
}

// RuntimeConfigListOptions represents options for listing runtime configs.
type RuntimeConfigListOptions struct {
	Name   string
	Latest bool
}

// ToValues converts RuntimeConfigListOptions to url.Values.
func (o *RuntimeConfigListOptions) ToValues() url.Values {
	if o == nil {
		return nil
	}

	values := url.Values{}

	if o.Name != "" {
		values.Set("name", o.Name)
	}

	if o.Latest {
		values.Set("latest", "true")
	}

	return values
}

// CPIConfigListOptions represents options for listing CPI configs.
type CPIConfigListOptions struct {
	Name   string
	Latest bool
}

// ToValues converts CPIConfigListOptions to url.Values.
func (o *CPIConfigListOptions) ToValues() url.Values {
	if o == nil {
		return nil
	}

	values := url.Values{}

	if o.Name != "" {
		values.Set("name", o.Name)
	}

	if o.Latest {
		values.Set("latest", "true")
	}

	return values
}

// UpdateCloudConfigOptions represents options for updating cloud config.
type UpdateCloudConfigOptions struct {
	Name             string
	ExpectedLatestID string
}

// UpdateRuntimeConfigOptions represents options for updating runtime config.
type UpdateRuntimeConfigOptions struct {
	Name             string
	ExpectedLatestID string
}
