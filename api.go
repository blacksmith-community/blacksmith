package main

import (
	"fmt"
	"net/http"
	"strings"

	"blacksmith/pkg/logger"
)

type API struct {
	Username string
	Password string
	Internal http.Handler
	Primary  http.Handler
	WebRoot  http.Handler
	Logger   logger.Logger
}

func (api API) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Log incoming request
	if api.Logger != nil {
		api.Logger.Info("[api] request: %s %s from %s", req.Method, req.URL.Path, req.RemoteAddr)

		// Debug logging for request headers
		api.Logger.Debug("[api] request headers: %v", req.Header)
	}

	username, password, ok := req.BasicAuth()
	if !ok {
		if api.Logger != nil {
			api.Logger.Info("[api] authentication failed: No basic auth credentials provided for %s %s from %s", req.Method, req.URL.Path, req.RemoteAddr)
		}

		w.Header().Set("WWW-Authenticate", "basic realm=Blacksmith")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprintf(w, "Authorization Required\n")

		return
	}

	if username != api.Username || password != api.Password {
		if api.Logger != nil {
			api.Logger.Info("[api] authentication failed: Invalid credentials for user '%s' from %s", username, req.RemoteAddr)
			api.Logger.Debug("[api] failed auth attempt for user: %s, path: %s", username, req.URL.Path)
		}

		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintf(w, "Forbidden\n")

		return
	}

	if strings.HasPrefix(req.URL.Path, "/b/") {
		if api.Logger != nil {
			api.Logger.Info("[api] routing request to Internal API: %s %s", req.Method, req.URL.Path)
			api.Logger.Debug("[api] internal API request details: user=%s, path=%s, query=%s", username, req.URL.Path, req.URL.RawQuery)
		}

		api.Internal.ServeHTTP(w, req)

		return
	}

	if strings.HasPrefix(req.URL.Path, "/v2/") {
		if api.Logger != nil {
			api.Logger.Info("[api] routing request to Primary API (Service Broker): %s %s", req.Method, req.URL.Path)
		}
		// Add the X-Broker-API-Version header if not present
		// This allows the UI to work without needing to send the header
		brokerAPIVersion := req.Header.Get("X-Broker-Api-Version")
		if brokerAPIVersion == "" {
			if api.Logger != nil {
				api.Logger.Info("[api] adding missing X-Broker-Api-Version header for %s", req.URL.Path)
			}

			req.Header.Set("X-Broker-Api-Version", "2.16")
		} else if api.Logger != nil {
			api.Logger.Debug("[api] X-Broker-Api-Version header already present: %s", brokerAPIVersion)
		}

		if api.Logger != nil {
			api.Logger.Debug("[api] service Broker request details: user=%s, path=%s, query=%s", username, req.URL.Path, req.URL.RawQuery)
		}

		api.Primary.ServeHTTP(w, req)

		return
	}

	if api.Logger != nil {
		api.Logger.Info("[api] routing request to WebRoot (UI): %s %s", req.Method, req.URL.Path)
		api.Logger.Debug("[api] webRoot request details: user=%s, path=%s", username, req.URL.Path)
	}

	api.WebRoot.ServeHTTP(w, req)
}

type NullHandler struct{}

func (n NullHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = fmt.Fprintf(w, "404 not found\n")
}
