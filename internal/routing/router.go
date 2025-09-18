package routing

import (
	"net/http"
	"strings"

	pkgmiddleware "blacksmith/pkg/http/middleware"
	"blacksmith/pkg/http/response"
)

// Router handles HTTP routing for the internal API.
type Router struct {
	middlewareChain pkgmiddleware.Chain
	handlers        map[string]http.Handler
}

// NewRouter creates a new router with the given middleware chain.
func NewRouter(middlewareChain pkgmiddleware.Chain) *Router {
	return &Router{
		middlewareChain: middlewareChain,
		handlers:        make(map[string]http.Handler),
	}
}

// NewRouterFromRoutes creates a new router from a slice of routes.
func NewRouterFromRoutes(middlewareChain pkgmiddleware.Chain, routes []Route) *Router {
	router := NewRouter(middlewareChain)

	for _, route := range routes {
		router.RegisterHandler(route.PathPrefix, route.Handler)
	}

	return router
}

// RegisterHandler registers a handler for a specific path prefix.
func (r *Router) RegisterHandler(pathPrefix string, handler http.Handler) {
	r.handlers[pathPrefix] = r.middlewareChain.Then(handler)
}

// RegisterHandlerFunc registers a handler function for a specific path prefix.
func (r *Router) RegisterHandlerFunc(pathPrefix string, handlerFunc http.HandlerFunc) {
	r.RegisterHandler(pathPrefix, handlerFunc)
}

// ServeHTTP implements the http.Handler interface.
func (r *Router) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	// Find the handler for this request
	handler := r.findHandler(req.URL.Path)
	if handler != nil {
		handler.ServeHTTP(writer, req)

		return
	}

	// No handler found
	response.WriteError(writer, http.StatusNotFound, "endpoint not found")
}

// FindHandler returns the handler for a given path, or nil if not found.
func (r *Router) FindHandler(path string) http.Handler {
	return r.findHandler(path)
}

// findHandler finds the appropriate handler for the given path.
func (r *Router) findHandler(path string) http.Handler {
	// Check for exact matches first
	if handler, exists := r.handlers[path]; exists {
		return handler
	}

	// Check for prefix matches, preferring the longest match
	var (
		longestPrefix string
		bestHandler   http.Handler
	)

	for prefix, handler := range r.handlers {
		if strings.HasPrefix(path, prefix) && len(prefix) > len(longestPrefix) {
			longestPrefix = prefix
			bestHandler = handler
		}
	}

	return bestHandler
}

// Route represents a route configuration.
type Route struct {
	PathPrefix string
	Handler    http.Handler
}
