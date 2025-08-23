package capi

import (
	"fmt"
	"net/url"
	"strings"
)

// QueryParams represents query parameters for list operations
type QueryParams struct {
	Page          int
	PerPage       int
	OrderBy       string
	LabelSelector string
	Fields        map[string][]string
	Include       []string
	Filters       map[string][]string
}

// NewQueryParams creates a new QueryParams with defaults
func NewQueryParams() *QueryParams {
	return &QueryParams{
		Fields:  make(map[string][]string),
		Filters: make(map[string][]string),
	}
}

// ToValues converts QueryParams to url.Values
func (q *QueryParams) ToValues() url.Values {
	values := url.Values{}

	if q.Page > 0 {
		values.Set("page", fmt.Sprintf("%d", q.Page))
	}
	if q.PerPage > 0 {
		values.Set("per_page", fmt.Sprintf("%d", q.PerPage))
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

	// Add fields
	for resource, fields := range q.Fields {
		key := fmt.Sprintf("fields[%s]", resource)
		values.Set(key, strings.Join(fields, ","))
	}

	// Add filters
	for key, vals := range q.Filters {
		if len(vals) > 0 {
			values.Set(key, strings.Join(vals, ","))
		}
	}

	return values
}

// WithPage sets the page number
func (q *QueryParams) WithPage(page int) *QueryParams {
	q.Page = page
	return q
}

// WithPerPage sets the number of results per page
func (q *QueryParams) WithPerPage(perPage int) *QueryParams {
	q.PerPage = perPage
	return q
}

// WithOrderBy sets the ordering
func (q *QueryParams) WithOrderBy(orderBy string) *QueryParams {
	q.OrderBy = orderBy
	return q
}

// WithLabelSelector sets the label selector
func (q *QueryParams) WithLabelSelector(selector string) *QueryParams {
	q.LabelSelector = selector
	return q
}

// WithInclude adds include parameters
func (q *QueryParams) WithInclude(includes ...string) *QueryParams {
	q.Include = append(q.Include, includes...)
	return q
}

// WithFields adds field selection for a resource
func (q *QueryParams) WithFields(resource string, fields ...string) *QueryParams {
	if q.Fields == nil {
		q.Fields = make(map[string][]string)
	}
	q.Fields[resource] = fields
	return q
}

// WithFilter adds a filter
func (q *QueryParams) WithFilter(key string, values ...string) *QueryParams {
	if q.Filters == nil {
		q.Filters = make(map[string][]string)
	}
	q.Filters[key] = append(q.Filters[key], values...)
	return q
}
