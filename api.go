package main

import (
	"fmt"
	"net/http"
	"strings"
)

type API struct {
	Username string
	Password string
	Internal http.Handler
	Primary  http.Handler
	WebRoot  http.Handler
}

func (api API) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Log incoming request
	Logger.Info("API request: %s %s from %s", req.Method, req.URL.Path, req.RemoteAddr)
	if Debugging {
		Logger.Debug("Request headers: %v", req.Header)
	}

	username, password, ok := req.BasicAuth()
	if !ok {
		Logger.Info("Authentication failed: No basic auth credentials provided for %s %s from %s", req.Method, req.URL.Path, req.RemoteAddr)
		w.Header().Set("WWW-Authenticate", "basic realm=Blacksmith")
		w.WriteHeader(401)
		fmt.Fprintf(w, "Authorization Required\n")
		return
	}
	if username != api.Username || password != api.Password {
		Logger.Info("Authentication failed: Invalid credentials for user '%s' from %s", username, req.RemoteAddr)
		if Debugging {
			Logger.Debug("Failed auth attempt for user: %s, path: %s", username, req.URL.Path)
		}
		w.WriteHeader(403)
		fmt.Fprintf(w, "Forbidden\n")
		return
	}

	if strings.HasPrefix(req.URL.Path, "/b/") {
		Logger.Info("Routing request to Internal API: %s %s", req.Method, req.URL.Path)
		if Debugging {
			Logger.Debug("Internal API request details: user=%s, path=%s, query=%s", username, req.URL.Path, req.URL.RawQuery)
		}
		api.Internal.ServeHTTP(w, req)
		return
	}

	if strings.HasPrefix(req.URL.Path, "/v2/") {
		Logger.Info("Routing request to Primary API (Service Broker): %s %s", req.Method, req.URL.Path)
		// Add the X-Broker-API-Version header if not present
		// This allows the UI to work without needing to send the header
		brokerAPIVersion := req.Header.Get("X-Broker-API-Version")
		if brokerAPIVersion == "" {
			Logger.Info("Adding missing X-Broker-API-Version header for %s", req.URL.Path)
			req.Header.Set("X-Broker-API-Version", "2.16")
		} else {
			if Debugging {
				Logger.Debug("X-Broker-API-Version header already present: %s", brokerAPIVersion)
			}
		}
		if Debugging {
			Logger.Debug("Service Broker request details: user=%s, path=%s, query=%s", username, req.URL.Path, req.URL.RawQuery)
		}
		api.Primary.ServeHTTP(w, req)
		return
	}

	Logger.Info("Routing request to WebRoot (UI): %s %s", req.Method, req.URL.Path)
	if Debugging {
		Logger.Debug("WebRoot request details: user=%s, path=%s", username, req.URL.Path)
	}
	api.WebRoot.ServeHTTP(w, req)
}

type NullHandler struct{}

func (n NullHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	Logger.Info("404 Not Found: %s %s from %s", req.Method, req.URL.Path, req.RemoteAddr)
	if Debugging {
		Logger.Debug("404 request details: path=%s, query=%s, headers=%v", req.URL.Path, req.URL.RawQuery, req.Header)
	}
	w.WriteHeader(404)
	fmt.Fprintf(w, "404 not found\n")
}
