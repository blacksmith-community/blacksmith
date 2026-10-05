package broker

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode"

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
		api.Logger.Info("request: %s %s from %s", req.Method, req.URL.Path, req.RemoteAddr)
		api.Logger.Debug("request headers: %v", redactedHeaders(req.Header))
	}
}

func (api API) handleMissingAuth(writer http.ResponseWriter, req *http.Request) {
	if api.Logger != nil {
		api.Logger.Info("authentication failed: No basic auth credentials provided for %s %s from %s", req.Method, req.URL.Path, req.RemoteAddr)
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
		api.Logger.Info("authentication failed: Invalid credentials for user '%s' from %s", username, req.RemoteAddr)
		api.Logger.Debug("failed auth attempt for user: %s, path: %s", username, req.URL.Path)
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
		api.Logger.Info("routing request to Internal API: %s %s", req.Method, req.URL.Path)
		api.Logger.Debug("internal API request details: user=%s, path=%s, query=%s", username, req.URL.Path, redactedQuery(req.URL.RawQuery))
	}

	api.Internal.ServeHTTP(writer, req)
}

func (api API) routeToPrimary(writer http.ResponseWriter, req *http.Request, username string) {
	if api.Logger != nil {
		api.Logger.Info("routing request to Primary API (Service Broker): %s %s", req.Method, req.URL.Path)
	}

	api.ensureBrokerAPIVersion(req)

	if api.Logger != nil {
		api.Logger.Debug("service Broker request details: user=%s, path=%s, query=%s", username, req.URL.Path, redactedQuery(req.URL.RawQuery))
	}

	api.Primary.ServeHTTP(writer, req)
}

func (api API) ensureBrokerAPIVersion(req *http.Request) {
	brokerAPIVersion := req.Header.Get("X-Broker-Api-Version")
	if brokerAPIVersion == "" {
		if api.Logger != nil {
			api.Logger.Info("adding missing X-Broker-Api-Version header for %s", req.URL.Path)
		}

		req.Header.Set("X-Broker-Api-Version", "2.17")
	} else if api.Logger != nil {
		api.Logger.Debug("X-Broker-Api-Version header already present: %s", brokerAPIVersion)
	}
}

func (api API) routeToWebRoot(writer http.ResponseWriter, req *http.Request, username string) {
	if api.Logger != nil {
		api.Logger.Info("routing request to WebRoot (UI): %s %s", req.Method, req.URL.Path)
		api.Logger.Debug("webRoot request details: user=%s, path=%s", username, req.URL.Path)
	}

	api.WebRoot.ServeHTTP(writer, req)
}

type NullHandler struct{}

func (n NullHandler) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	writer.WriteHeader(http.StatusNotFound)
	_, _ = fmt.Fprintf(writer, "404 not found\n")
}

// redactedHeaders copies h with the values of credential-bearing headers
// replaced, so debug logs show which headers arrived without their secrets.
func redactedHeaders(h http.Header) http.Header {
	out := make(http.Header, len(h))

	for name, values := range h {
		switch http.CanonicalHeaderKey(name) {
		case "Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Vault-Token":
			out[name] = []string{"<redacted>"}
		default:
			out[name] = values
		}
	}

	return out
}

// redactedQuery returns rawQuery with the values of credential-bearing
// parameters replaced, so debug logs show which parameters arrived without
// their secrets. A parameter is credential-bearing when its name contains
// password, secret, or token in any case, when "key" is a whole word of the
// name (api_key, apiKey, ssh-key), or when the name ends in key or keys once
// a trailing run of digits is dropped (passkey, ssh_keys, key1). A name like
// keyword or monkey_id stays visible.
func redactedQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}

	pairs := strings.Split(rawQuery, "&")

	for i, pair := range pairs {
		name, _, hasValue := strings.Cut(pair, "=")
		if !hasValue {
			continue
		}

		decoded, err := url.QueryUnescape(name)
		if err != nil {
			decoded = name
		}

		if isCredentialParameter(decoded) {
			pairs[i] = name + "=<redacted>"
		}
	}

	return strings.Join(pairs, "&")
}

// keyNameSuffixes are names that end in a key without a word break.
var keyNameSuffixes = []string{"apikey", "accesskey", "secretkey", "privatekey", "signingkey", "encryptionkey", "sshkey"}

func isCredentialParameter(name string) bool {
	lower := strings.ToLower(name)

	for _, marker := range []string{"password", "secret", "token"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}

	for _, suffix := range keyNameSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}

	// Any name that ends in key or keys, once a trailing run of digits is
	// dropped, is masked: passkey, ssh_keys, and key1 all end that way.
	trimmed := strings.TrimRight(lower, "0123456789")
	if strings.HasSuffix(trimmed, "key") || strings.HasSuffix(trimmed, "keys") {
		return true
	}

	return slices.Contains(nameWords(name), "key")
}

// nameWords splits a parameter name into lower-case words at every character
// that is not a letter or digit and before each capital that follows a
// lower-case letter, so api_key, api-key, and apiKey all give api and key.
func nameWords(name string) []string {
	var (
		words   []string
		current []rune
		last    rune
	)

	flush := func() {
		if len(current) > 0 {
			words = append(words, strings.ToLower(string(current)))
			current = nil
		}
	}

	for _, char := range name {
		switch {
		case !unicode.IsLetter(char) && !unicode.IsDigit(char):
			flush()
		case unicode.IsUpper(char) && unicode.IsLower(last):
			flush()

			current = append(current, char)
		default:
			current = append(current, char)
		}

		last = char
	}

	flush()

	return words
}
