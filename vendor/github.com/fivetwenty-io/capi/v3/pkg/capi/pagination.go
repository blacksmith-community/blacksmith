package capi

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// PaginationIterator provides an iterator interface for paginated responses
type PaginationIterator[T any] struct {
	client       PaginationClient[T]
	ctx          context.Context
	currentPage  *ListResponse[T]
	currentIndex int
	params       *QueryParams
	path         string
}

// PaginationClient is an interface that resource clients must implement to support pagination
type PaginationClient[T any] interface {
	// ListWithPath performs a list request with the given path and params
	ListWithPath(ctx context.Context, path string, params *QueryParams) (*ListResponse[T], error)
}

// NewPaginationIterator creates a new pagination iterator
func NewPaginationIterator[T any](
	ctx context.Context,
	client PaginationClient[T],
	path string,
	params *QueryParams,
) *PaginationIterator[T] {
	if params == nil {
		params = &QueryParams{}
	}
	// Set a reasonable default page size if not specified
	if params.PerPage == 0 {
		params.PerPage = 50
	}
	return &PaginationIterator[T]{
		client:       client,
		ctx:          ctx,
		params:       params,
		path:         path,
		currentIndex: -1,
	}
}

// HasNext returns true if there are more items to iterate
func (it *PaginationIterator[T]) HasNext() bool {
	// If we haven't fetched any page yet
	if it.currentPage == nil {
		return true
	}

	// If we have more items in the current page
	if it.currentIndex+1 < len(it.currentPage.Resources) {
		return true
	}

	// If there's a next page
	return it.currentPage.Pagination.Next != nil && it.currentPage.Pagination.Next.Href != ""
}

// Next returns the next item in the iteration
func (it *PaginationIterator[T]) Next() (*T, error) {
	// If we haven't fetched any page yet, fetch the first page
	if it.currentPage == nil {
		page, err := it.client.ListWithPath(it.ctx, it.path, it.params)
		if err != nil {
			return nil, fmt.Errorf("fetching first page: %w", err)
		}
		it.currentPage = page
		it.currentIndex = -1
	}

	// If we have items in the current page
	if it.currentIndex+1 < len(it.currentPage.Resources) {
		it.currentIndex++
		return &it.currentPage.Resources[it.currentIndex], nil
	}

	// If there's a next page, fetch it
	if it.currentPage.Pagination.Next != nil && it.currentPage.Pagination.Next.Href != "" {
		nextURL := it.currentPage.Pagination.Next.Href
		nextParams, err := it.extractParamsFromURL(nextURL)
		if err != nil {
			return nil, fmt.Errorf("parsing next page URL: %w", err)
		}

		page, err := it.client.ListWithPath(it.ctx, it.path, nextParams)
		if err != nil {
			return nil, fmt.Errorf("fetching next page: %w", err)
		}

		it.currentPage = page
		it.currentIndex = 0

		if len(page.Resources) > 0 {
			return &page.Resources[0], nil
		}
	}

	return nil, fmt.Errorf("no more items")
}

// All fetches all pages and returns all resources
func (it *PaginationIterator[T]) All() ([]T, error) {
	var allResources []T

	for it.HasNext() {
		resource, err := it.Next()
		if err != nil {
			return nil, err
		}
		allResources = append(allResources, *resource)
	}

	return allResources, nil
}

// ForEach applies a function to each item in the iteration
func (it *PaginationIterator[T]) ForEach(fn func(T) error) error {
	for it.HasNext() {
		resource, err := it.Next()
		if err != nil {
			return err
		}
		if err := fn(*resource); err != nil {
			return err
		}
	}
	return nil
}

// extractParamsFromURL extracts query parameters from a URL
func (it *PaginationIterator[T]) extractParamsFromURL(urlStr string) (*QueryParams, error) {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return nil, err
	}

	params := &QueryParams{
		Filters: make(map[string][]string),
	}

	queryValues := parsedURL.Query()

	// Extract pagination parameters
	if page := queryValues.Get("page"); page != "" {
		pageNum, _ := strconv.Atoi(page)
		params.Page = pageNum
	}

	if perPage := queryValues.Get("per_page"); perPage != "" {
		perPageNum, _ := strconv.Atoi(perPage)
		params.PerPage = perPageNum
	}

	if orderBy := queryValues.Get("order_by"); orderBy != "" {
		params.OrderBy = orderBy
	}

	// Extract all other parameters as filters
	for key, values := range queryValues {
		if key != "page" && key != "per_page" && key != "order_by" {
			params.Filters[key] = values
		}
	}

	return params, nil
}

