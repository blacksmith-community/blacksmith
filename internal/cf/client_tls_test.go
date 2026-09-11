package cf_test

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"blacksmith/internal/cf"
)

const (
	testEndpointKey = "test"
	testUsername    = "user"
	testPassword    = "pass"
)

func link(href string) map[string]string {
	return map[string]string{"href": href}
}

// newSelfSignedCFAPI serves the minimum of the CF API needed to build a client
// and answer GetInfo, over TLS with a certificate no system root trusts.
func newSelfSignedCFAPI(t *testing.T) *httptest.Server {
	t.Helper()

	var server *httptest.Server

	server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		writer.Header().Set("Content-Type", "application/json")

		var body any

		switch req.URL.Path {
		case "/", "/v3":
			body = map[string]any{
				"links": map[string]any{
					"self":  link(server.URL + "/v3"),
					"uaa":   link(server.URL),
					"login": link(server.URL),
				},
			}
		case "/oauth/token":
			body = map[string]any{"access_token": "test-token", "token_type": "bearer", "expires_in": 3600}
		case "/v3/info":
			body = map[string]any{"name": "fake-cf", "version": 3}
		default:
			writer.WriteHeader(http.StatusNotFound)

			return
		}

		err := json.NewEncoder(writer).Encode(body)
		if err != nil {
			t.Errorf("encoding fake CF response for %s: %v", req.URL.Path, err)
		}
	}))
	t.Cleanup(server.Close)

	return server
}

func serverCertPEM(t *testing.T, server *httptest.Server) string {
	t.Helper()

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))
}

func singleEndpointClient(t *testing.T, config cf.CFAPIConfig) *cf.EndpointClient {
	t.Helper()

	manager := cf.NewManager(map[string]cf.CFAPIConfig{testEndpointKey: config}, nil)

	clients := manager.GetClients()
	if len(clients) != 1 {
		t.Fatalf("expected one endpoint client, got %d", len(clients))
	}

	return clients[0]
}

func TestCFEndpointTLSTrust(t *testing.T) {
	t.Parallel()

	t.Run("self-signed endpoint is rejected without a CA", func(t *testing.T) {
		t.Parallel()

		server := newSelfSignedCFAPI(t)
		client := singleEndpointClient(t, cf.CFAPIConfig{
			Name:     "untrusted",
			Endpoint: server.URL,
			Username: testUsername,
			Password: testPassword,
		})

		if client.IsHealthy() {
			t.Fatal("expected connection to an untrusted endpoint to fail")
		}

		var unknownAuthority x509.UnknownAuthorityError
		if !errors.As(client.LastError(), &unknownAuthority) {
			t.Fatalf("expected an unknown authority error, got: %v", client.LastError())
		}
	})

	t.Run("self-signed endpoint is trusted with its CA", func(t *testing.T) {
		t.Parallel()

		server := newSelfSignedCFAPI(t)
		client := singleEndpointClient(t, cf.CFAPIConfig{
			Name:     "trusted",
			Endpoint: server.URL,
			Username: testUsername,
			Password: testPassword,
			CACert:   serverCertPEM(t, server),
		})

		if !client.IsHealthy() {
			t.Fatalf("expected connection with the endpoint CA to succeed, got: %v", client.LastError())
		}
	})

	t.Run("invalid CA bundle is reported", func(t *testing.T) {
		t.Parallel()

		server := newSelfSignedCFAPI(t)
		client := singleEndpointClient(t, cf.CFAPIConfig{
			Name:     "bad-ca",
			Endpoint: server.URL,
			Username: testUsername,
			Password: testPassword,
			CACert:   "not a certificate",
		})

		if client.IsHealthy() {
			t.Fatal("expected connection with an invalid CA bundle to fail")
		}
	})
}

// This test opens the library's process-wide development gate, so it must not
// run in parallel with tests that rely on the environment being untouched.
//
//nolint:paralleltest // mutates CAPI_DEV_MODE
func TestCFEndpointSkipSSLValidation(t *testing.T) {
	t.Setenv("CAPI_DEV_MODE", "")

	server := newSelfSignedCFAPI(t)
	client := singleEndpointClient(t, cf.CFAPIConfig{
		Name:              "insecure",
		Endpoint:          server.URL,
		Username:          testUsername,
		Password:          testPassword,
		SkipSSLValidation: true,
	})

	if !client.IsHealthy() {
		t.Fatalf("expected skip_ssl_validation to bypass verification, got: %v", client.LastError())
	}
}
