package middleware

import "net/http"

// Middleware represents a function that wraps an http.Handler with additional functionality.
type Middleware func(http.Handler) http.Handler

// Chain represents a chain of middleware that can be applied to an http.Handler.
type Chain struct {
	middlewares []Middleware
}

// New creates a new middleware chain.
func New(middlewares ...Middleware) Chain {
	return Chain{middlewares: append([]Middleware(nil), middlewares...)}
}

// Then applies the middleware chain to the provided handler.
func (c Chain) Then(handler http.Handler) http.Handler {
	if handler == nil {
		handler = http.DefaultServeMux
	}

	for i := len(c.middlewares) - 1; i >= 0; i-- {
		handler = c.middlewares[i](handler)
	}

	return handler
}

// ThenFunc applies the middleware chain to the provided handler function.
func (c Chain) ThenFunc(handlerFunc http.HandlerFunc) http.Handler {
	return c.Then(handlerFunc)
}

// Append extends the chain with additional middleware.
func (c Chain) Append(middlewares ...Middleware) Chain {
	newMiddlewares := make([]Middleware, 0, len(c.middlewares)+len(middlewares))
	newMiddlewares = append(newMiddlewares, c.middlewares...)
	newMiddlewares = append(newMiddlewares, middlewares...)

	return Chain{middlewares: newMiddlewares}
}
