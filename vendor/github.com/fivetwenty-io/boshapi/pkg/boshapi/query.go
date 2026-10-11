package boshapi

import (
	"net/url"
	"strconv"
	"strings"
)

// QueryParams represents generic query parameters for BOSH Director API list
// operations. BOSH does not use pagination (no page/per_page); queries are
// expressed as simple key-value filters.
type QueryParams struct {
	// Filters: arbitrary filter key -> values applied to the endpoint.
	// Values are joined with commas for transmission.
	Filters map[string][]string
}

// NewQueryParams creates a new QueryParams with an initialized Filters map.
func NewQueryParams() *QueryParams {
	return &QueryParams{
		Filters: make(map[string][]string),
	}
}

// ToValues converts QueryParams to url.Values.
func (q *QueryParams) ToValues() url.Values {
	values := url.Values{}

	for key, vals := range q.Filters {
		if len(vals) > 0 {
			values.Set(key, strings.Join(vals, ","))
		}
	}

	return values
}

// WithFilter adds a filter parameter. Multiple calls with the same key append
// to the existing values for that key.
func (q *QueryParams) WithFilter(key string, values ...string) *QueryParams {
	if q.Filters == nil {
		q.Filters = make(map[string][]string)
	}

	q.Filters[key] = append(q.Filters[key], values...)

	return q
}

// TaskListOptions provides structured options for filtering task lists.
type TaskListOptions struct {
	// State filters tasks by state (queued, processing, done, error, cancelled).
	State string
	// Deployment filters tasks by deployment name.
	Deployment string
	// ContextID filters tasks by context ID.
	ContextID string
	// Limit limits the number of results returned.
	Limit int
	// Verbose sets the verbosity level (1 or 2).
	Verbose int
}

// ToValues converts TaskListOptions to url.Values. Zero-value fields are
// omitted.
func (o *TaskListOptions) ToValues() url.Values {
	values := url.Values{}

	if o.State != "" {
		values.Set("state", o.State)
	}

	if o.Deployment != "" {
		values.Set("deployment", o.Deployment)
	}

	if o.ContextID != "" {
		values.Set("context_id", o.ContextID)
	}

	if o.Limit > 0 {
		values.Set(limitParam, strconv.Itoa(o.Limit))
	}

	if o.Verbose > 0 {
		values.Set("verbose", strconv.Itoa(o.Verbose))
	}

	return values
}

// ToValues converts DeployOptions to url.Values. Zero-value fields are
// omitted.
func (o *DeployOptions) ToValues() url.Values {
	values := url.Values{}

	if o.Recreate {
		values.Set("recreate", "true")
	}

	if o.RecreatePersistent {
		values.Set("recreate_persistent_disks", "true")
	}

	if o.SkipDrain != "" {
		values.Set("skip_drain", o.SkipDrain)
	}

	if o.Fix {
		values.Set("fix", "true")
	}

	if o.DryRun {
		values.Set("dry_run", "true")
	}

	if o.MaxInFlight != "" {
		values.Set("max_in_flight", o.MaxInFlight)
	}

	if o.Canaries != "" {
		values.Set("canaries", o.Canaries)
	}

	if o.RecreateVMsCreatedBefore != "" {
		values.Set("recreate_vms_created_before", o.RecreateVMsCreatedBefore)
	}

	if o.ForceLatestVariables {
		values.Set("force_latest_variables", "true")
	}

	return values
}

// ToValues converts UpdateStateOptions to url.Values. Zero-value fields are
// omitted. Hard combined with NewState "stopped" is translated to state
// "detached".
func (o *UpdateStateOptions) ToValues() url.Values {
	values := url.Values{}

	state := o.NewState
	if o.Hard && state == "stopped" {
		state = "detached"
	}

	if state != "" {
		values.Set("state", state)
	}

	if o.SkipDrain {
		values.Set("skip_drain", "true")
	}

	if o.Fix {
		values.Set("fix", "true")
	}

	if o.DryRun {
		values.Set("dry_run", "true")
	}

	if o.Canaries != "" {
		values.Set("canaries", o.Canaries)
	}

	if o.MaxInFlight != "" {
		values.Set("max_in_flight", o.MaxInFlight)
	}

	if o.RecreateVMsCreatedBefore != "" {
		values.Set("recreate_vms_created_before", o.RecreateVMsCreatedBefore)
	}

	return values
}

// ToValues converts StopInstanceOptions to url.Values. Zero-value fields are
// omitted.
func (o *StopInstanceOptions) ToValues() url.Values {
	values := url.Values{}

	if o.SkipDrain {
		values.Set("skip_drain", "true")
	}

	if o.Hard {
		values.Set("hard", "true")
	}

	return values
}

// ToValues converts RestartInstanceOptions to url.Values. Zero-value fields
// are omitted.
func (o *RestartInstanceOptions) ToValues() url.Values {
	values := url.Values{}

	if o.SkipDrain {
		values.Set("skip_drain", "true")
	}

	return values
}

// ToValues converts RecreateInstanceOptions to url.Values. Zero-value fields
// are omitted.
func (o *RecreateInstanceOptions) ToValues() url.Values {
	values := url.Values{}

	if o.SkipDrain {
		values.Set("skip_drain", "true")
	}

	if o.IgnoreUnresponsiveAgent {
		values.Set("ignore_unresponsive_agent", "true")
	}

	return values
}

// DeploymentListOptions provides structured options for filtering deployment
// lists.
type DeploymentListOptions struct {
	// ExcludeConfigs excludes config information from the response when true.
	ExcludeConfigs bool
	// ExcludeReleases excludes release information from the response when true.
	ExcludeReleases bool
	// ExcludeStemcells excludes stemcell information from the response when true.
	ExcludeStemcells bool
}

// ToValues converts DeploymentListOptions to url.Values. Only fields set to
// true are included.
func (o *DeploymentListOptions) ToValues() url.Values {
	values := url.Values{}

	if o.ExcludeConfigs {
		values.Set("exclude_configs", "true")
	}

	if o.ExcludeReleases {
		values.Set("exclude_releases", "true")
	}

	if o.ExcludeStemcells {
		values.Set("exclude_stemcells", "true")
	}

	return values
}
