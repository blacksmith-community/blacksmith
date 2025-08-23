package http

import (
	"net/url"
)

// RequestBuilder helps construct HTTP requests
type RequestBuilder struct {
	request *Request
}

// NewRequestBuilder creates a new request builder
func NewRequestBuilder() *RequestBuilder {
	return &RequestBuilder{
		request: &Request{
			Headers: make(map[string]string),
		},
	}
}

// Method sets the HTTP method
func (b *RequestBuilder) Method(method string) *RequestBuilder {
	b.request.Method = method
	return b
}

// Path sets the request path
func (b *RequestBuilder) Path(path string) *RequestBuilder {
	b.request.Path = path
	return b
}

// Query sets the query parameters
func (b *RequestBuilder) Query(query url.Values) *RequestBuilder {
	b.request.Query = query
	return b
}

// Body sets the request body
func (b *RequestBuilder) Body(body interface{}) *RequestBuilder {
	b.request.Body = body
	return b
}

// Header adds a header to the request
func (b *RequestBuilder) Header(key, value string) *RequestBuilder {
	if b.request.Headers == nil {
		b.request.Headers = make(map[string]string)
	}
	b.request.Headers[key] = value
	return b
}

// Headers sets multiple headers
func (b *RequestBuilder) Headers(headers map[string]string) *RequestBuilder {
	for key, value := range headers {
		b.Header(key, value)
	}
	return b
}

// Build returns the constructed request
func (b *RequestBuilder) Build() *Request {
	return b.request
}

// Get creates a GET request builder
func Get(path string) *RequestBuilder {
	return NewRequestBuilder().Method("GET").Path(path)
}

// Post creates a POST request builder
func Post(path string) *RequestBuilder {
	return NewRequestBuilder().Method("POST").Path(path)
}

// Put creates a PUT request builder
func Put(path string) *RequestBuilder {
	return NewRequestBuilder().Method("PUT").Path(path)
}

// Patch creates a PATCH request builder
func Patch(path string) *RequestBuilder {
	return NewRequestBuilder().Method("PATCH").Path(path)
}

// Delete creates a DELETE request builder
func Delete(path string) *RequestBuilder {
	return NewRequestBuilder().Method("DELETE").Path(path)
}
