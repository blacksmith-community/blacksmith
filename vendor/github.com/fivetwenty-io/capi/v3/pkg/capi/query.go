package capi

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// QueryParams represents query parameters for list operations. All fields are
// optional; zero values are omitted when converted to URL values.
type QueryParams struct {
	// Page: 1-based page index to request.
	Page int
	// PerPage: number of items per page. If unset, helpers often default to 50.
	PerPage int
	// OrderBy: server-defined sort expression, e.g. "created_at", "-name".
	OrderBy string
	// LabelSelector: Kubernetes-style label selector (if supported by endpoint).
	LabelSelector string
	// Fields: per-related-resource field selection, encoded as fields[<name>]=a,b.
	Fields map[string][]string
	// Include: related resources to include, encoded as include=a,b.
	Include []string
	// Filters: arbitrary filter key → values applied to the endpoint (e.g.,
	// "names": ["app-a","app-b"], "space_guids": ["..."]). Values are joined
	// with commas for transmission.
	Filters map[string][]string
}

// NewQueryParams creates a new QueryParams with initialized maps.
func NewQueryParams() *QueryParams {
	return &QueryParams{
		Fields:  make(map[string][]string),
		Filters: make(map[string][]string),
	}
}

// ToValues converts QueryParams to url.Values.
func (q *QueryParams) ToValues() url.Values {
	values := url.Values{}

	if q.Page > 0 {
		values.Set("page", strconv.Itoa(q.Page))
	}

	if q.PerPage > 0 {
		values.Set("per_page", strconv.Itoa(q.PerPage))
	}

	if q.OrderBy != "" {
		values.Set("order_by", q.OrderBy)
	}

	if q.LabelSelector != "" {
		values.Set("label_selector", q.LabelSelector)
	}

	if len(q.Include) > 0 {
		values.Set("include", strings.Join(q.Include, ","))
	}

	// Add fields — sort keys for deterministic URL output.
	fieldKeys := make([]string, 0, len(q.Fields))
	for resource := range q.Fields {
		fieldKeys = append(fieldKeys, resource)
	}

	sort.Strings(fieldKeys)

	for _, resource := range fieldKeys {
		key := fmt.Sprintf("fields[%s]", resource)
		values.Set(key, strings.Join(q.Fields[resource], ","))
	}

	// Add filters — sort keys for deterministic URL output.
	filterKeys := make([]string, 0, len(q.Filters))
	for key := range q.Filters {
		filterKeys = append(filterKeys, key)
	}

	sort.Strings(filterKeys)

	for _, key := range filterKeys {
		if len(q.Filters[key]) > 0 {
			values.Set(key, strings.Join(q.Filters[key], ","))
		}
	}

	return values
}

// WithPage sets the page number.
func (q *QueryParams) WithPage(page int) *QueryParams {
	q.Page = page

	return q
}

// maxPerPage is the CF v3 API hard limit for per_page.
const maxPerPage = 5000

// WithPerPage sets the number of results per page. Values above 5000 are
// clamped to 5000 to match the CF v3 API maximum — no error is returned.
func (q *QueryParams) WithPerPage(perPage int) *QueryParams {
	if perPage > maxPerPage {
		perPage = maxPerPage
	}

	q.PerPage = perPage

	return q
}

// WithOrderBy sets the ordering.
func (q *QueryParams) WithOrderBy(orderBy string) *QueryParams {
	q.OrderBy = orderBy

	return q
}

// WithLabelSelector sets the label selector.
func (q *QueryParams) WithLabelSelector(selector string) *QueryParams {
	q.LabelSelector = selector

	return q
}

// WithInclude adds include parameters, skipping values already present.
// Dedup semantics match appendInclude in query_options.go.
func (q *QueryParams) WithInclude(includes ...string) *QueryParams {
	for _, inc := range includes {
		duplicate := false

		for _, existing := range q.Include {
			if existing == inc {
				duplicate = true

				break
			}
		}

		if !duplicate {
			q.Include = append(q.Include, inc)
		}
	}

	return q
}

// WithFields adds field selection for a resource.
func (q *QueryParams) WithFields(resource string, fields ...string) *QueryParams {
	if q.Fields == nil {
		q.Fields = make(map[string][]string)
	}

	q.Fields[resource] = fields

	return q
}

// WithFilter adds a filter.
func (q *QueryParams) WithFilter(key string, values ...string) *QueryParams {
	if q.Filters == nil {
		q.Filters = make(map[string][]string)
	}

	q.Filters[key] = append(q.Filters[key], values...)

	return q
}