// PaginationOptions provides configuration for pagination operations
type PaginationOptions struct {
	// PageSize sets the number of items per page (default: 50, max: 5000)
	PageSize int

	// MaxPages limits the number of pages to fetch (0 = no limit)
	MaxPages int

	// Concurrent enables concurrent fetching of pages
	Concurrent bool

	// ConcurrentWorkers sets the number of concurrent workers (default: 3)
	ConcurrentWorkers int
}

// DefaultPaginationOptions returns default pagination options
func DefaultPaginationOptions() *PaginationOptions {
	return &PaginationOptions{
		PageSize:          50,
		MaxPages:          0,
		Concurrent:        false,
		ConcurrentWorkers: 3,
	}
}

// PaginationHelper provides utility functions for pagination
type PaginationHelper struct {
	options *PaginationOptions
}

// NewPaginationHelper creates a new pagination helper
func NewPaginationHelper(options *PaginationOptions) *PaginationHelper {
	if options == nil {
		options = DefaultPaginationOptions()
	}
	return &PaginationHelper{
		options: options,
	}
}

// FetchAllPages fetches all pages for a given resource type
func FetchAllPages[T any](
	ctx context.Context,
	client PaginationClient[T],
	path string,
	params *QueryParams,
	options *PaginationOptions,
) ([]T, error) {
	if options == nil {
		options = DefaultPaginationOptions()
	}
	if params == nil {
		params = &QueryParams{}
	}
	if params.PerPage == 0 {
		params.PerPage = options.PageSize
	}

	var allResources []T
	currentPage := 1
	params.Page = currentPage

	for {
		// Check if we've reached the max pages limit
		if options.MaxPages > 0 && currentPage > options.MaxPages {
			break
		}

		// Fetch the current page
		response, err := client.ListWithPath(ctx, path, params)
		if err != nil {
			return nil, fmt.Errorf("fetching page %d: %w", currentPage, err)
		}

		// Add resources from this page
		allResources = append(allResources, response.Resources...)

		// Check if there are more pages
		if response.Pagination.Next == nil || response.Pagination.Next.Href == "" {
			break
		}

		// Move to the next page
		currentPage++
		params.Page = currentPage
	}

	return allResources, nil
}

// StreamPages streams pages through a channel
func StreamPages[T any](
	ctx context.Context,
	client PaginationClient[T],
	path string,
	params *QueryParams,
	options *PaginationOptions,
) <-chan PageResult[T] {
	resultChan := make(chan PageResult[T])

	go func() {
		defer close(resultChan)

		if options == nil {
			options = DefaultPaginationOptions()
		}
		if params == nil {
			params = &QueryParams{}
		}
		if params.PerPage == 0 {
			params.PerPage = options.PageSize
		}

		currentPage := 1
		params.Page = currentPage

		for {
			// Check context cancellation
			select {
			case <-ctx.Done():
				resultChan <- PageResult[T]{Err: ctx.Err()}
				return
			default:
			}

			// Check if we've reached the max pages limit
			if options.MaxPages > 0 && currentPage > options.MaxPages {
				break
			}

			// Fetch the current page
			response, err := client.ListWithPath(ctx, path, params)
			if err != nil {
				resultChan <- PageResult[T]{Err: fmt.Errorf("fetching page %d: %w", currentPage, err)}
				return
			}

			// Send the page result
			resultChan <- PageResult[T]{
				Page:    currentPage,
				Items:   response.Resources,
				HasMore: response.Pagination.Next != nil && response.Pagination.Next.Href != "",
				Total:   response.Pagination.TotalResults,
			}

			// Check if there are more pages
			if response.Pagination.Next == nil || response.Pagination.Next.Href == "" {
				break
			}

			// Move to the next page
			currentPage++
			params.Page = currentPage
		}
	}()

	return resultChan
}

// PageResult represents a single page of results
type PageResult[T any] struct {
	Page    int
	Items   []T
	HasMore bool
	Total   int
	Err     error
}
