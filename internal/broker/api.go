package broker

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

func (api API) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	api.logIncomingRequest(req)

	username, password, ok := req.BasicAuth()
	if !ok {
		api.handleMissingAuth(writer, req)

		return
	}

	if !api.validateCredentials(username, password) {
		api.handleInvalidAuth(writer, req, username)

		return
	}

	api.routeRequest(writer, req, username)
}

func (api API) logIncomingRequest(req *http.Request) {
	if api.Logger != nil {
		api.Logger.Info("[api] request: %s %s from %s", req.Method, req.URL.Path, req.RemoteAddr)
		api.Logger.Debug("[api] request headers: %v", req.Header)
	}
}

func (api API) handleMissingAuth(writer http.ResponseWriter, req *http.Request) {
	if api.Logger != nil {
		api.Logger.Info("[api] authentication failed: No basic auth credentials provided for %s %s from %s", req.Method, req.URL.Path, req.RemoteAddr)
	}

	writer.Header().Set("WWW-Authenticate", "basic realm=Blacksmith")
	writer.WriteHeader(http.StatusUnauthorized)
	_, _ = fmt.Fprintf(writer, "Authorization Required\n")
}

func (api API) validateCredentials(username, password string) bool {
	return username == api.Username && password == api.Password
}

func (api API) handleInvalidAuth(writer http.ResponseWriter, req *http.Request, username string) {
	if api.Logger != nil {
		api.Logger.Info("[api] authentication failed: Invalid credentials for user '%s' from %s", username, req.RemoteAddr)
		api.Logger.Debug("[api] failed auth attempt for user: %s, path: %s", username, req.URL.Path)
	}

	writer.WriteHeader(http.StatusForbidden)
	_, _ = fmt.Fprintf(writer, "Forbidden\n")
}

func (api API) routeRequest(writer http.ResponseWriter, req *http.Request, username string) {
	switch {
	case strings.HasPrefix(req.URL.Path, "/b/"):
		api.routeToInternal(writer, req, username)
	case strings.HasPrefix(req.URL.Path, "/v2/"):
		api.routeToPrimary(writer, req, username)
	default:
		api.routeToWebRoot(writer, req, username)
	}
}

func (api API) routeToInternal(writer http.ResponseWriter, req *http.Request, username string) {
	if api.Logger != nil {
		api.Logger.Info("[api] routing request to Internal API: %s %s", req.Method, req.URL.Path)
		api.Logger.Debug("[api] internal API request details: user=%s, path=%s, query=%s", username, req.URL.Path, req.URL.RawQuery)
	}

	api.Internal.ServeHTTP(writer, req)
}

func (api API) routeToPrimary(writer http.ResponseWriter, req *http.Request, username string) {
	if api.Logger != nil {
		api.Logger.Info("[api] routing request to Primary API (Service Broker): %s %s", req.Method, req.URL.Path)
	}

	api.ensureBrokerAPIVersion(req)

	if api.Logger != nil {
		api.Logger.Debug("[api] service Broker request details: user=%s, path=%s, query=%s", username, req.URL.Path, req.URL.RawQuery)
	}

	api.Primary.ServeHTTP(writer, req)
}

func (api API) ensureBrokerAPIVersion(req *http.Request) {
	brokerAPIVersion := req.Header.Get("X-Broker-Api-Version")
	if brokerAPIVersion == "" {
		if api.Logger != nil {
			api.Logger.Info("[api] adding missing X-Broker-Api-Version header for %s", req.URL.Path)
		}

		req.Header.Set("X-Broker-Api-Version", "2.16")
	} else if api.Logger != nil {
		api.Logger.Debug("[api] X-Broker-Api-Version header already present: %s", brokerAPIVersion)
	}
}

func (api API) routeToWebRoot(writer http.ResponseWriter, req *http.Request, username string) {
	if api.Logger != nil {
		api.Logger.Info("[api] routing request to WebRoot (UI): %s %s", req.Method, req.URL.Path)
		api.Logger.Debug("[api] webRoot request details: user=%s, path=%s", username, req.URL.Path)
	}

	api.WebRoot.ServeHTTP(writer, req)
}

type NullHandler struct{}

func (n NullHandler) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	writer.WriteHeader(http.StatusNotFound)
	_, _ = fmt.Fprintf(writer, "404 not found\n")
}
